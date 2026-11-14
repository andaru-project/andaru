#!/bin/sh
# Builds Andaru's Debian packages in a Debian 13 container with the host's Go toolchain (go.mod's version), checks
# them with lintian and leaves them in OUT.
#   build-packages.sh OUT any|all [VERSION]
# any builds andaru for the host's architecture, all builds andaru-archive-keyring. VERSION, when given, is the
# Debian version of a release, written into debian/changelog of the copy that is built.
set -eu

out=$1
which=$2
version=${3-}
case $which in
any) arch=-B ;;
all) arch=-A ;;
*)
	echo "usage: build-packages.sh OUT any|all [VERSION]" >&2
	exit 2
	;;
esac

src=$(mktemp -d)
trap 'rm -rf -- "$src"' EXIT
git ls-files -z | xargs -0 tar -cf - | tar -xf - -C "$src"
if [ -n "$version" ]; then
	{
		printf 'andaru (%s) trixie; urgency=medium\n\n  * Release %s.\n\n -- Andaru project <apt@andaru.io>  %s\n\n' \
			"$version" "$version" "$(date -R)"
		cat "$src/debian/changelog"
	} >"$src/debian/changelog.new"
	mv "$src/debian/changelog.new" "$src/debian/changelog"
fi
mkdir -p "$out" "$(go env GOMODCACHE)" "$(go env GOCACHE)"

docker run --rm -v "$src:/b/src" -v "$(realpath "$out"):/b/out" -v "$(go env GOROOT):/goroot:ro" \
	-v "$(go env GOMODCACHE):/gomod" -v "$(go env GOCACHE):/gocache" \
	-e GOMODCACHE=/gomod -e GOCACHE=/gocache -e GOTOOLCHAIN=local -e HOME=/tmp \
	-e PATH=/goroot/bin:/usr/sbin:/usr/bin:/sbin:/bin -e ARCH="$arch" -e HOST_UID="$(id -u)" \
	debian:trixie sh -euc '
		apt-get -qq update
		DEBIAN_FRONTEND=noninteractive apt-get -qq install -y --no-install-recommends debhelper dpkg-dev gpg lintian >/dev/null
		cd /b/src
		# -d: Go comes from the mounted toolchain, not from Build-Depends.
		dpkg-buildpackage -us -uc -d "$ARCH"
		cd /b
		lintian --allow-root --fail-on error --info --display-info ./*.changes
		cp ./*.deb /b/out/
		chown -R "$HOST_UID" /b/out /gomod /gocache'
