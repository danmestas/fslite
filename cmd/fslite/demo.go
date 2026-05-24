package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// demoCmd is the one-shot "show me how this works" command. It spawns
// `fslite serve` as a child process, waits for the daemon to come up,
// auto-mounts it (on macOS), opens the mountpoint in Finder, and blocks
// on SIGINT. On signal it unmounts and shuts the child down cleanly.
//
// Defaults to a stable repo path at ~/.fslite/demo/repo.fossil so the
// demo works from any cwd and persists between runs. Override with
// --repo to serve any existing fossil.
type demoCmd struct {
	Repo       string `name:"repo" help:"Fossil repo file to serve. Default: ~/.fslite/demo/repo.fossil."`
	HTTPAddr   string `name:"http" help:"WebDAV bind address. Default: 127.0.0.1:<auto-picked free port>."`
	AgentID    string `name:"agent" help:"Agent name. Default: derived from the repo filename."`
	RandomName bool   `name:"random-name" help:"Generate a random electric-hyena-style agent name."`
	NoMount    bool   `name:"no-mount" help:"Don't auto-mount in Finder (macOS); just run the daemon."`
	Verbose    bool   `name:"verbose" help:"Log every incoming WebDAV request."`
}

func (d *demoCmd) Run() error {
	repo, err := resolveDemoRepoPath(d.Repo)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(repo), 0o755); err != nil {
		return err
	}

	addr := d.HTTPAddr
	if addr == "" {
		port, err := pickFreePort()
		if err != nil {
			return fmt.Errorf("pick port: %w", err)
		}
		addr = fmt.Sprintf("127.0.0.1:%d", port)
	}

	fmt.Fprintf(os.Stderr, "fslite demo: repo  = %s\n", repo)
	fmt.Fprintf(os.Stderr, "fslite demo: addr  = http://%s\n\n", addr)

	// Spawn `fslite serve` as a child. Easier than refactoring serve's
	// blocking signal handler — we just orchestrate around it.
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}
	args := []string{"serve", "--repo", repo, "--http", addr, "--no-nats"}
	switch {
	case d.AgentID != "":
		args = append(args, "--agent", d.AgentID)
	case d.RandomName:
		args = append(args, "--random-name")
	default:
		// Demo uses a stable agent name so /Volumes/demo.localhost is
		// predictable between runs.
		args = append(args, "--agent", "demo")
	}
	if d.Verbose {
		args = append(args, "--verbose")
	}
	serve := exec.Command(self, args...)
	serve.Stdout = os.Stderr
	serve.Stderr = os.Stderr
	if err := serve.Start(); err != nil {
		return fmt.Errorf("spawn serve: %w", err)
	}

	// Wait for /healthz.
	daemonURL := "http://" + addr
	if err := waitHealthz(daemonURL, 10*time.Second); err != nil {
		_ = serve.Process.Kill()
		return fmt.Errorf("daemon didn't become healthy: %w", err)
	}

	// Auto-mount (macOS only).
	mountpoint := ""
	if !d.NoMount && runtime.GOOS == "darwin" {
		volumeName := d.AgentID
		if volumeName == "" {
			if s, err := readState(); err == nil {
				volumeName = s.AgentID
			} else {
				volumeName = "fslite"
			}
		}
		mp, err := mountWebDAV(daemonURL, volumeName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fslite demo: mount failed (%v); daemon still running\n", err)
		} else {
			mountpoint = mp
			fmt.Fprintf(os.Stderr, "fslite demo: mounted at %s\n", mp)
			_ = exec.Command("open", mp).Start()
		}
	}

	fmt.Fprintln(os.Stderr, "fslite demo: Ctrl-C to stop")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	fmt.Fprintln(os.Stderr, "\nfslite demo: shutting down")
	if mountpoint != "" {
		_ = unmountWebDAV(mountpoint)
	}
	_ = serve.Process.Signal(syscall.SIGTERM)
	_ = serve.Wait()
	return nil
}

// resolveDemoRepoPath returns the absolute path the demo should use.
// Explicit --repo wins; otherwise default to ~/.fslite/demo/repo.fossil
// so the demo works regardless of cwd.
func resolveDemoRepoPath(repo string) (string, error) {
	if repo != "" {
		return filepath.Abs(repo)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".fslite", "demo", "repo.fossil"), nil
}

// pickFreePort opens a listener on :0 to ask the kernel for a free
// port, closes it, and returns the number. Race-window is tiny in
// practice.
func pickFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port, nil
}

// waitHealthz polls daemonURL/healthz until it returns 200 or the
// timeout elapses.
func waitHealthz(daemonURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for time.Now().Before(deadline) {
		r, err := client.Get(daemonURL + "/healthz")
		if err == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("timeout after %s waiting for %s/healthz", timeout, daemonURL)
}
