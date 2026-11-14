#shellcheck shell=sh
# Kea and Unbound may start before Andaru's AppArmor rules are loaded and fail on the denied reads; andaru-apparmor
# reloads each profile and starts such a failed daemon again. apparmor_parser, systemctl and deb-systemd-invoke are
# stubs on PATH that log their arguments; the scratch root has a kernel AppArmor directory.

Describe 'andaru-apparmor restarts a daemon that failed before its rules loaded'
  script="${SHELLSPEC_PROJECT_ROOT:-$PWD}/packaging/apparmor/andaru-apparmor"
  setup() {
    root=$(mktemp -d)
    bin=$(mktemp -d)
    log=$root/calls
    mkdir -p "$root/etc/apparmor.d/local" "$root/sys/kernel/security/apparmor"
    : >"$root/etc/apparmor.d/usr.sbin.unbound"
    : >"$root/etc/apparmor.d/usr.sbin.kea-dhcp4"
    for c in apparmor_parser deb-systemd-invoke; do
      printf '#!/bin/sh\necho "%s $*" >>"%s"\n' "$c" "$log" >"$bin/$c"
    done
    # is-failed succeeds only for the units named in FAILED; the stub expands its own arguments.
    printf '#!/bin/sh\necho "systemctl $*" >>"%s"\n' "$log" >"$bin/systemctl"
    # shellcheck disable=SC2016
    printf '%s\n' '[ "$1" = is-failed ] || exit 0' 'for u in $FAILED; do [ "$u" = "$3" ] && exit 0; done' 'exit 1' >>"$bin/systemctl"
    chmod +x "$bin"/*
    : >"$log"
  }
  cleanup() { rm -rf "$root" "$bin"; }
  BeforeEach 'setup'
  AfterEach 'cleanup'

  It 'reloads each profile and starts the failed units through deb-systemd-invoke'
    When run env DPKG_ROOT="$root" PATH="$bin:$PATH" FAILED='unbound.service kea-dhcp4-server.service' sh "$script"
    The status should equal 0
    The contents of file "$log" should include "apparmor_parser -r -T -W $root/etc/apparmor.d/usr.sbin.unbound"
    The contents of file "$log" should include 'systemctl reset-failed unbound.service'
    The contents of file "$log" should include 'deb-systemd-invoke start unbound.service'
    The contents of file "$log" should include 'systemctl reset-failed kea-dhcp4-server.service'
    The contents of file "$log" should include 'deb-systemd-invoke start kea-dhcp4-server.service'
  End

  It 'leaves an active daemon alone'
    When run env DPKG_ROOT="$root" PATH="$bin:$PATH" FAILED='' sh "$script"
    The status should equal 0
    The contents of file "$log" should include 'apparmor_parser'
    The contents of file "$log" should not include 'deb-systemd-invoke'
    The contents of file "$log" should not include 'reset-failed'
  End

  It 'loads nothing without a kernel AppArmor'
    rmdir "$root/sys/kernel/security/apparmor"
    When run env DPKG_ROOT="$root" PATH="$bin:$PATH" FAILED='unbound.service' sh "$script"
    The status should equal 0
    The contents of file "$log" should equal ''
  End
End
