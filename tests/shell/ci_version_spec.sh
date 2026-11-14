#shellcheck shell=sh
# The release workflow turns a pushed tag into what it builds: the package set, the Debian version and whether the
# GitHub release is a prerelease, which the APT repository publishes only to its trixie-test suite.

Describe 'scripts/ci/version.sh'
  script="${SHELLSPEC_PROJECT_ROOT:-$PWD}/scripts/ci/version.sh"

  It 'maps a release tag'
    When run sh "$script" v0.4.0
    The output should equal "any 0.4.0-1 false"
  End

  It 'maps a release candidate below its release'
    When run sh "$script" v0.4.0-rc2
    The output should equal "any 0.4.0~rc2-1 true"
  End

  It 'maps a keyring tag'
    When run sh "$script" keyring-2026.11.14
    The output should equal "all 2026.11.14 false"
  End

  Parameters
    v0.4
    v1.2.3-rc
    v01.2.3
    v1.2.3-rc01
    keyring-2026.1.14
  End

  It "refuses $1"
    When run sh "$script" "$1"
    The status should equal 1
    The stderr should include 'not a release tag'
  End
End
