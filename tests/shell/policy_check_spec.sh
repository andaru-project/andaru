#shellcheck shell=sh
# scripts/ci/policy-check.jq is what image-smoke.sh holds the image's signature policy to: it must pass the
# repository's policy and fail every way of weakening which key signs which repository.

Describe 'scripts/ci/policy-check.jq'
  root="${SHELLSPEC_PROJECT_ROOT:-$PWD}"
  ns=ghcr.io/andaru-project
  # 1 is the check's verdict "not Andaru's policy"; a broken mutation or a missing jq gives another status.
  check() {
    policy=$(sed "s|@NAMESPACE@|$ns|" "$root/image/signing/policy.json" | jq "$1") || return 99
    printf '%s\n' "$policy" | jq -e --arg ns "$ns" -f "$root/scripts/ci/policy-check.jq" >/dev/null
  }

  It 'passes the repository policy'
    When call check .
    The status should equal 0
  End

  Parameters
    '.default = [{"type": "insecureAcceptAnything"}]'
    '.transports.docker["ghcr.io/andaru-project"][0].keyPath = "/etc/pki/containers/andaru-nightly.pub"'
    '.transports.docker["ghcr.io/andaru-project/andaru-nightly"][0].keyPath = "/etc/pki/containers/andaru.pub"'
    '.transports.docker["ghcr.io/andaru-project/andaru-nightly"] = [{"type": "insecureAcceptAnything"}]'
    '.transports.docker["ghcr.io/andaru-project/andaru"] = [{"type": "insecureAcceptAnything"}]'
    '.transports.docker["ghcr.io/andaru-project"] += [{"type": "sigstoreSigned", "keyPath": "/etc/pki/containers/andaru-nightly.pub", "signedIdentity": {"type": "matchRepository"}}]'
    'del(.transports.docker["ghcr.io/andaru-project/andaru-nightly"])'
    '.transports.docker["ghcr.io/andaru-project"][0].signedIdentity = {"type": "matchRepoDigestOrExact"}'
    '.transports.docker["docker.io"] = [{"type": "sigstoreSigned", "keyPath": "/etc/pki/containers/andaru-nightly.pub", "signedIdentity": {"type": "matchRepository"}}]'
  End

  It "fails a weakened policy: $1"
    When call check "$1"
    The status should equal 1
  End
End
