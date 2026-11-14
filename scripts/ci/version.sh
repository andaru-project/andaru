#!/bin/sh
# Maps a release tag to "PACKAGES VERSION PRERELEASE" for the release workflow:
#   v1.2.3       any 1.2.3-1 false
#   v1.2.3-rcN   any 1.2.3~rcN-1 true   (~ sorts before 1.2.3-1, so the release upgrades the candidate)
#   keyring-D    all D false            (D is yyyy.mm.dd, andaru-archive-keyring's version)
# Numbers have no leading zeros: dpkg would compare 01.2.3 equal to 1.2.3.
set -eu

tag=${1-}
n='(0|[1-9][0-9]*)'
if printf '%s\n' "$tag" | grep -Eqx "v$n\.$n\.$n"; then
	echo "any ${tag#v}-1 false"
elif printf '%s\n' "$tag" | grep -Eqx "v$n\.$n\.$n-rc[1-9][0-9]*"; then
	core=${tag#v}
	echo "any ${core%-rc*}~rc${core##*-rc}-1 true"
elif printf '%s\n' "$tag" | grep -Eqx 'keyring-[0-9]{4}\.[0-9]{2}\.[0-9]{2}'; then
	echo "all ${tag#keyring-} false"
else
	echo "version.sh: $tag is not a release tag (v1.2.3, v1.2.3-rcN or keyring-yyyy.mm.dd)" >&2
	exit 1
fi
