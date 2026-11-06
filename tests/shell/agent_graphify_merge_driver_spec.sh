#shellcheck shell=sh

Describe 'ensure-graphify-merge-driver.sh'

  setup() {
    fixture=$(mktemp -d "${TMPDIR:-/tmp}/graphify_driver_spec.XXXXXX") || return 1
    fixture=$(cd "$fixture" && pwd -P) || return 1
    helperdir="$fixture/helper/scripts/agent"
    mkdir -p "$helperdir"
    for f in ensure-graphify-merge-driver.sh ensure-graphify.sh agent_env.sh resolve-graphify.sh; do
      cp "$SHELLSPEC_PROJECT_ROOT/scripts/agent/$f" "$helperdir/"
    done
    script_abs="$helperdir/ensure-graphify-merge-driver.sh"
    repo="$fixture/requested-root"
    git_fixture init -q "$repo" || return 1
    stubdir="$fixture/bin"
    mkdir -p "$stubdir"
    uv_log="$fixture/uv.log"
    graphify_log="$fixture/graphify.log"

    # Pins the exact install line: changing it (e.g. dropping the leiden extra,
    # which silently degrades clustering to Louvain) turns this suite red.
    cat > "$stubdir/uv" <<'UV'
#!/bin/sh
case "$*" in
  'tool install --upgrade graphifyy[leiden]>=0.9.60')
    printf '%s\n' "$*" >> "$UV_LOG"
    if [ "${UV_PROGRESS_FIXTURE:-0}" = 1 ]; then printf '%s\n' 'uv progress'; fi
    ;;
  'tool dir --bin')
    [ -n "${UV_TOOL_BIN_FIXTURE:-}" ] || exit 9
    printf '%s\n' "$UV_TOOL_BIN_FIXTURE"
    ;;
  *) exit 9 ;;
esac
UV
    cat > "$stubdir/graphify" <<'GRAPHIFY'
#!/bin/sh
printf '%s\t%s\n' "$PWD" "$*" >> "$GRAPHIFY_LOG"
[ "$#" -eq 2 ] && [ "$1" = hook ] && [ "$2" = install ] || exit 91
case "${GRAPHIFY_DRIVER_MODE:-valid}" in
  valid) git config --local merge.graphify.driver 'graphify merge-driver %O %A %B' ;;
  malformed) git config --local merge.graphify.driver 'graphify merge-driver %O %A' ;;
  missing) : ;;
  *) exit 92 ;;
esac
GRAPHIFY
    chmod +x "$stubdir/uv" "$stubdir/graphify"
    export UV_LOG="$uv_log" GRAPHIFY_LOG="$graphify_log" UV_TOOL_BIN_FIXTURE="$stubdir"
    PATH="$stubdir:$PATH"
    export PATH
  }

  cleanup() { rm -rf "$fixture"; }
  BeforeEach 'setup'
  AfterEach 'cleanup'

  It 'installs Graphify with the Leiden backend and configures the requested Git root'
    When run sh "$script_abs" "$repo"
    The status should equal 0
    The contents of file "$uv_log" should equal 'tool install --upgrade graphifyy[leiden]>=0.9.60'
    The contents of file "$graphify_log" should equal "$(printf '%s\thook install' "$repo")"
    The value "$(git_fixture -C "$repo" config --get merge.graphify.driver)" should include 'graphify merge-driver %O %A %B'
  End

  Context 'driver validation failures'
    Parameters
      'missing'
      'malformed'
    End

    It "fails loudly when the installed driver is $1"
      export GRAPHIFY_DRIVER_MODE="$1"
      When run sh "$script_abs" "$repo"
      The status should equal 1
      The stderr should include 'merge.graphify.driver'
      The stderr should include 'graphify merge-driver %O %A %B'
    End
  End

  It 'uses the off-PATH uv launcher when graphify is not on PATH'
    uv_tool_bin="$fixture/uv tool bin"
    mkdir -p "$uv_tool_bin"
    cp "$stubdir/graphify" "$uv_tool_bin/graphify"
    off_path="$fixture/off path"
    mkdir -p "$off_path"
    for tool in dirname git sh; do
      ln -s "$(command -v "$tool")" "$off_path/$tool"
    done
    ln -s "$stubdir/uv" "$off_path/uv"
    When run env PATH="$off_path" UV_TOOL_BIN_FIXTURE="$uv_tool_bin" \
      UV_PROGRESS_FIXTURE=1 sh "$script_abs" "$repo"
    The status should equal 0
    The stderr should include 'uv progress'
    The contents of file "$graphify_log" should equal "$(printf '%s\thook install' "$repo")"
    The value "$(git_fixture -C "$repo" config --get merge.graphify.driver)" should include 'graphify merge-driver %O %A %B'
  End

  It 'uses the launcher uv installed, not a stale graphify earlier on PATH'
    stale="$fixture/stale"
    mkdir -p "$stale"
    cat > "$stale/graphify" <<'STALE'
#!/bin/sh
printf 'stale\t%s\n' "$*" >> "$GRAPHIFY_LOG"
exit 93
STALE
    chmod +x "$stale/graphify"
    When run env PATH="$stale:$PATH" sh "$script_abs" "$repo"
    The status should equal 0
    The contents of file "$graphify_log" should equal "$(printf '%s\thook install' "$repo")"
  End

  It 'absolutizes a relative uv tool directory before entering the target'
    caller="$fixture/caller"
    mkdir -p "$caller/relbin"
    cp "$stubdir/graphify" "$caller/relbin/graphify"

    # A same-named launcher inside the target must never run.
    decoy_marker="$fixture/decoy-executed"
    mkdir -p "$repo/relbin"
    cat > "$repo/relbin/graphify" <<'DECOY'
#!/bin/sh
touch "$DECOY_MARKER"
"$GIT_TOOL" config --local merge.graphify.driver 'graphify merge-driver %O %A %B'
DECOY
    chmod +x "$repo/relbin/graphify"

    tool_path="$fixture/relative-tools"
    mkdir -p "$tool_path"
    for tool in dirname git touch; do
      ln -s "$(command -v "$tool")" "$tool_path/$tool"
    done
    ln -s "$(command -v dash || command -v sh)" "$tool_path/sh"
    ln -s "$stubdir/uv" "$tool_path/uv"
    When run env PATH="$tool_path" UV_TOOL_BIN_FIXTURE=relbin GIT_TOOL="$tool_path/git" \
      DECOY_MARKER="$decoy_marker" sh -c 'cd "$1" && exec sh "$2" "$3"' _ \
      "$caller" "$script_abs" "$repo"
    The status should equal 0
    The contents of file "$graphify_log" should equal "$(printf '%s\thook install' "$repo")"
    The path "$decoy_marker" should not be exist
  End
End
