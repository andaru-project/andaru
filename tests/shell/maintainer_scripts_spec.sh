#shellcheck shell=sh
# The andaru package's postinst and prerm, run against a DPKG_ROOT with systemctl stubbed on PATH.

Describe 'andaru maintainer scripts'
  postinst="${SHELLSPEC_PROJECT_ROOT:-$PWD}/debian/andaru.postinst"
  prerm="${SHELLSPEC_PROJECT_ROOT:-$PWD}/debian/andaru.prerm"
  setup() {
    root=$(mktemp -d)
    bin=$(mktemp -d)
    log=$root/calls
    mkdir -p "$root/usr/share/andaru" "$root/usr/sbin" "$root/etc/kea" "$root/var/lib/andaru/generations"
    printf '#!/bin/sh\necho andaru-apparmor >>"%s"\n' "$log" >"$root/usr/share/andaru/andaru-apparmor"
    printf '#!/bin/sh\necho "systemctl $*" >>"%s"\n' "$log" >"$bin/systemctl"
    chmod +x "$root/usr/share/andaru/andaru-apparmor" "$bin/systemctl"
    : >"$log"
  }
  cleanup() { rm -rf "$root" "$bin"; }
  BeforeEach 'setup'
  AfterEach 'cleanup'
  taken_over() { : >"$root/var/lib/andaru/generations/pointers.json"; }
  kea() {
    : >"$root/usr/sbin/kea-dhcp$1"
    chmod +x "$root/usr/sbin/kea-dhcp$1"
    : >"$root/etc/kea/kea-dhcp$1.conf"
  }

  Describe 'postinst configure'
    It 'leaves nftables.service alone: a reboot before a confirmed takeover keeps the host firewall'
      When run env DPKG_ROOT="$root" PATH="$bin:$PATH" sh "$postinst" configure
      The status should equal 0
      The contents of file "$log" should equal 'andaru-apparmor'
    End

    It 'warns that an installed Kea stops serving /etc/kea at its next restart'
      kea 4
      When run env DPKG_ROOT="$root" PATH="$bin:$PATH" sh "$postinst" configure
      The status should equal 0
      The stderr should include 'kea-dhcp4-server'
      The stderr should include '/etc/kea/kea-dhcp4.conf'
      The stderr should not include 'kea-dhcp6-server'
    End

    It 'says nothing about Kea on a host Andaru has taken over'
      kea 4
      kea 6
      taken_over
      When run env DPKG_ROOT="$root" PATH="$bin:$PATH" sh "$postinst" configure
      The status should equal 0
      The stderr should equal ''
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

    It 'removes from a host never taken over'
      When run env DPKG_ROOT="$root" sh "$prerm" remove
      The status should equal 0
    End

    Parameters:value upgrade deconfigure failed-upgrade
    It "never refuses $1"
      taken_over
      When run env DPKG_ROOT="$root" sh "$prerm" "$1"
      The status should equal 0
    End
  End
End
