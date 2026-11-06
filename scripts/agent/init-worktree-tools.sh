#!/bin/sh
# Initialize repository-intelligence tools for one exact worktree root.

usage() {
	echo "usage: init-worktree-tools.sh [WORKTREE]" >&2
	exit 2
}

main() {
	# shellcheck source=scripts/agent/agent_env.sh
	. "$(dirname "$0")/agent_env.sh"
	# shellcheck source=scripts/agent/resolve-graphify.sh
	. "$(dirname "$0")/resolve-graphify.sh"
	scrub_git_env
	[ "$#" -le 1 ] || usage
	require_tool git
	require_tool codegraph

	target=${1:-.}
	root=$(git -C "$target" rev-parse --show-toplevel 2>/dev/null) || {
		echo "init-worktree-tools.sh: '$target' is not a git worktree" >&2
		exit 2
	}
	root=$(CDPATH='' cd "$root" && pwd -P) || exit 2

	sh "$(dirname "$0")/ensure-codegraph.sh" "$root" || exit $?
	# Refreshing an existing root graph is mechanical, so it stays automated. Building
	# the FIRST graph is not: its scope (which trees, whether the semantic layer earns
	# its cost, what .graphifyignore allows) is a judgement call, so defer it to an
	# AI-assisted `/graphify` run rather than picking a default unattended.
	if [ -f "$root/graphify-out/graph.json" ]; then
		graphify_bin=$(resolve_graphify_launcher) || exit 1
		"$graphify_bin" update "$root" || exit $?
	else
		echo "No Graphify root graph in $root; run /graphify in your AI assistant to build one." >&2
	fi
}

main "$@"
