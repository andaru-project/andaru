#!/bin/sh
# Runs the root integration suites in $1 inside a Fedora container, against the Fedora packages the image ships.
# A test that would skip for a missing tool fails here instead.
set -eu

art=${1:?directory of test binaries}
dnf -yq install nftables kea kea-hooks unbound iproute iputils traceroute procps-ng python3 ppp rp-pppoe systemd >/dev/null
# Fedora's Unbound configuration names a trust anchor that unbound-checkconf requires.
unbound-anchor -a /var/lib/unbound/root.key || test -s /var/lib/unbound/root.key
export ANDARU_TEST_REQUIRE_TOOLS=1
"$art/config.test" -test.v -test.timeout=120s
ANDARU_TEST_BINARY="$art/andaru" "$art/engine.test" -test.v -test.timeout=120s
"$art/generation.test" -test.v -test.timeout=120s
"$art/identity.test" -test.v -test.timeout=120s
"$art/audit.test" -test.v -test.timeout=120s
"$art/ctl.test" -test.v -test.timeout=120s
"$art/reset.test" -test.v -test.timeout=120s
# The networkd and systemd tool proofs in this binary are lab proofs; CI runs the filter ones and dials PPPoE between
# namespaces, which needs the host's ppp_generic and pppoe modules. The proxy ARP probe applies the image's sysctl.d
# file, which the workflow puts beside the binaries.
ANDARU_TEST_PROXY_ARP_SYSCTL="$art/50-andaru-proxy-arp.conf" "$art/render.test" -test.v -test.timeout=180s \
  -test.run "FilterInNamespace|NftScriptIsValid|ServiceGoldensPassTheirTools|DHCPv6GoldensPassKea|PPPoESessionInNamespaces|ProxyARPInNamespace"
