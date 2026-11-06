#!/bin/sh
# One-time developer setup: install Graphify with its Leiden backend, activate the
# tracked hooks, register the Graphify merge driver, and bootstrap CodeGraph.
#
# Run once after cloning:
#   sh scripts/setup-hooks.sh
#
# git cannot auto-apply a committed core.hooksPath (by design: cloning a repository
# must not silently install executable hooks), so this explicit opt-in is the
# closest to "automatic".

set -eu

script_dir=$(CDPATH='' cd "$(dirname "$0")" && pwd -P)
root=$(git -C "$script_dir" rev-parse --show-toplevel)
# Installs Graphify and registers merge.graphify.driver for the tracked graph.
sh "$script_dir/agent/ensure-graphify-merge-driver.sh" "$root"
git -C "$root" config core.hooksPath .githooks

# Graphify's own `hook install` drops post-commit/post-checkout rebuild hooks into
# whatever core.hooksPath names, and leftover copies stay in .git/hooks. The
# tracked pre-commit and pre-push hooks own the graph's lifecycle, so strip those
# blocks wherever they landed and never invoke that installer.
remove_graphify_git_hook() {
	_file=$1
	_start=$2
	_end=$3
	[ -f "$_file" ] || return 0
	grep -Fq "$_start" "$_file" || return 0
	awk -v s="$_start" -v e="$_end" '
		index($0, s) { if (!st) st = NR }
		st && NR >= st && index($0, e) { ok = 1; exit }
		END { exit !ok }
	' "$_file" || return 0
	_tmp=${_file}.graphify-strip
	awk -v s="$_start" -v e="$_end" '
		index($0, s) && !inblock { inblock = 1; buf = $0 ORS; next }
		inblock {
			buf = buf $0 ORS
			if (index($0, e)) { inblock = 0; buf = ""; next }
			next
		}
		{ print }
		END { if (inblock) printf "%s", buf }
	' "$_file" > "$_tmp" || {
		rm -f "$_tmp"
		return 1
	}
	_leftover=$(sed '1{/^#!/d;}' "$_tmp" | grep -v '^[[:space:]]*$' || true)
	if [ -z "$_leftover" ]; then
		rm -f "$_file" "$_tmp"
	else
		[ -x "$_file" ] && chmod +x "$_tmp"
		mv "$_tmp" "$_file"
	fi
}
common=$(git -C "$root" rev-parse --git-common-dir)
case "$common" in
	/*) ;;
	*) common=$root/$common ;;
esac
for dir in "$common/hooks" "$root/.githooks"; do
	remove_graphify_git_hook "$dir/post-commit" '# graphify-hook-start' '# graphify-hook-end'
	remove_graphify_git_hook "$dir/post-checkout" '# graphify-checkout-hook-start' '# graphify-checkout-hook-end'
	remove_graphify_git_hook "$dir/post-merge" '# graphify-merge-hook-start' '# graphify-merge-hook-end'
	remove_graphify_git_hook "$dir/post-rewrite" '# graphify-rewrite-hook-start' '# graphify-rewrite-hook-end'
done
unset common

if command -v codegraph >/dev/null 2>&1; then
	sh "$script_dir/agent/ensure-codegraph.sh" "$root"
else
	echo 'codegraph not found; skipping the CodeGraph index' >&2
fi

printf 'core.hooksPath set to: %s\n' "$(git -C "$root" config core.hooksPath)"
printf 'Active hooks:\n'
for hook in "$root"/.githooks/*; do
	[ -f "$hook" ] || continue
	printf '  %s\n' "$(basename "$hook")"
done
