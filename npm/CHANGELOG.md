# Changelog

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
