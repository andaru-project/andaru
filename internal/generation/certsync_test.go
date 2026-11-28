package generation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestSyncCertificates(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"/run/andaru-web", "/run/andaru-addons/proxy/certs"} {
		if err := os.MkdirAll(root+dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	owner := os.Getuid()
	if owner == 0 {
		owner = 4321 // as root, show the file takes the directory's owner, as Podman's :U leaves it
		if err := os.Chown(root+"/run/andaru-addons/proxy/certs", owner, owner); err != nil {
			t.Fatal(err)
		}
	}
	saved := lookupWebUser
	lookupWebUser = func() (int, int, error) { return os.Getuid(), os.Getgid(), nil }
	defer func() { lookupWebUser = saved }()
	var ran []string
	r := Realizer{Root: root, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		ran = append(ran, filepath.Base(name)+" "+strings.Join(args, " "))
		return nil, nil
	}}
	payload := CertSync{
		Web:    &CertPair{Cert: "CERT\n", Key: "KEY\n"},
		Addons: map[string]map[string]string{"proxy": {"web.crt": "CERT\n", "web.key": "KEY\n"}, "absent": {"web.pem": "X"}},
	}
	if err := r.SyncCertificates(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{WebCertificate: "CERT\n", WebKey: "KEY\n", "/run/andaru-addons/proxy/certs/web.key": "KEY\n"} {
		info, err := os.Stat(root + path)
		got, _ := os.ReadFile(root + path)
		if err != nil || info.Mode().Perm() != 0o400 || string(got) != want {
			t.Fatalf("%s: %v %v %q", path, err, info, got)
		}
	}
	if info, _ := os.Stat(root + "/run/andaru-addons/proxy/certs/web.key"); int(info.Sys().(*syscall.Stat_t).Uid) != owner {
		t.Fatal("add-on certificate did not keep its directory's owner")
	}
	if _, err := os.Stat(root + "/run/andaru-addons/absent"); err == nil {
		t.Fatal("an add-on that is not realized got a directory")
	}
	if len(ran) != 1 || ran[0] != "systemctl try-reload-or-restart andaru-web.service" {
		t.Fatalf("ran %q", ran)
	}
	// Nothing changed: nothing is written or reloaded.
	ran = nil
	if err := r.SyncCertificates(context.Background(), payload); err != nil || len(ran) != 0 {
		t.Fatalf("unchanged sync: %v %q", err, ran)
	}
	// Unbound, the managed certificate goes and andaru-web reloads onto its bootstrap one.
	if err := r.SyncCertificates(context.Background(), CertSync{}); err != nil || len(ran) != 1 {
		t.Fatalf("clear: %v %q", err, ran)
	}
	if _, err := os.Stat(root + WebKey); err == nil {
		t.Fatal("managed key kept")
	}
	for _, bad := range []CertSync{
		{Addons: map[string]map[string]string{"proxy": {"../escape.crt": "x"}}},
		{Addons: map[string]map[string]string{"Bad/Slug": {"web.crt": "x"}}},
		{Web: &CertPair{Cert: "x"}},
	} {
		if err := r.SyncCertificates(context.Background(), bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}
