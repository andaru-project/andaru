#shellcheck shell=sh
# .githooks/pre-commit root-graph refresh: a commit that stages any path other than
# graphify-out/graph.json rebuilds graphify-out/graph.json in place through
# scripts/agent/check-graph-fresh.sh --refresh and stages the result, so the commit
# and its graph always agree. Only the graph itself staged, or nothing staged,
# rebuilds nothing. A rebase/merge/cherry-pick in progress changes nothing because
# the rebuild is synchronous. A failed, tool-less, or missing checker fails the commit.

Describe '.githooks/pre-commit root-graph refresh'
  gitc() { git_fixture -C "$repo" "$@"; }

  make_repo() {
    export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
    repo="$(mktemp -d "${SHELLSPEC_TMPBASE:-/tmp}/precommit-graph.XXXXXX")"
    git_fixture -C "$repo" init -q
    gitc config user.name t
    gitc config user.email t@t
    gitc config commit.gpgsign false
    mkdir -p "$repo/.githooks" "$repo/src" "$repo/scripts/agent" "$repo/graphify-out"
    cp "$SHELLSPEC_PROJECT_ROOT/.githooks/pre-commit" "$repo/.githooks/pre-commit"
    check_log="$repo/check-graph-fresh.log"
    export GRAPH_CHECK_LOG="$check_log"
    cat > "$repo/scripts/agent/check-graph-fresh.sh" <<'CHECK'
#!/bin/sh
printf '%s\n' "$*" >> "$GRAPH_CHECK_LOG"
[ "${GRAPH_CHECK_RC:-0}" -eq 0 ] || exit "$GRAPH_CHECK_RC"
printf 'rebuilt graph\n' > graphify-out/graph.json
CHECK
    printf 'committed graph\n' > "$repo/graphify-out/graph.json"
    gitc add graphify-out/graph.json
    gitc commit -q -m seed --no-verify
    printf 'content\n' > "$repo/src/ok.txt"
  }
  cleanup() {
    rm -rf "$repo"
    unset GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GRAPH_CHECK_LOG GRAPH_CHECK_RC
  }
  Before 'make_repo'
  After 'cleanup'

  staged_graph() { gitc diff --cached --name-only -- graphify-out/graph.json; }

  It 'rebuilds the graph in place and stages it when a path other than the graph is staged'
    gitc add src/ok.txt
    When run sh -c "cd '$repo' && sh .githooks/pre-commit"
    The status should equal 0
    The output should include '[pre-commit] graph freshness'
    The contents of file "$check_log" should equal '--refresh'
    The contents of file "$repo/graphify-out/graph.json" should equal 'rebuilt graph'
    The result of function staged_graph should equal 'graphify-out/graph.json'
  End

  It 'rebuilds nothing when only graphify-out/graph.json itself is staged'
    printf 'hand-refreshed graph\n' > "$repo/graphify-out/graph.json"
    gitc add graphify-out/graph.json
    When run sh -c "cd '$repo' && sh .githooks/pre-commit"
    The status should equal 0
    The output should equal ''
    The file "$check_log" should not be exist
    The contents of file "$repo/graphify-out/graph.json" should equal 'hand-refreshed graph'
  End

  It 'rebuilds when a staged deletion is the only change'
    gitc add src/ok.txt
    gitc commit -q -m add --no-verify
    gitc rm -q src/ok.txt
    When run sh -c "cd '$repo' && sh .githooks/pre-commit"
    The status should equal 0
    The output should include '[pre-commit] graph freshness'
    The result of function staged_graph should equal 'graphify-out/graph.json'
  End

  It 'rebuilds when the lone staged path merely SPLITS into graph.json lines: the guard is decided NUL-safe'
    mkdir -p "$repo/graphify-out/graph.json
graphify-out"
    printf 'not the graph\n' > "$repo/graphify-out/graph.json
graphify-out/graph.json"
    gitc add -- "graphify-out/graph.json
graphify-out/graph.json"
    When run sh -c "cd '$repo' && sh .githooks/pre-commit"
    The status should equal 0
    The output should include '[pre-commit] graph freshness'
    The result of function staged_graph should equal 'graphify-out/graph.json'
  End

  It 'rebuilds nothing with an empty index'
    When run sh -c "cd '$repo' && sh .githooks/pre-commit"
    The status should equal 0
    The output should equal ''
    The file "$check_log" should not be exist
  End

  Context 'commit inside an in-progress git operation'
    Parameters
      rebase-merge     dir
      rebase-apply     dir
      MERGE_HEAD       file
      CHERRY_PICK_HEAD file
    End

    It "still rebuilds and stages the graph with $1 present"
      case "$2" in
        dir) mkdir -p "$repo/.git/$1" ;;
        file) true > "$repo/.git/$1" ;;
      esac
      gitc add src/ok.txt
      When run sh -c "cd '$repo' && sh .githooks/pre-commit"
      The status should equal 0
      The output should include '[pre-commit] graph freshness'
      The result of function staged_graph should equal 'graphify-out/graph.json'
    End
  End

  Context 'checker failures fail the commit and stage nothing'
    Parameters
      1 'a failed rebuild'
      4 'a missing Graphify'
    End

    It "fails on $2 (exit $1)"
      export GRAPH_CHECK_RC="$1"
      gitc add src/ok.txt
      When run sh -c "cd '$repo' && sh .githooks/pre-commit"
      The status should equal 1
      The output should include '[pre-commit] graph freshness'
      The stderr should include '[pre-commit] FAILED: graph freshness'
      The result of function staged_graph should equal ''
    End
  End

  It 'fails a tree that ships no checker'
    rm -f "$repo/scripts/agent/check-graph-fresh.sh"
    gitc add src/ok.txt
    When run sh -c "cd '$repo' && sh .githooks/pre-commit"
    The status should equal 1
    The output should include '[pre-commit] graph freshness'
    The stderr should include '[pre-commit] FAILED: graph freshness'
  End
End
