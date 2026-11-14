#!/bin/sh
# Maps a release tag to "PACKAGES VERSION PRERELEASE" for the release workflow:
#   v1.2.3       any 1.2.3-1 false
#   v1.2.3-rcN   any 1.2.3~rcN-1 true   (~ sorts before 1.2.3-1, so the release upgrades the candidate)
#   keyring-D    all D false            (D is yyyy.mm.dd, andaru-archive-keyring's version)
set -eu

tag=${1-}
case $tag in
v*)
	core=${tag#v}
	rc=
	case $core in
	*-rc*)
		rc=${core#*-rc}
		core=${core%%-rc*}
		;;
	esac
	if printf '%s\n' "$core" | grep -Eqx '[0-9]+\.[0-9]+\.[0-9]+' &&
		{ [ -z "$rc" ] || printf '%s\n' "$rc" | grep -Eqx '[1-9][0-9]*'; }; then
		if [ -n "$rc" ]; then
			echo "any $core~rc$rc-1 true"
		else
			echo "any $core-1 false"
		fi
		exit 0
	fi
	;;
keyring-*)
	date=${tag#keyring-}
	if printf '%s\n' "$date" | grep -Eqx '[0-9]{4}\.[0-9]{2}\.[0-9]{2}'; then
		echo "all $date false"
		exit 0
	fi
	;;
esac
echo "version.sh: $tag is not a release tag (v1.2.3, v1.2.3-rcN or keyring-yyyy.mm.dd)" >&2
exit 1
