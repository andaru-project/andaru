#!/bin/sh
# Builds Andaru's install ISO for one architecture, the route the offline install spike proved
# (docs/research/offline-install-spike.md):
#   - Fedora CoreOS's own live ISO for the image's base build, verified with Fedora's key;
#   - the image as an OCI archive at /andaru.oci;
#   - the live Ignition config (render.sh), which runs the installer.
# Nothing runs code of the target architecture, so both ISOs build on an amd64 host.
#   build.sh REPO VERSION DIGEST ARCH AMD64_DIGEST OUT   (ENGINE=docker to use docker instead of podman)
# DIGEST is ARCH's image manifest in REPO, ARCH x86_64 or aarch64; REPO:VERSION must list it, since the installed box
# tracks that tag. coreos-installer runs from REPO@AMD64_DIGEST, the release's own amd64 image, so its Fedora keys are
# as new as the release. Needs skopeo, xorriso and jq.
set -eu

repo=${1:?repo} version=${2:?version} digest=${3:?digest} arch=${4:?arch} amd64=${5:?amd64 digest} out=${6:?out}
engine=${ENGINE:-podman}
dir=$(dirname "$0")
max=2147483648 # GitHub refuses a release asset of 2 GiB or more

fail() {
	echo "build.sh: $*" >&2
	exit 1
}
# one VALUE PATTERN: VALUE is one line matching the extended regular expression PATTERN in full.
one() {
	case $1 in *'
'*) return 1 ;; esac
	printf '%s\n' "$1" | grep -Eqx "$2"
}
for d in "$digest" "$amd64"; do
	one "$d" 'sha256:[0-9a-f]{64}' || fail "bad digest $d"
done
case $arch in x86_64) console=ttyS0 goarch=amd64 ;; aarch64) console=ttyAMA0 goarch=arm64 ;; *) fail "bad arch $arch" ;; esac
[ ! -d "$out" ] && [ -d "$(dirname "$out")" ] || fail "$out must be a file in an existing directory"

work=$(mktemp -d)
ctr=''
trap 'rm -rf "$work"; [ -z "$ctr" ] || "$engine" rm "$ctr" >/dev/null 2>&1 || :' EXIT
trap 'exit 1' INT TERM

# First, as it checks REPO and VERSION: a malformed one stops the build before any image or ISO is fetched.
sh "$dir/render.sh" "$repo" "$version" "$console" "$work/live.ign"
[ -s "$work/live.ign" ] || fail "render.sh wrote no config"

# Both digests must be their architecture's image and listed by the tag the installed box will track: coreos-installer
# runs from the release's own amd64 image.
listed=$(skopeo inspect --raw "docker://$repo:$version") || fail "cannot read $repo:$version"
for pair in "$digest $goarch" "$amd64 amd64"; do
	# shellcheck disable=SC2086 # a digest and an architecture, split on the space
	set -- $pair
	config=$(skopeo inspect --config "docker://$repo@$1") || fail "cannot read $repo@$1"
	[ "$(printf '%s' "$config" | jq -r .architecture)" = "$2" ] || fail "$1 is not the $2 image"
	printf '%s' "$listed" | jq -e --arg d "$1" '[.manifests[]?.digest] | index($d)' >/dev/null ||
		fail "$repo:$version does not list $1"
done

# The base's build and stream, from the image's os-release, parsed rather than sourced: it is not ours to run.
# Creating a container runs nothing, so this works for an aarch64 image on an amd64 host.
ctr=$("$engine" create "$repo@$digest" /bin/true)
"$engine" cp "$ctr:/usr/lib/os-release" "$work/os-release"
"$engine" rm "$ctr" >/dev/null
ctr=''
# Not needed again unless it is also the image coreos-installer runs from; skopeo fetches the archive itself.
[ "$digest" = "$amd64" ] || "$engine" rmi "$repo@$digest" >/dev/null || :
field() {
	sed -n "s/^$1=//p" "$work/os-release" | sed "s/^\\([\"']\\)\\(.*\\)\\1\$/\\2/"
}
build=$(field OSTREE_VERSION)
stream=$(field RELEASE_TYPE)
one "$build" '[0-9]+\.[0-9]{8}\.[0-9]+\.[0-9]+' || fail "the image's OSTREE_VERSION is '$build'"
one "$stream" '[a-z]+' || fail "the image's RELEASE_TYPE is '$stream'"

# The URL is built from the build ID, not read from the unsigned meta.json. coreos-installer fetches <url>.sig and
# checks it against the Fedora keys it ships; it refuses an unsigned image. The signature binds the content, not the
# name, so the volume ID must also name the build.
iso=fedora-coreos-$build-live-iso.$arch.iso
url=https://builds.coreos.fedoraproject.org/prod/streams/$stream/builds/$build/$arch/$iso
coreos_installer() {
	"$engine" run --rm -v "$work:/w:z" -w /w "$repo@$amd64" coreos-installer "$@"
}
echo "build.sh: Fedora CoreOS $build ($stream), $arch"
coreos_installer download --image-url "$url" --directory /w
[ -f "$work/$iso" ] || fail "coreos-installer did not download $iso"
volume=$(xorriso -indev "$work/$iso" -pvd_info 2>/dev/null | sed -n 's/^Volume Id *: //p')
[ "$volume" = "fedora-coreos-$build" ] || fail "$iso's volume ID is '$volume', not fedora-coreos-$build"

# The archive holds exactly the published manifest: the installed box deploys it and tracks REPO:VERSION.
skopeo copy --quiet --preserve-digests "docker://$repo@$digest" "oci-archive:$work/andaru.oci"
tar -xOf "$work/andaru.oci" index.json | jq -e --arg d "$digest" '[.manifests[].digest] == [$d]' >/dev/null ||
	fail "the archive does not hold exactly $digest"

xorriso -indev "$work/$iso" -outdev "$work/andaru.iso" -compliance iso_9660_level=1 \
	-map "$work/andaru.oci" /andaru.oci -boot_image any replay
# Freed now: iso customize writes a whole second ISO.
rm "$work/$iso" "$work/andaru.oci"

# The console is also /dev/console: systemd's status lines and kernel messages would draw over the installer there.
kargs="console=$console,115200n8 systemd.show_status=false loglevel=3"
set --
for k in $kargs; do set -- "$@" --live-karg-append "$k"; done
coreos_installer iso customize --live-ignition live.ign "$@" -o out.iso andaru.iso
rm "$work/andaru.iso"

# iso customize embeds the config as the one gzip, base64 source its own config merges, and adds nulls when it
# writes it back out.
src=$(coreos_installer iso ignition show out.iso |
	jq -er '.ignition.config.merge | select(length == 1) | .[0] | select(.compression == "gzip") | .source
		| select(startswith("data:;base64,")) | ltrimstr("data:;base64,")') ||
	fail "the ISO's live config is not one gzip, base64 merged source"
printf '%s' "$src" | base64 -d | gzip -dc | jq -S 'del(.. | nulls)' >"$work/shown.ign"
[ -s "$work/shown.ign" ] || fail "the ISO's live config is empty"
jq -S 'del(.. | nulls)' "$work/live.ign" | cmp -s - "$work/shown.ign" || fail "the ISO does not carry the rendered live config"
shown=$(coreos_installer iso kargs show out.iso)
for k in $kargs; do
	case " $shown " in *" $k "*) ;; *) fail "the ISO's kernel arguments lack $k" ;; esac
done

size=$(wc -c <"$work/out.iso")
[ "$size" -lt "$max" ] || fail "the ISO is $size bytes; GitHub takes under $max"
mv "$work/out.iso" "$out"
echo "build.sh: $out ($size bytes)"
