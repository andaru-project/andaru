#!/bin/sh
# One-time developer setup: bootstrap the CodeGraph index.
#
# Run once after cloning:
#   sh scripts/setup-hooks.sh

set -eu

script_dir=$(CDPATH='' cd "$(dirname "$0")" && pwd -P)
root=$(git -C "$script_dir" rev-parse --show-toplevel)

if command -v codegraph >/dev/null 2>&1; then
	sh "$script_dir/agent/ensure-codegraph.sh" "$root"
else
	echo 'codegraph not found; skipping the CodeGraph index' >&2
fi
