# fslite

> Single-file content-addressed filesystem for autonomous agents. [Project home →](https://github.com/danmestas/fslite)

A Fossil-backed worktree for autonomous agents. Each agent gets an isolated filesystem with full history, surfaced as an MCP server — no more stomping your git checkout.

## Install

```sh
npm install -g fslite
```

The postinstall step runs `go install` against `github.com/danmestas/fslite/cmd/fslite@latest`, so **Go 1.21+ is required** on your machine for now. Prebuilt binaries are planned for v0.2.

## Use with any MCP agent

Add to your agent's MCP config:

```json
{
  "mcpServers": {
    "fslite": {
      "command": "fslite",
      "args": ["mcp", "--repo", "~/agent-workspaces/mybox.fossil"]
    }
  }
}
```

The agent now has `list`, `read`, `write`, `stat`, `delete`, `rename`, `mkdir`, `commit`, `ignore_get`, `ignore_set` tools. Writes accumulate in an overlay; `commit("...")` drains them into a Fossil check-in.

## Inspect the workspace

```sh
fossil ui ~/agent-workspaces/mybox.fossil
```

Browses the timeline in a browser — every agent commit, full file history.

## License

MIT. See [LICENSE](./LICENSE) and the [main repo](https://github.com/danmestas/fslite) for source + contributing.
