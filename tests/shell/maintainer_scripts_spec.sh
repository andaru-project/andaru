#shellcheck shell=sh
# The andaru package's postinst and prerm, run against a DPKG_ROOT with systemctl and dpkg-query stubbed on PATH.

Describe 'andaru maintainer scripts'
  postinst="${SHELLSPEC_PROJECT_ROOT:-$PWD}/debian/andaru.postinst"
  prerm="${SHELLSPEC_PROJECT_ROOT:-$PWD}/debian/andaru.prerm"
  setup() {
    root=$(mktemp -d)
    bin=$(mktemp -d)
    log=$root/calls
    mkdir -p "$root/usr/share/andaru" "$root/etc/kea" "$root/var/lib/andaru/generations"
    printf '#!/bin/sh\necho andaru-apparmor >>"%s"\n' "$log" >"$root/usr/share/andaru/andaru-apparmor"
    printf '#!/bin/sh\necho "systemctl $*" >>"%s"\n' "$log" >"$bin/systemctl"
    # dpkg-query -W -f='${Conffiles}\n' PACKAGE: what $root/conffiles-PACKAGE holds; not installed without it.
    # shellcheck disable=SC2016
    printf '%s\n' '#!/bin/sh' 'for p; do :; done' "f=$root/conffiles-\$p" '[ -f "$f" ] || exit 1' 'cat "$f"' >"$bin/dpkg-query"
    chmod +x "$root/usr/share/andaru/andaru-apparmor" "$bin/systemctl" "$bin/dpkg-query"
    : >"$log"
  }
  cleanup() { rm -rf "$root" "$bin"; }
  BeforeEach 'setup'
  AfterEach 'cleanup'
  # pointers.json as internal/generation writes it: compact JSON.
  pointers() { printf '%s\n' "$1" >"$root/var/lib/andaru/generations/pointers.json"; }
  taken_over() { pointers '{"confirmed":3,"lastKnownGood":2,"pending":null,"live":3,"takeover":true}'; }
  # kea V installs kea-dhcpV-server with Debian's conffile; kea V changed makes the administrator's own.
  kea() {
    conf=$root/etc/kea/kea-dhcp$1.conf
    printf '{ "Dhcp%s": {} }\n' "$1" >"$conf"
    printf ' /etc/kea/kea-dhcp%s.conf %s\n' "$1" "$(md5sum <"$conf" | cut -d' ' -f1)" >"$root/conffiles-kea-dhcp$1-server"
    [ "${2-}" != changed ] || printf '# the LAN\n' >>"$conf"
  }

  Describe 'postinst configure'
    It 'leaves nftables.service alone: a reboot before a confirmed takeover keeps the host firewall'
      When run env DPKG_ROOT="$root" PATH="$bin:$PATH" sh "$postinst" configure
      The status should equal 0
      The contents of file "$log" should equal 'andaru-apparmor'
    End

    It 'warns that a Kea serving a changed /etc/kea stops serving it at its next restart'
      kea 4 changed
      kea 6
      When run env DPKG_ROOT="$root" PATH="$bin:$PATH" sh "$postinst" configure
      The status should equal 0
      The stderr should include 'kea-dhcp4-server'
      The stderr should include '/etc/kea/kea-dhcp4.conf'
      The stderr should not include 'kea-dhcp6-server'
    End

    It 'says nothing about Kea on a host Andaru has taken over'
      kea 4 changed
      kea 6 changed
      taken_over
      When run env DPKG_ROOT="$root" PATH="$bin:$PATH" sh "$postinst" configure
      The status should equal 0
      The stderr should equal ''
    End

    It "says nothing about Kea's unchanged default configuration"
      kea 4
      kea 6
      When run env DPKG_ROOT="$root" PATH="$bin:$PATH" sh "$postinst" configure
      The status should equal 0
      The stderr should equal ''
    End

    It 'says nothing when Kea is not installed, whatever /etc/kea holds'
      printf '{}\n' >"$root/etc/kea/kea-dhcp4.conf"
      When run env DPKG_ROOT="$root" PATH="$bin:$PATH" sh "$postinst" configure
      The status should equal 0
      The stderr should equal ''
    End

    It 'warns about kea-dhcp6-server alone'
      kea 4
      kea 6 changed
      When run env DPKG_ROOT="$root" PATH="$bin:$PATH" sh "$postinst" configure
      The status should equal 0
      The stderr should include 'kea-dhcp6-server'
      The stderr should not include 'kea-dhcp4-server'
    End

    # Kea unpacked in the same run as Andaru but not yet configured: dpkg lists the conffile, the file is not there.
    It 'says nothing when the listed conffile is not there yet'
      kea 4
      rm "$root/etc/kea/kea-dhcp4.conf"
      When run env DPKG_ROOT="$root" PATH="$bin:$PATH" sh "$postinst" configure
      The status should equal 0
      The stderr should equal ''
    End

    It 'says nothing when dpkg records no MD5 for the conffile'
      kea 4 changed
      printf ' /etc/kea/kea-dhcp4.conf newconffile\n' >"$root/conffiles-kea-dhcp4-server"
      When run env DPKG_ROOT="$root" PATH="$bin:$PATH" sh "$postinst" configure
      The status should equal 0
      The stderr should equal ''
    End
  End

  Describe 'postinst triggered'
    It 'adds the AppArmor includes of a Kea or Unbound installed after Andaru'
      When run env DPKG_ROOT="$root" PATH="$bin:$PATH" sh "$postinst" triggered
      The status should equal 0
      The contents of file "$log" should equal 'andaru-apparmor'
    End
  End

  Describe 'prerm'
    It 'refuses remove on a taken-over host'
      taken_over
      When run env DPKG_ROOT="$root" sh "$prerm" remove
      The status should equal 1
      The stderr should include 'no network'
      The stderr should include 'ANDARU_FORCE_REMOVE=1'
    End

    It 'removes on a taken-over host when forced'
      taken_over
      When run env DPKG_ROOT="$root" ANDARU_FORCE_REMOVE=1 sh "$prerm" remove
      The status should equal 0
    End

    It 'is forced by ANDARU_FORCE_REMOVE=1 alone'
      taken_over
      When run env DPKG_ROOT="$root" ANDARU_FORCE_REMOVE=0 sh "$prerm" remove
      The status should equal 1
      The stderr should include 'ANDARU_FORCE_REMOVE=1'
    End

    It 'reads a pretty-printed pointers.json'
      pointers '{
  "confirmed": null,
  "live": null
}'
      When run env DPKG_ROOT="$root" sh "$prerm" remove
      The status should equal 0
    End

    It 'removes from a host never taken over'
      When run env DPKG_ROOT="$root" sh "$prerm" remove
      The status should equal 0
    End

    It 'removes when no generation was ever confirmed'
      pointers '{"confirmed":null,"lastKnownGood":null,"pending":1,"live":1,"takeover":false}'
      When run env DPKG_ROOT="$root" sh "$prerm" remove
      The status should equal 0
    End

    It 'removes after a takeover was rolled back before confirmation'
      pointers '{"confirmed":null,"lastKnownGood":null,"pending":null,"live":null,"takeover":false}'
      When run env DPKG_ROOT="$root" sh "$prerm" remove
      The status should equal 0
    End

    It 'refuses when pointers.json cannot be read as unconfirmed'
      pointers 'garbage'
      When run env DPKG_ROOT="$root" sh "$prerm" remove
      The status should equal 1
      The stderr should include 'ANDARU_FORCE_REMOVE=1'
    End

    Parameters:value upgrade deconfigure failed-upgrade
    It "never refuses $1"
      taken_over
      When run env DPKG_ROOT="$root" sh "$prerm" "$1"
      The status should equal 0
    End
  End
End
