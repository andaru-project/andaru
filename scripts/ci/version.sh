#!/bin/sh
# Maps a release tag to "VERSION PRERELEASE" for the release workflow; VERSION is the image's tag and version label:
#   v1.2.3       1.2.3 false
#   v1.2.3-rc.N  1.2.3-rc.N true  (semver sorts a release candidate below its release, and rc.10 after rc.9)
# Numbers have no leading zeros, as semver requires.
set -eu

tag=${1-}
n='(0|[1-9][0-9]*)'
if printf '%s\n' "$tag" | grep -Eqx "v$n\.$n\.$n"; then
	echo "${tag#v} false"
elif printf '%s\n' "$tag" | grep -Eqx "v$n\.$n\.$n-rc\.[1-9][0-9]*"; then
	echo "${tag#v} true"
else
	echo "version.sh: $tag is not a release tag (v1.2.3 or v1.2.3-rc.N)" >&2
	exit 1
fi
