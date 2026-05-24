// Command fslite is the CLI for the Fossil-VFS daemon and its
// operational sidecars (mount/unmount/commit/status/stop). The default
// subcommand is `serve`, so containers can keep using bare `fslite` as
// their ENTRYPOINT and pick up config from environment variables.
//
//	fslite                       # serve (env-driven; what docker compose runs)
//	fslite serve                 # same; explicit form
//	fslite demo                  # spawn daemon + auto-mount at /Volumes/demo.localhost
//	fslite demo --repo path      # demo serving a specific fossil repo
//	fslite mount [--agent x]     # mount in Finder
//	fslite unmount               # unmount
//	fslite commit "msg"          # drain the overlay into a Fossil check-in
//	fslite ignore [--set/-reset] # manage the sentinel-file filter
//	fslite status [--agent x]    # print pid/url/repo of one daemon
//	fslite status --all          # table of every recorded daemon
//	fslite list                  # synonym for status --all
//	fslite stop [--agent x]      # SIGTERM the singleton (or a specific one)
//	fslite stop --all            # SIGTERM every alive daemon
package main

import (
	"github.com/alecthomas/kong"
)

type cli struct {
	Serve   serveCmd   `cmd:"" default:"withargs" help:"Run the WebDAV+NATS daemon (default; reads env vars)."`
	Demo    demoCmd    `cmd:"" help:"Spawn a daemon + auto-mount in Finder (macOS). Defaults to ~/.fslite/demo/repo.fossil; --repo to serve a specific fossil."`
	MCP     mcpCmd     `cmd:"" name:"mcp" help:"Run as a Model Context Protocol server over stdio — agents get filesystem ops without mounting."`
	Mount   mountCmd   `cmd:"" help:"Mount the running daemon's WebDAV URL in Finder (macOS)."`
	Unmount unmountCmd `cmd:"" help:"Unmount the daemon's volume (macOS)."`
	Commit  commitCmd  `cmd:"" help:"Drain the overlay into a Fossil check-in via the running daemon."`
	Ignore  ignoreCmd  `cmd:"" help:"Read or set the vfs-ignore-glob (sentinel-file filter)."`
	Status  statusCmd  `cmd:"" help:"Show running-daemon metadata. --all for a table of every daemon."`
	List    listCmd    `cmd:"" help:"Table of every recorded daemon (synonym for status --all)."`
	Stop    stopCmd    `cmd:"" help:"SIGTERM the running daemon. --all to stop every alive daemon."`
}

func main() {
	var c cli
	ctx := kong.Parse(&c,
		kong.Name("fslite"),
		kong.Description("Fossil-VFS: a userspace virtual filesystem over a Fossil repo, served as WebDAV."),
		kong.UsageOnError(),
	)
	ctx.FatalIfErrorf(ctx.Run())
}
