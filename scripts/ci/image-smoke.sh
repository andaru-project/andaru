#!/bin/sh
# Checks a built Andaru image as a container, without booting it: its version, its packages, its unit states,
# its SELinux labels, its os-release and its signature policy. Booting it is the lab's job. Run it from the repository
# root; SIGNED_NAMESPACE and COSIGN_PUB name a lab build's overrides.
#   image-smoke.sh IMAGE VERSION   (ENGINE=docker to use docker instead of podman)
set -eu

image=${1:?image}
version=${2:?version}
engine=${ENGINE:-podman}

fail() {
	echo "image-smoke: $*" >&2
	exit 1
}
# The signature policy and both keys, byte for byte as the repository has them: which key a scope trusts is the whole
# of the image's update security. tests/shell/policy_check_spec.sh holds the repository's policy to its meaning.
check_file() {
	"$engine" run --rm "$image" cat "$1" | cmp -s - "$2" || fail "$1 differs from $2"
}
rendered=$(mktemp)
trap 'rm -f "$rendered"' EXIT
sed "s|@NAMESPACE@|${SIGNED_NAMESPACE:-ghcr.io/andaru-project}|" image/signing/policy.json >"$rendered"
check_file /etc/containers/policy.json "$rendered"
check_file /etc/pki/containers/andaru.pub "${COSIGN_PUB:-image/cosign.pub}"
check_file /etc/pki/containers/andaru-nightly.pub image/cosign-nightly.pub

label=$("$engine" image inspect --format '{{index .Config.Labels "org.opencontainers.image.version"}}' "$image")
[ "$label" = "$version" ] || fail "version label is '$label', not '$version'"

# shellcheck disable=SC2016 # this script runs inside the image, with VERSION from its environment
"$engine" run --rm -e VERSION="$version" "$image" sh -eu -c '
fail() { echo "image-smoke: $*" >&2; exit 1; }
grep -aqF "$VERSION" /usr/bin/andaru || fail "andaru was not built as $VERSION"
for p in systemd-networkd kea kea-hooks unbound ppp openvpn qemu-guest-agent iputils traceroute; do
	rpm -q --quiet "$p" || fail "$p is not installed"
done
grep -q "^u andaru-diag " /usr/lib/sysusers.d/andaru.conf || fail "andaru-diag, the user diagnostics run as, is not declared"
grep -q "^g andaru-services " /usr/lib/sysusers.d/andaru.conf || fail "andaru-services, the group of the services bus socket, is not declared"
for p in moby-engine containerd; do
	! rpm -q --quiet "$p" || fail "$p is installed"
done
# Files Fedora CoreOS removes from systemd and grub2-tools; an upgrade of either brings them back. The efi.automount
# of systemd-gpt-auto-generator then fails on every boot (#83).
for f in /usr/lib/systemd/system-generators/systemd-gpt-auto-generator \
	/usr/lib/systemd/system/sysinit.target.wants/systemd-firstboot.service /etc/grub.d/08_fallback_counting; do
	[ ! -e "$f" ] || fail "$f is back: systemd or grub2-tools was upgraded"
done
# File capabilities, which rechunk.sh writes anew with every layer: shadow-utils gives these two theirs.
getcap /usr/bin/newuidmap | grep -q "cap_setuid=ep" || fail "/usr/bin/newuidmap lost its file capabilities"
getcap /usr/bin/newgidmap | grep -q "cap_setgid=ep" || fail "/usr/bin/newgidmap lost its file capabilities"
# os-release names Andaru and keeps what Fedora CoreOS tools and the ISO build read. OSTREE_VERSION is read by sourcing
# the file in a subshell: the base quotes it, and its VERSION would replace ours.
for l in NAME=\"Andaru\" VARIANT=\"Andaru\" ID=fedora VARIANT_ID=coreos; do
	grep -qxF "$l" /usr/lib/os-release || fail "os-release lacks $l"
done
build=$(. /usr/lib/os-release && echo "${OSTREE_VERSION:-}")
echo "$build" | grep -Eqx "[0-9]+\.[0-9]{8}\.[0-9]+\.[0-9]+" || fail "os-release OSTREE_VERSION is \"$build\""
grep -qxF "PRETTY_NAME=\"Andaru $VERSION (Fedora CoreOS $build)\"" /usr/lib/os-release ||
	fail "os-release $(grep ^PRETTY_NAME= /usr/lib/os-release)"
[ -n "$(. /usr/lib/os-release && echo "${RELEASE_TYPE:-}")" ] || fail "os-release lacks RELEASE_TYPE"
state() {
	got=$(systemctl is-enabled "$1" 2>/dev/null || true)
	[ "$got" = "$2" ] || fail "$1 is $got, not $2"
}
for u in systemd-networkd.service andaru-engine.service andaru-network-boot.service andaru-factory-reset.service andaru-install-cleanup.service \
	andaru-leases.path andaru-prefixes.path andaru-prefixes.timer andaru-update-check.timer kea-dhcp4.service kea-dhcp6.service unbound.service; do
	state "$u" enabled
done
# Nothing downloads or installs image updates automatically; andaru-update-check.timer checks metadata only.
for u in sshd.service zincati.service andaru-web.service bootc-fetch-apply-updates.timer rpm-ostreed-automatic.timer; do
	state "$u" disabled
done
for u in NetworkManager.service NetworkManager-wait-online.service systemd-networkd-wait-online.service unbound-keygen.service; do
	state "$u" masked
done
command -v bootc >/dev/null && command -v jq >/dev/null || fail "the install cleanup needs bootc and jq"
# First boot applies presets to every unit; the states above must survive that.
systemctl preset-all --preset-mode=enable-only >/dev/null 2>&1 || fail "presets do not apply"
for u in sshd.service zincati.service andaru-web.service bootc-fetch-apply-updates.timer rpm-ostreed-automatic.timer; do
	state "$u" disabled
done
for u in systemd-networkd-wait-online.service unbound-keygen.service; do
	state "$u" masked
done
for f in /run/andaru-unbound/andaru.conf /run/andaru-leases/leases.conf; do
	matchpathcon "$f" | grep -q ":named_conf_t:" || fail "$f would not be readable by Unbound: $(matchpathcon "$f")"
done
matchpathcon /run/andaru-ppp/pp0.options | grep -q ":pppd_etc_t:" ||
	fail "PPPoE options would not be readable by pppd: $(matchpathcon /run/andaru-ppp/pp0.options)"
echo "image-smoke: $VERSION ok"
'
