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
		ipsecDir + "/x509ca/" + ipsCrt + "-chain-0.pem":                  "0644",
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
	fps, _ := ipsecConnectionFingerprints([]placed{{path: ipsecConf, data: []byte(swanctlConf(ipsA, ipsB))}})
	if len(fps) != 2 || fps[ipsA] == "" || fps[ipsB] == "" {
		t.Fatalf("fingerprints %v", fps)
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
	for _, dir := range []string{"", "/x509", "/x509ca", "/private"} {
		info, err := os.Stat(filepath.Join(b.net.root, ipsecDir+dir))
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("%s%s: info=%v err=%v", ipsecDir, dir, info, err)
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
	state := "active\n"
	probeErr := error(nil)
	failReload := false
	var ran []string
	r := Realizer{Root: root, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		command := filepath.Base(name) + " " + strings.Join(args, " ")
		ran = append(ran, command)
		if command == "systemctl is-active strongswan.service" {
			if probeErr != nil {
				return nil, probeErr
			}
			if strings.TrimSpace(state) != "active" {
				return []byte(state), errors.New("exit status 3")
			}
			return []byte(state), nil
		}
		if strings.HasPrefix(command, "swanctl --load-all") && failReload {
			return nil, errors.New("vici timeout")
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
	if !slices.Contains(ran, "swanctl --load-all --clear --file "+ipsecConf+" --noprompt") {
		t.Fatalf("not reloaded: %q", ran)
	}
	for path, mode := range map[string]os.FileMode{ipsCertPEM: 0o644, ipsKey: 0o400} {
		info, err := os.Stat(root + path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s: %v %v", path, info, err)
		}
	}
	// Unchanged, nothing reloads.
	ran = nil
	if err := r.SyncCertificates(t.Context(), CertSync{IPsec: files}); err != nil || len(ran) != 0 {
		t.Fatalf("unchanged: %q %v", ran, err)
	}
	// Transient swanctl failure leaves .reload-pending so next retry reloads even without new file changes.
	failReload = true
	retryFiles := maps.Clone(files)
	retryFiles["x509ca/"+ipsCrt+"-chain-0.pem"] = "CHAIN1\n"
	retryFiles["x509ca/ca_00000001-0000-4000-8000-000000000001.pem"] = "CA2\n"
	if err := r.SyncCertificates(t.Context(), CertSync{IPsec: retryFiles}); err == nil {
		t.Fatal("expected transient swanctl error")
	}
	if _, err := os.Stat(root + ipsecReloadPending); err != nil {
		t.Fatalf("reload-pending marker missing: %v", err)
	}
	// Probe failure (no confirmed inactivity) returns error and keeps .reload-pending.
	failReload = false
	probeErr = errors.New("context deadline exceeded")
	if err := r.SyncCertificates(t.Context(), CertSync{IPsec: retryFiles}); err == nil {
		t.Fatal("expected probe error")
	}
	if _, err := os.Stat(root + ipsecReloadPending); err != nil {
		t.Fatalf("reload-pending cleared on probe failure: %v", err)
	}
	// Recovery succeeds and clears .reload-pending.
	probeErr = nil
	ran = nil
	if err := r.SyncCertificates(t.Context(), CertSync{IPsec: retryFiles}); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if !slices.Contains(ran, "swanctl --load-all --clear --file "+ipsecConf+" --noprompt") {
		t.Fatalf("retry did not reload swanctl: %q", ran)
	}
	if _, err := os.Stat(root + ipsecReloadPending); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reload-pending marker not cleared: %v", err)
	}
	// Removing chain-0 on renewal deletes stale chain file from disk.
	if err := r.SyncCertificates(t.Context(), CertSync{IPsec: files}); err != nil {
		t.Fatalf("chain shrink: %v", err)
	}
	if _, err := os.Stat(root + ipsecDir + "/x509ca/" + ipsCrt + "-chain-0.pem"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale chain file not removed: %v", err)
	}
	// Confirmed inactivity clears .reload-pending without invoking swanctl.
	state = "inactive\n"
	ran = nil
	changed := maps.Clone(files)
	changed["private/"+ipsCrt+".key"] = "KEY3\n"
	if err := r.SyncCertificates(t.Context(), CertSync{IPsec: changed}); err != nil || slices.ContainsFunc(ran, func(c string) bool { return strings.HasPrefix(c, "swanctl") }) {
		t.Fatalf("stopped: %q %v", ran, err)
	}
	if _, err := os.Stat(root + ipsecReloadPending); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reload-pending left after confirmed inactive: %v", err)
	}
	for _, bad := range []string{"swanctl.conf", "../x509/" + ipsCrt + ".pem", "private/x.key"} {
		if err := (CertSync{IPsec: map[string]string{bad: "X"}}).Validate(); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestParseViciHostileAndEmpty(t *testing.T) {
	// Empty value ("remote-id =") and hostile remote peer DN containing " {" or " = [" must not break parsing.
	raw := "list-sa event {\n" +
		"  " + ipsA + " {\n" +
		"    uniqueid = 7\n" +
		"    state = ESTABLISHED\n" +
		"    local-host = 203.0.113.1\n" +
		"    remote-host = 198.51.100.2\n" +
		"    remote-id = CN=evil { fake = [\n" +
		"    empty-field = \n" +
		"    established = 12\n" +
		"    child-sas {\n" +
		"      " + ipsA + "-1 {\n" +
		"        name = " + ipsA + "\n" +
		"        state = INSTALLED\n" +
		"        bytes-in = 10\n" +
		"        bytes-out = 20\n" +
		"        rekey-time = 300\n" +
		"        local-ts = [\n" +
		"          0.0.0.0/0\n" +
		"        ]\n" +
		"        remote-ts = [\n" +
		"          10.0.0.0/8\n" +
		"        ]\n" +
		"      }\n" +
		"    }\n" +
		"  }\n" +
		"}\n"
	sas, err := ParseSwanctlSAs([]byte(raw))
	if err != nil || len(sas) != 1 || sas[0].UniqueID != "7" || len(sas[0].ChildSAs) != 1 || sas[0].ChildSAs[0].BytesOut != 20 {
		t.Fatalf("hostile/empty vici: sas=%+v err=%v", sas, err)
	}
}

func TestIPsecChangedConnectionTerminatesAndReinitiates(t *testing.T) {
	b := newBench(t)
	b.live(one)
	conf1 := "connections {\n\t" + ipsA + " {\n\t\tif_id_in = 42\n\t\tchildren {\n\t\t\t" + ipsA + " {\n\t\t\t\tstart_action = start\n\t\t\t}\n\t\t}\n\t}\n}\n"
	conf2 := "connections {\n\t" + ipsA + " {\n\t\tif_id_in = 43\n\t\tchildren {\n\t\t\t" + ipsA + " {\n\t\t\t\tstart_action = start\n\t\t\t}\n\t\t}\n\t}\n}\n"
	id1 := b.gen(withIPsec(conf1))
	if err := b.r.Activate(t.Context(), id1); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), id1); err != nil {
		t.Fatal(err)
	}
	b.net.sas = sasOf(ipsA)
	b.net.argv = nil
	id2 := b.gen(withIPsec(conf2))
	if err := b.r.Activate(t.Context(), id2); err != nil {
		t.Fatal(err)
	}
	if b.net.ran("swanctl --terminate --ike "+ipsA+" --force --timeout 5") != 1 {
		t.Fatalf("changed connection SA not terminated: %q", b.net.argv)
	}
	if b.net.ran("swanctl --initiate --child "+ipsA+" --timeout 5") != 1 {
		t.Fatalf("changed start_action=start connection not re-initiated: %q", b.net.argv)
	}
}

func TestIPsecTeardownWithoutLivePointer(t *testing.T) {
	b := newBench(t)
	b.net.strongswan = true
	writeFile(t, b.net.root+ipsecConf, "connections {\n}\n")
	writeFile(t, b.net.root+ipsKey, "KEY\n")
	if err := b.r.Teardown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.net.strongswan || b.exists(ipsecConf[1:]) || b.exists(ipsKey[1:]) {
		t.Fatalf("teardown without live pointer left strongSwan/files: active=%v argv=%q", b.net.strongswan, b.net.argv)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestIPsecTeardownLeavesTmpfilesDirectoryAlone(t *testing.T) {
	b := newBench(t)
	// tmpfiles.d creates the empty directory at boot on every host, IPsec or not.
	if err := os.MkdirAll(b.net.root+ipsecDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Teardown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.net.ran("systemctl stop "+strongswanUnit) != 0 || !b.exists(ipsecDir[1:]) {
		t.Fatalf("teardown touched a host where IPsec never ran: %q", b.net.argv)
	}
}

func TestIPsecMalformedPendingTerminationsFailApply(t *testing.T) {
	b := newBench(t)
	b.live(one)
	conf := "connections {\n\t" + ipsA + " {\n\t\tchildren {\n\t\t\t" + ipsA + " {\n\t\t\t}\n\t\t}\n\t}\n}\n"
	id1 := b.gen(withIPsec(conf))
	if err := b.r.Activate(t.Context(), id1); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), id1); err != nil {
		t.Fatal(err)
	}
	// Unchanged fingerprints: only the marker says the SA must still end, so an unreadable one must not be dropped.
	writeFile(t, b.net.root+ipsecTerminatePending, "{not json\n")
	b.net.sas = sasOf(ipsA)
	if err := b.r.Activate(t.Context(), b.gen(withIPsec(conf))); err == nil {
		t.Fatal("malformed .terminate-pending accepted")
	} else if !strings.Contains(err.Error(), "pending IPsec terminations") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !b.exists(ipsecTerminatePending[1:]) {
		t.Fatal("malformed .terminate-pending removed")
	}
}
