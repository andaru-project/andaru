#shellcheck shell=sh

Describe 'andaru web packaging'
  root="${SHELLSPEC_PROJECT_ROOT:-$PWD}"

  It 'creates a static non-login service account without a fixed numeric id'
    When call grep -Eq '^u[[:space:]]+andaru-web[[:space:]]+-[[:space:]]+"Andaru web service"[[:space:]]+/nonexistent[[:space:]]+/usr/sbin/nologin$' "$root/packaging/sysusers.d/andaru.conf"
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
      grep -qx "EnvironmentFile=-/etc/andaru/engine-web.conf" "$2" &&
      grep -qx "ExecStart=/usr/bin/andaru engine [$]ANDARU_ENGINE_WEB_ARGS" "$2"' sh "$unit" "$root/packaging/systemd/andaru-engine.service"
    The status should equal 0
  End
End
