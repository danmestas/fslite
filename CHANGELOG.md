# Changelog

## [0.1.5] — 2026-08-07

Mounting a repo and saving a file from a Linux editor now works. It
didn't before, and the failure was quiet enough to be worse than a
crash: the save was lost and the editor's leftover temp file was
committed in its place.

### Fixed

- **Atomic saves over a Linux `davfs2` mount.** A MOVE that carried a
  lock token for the source only was refused with 412, which surfaces
  as `Input/output error` at the mount. davfs2 produces exactly that
  exchange on every editor save: it locks the temp file it just wrote,
  then renames it over the target, correctly sending no token for the
  unlocked destination. RFC 4918 only requires a token for resources
  that are actually locked; the in-memory lock system demanded one for
  both. The NATS-backed lock system already had the permissive rule, so
  behaviour no longer depends on whether sync happens to be enabled.
  Destinations genuinely held by another client are still refused.

- **Files created but never written were dropped.** `OpenFile` with
  `O_CREATE` followed by `Close` with no intervening `Write` left
  nothing behind, because only a write marked the handle dirty. LOCK on
  an unmapped URL is specified to create an empty locked resource
  (RFC 4918 §9.10.4) and is implemented exactly that way, so the lock
  reported success while the resource never existed. The same bug
  swallowed `: > file` truncations.

- **Check-ins now follow the repository's `hash-policy`** (via
  go-libfossil v0.9.0). Artifacts were always written with SHA1, so the
  first commit into a modern SHA3-256 repository rewrote every file's
  manifest entry and `fossil diff` reported the whole tree as changed
  after a one-file edit. Repository integrity was never affected.

- **README corrections.** `fslite demo` has had no `--repo` flag since
  it split from `fslite open`, and the overlay was described as
  offering "cheap rollback (just don't commit)" — it is durable state
  inside the `.fossil` file, so not committing discards nothing.

### Changed

- The libfossil dependency moved to `github.com/danmestas/go-libfossil`
  (upstream module rename) and was upgraded to v0.9.0.

### Added

- CI now covers Windows (unit suite plus a WebDAV protocol run against
  a live daemon) and mounts the daemon with `davfs2` on a native x86-64
  runner, driving it the way an editor does — so the bugs above stay
  fixed.

## [0.1.4] — 2026-05-24

### Added

- **Opt-in auto-commit.** `--auto-commit DURATION` (or
  `FSLITE_AUTO_COMMIT`) on `serve`/`demo`/`open`/`mcp` enables a
  debounced auto-commit: each overlay write resets a timer, and when
  the timer elapses the overlay drains into a Fossil check-in with
  a generated message (`auto: 3 files in src/`). Default off (manual
  `fslite commit` still required without the flag); suggested value
  when enabling: `10s`.

  Notably **not added**: commit-on-close. The overlay is durable
  SQLite state inside the `.fossil` file — killing the daemon does
  not lose writes. Reopening picks up pending writes intact. Session
  boundary is therefore intentionally separate from commit boundary.

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
