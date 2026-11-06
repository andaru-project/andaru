#shellcheck shell=sh
# Exact-root CodeGraph bootstrap.

Describe 'ensure-codegraph.sh exact-root initialization'
  script_abs="${SHELLSPEC_PROJECT_ROOT:-$PWD}/scripts/agent/ensure-codegraph.sh"

  setup() {
    fixture=$(mktemp -d "${TMPDIR:-/tmp}/codegraph_spec.XXXXXX") || return 1
    fixture=$(cd "$fixture" && pwd -P) || return 1
    primary="$fixture/primary"
    git_fixture init -q "$primary" &&
      git_fixture -C "$primary" -c user.email=t@t -c user.name=t -c commit.gpgsign=false \
        commit -q --allow-empty -m init || return 1
    stubdir="$fixture/bin"; mkdir -p "$stubdir"
    codegraph_log="$fixture/codegraph.log"
    cat > "$stubdir/codegraph" <<'CODEGRAPH'
#!/bin/sh
case "$1" in
  init|index)
    printf '%s\n' "$*" >> "$CODEGRAPH_LOG"
    if [ "${CODEGRAPH_CLEAR_INCOMPLETE:-0}" -eq 1 ]; then rm -f "$CODEGRAPH_INCOMPLETE"; fi
    if [ "${CODEGRAPH_CREATE_DB:-1}" -eq 1 ]; then
      mkdir -p "$2/.codegraph"
      true > "$2/.codegraph/codegraph.db"
    fi
    ;;
  status)
    if [ -f "$CODEGRAPH_INCOMPLETE" ]; then
      printf '%s\n' '{"initialized":true,"worktreeMismatch":null,"index":{"reindexRecommended":true,"state":"partial","pendingRefs":3}}'
    else
      printf '%s\n' '{"initialized":true,"worktreeMismatch":null,"index":{"reindexRecommended":false,"state":"complete","pendingRefs":0}}'
    fi
    ;;
  *) exit 9 ;;
esac
CODEGRAPH
    chmod +x "$stubdir/codegraph"
    # While this file exists, status reports an incomplete index.
    export CODEGRAPH_LOG="$codegraph_log" CODEGRAPH_INCOMPLETE="$fixture/incomplete"
    PATH="$stubdir:$PATH"; export PATH
  }
  cleanup() { rm -rf "$fixture"; }
  BeforeEach 'setup'
  AfterEach 'cleanup'

  It 'initializes the exact checkout root when its database is absent'
    When run sh "$script_abs" "$primary"
    The status should equal 0
    The stderr should include 'Initializing CodeGraph'
    The contents of file "$codegraph_log" should equal "init $primary"
    Assert [ -f "$primary/.codegraph/codegraph.db" ]
  End

  It 'does not reinitialize an exact-root database'
    mkdir -p "$primary/.codegraph"
    true > "$primary/.codegraph/codegraph.db"
    When run sh "$script_abs" "$primary"
    The status should equal 0
    The file "$codegraph_log" should not be exist
  End

  It 'does not accept a parent checkout index for a nested linked worktree'
    mkdir -p "$primary/.codegraph"
    true > "$primary/.codegraph/codegraph.db"
    git_fixture -C "$primary" worktree add -q --detach "$primary/.claude/worktrees/child" HEAD
    child="$primary/.claude/worktrees/child"
    When run sh "$script_abs" "$child"
    The status should equal 0
    The stderr should include 'Initializing CodeGraph'
    The contents of file "$codegraph_log" should equal "init $child"
    Assert [ -f "$child/.codegraph/codegraph.db" ]
  End

  It 'fails when CodeGraph reports success without creating its database'
    When run env CODEGRAPH_CREATE_DB=0 sh "$script_abs" "$primary"
    The status should equal 1
    The stderr should include 'reported success without creating'
  End

  It 'rebuilds an existing index that reports itself incomplete'
    mkdir -p "$primary/.codegraph"
    true > "$primary/.codegraph/codegraph.db"
    true > "$CODEGRAPH_INCOMPLETE"
    When run env CODEGRAPH_CLEAR_INCOMPLETE=1 sh "$script_abs" "$primary"
    The status should equal 0
    The stderr should include 'Rebuilding CodeGraph'
    The contents of file "$codegraph_log" should equal "index $primary"
  End

  It 'fails when the index stays incomplete after a rebuild'
    mkdir -p "$primary/.codegraph"
    true > "$primary/.codegraph/codegraph.db"
    true > "$CODEGRAPH_INCOMPLETE"
    When run sh "$script_abs" "$primary"
    The status should equal 1
    The stderr should include 'did not produce a complete index'
    The contents of file "$codegraph_log" should equal "index $primary"
  End
End
