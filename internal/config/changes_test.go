package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

const pppID = "lnk_00000000-0000-4000-8000-000000000003"
const secondPPP = "lnk_00000000-0000-4000-8000-000000000006"

var fixedTime = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func seededState(t *testing.T) savedState {
	t.Helper()
	d, e := decodeDeclaration(fixtureBytes(t))
	if e != nil {
		t.Fatal(e)
	}
	v := map[objectKey]objectVersion{}
	for _, l := range d.Links {
		v[objectKey{"links", l.ID}] = objectVersion{Version: "7"}
	}
	for _, s := range d.Segments {
		v[objectKey{"segments", s.ID}] = objectVersion{Version: "7"}
	}
	for _, z := range d.Zones {
		v[objectKey{"zones", z.ID}] = objectVersion{Version: "7"}
	}
	other := d.Links[2]
	other.ID = secondPPP
	other.Name = "second-ppp"
	d.Links = append(d.Links, other)
	v[objectKey{"links", secondPPP}] = objectVersion{Version: "7"}
	sec := emptySecrets()
	sec.Scopes["core"]["WAN_PASSWORD"] = secretEntry{Kind: "password", Value: "TEST_ONLY_OLD_VALUE", UpdatedAt: fixedTime.Add(-time.Hour).Format(time.RFC3339Nano), Version: "7"}
	return savedState{Version: "7", Config: d, Secrets: sec, Versions: v}
}
func changedLink(t *testing.T, s savedState, id string, version Version, fields map[string]any) Changes {
	t.Helper()
	var target link
	for _, l := range s.Config.Links {
		if l.ID == id {
			target = l
		}
	}
	raw, e := json.Marshal(target)
	if e != nil {
		t.Fatal(e)
	}
	var value map[string]any
	if e = json.Unmarshal(raw, &value); e != nil {
		t.Fatal(e)
	}
	for k, v := range fields {
		value[k] = v
	}
	raw, e = json.Marshal(value)
	if e != nil {
		t.Fatal(e)
	}
	c, e := ObjectChange("replace", "links", id, &version, raw)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func requireFault(t *testing.T, e error, code string) *Fault {
	t.Helper()
	var f *Fault
	if !errors.As(e, &f) || f.Code != code {
		t.Fatalf("wanted %s; got %v", code, e)
	}
	return f
}

func TestChangePreconditionsAndAtomicity(t *testing.T) {
	s := seededState(t)
	c := changedLink(t, s, pppID, "7", map[string]any{"name": "renamed"})
	p, e := prepareChanges(&s, c, "8", fixedTime)
	if e != nil {
		t.Fatal(e)
	}
	if p.State.Versions[objectKey{"links", pppID}].Version != "8" {
		t.Fatal("edit did not advance version")
	}
	stale := changedLink(t, p.State, pppID, "7", map[string]any{"name": "stale"})
	_, e = prepareChanges(&p.State, stale, "9", fixedTime)
	requireFault(t, e, "PRECONDITION_FAILED")
	back := changedLink(t, p.State, pppID, "8", map[string]any{"name": "wan-ppp"})
	p2, e := prepareChanges(&p.State, back, "9", fixedTime)
	if e != nil {
		t.Fatal(e)
	}
	if p2.State.Versions[objectKey{"links", pppID}].Version != "9" {
		t.Fatal("A-B-A revived old token")
	}
	s = seededState(t)
	v := Version("7")
	c, e = ObjectChange("delete", "links", secondPPP, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	_, e = prepareChanges(&s, c, "8", fixedTime)
	requireFault(t, e, "PRECONDITION_REQUIRED")
	c, _ = ObjectChange("delete", "links", "lnk_00000000-0000-4000-8000-999999999999", &v, nil)
	_, e = prepareChanges(&s, c, "8", fixedTime)
	requireFault(t, e, "NOT_FOUND")
	c, _ = ObjectChange("delete", "links", secondPPP, &v, nil)
	p, e = prepareChanges(&s, c, "8", fixedTime)
	if e != nil {
		t.Fatal(e)
	}
	_, e = prepareChanges(&p.State, c, "9", fixedTime)
	f := requireFault(t, e, "PRECONDITION_FAILED")
	if f.Current == nil || !f.Current.Deleted {
		t.Fatal("missing deleted marker")
	}
	s = seededState(t)
	c = changedLink(t, s, pppID, "7", map[string]any{"name": "second-ppp"})
	_, e = prepareChanges(&s, c, "8", fixedTime)
	f = requireFault(t, e, "VALIDATION_FAILED")
	if len(f.Details) == 0 || f.Details[0].Path != "/name" {
		t.Fatalf("wrong object pointer: %+v", f.Details)
	}
}

func TestNamedSecretSharing(t *testing.T) {
	s := seededState(t)
	s.Config.Links[len(s.Config.Links)-1].Name = "independent-rename"
	s.Versions[objectKey{"links", secondPPP}] = objectVersion{Version: "8"}
	s.Version = "8"
	c := changedLink(t, s, pppID, "7", map[string]any{"password": map[string]any{"secret": "WAN_PASSWORD", "set": "TEST_ONLY_NEW_VALUE", "expectedVersion": "7"}})
	p, e := prepareChanges(&s, c, "9", fixedTime)
	if e != nil {
		t.Fatal(e)
	}
	if p.State.Secrets.Scopes["core"]["WAN_PASSWORD"].Value != "TEST_ONLY_NEW_VALUE" || p.State.Versions[objectKey{"links", pppID}].Version != "9" || p.State.Versions[objectKey{"links", secondPPP}].Version != "9" {
		t.Fatal("shared value and all referrers did not version together")
	}
	var name string
	for _, l := range p.State.Config.Links {
		if l.ID == secondPPP {
			name = l.Name
		}
	}
	if name != "independent-rename" {
		t.Fatal("overwrote unrelated public edit")
	}
	c = changedLink(t, p.State, pppID, "9", map[string]any{"password": map[string]any{"secret": "WAN_PASSWORD", "set": "lost", "expectedVersion": "7"}})
	_, e = prepareChanges(&p.State, c, "10", fixedTime)
	requireFault(t, e, "PRECONDITION_FAILED")
	for _, expected := range []string{"0", "7"} {
		t.Run("absent "+expected, func(t *testing.T) {
			s := seededState(t)
			c := changedLink(t, s, pppID, "7", map[string]any{"password": map[string]any{"secret": "NEW_PASSWORD", "set": "TEST_NEW", "expectedVersion": expected}})
			p, e := prepareChanges(&s, c, "8", fixedTime)
			if expected == "0" {
				if e != nil || p.State.Secrets.Scopes["core"]["NEW_PASSWORD"].Version != "8" {
					t.Fatalf("new name: %v", e)
				}
			} else {
				f := requireFault(t, e, "PRECONDITION_FAILED")
				if f.Current.Version != "0" {
					t.Fatal("absence version is not zero")
				}
			}
		})
	}
	s = seededState(t)
	c = changedLink(t, s, pppID, "7", map[string]any{"password": map[string]any{"secret": "WAN_PASSWORD", "set": "TEST_ONLY_OLD_VALUE", "expectedVersion": "7"}})
	p, e = prepareChanges(&s, c, "8", fixedTime)
	if e != nil || !p.Noop || p.State.Version != "7" {
		t.Fatalf("identical value created history: %v", e)
	}
	v := Version("7")
	c, _ = ObjectChange("delete", "links", secondPPP, &v, nil)
	p, e = prepareChanges(&s, c, "8", fixedTime)
	if e != nil || p.State.Secrets.Scopes["core"]["WAN_PASSWORD"].Value != "TEST_ONLY_OLD_VALUE" {
		t.Fatalf("removed shared named value: %v", e)
	}
}

func TestBatchLocalReferences(t *testing.T) {
	raw := []byte(`{"operations":[{"op":"create","collection":"links","localId":"uplink","value":{"name":"@uplink","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"52:54:00:00:00:01"}},{"op":"create","collection":"zones","localId":"lan","value":{"name":"lan"}},{"op":"create","collection":"segments","localId":"seg","value":{"name":"lan","link":"@uplink","zone":"@lan","addressing":{"ipv4":{"mode":"static","addresses":["192.0.2.7/24"]},"ipv6":{"mode":"none"}}}}]}`)
	c, e := DecodeChanges(raw)
	if e != nil {
		t.Fatal(e)
	}
	s := initialState()
	p, e := prepareChanges(&s, c, "1", fixedTime)
	if e != nil {
		t.Fatal(e)
	}
	if p.State.Config.Links[0].Name != "@uplink" || p.State.Config.Segments[0].Link != p.Created["uplink"] || p.State.Config.Segments[0].Zone != p.Created["lan"] {
		t.Fatal("labels did not resolve exclusively in reference fields")
	}
	s = initialState()
	c, _ = DecodeChanges(bytes.ReplaceAll(raw, []byte(`"zone":"@lan"`), []byte(`"zone":"@missing"`)))
	_, e = prepareChanges(&s, c, "1", fixedTime)
	requireFault(t, e, "VALIDATION_FAILED")
	s = seededState(t)
	v := Version("7")
	c, _ = ObjectChange("delete", "zones", s.Config.Zones[0].ID, &v, nil)
	_, e = prepareChanges(&s, c, "8", fixedTime)
	requireFault(t, e, "VALIDATION_FAILED")
}

func TestSecretViewsAndErrors(t *testing.T) {
	s := seededState(t)
	view, e := publicSnapshot(s)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(view)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(raw, []byte("TEST_ONLY_OLD_VALUE")) || !bytes.Contains(raw, []byte(`"secret":"WAN_PASSWORD"`)) {
		t.Fatal("view leaked value or lost named reference")
	}
	for _, pw := range []map[string]any{{"secret": "other/SCOPE"}, {"secret": "WAN_PASSWORD", "isSet": true}, {"secret": "WAN_PASSWORD", "set": "TEST_ONLY_DO_NOT_LOG"}, {"secret": "WAN_PASSWORD", "set": "TEST_ONLY_DO_NOT_LOG", "expectedVersion": "*"}} {
		s := seededState(t)
		c := changedLink(t, s, pppID, "7", map[string]any{"password": pw})
		var logs bytes.Buffer
		old := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
		_, e := prepareChanges(&s, c, "8", fixedTime)
		slog.SetDefault(old)
		if e == nil {
			t.Fatal("accepted bad secret command")
		}
		if strings.Contains(e.Error()+logs.String(), "TEST_ONLY_DO_NOT_LOG") {
			t.Fatal("diagnostic disclosed submitted secret")
		}
	}
}

func TestObjectChangeStrictnessAndPointers(t *testing.T) {
	v := Version("7")
	for _, raw := range []string{`{"password":{"secret":"WAN_PASSWORD","set":"\ud800","expectedVersion":"7"}}`, `{"name":"x","name":"y"}`} {
		if _, e := ObjectChange("replace", "links", pppID, &v, []byte(raw)); e == nil {
			t.Fatal("single-object strict boundary bypassed")
		}
	}
	s := seededState(t)
	c := changedLink(t, s, pppID, "7", map[string]any{"name": "second-ppp"})
	raw, _ := json.Marshal(c)
	batch, e := DecodeChanges(raw)
	if e != nil {
		t.Fatal(e)
	}
	_, e = prepareChanges(&s, batch, "8", fixedTime)
	f := requireFault(t, e, "VALIDATION_FAILED")
	if len(f.Details) == 0 || f.Details[0].Path != "/operations/0/value/name" {
		t.Fatalf("wrong batch pointer: %+v", f.Details)
	}
}
