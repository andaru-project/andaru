# Agent tooling: Graphify and CodeGraph

Two code-intelligence indexes back AI-assisted work on this repository:

- **Graphify** builds a knowledge graph of the code and documentation
  (`graphify-out/graph.json`), queried with `graphify query`, `graphify explain`
  and `graphify path`.
- **CodeGraph** builds a per-machine SQLite symbol index (`.codegraph/`) that
  agents query through its MCP server.

## Setup

Run once per clone:

```sh
sh scripts/setup-hooks.sh
```

This needs `uv`. It installs Graphify with its native Leiden clustering backend,
installs Graphify's rebuild hooks and graph merge driver into the clone, and
builds the CodeGraph index when `codegraph` is installed. For a new linked
worktree, run `sh scripts/agent/init-worktree-tools.sh <worktree>` (needs
`codegraph`) to build that worktree's own CodeGraph index and refresh its graph.

## Decisions

- **Leiden, not Louvain.** The install line is `graphifyy[leiden]>=0.9.60`. From
  0.9.60 the `leiden` extra installs `graspologic-native` on Python 3.13 and
  later; without it Graphify falls back to Louvain clustering, which can produce
  internally disconnected communities. `tests/shell/agent_graphify_merge_driver_spec.sh`
  pins the exact `uv` invocation.
- **Corpus scope.** The graph covers the README, the documentation, the
  development lab and the agent scripts and specs; `.graphifyignore` excludes tool
  state, assets, the license and VM seed data. The repository is mostly shell
  today, which Graphify extracts coarsely and CodeGraph does not index at all;
  both earn their keep as the control plane and documentation grow, so they are
  wired in from the start.
- **The graph is tracked.** `graphify-out/graph.json` is committed so a fresh
  clone can query it without a rebuild. Parallel updates merge through the
  `graphify` union merge driver (`.gitattributes`), which `setup-hooks.sh`
  registers in each clone. Graphify's post-commit hook refreshes the code layer
  after each commit in the primary checkout only; it skips linked worktrees,
  which refresh through `init-worktree-tools.sh`. Documentation changes need an
  AI-assisted `/graphify --update`.
- **No freshness gate yet.** Nothing enforces that the committed graph equals a
  rebuild of the tree. Revisit once there is CI to run the check in.
- **Hooks are not tracked.** Graphify pins each machine's interpreter path into
  its hooks, so they are installed into the clone's own hooks directory rather
  than committed.
