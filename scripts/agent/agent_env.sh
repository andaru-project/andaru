#!/bin/sh
# agent_env.sh -- shared helpers for the agent-tooling scripts in this directory.
# Safe to source: functions only.
#
# Exit codes reserved across scripts/agent/: 0 success, 1 check failed,
# 2 usage/precondition, 4 tool missing.

require_tool() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "TOOL-MISSING: $1" >&2
		exit 4
	fi
}

# Under a git hook, exported GIT_DIR/GIT_INDEX_FILE would aim every git command at
# the hook's repository instead of the checkout the caller named. Git-touching
# scripts call this first.
scrub_git_env() {
	unset GIT_DIR GIT_INDEX_FILE GIT_WORK_TREE GIT_PREFIX GIT_OBJECT_DIRECTORY GIT_COMMON_DIR
}
