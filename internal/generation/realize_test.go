package generation

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeNetworkd models what the real tools report: a reload binds each
// interface to the first matching file on disk, after a settable number of polls.
type fakeNetworkd struct {
	root string

	mu      sync.Mutex
	argv    []string
	delay   int // list calls before a reload takes effect; negative: never
	waiting int // 0: nothing scheduled
	bound   map[string]string
	address map[string][]string
	devices map[string]bool
	state   string
	fail    string // commands starting with this fail
	chowns  []string

	noAddress, noRoute bool
}

func (f *fakeNetworkd) netFiles() map[string]string {
	out := map[string]string{}
	paths, _ := filepath.Glob(filepath.Join(f.root, netDir, "*"))
	for _, p := range paths {
		if data, err := os.ReadFile(p); err == nil {
			out[filepath.Base(p)] = string(data)
		}
	}
	return out
}

// apply is networkd finishing a reload.
func (f *fakeNetworkd) apply() {
	files := f.netFiles()
	f.bound, f.address = map[string]string{}, map[string][]string{}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if device, ok := strings.CutSuffix(name, ".netdev"); ok {
			f.devices[strings.TrimPrefix(device, "50-andaru-")] = true
		}
	}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if !strings.HasSuffix(name, ".network") {
			continue
		}
		for _, line := range strings.Split(files[name], "\n") {
			iface, ok := strings.CutPrefix(line, "Name=")
			if !ok || f.bound[iface] != "" || !f.devices[iface] {
				continue
			}
			f.bound[iface] = netDir + name
			for _, l := range strings.Split(files[name], "\n") {
				if a, ok := strings.CutPrefix(l, "Address="); ok {
					f.address[iface] = append(f.address[iface], a)
				}
				if strings.HasPrefix(l, "DHCP=") {
					f.address[iface] = append(f.address[iface], "10.0.1.19/20")
				}
			}
		}
	}
}

func (f *fakeNetworkd) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	command := filepath.Base(name) + " " + strings.Join(args, " ")
	f.argv = append(f.argv, command)
	if f.fail != "" && strings.HasPrefix(command, f.fail) {
		return nil, errors.New(command + ": injected failure")
	}
	switch {
	case command == "networkctl reload":
		if f.delay == 0 {
			f.apply()
		} else {
			f.waiting = f.delay
		}
	case strings.HasPrefix(command, "networkctl delete "):
		delete(f.devices, args[1])
		delete(f.bound, args[1])
	case command == "networkctl list --json=short":
		if f.waiting > 0 {
			if f.waiting--; f.waiting == 0 {
				f.apply()
			}
		}
		type iface struct{ Name, AdministrativeState, NetworkFile string }
		reply := struct{ Interfaces []iface }{[]iface{{Name: "lo", AdministrativeState: "unmanaged"}}}
		for _, device := range slices.Sorted(maps.Keys(f.devices)) {
			reply.Interfaces = append(reply.Interfaces, iface{device, f.state, f.bound[device]})
		}
		return json.Marshal(reply)
	case command == "ip -d -j address show":
		var links []map[string]any
		for i, device := range slices.Sorted(maps.Keys(f.devices)) {
			link := map[string]any{"ifindex": i + 2, "ifname": device, "link_type": "ether", "operstate": "UP", "mtu": 1500, "flags": []string{}, "address": "02:00:00:00:00:01"}
			if device != "eth0" && device != "eth1" {
				link["linkinfo"] = map[string]string{"info_kind": "vlan"}
			}
			addresses := []map[string]any{}
			for _, a := range f.address[device] {
				if f.noAddress {
					break
				}
				ip, bits, _ := strings.Cut(a, "/")
				var n int
				_ = json.Unmarshal([]byte(bits), &n)
				addresses = append(addresses, map[string]any{"family": "inet", "local": ip, "prefixlen": n, "scope": "global"})
			}
			link["addr_info"] = addresses
			links = append(links, link)
		}
		return json.Marshal(links)
	case command == "ip -j route show default":
		if f.noRoute || f.noAddress || len(f.address["eth0"]) == 0 {
			return []byte("[]"), nil
		}
		return []byte(`[{"dst":"default","gateway":"10.0.0.1","dev":"eth0"}]`), nil
	}
	return nil, nil
}

func (f *fakeNetworkd) ran(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, a := range f.argv {
		if strings.HasPrefix(a, prefix) {
			n++
		}
	}
	return n
}

type bench struct {
	t   *testing.T
	r   Realizer
	net *fakeNetworkd
	dir string
	now time.Time
}

const (
	uplinkDHCP   = "[Match]\nName=eth0\n\n[Network]\nDHCP=ipv4\n"
	uplinkStatic = "[Match]\nName=eth0\n\n[Network]\nDHCP=ipv4\nAddress=192.0.2.1/24\n"
	vlanDevice   = "[NetDev]\nName=vl0\nKind=vlan\n"
	vlanNetwork  = "[Match]\nName=vl0\n\n[Network]\nAddress=198.51.100.1/24\n"
)

func newBench(t *testing.T) *bench {
	t.Helper()
	state, root := t.TempDir(), t.TempDir()
	b := &bench{t: t, dir: Dir(state), now: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	b.net = &fakeNetworkd{root: root, devices: map[string]bool{"eth0": true}, state: "configured"}
	if err := os.MkdirAll(filepath.Join(root, netDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b.r = Realizer{
		StateDir: state, Root: root, Run: b.net.run, Now: func() time.Time { return b.now },
		Chown: func(path string, uid, gid int) error {
			b.net.chowns = append(b.net.chowns, strings.TrimPrefix(path, root)+" "+strings.Join([]string{itoa(uid), itoa(gid)}, ":"))
			return nil
		},
		VerifyWait: 2 * time.Second, LockWait: 2 * time.Second, Poll: 5 * time.Millisecond,
	}
	return b
}

func itoa(n int) string { data, _ := json.Marshal(n); return string(data) }

func (b *bench) gen(takeover bool, files map[string]string) int {
	b.t.Helper()
	return create(b.t, b.dir, Manifest{SavedVersion: "7", Takeover: takeover, ConfirmTimeoutSeconds: 120}, files)
}

// live makes generation 1 the confirmed, running configuration.
func (b *bench) live(files map[string]string) {
	b.t.Helper()
	id := b.gen(false, files)
	for name, body := range files {
		writeFile(b.t, filepath.Join(b.net.root, netDir, name), body)
	}
	writeFile(b.t, filepath.Join(b.dir, itoa(id), "state.json"), `{"state":"confirmed","previous":null}`)
	if err := writeJSON(filepath.Join(b.dir, "pointers.json"), Pointers{Confirmed: &id, Live: &id}); err != nil {
		b.t.Fatal(err)
	}
	b.net.apply()
}

func (b *bench) pointers() Pointers {
	b.t.Helper()
	p, err := readPointers(b.dir)
	if err != nil {
		b.t.Fatal(err)
	}
	return p
}

func (b *bench) read(id int) Generation {
	b.t.Helper()
	g, err := Read(b.dir, id)
	if err != nil {
		b.t.Fatal(err)
	}
	return g
}

func (b *bench) wantDisk(want map[string]string) {
	b.t.Helper()
	if got := b.net.netFiles(); !maps.Equal(got, want) {
		b.t.Fatalf("/run/systemd/network = %q\nwant %q", got, want)
	}
}

func (b *bench) wantPointers(confirmed, lastKnownGood, pending, live int) {
	b.t.Helper()
	p := b.pointers()
	if got := [4]int{deref(p.Confirmed), deref(p.LastKnownGood), deref(p.Pending), deref(p.Live)}; got != [4]int{confirmed, lastKnownGood, pending, live} {
		b.t.Fatalf("pointers (confirmed, lastKnownGood, pending, live) = %v, want %v", got, [4]int{confirmed, lastKnownGood, pending, live})
	}
}

var (
	one = map[string]string{"50-andaru-eth0.network": uplinkDHCP}
	two = map[string]string{"50-andaru-eth0.network": uplinkStatic, "50-andaru-vl0.netdev": vlanDevice, "50-andaru-vl0.network": vlanNetwork}
)

func TestActivateSwapsAndAwaits(t *testing.T) {
	b := newBench(t)
	b.live(one)
	writeFile(t, filepath.Join(b.net.root, netDir, "10-other.network"), "[Match]\nName=eth9\n")
	id := b.gen(false, two)
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	want := maps.Clone(two)
	want["10-other.network"] = "[Match]\nName=eth9\n"
	b.wantDisk(want)
	b.wantPointers(1, 0, 2, 2)
	g := b.read(id)
	if g.State.State != Awaiting || g.Deadline == nil || !g.Deadline.Equal(b.now.Add(120*time.Second)) || deref(g.Previous) != 1 {
		t.Fatalf("state = %+v", g.State)
	}
	if b.net.ran("networkctl reload") != 1 {
		t.Fatalf("argv = %q", b.net.argv)
	}
	info, _ := os.Stat(filepath.Join(b.net.root, netDir, "50-andaru-vl0.network"))
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v; networkd runs unprivileged and must read it", info.Mode())
	}
}

func TestActivateWaitsForReload(t *testing.T) {
	b := newBench(t)
	b.live(one)
	b.net.delay = 3
	id := b.gen(false, map[string]string{"50-andaru-eth0.network": uplinkStatic})
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	// One list call is the snapshot before the swap; the reload lands on the third after it.
	if n := b.net.ran("networkctl list"); n < 4 {
		t.Fatalf("verified after %d list calls, before networkd applied the new address", n)
	}

	b = newBench(t)
	b.live(one)
	b.net.delay = -1
	b.r.VerifyWait = 100 * time.Millisecond
	id = b.gen(false, map[string]string{"50-andaru-eth0.network": uplinkStatic})
	if err := b.r.Activate(t.Context(), id); err == nil {
		t.Fatal("activation succeeded although networkd never applied the file")
	}
	b.wantDisk(one)
	if g := b.read(id); g.State.State != Failed {
		t.Fatalf("state = %+v", g.State)
	}
}

func TestActivateFailsVerificationAndRestores(t *testing.T) {
	for name, breakIt := range map[string]func(*bench){
		"address gone":   func(b *bench) { b.net.noAddress = true },
		"route gone":     func(b *bench) { b.net.noRoute = true },
		"configuring":    func(b *bench) { b.net.state = "configuring" },
		"reload fails":   func(b *bench) { b.net.fail = "networkctl reload" },
		"file not bound": nil,
	} {
		t.Run(name, func(t *testing.T) {
			b := newBench(t)
			b.live(one)
			b.r.VerifyWait = 60 * time.Millisecond
			files := two
			if breakIt == nil {
				// A second file for eth0 sorts after the first and never binds.
				files = map[string]string{"50-andaru-eth0.network": uplinkDHCP, "50-andaru-zz.network": uplinkDHCP}
			}
			id := b.gen(false, files)
			// The world breaks only once the new files are in place.
			if breakIt != nil {
				b.r.failAt = func(step string) error {
					if step == "activate:pointers-pending" {
						breakIt(b)
					}
					return nil
				}
			}
			if err := b.r.Activate(t.Context(), id); err == nil {
				t.Fatal("activation succeeded")
			}
			b.wantDisk(one)
			b.wantPointers(1, 0, 0, 1)
			if g := b.read(id); g.State.State != Failed || g.Reason == "" {
				t.Fatalf("state = %+v", g.State)
			}
		})
	}
}

func TestActivateRefusals(t *testing.T) {
	b := newBench(t)
	b.live(one)
	first, second := b.gen(false, two), b.gen(false, one)
	if err := b.r.Activate(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	before := len(b.net.argv)
	if err := b.r.Activate(t.Context(), second); !errors.Is(err, ErrWrongState) {
		t.Fatalf("second pending: %v", err)
	}
	if err := b.r.Rollback(t.Context(), first, "requested"); err != nil {
		t.Fatal(err)
	}
	before = len(b.net.argv)
	if err := b.r.Activate(t.Context(), first); !errors.Is(err, ErrWrongState) {
		t.Fatalf("reuse: %v", err)
	}
	if len(b.net.argv) != before {
		t.Fatalf("a refused activation ran %q", b.net.argv[before:])
	}
	b.wantDisk(one)

	corrupt := b.gen(false, two)
	writeFile(t, filepath.Join(b.dir, itoa(corrupt), "files", "50-andaru-vl0.network"), "evil")
	if err := b.r.Activate(t.Context(), corrupt); err == nil || errors.Is(err, ErrWrongState) {
		t.Fatalf("corrupt generation: %v", err)
	}
	b.wantDisk(one)
	b.wantPointers(1, 0, 0, 1)
}

func TestRollbackRestoresPreviousAndDeletesDevices(t *testing.T) {
	b := newBench(t)
	b.live(one)
	id := b.gen(false, two)
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.timerFires(id); err != nil {
		t.Fatal(err)
	}
	b.wantDisk(one)
	b.wantPointers(1, 0, 0, 1)
	if g := b.read(id); g.State.State != RolledBack || g.Reason != "deadline" {
		t.Fatalf("state = %+v", g.State)
	}
	if b.net.ran("networkctl delete vl0") != 1 || b.net.ran("networkctl delete eth0") != 0 {
		t.Fatalf("argv = %q", b.net.argv)
	}
}

func TestRollbackSurvivesFailingReload(t *testing.T) {
	b := newBench(t)
	b.live(one)
	id := b.gen(false, two)
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.net.fail = "networkctl"
	if err := b.timerFires(id); err != nil {
		t.Fatal(err)
	}
	b.wantDisk(one)
	b.wantPointers(1, 0, 0, 1)
	if g := b.read(id); g.State.State != RolledBack || !strings.Contains(g.Reason, "injected failure") {
		t.Fatalf("state = %+v", g.State)
	}
}

func TestRollbackIsNoOpUnlessPending(t *testing.T) {
	b := newBench(t)
	b.live(one)
	rolled := b.gen(false, two)
	if err := b.r.Activate(t.Context(), rolled); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Rollback(t.Context(), rolled, "requested"); err != nil {
		t.Fatal(err)
	}
	never := b.gen(false, two)
	before := len(b.net.argv)
	for _, id := range []int{1, rolled, never, 99} {
		if err := b.timerFires(id); !errors.Is(err, ErrWrongState) {
			t.Fatalf("rollback %d: %v", id, err)
		}
	}
	if len(b.net.argv) != before {
		t.Fatalf("a refused rollback ran %q", b.net.argv[before:])
	}
	b.wantDisk(one)
	b.wantPointers(1, 0, 0, 1)
}

func TestRollbackFallsBack(t *testing.T) {
	b := newBench(t)
	b.live(one)
	lkg := b.gen(false, map[string]string{"50-andaru-eth0.network": uplinkStatic})
	id := b.gen(false, two)
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	p := b.pointers()
	p.LastKnownGood = &lkg
	if err := writeJSON(filepath.Join(b.dir, "pointers.json"), p); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(b.dir, "1", "files", "50-andaru-eth0.network"), "evil")
	if err := b.timerFires(id); err != nil {
		t.Fatal(err)
	}
	b.wantDisk(map[string]string{"50-andaru-eth0.network": uplinkStatic})
	if g := b.read(id); g.State.State != RolledBack || !strings.HasSuffix(g.Reason, "(restored last-known-good)") {
		t.Fatalf("state = %+v", g.State)
	}
	if p := b.pointers(); deref(p.Live) != lkg || p.Pending != nil {
		t.Fatalf("pointers = %+v", p)
	}
}

func TestRollbackWithNothingUsableLeavesFiles(t *testing.T) {
	b := newBench(t)
	b.live(one)
	id := b.gen(false, two)
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(b.dir, "1", "files", "50-andaru-eth0.network"), "evil")
	if err := b.timerFires(id); err == nil {
		t.Fatal("rollback reported success with nothing to restore")
	}
	// Removing the files would take the host off the network; what runs now at least works.
	b.wantDisk(two)
	if g := b.read(id); g.State.State != Failed || b.pointers().Pending != nil {
		t.Fatalf("state = %+v, pointers = %+v", g.State, b.pointers())
	}
}

func TestFirstApplyWithoutBaselineRollsBackToNothing(t *testing.T) {
	b := newBench(t)
	writeFile(t, filepath.Join(b.net.root, netDir, "10-other.network"), "[Match]\nName=eth9\n")
	b.net.apply()
	id := b.gen(false, one)
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.timerFires(id); err != nil {
		t.Fatal(err)
	}
	b.wantDisk(map[string]string{"10-other.network": "[Match]\nName=eth9\n"})
	b.wantPointers(0, 0, 0, 0)
	if g := b.read(id); g.State.State != RolledBack {
		t.Fatalf("state = %+v", g.State)
	}
}

func TestKillPoints(t *testing.T) {
	for _, step := range []string{
		"activate:state-activating", "activate:pointers-pending", "activate:state-awaiting", "activate:pointers-live",
		"rollback:pointers", "rollback:state",
	} {
		t.Run(step, func(t *testing.T) {
			b := newBench(t)
			b.live(one)
			id := b.gen(false, two)
			killed := errors.New("killed")
			b.r.failAt = func(at string) error {
				if at == step {
					return killed
				}
				return nil
			}
			err := b.r.Activate(t.Context(), id)
			if err == nil {
				err = b.r.Rollback(t.Context(), id, "requested")
			}
			if !errors.Is(err, killed) {
				t.Fatalf("the kill point was never reached: %v", err)
			}
			// The timer fires later, with nothing left of the killed process.
			b.r.failAt = nil
			if err := b.timerFires(id); err != nil && !errors.Is(err, ErrWrongState) {
				t.Fatal(err)
			}
			b.wantDisk(one)
			b.wantPointers(1, 0, 0, 1)
			if g := b.read(id); g.State.State != Failed && g.State.State != RolledBack {
				t.Fatalf("state = %+v", g.State)
			}
		})
	}
}

func TestLockSerialises(t *testing.T) {
	b := newBench(t)
	b.live(one)
	id := b.gen(false, two)
	held, err := os.OpenFile(filepath.Join(b.dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	b.r.LockWait = 150 * time.Millisecond
	if err := b.r.Activate(t.Context(), id); err == nil || errors.Is(err, ErrWrongState) {
		t.Fatalf("activation ran while another verb held the lock: %v", err)
	}
	b.wantDisk(one)
	if len(b.net.argv) != 0 {
		t.Fatalf("argv = %q", b.net.argv)
	}
}

func TestPlaceWritesBeforeItRemoves(t *testing.T) {
	b := newBench(t)
	b.live(one)
	renamed := map[string]string{"50-andaru-ens18.network": uplinkDHCP}
	id := b.gen(false, renamed)
	killed := errors.New("killed")
	b.r.failAt = func(step string) error {
		if step == "place:remove" {
			return killed
		}
		return nil
	}
	if err := b.r.Activate(t.Context(), id); err == nil {
		t.Fatal("the kill point was never reached")
	}
	// A networkd restart at this instant must still find a complete configuration.
	if got := b.net.netFiles(); got["50-andaru-eth0.network"] == "" {
		t.Fatalf("the old file was removed before the new set was complete: %v", got)
	}
}

func TestConfirmRepairsLivePointer(t *testing.T) {
	b := newBench(t)
	b.live(one)
	id := b.gen(false, two)
	b.r.failAt = func(step string) error {
		if step == "activate:pointers-live" {
			return errors.New("killed")
		}
		return nil
	}
	_ = b.r.Activate(t.Context(), id)
	b.r.failAt = nil
	if err := b.r.Confirm(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	// A stale live pointer would make the next apply replace the wrong files.
	b.wantPointers(id, 1, 0, id)
}

// timerFires is the rollback timer running after the confirmation deadline.
func (b *bench) timerFires(id int) error {
	b.now = b.now.Add(10 * time.Minute)
	return b.r.Rollback(b.t.Context(), id, "deadline")
}
