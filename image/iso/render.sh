#!/bin/sh
# Renders the install ISO's live Ignition config: dest.bu first, with the image reference the installed box will
# track, then live.bu, which carries dest.ign and the installer. Butane runs from its container, pinned by digest.
#   render.sh IMAGE VERSION CONSOLE OUT   (ENGINE=docker to use docker instead of podman)
# IMAGE is a registry repository (ghcr.io/andaru-project/andaru), VERSION its tag, CONSOLE ttyS0 or ttyAMA0.
set -eu

image=${1:?image} version=${2:?version} console=${3:?console} out=${4:?out}
engine=${ENGINE:-podman}
butane=quay.io/coreos/butane@sha256:143e7efd568e46e6f2bcb6f4250d01b0b49227232cc21a7150f8d39e577b9ea5
dir=$(dirname "$0")

bad() {
	echo "render.sh: bad $1: $2" >&2
	exit 1
}
# They go into sed replacements and a systemd ExecStart line: only the registry's grammar, one line.
case $image$version in *'
'*) bad "image or version" "a newline" ;; esac
printf '%s\n' "$image" | grep -Eqx '[a-z0-9]+([.-][a-z0-9]+)*(:[0-9]+)?(/[a-z0-9]+([._-][a-z0-9]+)*)+' || bad image "$image"
printf '%s\n' "$version" | grep -Eqx '[A-Za-z0-9_][A-Za-z0-9._-]{0,127}' || bad version "$version"
case $console in ttyS0 | ttyAMA0) ;; *) bad console "$console" ;; esac

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
trap 'exit 1' INT TERM
cp "$dir/andaru-install" "$work/"
sed -e "s|@IMAGE@|$image|g" -e "s|@VERSION@|$version|g" "$dir/dest.bu" |
	"$engine" run --rm -i "$butane" --strict >"$work/dest.ign"
sed -e "s|@CONSOLE@|$console|g" "$dir/live.bu" |
	"$engine" run --rm -i -v "$work:/w:ro,z" "$butane" --strict --files-dir /w >"$work/live.ign"
cp "$work/live.ign" "$out"
