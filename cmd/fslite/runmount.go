package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"
)

// runServeAndMount spawns `fslite serve` as a child process, waits for
// /healthz, optionally mounts at /Volumes/<volumeName>.localhost on
// macOS + opens Finder, then blocks until SIGINT/SIGTERM and tears
// everything down cleanly. Shared by `demo` and `open`.
//
// If volumeName is empty, the agent name auto-derived by serve (read
// from the state file after startup) is used.
type runMountArgs struct {
	Repo       string // absolute path
	HTTPAddr   string // "host:port"; "" → auto-pick a free port
	AgentID    string // explicit name; "" → serve auto-derives
	RandomName bool
	NoMount    bool
	VolumeName string // empty → use the agent name
	AutoCommit time.Duration
	Verbose    bool
	OnExit     func() // optional callback fired after shutdown (e.g. cleanup of seed dir)
}

func runServeAndMount(args runMountArgs) error {
	addr := args.HTTPAddr
	if addr == "" {
		port, err := pickFreePort()
		if err != nil {
			return fmt.Errorf("pick port: %w", err)
		}
		addr = fmt.Sprintf("127.0.0.1:%d", port)
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}
	serveArgs := []string{"serve", "--repo", args.Repo, "--http", addr, "--no-nats"}
	switch {
	case args.AgentID != "":
		serveArgs = append(serveArgs, "--agent", args.AgentID)
	case args.RandomName:
		serveArgs = append(serveArgs, "--random-name")
	}
	if args.AutoCommit > 0 {
		serveArgs = append(serveArgs, "--auto-commit", args.AutoCommit.String())
	}
	if args.Verbose {
		serveArgs = append(serveArgs, "--verbose")
	}

	serve := exec.Command(self, serveArgs...)
	serve.Stdout = os.Stderr
	serve.Stderr = os.Stderr
	if err := serve.Start(); err != nil {
		return fmt.Errorf("spawn serve: %w", err)
	}

	daemonURL := "http://" + addr
	if err := waitHealthz(daemonURL, 10*time.Second); err != nil {
		_ = serve.Process.Kill()
		return fmt.Errorf("daemon didn't become healthy: %w", err)
	}

	mountpoint := ""
	if !args.NoMount && runtime.GOOS == "darwin" {
		vol := args.VolumeName
		if vol == "" {
			if s, err := readState(); err == nil {
				vol = s.AgentID
			} else {
				vol = "fslite"
			}
		}
		mp, err := mountWebDAV(daemonURL, vol)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fslite: mount failed (%v); daemon still running\n", err)
		} else {
			mountpoint = mp
			fmt.Fprintf(os.Stderr, "fslite: mounted at %s\n", mp)
			_ = exec.Command("open", mp).Start()
		}
	}

	fmt.Fprintln(os.Stderr, "fslite: Ctrl-C to stop")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	fmt.Fprintln(os.Stderr, "\nfslite: shutting down")
	if mountpoint != "" {
		_ = unmountWebDAV(mountpoint)
	}
	_ = serve.Process.Signal(syscall.SIGTERM)
	_ = serve.Wait()

	if args.OnExit != nil {
		args.OnExit()
	}
	return nil
}

// pickFreePort opens a listener on :0 to ask the kernel for a free
// port, closes it, and returns the number. Race-window is tiny.
func pickFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port, nil
}

// waitHealthz polls daemonURL/healthz until 200 or timeout.
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
