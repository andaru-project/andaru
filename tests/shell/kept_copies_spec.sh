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
    printf '{"confirmed":null,"takeover":false}' >"$root/var/lib/andaru/generations/pointers.json"
    printf 'lan\n' >"$root/var/lib/andaru-kept/first/first.json"
    # A link inside the tree must go without its target.
    ln -s "$outside/portable-backup.zip" "$root/var/lib/andaru/backup-link"
    ln -s "$outside" "$root/var/lib/andaru/dir-link"
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
    The path "$root/var/lib/andaru-reset" should be directory
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
    The stderr should equal ''
  End

  It 'leaves a tree whole when something is mounted inside it'
    # ANDARU_MOUNTINFO stands in for /proc/self/mountinfo: a bind mount of /srv/backups at andaru/archive.
    mkdir -p "$root/var/lib/andaru/archive"
    printf 'keep\n' >"$root/var/lib/andaru/archive/backup.zip"
    real=$(readlink -f "$root")
    printf '36 25 8:1 /srv/backups %s/var/lib/andaru/archive rw - ext4 /dev/sda1 rw\n' "$real" >"$outside/mountinfo"
    When run env DPKG_ROOT="$root" ANDARU_MOUNTINFO="$outside/mountinfo" sh "$postrm" purge
    The status should equal 0
    The stderr should include 'mounted inside it'
    The path "$root/var/lib/andaru/archive/backup.zip" should be file
    The path "$root/var/lib/andaru-kept" should not be exist
  End

  It 'decodes a backslash in a mount path before matching it'
    tree=$(mktemp -d)
    mkdir -p "$tree/a\\b/inner"
    printf 'keep\n' >"$tree/a\\b/inner/backup.zip"
    rm -rf "$root/var/lib/andaru-upgrades"
    ln -s "$tree/a\\b" "$root/var/lib/andaru-upgrades"
    real=$(readlink -f "$tree")
    printf '36 25 8:1 /srv %s/a\\134b/inner rw - ext4 /dev/sda1 rw\n' "$real" >"$outside/mountinfo"
    When run env DPKG_ROOT="$root" ANDARU_MOUNTINFO="$outside/mountinfo" sh "$postrm" purge
    The status should equal 0
    The stderr should include 'mounted inside it'
    The path "$tree/a\\b/inner/backup.zip" should be file
  End

  It 'keeps a trailing newline in a linked tree'"'"'s target when matching mounts'
    tree=$(mktemp -d)
    nl=$(printf 'a\n.')
    nl=${nl%.}
    mkdir -p "$tree/$nl/inner"
    printf 'keep\n' >"$tree/$nl/inner/backup.zip"
    rm -rf "$root/var/lib/andaru-upgrades"
    ln -s "$tree/$nl" "$root/var/lib/andaru-upgrades"
    real=$(readlink -f "$tree")
    printf '36 25 8:1 /srv %s/a\\012/inner rw - ext4 /dev/sda1 rw\n' "$real" >"$outside/mountinfo"
    When run env DPKG_ROOT="$root" ANDARU_MOUNTINFO="$outside/mountinfo" sh "$postrm" purge
    The status should equal 0
    The stderr should include 'mounted inside it'
    The path "$tree/$nl/inner/backup.zip" should be file
  End

  It 'leaves every tree whole when the mounts cannot be read'
    When run env DPKG_ROOT="$root" ANDARU_MOUNTINFO="$outside/no-such-file" sh "$postrm" purge
    The status should equal 0
    The stderr should include 'cannot be read'
    The path "$root/var/lib/andaru-kept/first/first.json" should be file
    The path "$root/etc/andaru/web.conf" should be file
  End

  It 'warns when it purges a host with a confirmed generation'
    printf '{"confirmed":3,"takeover":false}' >"$root/var/lib/andaru/generations/pointers.json"
    When run env DPKG_ROOT="$root" sh "$postrm" purge
    The status should equal 0
    The stderr should include 'still configured'
  End

  It 'warns when it purges a host Andaru still configured'
    printf '{"confirmed":null,"takeover":true}' >"$root/var/lib/andaru/generations/pointers.json"
    When run env DPKG_ROOT="$root" sh "$postrm" purge
    The status should equal 0
    The stderr should include 'still configured'
    The path "$root/var/lib/andaru-kept" should not be exist
  End

  It 'empties a tree that is a link to elsewhere, and removes the link'
    target=$(mktemp -d)
    printf 'secret\n' >"$target/secrets.db"
    rm -rf "$root/var/lib/andaru-upgrades"
    ln -s "$target" "$root/var/lib/andaru-upgrades"
    When run env DPKG_ROOT="$root" sh "$postrm" purge
    The status should equal 0
    The path "$root/var/lib/andaru-upgrades" should not be exist
    The path "$target/secrets.db" should not be exist
  End
End
# A mount inside a tree (left in place, with a warning, and the purge going on) needs root and a mount namespace;
# it was checked by hand on a tmpfs.

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
