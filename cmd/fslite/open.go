package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// openCmd serves a specific fossil repo, mounts it in Finder on macOS,
// and blocks on Ctrl-C. The repo persists; only the mount + daemon
// process are torn down on exit. If the repo file doesn't exist, it's
// bootstrapped with an initial commit so first-run works.
type openCmd struct {
	Repo       string `arg:"" name:"repo" help:"Fossil repo file to open (created if missing)."`
	HTTPAddr   string `name:"http" help:"WebDAV bind address. Default: 127.0.0.1:<auto-picked>."`
	AgentID    string `name:"agent" help:"Agent name. Default: derived from the repo filename."`
	RandomName bool   `name:"random-name" help:"Generate a random electric-hyena-style agent name."`
	NoMount    bool   `name:"no-mount" help:"Don't auto-mount in Finder (macOS); just run the daemon."`
	Verbose    bool   `name:"verbose" help:"Log every incoming WebDAV request."`
}

func (o *openCmd) Run() error {
	abs, err := filepath.Abs(o.Repo)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "fslite open: repo = %s\n\n", abs)
	return runServeAndMount(runMountArgs{
		Repo:       abs,
		HTTPAddr:   o.HTTPAddr,
		AgentID:    o.AgentID,
		RandomName: o.RandomName,
		NoMount:    o.NoMount,
		Verbose:    o.Verbose,
	})
}
