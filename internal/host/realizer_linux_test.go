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
		"usr/sbin/netplan":                            "",
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
	if !got.Netplan || got.Ifupdown ||
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
	if got, _ := readBaseline(root); !got.Ifupdown {
		t.Fatal("ifupdown not seen")
	}
}
