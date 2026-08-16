# The Andaru QEMU lab — Debian 13 (trixie) arm64 on macOS

A reusable two-VM lab for repeated, reproducible testing of the Andaru firewall
platform. Both VMs run Debian 13 for arm64 under QEMU with HVF acceleration on
Apple silicon.

The scripts live in [`dev/vm-lab/`](../../dev/vm-lab). **The VM data does not
live in this repository.** Images, goldens, overlays, firmware variables and
logs are large, host-specific and disposable, so they are written to
`$ANDARU_LAB_DIR` (default `~/vm/andaru`) instead. Nothing in the lab ever
writes into the working tree.

## Serial console only

Neither VM has an SSH port forward, an SSH key, or a running SSH server, and no
script in this lab uses SSH. This is deliberate and must not be reintroduced in
any form.

When the network stack is the system under test, an SSH session is *part of the
system under test*, and therefore not a trustworthy control channel. A firewall
change that breaks connectivity must still leave you able to observe and repair
the guest. `lab-console` and `lab-run` are the only interaction paths.

The same reasoning drives the networking setup: both NICs are QEMU user-mode
netdevs with no `hostfwd`, so the guest can reach outward but the host cannot
reach in.

## Quick start

From a fresh clone, on an Apple-silicon Mac with QEMU installed:

```sh
brew install qemu          # if you have not already
dev/vm-lab/lab-setup
```

`lab-setup` is the single bootstrap entry point. It checks prerequisites,
downloads both Debian images, verifies them against the published
`SHA512SUMS`, vendors the EDK2 firmware, builds the cloud-init seed ISO, and
prepares both goldens. There are no manual steps.

It is idempotent. An image that is already downloaded and still matches its
checksum is not re-fetched, and a golden that is already built and still
matches its `.sha256` sidecar is left alone rather than clobbered. Pass
`--force-goldens` to rebuild both goldens from the pristine images anyway.

To put the lab somewhere else:

```sh
ANDARU_LAB_DIR=/scratch/andaru-lab dev/vm-lab/lab-setup
```

### Prerequisites

`lab-setup` checks for all of these and names what to install if any are
missing: `qemu-system-aarch64` and `qemu-img` (`brew install qemu`), `expect`,
and `nc`, `shasum`, `base64`, `curl` and `hdiutil` from the base macOS install.
It also locates QEMU's EDK2 firmware (`edk2-aarch64-code.fd`) and fails with a
clear message if it is absent.

`socat` would work for `lab-console`, but it is not part of a stock macOS
install, so the lab standardises on `nc -U`.

## Layout

Scripts, in the repository:

```
dev/vm-lab/
  lab-setup               bootstrap everything from nothing
  lab-common.sh           shared settings + helpers (sourced, never executed)
  lab-build-golden        build one immutable golden from a pristine image
  lab-new / lab-boot / lab-console / lab-run
  lab-list / lab-stop / lab-destroy / lab-reset
  lab-drive.exp           the serial-console driver
  golden-prep-*.cmds      one-time in-guest preparation, one command per line
  cidata/                 cloud-init NoCloud seed source (cloud flavor only)
  tests/verify.cmds       the baseline inventory probe
```

VM data, outside the repository, under `$ANDARU_LAB_DIR`:

```
images/     pristine downloads + SHA512SUMS, checksum-verified, chmod a-w, never modified
golden/     prepared base images, chmod a-w, plus .sha256 sidecars — never booted for tests
overlays/   per-test qcow2 overlays backed by golden/, plus a <name>.meta per overlay
seeds/      the generated cloud-init NoCloud seed ISO and its source dir
fw/         vendored EDK2 firmware, the pristine vars template, per-overlay vars copies
run/        per-overlay runtime state: serial.sock, qemu.pid, qemu.log (transient)
logs/       serial console transcripts from lab-run and from golden builds
```

`run/` is separate from `overlays/` so that `overlays/` holds only disk images.
`fw/` vendors the firmware itself, so the lab does not silently change behaviour
when Homebrew updates QEMU. `overlays/<name>.meta` records which flavor an
overlay came from, so `lab-boot` and `lab-reset` need only the test name.

## The two VMs

They represent the two baselines the product must handle.

| | **cloud** | **nocloud** |
|---|---|---|
| Debian image | `debian-13-genericcloud-arm64` | `debian-13-nocloud-arm64` |
| Represents | the cloud-image baseline | the clean baseline |
| cloud-init | installed, ran once from the seed ISO | not installed |
| Seed ISO | `seeds/cloud-seed.iso` (label `CIDATA`) | none |
| Hostname | `andaru-cloud` | `localhost` |
| NIC 0 MAC | `52:54:00:aa:00:01` | `52:54:00:bb:00:01` |
| NIC 1 MAC | `52:54:00:aa:00:02` | `52:54:00:bb:00:02` |

Both VMs get 2 vCPU, 2048 MB RAM (`LAB_SMP` / `LAB_MEM` override), a 10 G disk,
and two `virtio-net-pci` NICs on separate user-mode networks (`10.0.2.0/24` and
`10.0.3.0/24`). The MACs are fixed, so interface-identity tests are
reproducible: `enp0s1` is always NIC 0 and `enp0s2` always NIC 1.

Root password on both goldens: **`andaru`** (`LAB_ROOT_PW` in `lab-common.sh`).
Trivial by design; nothing is reachable from outside.

## Goldens are immutable

A golden is booted exactly once, by `lab-build-golden`, to prepare it. It is
then powered off cleanly and `chmod a-w`. Every test afterwards runs on a
copy-on-write qcow2 overlay with its own copy of the UEFI variables, because
the guest writes to those too.

`lab-build-golden --force` chmods a golden back to writable and rebuilds it
*from the pristine image*. Never run it while an overlay backed by that golden
is running. `lab-destroy` refuses to remove anything outside `overlays/` and
`fw/`, and nothing in the lab writes to `images/` or `golden/` outside
`lab-build-golden`.

## What is changed from stock

**Both goldens:** grown to a 10 G virtual disk with `qemu-img resize`; a root
password set; `/var/lib/andaru-golden-prepared` touched as a marker; powered off
cleanly and `chmod a-w`.

**cloud golden.** Prepared from a NoCloud seed ISO (`cidata/`) that sets the
root password and the hostname, and — deliberately — seeds **no** SSH keys, sets
`ssh_pwauth: false`, and then disables and masks `ssh.service`.
`openssh-server` is still installed, because it is part of the stock image, but
it can never start. cloud-init's `growpart` grows the root filesystem on its
own. The seed ISO stays attached on every boot so the datasource remains
present; the instance-id is unchanged, so per-instance modules do not re-run.

**nocloud golden.** Two things about the stock nocloud image drive its
preparation.

*First boot runs an interactive wizard.* The stock image boots
`systemd-firstboot` on the serial console. Left alone it prints `No password
entered, skipping.` and root ends up with no usable password — a later `root`
login is then rejected with `Login incorrect`. So the documented "root login on
the serial console" only works if that wizard is answered. `lab-drive.exp`
answers it during the single golden-preparation boot: the locale, keymap,
timezone and hostname prompts are skipped, keeping the stock defaults, and the
root password is set to `andaru`.

*The image ships no partition editor.* There is no `sfdisk`, `fdisk`, `parted`
or `growpart`, and no cloud-init to grow anything, so `qemu-img resize` alone
leaves root at 2.8 G. The preparation therefore installs `cloud-guest-utils`,
runs `growpart /dev/vda 1` and `resize2fs /dev/vda1`, and then purges the
package again — see the next section for exactly what that leaves behind.

`/etc/securetty` does not exist on either image, so no change is needed there;
root console login is permitted by PAM as shipped.

### Package residue in the nocloud golden

The preparation records `dpkg-query` output before and after, and compares them.
The result of the run that built the current golden:

* The **package set is identical to stock**: 277 packages before, 277 after.
  `cloud-guest-utils` and everything it pulled in (`fdisk`, `gdisk`,
  `libfdisk1`) are gone after `apt-get purge` plus `apt-get autoremove --purge`.
* **No `rc`-state residue**: `dpkg -l` lists zero packages in the
  removed-but-not-purged state.
* **`/var/lib/apt/lists` is byte-for-byte back to stock** (empty, as shipped):
  the preparation removes only the index files that its own `apt-get update`
  added.
* `/root` contains only stock files (`.bashrc`, `.profile`, `.ssh`).

There is one genuine, unavoidable difference. `apt-get update` picks up the
current Debian point release, so resolving `cloud-guest-utils`' dependencies
**upgrades eleven stock util-linux-family packages** from `2.41-5` to
`2.41.5-0+deb13u1`:

```
bsdextrautils  bsdutils  libblkid1  liblastlog2-2  libmount1  libsmartcols1
libuuid1  login  mount  util-linux  uuid-runtime
```

Purging cannot undo an upgrade, so those versions stay. This is why the
preparation prints `PKGSET-DIFFERS-FROM-STOCK`: the *names* match, the
*versions* of those eleven do not.

We purge anyway, deliberately. Leaving `cloud-guest-utils` installed would not
avoid the upgrades — they are caused by dependency resolution against the
current archive, not by the package remaining — so it would give a golden with
*both* the version drift *and* four extra packages. Purging is strictly the
closer of the two to stock. If you need a golden with no version drift at all,
the images themselves must be re-downloaded after the point release, so that
stock and archive agree.

## Commands

```sh
dev/vm-lab/lab-new nocloud mytest        # overlay + private UEFI vars from the golden
dev/vm-lab/lab-boot mytest               # headless, serial on a unix socket, backgrounded
dev/vm-lab/lab-console mytest            # interactive console; ctrl-c detaches
dev/vm-lab/lab-run mytest dev/vm-lab/tests/verify.cmds   # drive it non-interactively
dev/vm-lab/lab-stop mytest               # ACPI power off
dev/vm-lab/lab-reset mytest              # destroy + recreate from the same golden
dev/vm-lab/lab-destroy mytest            # remove overlay + vars + run state
dev/vm-lab/lab-list                      # every overlay and whether it is running
```

`lab-run` takes a file with **one shell command per line** (blank lines and
lines starting with `#` are ignored). It logs in over the console, sets a
sentinel prompt, and runs each line, capturing that line's exit status.

- exit `0` — every command returned 0
- exit `1` — at least one command returned non-zero (each is reported on stderr)
- exit `2` — driver failure: no login, a per-command timeout, or the hard deadline

`LAB_CMD_TIMEOUT` (default 120 s) caps a single command; `LAB_DEADLINE`
(default 900 s) is a hard cap on the whole session, enforced inside the driver
so it can never wait forever. Add `--poweroff` to shut the guest down at the
end. The full transcript, including boot noise, lands in
`logs/<test>-<script>-<UTC timestamp>.log`.

```sh
LAB_CMD_TIMEOUT=300 LAB_DEADLINE=1800 dev/vm-lab/lab-run mytest long.cmds --poweroff
```

## Traps

These are recorded as comments in the code as well, because each one cost real
debugging time and none of them announces itself.

- **Never use `-daemonize` with `-serial unix:...,server=on`.** The daemonized
  QEMU never accepts on the listening socket: `connect()` succeeds because of
  the kernel backlog, so `nc` attaches happily, but not one byte of console
  output is ever delivered and there is no error anywhere. `qemu_boot` uses
  `nohup ... &` instead. Do not "simplify" it back.
- **The serial tty is in canonical mode, so one input line is capped at ~4 KiB.**
  Anything longer is silently swallowed and the command never completes. To get
  a file into a guest, use `emit_push` in `lab-common.sh`, which base64s it and
  feeds it in 400-byte chunks.
- **Give `apt` and `dpkg` `</dev/null` in a `.cmds` script.** The driver
  pipelines: it queues its exit-status sentinel line into the guest's tty before
  the command has finished. `dpkg` reads from stdin, and on a serial tty it
  drains that queued line out of the input buffer, so the sentinel never reaches
  the shell and the driver aborts with `lost rc sentinel`. `</dev/null` gives
  `dpkg` an immediate EOF and leaves the input queue alone.
- **One command per line, and only one.** The driver appends an `echo` after
  each line to read `$?`, so a line that opens a heredoc or a multi-line
  construct will desynchronise it. Put anything complicated in a script and push
  it in with `emit_push`.
- **`LAB_CMD_TIMEOUT` is enforced on the sentinel wait, not the prompt wait**,
  precisely because of the pipelining above. The prompt match returns
  immediately from a stale prompt in the buffer; the wait for the exit-status
  sentinel is the one that actually spans the command's runtime.
- `/root/.bash_history` accumulates inside long-lived *overlays*. That is fine —
  overlays are disposable, and the goldens are clean.
- The guests are wall-clock sensitive only insofar as HVF is available; without
  it QEMU falls back to TCG and everything gets much slower.

## Not yet verified

The lab as a whole was built, booted and exercised in an earlier session, and
the findings above about the two baselines, the `-daemonize` trap and the 4 KiB
line cap come from that work. The following were **not** re-executed when the
lab was moved into this repository, and should be re-run before the lab is
relied on:

- **`lab-setup` end to end from an empty `$ANDARU_LAB_DIR`.** It was assembled
  from steps that had each been run by hand, but never executed as one command
  against a clean directory. The image download, checksum verification, firmware
  vendoring and seed-ISO build paths inside it are unexercised as written.
- **Rebuilding the cloud golden.** Only the nocloud golden was rebuilt from
  scratch after the move.
- **The overlay isolation proof** (contaminate, destroy, recreate, confirm the
  contamination is gone and the golden's checksum is unchanged).
- **The `lab-run` pass/fail battery** on both VMs, including confirming that a
  failing script really does produce a non-zero exit.
- **The listener check** confirming no sshd and no unexpected listening sockets
  on either VM.

The nocloud golden preparation described above — installing `cloud-guest-utils`,
`growpart`, `resize2fs`, and purging back — *was* executed, and produced a 9.7 G
root filesystem and the package comparison quoted in the residue section.

## Known follow-up

`~/vm/andaru-packaging-probe/` is a separate, older VM tree left over from an
earlier packaging probe. It is unrelated to this lab and is untouched by these
scripts; folding it in, or deleting it, is a later cleanup.
