# Changelog

## [0.1.3] — 2026-05-24

### Changed

- **`fslite demo` is now self-contained.** Drops the `--repo` flag,
  creates a fresh `mkdtemp` repo, seeds it with example files
  (`README.md`, `notes/scratch.md`, `src/hello.go`), mounts at
  `/Volumes/demo.localhost`, and **deletes the repo on Ctrl-C**.

### Added

- **`fslite open <repo>`** — serves any fossil file + auto-mounts in
  Finder + blocks on Ctrl-C. The repo persists; only the daemon and
  mount are torn down. Bootstraps the file if missing. This is the
  command that took the `--repo` role demo used to confuse.

## [0.1.2] — 2026-05-24

### Changed

- `fslite demo` is now a one-command experience: spawns the daemon,
  auto-mounts at `/Volumes/demo.localhost` on macOS, opens the
  mountpoint in Finder, and blocks on Ctrl-C. On signal it unmounts
  + shuts the child down. `--no-mount` for headless / non-macOS.
- Demo repo defaults to `~/.fslite/demo/repo.fossil` (stable across
  cwds) instead of `./demo-data/repo.fossil` (which depended on
  wherever you ran `fslite demo` from). Override with `--repo`.
- Demo port defaults to a free auto-picked port (was hardcoded 8080;
  failed if anything else was on it). Override with `--http`.
- Daemon startup log is now one consolidated line:
  `fslite: agent=X mode=Y url=Z`. Dropped the duplicate
  `agent=X repo=Y` and the `--no-nats set` line.
- Bootstrap log only fires the first time a repo is created
  (was a weird `no seed at ""` message every run).

## [0.1.1] — 2026-05-24

### Fixed

- `npm install -g @agent-ops/fslite` postinstall failed in v0.1.0 because
  the Go module had `replace` directives pointing at local libfossil
  checkouts (`/Users/.../libfossil`). `go install` refuses modules with
  replace directives. Removed the replaces; pinned to public libfossil
  v0.6.3 + driver v0.2.0 from GitHub tags. End-to-end install now
  works.

## [0.1.0] — 2026-05-24

Initial release.

### Added

- MCP server (`fslite mcp`) exposing filesystem tools — `list`, `read`,
  `write`, `stat`, `delete`, `rename`, `mkdir`, `commit`, `ignore_get`,
  `ignore_set` — backed by a Fossil repo. Speak it over stdio from any
  Model Context Protocol-compatible agent runtime.
- WebDAV server (`fslite serve`) with macOS-quirk middleware so atomic
  saves through the kernel mount roundtrip cleanly.
- Optional NATS-mediated autosync between peer daemons sharing a
  project code. Optional cross-agent WebDAV locks via JetStream KV.
- Multi-daemon support: auto-derived agent names from the repo
  filename, deconflicted with `-2`/`-3` suffixes; `--random-name` for
  electric-hyena-style identifiers; `--agent <id>` selector on every
  sibling subcommand; `fslite list` + `fslite stop --all`.
- Go library import (`github.com/danmestas/fslite/vfs`) — `*VFS`
  implements `io/fs.FS` / `fs.ReadDirFS` / `fs.StatFS` with an
  optional writable overlay surface.
- Cross-platform sentinel filter (`vfs-ignore-glob` in the fossil
  config table) — defaults cover macOS, Windows, Linux, and editor
  swap/backup patterns.
- WASM cross-compile for `wasip1` and `js` targets.
- Cross-container Docker e2e harness covering sync, locks, restart
  resilience.
