package host

import (
	_ "embed"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestReadRealizerFacts(t *testing.T) {
	root := t.TempDir()
	etc, run := filepath.Join(root, "etc"), filepath.Join(root, "run")
	for _, path := range []string{
		filepath.Join(etc, "99-admin.link"), filepath.Join(etc, "notes.txt"),
		filepath.Join(run, "10-netplan-eth0.network"), filepath.Join(run, "50-andaru-x.network"),
		// A drop-in is merged over Andaru's own file, from /etc or /run alike; Andaru never writes one.
		filepath.Join(etc, "50-andaru-x.network.d", "override.conf"), filepath.Join(etc, "50-andaru-x.network.d", "README"),
		filepath.Join(run, "50-andaru-x.network.d", "10-other.conf"), filepath.Join(etc, "unrelated.d", "x.conf"),
		filepath.Join(root, "pppd"), filepath.Join(root, "2.5.2", "pppoe.so"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(run, "x.network"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Andaru's prefix does not make a file in /etc its own: /etc always wins over /run.
	if err := os.WriteFile(filepath.Join(etc, "50-andaru-y.network"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readRealizerFacts(etc, run, filepath.Join(root, "pppd"), filepath.Join(root, "*", "pppoe.so"), filepath.Join(root, "ppp"))
	want := []string{
		filepath.Join(etc, "50-andaru-x.network.d", "override.conf"), filepath.Join(etc, "50-andaru-y.network"), filepath.Join(etc, "99-admin.link"),
		filepath.Join(run, "10-netplan-eth0.network"), filepath.Join(run, "50-andaru-x.network.d", "10-other.conf"),
	}
	if err != nil || !slices.Equal(got.ForeignFiles, want) || !got.PPPDaemon || !got.PPPoEPlugin || got.PPPDevice {
		t.Fatalf("%v %+v", err, got)
	}
	if err = os.WriteFile(filepath.Join(root, "ppp"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if kernel, err := readRealizerFacts(etc, run, filepath.Join(root, "pppd"), filepath.Join(root, "*", "pppoe.so"), filepath.Join(root, "ppp")); err != nil || !kernel.PPPDevice {
		t.Fatalf("present PPP device not observed: %v %+v", err, kernel)
	}
	empty, err := readRealizerFacts(filepath.Join(root, "absent"), filepath.Join(root, "absent2"), "/absent", "/absent/*", "/absent")
	if err != nil || empty.ForeignFiles == nil || len(empty.ForeignFiles) != 0 || empty.PPPDaemon || empty.PPPoEPlugin {
		t.Fatalf("missing directories are an empty host, not an error: %v %+v", err, empty)
	}
	if _, err = readRealizerFacts(filepath.Join(root, "pppd"), run, "", "", ""); err == nil {
		t.Fatal("an unreadable configuration directory was reported as empty")
	}
}

//go:embed testdata/networkctl-list.json
var networkctlList []byte

func TestParseManaged(t *testing.T) {
	got, err := ParseManaged(networkctlList)
	want := []Managed{{Name: "eth0", NetworkFile: "/run/systemd/network/10-netplan-eth0.network", State: "configured"}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("managed = %+v, %v; want only the link networkd configures", got, err)
	}
	if _, err := ParseManaged([]byte(`[]`)); err == nil {
		t.Fatal("a reply that is not networkctl's object was accepted")
	}
}

func TestReadBaseline(t *testing.T) {
	root := t.TempDir()
	for path, body := range map[string]string{
		"usr/sbin/netplan": "",
		"etc/systemd/system/sysinit.target.wants/andaru-network-boot.service": "",
		"lib/netplan/PLACEHOLDER":                     "",
		"usr/lib/netplan/PLACEHOLDER":                 "",
		"run/netplan/x.yaml":                          "network: {}\n",
		"run/systemd/network/10-netplan-eth0.network": "[Network]\nDHCP=ipv4\n",
		"run/systemd/network/10-netplan-eth1.network": "[Network]\nAddress=192.0.2.2/24\nDNS=1.1.1.1\n",
		"run/systemd/network/10-netplan-eth2.network": "[Network]\nDomains=example.org\n",
		"run/systemd/network/50-andaru-eth3.network":  "[Network]\nDNS=9.9.9.9\n",
	} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := readBaseline(root)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Netplan || got.Ifupdown || !got.BootUnit ||
		!slices.Equal(got.StrayYAML, []string{"/run/netplan/x.yaml"}) ||
		!slices.Equal(got.StaticDNS, []string{"/run/systemd/network/10-netplan-eth1.network", "/run/systemd/network/10-netplan-eth2.network"}) {
		t.Fatalf("baseline = %+v", got)
	}
	if err := os.MkdirAll(filepath.Join(root, "sbin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sbin/ifup"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	// A masked unit is a link to /dev/null: it resolves, and it never runs.
	wanted := filepath.Join(root, "etc/systemd/system/sysinit.target.wants/andaru-network-boot.service")
	if err := os.Remove(wanted); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/null", wanted); err != nil {
		t.Fatal(err)
	}
	if got, _ := readBaseline(root); got.BootUnit {
		t.Fatal("a masked boot unit counted as enabled")
	}
	// Or the mask sits beside a wants link that still points at the vendor unit.
	if err := os.Remove(wanted); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wanted, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/null", filepath.Join(root, "etc/systemd/system/andaru-network-boot.service")); err != nil {
		t.Fatal(err)
	}
	if got, _ := readBaseline(root); got.BootUnit {
		t.Fatal("a unit masked in /etc/systemd/system counted as enabled")
	}
	if got, _ := readBaseline(root); !got.Ifupdown {
		t.Fatal("ifupdown not seen")
	}
}

func TestResolvedInCharge(t *testing.T) {
	for name, tc := range map[string]struct {
		links map[string]string // path under root -> link target
		files []string
		want  bool
	}{
		"absolute link to the stub":       {map[string]string{"etc/resolv.conf": "/run/systemd/resolve/stub-resolv.conf"}, []string{"run/systemd/resolve/stub-resolv.conf"}, true},
		"relative link, Debian's default": {map[string]string{"etc/resolv.conf": "../run/systemd/resolve/stub-resolv.conf"}, []string{"run/systemd/resolve/stub-resolv.conf"}, true},
		"static stub in /usr/lib":         {map[string]string{"etc/resolv.conf": "/usr/lib/systemd/resolv.conf"}, []string{"usr/lib/systemd/resolv.conf"}, true},
		"chain of two links":              {map[string]string{"etc/resolv.conf": "resolv.link", "etc/resolv.link": "/run/systemd/resolve/resolv.conf"}, []string{"run/systemd/resolve/resolv.conf"}, true},
		"plain file":                      {nil, []string{"etc/resolv.conf"}, false},
		"link to another manager":         {map[string]string{"etc/resolv.conf": "/run/resolvconf/resolv.conf"}, []string{"run/resolvconf/resolv.conf"}, false},
		"dangling link":                   {map[string]string{"etc/resolv.conf": "/run/systemd/resolve/stub-resolv.conf"}, nil, false},
		"link loop":                       {map[string]string{"etc/resolv.conf": "loop", "etc/loop": "resolv.conf"}, nil, false},
		"no file":                         {nil, nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
				t.Fatal(err)
			}
			for _, path := range tc.files {
				full := filepath.Join(root, path)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			for path, target := range tc.links {
				if err := os.Symlink(target, filepath.Join(root, path)); err != nil {
					t.Fatal(err)
				}
			}
			if got := ResolvedInCharge(root); got != tc.want {
				t.Fatalf("ResolvedInCharge = %v, want %v", got, tc.want)
			}
		})
	}
}
