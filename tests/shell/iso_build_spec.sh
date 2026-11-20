#shellcheck shell=sh
# The install ISO build (image/iso/build.sh), with the container engine, skopeo and xorriso replaced by stubs on PATH
# that log their arguments and answer from FAKE_* variables. A real build is the release's (task 7).

Describe 'image/iso/build.sh'
  root="${SHELLSPEC_PROJECT_ROOT:-$PWD}"
  amd64=sha256:72eff027c5d62dc482c74b7a2b76af29bc870d13b1a54263b8e8ff1eb437adb9
  arm64=sha256:099877444417b22657821920a624e5d833a4a97e9b4f482d955bc0e8b620ae4e

  setup() {
    work=$(mktemp -d)
    mkdir "$work/bin" "$work/out"
    log=$work/log
    # The engine: butane (render.sh), create/cp/rm (os-release), and coreos-installer in the release's image.
    cat >"$work/bin/podman" <<'EOF'
#!/bin/sh
echo "podman $*" >>"$FAKE_LOG"
case " $* " in
*" --strict "*) cat >/dev/null; echo '{"ignition":{"version":"3.5.0"}}'; exit ;;
esac
case $1 in
create) echo ctr1; exit ;;
cp) printf '%b' "$FAKE_OSRELEASE" >"$3"; exit ;;
rm) exit ;;
esac
w=; while [ $# -gt 0 ]; do case $1 in -v) w=${2%%:*}; shift ;; coreos-installer) shift; break ;; esac; shift; done
case "$1 $2" in
"download --image-url") [ -n "${FAKE_NO_DOWNLOAD:-}" ] || : >"$w/${3##*/}" ;;
"iso customize") truncate -s "${FAKE_SIZE:-1000}" "$w/out.iso"; echo "$*" >"$w/customized" ;;
"iso ignition") src=$(gzip -c "${FAKE_IGN:-$w/live.ign}" | base64 -w0)
  printf '{"ignition":{"version":"3.5.0","config":{"merge":[{"compression":"gzip","source":"data:;base64,%s"}]}}}\n' "$src" ;;
"iso kargs") echo "${FAKE_KARGS:-rw coreos.liveiso=x console=ttyAMA0,115200n8 systemd.show_status=false loglevel=3}" ;;
esac
EOF
    cat >"$work/bin/skopeo" <<'EOF'
#!/bin/sh
echo "skopeo $*" >>"$FAKE_LOG"
case "$1 $2" in
"inspect --config") case $3 in *"$FAKE_AMD64"*) echo '{"architecture":"amd64"}' ;; *) echo "{\"architecture\":\"$FAKE_ARCH\"}" ;; esac ;;
"inspect --raw") echo "{\"manifests\":[{\"digest\":\"$FAKE_LISTED\"},{\"digest\":\"$FAKE_AMD64\"}]}" ;;
"copy --quiet")
  d=$(mktemp -d); echo "$FAKE_INDEX" >"$d/index.json"
  tar -cf "${5#oci-archive:}" -C "$d" index.json; rm -r "$d" ;;
esac
EOF
    cat >"$work/bin/xorriso" <<'EOF'
#!/bin/sh
echo "xorriso $*" >>"$FAKE_LOG"
case " $* " in
*" -pvd_info "*) echo "Volume Id    : $FAKE_VOLUME" ;;
*) : >"$4" ;;
esac
EOF
    chmod +x "$work/bin/"*
    PATH=$work/bin:$PATH
    export FAKE_LOG="$log" FAKE_AMD64="$amd64" FAKE_ARCH=arm64 FAKE_LISTED="$arm64" FAKE_VOLUME=fedora-coreos-44.20261023.3.1
    export FAKE_OSRELEASE="NAME=\"Andaru\"\nRELEASE_TYPE=stable\nOSTREE_VERSION='44.20261023.3.1'\n"
    export FAKE_INDEX="{\"manifests\":[{\"digest\":\"$arm64\"}]}"
  }
  cleanup() { rm -rf "$work"; }
  BeforeEach 'setup'
  AfterEach 'cleanup'
  build() { sh "$root/image/iso/build.sh" ghcr.io/andaru-project/andaru 1.2.3 "${DIGEST:-$arm64}" "${ARCH:-aarch64}" "$amd64" "$work/out/a.iso"; }

  It 'builds an ISO from the signed live ISO, the archive and the live config'
    When call build
    The status should be success
    The line 1 of output should equal 'build.sh: Fedora CoreOS 44.20261023.3.1 (stable), aarch64'
    The output should include "build.sh: $work/out/a.iso (1000 bytes)"
    The path "$work/out/a.iso" should be file
    The contents of file "$log" should include "coreos-installer download --image-url https://builds.coreos.fedoraproject.org/prod/streams/stable/builds/44.20261023.3.1/aarch64/fedora-coreos-44.20261023.3.1-live-iso.aarch64.iso --directory /w"
    The contents of file "$log" should include "--live-karg-append console=ttyAMA0,115200n8 --live-karg-append systemd.show_status=false --live-karg-append loglevel=3"
  End

  It 'refuses a digest with a second line'
    DIGEST="$arm64
x"
    When call build
    The status should be failure
    The error should start with 'build.sh: bad digest'
  End
  It 'refuses an unknown architecture'
    ARCH=riscv64
    When call build
    The status should be failure
    The error should equal 'build.sh: bad arch riscv64'
  End
  It 'refuses an output that is a directory'
    When run sh "$root/image/iso/build.sh" ghcr.io/andaru-project/andaru 1.2.3 "$arm64" aarch64 "$amd64" "$work/out"
    The status should be failure
    The error should equal "build.sh: $work/out must be a file in an existing directory"
  End
  It 'checks the repository before it downloads anything'
    When run sh "$root/image/iso/build.sh" -x/andaru 1.2.3 "$arm64" aarch64 "$amd64" "$work/out/a.iso"
    The status should be failure
    The error should equal 'render.sh: bad image: -x/andaru'
    The contents of file "$log" should not include skopeo
  End
  It "refuses a digest that is not ARCH's image"
    ARCH=x86_64
    When call build
    The status should be failure
    The error should equal "build.sh: $arm64 is not the amd64 image"
  End
  It 'refuses an amd64 digest that is not the amd64 image'
    export FAKE_AMD64=sha256:1111111111111111111111111111111111111111111111111111111111111111
    When call build
    The status should be failure
    The error should equal "build.sh: $amd64 is not the amd64 image"
  End
  It 'names a registry it cannot read'
    printf '#!/bin/sh\nexit 1\n' >"$work/bin/skopeo"
    When call build
    The status should be failure
    The error should equal 'build.sh: cannot read ghcr.io/andaru-project/andaru:1.2.3'
  End
  It 'refuses a digest the tag does not list'
    export FAKE_LISTED=sha256:0000000000000000000000000000000000000000000000000000000000000000
    When call build
    The status should be failure
    The error should equal "build.sh: ghcr.io/andaru-project/andaru:1.2.3 does not list $arm64"
  End
  It "never runs the image's os-release"
    export FAKE_OSRELEASE="RELEASE_TYPE=stable\nOSTREE_VERSION=\"\$(touch $work/ran)44.20261023.3.1\"\n"
    When call build
    The status should be failure
    The error should start with "build.sh: the image's OSTREE_VERSION is"
    The path "$work/ran" should not be exist
  End
  It 'refuses a stream of more than one line'
    export FAKE_OSRELEASE="RELEASE_TYPE=stable\nRELEASE_TYPE=x\nOSTREE_VERSION=44.20261023.3.1\n"
    When call build
    The status should be failure
    The error should start with "build.sh: the image's RELEASE_TYPE is"
  End
  It 'stops when the download wrote nothing'
    export FAKE_NO_DOWNLOAD=1
    When call build
    The status should be failure
    The output should include 'Fedora CoreOS'
    The error should equal 'build.sh: coreos-installer did not download fedora-coreos-44.20261023.3.1-live-iso.aarch64.iso'
  End
  It 'refuses a live ISO whose volume ID names another build'
    export FAKE_VOLUME=fedora-coreos-44.20260101.3.0
    When call build
    The status should be failure
    The output should include 'Fedora CoreOS'
    The error should include "volume ID is 'fedora-coreos-44.20260101.3.0', not fedora-coreos-44.20261023.3.1"
  End
  It 'refuses an archive holding more than the digest'
    export FAKE_INDEX="{\"manifests\":[{\"digest\":\"$arm64\"},{\"digest\":\"$amd64\"}]}"
    When call build
    The status should be failure
    The output should include 'Fedora CoreOS'
    The error should equal "build.sh: the archive does not hold exactly $arm64"
  End
  It 'refuses an ISO carrying another live config'
    echo '{"ignition":{"version":"3.5.0"},"passwd":{}}' >"$work/other.ign"
    export FAKE_IGN=$work/other.ign
    When call build
    The status should be failure
    The output should include 'Fedora CoreOS'
    The error should equal 'build.sh: the ISO does not carry the rendered live config'
  End
  It 'refuses an ISO without every kernel argument'
    export FAKE_KARGS='rw console=ttyAMA0,115200n8 systemd.show_status=false'
    When call build
    The status should be failure
    The output should include 'Fedora CoreOS'
    The error should equal 'build.sh: the ISO'"'"'s kernel arguments lack loglevel=3'
  End
  It 'refuses an ISO of 2 GiB, which GitHub would not take'
    export FAKE_SIZE=2147483648
    When call build
    The status should be failure
    The output should include 'Fedora CoreOS'
    The error should equal 'build.sh: the ISO is 2147483648 bytes; GitHub takes under 2147483648'
    The path "$work/out/a.iso" should not be exist
  End
End
