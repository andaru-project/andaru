#!/bin/sh
# One-time developer setup: install Graphify with its Leiden backend, install its
# rebuild hooks and merge driver into this clone, and bootstrap CodeGraph.
#
# Run once after cloning:
#   sh scripts/setup-hooks.sh
#
# The hooks live in the clone's own hooks directory, not in the tree: Graphify pins
# each machine's interpreter path into them, so tracked copies would churn.

set -eu

script_dir=$(CDPATH='' cd "$(dirname "$0")" && pwd -P)
root=$(git -C "$script_dir" rev-parse --show-toplevel)
sh "$script_dir/agent/ensure-graphify-merge-driver.sh" "$root"

if command -v codegraph >/dev/null 2>&1; then
	sh "$script_dir/agent/ensure-codegraph.sh" "$root"
else
	echo 'codegraph not found; skipping the CodeGraph index' >&2
fi
