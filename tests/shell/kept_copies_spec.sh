#shellcheck shell=sh
# ProtectSystem=strict leaves only the unit's state directories writable: the engine records last-lan/ and stores
# recovery/ there, so the kept tree must be one of them.
Describe 'the engine may write the kept copies'
  unit="${SHELLSPEC_PROJECT_ROOT:-$PWD}/packaging/systemd/andaru-engine.service"
  It 'lists andaru-kept among its state directories'
    When run sh -c "grep -E '^StateDirectory=.*(^|[= ])andaru-kept( |$)' '$unit'"
    The status should equal 0
    The output should include 'andaru-kept'
  End
End
