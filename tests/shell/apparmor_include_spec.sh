#shellcheck shell=sh
# The andaru package adds one include line to each daemon's AppArmor local file, which the administrator owns,
# and removes exactly that line on purge.

Describe 'andaru AppArmor includes'
  script="${SHELLSPEC_PROJECT_ROOT:-$PWD}/packaging/apparmor/andaru-apparmor"
  postrm="${SHELLSPEC_PROJECT_ROOT:-$PWD}/debian/andaru.postrm"
  setup() {
    root=$(mktemp -d)
    mkdir -p "$root/etc/apparmor.d/local"
    : >"$root/etc/apparmor.d/usr.sbin.kea-dhcp6"
    : >"$root/etc/apparmor.d/usr.sbin.unbound"
    printf '/srv/x r,\n' >"$root/etc/apparmor.d/local/usr.sbin.kea-dhcp6"
  }
  cleanup() { rm -rf "$root"; }
  BeforeEach 'setup'
  AfterEach 'cleanup'

  It 'adds the include once and keeps the administrator lines'
    DPKG_ROOT="$root" sh "$script"
    When run env DPKG_ROOT="$root" sh "$script"
    The status should equal 0
    The contents of file "$root/etc/apparmor.d/local/usr.sbin.kea-dhcp6" should equal "/srv/x r,
include if exists <andaru/kea-dhcp6>"
  End

  It 'creates a missing local file'
    When run env DPKG_ROOT="$root" sh "$script"
    The contents of file "$root/etc/apparmor.d/local/usr.sbin.unbound" should equal "include if exists <andaru/unbound>"
  End

  It 'leaves a daemon without a profile alone'
    When run env DPKG_ROOT="$root" sh "$script"
    The path "$root/etc/apparmor.d/local/usr.sbin.kea-dhcp4" should not be exist
  End

  It 'removes only its own lines on purge'
    DPKG_ROOT="$root" sh "$script"
    When run env DPKG_ROOT="$root" sh "$postrm" purge
    The status should equal 0
    The contents of file "$root/etc/apparmor.d/local/usr.sbin.kea-dhcp6" should equal "/srv/x r,"
  End
End
