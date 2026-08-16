# shellcheck shell=sh
# Shared settings + helpers for the Andaru QEMU lab. Sourced, never executed.
#
# NOTE: there is no SSH anywhere in this lab -- no hostfwd, no seeded keys, no
# running sshd. The serial console is the only control channel. When the
# network stack is the system under test, an SSH session is *part of the system
# under test* and therefore not a trustworthy way to observe or repair it.

# Where the scripts live (inside the git checkout) ...
LAB_SCRIPTS=$(cd "$(dirname "$0")" && pwd)
# ... and where the VM data lives (deliberately OUTSIDE the checkout: images,
# overlays, firmware vars and logs are large, host-specific and disposable).
LAB_ROOT="${ANDARU_LAB_DIR:-$HOME/vm/andaru}"
export LAB_SCRIPTS LAB_ROOT

LAB_IMAGES="$LAB_ROOT/images"
LAB_GOLDEN="$LAB_ROOT/golden"
LAB_OVERLAYS="$LAB_ROOT/overlays"
LAB_SEEDS="$LAB_ROOT/seeds"
LAB_FW="$LAB_ROOT/fw"
LAB_LOGS="$LAB_ROOT/logs"
LAB_RUN="$LAB_ROOT/run"

LAB_CODE_FD="$LAB_FW/edk2-aarch64-code.fd"
LAB_VARS_TEMPLATE="$LAB_FW/edk2-arm-vars-template.fd"

# Published Debian cloud images. Both the images and the SHA512SUMS that verify
# them come from this directory, so a Debian point release updates both together.
LAB_IMAGE_BASE_URL="${LAB_IMAGE_BASE_URL:-https://cloud.debian.org/images/cloud/trixie/latest}"

# Golden credentials. Console-only lab, deliberately trivial, never exposed:
# nothing in this lab is reachable from the host, let alone from outside it.
LAB_ROOT_PW="andaru"

LAB_MEM="${LAB_MEM:-2048}"
LAB_SMP="${LAB_SMP:-2}"

die() { echo "lab: $*" >&2; exit 1; }

# require_tools -- fail loudly, naming what to install, rather than dying
# halfway through a 10-minute golden build.
require_tools() {
    _missing=
    for _t in qemu-system-aarch64 qemu-img expect nc shasum base64 curl hdiutil; do
        command -v "$_t" >/dev/null 2>&1 || _missing="$_missing $_t"
    done
    if [ -n "$_missing" ]; then
        echo "lab: missing required tool(s):$_missing" >&2
        echo "lab:   qemu-system-aarch64, qemu-img -> brew install qemu" >&2
        echo "lab:   expect                        -> brew install expect (macOS ships one)" >&2
        echo "lab:   nc, shasum, base64, curl, hdiutil -> base macOS install" >&2
        exit 1
    fi
    # socat would work for lab-console too, but it is not part of a stock macOS
    # install, so the lab standardises on `nc -U`.
}

# qemu_share_dir -- locate the EDK2 firmware that ships with QEMU. The lab
# vendors a copy into fw/ so a Homebrew QEMU upgrade cannot silently change
# guest firmware behaviour underneath a golden.
qemu_share_dir() {
    _q=$(command -v qemu-system-aarch64) || die "qemu-system-aarch64 not found"
    # Resolve symlinks by hand: macOS has no readlink -f.
    while [ -L "$_q" ]; do
        _l=$(readlink "$_q")
        case "$_l" in /*) _q=$_l ;; *) _q=$(dirname "$_q")/$_l ;; esac
    done
    for _d in "$(dirname "$(dirname "$_q")")/share/qemu" /usr/share/qemu /usr/local/share/qemu; do
        [ -f "$_d/edk2-aarch64-code.fd" ] && { echo "$_d"; return 0; }
    done
    die "EDK2 firmware (edk2-aarch64-code.fd) not found next to $_q; install QEMU's firmware files"
}

# flavor_check <flavor>
flavor_check() {
    case "$1" in
        cloud|nocloud) : ;;
        *) die "unknown flavor '$1' (want: cloud|nocloud)" ;;
    esac
}

golden_path() { echo "$LAB_GOLDEN/debian-13-$1-arm64.qcow2"; }

# Debian calls the cloud-init variant "genericcloud"; the lab calls it "cloud".
image_name_for() {
    case "$1" in
        cloud)   echo debian-13-genericcloud-arm64.qcow2 ;;
        nocloud) echo debian-13-nocloud-arm64.qcow2 ;;
        *) die "no pristine image for flavor '$1'" ;;
    esac
}

# Fixed MACs, so interface-identity tests are reproducible across runs:
# enp0s1 is always NIC 0 and enp0s2 always NIC 1. Both NICs are user-mode
# netdevs on distinct subnets. No hostfwd: the guest is never reachable from
# the host, by design.
mac_for() {
    case "$1:$2" in
        cloud:0)   echo 52:54:00:aa:00:01 ;;
        cloud:1)   echo 52:54:00:aa:00:02 ;;
        nocloud:0) echo 52:54:00:bb:00:01 ;;
        nocloud:1) echo 52:54:00:bb:00:02 ;;
        *) die "no MAC for flavor '$1' nic '$2'" ;;
    esac
}

meta_path()  { echo "$LAB_OVERLAYS/$1.meta"; }
disk_path()  { echo "$LAB_OVERLAYS/$1.qcow2"; }
vars_path()  { echo "$LAB_FW/$1-vars.fd"; }
rundir()     { echo "$LAB_RUN/$1"; }
sock_path()  { echo "$LAB_RUN/$1/serial.sock"; }
pid_path()   { echo "$LAB_RUN/$1/qemu.pid"; }

# read_meta <test-name> -> sets LAB_FLAVOR
read_meta() {
    _m=$(meta_path "$1")
    [ -f "$_m" ] || die "no such test overlay '$1' (expected $_m); run lab-new first"
    # shellcheck disable=SC1090
    . "$_m"
    [ -n "${LAB_FLAVOR:-}" ] || die "meta file $_m has no LAB_FLAVOR"
}

vm_running() {
    _p=$(pid_path "$1")
    [ -f "$_p" ] || return 1
    kill -0 "$(cat "$_p")" 2>/dev/null
}

# qemu_boot <flavor> <disk> <vars> <sock> <pidfile> <logfile> [extra qemu args...]
qemu_boot() {
    _flavor=$1; _disk=$2; _vars=$3; _sock=$4; _pid=$5; _log=$6
    shift 6
    _seed=""
    if [ "$_flavor" = cloud ]; then
        _seed="$LAB_SEEDS/cloud-seed.iso"
        [ -f "$_seed" ] || die "missing seed ISO $_seed (run lab-setup)"
    fi
    rm -f "$_sock"
    # NOTE: do NOT use -daemonize here. With `-serial unix:...,server=on` the
    # daemonized QEMU never accepts on the listening socket: connect() succeeds
    # (the kernel backlog absorbs it) so `nc` attaches happily, but not one byte
    # of console output is ever delivered and there is no error anywhere --
    # neither in the QEMU log nor on the client. Backgrounding with `nohup ... &`
    # works correctly. This cost real debugging time; do not "simplify" it back.
    # shellcheck disable=SC2086
    nohup qemu-system-aarch64 \
        -machine virt,accel=hvf -cpu host -smp "$LAB_SMP" -m "$LAB_MEM" \
        -drive "if=pflash,format=raw,unit=0,file=$LAB_CODE_FD,readonly=on" \
        -drive "if=pflash,format=raw,unit=1,file=$_vars" \
        -drive "if=virtio,format=qcow2,file=$_disk" \
        ${_seed:+-drive "if=virtio,format=raw,media=cdrom,readonly=on,file=$_seed"} \
        -netdev user,id=n0,net=10.0.2.0/24 \
        -device "virtio-net-pci,netdev=n0,mac=$(mac_for "$_flavor" 0)" \
        -netdev user,id=n1,net=10.0.3.0/24 \
        -device "virtio-net-pci,netdev=n1,mac=$(mac_for "$_flavor" 1)" \
        -display none \
        -serial "unix:$_sock,server=on,wait=off" \
        "$@" >>"$_log" 2>&1 &
    echo $! > "$_pid"
}

# wait_for_sock <sock> <seconds>
wait_for_sock() {
    _i=0
    while [ ! -S "$1" ]; do
        _i=$((_i + 1))
        [ "$_i" -gt "$(( $2 * 10 ))" ] && die "serial socket $1 never appeared"
        perl -e 'select undef,undef,undef,0.1'
    done
}

# emit_push <local-file> <remote-path>
# Print the command lines that recreate <local-file> inside the guest. The
# serial tty is in canonical mode, so a single input line is capped at ~4 KiB;
# anything longer is silently swallowed and the command never completes. The
# payload is therefore base64'd and fed in 400-byte chunks, well under the cap.
emit_push() {
    _rem=$2
    echo ": > $_rem.b64"
    # `|| [ -n "$_c" ]`: fold's final chunk has no trailing newline, and a bare
    # `read` would silently discard it -- truncating the pushed file.
    base64 < "$1" | tr -d '\n' | fold -w 400 | while IFS= read -r _c || [ -n "$_c" ]; do
        echo "printf '%s' '$_c' >> $_rem.b64"
    done
    echo "base64 -d < $_rem.b64 > $_rem"
    echo "rm -f $_rem.b64"
}
