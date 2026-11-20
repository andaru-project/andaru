#!/bin/sh
# Checks a rendered live Ignition config for what matters on the ISO and on the installed box: butane --strict
# renders a config without reading its units, so a disabled installer or an unsigned origin would pass it.
#   check-ign.sh LIVE.IGN IMAGE VERSION CONSOLE
set -eu

ign=${1:?ign} image=${2:?image} version=${3:?version} console=${4:?console}
fail() {
	echo "check-ign: $*" >&2
	exit 1
}
# file IGN PATH prints a file's contents, decoding Ignition's data URL: percent-encoded or base64, gzip or not.
file() {
	jq -e --arg p "$2" '.storage.files[] | select(.path == $p) | .contents' "$1" | python3 -I -c '
import base64, gzip, json, sys, urllib.parse
c = json.load(sys.stdin)
head, data = c["source"].split(",", 1)
raw = base64.b64decode(data) if head.endswith(";base64") else urllib.parse.unquote_to_bytes(data)
sys.stdout.buffer.write(gzip.decompress(raw) if c.get("compression") == "gzip" else raw)' || fail "no $2"
}
unit() {
	jq -r --arg n "$2" '.systemd.units[] | select(.name == $n) | .contents // empty' "$1"
}
has() {
	jq -e --arg n "$2" ".systemd.units[] | select(.name == \$n) | $3" "$1" >/dev/null || fail "$2 is not $4"
}

for u in NetworkManager.service NetworkManager-wait-online.service sshd.service sshd.socket zincati.service kmsconvt@tty1.service autovt@tty1.service \
	getty@tty1.service "serial-getty@$console.service"; do
	has "$ign" "$u" '.mask == true' masked
done
for u in andaru-install@tty1.service "andaru-install@$console.service"; do
	has "$ign" "$u" '.enabled == true' enabled
done
installer=$(unit "$ign" andaru-install@.service)
for line in 'ExecStart=/usr/local/bin/andaru-install' 'StandardInput=tty' 'TTYPath=/dev/%I' 'Restart=always' \
	'StartLimitIntervalSec=0' 'Conflicts=getty@%i.service serial-getty@%i.service kmsconvt@%i.service autovt@%i.service'; do
	printf '%s\n' "$installer" | grep -qxF "$line" || fail "the installer unit lacks $line"
done
[ "$(jq -r '.storage.files[] | select(.path == "/usr/local/bin/andaru-install") | .mode' "$ign")" = 493 ] ||
	fail "the installer is not 0755"
file "$ign" /etc/sysctl.d/90-andaru-install.conf | grep -qx 'net.ipv6.conf.all.disable_ipv6 = 1' || fail "IPv6 is on in the live system"

dest=$(mktemp)
trap 'rm -f "$dest"' EXIT
file "$ign" /usr/local/share/andaru-install/dest.ign >"$dest"
for u in NetworkManager.service NetworkManager-wait-online.service zincati.service; do has "$dest" "$u" '.mask == true' masked; done
for u in sshd.service sshd.socket; do has "$dest" "$u" '.enabled == false' disabled; done
has "$dest" andaru-install-rebase.service '.enabled == true' enabled
rebase=$(unit "$dest" andaru-install-rebase.service)
printf '%s\n' "$rebase" | grep -qF -- "--target-imgref ostree-image-signed:docker://$image:$version'" ||
	fail "the rebase does not target ostree-image-signed:docker://$image:$version"
printf '%s\n' "$rebase" | grep -qx 'OnFailure=andaru-install-failed.service' || fail "a failed rebase says nothing"
failed=$(unit "$dest" andaru-install-failed.service)
# In /run, so the message never outlives a boot whose rebase failed; and agetty on tty1, since kmscon shows no issue.
printf '%s\n' "$failed" | grep -qF '>/run/issue.d/andaru-install.issue' || fail "the failure message is not in /run/issue.d"
printf '%s\n' "$failed" | grep -qxF 'ExecStart=-/usr/bin/systemctl --no-block start getty@tty1.service' || fail "the failure message never reaches the screen"
echo "check-ign: $ign ok"
