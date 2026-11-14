//go:build integration

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestUpgradePrepareUnprivilegedHelper(t *testing.T) {
	if os.Getenv("ANDARU_UPGRADE_UNPRIVILEGED") != "1" {
		t.Skip("subprocess fixture")
	}
	if os.Geteuid() == 0 {
		t.Fatal("helper did not drop privileges")
	}
	var out, diag bytes.Buffer
	code := run(context.Background(), []string{"upgrade", "prepare"}, strings.NewReader(""), &out, &diag)
	if code != 1 || out.Len() != 0 || !strings.Contains(diag.String(), "root") {
		t.Fatalf("prepare privilege guard: code=%d stdout=%s stderr=%s", code, &out, &diag)
	}
}

func TestUpgradePrepareRejectsNonRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to drop subprocess credentials")
	}
	root, err := os.MkdirTemp("/var/tmp", "andaru-cli-privilege-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err = os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	target := filepath.Join(root, "cli.test")
	dst, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(dst, src); err != nil {
		dst.Close()
		t.Fatal(err)
	}
	if err = dst.Close(); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(target, "-test.run=^TestUpgradePrepareUnprivilegedHelper$")
	child.Env = append(os.Environ(), "ANDARU_UPGRADE_UNPRIVILEGED=1")
	child.Dir = "/"
	child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("unprivileged CLI: %v\n%s", err, out)
	}
}
