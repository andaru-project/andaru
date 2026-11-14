package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testDeb(t *testing.T, version, arch string) []byte {
	t.Helper()
	root := t.TempDir()
	control := filepath.Join(root, "pkg", "DEBIAN")
	if err := os.MkdirAll(control, 0755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("Package: andaru\nVersion: %s\nArchitecture: %s\nMaintainer: Test <test@example.invalid>\nDescription: upgrade artifact fixture\n", version, arch)
	if err := os.WriteFile(filepath.Join(control, "control"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "fixture.deb")
	if out, err := exec.Command("dpkg-deb", "--build", filepath.Dir(control), path).CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v %s", err, out)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func artifactMetadata(raw []byte, version, arch string) []byte {
	hash := sha256.Sum256(raw)
	return []byte(fmt.Sprintf("Package: andaru\nVersion: %s\nArchitecture: %s\nSize: %d\nSHA256: %s\n\n", version, arch, len(raw), hex.EncodeToString(hash[:])))
}

func artifactRunner(t *testing.T, stage string, raw, metadata []byte, downloadErr error) commandRunner {
	t.Helper()
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "apt-cache":
			return metadata, nil
		case "apt-get":
			if len(args) != 2 || args[0] != "download" || args[1] != "andaru:amd64=0.4.0~rc2-1" {
				return nil, fmt.Errorf("not the installed package: %q", args)
			}
			if downloadErr != nil {
				return nil, downloadErr
			}
			return nil, os.WriteFile(filepath.Join(stage, "download.deb"), raw, 0600)
		case "dpkg-deb":
			return exec.CommandContext(ctx, name, args...).Output()
		default:
			return nil, fmt.Errorf("unexpected command %s", name)
		}
	}
}

func TestRetainInstalledPackageNotCandidate(t *testing.T) {
	raw := testDeb(t, "0.4.0~rc2-1", "amd64")
	stage := t.TempDir()
	got, err := retainPackage(context.Background(), artifactRunner(t, stage, raw, artifactMetadata(raw, "0.4.0~rc2-1", "amd64"), nil), packageInfo{"0.4.0~rc2-1", "amd64"}, stage)
	if err != nil || !got.Available {
		t.Fatalf("artifact: %+v %v", got, err)
	}
	copied, err := os.ReadFile(got.Path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	if string(copied) != string(raw) || got.SHA256 != hex.EncodeToString(hash[:]) {
		t.Fatal("retained package differs from signed metadata")
	}
}

func TestOldRCUnavailable(t *testing.T) {
	for _, metadata := range []string{"", "Package: andaru\nVersion: 0.4.0~rc2-1\nArchitecture: amd64\n\n"} {
		run := func(context.Context, string, ...string) ([]byte, error) { return []byte(metadata), nil }
		got, err := retainPackage(context.Background(), run, packageInfo{"0.4.0~rc2-1", "amd64"}, t.TempDir())
		if err != nil || got.Available {
			t.Fatalf("invented prior package: %+v %v", got, err)
		}
	}
}

func TestWrongCachedPackageRefused(t *testing.T) {
	for _, tc := range []struct {
		name, version, arch string
		badHash             bool
	}{
		{"version", "0.4.0-1", "amd64", false},
		{"architecture", "0.4.0~rc2-1", "arm64", false},
		{"hash", "0.4.0~rc2-1", "amd64", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := testDeb(t, tc.version, tc.arch)
			stage := t.TempDir()
			meta := artifactMetadata(raw, "0.4.0~rc2-1", "amd64")
			if tc.badHash {
				sum := sha256.Sum256(raw)
				meta = []byte(strings.Replace(string(meta), hex.EncodeToString(sum[:]), strings.Repeat("0", 64), 1))
			}
			if err := os.WriteFile(filepath.Join(stage, "cached.deb"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			_, err := retainPackage(context.Background(), artifactRunner(t, stage, raw, meta, errors.New("must verify existing file")), packageInfo{"0.4.0~rc2-1", "amd64"}, stage)
			if err == nil {
				t.Fatal("accepted a wrong cached recovery package")
			}
		})
	}
}

func TestPackageDownloadFails(t *testing.T) {
	raw := testDeb(t, "0.4.0~rc2-1", "amd64")
	stage := t.TempDir()
	_, err := retainPackage(context.Background(), artifactRunner(t, stage, raw, artifactMetadata(raw, "0.4.0~rc2-1", "amd64"), errors.New("network unavailable")), packageInfo{"0.4.0~rc2-1", "amd64"}, stage)
	if err == nil {
		t.Fatal("download failure became success")
	}
}

func TestPackageVersionIsAnArgument(t *testing.T) {
	called := false
	_, err := retainPackage(context.Background(), func(context.Context, string, ...string) ([]byte, error) { called = true; return nil, nil }, packageInfo{"0.4.0-1;touch /tmp/pwn", "amd64"}, t.TempDir())
	if err == nil || called {
		t.Fatal("invalid package version reached a command")
	}
}
