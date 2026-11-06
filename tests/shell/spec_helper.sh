#!/bin/sh
# shellcheck shell=sh
# shellspec spec_helper -- shared setup for the POSIX-sh test suite.

# shellcheck source=scripts/agent/agent_env.sh
. "${SHELLSPEC_PROJECT_ROOT}/scripts/agent/agent_env.sh"
scrub_git_env

git_fixture() {
	GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null git "$@"
}
