#shellcheck shell=sh
# Kea runs packaging/kea/andaru-lease-event as _kea on lease events. It marks that leases changed and, for
# DHCPv6 prefix delegation, appends which router each prefix went to: Kea's leases do not record it.

Describe 'andaru-lease-event'
  script="${SHELLSPEC_PROJECT_ROOT:-$PWD}/packaging/kea/andaru-lease-event"
  setup() { run_dir=$(mktemp -d); }
  cleanup() { rm -rf "$run_dir"; }
  BeforeEach 'setup'
  AfterEach 'cleanup'

  # The environment Kea 2.6.3 gave the hook on a Request for an address and a /56 (probed).
  It 'records each delegated prefix with its router and interface, and marks the change'
    When run env ANDARU_KEA_RUN="$run_dir" QUERY6_REMOTE_ADDR=fe80::7c14:26ff:fefd:c799 QUERY6_IFACE_NAME=lan0 \
      LEASES6_SIZE=2 LEASES6_AT0_TYPE=IA_NA LEASES6_AT0_ADDRESS=fd00:a5d:1::100 LEASES6_AT0_PREFIX_LEN=128 \
      LEASES6_AT1_TYPE=IA_PD LEASES6_AT1_ADDRESS=fd00:aa:: LEASES6_AT1_PREFIX_LEN=56 DELETED_LEASES6_SIZE=0 \
      sh "$script" leases6_committed
    The status should equal 0
    The contents of file "$run_dir/andaru-pd-events" should equal "add fd00:aa::/56 fe80::7c14:26ff:fefd:c799 lan0"
    The path "$run_dir/andaru-leases-changed" should be exist
  End

  It 'records a released prefix as deleted'
    When run env ANDARU_KEA_RUN="$run_dir" QUERY6_REMOTE_ADDR=fe80::1 QUERY6_IFACE_NAME=lan0 LEASES6_SIZE=0 \
      DELETED_LEASES6_SIZE=1 DELETED_LEASES6_AT0_TYPE=IA_PD DELETED_LEASES6_AT0_ADDRESS=fd00:aa:: DELETED_LEASES6_AT0_PREFIX_LEN=56 \
      sh "$script" leases6_committed
    The contents of file "$run_dir/andaru-pd-events" should equal "del fd00:aa::/56 fe80::1 lan0"
  End

  It 'appends to a batch Andaru has not taken yet'
    echo 'add fd00:bb::/56 fe80::2 lan0' > "$run_dir/andaru-pd-events"
    When run env ANDARU_KEA_RUN="$run_dir" QUERY6_REMOTE_ADDR=fe80::1 QUERY6_IFACE_NAME=lan0 \
      LEASES6_SIZE=1 LEASES6_AT0_TYPE=IA_PD LEASES6_AT0_ADDRESS=fd00:aa:: LEASES6_AT0_PREFIX_LEN=56 \
      sh "$script" leases6_committed
    The line 1 of contents of file "$run_dir/andaru-pd-events" should equal "add fd00:bb::/56 fe80::2 lan0"
    The line 2 of contents of file "$run_dir/andaru-pd-events" should equal "add fd00:aa::/56 fe80::1 lan0"
  End

  It 'only marks the change for DHCPv4'
    When run env ANDARU_KEA_RUN="$run_dir" sh "$script" leases4_committed
    The path "$run_dir/andaru-pd-events" should not be exist
    The path "$run_dir/andaru-leases-changed" should be exist
  End
End
