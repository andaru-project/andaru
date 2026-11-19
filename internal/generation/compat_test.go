package generation

import (
	"embed"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// v050 holds the exact bytes the v0.5.0-a.1 image writes, generated with its writers (writeJSON of Pointers, State,
// Manifest and baselineManifest). Boxes in the field carry these files, and older images read them after a rollback.
//
//go:embed testdata/v0.5.0-a.1
var v050 embed.FS

func (b *bench) oldFile(name, to string) {
	b.t.Helper()
	data, err := v050.ReadFile("testdata/v0.5.0-a.1/" + name)
	if err != nil {
		b.t.Fatal(err)
	}
	writeFile(b.t, filepath.Join(b.dir, to), string(data))
}

func TestV050TreeStillLoads(t *testing.T) {
	b := newBench(t)
	b.oldFile("pointers.json", "pointers.json")
	b.oldFile("manifest.json", "1/manifest.json")
	b.oldFile("state.json", "1/state.json")
	writeFile(t, filepath.Join(b.dir, "1", "files", "50-andaru-eth0.network"), uplinkDHCP)
	p, err := ReadPointers(b.dir)
	if err != nil || deref(p.Confirmed) != 1 || deref(p.Live) != 1 {
		t.Fatalf("pointers = %+v, %v", p, err)
	}
	g, err := Read(b.dir, 1)
	if err != nil || g.SavedVersion != "7" || g.State.State != Confirmed || g.IdentityRevision != 1 || len(g.Files) != 1 || g.verify(b.dir) != nil {
		t.Fatalf("generation = %+v, %v", g, err)
	}
	if o, err := b.r.ReadOwnership(); err != nil || !o.Live {
		t.Fatalf("ownership = %+v, %v", o, err)
	}
	b.reboot()
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.wantDisk(one)
	b.wantPointers(1, 0, 0, 1)
	// The obsolete key is read and never written again.
	if data, _ := os.ReadFile(filepath.Join(b.dir, "pointers.json")); strings.Contains(string(data), "takeover") {
		t.Fatalf("pointers.json = %s", data)
	}
}

// A reset tree is told apart by its baseline manifest's reset key. Debian-era reset trees also carry a takeover's
// kind, interfaces and nftables mask; the keys are ignored, and the tree is fenced like any other reset tree.
func TestV050ResetTreeStillLoads(t *testing.T) {
	debian := `{"kind":"ifupdown","files":[],"ifupdown":{"interfaces":[{"name":"ens18","permanentMac":"bc:24:11:38:71:37"}],"networkdWasActive":false,"connected":[]},"nftablesMasked":true,"reset":true}` + "\n"
	for name, manifest := range map[string]string{"image": "", "debian": debian} {
		t.Run(name, func(t *testing.T) {
			b := newBench(t)
			b.oldFile("reset-pointers.json", "pointers.json")
			b.oldFile("reset-baseline-manifest.json", "baseline/manifest.json")
			if manifest != "" {
				writeFile(t, filepath.Join(b.dir, "baseline", "manifest.json"), manifest)
			}
			if o, err := b.r.ReadOwnership(); err != nil || !o.Live || !b.r.resetTree() {
				t.Fatalf("ownership = %+v, %v", o, err)
			}
			if err := b.r.Boot(t.Context()); err == nil || !strings.Contains(err.Error(), "CONSOLE_REQUIRED") {
				t.Fatalf("boot = %v", err)
			}
			if b.net.table != denyAllComment || !b.exists(catchAllPath[1:]) {
				t.Fatalf("filter %q, catch-all %v", b.net.table, b.exists(catchAllPath[1:]))
			}
		})
	}
}

// A Debian-era takeover generation also names the baseline it took over; the key is read and ignored.
func TestDebianTakeoverGenerationStillLoads(t *testing.T) {
	b := newBench(t)
	data, err := v050.ReadFile("testdata/v0.5.0-a.1/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(b.dir, "1", "manifest.json"), strings.Replace(string(data), `"takeover":false`, `"takeover":true,"baseline":"netplan"`, 1))
	b.oldFile("state.json", "1/state.json")
	if g, err := Read(b.dir, 1); err != nil || g.SavedVersion != "7" {
		t.Fatalf("generation = %+v, %v", g, err)
	}
}
