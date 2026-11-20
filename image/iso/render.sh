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

# They go into sed replacements and a systemd ExecStart line: only characters safe in both.
printf '%s\n' "$image" | grep -Eqx '[a-z0-9.-]+(:[0-9]+)?(/[a-z0-9._-]+)+' || { echo "render.sh: bad image $image" >&2; exit 1; }
printf '%s\n' "$version" | grep -Eqx '[A-Za-z0-9_][A-Za-z0-9._-]{0,127}' || { echo "render.sh: bad version $version" >&2; exit 1; }
case $console in ttyS0 | ttyAMA0) ;; *) echo "render.sh: bad console $console" >&2; exit 1 ;; esac

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp "$dir/andaru-install" "$work/"
sed -e "s|@IMAGE@|$image|g" -e "s|@VERSION@|$version|g" "$dir/dest.bu" >"$work/dest.bu"
sed -e "s|@CONSOLE@|$console|g" "$dir/live.bu" >"$work/live.bu"
for f in dest live; do
	"$engine" run --rm -v "$work:/w:z" "$butane" --strict --files-dir /w --output "/w/$f.ign" "/w/$f.bu"
done
cp "$work/live.ign" "$out"
