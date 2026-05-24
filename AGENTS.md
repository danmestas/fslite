# AGENTS.md — orientation for autonomous agents working in this repo

A userspace virtual filesystem over a Fossil repo. See `README.md` for the
user-facing intro and `docs/design.md` for the full design.

## What an agent should know

- **Two deployment shapes.** Single-instance userspace (one daemon, no
  NATS) or multi-agent (NATS broker + JetStream KV + cross-host
  autosync). The shape is selected by `Config.NoNATS` or the
  `--no-nats` / `FSLITE_NO_NATS=1` flag.
- **Three packages.** `engine/` is the libfossil bridge. `vfs/`
  implements `io/fs.FS`, the writable overlay, WebDAV adapter, NATS
  sync, and the cross-agent LockSystem. `cmd/fslite/` is the kong-
  driven CLI.
- **Driver selection is build-tag gated.** Native targets use
  `modernc.org/sqlite`; WASM targets (`wasip1`, `js`) use the ncruces
  driver. See `engine/driver_*.go`.
- **macOS quirk middleware.** `vfs/webdav.go` wraps
  `golang.org/x/net/webdav.Handler` to patch a MOVE-Overwrite default
  bug. New WebDAV-spec deviations belong in the same middleware.
- **Ignore-glob lives in the fossil config table** as
  `vfs-ignore-glob`. It travels with the repo on sync. CLI surface is
  `fslite ignore [--set | --reset]`.
- **Tests are e2e where they matter.** `vfs/` exercises the full
  read/write/commit path including `fstest.TestFS` conformance. The
  WebDAV adapter is tested via `httptest`. NATS sync uses an embedded
  test server. Cross-container behaviour is in `docker/e2e_test.go`
  (opt-in via `RUN_DOCKER_E2E=1`, run via `make docker-e2e`).

## Common tasks

```sh
make build           # ./fslite
make test            # in-process tests (engine + vfs)
make docker-e2e      # cross-container behaviour
make wasm-build      # GOOS=wasip1 compile check
make wasm-build-js   # GOOS=js compile check

./fslite demo &      # single-instance demo daemon
./fslite mount       # macOS Finder mount via osascript
./fslite commit "..." # drain overlay → new check-in
./fslite stop
```

## Things to avoid

- Don't reintroduce FUSE / FSKit / kernel filesystem code. The userspace
  model is the deliberate choice.
- Don't add consumer-side workarounds for upstream bugs in libfossil —
  file there instead and wait. Third-party bugs (e.g.
  `golang.org/x/net/webdav`) can have local middleware workarounds.
- Don't store macOS-specific sentinel patterns hardcoded in Go — they
  live in `DefaultIgnoreGlob` and the fossil config table.
- Don't commit the `demo-data/` directory or any `.fossil` file (in
  `.gitignore`).
