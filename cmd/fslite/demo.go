package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/danmestas/fslite/engine"
)

// demoCmd is a self-contained showcase: creates a fresh tmp repo,
// seeds it with example files, mounts it in Finder (macOS), and on
// Ctrl-C unmounts + deletes the repo entirely. No flags for picking
// the repo path — for that, use `fslite open <repo>`.
type demoCmd struct {
	HTTPAddr   string        `name:"http" help:"WebDAV bind address. Default: 127.0.0.1:<auto-picked>."`
	NoMount    bool          `name:"no-mount" help:"Don't auto-mount in Finder (macOS); just run the daemon."`
	AutoCommit time.Duration `name:"auto-commit" help:"Debounced auto-commit window. e.g. 10s. Default 0 = manual commits only."`
	Verbose    bool          `name:"verbose" help:"Log every incoming WebDAV request."`
}

func (d *demoCmd) Run() error {
	tmp, err := os.MkdirTemp("", "fslite-demo-")
	if err != nil {
		return fmt.Errorf("create tmp dir: %w", err)
	}
	repo := filepath.Join(tmp, "demo.fossil")

	if err := seedDemoRepo(repo); err != nil {
		os.RemoveAll(tmp)
		return fmt.Errorf("seed demo repo: %w", err)
	}

	fmt.Fprintf(os.Stderr, "fslite demo: ephemeral repo at %s\n\n", repo)

	return runServeAndMount(runMountArgs{
		Repo:       repo,
		HTTPAddr:   d.HTTPAddr,
		AgentID:    "demo",
		VolumeName: "demo",
		AutoCommit: d.AutoCommit,
		NoMount:    d.NoMount,
		Verbose:    d.Verbose,
		OnExit: func() {
			fmt.Fprintf(os.Stderr, "fslite demo: cleaning up %s\n", tmp)
			os.RemoveAll(tmp)
		},
	})
}

// seedDemoRepo creates a brand-new Fossil repo with a small example
// tree so the user has something to poke at when the mount appears.
func seedDemoRepo(repoPath string) error {
	eng, err := engine.Create(repoPath)
	if err != nil {
		return err
	}
	defer eng.Close()
	_, _, err = eng.CommitFiles([]engine.CommitFile{
		{Name: "README.md", Content: []byte(demoSeedReadme)},
		{Name: "notes/scratch.md", Content: []byte(demoSeedScratch)},
		{Name: "src/hello.go", Content: []byte(demoSeedGo)},
	}, "demo seed", "fslite-demo", 0, false)
	return err
}

const demoSeedReadme = `# fslite demo

This is an ephemeral Fossil-backed worktree. Edit anything you like —
the changes accumulate in memory until you commit them.

Try:

- Open this README in your editor and change it.
- Drag files into the volume in Finder.
- Create new files / dirs.
- Delete a file.

When you're ready to record your edits as a check-in:

    fslite commit "what I changed"

When you press Ctrl-C in the terminal running this demo, the daemon
shuts down, the volume unmounts, and the entire repo is deleted —
nothing left behind.

For a persistent workspace use:

    fslite open ~/path/to/your.fossil
`

const demoSeedScratch = `# Scratch

Edit me. Or don't.
`

const demoSeedGo = `package main

import "fmt"

func main() {
	fmt.Println("hello from the fslite demo")
}
`
