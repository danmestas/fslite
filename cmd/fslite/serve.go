package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/danmestas/fslite/engine"
	"github.com/danmestas/fslite/vfs"
)

// serveCmd runs the WebDAV+NATS daemon. Config comes from environment
// variables so containers can keep using bare `fslite` as their entrypoint.
// Flags are accepted as a convenience for interactive use; env vars win
// when both are set.
type serveCmd struct {
	RepoPath    string `name:"repo" env:"REPO_PATH" default:"/data/repo.fossil" help:"On-disk fossil repo path."`
	SeedRepo    string `name:"seed" env:"SEED_REPO" help:"Optional seed repo to copy from on first start."`
	AgentID     string `name:"agent" env:"AGENT_ID" help:"Agent identifier. Default: derived from --repo basename (myproject.fossil → myproject), deconflicted with -2/-3/... against other live daemons."`
	RandomName  bool   `name:"random-name" help:"Generate a random electric-hyena-style agent name instead of deriving from the repo path."`
	ProjectCode string `name:"project-code" env:"PROJECT_CODE" help:"Shared project code for autosync peers."`
	NATSURL     string `name:"nats" env:"NATS_URL" help:"NATS broker URL (enables autosync when set; ignored if --no-nats)."`
	HTTPAddr    string `name:"http" env:"HTTP_ADDR" default:"0.0.0.0:8080" help:"WebDAV bind address."`
	NoNATS      bool   `name:"no-nats" env:"FSLITE_NO_NATS" help:"Local-only mode. Skip all NATS wiring even if NATS_URL is set; cross-agent sync + locks are disabled."`
	AutoCommit  time.Duration `name:"auto-commit" env:"FSLITE_AUTO_COMMIT" help:"Debounced auto-commit window. e.g. 10s. Default 0 = manual commits only. Each overlay write resets the timer; when it elapses, the overlay drains into a new check-in with a generated message."`
	Verbose     bool   `name:"verbose" env:"FSLITE_VERBOSE" help:"Log every incoming WebDAV request (method, path, headers)."`
}

func (s *serveCmd) Run() error {
	if err := ensureRepo(s.RepoPath, s.SeedRepo, s.ProjectCode); err != nil {
		return fmt.Errorf("repo bootstrap: %w", err)
	}

	// Auto-name from the repo path when the user didn't specify; falls
	// back to a random electric-hyena name if --random-name or if the
	// repo path is too generic to derive from.
	agentID, err := resolveAgentName(s.AgentID, s.RandomName, s.RepoPath)
	if err != nil {
		return err
	}
	s.AgentID = agentID

	cfg := vfs.Config{
		RepoPath:     s.RepoPath,
		Version:      "tip",
		EnableWrites: true,
		User:         s.AgentID,
		AutoCommit:   s.AutoCommit,
	}

	mode := "local-only"
	var nc *nats.Conn
	switch {
	case s.NoNATS:
		cfg.NoNATS = true
	case s.NATSURL != "":
		var err error
		nc, err = nats.Connect(s.NATSURL,
			nats.MaxReconnects(-1),
			nats.ReconnectWait(time.Second),
		)
		if err != nil {
			return fmt.Errorf("nats connect %s: %w", s.NATSURL, err)
		}
		defer nc.Close()
		cfg.Sync = vfs.SyncConfig{
			NATS:        nc,
			ProjectCode: s.ProjectCode,
			AgentID:     s.AgentID,
		}
		mode = fmt.Sprintf("synced via %s", s.NATSURL)
	}

	v, err := vfs.New(cfg)
	if err != nil {
		return fmt.Errorf("vfs.New: %w", err)
	}
	defer v.Close()

	var dav http.Handler
	if s.Verbose {
		dav = v.WebDAVHandlerWithLogger(func(r *http.Request, err error) {
			log.Printf("webdav error: %s %s: %v", r.Method, r.URL.Path, err)
		})
	} else {
		dav = v.WebDAVHandler()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/_admin/commit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		msg, _ := io.ReadAll(r.Body)
		message := string(msg)
		if message == "" {
			message = fmt.Sprintf("webdav commit by %s", s.AgentID)
		}
		ck, err := v.Commit(message)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, vfs.ErrNothingToCommit) {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		fmt.Fprintf(w, "rid=%d uuid=%s\n", ck.RID, ck.UUID)
	})
	mux.HandleFunc("/_admin/ignore", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			source, glob := v.IgnoreGlob()
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprintf(w, "%s\t%s\n", source, glob)
		case http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			glob := strings.TrimRight(string(body), "\n")
			if err := v.SetIgnoreGlob(glob); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			source, current := v.IgnoreGlob()
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprintf(w, "source: %s\nglob:   %s\n", source, current)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	if s.Verbose {
		mux.Handle("/", &requestLogger{inner: dav})
	} else {
		mux.Handle("/", dav)
	}

	srv := &http.Server{
		Addr:              s.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Only record NATS/project in the state file when sync is actually
	// wired. Without this guard, --no-nats would still show "synced
	// mode" in `fslite status` because NATS_URL leaked through from the
	// environment.
	stateNATS, stateProject := "", ""
	if nc != nil {
		stateNATS = s.NATSURL
		stateProject = s.ProjectCode
	}
	state := daemonState{
		PID:         os.Getpid(),
		HTTPAddr:    s.HTTPAddr,
		URL:         daemonURL(s.HTTPAddr),
		RepoPath:    s.RepoPath,
		AgentID:     s.AgentID,
		ProjectCode: stateProject,
		NATSURL:     stateNATS,
		StartedAt:   time.Now(),
	}
	if err := writeState(state); err != nil {
		log.Printf("fslite: warning: could not write state file: %v", err)
	}
	defer removeState(s.AgentID)

	go func() {
		log.Printf("fslite: agent=%s mode=%s url=http://%s", s.AgentID, mode, s.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("fslite: shutdown signal received")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	return nil
}

// ensureRepo copies seedPath → repoPath if repoPath is missing. If both
// are missing, creates a fresh repo at repoPath with the given
// projectCode and writes an initial commit so VFS.New can resolve "tip".
func ensureRepo(repoPath, seedPath, projectCode string) error {
	if _, err := os.Stat(repoPath); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		return err
	}
	if seedPath != "" {
		if _, err := os.Stat(seedPath); err == nil {
			return copyFile(seedPath, repoPath)
		}
	}
	log.Printf("fslite: bootstrapping fresh repo at %s", repoPath)
	eng, err := engine.Create(repoPath)
	if err != nil {
		return fmt.Errorf("engine.Create: %w", err)
	}
	defer eng.Close()
	_, _, err = eng.CommitFiles(
		[]engine.CommitFile{{Name: ".fslite-init", Content: []byte("fslite initial commit\n")}},
		"initial commit", "fslite", 0, false,
	)
	if err != nil {
		return fmt.Errorf("initial commit: %w", err)
	}
	return nil
}

func copyFile(src, dst string) error {
	sf, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sf.Close()
	df, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer df.Close()
	if _, err := io.Copy(df, sf); err != nil {
		return err
	}
	log.Printf("fslite: seeded %s from %s", dst, src)
	return nil
}

// daemonURL constructs a loopback-friendly URL from a listen address.
// "0.0.0.0:8080" becomes "http://127.0.0.1:8080" so clients running on
// the same host can hit it without parsing.
func daemonURL(addr string) string {
	host, port := splitHostPort(addr)
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%s", host, port)
}

// requestLogger wraps an http.Handler and logs each incoming request's
// method, path, and header set. Useful for diagnosing what an opaque
// client (kernel WebDAV, Office, etc.) actually sends.
type requestLogger struct{ inner http.Handler }

func (rl *requestLogger) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Printf("--> %s %s", r.Method, r.URL.Path)
	for k, vs := range r.Header {
		for _, v := range vs {
			log.Printf("    %s: %s", k, v)
		}
	}
	rl.inner.ServeHTTP(w, r)
}

func splitHostPort(addr string) (string, string) {
	// minimal split — addresses are always host:port in our config
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i], addr[i+1:]
		}
	}
	return "", addr
}
