# Changelog

## [Unreleased]

### Changed

- **Install with a script, not npm.** `install.sh` (macOS and Linux) and
  `install.ps1` (Windows) download the release archive for the machine,
  check it against the release's `SHA256SUMS`, and install the binary.
  The npm package built fslite from source and needed Go; it is no longer
  published, and 0.1.7 is its last version.

## [0.1.7] — 2026-10-31

A directory listing no longer decompresses every file in it, and an
editor's ordinary save no longer costs the whole tree at commit.

### Changed

- **Stat reads a size, not a file.** `Stat` and `ReadDir` reported a
  file's size by reading its entire content and measuring it, once per
  path per check-in. Finder and davfs2 list a directory before almost
  every operation, so a listing expanded every delta chain in the
  directory. The size now comes from the repository's own `blob.size`,
  which Fossil records for every artifact when it stores it, through a
  single indexed lookup by hash. A hash the store does not hold falls
  back to a read.

- **A deletion commit names unchanged files by hash.** A commit that
  dropped a file spelled the manifest out in full and re-read every
  unchanged file's content to do it. Unchanged files are now carried by
  the hash the parent already holds (`FileToCommit.UUID`, new in
  go-libfossil), so a delete reads nothing it does not change.

- **A temp-file tombstone is not a deletion.** Every editor's atomic save
  writes a temp file and renames it over the target, which left a
  tombstone for a path no check-in ever held. That tombstone pushed every
  such commit onto the full-manifest path. Tombstones now count as
  deletions only when they name a file the parent tracks; a commit left
  with nothing but stale tombstones clears them and reports
  `ErrNothingToCommit`.

- **Content and sizes are cached by hash.** Expanded file content lives in
  a byte-bounded LRU keyed by artifact hash (`Config.CacheBytes`, default
  64 MiB, negative disables), and sizes are memoized the same way. Neither
  is invalidated by a commit, a pull or a rename, because a hash names its
  bytes forever; the old path-keyed size cache was wiped on each of those.

- **Files are read by hash.** A committed file is read by its own artifact
  hash (go-libfossil's `Repo.ReadBlob`), not by path at the current
  check-in, so a read skips the manifest lookup, and a read that raced a
  commit or pull returns the file it looked up rather than the new tip's.

- **go-libfossil v0.17.0**, up from v0.16.1. v0.16.2 added
  `FileToCommit.UUID` and `Repo.ReadBlob`; v0.17.0 brings horizon and
  sync fixes and smaller WebAssembly builds. fslite sets no horizon seed
  key, so v0.17.0's `sshverify` opt-in needs nothing here.

## [0.1.6] — 2026-10-25

A dependency release: fslite now builds on go-libfossil v0.16.1. No
behaviour of the mount or the MCP server changes.

### Changed

- **go-libfossil v0.16.1.** Up from v0.9.0. This picks up seven releases
  of sync and repository fixes, including delta-chain bounds that keep a
  malformed push from holding the write lock. See go-libfossil's
  changelog for the full list.

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
