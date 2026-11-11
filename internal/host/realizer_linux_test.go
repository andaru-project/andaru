package host

import (
	_ "embed"
	"net/netip"
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
	// The real wiring: the fact must come from the host's own resolv.conf.
	if err := os.MkdirAll(filepath.Join(root, "run/systemd/resolve"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "run/systemd/resolve/stub-resolv.conf"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../run/systemd/resolve/stub-resolv.conf", filepath.Join(etc, "resolv.conf")); err != nil {
		t.Fatal(err)
	}
	got, err := readRealizerFacts(etc, run, filepath.Join(root, "pppd"), filepath.Join(root, "*", "pppoe.so"), filepath.Join(root, "ppp"), root)
	if !got.ResolvedInCharge {
		t.Fatal("systemd-resolved manages resolv.conf, yet the facts say it does not")
	}
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
	if kernel, err := readRealizerFacts(etc, run, filepath.Join(root, "pppd"), filepath.Join(root, "*", "pppoe.so"), filepath.Join(root, "ppp"), root); err != nil || !kernel.PPPDevice {
		t.Fatalf("present PPP device not observed: %v %+v", err, kernel)
	}
	empty, err := readRealizerFacts(filepath.Join(root, "absent"), filepath.Join(root, "absent2"), "/absent", "/absent/*", "/absent", filepath.Join(root, "absent"))
	if err != nil || empty.ForeignFiles == nil || len(empty.ForeignFiles) != 0 || empty.PPPDaemon || empty.PPPoEPlugin || empty.ResolvedInCharge {
		t.Fatalf("missing directories are an empty host, not an error: %v %+v", err, empty)
	}
	if _, err = readRealizerFacts(filepath.Join(root, "pppd"), run, "", "", "", root); err == nil {
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
		// A link to resolved's static file says nothing about resolved running; only its /run files do.
		"static stub in /usr/lib":            {map[string]string{"etc/resolv.conf": "/usr/lib/systemd/resolv.conf"}, []string{"usr/lib/systemd/resolv.conf"}, false},
		"directory where the stub should be": {map[string]string{"etc/resolv.conf": "/run/systemd/resolve/stub-resolv.conf"}, []string{"run/systemd/resolve/stub-resolv.conf/x"}, false},
		"chain of two links":                 {map[string]string{"etc/resolv.conf": "resolv.link", "etc/resolv.link": "/run/systemd/resolve/resolv.conf"}, []string{"run/systemd/resolve/resolv.conf"}, true},
		"plain file":                         {nil, []string{"etc/resolv.conf"}, false},
		"link to another manager":            {map[string]string{"etc/resolv.conf": "/run/resolvconf/resolv.conf"}, []string{"run/resolvconf/resolv.conf"}, false},
		"dangling link":                      {map[string]string{"etc/resolv.conf": "/run/systemd/resolve/stub-resolv.conf"}, nil, false},
		"link loop":                          {map[string]string{"etc/resolv.conf": "loop", "etc/loop": "resolv.conf"}, nil, false},
		"no file":                            {nil, nil, false},
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

//go:embed testdata/resolvectl-dns.txt
var resolvectlDNS []byte

func TestParseResolvedDNS(t *testing.T) {
	got, err := ParseResolvedDNS(resolvectlDNS)
	want := []netip.Addr{netip.MustParseAddr("172.16.53.53"), netip.MustParseAddr("fd00:53:53:53:53:53:53:53")}
	if err != nil || len(got) != 1 || !slices.Equal(got["eth0"], want) {
		t.Fatalf("servers = %v, %v", got, err)
	}
	got, err = ParseResolvedDNS([]byte("Global: 192.0.2.1\nLink 2 (eth0):\nLink 3 (vl0aa29874febd): 192.0.2.53\n"))
	if err != nil || len(got) != 2 || len(got["eth0"]) != 0 || !slices.Equal(got["vl0aa29874febd"], []netip.Addr{netip.MustParseAddr("192.0.2.53")}) {
		t.Fatalf("servers = %v, %v", got, err)
	}
	// Another link may carry servers in forms Andaru never writes; that must not hide the link being verified.
	got, err = ParseResolvedDNS([]byte("Link 2 (eth0): 192.0.2.53\nLink 3 (eth1): 1.1.1.1#one.one.one.one 192.0.2.9:5353\nLink 4 (a)b): 192.0.2.54\n"))
	if err != nil || !slices.Equal(got["eth0"], []netip.Addr{netip.MustParseAddr("192.0.2.53")}) || !slices.Equal(got["a)b"], []netip.Addr{netip.MustParseAddr("192.0.2.54")}) {
		t.Fatalf("servers = %v, %v", got, err)
	}
	if slices.Equal(got["eth1"], []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("192.0.2.9")}) || len(got["eth1"]) != 2 {
		t.Fatalf("an unreadable server must stay unequal to any declared one: %v", got["eth1"])
	}
	for _, bad := range []string{"eth0: 192.0.2.53\n", "Link 2 eth0: 192.0.2.53\n"} {
		if _, err := ParseResolvedDNS([]byte(bad)); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

//go:embed testdata/resolvectl-dns-wrapped.txt
var resolvectlWrapped []byte

// resolvectl wraps a long server list onto indented lines, at 80 columns when it has no terminal.
func TestParseResolvedDNSWrapped(t *testing.T) {
	got, err := ParseResolvedDNS(resolvectlWrapped)
	var want []netip.Addr
	for _, s := range []string{"2001:db8:1111:2222:3333:4444:5555:1", "2001:db8:1111:2222:3333:4444:5555:2", "2001:db8:1111:2222:3333:4444:5555:3", "2001:db8:1111:2222:3333:4444:5555:4", "192.0.2.53"} {
		want = append(want, netip.MustParseAddr(s))
	}
	if err != nil || !slices.Equal(got["d0"], want) || len(got["eth0"]) != 2 {
		t.Fatalf("servers = %v, %v", got, err)
	}
	// A wrapped global list belongs to no interface.
	got, err = ParseResolvedDNS([]byte("Global: 192.0.2.1\n        192.0.2.2\nLink 2 (eth0): 192.0.2.53\n"))
	if err != nil || len(got) != 1 || !slices.Equal(got["eth0"], []netip.Addr{netip.MustParseAddr("192.0.2.53")}) {
		t.Fatalf("servers = %v, %v", got, err)
	}
	if _, err := ParseResolvedDNS([]byte("        192.0.2.2\n")); err == nil {
		t.Fatal("a continuation with nothing to continue was accepted")
	}
}
