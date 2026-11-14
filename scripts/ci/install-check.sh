#!/bin/sh
# Checks the andaru package on a Debian 13 host booted with systemd: CI's container, or a lab VM. The sequence is
# before, install the package, after, upgrade it, upgraded:
#   install-check.sh before   records the interfaces, routes and unit invocations, before installing
#   install-check.sh after    checks the installed package, and that installing changed no interface or route
#   install-check.sh upgraded checks an upgrade restarted the engine and ran no oneshot
# Run before, install, after, upgrade, upgraded within one boot: a reboot gives every unit a new InvocationID, so after
# one, run before and after again before upgrading.
set -eu

state=/var/tmp/andaru-install-check
fail() {
	echo "install-check: $*" >&2
	exit 1
}
# Lifetimes count down while the package installs; any other change to an address or a route counts.
network() {
	addresses=$(ip -j addr show) && routes=$(ip -j route show table all) || return 1
	printf '%s\n%s\n' "$addresses" "$routes" | sed -E 's/"(valid_life_time|preferred_life_time|expires)":[0-9]+,?//g'
}
unchanged() {
	network >"$state/now" || fail "could not read the interfaces and routes"
	cmp -s "$state/now" "$state/network"
}
invocation() { systemctl show -P InvocationID "$1"; }

case ${1-} in
before)
	mkdir -p "$state"
	# network-boot runs at every boot; what counts is that installing or upgrading does not run it again.
	invocation andaru-network-boot.service >"$state/network-boot"
	# Installing or upgrading must never restart systemd-networkd: a restart can drop the administrator's link.
	invocation systemd-networkd.service >"$state/networkd"
	network >"$state/network" || fail "could not read the interfaces and routes"
	;;
after)
	for f in /usr/bin/andaru \
		/usr/lib/systemd/system/andaru-engine.service /usr/lib/systemd/system/andaru-network-boot.service \
		/usr/lib/systemd/system/andaru-leases.path /usr/lib/systemd/system/andaru-leases.service \
		/usr/lib/systemd/system/andaru-prefixes.path /usr/lib/systemd/system/andaru-prefixes.service \
		/usr/lib/systemd/system/andaru-prefixes.timer \
		/usr/lib/systemd/system/kea-dhcp4-server.service.d/andaru.conf \
		/usr/lib/systemd/system/kea-dhcp6-server.service.d/andaru.conf \
		/usr/lib/systemd/networkd.conf.d/andaru.conf /usr/share/iproute2/rt_protos.d/andaru.conf \
		/usr/lib/tmpfiles.d/andaru.conf /usr/share/kea/scripts/andaru-lease-event \
		/etc/unbound/unbound.conf.d/andaru.conf /etc/apparmor.d/andaru/kea-dhcp4 \
		/etc/apparmor.d/andaru/kea-dhcp6 /etc/apparmor.d/andaru/unbound; do
		[ -e "$f" ] || fail "$f is missing"
	done
	# A confirmed takeover masks it; until then a reboot must still load the host's own firewall.
	[ "$(systemctl is-enabled nftables.service)" != masked ] || fail "installing masked nftables.service"
	for unit in andaru-engine.service andaru-network-boot.service andaru-leases.path andaru-prefixes.path \
		andaru-prefixes.timer; do
		systemctl is-enabled --quiet "$unit" || fail "$unit is not enabled"
	done
	for unit in andaru-engine.service andaru-leases.path andaru-prefixes.path andaru-prefixes.timer; do
		systemctl is-active --quiet "$unit" || fail "$unit is not active"
	done
	[ "$(invocation andaru-network-boot.service)" = "$(cat "$state/network-boot")" ] ||
		fail "installing ran andaru-network-boot.service"
	[ "$(invocation systemd-networkd.service)" = "$(cat "$state/networkd")" ] || fail "installing restarted systemd-networkd"
	andaru ctl links >/dev/null || fail "the engine does not answer"
	for daemon in kea-dhcp4 kea-dhcp6 unbound; do
		grep -qxF "include if exists <andaru/$daemon>" "/etc/apparmor.d/local/usr.sbin.$daemon" ||
			fail "the AppArmor include for $daemon is missing"
	done
	# Unbound reads Andaru's include, whose directories must exist however the two were installed.
	systemctl is-active --quiet unbound.service || fail "unbound.service is not active"
	ip route show proto andaru >/dev/null || fail "ip does not know the andaru route protocol"
	unchanged || fail "installing changed an interface or a route"
	invocation andaru-engine.service >"$state/engine"
	;;
upgraded)
	[ "$(invocation andaru-engine.service)" != "$(cat "$state/engine")" ] || fail "the upgrade did not restart the engine"
	systemctl is-active --quiet andaru-engine.service || fail "the engine is not running after the upgrade"
	[ "$(invocation andaru-network-boot.service)" = "$(cat "$state/network-boot")" ] ||
		fail "upgrading ran andaru-network-boot.service"
	[ "$(invocation systemd-networkd.service)" = "$(cat "$state/networkd")" ] || fail "upgrading restarted systemd-networkd"
	unchanged || fail "upgrading changed an interface or a route"
	;;
*)
	echo "usage: install-check.sh before|after|upgraded" >&2
	exit 2
	;;
esac
