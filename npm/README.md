# fslite

> Mount a Fossil repository as an ordinary folder. [Project home →](https://github.com/danmestas/fslite)

One `.fossil` file holds every version of every file. fslite serves it over WebDAV, so Finder, Windows Explorer or a Linux `davfs2` mount treats it like any other network folder — people who have never heard of version control open it, edit documents, and save, and every save lands as a real Fossil check-in.

## Install

```sh
npm install -g @agent-ops/fslite
```

(The CLI command is still `fslite`; npm scope is namespace only.)

The postinstall step runs `go install` against `github.com/danmestas/fslite/cmd/fslite@latest`, so **Go 1.21+ is required** on your machine for now. Prebuilt binaries are planned for v0.2.

## Use

```sh
fslite demo                                  # throwaway repo, mounted, cleaned up on exit
fslite open ~/Documents/contracts.fossil     # serve a real repo (created if missing)

fslite commit "Q3 contracts from legal"      # drain pending writes into one check-in
fslite unmount && fslite stop
```

On macOS the volume appears at `/Volumes/contracts.localhost`. On Linux, mount the same URL with `davfs2`. Pass `--auto-commit=10s` to commit automatically once writes go quiet.

Inspect the result with ordinary Fossil tooling:

```sh
fossil ui ~/Documents/contracts.fossil
```

## For AI agents

The same filesystem is exposed as an MCP server (`fslite mcp --repo <file>`), giving an agent a worktree with full history without touching your git checkout. See the [project README](https://github.com/danmestas/fslite#for-ai-agents).

## License

MIT. See [LICENSE](./LICENSE), the [changelog](https://github.com/danmestas/fslite/blob/main/CHANGELOG.md), and the [main repo](https://github.com/danmestas/fslite) for source + contributing.
