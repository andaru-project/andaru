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
points `core.hooksPath` at the tracked `.githooks/`, registers the graph merge
driver, removes any rebuild hooks `graphify hook install` left behind, and
builds the CodeGraph index when `codegraph` is installed. For a new linked
worktree, run `sh scripts/agent/init-worktree-tools.sh <worktree>` (needs
`codegraph`) to build that worktree's own CodeGraph index and warm its graph cache.

## Graph freshness

The committed `graphify-out/graph.json` always equals
`PYTHONHASHSEED=0 graphify update .` run on the commit's own tree.
`scripts/agent/check-graph-fresh.sh` checks that (or, with `--refresh`, rebuilds
in place), and two tracked hooks enforce it:

- **pre-commit** rebuilds and stages the graph whenever anything else is staged.
- **pre-push** checks out each pushed tip into a scratch worktree under
  `../.andaru_worktrees/.graph-check-<sha7>` and refuses the push if its graph is
  stale. A rebase replays commits without the pre-commit rebuild, and a clean
  merge commits the merge driver's union rather than a rebuild, so after either
  run `sh scripts/agent/check-graph-fresh.sh --refresh` and commit the graph.

`graphify update` re-extracts code only; it keeps the documentation nodes already
in the graph. After a documentation change, run an AI-assisted `/graphify --update`
to refresh them, then commit.

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
  `graphify` union merge driver (`.gitattributes`), registered by
  `setup-hooks.sh` with two `git config` writes. The driver only keeps conflict
  markers out of the graph; the hooks rebuild it.
- **The hooks are tracked; Graphify's own are not used.** `graphify hook install`
  writes post-commit and post-checkout rebuild hooks that run in the background,
  skip linked worktrees, and pin each machine's interpreter path. The tracked
  pre-commit and pre-push hooks replace them and run in every worktree.
- **No CI gate yet.** The hooks are the only enforcement; add the same
  `check-graph-fresh.sh` run to CI once CI exists.
