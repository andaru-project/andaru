#!/bin/sh
# Maps a release tag to "VERSION PRERELEASE" for the release workflow; VERSION is the image's tag and version label:
#   v1.2.3       1.2.3 false
#   v1.2.3-a.N   1.2.3-a.N true   alpha
#   v1.2.3-b.N   1.2.3-b.N true   beta
#   v1.2.3-rc.N  1.2.3-rc.N true  release candidate
# Semver sorts a.N below b.N below rc.N below the release, and .10 after .9.
# Numbers have no leading zeros, as semver requires.
set -eu

tag=${1-}
n='(0|[1-9][0-9]*)'
if printf '%s\n' "$tag" | grep -Eqx "v$n\.$n\.$n"; then
	echo "${tag#v} false"
elif printf '%s\n' "$tag" | grep -Eqx "v$n\.$n\.$n-(a|b|rc)\.[1-9][0-9]*"; then
	echo "${tag#v} true"
else
	echo "version.sh: $tag is not a release tag (v1.2.3, v1.2.3-a.N, v1.2.3-b.N or v1.2.3-rc.N)" >&2
	exit 1
fi
