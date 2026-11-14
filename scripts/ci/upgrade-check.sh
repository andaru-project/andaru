#!/bin/sh
# Runs inside install-test's disposable Debian 13 systemd container, after installing /pkg/andaru_*.deb.
# A signed local archive advertises the same binary at a newer version: check must not install/download it,
# preparation must preserve the OLD package, and ordinary APT upgrade/reinstall must still work.
set -eu

old=$(dpkg-query -W -f='${Version}' andaru)
dpkg-deb -R /pkg/andaru_*.deb /tmp/next
sed -i "s/^Version: .*/&+next/" /tmp/next/DEBIAN/control
dpkg-deb -b /tmp/next /tmp/next.deb >/dev/null
next=$(dpkg-deb -f /tmp/next.deb Version)
repo=/var/tmp/andaru-package-test-repo
mkdir -p "$repo/pool"
chmod 0755 "$repo" "$repo/pool"
cp /pkg/andaru_*.deb "$repo/pool/old.deb"
cp /tmp/next.deb "$repo/pool/next.deb"
chmod 0644 "$repo"/pool/*.deb
GNUPGHOME=$(mktemp -d)
export GNUPGHOME
trap 'rm -rf -- "$GNUPGHOME"' EXIT
gpg --batch --quiet --pinentry-mode loopback --passphrase '' \
 --quick-gen-key 'Andaru CI archive <ci@example.invalid>' ed25519 sign 1d
gpg --batch --export > /usr/share/keyrings/andaru-ci-archive.gpg
chmod 0644 /usr/share/keyrings/andaru-ci-archive.gpg
(cd "$repo" && apt-ftparchive packages pool >Packages && gzip -9 -k Packages)
apt-ftparchive release "$repo" >/tmp/andaru-ci-Release
mv /tmp/andaru-ci-Release "$repo/Release"
gpg --batch --yes --clearsign --output "$repo/InRelease" "$repo/Release"
cat > /etc/apt/sources.list.d/andaru-ci.sources <<EOF
Types: deb
URIs: file:$repo
Suites: ./
Signed-By: /usr/share/keyrings/andaru-ci-archive.gpg
EOF

engine=$(systemctl show -P InvocationID andaru-engine.service)
cache=$(find /var/cache/apt/archives -maxdepth 1 -name 'andaru_*.deb' -printf '%f\n' | sort)
andaru upgrade check >/tmp/andaru-ci-available.json
grep -Fq "\"installed\":\"$old\"" /tmp/andaru-ci-available.json
grep -Fq "\"candidate\":\"$next\"" /tmp/andaru-ci-available.json
[ "$(dpkg-query -W -f='${Version}' andaru)" = "$old" ]
[ "$(systemctl show -P InvocationID andaru-engine.service)" = "$engine" ]
[ "$(find /var/cache/apt/archives -maxdepth 1 -name 'andaru_*.deb' -printf '%f\n' | sort)" = "$cache" ]

andaru upgrade prepare >/tmp/andaru-ci-prepared.json
grep -Fq '"packageRetained":true' /tmp/andaru-ci-prepared.json
grep -Fq "\"repository\":\"file:$repo\"" /tmp/andaru-ci-available.json
grep -Fq '"suite":"./"' /tmp/andaru-ci-available.json
grep -Fq "\"version\":\"$old\"" /tmp/andaru-ci-prepared.json
set_dir=$(find /var/lib/andaru-upgrades -mindepth 1 -maxdepth 1 -type d -name 'set-*')
cmp "$set_dir/package.deb" "$repo/pool/old.deb"
[ -f "$set_dir/state/configuration/config.db" ]
[ -f "$set_dir/state/configuration/secrets.db" ]
[ ! -e /var/lib/andaru-upgrades/preparing.json ]
# Preparation deliberately restarts the engine: record a fresh baseline so the next check proves APT's restart.
sh /ci/install-check.sh after
DEBIAN_FRONTEND=noninteractive apt-get -qq install -y andaru >/dev/null
[ "$(dpkg-query -W -f='${Version}' andaru)" = "$next" ]
sh /ci/install-check.sh upgraded

# Direct APT administration never requires or automatically repeats preparation.
recovery=$(cat /var/lib/andaru-upgrades/current.json)
sh /ci/install-check.sh after
DEBIAN_FRONTEND=noninteractive apt-get -qq install -y --reinstall andaru >/dev/null
sh /ci/install-check.sh upgraded
[ "$(cat /var/lib/andaru-upgrades/current.json)" = "$recovery" ]
echo 'upgrade-check: metadata check, recovery preparation and ordinary APT upgrade/reinstall passed'
