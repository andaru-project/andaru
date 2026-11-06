#shellcheck shell=sh
# scripts/setup-hooks.sh contributor bootstrap: installs Graphify (never running
# it), registers the Graphify merge driver, activates core.hooksPath .githooks,
# strips Graphify's own post-commit/post-checkout/post-merge/post-rewrite blocks
# from <git-common-dir>/hooks and .githooks, then bootstraps CodeGraph when the
# CLI exists. A missing uv exits 4 before anything is activated.
#
# Fixture: a throwaway repo holding its OWN copy of scripts/setup-hooks.sh and
# scripts/agent/*.sh (setup resolves the repo root from its script directory, so
# the real worktree is never touched), a uv stub that records the exact install
# and tool-dir calls, and a codegraph stub. Restricted PATHs hold symlinks to the
# few real tools setup needs.

Describe 'setup-hooks.sh contributor bootstrap'
  real_root="${SHELLSPEC_PROJECT_ROOT:-$PWD}"

  setup() {
    export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
    fixture=$(mktemp -d "${TMPDIR:-/tmp}/setup_hooks.XXXXXX") || return 1
    fixture=$(cd "$fixture" && pwd -P) || return 1
    primary="$fixture/primary"
    git_fixture init -q "$primary" &&
      mkdir -p "$primary/.githooks" "$primary/scripts/agent" &&
      true > "$primary/.githooks/pre-commit" &&
      cp "$real_root/scripts/setup-hooks.sh" "$primary/scripts/setup-hooks.sh" &&
      cp "$real_root/scripts/agent/"*.sh "$primary/scripts/agent/" &&
      git_fixture -C "$primary" -c user.email=t@t -c user.name=t -c commit.gpgsign=false \
        commit -q --allow-empty -m init || return 1
    script_abs="$primary/scripts/setup-hooks.sh"
    stubdir="$fixture/bin"; mkdir -p "$stubdir"
    codegraph_log="$fixture/codegraph.log"
    install_log="$fixture/install.log"
    graphify_log="$fixture/graphify.log"
    true > "$graphify_log"
    missing_codegraph_path="$fixture/no-codegraph"; mkdir -p "$missing_codegraph_path"
    missing_uv_path="$fixture/no-uv"; mkdir -p "$missing_uv_path"
    for tool in awk basename cat chmod dirname git grep mkdir mv rm sed sh tr; do
      ln -s "$(command -v "$tool")" "$missing_codegraph_path/$tool"
      ln -s "$(command -v "$tool")" "$missing_uv_path/$tool"
    done
    cat > "$stubdir/uv" <<'UV'
#!/bin/sh
case "$*" in
  'tool install --upgrade graphifyy[leiden]>=0.9.60')
    printf '%s\n' "$*" >> "$INSTALL_LOG"
    mkdir -p "$UV_TOOL_BIN_FIXTURE"
    printf '%s\n' '#!/bin/sh' 'printf %s\\n "$*" >> "$GRAPHIFY_LOG"' 'exit 0' > "$UV_TOOL_BIN_FIXTURE/graphify"
    chmod +x "$UV_TOOL_BIN_FIXTURE/graphify"
    ;;
  'tool dir --bin') printf '%s\n' "$UV_TOOL_BIN_FIXTURE" ;;
  *) exit 9 ;;
esac
UV
    ln -s "$stubdir/uv" "$missing_codegraph_path/uv"
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
    chmod +x "$stubdir/uv" "$stubdir/codegraph"
    uv_tool_bin_fixture="$fixture/default uv tool bin"
    export CODEGRAPH_LOG="$codegraph_log" INSTALL_LOG="$install_log" \
      GRAPHIFY_LOG="$graphify_log" UV_TOOL_BIN_FIXTURE="$uv_tool_bin_fixture"
    PATH="$stubdir:$PATH"; export PATH
  }
  cleanup() {
    rm -rf "$fixture"
    unset GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM CODEGRAPH_LOG INSTALL_LOG GRAPHIFY_LOG UV_TOOL_BIN_FIXTURE
  }
  BeforeEach 'setup'
  AfterEach 'cleanup'

  # Runs the fixture's setup-hooks.sh from outside any repository.
  run_setup() { (cd "$fixture" && exec sh "$script_abs"); }
  hooks_path() { git_fixture -C "$primary" config --get core.hooksPath 2>/dev/null || true; }
  # Every config line plus the hooks directory listing: what a second run must not change.
  state() {
    git_fixture -C "$primary" config --local --list
    ls -A "$primary/.git/hooks" "$primary/.githooks"
  }
  twice() {
    run_setup >/dev/null 2>&1 || return 1
    first=$(state)
    run_setup >/dev/null 2>&1 || return 1
    [ "$first" = "$(state)" ]
  }

  It 'initializes CodeGraph in the checkout when the CLI is available'
    When run run_setup
    The status should equal 0
    The output should include 'core.hooksPath set to: .githooks'
    The stderr should include 'Initializing CodeGraph'
    The contents of file "$codegraph_log" should equal "init $primary"
    The contents of file "$install_log" should equal 'tool install --upgrade graphifyy[leiden]>=0.9.60'
    Assert [ -f "$primary/.codegraph/codegraph.db" ]
  End

  It 'still activates Git hooks when CodeGraph is unavailable'
    When run env PATH="$missing_codegraph_path" sh "$script_abs"
    The status should equal 0
    The output should include 'core.hooksPath set to: .githooks'
    The stderr should include 'codegraph not found'
    The contents of file "$install_log" should equal 'tool install --upgrade graphifyy[leiden]>=0.9.60'
    The file "$codegraph_log" should not be exist
  End

  It 'resolves the uv launcher when its bin directory is absent from PATH'
    uv_tool_bin="$fixture/uv tool bin"
    When run env PATH="$missing_codegraph_path" UV_TOOL_BIN_FIXTURE="$uv_tool_bin" sh "$script_abs"
    The status should equal 0
    The stderr should include 'codegraph not found'
    The output should include 'core.hooksPath set to: .githooks'
    The value "$(hooks_path)" should equal '.githooks'
    The value "$(git_fixture -C "$primary" config --get merge.graphify.driver)" should equal "\"$uv_tool_bin/graphify\" merge-driver %O %A %B"
  End

  It 'fails with TOOL-MISSING before activating hooks when uv is unavailable'
    When run env PATH="$missing_uv_path" sh "$script_abs"
    The status should equal 4
    The stderr should include 'TOOL-MISSING: uv'
    The value "$(hooks_path)" should equal ''
    The file "$install_log" should not be exist
  End

  It 'registers the Graphify merge driver without ever running graphify'
    When run run_setup
    The status should equal 0
    The stderr should include 'Initializing CodeGraph'
    The output should include 'core.hooksPath set to: .githooks'
    The value "$(git_fixture -C "$primary" config --get merge.graphify.name)" should equal 'graphify graph.json union merge'
    The value "$(git_fixture -C "$primary" config --get merge.graphify.driver)" should equal "\"$uv_tool_bin_fixture/graphify\" merge-driver %O %A %B"
    The contents of file "$graphify_log" should equal ''
  End

  It 'is idempotent: a second run succeeds and changes neither config nor hooks'
    When run twice
    The status should equal 0
  End

  It 'declares the tracked graph merge=graphify and diff unset in the real repository'
    When run git_fixture -C "$real_root" check-attr merge diff -- graphify-out/graph.json
    The status should equal 0
    The line 1 of output should equal 'graphify-out/graph.json: merge: graphify'
    The line 2 of output should equal 'graphify-out/graph.json: diff: unset'
  End

  Describe 'Graphify hook block stripping'
    strip_run() { env PATH="$missing_codegraph_path" sh "$script_abs"; }
    hook() { printf '%s\n' "$@" > "$primary/.git/hooks/$hook_name"; chmod +x "$primary/.git/hooks/$hook_name"; }

    It 'removes leftover Graphify hooks from the git hooks dir and .githooks, never running graphify hook install'
      # `graphify hook uninstall` would also drop merge.graphify.driver, so setup
      # strips the blocks itself.
      hook_name=post-commit
      hook '#!/bin/sh' '# graphify-hook-start' 'echo leftover-commit' '# graphify-hook-end'
      printf '%s\n' '#!/bin/sh' '# graphify-checkout-hook-start' 'echo leftover-checkout' '# graphify-checkout-hook-end' > "$primary/.git/hooks/post-checkout"
      printf '%s\n' '#!/bin/sh' '# graphify-hook-start' 'echo hooksPath-commit' '# graphify-hook-end' > "$primary/.githooks/post-commit"
      chmod +x "$primary/.git/hooks/post-checkout" "$primary/.githooks/post-commit"
      When run strip_run
      The status should equal 0
      The stderr should include 'codegraph not found'
      The output should include 'core.hooksPath set to: .githooks'
      The path "$primary/.git/hooks/post-commit" should not be exist
      The path "$primary/.git/hooks/post-checkout" should not be exist
      The path "$primary/.githooks/post-commit" should not be exist
      The path "$primary/.githooks/pre-commit" should be exist
      The contents of file "$graphify_log" should equal ''
      The value "$(git_fixture -C "$primary" config --get merge.graphify.driver)" should include 'merge-driver %O %A %B'
    End

    It 'removes a lone post-merge block and keeps the merge driver registered'
      hook_name=post-merge
      hook '#!/bin/sh' '# graphify-merge-hook-start' 'echo leftover-merge' '# graphify-merge-hook-end'
      When run strip_run
      The status should equal 0
      The stderr should include 'codegraph not found'
      The output should include 'core.hooksPath set to: .githooks'
      The path "$primary/.git/hooks/post-merge" should not be exist
      The value "$(git_fixture -C "$primary" config --get merge.graphify.driver)" should include 'merge-driver %O %A %B'
    End

    It 'leaves a Graphify start marker without its end marker untouched'
      hook_name=post-commit
      hook '#!/bin/sh' 'echo keep-me' '# graphify-hook-start' 'echo truncated'
      When run strip_run
      The status should equal 0
      The stderr should include 'codegraph not found'
      The output should include 'core.hooksPath set to: .githooks'
      The contents of file "$primary/.git/hooks/post-commit" should include 'echo keep-me'
      The contents of file "$primary/.git/hooks/post-commit" should include 'echo truncated'
    End

    It 'leaves a start marker after an earlier end marker untouched'
      hook_name=post-commit
      hook '#!/bin/sh' 'echo keep-me' '# graphify-hook-end' 'echo after-end' '# graphify-hook-start' 'echo dangling-start'
      When run strip_run
      The status should equal 0
      The stderr should include 'codegraph not found'
      The output should include 'core.hooksPath set to: .githooks'
      The contents of file "$primary/.git/hooks/post-commit" should include 'echo dangling-start'
      The contents of file "$primary/.git/hooks/post-commit" should include 'echo after-end'
    End

    It 'keeps execute permission on a retained mixed post-commit hook'
      hook_name=post-commit
      hook '#!/bin/sh' 'echo custom' '# graphify-hook-start' 'echo g' '# graphify-hook-end'
      When run strip_run
      The status should equal 0
      The stderr should include 'codegraph not found'
      The output should include 'core.hooksPath set to: .githooks'
      The contents of file "$primary/.git/hooks/post-commit" should include 'echo custom'
      The contents of file "$primary/.git/hooks/post-commit" should not include 'echo g'
      The path "$primary/.git/hooks/post-commit" should be executable
    End

    It 'keeps custom text after a complete Graphify block and a dangling start'
      hook_name=post-commit
      hook '#!/bin/sh' 'echo keep-me' '# graphify-hook-start' 'echo g' '# graphify-hook-end' '# graphify-hook-start' 'echo dangling-after-complete'
      When run strip_run
      The status should equal 0
      The stderr should include 'codegraph not found'
      The output should include 'core.hooksPath set to: .githooks'
      The contents of file "$primary/.git/hooks/post-commit" should include 'echo keep-me'
      The contents of file "$primary/.git/hooks/post-commit" should include 'echo dangling-after-complete'
      The contents of file "$primary/.git/hooks/post-commit" should not include 'echo g'
    End

    It 'keeps a later hashbang line after stripping a complete Graphify block'
      hook_name=post-commit
      hook '#!/bin/sh' '# graphify-hook-start' 'echo g' '# graphify-hook-end' '#!keep-me-hashbang-line'
      When run strip_run
      The status should equal 0
      The stderr should include 'codegraph not found'
      The output should include 'core.hooksPath set to: .githooks'
      The path "$primary/.git/hooks/post-commit" should be exist
      The contents of file "$primary/.git/hooks/post-commit" should include '#!keep-me-hashbang-line'
      The contents of file "$primary/.git/hooks/post-commit" should not include 'echo g'
    End
  End
End
