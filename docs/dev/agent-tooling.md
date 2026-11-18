# Agent tooling: CodeGraph

CodeGraph builds a per-machine SQLite symbol index (`.codegraph/`) that agents
query through its MCP server.

## Setup

Run once per clone:

```sh
sh scripts/setup-hooks.sh
```

This builds the CodeGraph index when `codegraph` is installed. Each linked
worktree needs its own CodeGraph index: run `sh scripts/agent/ensure-codegraph.sh
<worktree>`.
