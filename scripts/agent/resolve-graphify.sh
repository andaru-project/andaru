#!/bin/sh
# Shared POSIX resolver for the Graphify launcher and its owning interpreter.
# Safe to source: functions only, with all results written to stdout.

absolutize_graphify_launcher() {
	[ "$#" -eq 1 ] || return 2
	_graphify_launcher=$1
	case "$_graphify_launcher" in
		/*) ;;
		*)
			_graphify_name=${_graphify_launcher##*/}
			_graphify_dir=${_graphify_launcher%/*}
			[ "$_graphify_dir" != "$_graphify_launcher" ] || _graphify_dir=.
			case "$_graphify_dir" in
				-*) _graphify_dir=./$_graphify_dir ;;
			esac
			_graphify_dir=$(CDPATH='' cd -P "$_graphify_dir" 2>/dev/null && pwd -P) || {
				echo "resolve-graphify.sh: cannot resolve selected launcher directory '$_graphify_dir'" >&2
				return 1
			}
			_graphify_launcher=$_graphify_dir/$_graphify_name
			;;
	esac
	printf '%s\n' "$_graphify_launcher"
}

# The launcher uv installed, regardless of what else is on PATH.
resolve_uv_graphify_launcher() {
	command -v uv >/dev/null 2>&1 || {
		echo "resolve-graphify.sh: Graphify is not installed; run 'sh scripts/agent/ensure-graphify.sh' first" >&2
		return 1
	}
	_graphify_uv_bin=$(uv tool dir --bin 2>/dev/null) || {
		echo 'resolve-graphify.sh: cannot resolve uv tool executable directory' >&2
		return 1
	}
	check_graphify_launcher "$_graphify_uv_bin/graphify"
}

# The graphify on PATH, falling back to uv's tool directory.
resolve_graphify_launcher() {
	if _graphify_launcher=$(command -v graphify 2>/dev/null) &&
		[ -n "$_graphify_launcher" ]; then
		check_graphify_launcher "$_graphify_launcher"
	else
		resolve_uv_graphify_launcher
	fi
}

check_graphify_launcher() {
	_graphify_launcher=$(absolutize_graphify_launcher "$1") || return 1
	[ -x "$_graphify_launcher" ] || {
		echo "resolve-graphify.sh: Graphify launcher '$_graphify_launcher' is not executable" >&2
		return 1
	}
	printf '%s\n' "$_graphify_launcher"
}
