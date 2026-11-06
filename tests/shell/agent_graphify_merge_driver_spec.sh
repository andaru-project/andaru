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
    # Registration never runs the Graphify CLI: `graphify hook install` would drop
    # rebuild hooks the tracked .githooks retire. Any call trips this stub.
    cat > "$stubdir/graphify" <<'GRAPHIFY'
#!/bin/sh
touch "$GRAPHIFY_CALLED"
exit 91
GRAPHIFY
    real_git=$(command -v git)
    gitstub="$fixture/gitstub"
    mkdir -p "$gitstub"
    cat > "$gitstub/git" <<'GIT'
#!/bin/sh
case "${GIT_STUB_MODE:-}:$*" in
  write:*' config merge.graphify.driver '*) exit 1 ;;
  readback:*' config --local --get merge.graphify.driver') printf '%s\n' 'graphify merge-driver %O %A'; exit 0 ;;
esac
exec "$REAL_GIT" "$@"
GIT
    chmod +x "$stubdir/uv" "$stubdir/graphify" "$gitstub/git"
    graphify_called="$fixture/graphify-called"
    export UV_LOG="$uv_log" UV_TOOL_BIN_FIXTURE="$stubdir" GRAPHIFY_CALLED="$graphify_called"
    PATH="$stubdir:$PATH"
    export PATH
  }

  cleanup() { rm -rf "$fixture"; }
  BeforeEach 'setup'
  AfterEach 'cleanup'

  driver() { git_fixture -C "$repo" config --get merge.graphify.driver; }

  It 'installs Graphify with the Leiden backend and registers its launcher as the union merge driver'
    When run sh "$script_abs" "$repo"
    The status should equal 0
    The contents of file "$uv_log" should equal 'tool install --upgrade graphifyy[leiden]>=0.9.60'
    The value "$(git_fixture -C "$repo" config --get merge.graphify.name)" should equal 'graphify graph.json union merge'
    The result of function driver should equal "\"$stubdir/graphify\" merge-driver %O %A %B"
    The path "$graphify_called" should not be exist
  End

  It 'never runs `graphify hook install`: no rebuild hooks appear in core.hooksPath'
    git_fixture -C "$repo" config core.hooksPath .githooks
    mkdir -p "$repo/.githooks"
    When run sh "$script_abs" "$repo"
    The status should equal 0
    The path "$repo/.githooks/post-commit" should not be exist
    The path "$repo/.githooks/post-checkout" should not be exist
    The path "$graphify_called" should not be exist
  End

  It 'registers the uv-installed launcher, not a stale graphify earlier on PATH'
    uv_tool_bin="$fixture/uv tool bin"
    mkdir -p "$uv_tool_bin"
    cp "$stubdir/graphify" "$uv_tool_bin/graphify"
    When run env UV_TOOL_BIN_FIXTURE="$uv_tool_bin" UV_PROGRESS_FIXTURE=1 sh "$script_abs" "$repo"
    The status should equal 0
    The stderr should include 'uv progress'
    The result of function driver should equal "\"$uv_tool_bin/graphify\" merge-driver %O %A %B"
  End

  It 'absolutizes a relative uv tool directory against the caller, never the target'
    caller="$fixture/caller"
    mkdir -p "$caller/relbin" "$repo/relbin"
    cp "$stubdir/graphify" "$caller/relbin/graphify"
    cp "$stubdir/graphify" "$repo/relbin/graphify"
    When run env UV_TOOL_BIN_FIXTURE=relbin sh -c 'cd "$1" && exec sh "$2" "$3"' _ \
      "$caller" "$script_abs" "$repo"
    The status should equal 0
    The result of function driver should equal "\"$caller/relbin/graphify\" merge-driver %O %A %B"
  End

  Context 'registration failures'
    Parameters
      write    'git refuses to record the driver'
      readback 'the recorded driver reads back malformed'
    End

    It "fails loudly when $2"
      When run env PATH="$gitstub:$PATH" REAL_GIT="$real_git" GIT_STUB_MODE="$1" sh "$script_abs" "$repo"
      The status should equal 1
      The stderr should include 'merge.graphify.driver'
      The stderr should include 'merge-driver %O %A %B'
    End
  End

  Context 'launcher path the driver string cannot carry'
    # git hands the string to sh inside double quotes, and expands %-sequences
    # (%O %A %B) anywhere in it, quoted or not: such a driver registers but never runs.
    Parameters
      "uv\$bin"  'a shell metacharacter'
      'uv%Abin'  'a git %-sequence'
    End

    It "fails closed on $2 in the launcher path"
      uv_tool_bin="$fixture/$1"
      mkdir -p "$uv_tool_bin"
      cp "$stubdir/graphify" "$uv_tool_bin/graphify"
      When run env UV_TOOL_BIN_FIXTURE="$uv_tool_bin" sh "$script_abs" "$repo"
      The status should equal 1
      The stderr should include 'cannot be quoted'
      The value "$(driver)" should equal ''
    End
  End
End
