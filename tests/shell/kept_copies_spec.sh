#shellcheck shell=sh
# Purge and remove must leave the root-disk copies alone.

Describe 'kept copies survive package removal'
  postrm="${SHELLSPEC_PROJECT_ROOT:-$PWD}/debian/andaru.postrm"
  setup() {
    root=$(mktemp -d)
    mkdir -p "$root/var/lib/andaru-kept" "$root/var/lib/andaru-upgrades/recovery"
    printf 'lan\n' >"$root/var/lib/andaru-kept/first.json"
  }
  cleanup() { rm -rf "$root"; }
  BeforeEach 'setup'
  AfterEach 'cleanup'

  Parameters:value purge remove
  It "keeps the copies on postrm $1"
    When run env DPKG_ROOT="$root" sh "$postrm" "$1"
    The status should equal 0
    The path "$root/var/lib/andaru-kept/first.json" should be file
    The path "$root/var/lib/andaru-upgrades/recovery" should be directory
  End
End

# ProtectSystem=strict leaves only the unit's state directories writable: the engine records last-lan/ and stores
# recovery/ there, so the kept tree must be one of them.
Describe 'the engine may write the kept copies'
  unit="${SHELLSPEC_PROJECT_ROOT:-$PWD}/packaging/systemd/andaru-engine.service"
  It 'lists andaru-kept among its state directories'
    When run sh -c "grep -E '^StateDirectory=.*(^|[= ])andaru-kept( |$)' '$unit'"
    The status should equal 0
    The output should include 'andaru-kept'
  End
End
