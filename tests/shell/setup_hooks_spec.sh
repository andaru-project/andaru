#shellcheck shell=sh
# scripts/setup-hooks.sh contributor bootstrap: bootstraps CodeGraph when the CLI
# exists and says so when it does not.
#
# Fixture: a throwaway repo holding its OWN copy of scripts/setup-hooks.sh and
# scripts/agent/*.sh (setup resolves the repo root from its script directory, so
# the real worktree is never touched) and a codegraph stub.

Describe 'setup-hooks.sh contributor bootstrap'
  real_root="${SHELLSPEC_PROJECT_ROOT:-$PWD}"

  setup() {
    export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
    fixture=$(mktemp -d "${TMPDIR:-/tmp}/setup_hooks.XXXXXX") || return 1
    fixture=$(cd "$fixture" && pwd -P) || return 1
    primary="$fixture/primary"
    git_fixture init -q "$primary" &&
      mkdir -p "$primary/scripts/agent" &&
      cp "$real_root/scripts/setup-hooks.sh" "$primary/scripts/setup-hooks.sh" &&
      cp "$real_root/scripts/agent/"*.sh "$primary/scripts/agent/" &&
      git_fixture -C "$primary" -c user.email=t@t -c user.name=t -c commit.gpgsign=false \
        commit -q --allow-empty -m init || return 1
    script_abs="$primary/scripts/setup-hooks.sh"
    stubdir="$fixture/bin"; mkdir -p "$stubdir"
    codegraph_log="$fixture/codegraph.log"
    missing_codegraph_path="$fixture/no-codegraph"; mkdir -p "$missing_codegraph_path"
    for tool in basename cat dirname git mkdir sh tr; do
      ln -s "$(command -v "$tool")" "$missing_codegraph_path/$tool"
    done
    cat > "$stubdir/codegraph" <<'CODEGRAPH'
#!/bin/sh
case "$1" in
  init|index)
    printf '%s\n' "$*" >> "$CODEGRAPH_LOG"
    mkdir -p "$2/.codegraph"
    true > "$2/.codegraph/codegraph.db"
    ;;
  status)
    printf '%s\n' '{"initialized":true,"worktreeMismatch":null,"index":{"reindexRecommended":false,"state":"complete","pendingRefs":0}}'
    ;;
  *) exit 9 ;;
esac
CODEGRAPH
    chmod +x "$stubdir/codegraph"
    export CODEGRAPH_LOG="$codegraph_log"
    PATH="$stubdir:$PATH"; export PATH
  }
  cleanup() {
    rm -rf "$fixture"
    unset GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM CODEGRAPH_LOG
  }
  BeforeEach 'setup'
  AfterEach 'cleanup'

  # Runs the fixture's setup-hooks.sh from outside any repository.
  run_setup() { (cd "$fixture" && exec sh "$script_abs"); }
  twice() {
    run_setup >/dev/null 2>&1 || return 1
    first=$(cat "$codegraph_log")
    run_setup >/dev/null 2>&1 || return 1
    [ "$first" = "$(cat "$codegraph_log")" ]
  }

  It 'initializes CodeGraph in the checkout when the CLI is available'
    When run run_setup
    The status should equal 0
    The stderr should include 'Initializing CodeGraph'
    The contents of file "$codegraph_log" should equal "init $primary"
    Assert [ -f "$primary/.codegraph/codegraph.db" ]
  End

  It 'skips the index when CodeGraph is unavailable'
    When run env PATH="$missing_codegraph_path" sh "$script_abs"
    The status should equal 0
    The stderr should include 'codegraph not found'
    The file "$codegraph_log" should not be exist
  End

  It 'is idempotent: a second run does not re-initialize CodeGraph'
    When run twice
    The status should equal 0
  End
End
