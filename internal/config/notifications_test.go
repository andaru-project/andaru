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
	if v := p.State.Secrets.Scopes["core"]["OPS_URL"]; v.Value != "https://hooks.example/CHANNEL_SECRET_TOKEN" || v.Kind != "notification" || v.Version != "8" {
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

const emailChannel = `{"name":"x","kind":"email","email":{"host":"smtp.example.com","port":465,"security":"tls","username":"a@example.com","password":{"secret":"P","set":"pw","expectedVersion":"0"},"from":"Andaru <a@example.com>","to":["ops@example.com"]}}`

func TestChannelRefusals(t *testing.T) {
	refusals := map[string]struct{ body, path string }{}
	for name, c := range map[string]struct{ body, path string }{
		"missing secret":        {`{"name":"x","kind":"webhook","webhook":{"url":{"secret":"NOPE"}}}`, "/webhook/url"},
		"not a url":             {`{"name":"x","kind":"webhook","webhook":{"url":{"secret":"WAN_PASSWORD"}}}`, "/webhook/url"},
		"ftp":                   {`{"name":"x","kind":"webhook","webhook":{"url":{"secret":"U","set":"ftp://hooks.example/","expectedVersion":"0"}}}`, "/webhook/url"},
		"no host":               {`{"name":"x","kind":"webhook","webhook":{"url":{"secret":"U","set":"https:///path","expectedVersion":"0"}}}`, "/webhook/url"},
		"bad severity":          {`{"name":"x","kind":"webhook","filter":{"minSeverity":"debug"},"webhook":{"url":{"secret":"U","set":"https://h/","expectedVersion":"0"}}}`, "/filter/minSeverity"},
		"empty source":          {`{"name":"x","kind":"webhook","filter":{"sources":[""]},"webhook":{"url":{"secret":"U","set":"https://h/","expectedVersion":"0"}}}`, "/filter/sources/0"},
		"control source":        {`{"name":"x","kind":"webhook","filter":{"sources":["a\u0007"]},"webhook":{"url":{"secret":"U","set":"https://h/","expectedVersion":"0"}}}`, "/filter/sources/0"},
		"other kind":            {`{"name":"x","kind":"email","webhook":{"url":{"secret":"U","set":"https://h/","expectedVersion":"0"}}}`, "/kind"},
		"no webhook":            {`{"name":"x","kind":"webhook"}`, ""},
		"ntfy without ntfy":     {`{"name":"x","kind":"ntfy","webhook":{"url":{"secret":"U","set":"https://h/","expectedVersion":"0"}}}`, ""},
		"ntfy credentials":      {`{"name":"x","kind":"ntfy","ntfy":{"server":"https://alice:hunter2@ntfy.example","topic":"t"}}`, "/ntfy/server"},
		"ntfy query":            {`{"name":"x","kind":"ntfy","ntfy":{"server":"https://ntfy.example/?a=b","topic":"t"}}`, "/ntfy/server"},
		"ntfy fragment":         {`{"name":"x","kind":"ntfy","ntfy":{"server":"https://ntfy.example/#f","topic":"t"}}`, "/ntfy/server"},
		"ntfy and webhook":      {`{"name":"x","kind":"ntfy","ntfy":{"server":"https://ntfy.sh","topic":"t"},"webhook":{"url":{"secret":"U","set":"https://h/","expectedVersion":"0"}}}`, ""},
		"ntfy bad topic":        {`{"name":"x","kind":"ntfy","ntfy":{"server":"https://ntfy.sh","topic":"a/b"}}`, "/ntfy/topic"},
		"ntfy long topic":       {`{"name":"x","kind":"ntfy","ntfy":{"server":"https://ntfy.sh","topic":"` + strings.Repeat("a", 65) + `"}}`, "/ntfy/topic"},
		"ntfy bad server":       {`{"name":"x","kind":"ntfy","ntfy":{"server":"ftp://ntfy.sh","topic":"t"}}`, "/ntfy/server"},
		"ntfy no topic":         {`{"name":"x","kind":"ntfy","ntfy":{"server":"https://ntfy.sh"}}`, ""},
		"ntfy password token":   {`{"name":"x","kind":"ntfy","ntfy":{"server":"https://ntfy.sh","topic":"t","token":{"secret":"WAN_PASSWORD"}}}`, "/ntfy/token"},
		"pushover no user":      {`{"name":"x","kind":"pushover","pushover":{"token":{"secret":"T","set":"a","expectedVersion":"0"}}}`, ""},
		"pushover missing user": {`{"name":"x","kind":"pushover","pushover":{"token":{"secret":"T","set":"a","expectedVersion":"0"},"user":{"secret":"NOPE"}}}`, "/pushover/user"},
		"telegram no token":     {`{"name":"x","kind":"telegram","telegram":{"chatId":"-100"}}`, ""},
		"telegram bad chat":     {`{"name":"x","kind":"telegram","telegram":{"token":{"secret":"T","set":"1:a","expectedVersion":"0"},"chatId":"12a"}}`, "/telegram/chatId"},
		"telegram plain token":  {`{"name":"x","kind":"telegram","telegram":{"token":"1:a","chatId":"-100"}}`, ""},
		"email without email":   {`{"name":"x","kind":"email"}`, ""},
		"duplicate setter":      {`{"name":"x","kind":"webhook","webhook":{"url":{"secret":"U","set":"https://h/","expectedVersion":"0"},"authorization":{"secret":"U","set":"https://h/","expectedVersion":"0"}}}`, "/webhook/authorization"},
	} {
		refusals[name] = c
	}
	for name, c := range map[string]struct{ old, new, path string }{
		"email scheme host":      {"smtp.example.com", "smtp://smtp.example.com", "/email/host"},
		"email host port":        {"smtp.example.com", "smtp.example.com:465", "/email/host"},
		"email host zone":        {"smtp.example.com", "fe80::1%eth0", "/email/host"},
		"email host newline":     {"smtp.example.com", `smtp.example.com\nx`, "/email/host"},
		"email port 0":           {`"port":465`, `"port":0`, "/email/port"},
		"email security none":    {`"tls"`, `"none"`, "/email/security"},
		"email from no address":  {"Andaru <a@example.com>", "nobody", "/email/from"},
		"email from two":         {"Andaru <a@example.com>", "a@example.com, b@example.com", "/email/from"},
		"email from CRLF":        {"Andaru <a@example.com>", `Andaru\r\nBcc: e@example.com <a@example.com>`, "/email/from"},
		"email from encoded LF":  {"Andaru <a@example.com>", "=?utf-8?q?A=0D=0AB?= <a@example.com>", "/email/from"},
		"email quoted local":     {"Andaru <a@example.com>", `\"x> SIZE=999 RET=FULL\"@example.com`, "/email/from"},
		"email long name":        {"Andaru <a@example.com>", "ééééééééééééééééééééééééééééééééé <a@example.com>", "/email/from"},
		"email quoted at":        {"Andaru <a@example.com>", `\"ops@internal\"@example.com`, "/email/from"},
		"email numeric host":     {"smtp.example.com", "192.168.1.300", "/email/host"},
		"email to empty":         {`["ops@example.com"]`, `[]`, "/email/to"},
		"email to too many":      {`["ops@example.com"]`, `["` + strings.Repeat(`a@example.com","`, 20) + `ops@example.com"]`, "/email/to"},
		"email to duplicate":     {`["ops@example.com"]`, `["ops@example.com","ops@example.com"]`, "/email/to"},
		"email to same address":  {`["ops@example.com"]`, `["ops@example.com","Ops <OPS@example.com>"]`, "/email/to/1"},
		"email to bad":           {`["ops@example.com"]`, `["ops"]`, "/email/to/0"},
		"email to CR":            {`["ops@example.com"]`, `["ops@example.com\r"]`, "/email/to/0"},
		"email username CR":      {`"username":"a@example.com"`, `"username":"a\r@example.com"`, "/email/username"},
		"email no password":      {`,"password":{"secret":"P","set":"pw","expectedVersion":"0"}`, "", "/email/password"},
		"email no username":      {`"username":"a@example.com",`, "", "/email/username"},
		"email password of link": {`{"secret":"P","set":"pw","expectedVersion":"0"}`, `{"secret":"WAN_PASSWORD"}`, "/email/password"},
	} {
		if !strings.Contains(emailChannel, c.old) {
			t.Fatalf("%s: no %s", name, c.old)
		}
		refusals[name] = struct{ body, path string }{strings.Replace(emailChannel, c.old, c.new, 1), c.path}
	}
	for name, c := range refusals {
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

// ntfy, Pushover and Telegram channels hold their tokens as secrets of the kind notification, shown as set and
// versioned only.
func TestAppChannelKinds(t *testing.T) {
	for _, row := range []struct{ body, secrets, view string }{
		{`{"name":"n","kind":"ntfy","ntfy":{"server":"https://ntfy.example","topic":"andaru-alerts","token":{"secret":"N_TOKEN","set":"tk_CHANNEL_SECRET","expectedVersion":"0"}}}`,
			"N_TOKEN", `"ntfy":{"server":"https://ntfy.example","topic":"andaru-alerts","token":{"secret":"N_TOKEN","isSet":true,`},
		{`{"name":"n","kind":"ntfy","ntfy":{"server":"http://ntfy.lan:8080/sub","topic":"a_b-C"}}`, "", `"ntfy":{"server":"http://ntfy.lan:8080/sub","topic":"a_b-C"}`},
		{`{"name":"p","kind":"pushover","pushover":{"token":{"secret":"P_APP","set":"app_CHANNEL_SECRET","expectedVersion":"0"},"user":{"secret":"P_USER","set":"usr_CHANNEL_SECRET","expectedVersion":"0"}}}`,
			"P_APP P_USER", `"pushover":{"token":{"secret":"P_APP","isSet":true,`},
		{`{"name":"t","kind":"telegram","telegram":{"token":{"secret":"T_BOT","set":"1:bot_CHANNEL_SECRET","expectedVersion":"0"},"chatId":"-1001234567890"}}`,
			"T_BOT", `"telegram":{"token":{"secret":"T_BOT","isSet":true,`},
		{`{"name":"t","kind":"telegram","telegram":{"token":{"secret":"T_BOT","set":"1:bot_CHANNEL_SECRET","expectedVersion":"0"},"chatId":"@andaru_alerts"}}`,
			"T_BOT", `"chatId":"@andaru_alerts"}`},
		{`{"name":"e","kind":"email","email":{"host":"smtp.example.com","port":465,"security":"tls","username":"alerts@example.com","password":{"secret":"E_PASS","set":"pw_CHANNEL_SECRET","expectedVersion":"0"},"from":"Andaru <alerts@example.com>","to":["ops@example.com","Two <two@example.com>"]}}`,
			"E_PASS", `"email":{"host":"smtp.example.com","port":465,"security":"tls","username":"alerts@example.com","from":"Andaru \u003calerts@example.com\u003e","to":["ops@example.com","Two \u003ctwo@example.com\u003e"],"password":{"secret":"E_PASS","isSet":true,`},
		{`{"name":"e","kind":"email","email":{"host":"192.0.2.25","port":587,"security":"starttls","from":"a@example.com","to":["ops@example.com"]}}`,
			"", `"email":{"host":"192.0.2.25","port":587,"security":"starttls","from":"a@example.com","to":["ops@example.com"]}`},
	} {
		s := seededState(t)
		p, e := channelChange(t, &s, "create", "", "", row.body, "8")
		if e != nil {
			t.Fatalf("%s: %v", row.body, e)
		}
		for _, name := range strings.Fields(row.secrets) {
			if v := p.State.Secrets.Scopes["core"][name]; v.Kind != "notification" || !strings.Contains(v.Value, "CHANNEL_SECRET") || v.Version != "8" {
				t.Fatalf("secret %s %+v", name, v)
			}
		}
		result, e := resultView(p)
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(result)
		if bytes.Contains(raw, []byte("CHANNEL_SECRET")) || bytes.Contains(p.ConfigBody, []byte("CHANNEL_SECRET")) || !bytes.Contains(raw, []byte(row.view)) {
			t.Fatalf("view %s", raw)
		}
		d, e := decodeDeclaration(p.ConfigBody)
		if e != nil {
			t.Fatal(e)
		}
		if again, _ := canonical(d); !bytes.Equal(again, p.ConfigBody) {
			t.Fatalf("round trip\n%s\n%s", p.ConfigBody, again)
		}
	}
}

// A secret is bound to its kind: configuration.write cannot redirect a channel through a link's password, and
// notifications.manage cannot reach a link's password through a channel, even once the link is gone.
func TestChannelsAndLinksShareNoSecret(t *testing.T) {
	refused := func(what string, e error) {
		t.Helper()
		if e == nil || !strings.Contains(fmt.Sprintf("%#v", e), "another kind of object") {
			t.Fatalf("%s: %#v", what, e)
		}
	}
	s := seededState(t)
	viaChannel := `{"name":"leak","kind":"webhook","webhook":{"url":{"secret":"LEAK_URL","set":"https://evil.example/","expectedVersion":"0"},"authorization":{"secret":"WAN_PASSWORD"}}}`
	_, e := channelChange(t, &s, "create", "", "", viaChannel, "8")
	refused("a channel holding a link's secret", e)
	overwrite := `{"name":"leak","kind":"webhook","webhook":{"url":{"secret":"LEAK2_URL","set":"https://evil.example/","expectedVersion":"0"},"authorization":{"secret":"WAN_PASSWORD","set":"x","expectedVersion":"7"}}}`
	fresh := seededState(t) // a refused batch may leave its secret writes in the state it was given
	_, e = channelChange(t, &fresh, "create", "", "", overwrite, "8")
	refused("a channel setting a link's secret", e)
	s = seededState(t)
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
	_, e = prepareChanges(&p.State, c, "9", fixedTime)
	refused("a link holding a channel's secret", e)
	if !strings.Contains(fmt.Sprintf("%#v", e), "/password") {
		t.Fatalf("blamed %#v, want the link's password", e)
	}
	// A link's secret outlives the link, held by nothing: a channel still cannot hold it.
	orphan := seededState(t)
	orphan.Secrets.Scopes["core"]["ORPHAN"] = secretEntry{Kind: "password", Value: "TEST_ONLY", Version: "7"}
	_, e = channelChange(t, &orphan, "create", "", "", strings.ReplaceAll(viaChannel, "WAN_PASSWORD", "ORPHAN"), "8")
	refused("a channel holding an orphaned link secret", e)
}

// Only a restore leaves a channel's secret unresolved; a client may not, or the channel would skip its checks.
func TestChannelSecretCannotBeWrittenUnresolved(t *testing.T) {
	s := seededState(t)
	body := `{"name":"x","kind":"webhook","webhook":{"url":{"secret":"NOPE","unresolved":true}}}`
	if _, e := channelChange(t, &s, "create", "", "", body, "8"); e == nil || !strings.Contains(fmt.Sprintf("%#v", e), "must be set or exist") {
		t.Fatalf("an unresolved channel secret: %#v", e)
	}
}

// A channel a restore left unresolved can still be edited, as a client echoes the reference it read.
func TestRestoredUnresolvedChannelCanBeEdited(t *testing.T) {
	s := seededState(t)
	p, e := channelChange(t, &s, "create", "", "", opsChannel, "8")
	if e != nil {
		t.Fatal(e)
	}
	ch := &p.State.Config.Notifications.Channels[0]
	ch.Webhook.URL.Unresolved = true
	echo := `{"name":"ops","enabled":false,"kind":"webhook","webhook":{"url":{"secret":"OPS_URL","unresolved":true}}}`
	if _, e := channelChange(t, &p.State, "replace", ch.ID, "8", echo, "9"); e != nil {
		t.Fatalf("disabling it: %#v", e)
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

// An unresolved reference is bound to its kind too: a link cannot point at a channel's secret, even unresolved.
func TestUnresolvedLinkReferenceKeepsItsKind(t *testing.T) {
	s := seededState(t)
	p, e := channelChange(t, &s, "create", "", "", opsChannel, "8")
	if e != nil {
		t.Fatal(e)
	}
	i := slices.IndexFunc(p.State.Config.Links, func(l link) bool { return l.Password != nil })
	l := p.State.Config.Links[i]
	l.Password = &secretReference{Secret: "OPS_URL", Unresolved: true}
	body, _ := json.Marshal(l)
	version := p.State.Versions[objectKey{"links", l.ID}].Version
	c, e := ObjectChange("replace", "links", l.ID, &version, body)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = prepareChanges(&p.State, c, "9", fixedTime); e == nil || !strings.Contains(fmt.Sprintf("%#v", e), "another kind of object") {
		t.Fatalf("%#v", e)
	}
}

// A save may not create a secret under a name an object of the other kind holds unresolved: that object could then
// never be saved or applied.
func TestNewSecretTakesNoNameOfTheOtherKind(t *testing.T) {
	s := seededState(t)
	i := slices.IndexFunc(s.Config.Links, func(l link) bool { return l.Password != nil })
	s.Config.Links[i].Password = &secretReference{Secret: "RESTORED_X", Unresolved: true}
	body := `{"name":"c","kind":"webhook","webhook":{"url":{"secret":"RESTORED_X","set":"https://hooks.example/x","expectedVersion":"0"}}}`
	if _, e := channelChange(t, &s, "create", "", "", body, "8"); e == nil || !strings.Contains(fmt.Sprintf("%#v", e), "Another kind of object holds") {
		t.Fatalf("%#v", e)
	}
}

// A recovery copy is taken from the public view, with the links' secrets only: it drops the channels, so a factory
// reset can still restore it.
func TestRecoveryCopyWithChannelsRestores(t *testing.T) {
	s := seededState(t)
	p, e := channelChange(t, &s, "create", "", "", opsChannel, "8")
	if e != nil {
		t.Fatal(e)
	}
	view, e := publicSnapshot(p.State)
	if e != nil {
		t.Fatal(e)
	}
	body, _ := json.Marshal(map[string]any{"declaration": view.Value, "secrets": map[string]string{"WAN_PASSWORD": "TEST_ONLY"}})
	docs, e := KeptDeclaration(body)
	if e != nil {
		t.Fatal(e)
	}
	if e = ValidateBackupDocuments(docs); e != nil || bytes.Contains(docs.Configuration, []byte("notifications")) {
		t.Fatalf("%#v %s", e, docs.Configuration)
	}
}

// A restored channel of any kind may be edited with its unresolved secret sent back unchanged.
func TestRestoredUnresolvedAppChannelCanBeEdited(t *testing.T) {
	s := seededState(t)
	body := `{"name":"t","kind":"telegram","telegram":{"token":{"secret":"TG_TOKEN","set":"123:abc","expectedVersion":"0"},"chatId":"-100"}}`
	p, e := channelChange(t, &s, "create", "", "", body, "8")
	if e != nil {
		t.Fatal(e)
	}
	ch := &p.State.Config.Notifications.Channels[0]
	ch.Telegram.Token.Unresolved = true
	echo := `{"name":"t","enabled":false,"kind":"telegram","telegram":{"token":{"secret":"TG_TOKEN","unresolved":true},"chatId":"-100"}}`
	if _, e := channelChange(t, &p.State, "replace", ch.ID, "8", echo, "9"); e != nil {
		t.Fatalf("disabling it: %#v", e)
	}
}
