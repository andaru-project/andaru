package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpgradeRejectsArgumentsBeforeExecution(t *testing.T) {
	for _, args := range [][]string{{"upgrade"}, {"upgrade", "install"}, {"upgrade", "check", "extra"}, {"upgrade", "--state-dir", "/tmp", "check"}} {
		var out, errOut bytes.Buffer
		if code := run(context.Background(), args, strings.NewReader(""), &out, &errOut); code != 2 || out.Len() != 0 || errOut.Len() == 0 {
			t.Fatalf("args=%q code=%d out=%q err=%q", args, code, out.String(), errOut.String())
		}
	}
}

func TestUpgradeCheckDoesNotNeedEngine(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("CLI package check requires root")
	}
	bin := t.TempDir()
	scripts := map[string]string{
		"apt-get":    "[ \"$*\" = '-o APT::Update::Error-Mode=any update' ] || exit 91\n",
		"dpkg-query": "printf 'install ok installed\\t0.4.0~rc2-1\\tamd64\\n'\n",
		"apt-cache": `cat <<'EOF'
andaru:
  Installed: 0.4.0~rc2-1
  Candidate: 0.4.0-1
  Version table:
     0.4.0-1 500
        500 https://apt.andaru.io trixie/main amd64 Packages
EOF
`,
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"upgrade", "check"}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d error=%s", code, &errOut)
	}
	var got struct{ Installed, Candidate, Repository string }
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Installed != "0.4.0~rc2-1" || got.Candidate != "0.4.0-1" || got.Repository != "https://apt.andaru.io" {
		t.Fatalf("wrong CLI update: %s", &out)
	}
}
