#shellcheck shell=sh
# scripts/ci/npm-audit.sh fails on high or critical npm advisories except the ones it lists.

Describe 'npm audit with listed exceptions'
  script="${SHELLSPEC_PROJECT_ROOT:-$PWD}/scripts/ci/npm-audit.sh"
  setup() {
    bin=$(mktemp -d)
    # A stand-in npm that prints the report in $REPORT, as `npm audit --json` does.
    # shellcheck disable=SC2016 # $REPORT expands when the stand-in npm runs.
    printf '#!/bin/sh\ncat "$REPORT"\nexit 1\n' >"$bin/npm"
    chmod +x "$bin/npm"
    REPORT="$bin/report.json"
    export REPORT
  }
  cleanup() { rm -rf "$bin"; }
  BeforeEach 'setup'
  AfterEach 'cleanup'

  advisory() { # $1=url $2=severity
    printf '{"vulnerabilities":{"x":{"via":[{"url":"%s","severity":"%s","name":"x","title":"t"}]}}}' "$1" "$2" >"$REPORT"
  }

  It 'passes when the only high advisory is listed'
    advisory https://github.com/advisories/GHSA-fc5q-39xc-vjpf high
    When run env PATH="$bin:$PATH" sh "$script"
    The status should equal 0
    The output should include 'allowed'
  End

  It 'fails on a high advisory that is not listed'
    advisory https://github.com/advisories/GHSA-0000-0000-0000 high
    When run env PATH="$bin:$PATH" sh "$script"
    The status should equal 1
    The stderr should include 'GHSA-0000-0000-0000'
    The output should include 'no longer applies'
  End

  It 'ignores a moderate advisory'
    advisory https://github.com/advisories/GHSA-0000-0000-0000 moderate
    When run env PATH="$bin:$PATH" sh "$script"
    The status should equal 0
    The output should include 'no longer applies'
  End

  It 'fails when npm audit itself fails'
    printf '{"error":{"code":"ENOTFOUND"}}' >"$REPORT"
    When run env PATH="$bin:$PATH" sh "$script"
    The status should equal 1
    The stderr should include 'npm audit failed'
  End
End
