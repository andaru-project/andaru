#shellcheck shell=sh
# Debian's convention: remove deletes nothing; purge deletes everything Andaru created.

Describe 'package removal and purge'
  postrm="${SHELLSPEC_PROJECT_ROOT:-$PWD}/debian/andaru.postrm"
  setup() {
    root=$(mktemp -d)
    outside=$(mktemp -d)
    printf 'keep\n' >"$outside/portable-backup.zip"
    mkdir -p "$root/etc/andaru/tls" "$root/var/lib/andaru/generations" "$root/var/lib/andaru-kept/first" \
      "$root/var/lib/andaru-upgrades/recovery" "$root/var/lib/andaru-reset"
    printf 'x\n' >"$root/etc/andaru/web.conf"
    printf 'x\n' >"$root/var/lib/andaru/generations/pointers.json"
    printf 'lan\n' >"$root/var/lib/andaru-kept/first/first.json"
    # A link inside the tree must go without its target.
    ln -s "$outside/portable-backup.zip" "$root/var/lib/andaru/backup-link"
  }
  cleanup() { rm -rf "$root" "$outside"; }
  BeforeEach 'setup'
  AfterEach 'cleanup'

  It 'keeps everything on remove'
    When run env DPKG_ROOT="$root" sh "$postrm" remove
    The status should equal 0
    The path "$root/etc/andaru/web.conf" should be file
    The path "$root/var/lib/andaru/generations/pointers.json" should be file
    The path "$root/var/lib/andaru-kept/first/first.json" should be file
    The path "$root/var/lib/andaru-upgrades/recovery" should be directory
  End

  It 'deletes Andaru'"'"'s configuration and state on purge, and nothing a link points to'
    When run env DPKG_ROOT="$root" sh "$postrm" purge
    The status should equal 0
    The path "$root/etc/andaru" should not be exist
    The path "$root/var/lib/andaru" should not be exist
    The path "$root/var/lib/andaru-kept" should not be exist
    The path "$root/var/lib/andaru-upgrades" should not be exist
    The path "$root/var/lib/andaru-reset" should not be exist
    The path "$outside/portable-backup.zip" should be file
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
