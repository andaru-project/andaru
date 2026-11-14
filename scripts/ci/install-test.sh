#!/bin/sh
# Installs the andaru package from DIR in a Debian 13 container booted with systemd, checks it with install-check.sh,
# then upgrades it to the same package with a higher version and checks again. Twice: with its recommends, and with
# Kea, Unbound and AppArmor installed afterwards, when Andaru's trigger adds its AppArmor includes.
#   install-test.sh DIR
set -eu

dir=$(realpath "$1")
image=andaru-install-test
here=$(dirname "$(realpath "$0")")

# Docker's Debian images forbid packages to start services (policy-rc.d); a host does not.
printf '%s\n' 'FROM debian:trixie' \
	'RUN apt-get -qq update && DEBIAN_FRONTEND=noninteractive apt-get -qq install -y systemd systemd-sysv iproute2 >/dev/null' \
	'RUN rm -f /usr/sbin/policy-rc.d' \
	'CMD ["/sbin/init"]' | docker build -q -t "$image" - >/dev/null
run() { docker exec "$name" "$@"; }
recommends='apparmor kea-dhcp4-server kea-dhcp6-server unbound'
for order in together later; do
	name=andaru-install-test-$order-$$
	docker run -d --name "$name" --privileged --cgroupns=private -v "$dir:/pkg:ro" -v "$here:/ci:ro" "$image" >/dev/null
	trap 'docker rm -f "$name" >/dev/null' EXIT

	deadline=$(($(date +%s) + 120))
	# Polled without --wait and with a bounded probe, so the deadline is checked however systemd or Docker stalls.
	until case $(timeout 10 docker exec "$name" systemctl is-system-running 2>/dev/null) in running | degraded) true ;; *) false ;; esac do
		if [ "$(docker inspect -f '{{.State.Running}}' "$name")" != true ] || [ "$(date +%s)" -ge "$deadline" ]; then
			docker logs "$name" >&2
			echo "install-test: the container did not boot" >&2
			exit 1
		fi
		sleep 1
	done
	run sh /ci/install-check.sh before
	if [ "$order" = together ]; then
		run sh -c 'DEBIAN_FRONTEND=noninteractive apt-get -qq install -y /pkg/andaru_*.deb >/dev/null'
	else
		run sh -c 'DEBIAN_FRONTEND=noninteractive apt-get -qq install -y --no-install-recommends /pkg/andaru_*.deb >/dev/null'
		run sh -c "DEBIAN_FRONTEND=noninteractive apt-get -qq install -y $recommends >/dev/null"
	fi
	run sh /ci/install-check.sh after

	# The same package, one version higher: what an upgrade does to the units, without a second build.
	run sh -euc '
		dpkg-deb -R /pkg/andaru_*.deb /tmp/next
		sed -i "s/^Version: .*/&+next/" /tmp/next/DEBIAN/control
		dpkg-deb -b /tmp/next /tmp/next.deb >/dev/null
		dpkg -i /tmp/next.deb >/dev/null'
	run sh /ci/install-check.sh upgraded
	docker rm -f "$name" >/dev/null
done
echo "install-test: the package installs and upgrades cleanly"
