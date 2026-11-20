#!/bin/sh
# Builds Andaru's install ISO for one architecture, the route the offline install spike proved
# (docs/research/offline-install-spike.md):
#   - Fedora CoreOS's own live ISO for the image's base build, verified with Fedora's key;
#   - the image as an OCI archive at /andaru.oci;
#   - the live Ignition config (render.sh), which runs the installer.
# Nothing runs code of the target architecture, so both ISOs build on an amd64 host.
#   build.sh REPO VERSION DIGEST ARCH AMD64_DIGEST OUT   (ENGINE=docker to use docker instead of podman)
# DIGEST is ARCH's image manifest in REPO, ARCH x86_64 or aarch64. coreos-installer runs from REPO@AMD64_DIGEST, the
# release's own amd64 image, so its Fedora keys are as new as the release. Needs skopeo, xorriso and jq.
set -eu

repo=${1:?repo} version=${2:?version} digest=${3:?digest} arch=${4:?arch} amd64=${5:?amd64 digest} out=${6:?out}
engine=${ENGINE:-podman}
dir=$(cd "$(dirname "$0")" && pwd)
max=2147483648 # GitHub's limit for a release asset

fail() {
	echo "build.sh: $*" >&2
	exit 1
}
for d in "$digest" "$amd64"; do
	printf '%s\n' "$d" | grep -Eqx 'sha256:[0-9a-f]{64}' || fail "bad digest $d"
done
case $arch in x86_64) console=ttyS0 ;; aarch64) console=ttyAMA0 ;; *) fail "bad arch $arch" ;; esac

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
trap 'exit 1' INT TERM

# The base's build and stream, from the image's os-release. Creating a container runs nothing, so this works for an
# aarch64 image on an amd64 host.
ctr=$("$engine" create "$repo@$digest" /bin/true)
"$engine" cp "$ctr:/usr/lib/os-release" "$work/os-release"
"$engine" rm "$ctr" >/dev/null
# Sourced in a subshell: os-release's VERSION would replace ours.
# shellcheck source=/dev/null
build=$(. "$work/os-release" && echo "${OSTREE_VERSION:-}")
# shellcheck source=/dev/null
stream=$(. "$work/os-release" && echo "${RELEASE_TYPE:-}")
printf '%s\n' "$build" | grep -Eqx '[0-9]+\.[0-9]{8}\.[0-9]+\.[0-9]+' || fail "the image's OSTREE_VERSION is '$build'"
printf '%s\n' "$stream" | grep -Eqx '[a-z]+' || fail "the image's RELEASE_TYPE is '$stream'"

# The URL is built from the build ID, not read from the unsigned meta.json, so the file name binds the build.
# coreos-installer fetches <url>.sig and checks it against the Fedora keys it ships; it refuses an unsigned image.
iso=fedora-coreos-$build-live-iso.$arch.iso
url=https://builds.coreos.fedoraproject.org/prod/streams/$stream/builds/$build/$arch/$iso
coreos_installer() {
	"$engine" run --rm -v "$work:/w:z" -w /w "$repo@$amd64" coreos-installer "$@"
}
echo "build.sh: Fedora CoreOS $build ($stream), $arch"
coreos_installer download --image-url "$url" --directory /w
[ -f "$work/$iso" ] || fail "coreos-installer did not download $iso"

# The archive holds exactly the published manifest: the installed box deploys it and tracks REPO:VERSION.
skopeo copy --quiet --preserve-digests "docker://$repo@$digest" "oci-archive:$work/andaru.oci"
tar -xOf "$work/andaru.oci" index.json | jq -e --arg d "$digest" '[.manifests[].digest] == [$d]' >/dev/null ||
	fail "the archive does not hold exactly $digest"

xorriso -indev "$work/$iso" -outdev "$work/andaru.iso" -compliance iso_9660_level=1 \
	-map "$work/andaru.oci" /andaru.oci -boot_image any replay
rm "$work/$iso" "$work/andaru.oci"

sh "$dir/render.sh" "$repo" "$version" "$console" "$work/live.ign"
# The console is also /dev/console: systemd's status lines and kernel messages would draw over the installer there.
coreos_installer iso customize --live-ignition live.ign --live-karg-append "console=$console,115200n8" \
	--live-karg-append systemd.show_status=false --live-karg-append loglevel=3 -o out.iso andaru.iso

# iso customize embeds the config as the one gzip, base64 source its own config merges.
src=$(coreos_installer iso ignition show out.iso |
	jq -er '.ignition.config.merge | select(length == 1) | .[0] | select(.compression == "gzip") | .source') ||
	fail "the ISO's live config is not one merged source"
case $src in data:\;base64,*) ;; *) fail "the ISO's live config source is not a base64 data URL" ;; esac
printf '%s' "${src#data:;base64,}" | base64 -d | gzip -dc | jq -S "del(.. | nulls)" >"$work/shown.ign"
jq -S "del(.. | nulls)" "$work/live.ign" | cmp -s - "$work/shown.ign" || fail "the ISO does not carry the rendered live config"
coreos_installer iso kargs show out.iso | grep -qw "console=$console,115200n8" || fail "the ISO's kernel arguments lack the console"

size=$(wc -c <"$work/out.iso")
[ "$size" -le "$max" ] || fail "the ISO is $size bytes, over GitHub's $max"
mv "$work/out.iso" "$out"
echo "build.sh: $out ($size bytes)"
