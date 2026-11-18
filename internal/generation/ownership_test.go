package generation

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/host"
)

func ifupdownOwnership() baselineManifest {
	duid := func(b byte) *host.DHCPIdentity { return &host.DHCPIdentity{DUID: []byte{0, 4, b}, Hostname: "fw"} }
	return baselineManifest{Kind: "ifupdown", Files: []baselineFile{}, Ifupdown: &IfupdownBaseline{
		Interfaces: []host.IfupdownInterface{
			{Detected: host.Detected{Name: "eth0", PermanentMAC: "02:00:00:00:00:01"}, DHCP: true, Identity: duid(1)},
			{Detected: host.Detected{Name: "eth1", PermanentMAC: "02:00:00:00:00:02"}, Hotplug: true, DHCP: true, Identity: duid(2)},
		},
		NetworkdWasActive: true, Connected: []string{"eth0"},
	}}
}

// owned writes a confirmed takeover's pointers and baseline, with one generation and the netplan YAML it kept.
func owned(t *testing.T, b *bench, m baselineManifest) {
	t.Helper()
	id := b.gen(true, one)
	if err := writeJSON(filepath.Join(b.dir, "pointers.json"), Pointers{Confirmed: &id, Live: &id, Takeover: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(b.dir, "baseline", "netplan"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(b.dir, "baseline", "netplan", "50-cloud-init.yaml"), netplanYAML)
	if err := writeJSON(filepath.Join(b.dir, "baseline", "manifest.json"), m); err != nil {
		t.Fatal(err)
	}
}

// fresh is the same host after a wipe: an empty generations directory.
func fresh(t *testing.T, b *bench) *bench {
	t.Helper()
	if err := os.RemoveAll(b.dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOwnershipRoundTrip(t *testing.T) {
	for _, m := range []baselineManifest{{Kind: "netplan", Files: []baselineFile{}, NftablesMasked: true}, ifupdownOwnership()} {
		t.Run(m.Kind, func(t *testing.T) {
			b := newBench(t)
			owned(t, b, m)
			o, err := b.r.ReadOwnership()
			if err != nil {
				t.Fatal(err)
			}
			fresh(t, b)
			if err = b.r.WriteOwnership(o); err != nil {
				t.Fatal(err)
			}
			if p := b.pointers(); !p.Takeover || p.Confirmed != nil || p.Live != nil {
				t.Fatalf("pointers = %+v", p)
			}
			kind, ifupdown, err := ReadBaselineKind(b.dir)
			if err != nil || kind != m.Kind {
				t.Fatalf("kind = %q, %v", kind, err)
			}
			if b.r.ownsNftablesMask(true) == m.NftablesMasked {
				t.Fatalf("ownsNftablesMask = %v with nftablesMasked %v", !m.NftablesMasked, m.NftablesMasked)
			}
			if m.Ifupdown != nil {
				if ifupdown == nil || len(ifupdown.Interfaces) != 2 || len(ifupdown.Connected) != 0 {
					t.Fatalf("ifupdown = %+v", ifupdown)
				}
				for i, want := range m.Ifupdown.Interfaces {
					got := ifupdown.Interfaces[i]
					if got.Name != want.Name || got.Hotplug != want.Hotplug || got.Identity == nil || string(got.Identity.DUID) != string(want.Identity.DUID) {
						t.Fatalf("interface %d = %+v, want %+v", i, got, want)
					}
				}
			}
			set, captured, err := b.r.baseline()
			if err != nil || !captured || len(set) != 0 {
				t.Fatalf("baseline files = %d, captured %v, %v", len(set), captured, err)
			}
			if dirExists(filepath.Join(b.dir, "baseline", "netplan")) {
				t.Fatal("the netplan YAML copy survived")
			}
		})
	}
}

func dirExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestOwnershipWithoutTakeover(t *testing.T) {
	b := newBench(t)
	o, err := b.r.ReadOwnership()
	if err != nil || o.Takeover || o.Kind != "" {
		t.Fatalf("ownership = %+v, %v", o, err)
	}
	if err = b.r.WriteOwnership(o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(b.dir, "baseline")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a host never taken over got a baseline: %v", err)
	}
}

func TestOwnershipRefusesTakeoverWithoutBaseline(t *testing.T) {
	b := newBench(t)
	id := b.gen(true, one)
	if err := writeJSON(filepath.Join(b.dir, "pointers.json"), Pointers{Confirmed: &id, Takeover: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.r.ReadOwnership(); err == nil {
		t.Fatal("a takeover without its baseline was read as ownership")
	}
}

func TestOwnershipRefusesMissingIdentity(t *testing.T) {
	b := newBench(t)
	m := ifupdownOwnership()
	m.Ifupdown.Interfaces[1].Identity = nil
	owned(t, b, m)
	if _, err := b.r.ReadOwnership(); err == nil {
		t.Fatal("a DHCP interface without its DUID was read as ownership")
	}
}

func TestOwnershipRefusesNonEmpty(t *testing.T) {
	b := newBench(t)
	owned(t, b, ifupdownOwnership())
	o, err := b.r.ReadOwnership()
	if err != nil {
		t.Fatal(err)
	}
	if err = b.r.WriteOwnership(o); err == nil {
		t.Fatal("ownership was written over existing generations")
	}
}

func TestResetTreeNeverHandsBack(t *testing.T) {
	for _, withGeneration := range []bool{false, true} {
		b := newBench(t)
		owned(t, b, ifupdownOwnership())
		o, err := b.r.ReadOwnership()
		if err != nil {
			t.Fatal(err)
		}
		fresh(t, b)
		if err = b.r.WriteOwnership(o); err != nil {
			t.Fatal(err)
		}
		if withGeneration {
			// A confirmed generation that cannot be read: no candidate is usable.
			id := b.gen(false, one)
			if err := os.RemoveAll(filepath.Join(b.dir, itoa(id))); err != nil {
				t.Fatal(err)
			}
			p := b.pointers()
			p.Confirmed = &id
			if err := writeJSON(filepath.Join(b.dir, "pointers.json"), p); err != nil {
				t.Fatal(err)
			}
		}
		b.net.argv = nil
		err = b.r.Boot(t.Context())
		if err == nil || !strings.Contains(err.Error(), "CONSOLE_REQUIRED") {
			t.Fatalf("withGeneration=%v: boot = %v", withGeneration, err)
		}
		if b.net.ran("systemctl unmask") != 0 || b.net.ran("systemctl start --no-block networking") != 0 || b.net.ran("systemctl start --no-block ifup@") != 0 {
			t.Fatalf("withGeneration=%v: handed back: %q", withGeneration, b.net.argv)
		}
		if b.net.table != denyAllComment || !strings.Contains(b.net.script, "policy drop") {
			t.Fatalf("withGeneration=%v: filter %q: %s", withGeneration, b.net.table, b.net.script)
		}
		if slices.Contains(b.net.argv, "systemctl start --no-block nftables.service") {
			t.Fatalf("withGeneration=%v: started the host firewall", withGeneration)
		}
	}
}
