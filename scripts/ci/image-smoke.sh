#!/bin/sh
# Checks a built Andaru image as a container, without booting it: its version, its packages, its unit states and
# its SELinux labels and its signature policy. Booting it is the lab's job. Run it from the repository root;
# SIGNED_NAMESPACE and COSIGN_PUB name a lab build's overrides.
#   image-smoke.sh IMAGE VERSION   (ENGINE=docker to use docker instead of podman)
set -eu

image=${1:?image}
version=${2:?version}
engine=${ENGINE:-podman}

# The signature policy and both keys, byte for byte as the repository has them: which key a scope trusts is the
# whole of the image's update security. The default must stay reject, or bootc refuses to enforce the policy at all.
check_file() {
	"$engine" run --rm "$image" cat "$1" | cmp -s - "$2" || { echo "image-smoke: $1 differs from $2" >&2; exit 1; }
}
rendered=$(mktemp)
trap 'rm -f "$rendered"' EXIT
sed "s|@NAMESPACE@|${SIGNED_NAMESPACE:-ghcr.io/andaru-project}|" image/signing/policy.json >"$rendered"
check_file /etc/containers/policy.json "$rendered"
check_file /etc/pki/containers/andaru.pub "${COSIGN_PUB:-image/cosign.pub}"
check_file /etc/pki/containers/andaru-nightly.pub image/cosign-nightly.pub

label=$("$engine" image inspect --format '{{index .Config.Labels "org.opencontainers.image.version"}}' "$image")
[ "$label" = "$version" ] || { echo "image-smoke: version label is '$label', not '$version'" >&2; exit 1; }

# shellcheck disable=SC2016 # this script runs inside the image, with VERSION from its environment
"$engine" run --rm -e VERSION="$version" "$image" sh -eu -c '
fail() { echo "image-smoke: $*" >&2; exit 1; }
grep -aqF "$VERSION" /usr/bin/andaru || fail "andaru was not built as $VERSION"
for p in systemd-networkd kea kea-hooks unbound ppp openvpn qemu-guest-agent; do
	rpm -q --quiet "$p" || fail "$p is not installed"
done
for p in moby-engine containerd; do
	! rpm -q --quiet "$p" || fail "$p is installed"
done
state() {
	got=$(systemctl is-enabled "$1" 2>/dev/null || true)
	[ "$got" = "$2" ] || fail "$1 is $got, not $2"
}
for u in systemd-networkd.service andaru-engine.service andaru-network-boot.service andaru-factory-reset.service \
	andaru-leases.path andaru-prefixes.path andaru-prefixes.timer kea-dhcp4.service kea-dhcp6.service unbound.service; do
	state "$u" enabled
done
for u in sshd.service zincati.service andaru-web.service; do
	state "$u" disabled
done
for u in NetworkManager.service NetworkManager-wait-online.service systemd-networkd-wait-online.service; do
	state "$u" masked
done
# First boot applies presets to every unit; the states above must survive that.
systemctl preset-all --preset-mode=enable-only >/dev/null 2>&1 || fail "presets do not apply"
for u in sshd.service zincati.service andaru-web.service; do
	state "$u" disabled
done
state systemd-networkd-wait-online.service masked
for f in /run/andaru-unbound/andaru.conf /run/andaru-leases/leases.conf; do
	matchpathcon "$f" | grep -q ":named_conf_t:" || fail "$f would not be readable by Unbound: $(matchpathcon "$f")"
done
echo "image-smoke: $VERSION ok"
'
