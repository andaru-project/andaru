#shellcheck shell=sh
# The ISO's installer (image/iso/andaru-install): each step against captures of the live system, and the seed it
# writes against docs/dev/install-seed.md's fixtures, byte for byte.

Describe 'andaru-install'
  root="${SHELLSPEC_PROJECT_ROOT:-$PWD}"
  data="$root/tests/shell/installer"
  seeds="$root/image/iso/testdata"
  Include "$root/image/iso/andaru-install"

  ed25519='ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIO+VXyljs/lkyfnto5CqJDJaSFTQana6UuzepSCNrhwa admin@laptop'

  Describe 'IPv4 answers'
    Parameters
      192.168.1.1/24 success
      10.0.0.0/31 success
      10.0.0.1/32 success
      192.168.01.1/24 failure
      192.168.1.0/24 failure
      192.168.1.255/24 failure
      127.0.0.1/8 failure
      224.0.0.1/24 failure
      0.0.0.0/8 failure
      192.168.1.1/0 failure
      192.168.1.1/33 failure
      192.168.1.1/024 failure
      192.168.1.1 failure
      1.2.3/24 failure
      256.1.1.1/24 failure
    End
    It "holds $1 to the validator's rules for a host address"
      When call ip4_local "$1"
      The status should be "$2"
    End
  End

  It 'refuses the IPv4 broadcast address as a gateway'
    When call ip4_unicast 255.255.255.255
    The status should be failure
  End

  Describe 'IPv6 parsing'
    Parameters
      2001:db8::1 20010db8000000000000000000000001
      :: 00000000000000000000000000000000
      1:2:3:4:5:6:7:: 00010002000300040005000600070000
      ::ffff:1.2.3.4 00000000000000000000ffff01020304
      FD00:1::A fd00000100000000000000000000000a
    End
    It "expands $1"
      When call ip6_hex "$1"
      The output should equal "$2"
    End
  End

  Describe 'IPv6 answers'
    Parameters
      fd00:1::1/64 success
      2001:db8::1/128 success
      ::/64 failure
      ::1/128 failure
      ff02::1/64 failure
      ::ffff:1.2.3.4/96 failure
      fe80::1%eth0/64 failure
      1:2:3:4:5:6:7:8:9/64 failure
      1::2::3/64 failure
      12345::1/64 failure
      ::1.2.03.4/64 failure
      2001:db8::1/129 failure
      2001:db8::1/0 failure
    End
    It "holds $1 to the validator's rules"
      When call ip6_local "$1"
      The status should be "$2"
    End
  End

  Describe 'DNS servers'
    Parameters
      '203.0.113.53 2001:db8:1::53' success
      '169.254.1.1' failure
      'fe80::1' failure
      '2001:db8::1 2001:db8:0::1' failure
      '1.1.1.1 1.1.1.1' failure
      '1.1.1.1 1.0.0.1 8.8.8.8 8.8.4.4 9.9.9.9 149.112.112.112 208.67.222.222 208.67.220.220 4.2.2.2' failure
      '' failure
    End
    It "checks '$1'"
      When call dns_servers "$1"
      The status should be "$2"
    End
  End

  Describe 'DUIDs'
    Parameters
      00:04:aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99 success
      00:01:00:01:aa:bb:cc:dd:ee success
      00:03:00:01:aa success
      00:04:aa:bb failure
      00:05:01:02:03:04 failure
      00:02:aa:bb:cc:dd failure
      0004aabb failure
    End
    It "checks $1"
      When call duid "$1"
      The status should be "$2"
    End
  End

  Describe 'subnet IDs'
    Parameters
      0 56 success
      ff 56 success
      100 56 failure
      f 60 success
      10 60 failure
      0 64 success
      1 64 failure
      ffff 48 success
      12345 48 failure
      g 56 failure
    End
    It "checks $1 within a /$2"
      When call subnet_id "$1" "$2"
      The status should be "$3"
    End
  End

  Describe 'SSH keys'
    It 'splits a key into type, blob and comment'
      When call ssh_key "$ed25519"
      The output should equal 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIO+VXyljs/lkyfnto5CqJDJaSFTQana6UuzepSCNrhwa admin@laptop'
    End
    It 'takes a key with no comment'
      When call ssh_key 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIO+VXyljs/lkyfnto5CqJDJaSFTQana6UuzepSCNrhwa'
      The output should equal 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIO+VXyljs/lkyfnto5CqJDJaSFTQana6UuzepSCNrhwa '
    End
    It 'takes an RSA key of 2048 bits'
      When call ssh_key "$(cat "$data/rsa-2048.pub")"
      The status should be success
      The output should start with 'ssh-rsa '
    End
    It 'refuses an RSA key of 1024 bits'
      When call ssh_key "$(cat "$data/rsa-1024.pub")"
      The status should be failure
    End
    It 'refuses a private key'
      When call ssh_key '-----BEGIN OPENSSH PRIVATE KEY-----'
      The status should be failure
    End
    It 'refuses a key with options'
      When call ssh_key "no-pty $ed25519"
      The status should be failure
    End
    It 'refuses two keys'
      When call ssh_key "$ed25519
$ed25519"
      The status should be failure
    End
    It 'refuses control bytes in the comment'
      When call ssh_key "$ed25519$(printf '\033')[2J"
      The status should be failure
    End
    It 'refuses a blob ssh-keygen cannot read'
      When call ssh_key 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA admin@laptop'
      The status should be failure
    End
  End

  Describe 'key files'
    setup() { work=$(mktemp -d); }
    cleanup() { rm -rf "$work"; }
    BeforeEach 'setup'
    AfterEach 'cleanup'

    It 'reads a regular file'
      printf '%s\n' "$ed25519" >"$work/andaru-ssh.pub"
      When call read_key_file "$work/andaru-ssh.pub"
      The output should equal "$ed25519"
    End
    It 'refuses a link'
      printf '%s\n' "$ed25519" >"$work/real"
      ln -s real "$work/andaru-ssh.pub"
      When call read_key_file "$work/andaru-ssh.pub"
      The status should be failure
    End
    It 'refuses a FIFO'
      mkfifo "$work/andaru-ssh.pub"
      When call read_key_file "$work/andaru-ssh.pub"
      The status should be failure
    End

    # The media are directories named after their devices; "mounting" one copies it.
    mount_ro() { [ -d "$work/media/${1##*/}" ] && cp -R "$work/media/${1##*/}/." "$2/"; }
    umount() { find "$1" -mindepth 1 -delete; }
    It 'finds the file on media other than the boot medium and the target'
      mkdir -p "$work/media/sde1" "$work/media/sdb2" "$work/out"
      printf '%s\n' "$ed25519" >"$work/media/sde1/andaru-ssh.pub"
      printf '%s\n' "$ed25519" >"$work/media/sdb2/andaru-ssh.pub"
      When call eval 'cd "$work/out" && key_files "$(cat "$data/lsblk-stick.json")" sdb sda'
      The output should equal /dev/sde1
      The contents of file "$work/out/key.0" should equal "$ed25519"
    End
    It 'reports every medium carrying one'
      mkdir -p "$work/media/sde1" "$work/media/sdd4" "$work/out"
      printf '%s\n' "$ed25519" >"$work/media/sde1/andaru-ssh.pub"
      printf '%s\n' "$ed25519" >"$work/media/sdd4/andaru-ssh.pub"
      When call eval 'cd "$work/out" && key_files "$(cat "$data/lsblk-stray-boot.json")" sdb sda'
      The line 1 of output should equal /dev/sde1
      The line 2 of output should equal /dev/sdd4
    End
  End

  Describe 'disks'
    stick=$(cat "$data/lsblk-stick.json")
    It 'finds the boot medium from a partition of an isohybrid stick'
      When call boot_disk "$stick" /dev/sdb1
      The output should equal sdb
    End
    It 'finds the boot medium from an optical drive'
      When call boot_disk "$stick" /dev/sr0
      The output should equal sr0
    End
    It 'lists writable disks of 8 GB or more, never the boot medium, a read-only disk or zram'
      When call disks "$stick" sdb
      The output should equal 'sda /dev/sda 32000000000 QEMU_HARDDISK
vdb /dev/vdb 12884901888 '
    End
    It 'refuses a disk other than the target with a filesystem labelled boot'
      When call boot_labels "$(cat "$data/lsblk-stray-boot.json")" sda
      The output should equal /dev/sdd3
    End
    It 'accepts a boot label on the target itself'
      When call boot_labels "$(cat "$data/lsblk-stray-boot.json")" sdd
      The output should equal ''
    End
    It 'reads andaru.disk= from the kernel command line'
      CMDLINE="$data/cmdline"
      When call cmdline_disk
      The output should equal /dev/disk/by-id/ata-QEMU_HARDDISK_QM00001
    End
    It 'resolves andaru.disk= through a link to a listed disk'
      link=$(mktemp -u)
      ln -s /dev/sda "$link"
      When call resolve_disk "$link" "$(disks "$stick" sdb)"
      The output should equal 'sda /dev/sda 32000000000 QEMU_HARDDISK'
      rm -f "$link"
    End
    It 'refuses andaru.disk= naming the boot medium'
      When call resolve_disk /dev/sdb "$(disks "$stick" sdb)"
      The status should be failure
    End
    It 'refuses andaru.disk= naming a disk under 8 GB'
      When call resolve_disk /dev/vda "$(disks "$stick" sdb)"
      The status should be failure
    End
  End

  Describe 'NICs'
    It 'lists physical Ethernet ports with their permanent MAC, lowercased'
      When call nics "$(cat "$data/ip-link.json")"
      The output should equal 'enp1s0 52:54:00:aa:bb:01
enp2s0 52:54:00:aa:bb:02'
    End
  End

  Describe 'the seed'
    BeforeEach reset_answers
    k() { KEY_TYPE=ssh-ed25519 KEY_BLOB=AAAAC3NzaC1lZDI1NTE5AAAAIO+VXyljs/lkyfnto5CqJDJaSFTQana6UuzepSCNrhwa KEY_COMMENT=admin@laptop; }
    mgmt_static() { M_MAC=52:54:00:aa:bb:01 M4_MODE=static M4_ADDR=192.168.1.1/24 M6_MODE=none; }
    serving() { DHCP_START=192.168.1.100 DHCP_END=192.168.1.199 SERVE_DNS=true; }
    wan_dhcp() { W_MAC=52:54:00:aa:bb:02 W4_MODE=dhcp W6_MODE=none; }

    It 'matches static-management-serving.json'
      mgmt_static; serving; wan_dhcp; k
      When call seed_json
      The output should equal "$(cat "$seeds/static-management-serving.json")"
    End
    It 'matches dhcp-management.json'
      M_MAC=52:54:00:aa:bb:01 M4_MODE=dhcp M6_MODE=none; wan_dhcp; k
      When call seed_json
      The output should equal "$(cat "$seeds/dhcp-management.json")"
    End
    It 'matches ipv6-only-management.json'
      M_MAC=52:54:00:aa:bb:01 M4_MODE=none M6_MODE=static M6_ADDR=fd00:1::1/64 M6_RA=unmanaged
      W_MAC=52:54:00:aa:bb:02 W4_MODE=dhcp W6_MODE=dhcp6; k
      When call seed_json
      The output should equal "$(cat "$seeds/ipv6-only-management.json")"
    End
    It 'matches delegated-management.json'
      mgmt_static; SERVE_DNS=true M6_MODE=delegated M6_SUBNET=0 M6_RA=unmanaged
      W_MAC=52:54:00:aa:bb:02 W4_MODE=dhcp W6_MODE=dhcp6 W6_REQUEST=56; k
      When call seed_json
      The output should equal "$(cat "$seeds/delegated-management.json")"
    End
    It 'matches static-wan.json'
      mgmt_static; serving; k
      W_MAC=52:54:00:aa:bb:02 W4_MODE=static W4_ADDR=203.0.113.2/24 W4_GW=203.0.113.1
      W6_MODE=static W6_ADDR=2001:db8:1::2/64 W6_GW=2001:db8:1::1 W_DNS='203.0.113.53 2001:db8:1::53'
      When call seed_json
      The output should equal "$(cat "$seeds/static-wan.json")"
    End
    It 'matches slaac-wan.json'
      mgmt_static; SERVE_DNS=true; k
      W_MAC=52:54:00:aa:bb:02 W4_MODE=none W6_MODE=slaac
      When call seed_json
      The output should equal "$(cat "$seeds/slaac-wan.json")"
    End
    It 'matches dhcp6-advanced-wan.json'
      mgmt_static; SERVE_DNS=false; k
      W_MAC=52:54:00:aa:bb:02 W4_MODE=dhcp W6_MODE=dhcp6 W6_REQUEST=60 W6_PREFIX_ONLY=true W6_WITHOUT_RA=true
      W6_SEND_RELEASE=false W6_DUID=00:04:aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99
      When call seed_json
      The output should equal "$(cat "$seeds/dhcp6-advanced-wan.json")"
    End
    It 'matches no-wan.json'
      mgmt_static; serving; k
      When call seed_json
      The output should equal "$(cat "$seeds/no-wan.json")"
    End
    It 'matches no-key.json'
      mgmt_static; serving; wan_dhcp
      When call seed_json
      The output should equal "$(cat "$seeds/no-key.json")"
    End
  End

  # The screens, driven through ui() with scripted answers, one per line; "<cancel>" cancels, and a yes/no question
  # takes "yes" or "no".
  Describe 'the questions'
    setup() {
      work=$(mktemp -d)
      mkdir -p "$work/sys/class/net/enp1s0" "$work/sys/class/net/enp2s0"
      echo 1 >"$work/sys/class/net/enp1s0/carrier"
      SYS=$work/sys CMDLINE=$work/cmdline
      echo 'console=tty0' >"$CMDLINE"
    }
    cleanup() { rm -rf "$work"; }
    BeforeEach 'setup'
    AfterEach 'cleanup'
    ui() {
      answer=$(sed -n 1p "$work/answers")
      sed -i 1d "$work/answers"
      case $answer in '<cancel>') return 1 ;; esac
      case " $* " in *" --yesno "*) [ "$answer" = yes ]; return ;; esac
      printf '%s\n' "$answer"
    }
    say() { printf '%s\n' "$1" >>"$work/said"; }
    lsblk_json() { cat "$data/lsblk-stick.json"; }
    boot_source() { echo /dev/sdb1; }
    ip_links() { cat "$data/ip-link.json"; }
    ip() { :; }
    sleep() { :; }
    answers() { printf '%s\n' "$@" >"$work/answers"; }

    It 'collects static-management-serving.json'
      answers sda enp1s0 enp2s0 dhcp none static 192.168.1.1/24 yes 192.168.1.100 192.168.1.199 yes none \
        paste "$ed25519" yes sda
      When call eval 'questions && seed_json'
      The output should equal "$(cat "$seeds/static-management-serving.json")"
    End
    It 'collects delegated-management.json, asking again after a refused subnet ID'
      answers sda enp1s0 enp2s0 dhcp dhcp6 56 no static 192.168.1.1/24 no yes delegated 100 0 unmanaged \
        paste "$ed25519" yes sda
      When call eval 'questions && seed_json'
      The output should equal "$(cat "$seeds/delegated-management.json")"
      The contents of file "$work/said" should equal 'Enter 1 to 4 hex digits that fit the /56.'
    End
    It 'offers no WAN port but "none" when there is one port left'
      answers sda enp1s0 none static 192.168.1.1/24 yes 192.168.1.100 192.168.1.199 yes none paste "$ed25519" yes sda
      When call eval 'questions && seed_json'
      The output should equal "$(cat "$seeds/no-wan.json")"
    End
    It 'starts over when the typed disk name is wrong'
      answers sda enp1s0 none static 192.168.1.1/24 no no none none yes sdb
      When call questions
      The status should equal 1
      The contents of file "$work/said" should equal 'That was not sda; nothing was written.'
    End
    It 'stops when andaru.disk= names the boot medium'
      echo 'console=tty0 andaru.disk=/dev/sdb' >"$CMDLINE"
      When call questions
      The status should equal 2
      The contents of file "$work/said" should start with 'andaru.disk=/dev/sdb cannot be installed to'
    End
    It 'stops when another disk has a filesystem labelled boot'
      lsblk_json() { cat "$data/lsblk-stray-boot.json"; }
      answers sda
      When call questions
      The status should equal 2
      The contents of file "$work/said" should start with 'Another disk has a filesystem labelled "boot" (/dev/sdd3 )'
    End
    It 'skips the typed confirmation with andaru.disk='
      echo 'console=tty0 andaru.disk=/dev/sda' >"$CMDLINE"
      answers enp1s0 none static 192.168.1.1/24 no no none none yes
      When call questions
      The status should equal 0
      The variable DISK should equal /dev/sda
    End
    It 'refuses management with neither family, then asks again'
      answers sda enp1s0 none none none static 192.168.1.1/24 no no none none yes sda
      When call questions
      The status should equal 0
      The contents of file "$work/said" should equal 'The management port needs IPv4 or IPv6, or Andaru cannot be reached.'
    End
    It 'leaves unmanaged RA out for a static address that is not a /64'
      answers sda enp1s0 none none static fd00:1::1/48 router none yes sda
      When call eval 'questions && seed_json | jq -c .management.addressing.ipv6'
      The output should equal '{"mode":"static","addresses":["fd00:1::1/48"],"ra":{"mode":"router"}}'
    End
  End
End
