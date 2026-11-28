package generation

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	ipsA       = "ips_00000001-0000-4000-8000-000000000001"
	ipsB       = "ips_00000002-0000-4000-8000-000000000002"
	ipsCrt     = "crt_00000001-0000-4000-8000-000000000001"
	ipsCertPEM = ipsecDir + "/x509/" + ipsCrt + ".pem"
	ipsKey     = ipsecDir + "/private/" + ipsCrt + ".key"
)

func swanctlConf(conns ...string) string {
	out := "connections {\n"
	for _, c := range conns {
		out += "\t" + c + " {\n\t\tversion = 2\n\t\tchildren {\n\t\t\t" + c + " {\n\t\t\t}\n\t\t}\n\t}\n"
	}
	return out + "}\n"
}

func withIPsec(conf string) map[string]string {
	return map[string]string{
		"50-andaru-eth0.network":   uplinkStatic,
		"50-andaru-ipsec0.netdev":  "[NetDev]\nName=ipsec0\nKind=xfrm\n\n[Xfrm]\nInterfaceId=42\nIndependent=yes\n",
		"50-andaru-ipsec0.network": "[Match]\nName=ipsec0\n\n[Network]\nAddress=169.254.42.1/30\nIPv4Forwarding=yes\nIPv6Forwarding=yes\n",
		ipsecConf:                  conf,
		ipsCertPEM:                 "CERT\n",
		ipsKey:                     "KEY\n",
	}
}

// sasOf is swanctl --list-sas --pretty for an established IKE SA of each connection.
func sasOf(conns ...string) string {
	out := ""
	for i, c := range conns {
		out += "list-sa event {\n  " + c + " {\n    uniqueid = " + itoa(i+1) + "\n    state = ESTABLISHED\n  }\n}\n"
	}
	return out + "list-sas reply {\n}\n"
}

func TestIPsecPaths(t *testing.T) {
	for path, mode := range map[string]string{
		ipsecConf:  "0600",
		ipsKey:     "0400",
		ipsCertPEM: "0644",
		ipsecDir + "/x509ca/ca_00000001-0000-4000-8000-000000000001.pem": "0644",
	} {
		if !allowed(path) || modeFor(path) != mode {
			t.Errorf("%s: allowed %v mode %s", path, allowed(path), modeFor(path))
		}
	}
	for _, path := range []string{ipsecDir + "/other.conf", ipsecDir + "/x509/../swanctl.conf", ipsecDir + "/private/ca_00000001-0000-4000-8000-000000000001.key",
		ipsecDir + "/x509/crt_1.pem", "/etc/strongswan/swanctl/swanctl.conf"} {
		if allowed(path) {
			t.Errorf("%s allowed", path)
		}
	}
	if got := ipsecConnections([]byte(swanctlConf(ipsA, ipsB))); !slices.Equal(got, []string{ipsA, ipsB}) {
		t.Fatalf("connections %v", got)
	}
}

func TestIPsecLifecycle(t *testing.T) {
	b := newBench(t)
	b.live(one)
	first := withIPsec(swanctlConf(ipsA, ipsB))
	id := b.gen(first)
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if !b.net.strongswan || b.net.ran("systemctl start strongswan.service") != 1 || b.net.ran("swanctl --load-all") != 0 {
		t.Fatalf("strongSwan not started: %q", b.net.argv)
	}
	for _, dir := range []string{"x509", "x509ca", "private"} {
		if !b.exists(ipsecDir[1:] + "/" + dir) {
			t.Fatalf("%s/%s missing", ipsecDir, dir)
		}
	}
	for path, mode := range map[string]os.FileMode{ipsecConf: 0o600, ipsKey: 0o400, ipsCertPEM: 0o644} {
		if info, err := os.Stat(filepath.Join(b.net.root, path)); err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s: %v %v", path, info, err)
		}
	}
	if err := b.r.Confirm(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	// Running, it reloads, and the SAs of a connection no longer declared end; the XFRM device whose .netdev changed
	// is recreated before networkd reloads.
	b.net.sas = sasOf(ipsA, ipsB)
	b.net.argv = nil
	second := withIPsec(swanctlConf(ipsA))
	second["50-andaru-ipsec0.netdev"] = strings.Replace(second["50-andaru-ipsec0.netdev"], "InterfaceId=42", "InterfaceId=43", 1)
	id2 := b.gen(second)
	if err := b.r.Activate(t.Context(), id2); err != nil {
		t.Fatal(err)
	}
	if b.net.ran("swanctl --load-all --clear --file "+ipsecConf+" --noprompt") != 1 || b.net.ran("swanctl --terminate --ike "+ipsB) != 1 || b.net.ran("swanctl --terminate --ike "+ipsA) != 0 {
		t.Fatalf("reload: %q", b.net.argv)
	}
	if del, reload := b.index("networkctl delete ipsec0"), b.index("networkctl reload"); del < 0 || del > reload {
		t.Fatalf("ipsec0 not recreated before the reload: %q", b.net.argv)
	}
	// Rolled back, the first generation's connections load again.
	b.net.argv = nil
	if err := b.timerFires(id2); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(b.net.root, ipsecConf)); string(data) != first[ipsecConf] || b.net.ran("swanctl --load-all --clear") != 1 {
		t.Fatalf("rollback: %q %q", data, b.net.argv)
	}
	// Without a connection, strongSwan stops and its credentials go.
	b.net.argv = nil
	id3 := b.gen(one)
	if err := b.r.Activate(t.Context(), id3); err != nil {
		t.Fatal(err)
	}
	if b.net.strongswan || b.net.ran("systemctl stop strongswan.service") != 1 || b.net.ran("networkctl delete ipsec0") != 1 {
		t.Fatalf("not stopped: %q", b.net.argv)
	}
	for _, path := range []string{ipsecConf, ipsKey, ipsCertPEM, ipsecDir + "/private"} {
		if b.exists(path[1:]) {
			t.Fatalf("%s left behind", path)
		}
	}
}

// A strongSwan that refuses to start fails the activation, which restores the generation before.
func TestIPsecStartFailureRollsBack(t *testing.T) {
	b := newBench(t)
	b.live(one)
	b.net.fail = "systemctl start strongswan.service"
	id := b.gen(withIPsec(swanctlConf(ipsA)))
	if err := b.r.Activate(t.Context(), id); err == nil {
		t.Fatal("activation succeeded without strongSwan")
	}
	b.wantDisk(one)
	if b.exists(ipsecConf[1:]) {
		t.Fatal("swanctl.conf left behind")
	}
}

// A reset's teardown stops strongSwan, removes its files and deletes the XFRM device.
func TestIPsecTeardown(t *testing.T) {
	b := newBench(t)
	id := b.gen(withIPsec(swanctlConf(ipsA)))
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.net.argv = nil
	if err := b.r.Teardown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.net.strongswan || b.net.ran("systemctl stop strongswan.service") != 1 || b.net.ran("networkctl delete ipsec0") != 1 {
		t.Fatalf("teardown: %q", b.net.argv)
	}
	for _, path := range []string{ipsecConf, ipsKey, ipsecDir + "/x509"} {
		if b.exists(path[1:]) {
			t.Fatalf("%s left behind", path)
		}
	}
}

// Boot queues strongSwan behind itself, labelling its directory where SELinux is installed.
func TestIPsecBoot(t *testing.T) {
	b := newBench(t)
	writeFile(t, filepath.Join(b.net.root, restorecon), "")
	var ran []string
	r := Realizer{Root: b.net.root, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		ran = append(ran, filepath.Base(name)+" "+strings.Join(args, " "))
		return nil, nil
	}}
	if err := r.bootIPsec(t.Context(), []placed{{path: ipsecConf}}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ran, []string{"restorecon -RF " + ipsecDir, "systemctl start --no-block strongswan.service"}) {
		t.Fatalf("boot: %q", ran)
	}
	ran = nil
	if err := r.bootIPsec(t.Context(), []placed{{path: catchAllPath}}); err != nil || len(ran) != 0 {
		t.Fatalf("boot without IPsec: %q %v", ran, err)
	}
}

const prettySAs = `list-sa event {
  ips_00000001-0000-4000-8000-000000000001 {
    uniqueid = 3
    version = 2
    state = ESTABLISHED
    local-host = 203.0.113.2
    local-port = 4500
    local-id = C=CH, O=Example, CN=gw
    remote-host = 198.51.100.7
    established = 120
    rekey-time = 13000
    child-sas {
      ips_00000001-0000-4000-8000-000000000001-7 {
        name = ips_00000001-0000-4000-8000-000000000001
        uniqueid = 7
        reqid = 1
        state = INSTALLED
        bytes-in = 1024
        bytes-out = 2048
        rekey-time = 3000
        local-ts = [
          0.0.0.0/0
          ::/0
        ]
        remote-ts = [
          10.40.0.0/16
        ]
      }
    }
  }
}
list-sa event {
  ips_00000002-0000-4000-8000-000000000002 {
    uniqueid = 4
    state = CONNECTING
    local-host = 203.0.113.2
    remote-host = 192.0.2.9
    child-sas {
    }
  }
}
list-sas reply {
}
`

func TestParseSwanctlSAs(t *testing.T) {
	sas, err := ParseSwanctlSAs([]byte(prettySAs))
	if err != nil || len(sas) != 2 {
		t.Fatalf("%+v %v", sas, err)
	}
	a := sas[0]
	if a.Name != ipsA || a.UniqueID != "3" || a.State != "ESTABLISHED" || a.LocalHost != "203.0.113.2" || a.RemoteHost != "198.51.100.7" || a.EstablishedSec != 120 || len(a.ChildSAs) != 1 {
		t.Fatalf("ike %+v", a)
	}
	c := a.ChildSAs[0]
	if c.Name != ipsA || c.State != "INSTALLED" || c.BytesIn != 1024 || c.BytesOut != 2048 || c.RekeySec != 3000 || !slices.Equal(c.LocalTS, []string{"0.0.0.0/0", "::/0"}) || !slices.Equal(c.RemoteTS, []string{"10.40.0.0/16"}) {
		t.Fatalf("child %+v", c)
	}
	if b := sas[1]; b.State != "CONNECTING" || b.EstablishedSec != 0 || b.ChildSAs == nil || len(b.ChildSAs) != 0 {
		t.Fatalf("connecting %+v", b)
	}
	if sas, err := ParseSwanctlSAs([]byte("list-sas reply {\n}\n")); err != nil || sas == nil || len(sas) != 0 {
		t.Fatalf("none: %+v %v", sas, err)
	}
	for _, bad := range []string{"list-sa event {\n", "}\n", "list-sa event {\n  x {\n    local-ts = [\n", "list-sa event {\n  garbage\n}\n"} {
		if _, err := ParseSwanctlSAs([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestReadIPsecStatus(t *testing.T) {
	active := "active\n"
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch filepath.Base(name) + " " + strings.Join(args, " ") {
		case "systemctl is-active strongswan.service":
			if active != "active\n" {
				return []byte(active), errors.New("exit status 3")
			}
			return []byte(active), nil
		case "swanctl --list-conns --pretty":
			return []byte("list-conn event {\n  " + ipsA + " {\n    local_addrs = [\n      %any\n    ]\n    children {\n      " + ipsA + " {\n      }\n    }\n  }\n}\nlist-conn event {\n  " + ipsB + " {\n  }\n}\nlist-conns reply {\n}\n"), nil
		case "swanctl --list-sas --pretty":
			return []byte(prettySAs), nil
		}
		return nil, errors.New("unexpected " + name)
	}
	s, err := ReadIPsecStatus(t.Context(), run)
	if err != nil || !s.ServiceActive || !slices.Equal(s.Connections, []string{ipsA, ipsB}) || len(s.SAs) != 2 {
		t.Fatalf("%+v %v", s, err)
	}
	active = "inactive\n"
	if s, err = ReadIPsecStatus(t.Context(), run); err != nil || s.ServiceActive || s.Connections == nil || s.SAs == nil {
		t.Fatalf("stopped: %+v %v", s, err)
	}
}

func TestControlIPsec(t *testing.T) {
	var ran []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		ran = append(ran, filepath.Base(name)+" "+strings.Join(args, " "))
		return nil, nil
	}
	if err := ControlIPsec(t.Context(), run, "initiate", ipsA); err != nil {
		t.Fatal(err)
	}
	if err := ControlIPsec(t.Context(), run, "terminate", ipsA); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ran, []string{"swanctl --initiate --child " + ipsA + " --timeout 15", "swanctl --terminate --ike " + ipsA + " --timeout 15"}) {
		t.Fatalf("%q", ran)
	}
	for _, c := range [][2]string{{"initiate", "ips_1"}, {"initiate", "--all"}, {"reload", ipsA}, {"terminate", ipsA + " x"}} {
		if err := ControlIPsec(t.Context(), run, c[0], c[1]); err == nil {
			t.Errorf("accepted %v", c)
		}
	}
}

// cert-sync rewrites a changed IPsec certificate and key while strongSwan runs them, and has charon load them.
func TestSyncIPsecCertificates(t *testing.T) {
	root := t.TempDir()
	running := true
	var ran []string
	r := Realizer{Root: root, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		command := filepath.Base(name) + " " + strings.Join(args, " ")
		ran = append(ran, command)
		if command == "systemctl is-active --quiet strongswan.service" && !running {
			return nil, errors.New("inactive")
		}
		return nil, nil
	}}
	files := map[string]string{"x509/" + ipsCrt + ".pem": "CERT2\n", "private/" + ipsCrt + ".key": "KEY2\n"}
	// No generation runs strongSwan: nothing is placed.
	if err := r.SyncCertificates(t.Context(), CertSync{IPsec: files}); err != nil || len(ran) != 0 || len(must(os.ReadDir(root))) != 0 {
		t.Fatalf("placed without swanctl.conf: %q %v", ran, err)
	}
	writeFile(t, root+ipsecConf, "connections {\n}\n")
	if err := r.SyncCertificates(t.Context(), CertSync{IPsec: files}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ran, "swanctl --load-creds --file "+ipsecConf+" --noprompt") {
		t.Fatalf("not reloaded: %q", ran)
	}
	for path, mode := range map[string]os.FileMode{ipsCertPEM: 0o644, ipsKey: 0o400} {
		info, err := os.Stat(root + path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s: %v %v", path, info, err)
		}
	}
	// Unchanged, nothing reloads; stopped, nothing is told.
	ran = nil
	if err := r.SyncCertificates(t.Context(), CertSync{IPsec: files}); err != nil || len(ran) != 0 {
		t.Fatalf("unchanged: %q %v", ran, err)
	}
	running = false
	changed := maps.Clone(files)
	changed["private/"+ipsCrt+".key"] = "KEY3\n"
	if err := r.SyncCertificates(t.Context(), CertSync{IPsec: changed}); err != nil || slices.ContainsFunc(ran, func(c string) bool { return strings.HasPrefix(c, "swanctl") }) {
		t.Fatalf("stopped: %q %v", ran, err)
	}
	for _, bad := range []string{"swanctl.conf", "x509ca/ca_00000001-0000-4000-8000-000000000001.pem", "../x509/" + ipsCrt + ".pem", "private/x.key"} {
		if err := (CertSync{IPsec: map[string]string{bad: "X"}}).Validate(); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
