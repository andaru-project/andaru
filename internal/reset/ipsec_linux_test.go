package reset

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/andaru-project/andaru/internal/generation"
)

// A reset tears down the live generation's IPsec: strongSwan stops, its XFRM device goes and /run/andaru-ipsec is
// emptied of what the generation placed.
func TestResetStopsIPsec(t *testing.T) {
	b := newBox(t)
	files := map[string][]byte{
		"/run/systemd/network/50-andaru-ipsec0.netdev":                           []byte("[NetDev]\nName=ipsec0\nKind=xfrm\n\n[Xfrm]\nInterfaceId=42\nIndependent=yes\n"),
		"/run/andaru-ipsec/swanctl.conf":                                         []byte("connections {\n}\n"),
		"/run/andaru-ipsec/private/crt_00000001-0000-4000-8000-000000000001.key": []byte("KEY\n"),
	}
	m := generation.Manifest{SavedVersion: "1", ConfirmTimeoutSeconds: 120}
	for path := range files {
		mode := "0644"
		switch filepath.Ext(path) {
		case ".conf":
			mode = "0600"
		case ".key":
			mode = "0400"
		}
		m.Files = append(m.Files, generation.FileEntry{Path: path, Mode: mode})
		write(t, b.env.Realizer.Root+path, string(files[path]))
	}
	dir := generation.Dir(b.env.StateDir)
	id, err := generation.Create(dir, m, files)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, itoa(id), "state.json"), `{"state":"confirmed","previous":null}`)
	write(t, filepath.Join(dir, "pointers.json"), `{"confirmed":`+itoa(id)+`,"lastKnownGood":null,"pending":null,"live":`+itoa(id)+`}`)
	b.plan("")
	if op, err := Resume(context.Background(), b.env); err != nil || op.Phase != Up {
		t.Fatalf("phase %s: %v", op.Phase, err)
	}
	if !b.ran("systemctl stop strongswan.service") || !b.ran("networkctl delete ipsec0") {
		t.Fatalf("IPsec not torn down: %q", b.commands)
	}
	for path := range files {
		if _, err := os.Stat(b.env.Realizer.Root + path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s left behind: %v", path, err)
		}
	}
	if _, err := os.Stat(b.env.Realizer.Root + "/run/andaru-ipsec/private"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credentials directory left behind: %v", err)
	}
}
