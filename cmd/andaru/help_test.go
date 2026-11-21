package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func checkWidth(t *testing.T, name, text string) {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if len(line) > 80 {
			t.Errorf("%s: line over 80 columns: %q", name, line)
		}
	}
}

func TestHelpExitsZeroOnStdout(t *testing.T) {
	for _, row := range []struct {
		args       []string
		want, omit string
	}{
		{[]string{"--help"}, "  realize  ", ""},
		{[]string{"-h"}, "Run 'andaru <command> --help'", ""},
		{[]string{"help"}, "  ctl  ", ""},
		{[]string{"engine", "--help"}, "--web-origin ORIGIN", ""},
		{[]string{"ctl", "--help"}, "  factory-reset  Erase", "--saved-version"},
		{[]string{"ctl", "-h"}, "  config         Read and change", ""},
		{[]string{"ctl", "help"}, "Run 'andaru ctl COMMAND --help'", ""},
		{[]string{"ctl", "--socket", "/run/x.sock", "help", "audit"}, "andaru ctl audit list", "tokens"},
		{[]string{"ctl", "config", "--help"}, "Singletons: management, nat/outbound, dns", "applies"},
		{[]string{"ctl", "config", "replace", "--version", "3", "zones", "-h"}, "--version V", ""},
		{[]string{"ctl", "applies", "--help"}, "andaru ctl applies get|confirm|rollback ID", "users"},
		{[]string{"ctl", "applies", "get", "-h"}, "rollback  Undo", ""},
		{[]string{"ctl", "users", "bootstrap-admin", "--help"}, "bootstrap-admin  Create the first administrator", "tokens create"},
		{[]string{"ctl", "users", "create", "--disabled", "--help"}, "--revision N", ""},
		{[]string{"ctl", "backup", "--help"}, "--include-credentials  include user password credentials", "users list"},
		{[]string{"ctl", "backup", "restore", "--input=x.zip", "--help"}, "--operation-id ID", ""},
		{[]string{"ctl", "versions", "list", "--help"}, "--limit N   maximum saved versions, 1-100 (default 50)", ""},
		{[]string{"realize", "--help"}, "rollback N REASON", ""},
		{[]string{"realize", "--state-dir", "/srv", "-h"}, "factory-reset", ""},
		{[]string{"realize", "rollback", "--help"}, "confirm N", ""},
	} {
		var out, diag bytes.Buffer
		code := run(context.Background(), row.args, strings.NewReader(""), &out, &diag)
		if code != 0 || diag.Len() != 0 || !strings.Contains(out.String(), row.want) || row.omit != "" && strings.Contains(out.String(), row.omit) {
			t.Fatalf("%q: exit=%d stderr=%q stdout=%q", row.args, code, diag.String(), out.String())
		}
		checkWidth(t, strings.Join(row.args, " "), out.String())
	}
}

func TestUsageErrorsShowTheCommandAndAHint(t *testing.T) {
	for _, row := range []struct {
		args       []string
		want, hint string
	}{
		{nil, "usage: andaru engine", "Run 'andaru --help' for details."},
		{[]string{"unknown"}, "andaru ctl [--socket PATH] COMMAND", "Run 'andaru --help' for details."},
		{[]string{"engine", "extra"}, "usage: andaru engine", "Run 'andaru engine --help' for details."},
		{[]string{"engine", "--bogus"}, "usage: andaru engine", "Run 'andaru engine --help' for details."},
		{[]string{"ctl"}, "usage: andaru ctl [--socket PATH] COMMAND", "Run 'andaru ctl --help' for the list of commands."},
		{[]string{"ctl", "unknown"}, "usage: andaru ctl [--socket PATH] COMMAND", "Run 'andaru ctl --help' for the list of commands."},
		{[]string{"ctl", "applies", "get"}, "usage: andaru ctl applies list", "Run 'andaru ctl applies --help' for details."},
		{[]string{"ctl", "config", "list", "unknown"}, "usage: andaru ctl config show", "Run 'andaru ctl config --help' for details."},
		{[]string{"ctl", "preview", "network", "--bogus"}, "usage: andaru ctl preview network", "Run 'andaru ctl preview --help' for details."},
		{[]string{"ctl", "users", "list", "--limit", "0"}, "usage: andaru ctl users bootstrap-admin", "Run 'andaru ctl users --help' for details."},
		{[]string{"ctl", "tokens"}, "usage: andaru ctl tokens create", "Run 'andaru ctl tokens --help' for details."},
		{[]string{"ctl", "audit", "show"}, "usage: andaru ctl audit list", "Run 'andaru ctl audit --help' for details."},
		{[]string{"ctl", "backup", "create"}, "usage: andaru ctl backup create", "Run 'andaru ctl backup --help' for details."},
		{[]string{"ctl", "kept"}, "usage: andaru ctl kept store-recovery", "Run 'andaru ctl kept --help' for details."},
		{[]string{"ctl", "factory-reset", "go"}, "usage: andaru ctl factory-reset run|abort|status", "Run 'andaru ctl factory-reset --help' for details."},
		{[]string{"realize", "activate"}, "usage: andaru realize [--state-dir PATH] activate|confirm N", "Run 'andaru realize --help' for details."},
	} {
		var out, diag bytes.Buffer
		code := run(context.Background(), row.args, strings.NewReader(""), &out, &diag)
		if code != 2 || out.Len() != 0 || !strings.Contains(diag.String(), row.want) || !strings.HasSuffix(diag.String(), row.hint+"\n") {
			t.Fatalf("%q: exit=%d stdout=%q stderr=%q", row.args, code, out.String(), diag.String())
		}
		checkWidth(t, strings.Join(row.args, " "), diag.String())
	}
}

func TestEveryHelpTextFitsIn80Columns(t *testing.T) {
	for _, text := range []string{topHelp, engineHelp, realizeHelp} {
		checkWidth(t, "top-level help", text)
	}
	for _, c := range ctlCommands {
		var out bytes.Buffer
		ctlCommandHelp(&out, c.name)
		checkWidth(t, c.name, out.String())
	}
}

func TestHelpFlagGivenAsAValueIsNotHelp(t *testing.T) {
	for args, want := range map[string]bool{
		"create --display-name -h":     false,
		"restore --input --help":       false,
		"create --disabled -h":         true,
		"restore --input=x.zip --help": true,
		"get --help":                   true,
		"--help":                       true,
		"replace --version 3 zones ID": false,
	} {
		if got := helpRequested(strings.Fields(args)); got != want {
			t.Errorf("helpRequested(%q) = %v", args, got)
		}
	}
	// A rollback REASON is free text, even one that looks like a flag.
	if realizeHelpRequested([]string{"rollback", "3", "--help"}) {
		t.Error("a rollback reason became a help request")
	}
}
