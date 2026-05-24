# Contributing to fslite

Thanks for considering a contribution. fslite is a small project; one or two well-scoped PRs are easier to land than a single sweeping one.

## Development setup

```sh
git clone https://github.com/danmestas/fslite
cd fslite
make build       # → ./fslite
make test        # in-process tests
```

The Go toolchain needs to be ≥1.26. The `libfossil` dependency is fetched normally via `go mod`.

The docker e2e harness (`make docker-e2e`) needs Docker installed and running. It cross-compiles the binary on the host, builds a distroless image, brings up NATS + two agent containers, and runs an opt-in test (`RUN_DOCKER_E2E=1`).

## Branching + commits

- Open a topic branch off `main`: `git checkout -b fix/whatever` or `feat/whatever`.
- One coherent change per commit. The pivot history in this repo is the style we're trying to maintain — each commit message names *what changed* in the subject and *why* in the body.
- Add tests when fixing bugs; the failing test should be its own commit before the fix, where practical.
- `make test` must be green before opening a PR.

## What's in scope

- WebDAV-spec compliance fixes against `golang.org/x/net/webdav` quirks.
- Cross-platform sentinel filters (ignore-glob defaults).
- Performance work on the manifest cache / overlay path.
- New `Config` knobs that make the dual-use (agent / userspace) split clearer.
- Documentation, especially examples and gotchas.

## What's out of scope (for now)

- Re-introducing kernel filesystem code (FUSE / FSKit). The project history records why this was torn down; the userspace model is the deliberate choice.
- Multi-master commit semantics beyond what Fossil natively supports. Conflicts are surfaced as `vfs.ErrMergeRequired`; resolution is the caller's policy.
- Iroh transport — interesting future addition; let's discuss in an issue first.

## Reporting bugs

Open an issue with:

- macOS / Linux / Windows version.
- Go version.
- Minimal reproduction (a small shell script or Go program).
- What you expected, what happened.

If the bug is a WebDAV-client quirk, run `fslite serve --verbose` and attach the request trace.

## License

By contributing you agree your work is licensed under the MIT License (see `LICENSE`).
