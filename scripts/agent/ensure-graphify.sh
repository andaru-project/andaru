#!/bin/sh
# Install or upgrade Graphify with its native Leiden clustering backend.
# Usage: ensure-graphify.sh [REPOSITORY]

set -eu

usage() {
	echo "usage: ensure-graphify.sh [REPOSITORY]" >&2
	exit 2
}

fail() {
	echo "ensure-graphify.sh: $*" >&2
	exit 1
}

main() {
	# shellcheck source=scripts/agent/agent_env.sh
	. "$(dirname "$0")/agent_env.sh"
	# shellcheck source=scripts/agent/resolve-graphify.sh
	. "$(dirname "$0")/resolve-graphify.sh"
	scrub_git_env
	[ "$#" -le 1 ] || usage
	require_tool git
	require_tool uv

	target=${1:-.}
	git -C "$target" rev-parse --show-toplevel >/dev/null 2>&1 || {
		echo "ensure-graphify.sh: '$target' is not a git worktree" >&2
		exit 2
	}

	# The leiden extra installs graspologic-native on Python 3.13+ from 0.9.60 on.
	# Without it clustering silently falls back to Louvain, which can emit
	# internally disconnected communities.
	uv tool install --upgrade 'graphifyy[leiden]>=0.9.60' 1>&2 ||
		fail 'Graphify installation failed'
	graphify_bin=$(resolve_uv_graphify_launcher) ||
		fail 'cannot resolve the installed Graphify launcher'
	printf '%s\n' "$graphify_bin"
}

main "$@"
