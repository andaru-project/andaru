package main

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
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
		{[]string{"-help"}, "  realize  ", ""},
		{[]string{"help"}, "  ctl  ", ""},
		{[]string{"help", "--help"}, "  realize  ", ""},
		{[]string{"help", "help"}, "  realize  ", ""},
		{[]string{"help", "ctl", "config"}, "Singletons: management, nat/outbound, dns", "applies"},
		{[]string{"ctl", "versions", "list", "--help=true"}, "andaru ctl versions", ""},
		{[]string{"realize", "--help", "--state-dir", "/srv"}, "rollback N REASON", ""},
		{[]string{"realize", "-h", "--state-dir", "/srv", "boot"}, "rollback N REASON", ""},
		{[]string{"realize", "--help=true"}, "rollback N REASON", ""},
		{[]string{"help", "ctl"}, "  factory-reset  Erase", "  realize  "},
		{[]string{"help", "engine"}, "--web-uid UID", "  realize  "},
		{[]string{"help", "realize"}, "rollback N REASON", "  ctl  "},
		{[]string{"engine", "--help"}, "  --web-origin HTTPS_ORIGIN\n", ""},
		{[]string{"engine", "-help"}, "(default /var/lib/andaru)", ""},
		{[]string{"ctl", "--help"}, "  factory-reset  Erase", "--saved-version"},
		{[]string{"ctl", "-h"}, "  config         Read and change", ""},
		{[]string{"ctl", "-help"}, "(default /run/andaru/engine.sock)", ""},
		{[]string{"ctl", "help"}, "Run 'andaru ctl COMMAND --help'", ""},
		{[]string{"ctl", "help", "--help"}, "Run 'andaru ctl COMMAND --help'", ""},
		{[]string{"ctl", "--socket", "/run/x.sock", "help", "audit"}, "andaru ctl audit list", "tokens"},
		{[]string{"ctl", "config", "--help"}, "Singletons: management, nat/outbound, dns", "applies"},
		{[]string{"ctl", "config", "replace", "--version", "3", "zones", "-h"}, "--version V", ""},
		{[]string{"ctl", "config", "replace", "--version", "-help"}, "andaru ctl config order LIST --version V ID...", ""},
		{[]string{"ctl", "applies", "--help"}, "andaru ctl applies get|confirm|rollback ID", "users"},
		{[]string{"ctl", "applies", "get", "-h"}, "rollback  Undo", ""},
		{[]string{"ctl", "users", "bootstrap-admin", "--help"}, "bootstrap-admin  Create the first administrator", "tokens create"},
		{[]string{"ctl", "users", "create", "--disabled", "--help"}, "--revision N", ""},
		{[]string{"ctl", "users", "create", "--display-name", "-h"}, "display name, at most 128 characters", ""},
		{[]string{"ctl", "tokens", "create", "--no-expiry", "-help"}, "token name, 1-128 characters", ""},
		{[]string{"ctl", "backup", "--help"}, "--include-credentials  include user password credentials", "users list"},
		{[]string{"ctl", "backup", "restore", "--input", "--help"}, "--operation-id ID", ""},
		{[]string{"ctl", "versions", "list", "--help"}, "--limit N   maximum saved versions, 1-100 (default 50)", ""},
		{[]string{"ctl", "factory-reset", "--help"}, "stored recovery network,\n          else on the last LAN an administrator used", ""},
		{[]string{"realize", "--help"}, "rollback N REASON", ""},
		{[]string{"realize", "help"}, "rollback N REASON", ""},
		{[]string{"realize", "--state-dir", "/srv", "-h"}, "factory-reset", ""},
		{[]string{"realize", "--state-dir", "--help", "boot"}, "factory-reset", ""},
		{[]string{"realize", "rollback", "--help"}, "confirm N", ""},
		{[]string{"realize", "boot", "-help"}, "confirm N", ""},
	} {
		var out, diag bytes.Buffer
		code := run(context.Background(), row.args, strings.NewReader(""), &out, &diag)
		if code != 0 || diag.Len() != 0 || !strings.Contains(out.String(), row.want) || row.omit != "" && strings.Contains(out.String(), row.omit) {
			t.Fatalf("%q: exit=%d stderr=%q stdout=%q", row.args, code, diag.String(), out.String())
		}
	}
}

func TestUsageErrorsShowTheCommandAndAHint(t *testing.T) {
	for _, row := range []struct {
		args       []string
		want, hint string
	}{
		{nil, "usage: andaru engine", "Run 'andaru --help' for details."},
		{[]string{"unknown"}, "andaru ctl [--socket PATH] COMMAND", "Run 'andaru --help' for details."},
		{[]string{"help", "bogus"}, "usage: andaru engine", "Run 'andaru --help' for details."},
		{[]string{"engine", "extra"}, "usage: andaru engine", "Run 'andaru engine --help' for details."},
		{[]string{"engine", "--bogus"}, "usage: andaru engine", "Run 'andaru engine --help' for details."},
		{[]string{"ctl"}, "usage: andaru ctl [--socket PATH] COMMAND", "Run 'andaru ctl --help' for details."},
		{[]string{"ctl", "unknown"}, "usage: andaru ctl [--socket PATH] COMMAND", "Run 'andaru ctl --help' for details."},
		{[]string{"ctl", "bogus", "--help"}, "usage: andaru ctl [--socket PATH] COMMAND", "Run 'andaru ctl --help' for details."},
		{[]string{"ctl", "help", "bogus"}, "usage: andaru ctl [--socket PATH] COMMAND", "Run 'andaru ctl --help' for details."},
		{[]string{"ctl", "applies", "get"}, "usage: andaru ctl applies list", "Run 'andaru ctl applies --help' for details."},
		{[]string{"ctl", "applies", "get", "help"}, "usage: andaru ctl applies list", "Run 'andaru ctl applies --help' for details."},
		{[]string{"ctl", "config", "list", "unknown"}, "usage: andaru ctl config show", "Run 'andaru ctl config --help' for details."},
		{[]string{"ctl", "preview", "network", "--bogus"}, "usage: andaru ctl preview network", "Run 'andaru ctl preview --help' for details."},
		{[]string{"ctl", "users", "list", "--limit", "0"}, "usage: andaru ctl users bootstrap-admin", "Run 'andaru ctl users --help' for details."},
		{[]string{"ctl", "tokens"}, "usage: andaru ctl tokens create", "Run 'andaru ctl tokens --help' for details."},
		{[]string{"ctl", "audit", "show"}, "usage: andaru ctl audit list", "Run 'andaru ctl audit --help' for details."},
		{[]string{"ctl", "backup", "create"}, "usage: andaru ctl backup create", "Run 'andaru ctl backup --help' for details."},
		{[]string{"ctl", "kept"}, "usage: andaru ctl kept store-recovery", "Run 'andaru ctl kept --help' for details."},
		{[]string{"ctl", "factory-reset", "go"}, "usage: andaru ctl factory-reset run|abort|status", "Run 'andaru ctl factory-reset --help' for details."},
		{[]string{"realize", "activate"}, "usage: andaru realize [OPTIONS] activate|confirm N", "Run 'andaru realize --help' for details."},
		{[]string{"realize", "bogus", "--help"}, "usage: andaru realize [OPTIONS] activate|confirm N", "Run 'andaru realize --help' for details."},
		{[]string{"realize", "--help", "bogus"}, "usage: andaru realize [OPTIONS] activate|confirm N", "Run 'andaru realize --help' for details."},
	} {
		var out, diag bytes.Buffer
		code := run(context.Background(), row.args, strings.NewReader(""), &out, &diag)
		if code != 2 || out.Len() != 0 || !strings.Contains(diag.String(), row.want) || !strings.HasSuffix(diag.String(), row.hint+"\n") {
			t.Fatalf("%q: exit=%d stdout=%q stderr=%q", row.args, code, out.String(), diag.String())
		}
		checkWidth(t, strings.Join(row.args, " "), diag.String())
	}
	var diag bytes.Buffer
	if code := runIdentity(context.Background(), "/does/not/exist", nil, &bytes.Buffer{}, &diag); code != 2 || !strings.Contains(diag.String(), ctlUsage) {
		t.Fatalf("runIdentity without arguments: exit=%d stderr=%q", code, diag.String())
	}
}

func TestEveryHelpTextFitsIn80Columns(t *testing.T) {
	var list bytes.Buffer
	ctlHelp(&list)
	for _, text := range []string{topHelp, engineHelp, realizeHelp, list.String()} {
		checkWidth(t, "help", text)
	}
	for _, c := range ctlCommands {
		checkWidth(t, c.name, c.help())
	}
}

// A help flag after a realize verb is help, never a verb run with the flag as its REASON; the word help is data.
func TestRealizeHelpRunsNoVerb(t *testing.T) {
	// Not the host's reset journal: the rows that run a verb pass the reset check.
	old := resetDir
	resetDir = t.TempDir()
	t.Cleanup(func() { resetDir = old })
	for args, want := range map[string]string{
		"rollback 3 --help":                 "",
		"rollback 3 -h":                     "",
		"--state-dir /srv rollback -help 3": "",
		"activate 3 --help":                 "",
		"rollback 3 help":                   "rollback 3 help",
		"rollback 3 deadline":               "rollback 3 deadline",
	} {
		verbs := &fakeVerbs{}
		var out, diag bytes.Buffer
		code := realizeCommand(context.Background(), strings.Fields(args), &out, &diag, func(string, string) (realizeVerbs, error) { return verbs, nil })
		if code != 0 || diag.Len() != 0 || strings.Join(verbs.calls, ",") != want || (want == "") != strings.Contains(out.String(), "rollback N REASON") {
			t.Errorf("%q: exit=%d calls=%q stdout=%q stderr=%q", args, code, verbs.calls, out.String(), diag.String())
		}
	}
}

// Every form the ctl help documents must get past its parser: it fails on the missing engine (exit 1), not on usage (exit 2).
func TestDocumentedCtlFormsParse(t *testing.T) {
	// Not root: identity and factory-reset commands stop after parsing, before any prompt or change.
	oldIdentity, oldReset := identityEUID, resetEUID
	identityEUID, resetEUID = func() int { return 1000 }, func() int { return 1000 }
	t.Cleanup(func() { identityEUID, resetEUID = oldIdentity, oldReset })
	const uuid = "00000000-0000-4000-8000-000000000000"
	forms := []string{
		"links", "prefixes", "leases", "lldp", "offloads", "openapi", "setup-code", "setup-code --rotate",
		"config show", "config list zones", "config create zones", "config get zones zon_" + uuid,
		"config replace --version 3 zones zon_" + uuid, "config delete --version 3 zones zon_" + uuid,
		"config get dns", "config replace --version 0 dns", "config replace --version 3 nat/outbound", "config delete --version 3 management",
		"config order rules --version 3 rul_" + uuid,
		"config order nat/port-forwards --version 3 pfw_" + uuid, "config order nat/one-to-one --version 3 oto_" + uuid,
		"config changes",
		"versions list", "versions list --limit 5 --before 9", "versions get 3",
		"preview network", "preview network --saved-version 3",
		"apply network --saved-version 3", "apply network --saved-version 3 --confirm-timeout 60",
		"applies list", "applies get 1", "applies confirm 1", "applies rollback 1",
		"backup create --output /x.zip", "backup create --output /x.zip --include-secrets --include-credentials",
		"backup inspect --input /x.zip", "backup restore --input /x.zip --stores configuration,configurationSecrets,users,userCredentials",
		"backup restore --input /x.zip --stores configuration --operation-id retry-1",
		"kept store-recovery",
		"users bootstrap-admin --login admin", "users bootstrap-admin --login admin --display-name Admin",
		"users list", "users list --cursor c --limit 5", "users get " + uuid,
		"users create --login bob --kind human --revision 1", "users create --login bob --kind service --revision 1 --display-name Bob --roles a,b --disabled",
		"users update " + uuid + " --revision 1 --login bob --display-name Bob --kind human --roles a,b --enabled false",
		"users disable " + uuid + " --revision 1", "users delete " + uuid + " --revision 1", "users password-reset " + uuid + " --revision 1",
		"tokens create --account-id " + uuid + " --name ci --scope a,b", "tokens create --account-id " + uuid + " --name ci --scope a --no-expiry",
		"tokens create --account-id " + uuid + " --name ci --scope a --expires-at 2030-01-02T03:04:05Z --revoke-on-password-change true",
		"tokens list --account-id " + uuid, "tokens list --account-id " + uuid + " --cursor c --limit 5", "tokens revoke AAAAAAAAAAAAAAAAAAAAAA",
		"audit list", "audit list --cursor c --limit 5",
		"factory-reset run", "factory-reset abort", "factory-reset status",
	}
	socket := filepath.Join(t.TempDir(), "absent.sock")
	covered := map[string]bool{}
	for _, form := range forms {
		args := strings.Fields(form)
		covered[args[0]] = true
		if args[0] == "backup" {
			// The parser alone: create with secrets would ask a terminal for a passphrase.
			if _, err := parseBackupCommand(args[1:]); err != nil {
				t.Errorf("%q: %v", form, err)
			}
			continue
		}
		var out, diag bytes.Buffer
		if code := run(context.Background(), append([]string{"ctl", "--socket", socket}, args...), strings.NewReader("{}"), &out, &diag); code != 1 || out.Len() != 0 {
			t.Errorf("%q: exit=%d stdout=%q stderr=%q", form, code, out.String(), diag.String())
		}
	}
	// A command reaches its code only through its ctlCommands entry, so an entry no form exercises has no command behind it.
	for _, c := range ctlCommands {
		if !covered[c.name] {
			t.Errorf("no documented form of %q is checked", c.name)
		}
	}
}

func TestConfigHelpNamesEveryCollectionAndSingleton(t *testing.T) {
	config, _ := findCtlCommand("config")
	section := func(from, to string) []string {
		_, text, _ := strings.Cut(config.details, from)
		text, _, _ = strings.Cut(text, to)
		return strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' })
	}
	if got := section("Collections:", "Singletons:"); !slices.Equal(got, collections) {
		t.Errorf("help collections %q, parser %q", got, collections)
	}
	if got := section("Singletons:", "Lists:"); !slices.Equal(got, singletons) {
		t.Errorf("help singletons %q, parser %q", got, singletons)
	}
}
