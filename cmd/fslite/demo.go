package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// demoCmd is a thin convenience wrapper over serve for local
// single-instance testing. It picks sensible defaults so you don't
// have to set env vars.
//
// `--repo` points at any fossil repo file (existing or not — missing
// files get bootstrapped with an initial commit). Defaults to
// `./demo-data/repo.fossil`. The legacy `--dir` flag is preserved as
// shorthand: `--dir foo` is equivalent to `--repo foo/repo.fossil`.
type demoCmd struct {
	Repo       string `name:"repo" help:"Fossil repo file to serve. Default: ./demo-data/repo.fossil (or <--dir>/repo.fossil)."`
	Dir        string `name:"dir" help:"Shorthand for --repo <dir>/repo.fossil."`
	HTTPAddr   string `name:"http" default:"127.0.0.1:8080" help:"WebDAV bind address."`
	AgentID    string `name:"agent" help:"Agent name. Default: derived from the repo filename."`
	RandomName bool   `name:"random-name" help:"Generate a random electric-hyena-style agent name."`
	Verbose    bool   `name:"verbose" help:"Log every incoming WebDAV request."`
}

func (d *demoCmd) Run() error {
	repo := d.Repo
	if repo == "" {
		dir := d.Dir
		if dir == "" {
			dir = "./demo-data"
		}
		repo = filepath.Join(dir, "repo.fossil")
	}
	abs, err := filepath.Abs(repo)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "fslite demo: repo   = %s\n", abs)
	fmt.Fprintf(os.Stderr, "fslite demo: WebDAV = http://%s\n", d.HTTPAddr)
	fmt.Fprintf(os.Stderr, "fslite demo: next:    fslite mount (in another terminal)\n\n")

	// Demo is local-only by design; even if NATS_URL leaks in from the
	// environment, the demo shouldn't try to autosync to peers.
	s := serveCmd{
		RepoPath:   abs,
		AgentID:    d.AgentID,
		RandomName: d.RandomName,
		HTTPAddr:   d.HTTPAddr,
		NoNATS:     true,
		Verbose:    d.Verbose,
	}
	return s.Run()
}
