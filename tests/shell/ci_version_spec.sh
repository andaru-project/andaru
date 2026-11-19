#shellcheck shell=sh
# The release workflow turns a pushed tag into the image's version and whether the GitHub release is a prerelease.

Describe 'scripts/ci/version.sh'
  script="${SHELLSPEC_PROJECT_ROOT:-$PWD}/scripts/ci/version.sh"

  It 'maps a release tag'
    When run sh "$script" v0.4.0
    The output should equal "0.4.0 false"
  End

  Parameters:dynamic
    for stage in a b rc; do
      %data "$stage"
    done
  End

  It "maps a $1 prerelease"
    When run sh "$script" "v0.4.0-$1.2"
    The output should equal "0.4.0-$1.2 true"
  End

  Parameters
    v0.4
    v1.2.3-rc
    v01.2.3
    v1.2.3-rc01
    v1.2.3-rc.01
    v1.2.3-rc2
    v1.2.3-a1
    v1.2.3-c.1
    v1.2.3-alpha.1
    keyring-2026.11.14
    v1x2x3
    v1x2x3-rc1
    v1.2.3~rc1
  End

  It "refuses $1"
    When run sh "$script" "$1"
    The status should equal 1
    The stderr should include 'not a release tag'
  End
End
