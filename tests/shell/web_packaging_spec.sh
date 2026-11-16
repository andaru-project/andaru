#shellcheck shell=sh

Describe 'andaru web packaging'
  root="${SHELLSPEC_PROJECT_ROOT:-$PWD}"

  It 'creates a static non-login service account without a fixed numeric id'
    When call grep -Eq '^u[[:space:]]+andaru-web[[:space:]]+-[[:space:]]+"Andaru web service"[[:space:]]+/nonexistent[[:space:]]+/usr/sbin/nologin$' "$root/debian/andaru.sysusers"
    The status should equal 0
  End

  It 'declares the root-owned web socket parent and group traversal mode'
    When call grep -Eq '^d[[:space:]]+/run/andaru-web[[:space:]]+0710[[:space:]]+root[[:space:]]+andaru-web[[:space:]]+-$' "$root/packaging/tmpfiles.d/andaru.conf"
    The status should equal 0
  End

  It 'keeps the TLS web service disabled until explicit configuration'
    unit="$root/packaging/systemd/andaru-web.service"
    When call sh -c '
      grep -qx "User=andaru-web" "$1" &&
      grep -qx "Group=andaru-web" "$1" &&
      grep -qx "EnvironmentFile=/etc/andaru/web.conf" "$1" &&
      grep -qx "ConditionPathExists=/etc/andaru/engine-web.conf" "$1" &&
      grep -q -- "--tls-cert.*--tls-key.*--origin.*--backend-socket /run/andaru-web/engine.sock.*--assets-dir /usr/share/andaru/web" "$1" &&
      grep -qx "NoNewPrivileges=yes" "$1" &&
      grep -qx "PrivateTmp=yes" "$1" &&
      grep -qx "ProtectHome=yes" "$1" &&
      grep -qx "ProtectSystem=strict" "$1" &&
      grep -qx "CapabilityBoundingSet=" "$1" &&
      grep -q -- "--no-enable --no-start andaru-web.service" "$2" &&
      grep -qx "EnvironmentFile=-/etc/andaru/engine-web.conf" "$3" &&
      grep -qx "ExecStart=/usr/bin/andaru engine [$]ANDARU_ENGINE_WEB_ARGS" "$3"' sh "$unit" "$root/debian/rules" "$root/packaging/systemd/andaru-engine.service"
    The status should equal 0
  End

  It 'builds both Go binaries and locked local static assets'
    When call sh -c '
      grep -q "npm --prefix web ci --ignore-scripts" "$1" &&
      grep -q "npm --prefix web run build" "$1" &&
      grep -q -- "-o debian/build/andaru-web ./cmd/andaru-web" "$1" &&
      grep -qx "debian/build/andaru-web usr/bin/" "$2" &&
      grep -qx "web/dist/ usr/share/andaru/web/" "$2"' sh "$root/debian/rules" "$root/debian/andaru.install"
    The status should equal 0
  End

  It 'ships an AppArmor profile for the network-facing process'
    profile="$root/packaging/apparmor/usr.bin.andaru-web"
    When call sh -c '
      grep -q "profile andaru-web /usr/bin/andaru-web" "$1" &&
      grep -q "/run/andaru-web/engine.sock rw," "$1" &&
      grep -q "/usr/share/andaru/web/\\*\\* r," "$1" &&
      grep -q "/etc/andaru/\\*\\* r," "$1"' sh "$profile"
    The status should equal 0
  End
End
