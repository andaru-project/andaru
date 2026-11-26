package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

const opsChannel = `{"name":"ops","kind":"webhook","filter":{"minSeverity":"warning","sources":["upgrade","apply"]},"webhook":{"url":{"secret":"OPS_URL","set":"https://hooks.example/CHANNEL_SECRET_TOKEN","expectedVersion":"0"},"authorization":{"secret":"OPS_AUTH","set":"Bearer CHANNEL_SECRET_AUTH","expectedVersion":"0"}}}`

func channelChange(t *testing.T, s *savedState, op, id string, version Version, body string, next Version) (preparedChange, error) {
	t.Helper()
	var expected *Version
	if op != "create" {
		expected = &version
	}
	c, e := ObjectChange(op, "notifications.channels", id, expected, []byte(body))
	if e != nil {
		t.Fatal(e)
	}
	return prepareChanges(s, c, next, fixedTime)
}

func TestChannelLifecycle(t *testing.T) {
	s := seededState(t)
	p, e := channelChange(t, &s, "create", "", "", opsChannel, "8")
	if e != nil {
		t.Fatal(e)
	}
	id := p.Created["object"]
	c := p.State.Config.Notifications.Channels
	if !strings.HasPrefix(id, "ntf_") || len(c) != 1 || !*c[0].Enabled || c[0].Filter.MinSeverity != "warning" || !slices.Equal(c[0].Filter.Sources, []string{"apply", "upgrade"}) {
		t.Fatalf("created %s %+v", id, c)
	}
	if v := p.State.Secrets.Scopes["core"]["OPS_URL"]; v.Value != "https://hooks.example/CHANNEL_SECRET_TOKEN" || v.Kind != "password" || v.Version != "8" {
		t.Fatalf("url secret %+v", v)
	}
	result, e := resultView(p)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(result)
	if bytes.Contains(raw, []byte("CHANNEL_SECRET")) || bytes.Contains(p.ConfigBody, []byte("CHANNEL_SECRET")) ||
		!bytes.Contains(raw, []byte(`"url":{"secret":"OPS_URL","isSet":true,`)) || !bytes.Contains(raw, []byte(`"authorization":{"secret":"OPS_AUTH","isSet":true,`)) {
		t.Fatalf("view %s", raw)
	}
	// The stored form reads back to the same bytes.
	d, e := decodeDeclaration(p.ConfigBody)
	if e != nil {
		t.Fatal(e)
	}
	if again, _ := canonical(d); !bytes.Equal(again, p.ConfigBody) {
		t.Fatalf("round trip\n%s\n%s", p.ConfigBody, again)
	}
	snap, e := publicSnapshot(p.State)
	if e != nil || len(snap.UnusedSecrets) != 0 || bytes.Contains(snap.Value, []byte("CHANNEL_SECRET")) || !bytes.Contains(snap.Value, []byte(`"isSet":true`)) {
		t.Fatalf("snapshot %+v %v", snap, e)
	}

	// A second channel reuses the URL secret, with the defaults.
	s = p.State
	p, e = channelChange(t, &s, "create", "", "", `{"name":"second","kind":"webhook","webhook":{"url":{"secret":"OPS_URL"}}}`, "9")
	if e != nil {
		t.Fatal(e)
	}
	second := p.Created["object"]
	for _, c := range p.State.Config.Notifications.Channels {
		if c.ID == second && (!*c.Enabled || c.Filter.MinSeverity != "info" || c.Webhook.Authorization != nil) {
			t.Fatalf("defaults %+v", c)
		}
	}

	// Setting the shared URL versions both channels; dropping authorization leaves its secret unused.
	s = p.State
	p, e = channelChange(t, &s, "replace", id, "8", `{"name":"ops","enabled":false,"kind":"webhook","webhook":{"url":{"secret":"OPS_URL","set":"http://hooks.example:8080/x","expectedVersion":"8"}}}`, "10")
	if e != nil {
		t.Fatal(e)
	}
	if p.State.Versions[objectKey{"notifications.channels", id}].Version != "10" || p.State.Versions[objectKey{"notifications.channels", second}].Version != "10" {
		t.Fatal("referrers of the changed secret did not version together")
	}
	if snap, _ = publicSnapshot(p.State); !slices.Equal(snap.UnusedSecrets, []string{"OPS_AUTH"}) {
		t.Fatalf("unused %v", snap.UnusedSecrets)
	}

	s = p.State
	if p, e = channelChange(t, &s, "delete", second, "10", "", "11"); e != nil || len(p.State.Config.Notifications.Channels) != 1 {
		t.Fatalf("delete %v", e)
	}
	s = p.State
	if p, e = channelChange(t, &s, "delete", id, "10", "", "12"); e != nil || bytes.Contains(p.ConfigBody, []byte("notifications")) {
		t.Fatalf("last delete %v %s", e, p.ConfigBody)
	}
}

func TestChannelRefusals(t *testing.T) {
	for name, c := range map[string]struct{ body, path string }{
		"missing secret":   {`{"name":"x","kind":"webhook","webhook":{"url":{"secret":"NOPE"}}}`, "/webhook/url"},
		"not a url":        {`{"name":"x","kind":"webhook","webhook":{"url":{"secret":"WAN_PASSWORD"}}}`, "/webhook/url"},
		"ftp":              {`{"name":"x","kind":"webhook","webhook":{"url":{"secret":"U","set":"ftp://hooks.example/","expectedVersion":"0"}}}`, "/webhook/url"},
		"no host":          {`{"name":"x","kind":"webhook","webhook":{"url":{"secret":"U","set":"https:///path","expectedVersion":"0"}}}`, "/webhook/url"},
		"bad severity":     {`{"name":"x","kind":"webhook","filter":{"minSeverity":"debug"},"webhook":{"url":{"secret":"U","set":"https://h/","expectedVersion":"0"}}}`, "/filter/minSeverity"},
		"empty source":     {`{"name":"x","kind":"webhook","filter":{"sources":[""]},"webhook":{"url":{"secret":"U","set":"https://h/","expectedVersion":"0"}}}`, "/filter/sources/0"},
		"control source":   {`{"name":"x","kind":"webhook","filter":{"sources":["a\u0007"]},"webhook":{"url":{"secret":"U","set":"https://h/","expectedVersion":"0"}}}`, "/filter/sources/0"},
		"other kind":       {`{"name":"x","kind":"email","webhook":{"url":{"secret":"U","set":"https://h/","expectedVersion":"0"}}}`, "/kind"},
		"no webhook":       {`{"name":"x","kind":"webhook"}`, ""},
		"duplicate setter": {`{"name":"x","kind":"webhook","webhook":{"url":{"secret":"U","set":"https://h/","expectedVersion":"0"},"authorization":{"secret":"U","set":"https://h/","expectedVersion":"0"}}}`, "/webhook/authorization"},
	} {
		t.Run(name, func(t *testing.T) {
			s := seededState(t)
			_, e := channelChange(t, &s, "create", "", "", c.body, "8")
			f := requireFault(t, e, "VALIDATION_FAILED")
			if len(f.Details) == 0 || !strings.HasPrefix(f.Details[0].Path, c.path) {
				t.Fatalf("details %+v", f.Details)
			}
		})
	}
	s := seededState(t)
	_, e := channelChange(t, &s, "create", "", "", strings.Replace(opsChannel, `,"expectedVersion":"0"}`, `}`, 1), "8")
	requireFault(t, e, "PRECONDITION_REQUIRED")
	p, e := channelChange(t, &s, "create", "", "", opsChannel, "8")
	if e != nil {
		t.Fatal(e)
	}
	s = p.State
	_, e = channelChange(t, &s, "create", "", "", `{"name":"ops","kind":"webhook","webhook":{"url":{"secret":"OPS_URL"}}}`, "9")
	if f := requireFault(t, e, "VALIDATION_FAILED"); f.Details[0].Path != "/name" {
		t.Fatalf("duplicate name %+v", f.Details)
	}
}

// A secret is held by links or by channels, not both: otherwise configuration.write could redirect a channel through a
// link's password, and notifications.manage could reach a link's password through a channel.
func TestChannelsAndLinksShareNoSecret(t *testing.T) {
	s := seededState(t)
	viaChannel := `{"name":"leak","kind":"webhook","webhook":{"url":{"secret":"LEAK_URL","set":"https://evil.example/","expectedVersion":"0"},"authorization":{"secret":"WAN_PASSWORD"}}}`
	if _, e := channelChange(t, &s, "create", "", "", viaChannel, "8"); e == nil || !strings.Contains(e.Error(), "VALIDATION_FAILED") {
		t.Fatalf("a channel holding a link's secret: %v", e)
	}
	p, e := channelChange(t, &s, "create", "", "", opsChannel, "8")
	if e != nil {
		t.Fatal(e)
	}
	i := slices.IndexFunc(p.State.Config.Links, func(l link) bool { return l.Password != nil })
	l := p.State.Config.Links[i]
	l.Password = &secretReference{Secret: "OPS_URL"}
	body, _ := json.Marshal(l)
	version := p.State.Versions[objectKey{"links", l.ID}].Version
	c, e := ObjectChange("replace", "links", l.ID, &version, body)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := prepareChanges(&p.State, c, "9", fixedTime); e == nil || !strings.Contains(e.Error(), "VALIDATION_FAILED") {
		t.Fatalf("a link holding a channel's secret: %v", e)
	}
}

// Only a restore leaves a channel's secret unresolved; a client may not, or the channel would skip its checks.
func TestChannelSecretCannotBeWrittenUnresolved(t *testing.T) {
	s := seededState(t)
	body := `{"name":"x","kind":"webhook","webhook":{"url":{"secret":"NOPE","unresolved":true}}}`
	if _, e := channelChange(t, &s, "create", "", "", body, "8"); e == nil {
		t.Fatal("an unresolved channel secret was saved")
	}
}

// A channel URL that no longer holds a URL, as a restore may leave it, blocks saves of that channel only.
func TestBadChannelURLBlocksOnlyItsChannel(t *testing.T) {
	s := seededState(t)
	p, e := channelChange(t, &s, "create", "", "", opsChannel, "8")
	if e != nil {
		t.Fatal(e)
	}
	entry := p.State.Secrets.Scopes["core"]["OPS_URL"]
	entry.Value = "Bearer not-a-url"
	p.State.Secrets.Scopes["core"]["OPS_URL"] = entry
	other := `{"name":"other","kind":"webhook","webhook":{"url":{"secret":"OTHER_URL","set":"https://hooks.example/o","expectedVersion":"0"}}}`
	if _, e := channelChange(t, &p.State, "create", "", "", other, "9"); e != nil {
		t.Fatalf("an unrelated save: %v", e)
	}
	id := p.State.Config.Notifications.Channels[0].ID
	replace := `{"name":"ops","kind":"webhook","webhook":{"url":{"secret":"OPS_URL"}}}`
	if _, e := channelChange(t, &p.State, "replace", id, "8", replace, "9"); e == nil || !strings.Contains(fmt.Sprintf("%#v", e), "http or https") {
		t.Fatalf("saving the channel itself: %#v", e)
	}
}
