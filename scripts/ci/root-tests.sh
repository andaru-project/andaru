#!/bin/sh
# Runs the root integration suites in $1 inside a Fedora container, against the Fedora packages the image ships.
# A test that would skip for a missing tool fails here instead.
set -eu

art=${1:?directory of test binaries}
dnf -yq install nftables kea kea-hooks unbound iproute iputils traceroute procps-ng python3 >/dev/null
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
# The pppd and networkd tool proofs in this binary are lab proofs; CI runs the filter ones.
"$art/render.test" -test.v -test.timeout=120s -test.run "FilterInNamespace|NftScriptIsValid|ServiceGoldensPassTheirTools|DHCPv6GoldensPassKea"
