package config

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestUnresolvedReferenceView(t *testing.T) {
	state := seededState(t)
	for i := range state.Config.Links {
		if state.Config.Links[i].Password != nil {
			state.Config.Links[i].Password.Unresolved = true
		}
	}
	view, err := publicSnapshot(state)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(view.Value, []byte("TEST_ONLY_OLD_VALUE")) {
		t.Fatal("secret exposed")
	}
	var declaration struct {
		Links []struct {
			Password *struct {
				IsSet, Unresolved bool
				Version           Version
			}
		}
	}
	if err = json.Unmarshal(view.Value, &declaration); err != nil {
		t.Fatal(err)
	}
	for _, l := range declaration.Links {
		if l.Password != nil && (l.Password.IsSet || !l.Password.Unresolved || l.Password.Version != "7") {
			t.Fatal("unresolved reference silently used the same-named local secret")
		}
	}
}

func TestUnresolvedDeclarationSchemas(t *testing.T) {
	for _, tc := range []struct {
		version           int
		unresolved, valid bool
	}{
		// The forms before schema 4 said by their number whether a reference may be unresolved, and 3 needs roles.
		{1, false, true}, {1, true, false}, {2, false, true}, {2, true, true}, {3, true, false}, {4, false, true}, {4, true, true},
	} {
		d := fixtureMap(t)
		d["schemaVersion"] = tc.version
		for _, raw := range d["links"].([]any) {
			l := raw.(map[string]any)
			if pw, ok := l["password"].(map[string]any); ok && tc.unresolved {
				pw["unresolved"] = true
			}
		}
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		_, _, _, err = decodeSaved(raw)
		if (err == nil) != tc.valid {
			t.Fatalf("schema=%d unresolved=%t accepted=%t", tc.version, tc.unresolved, err == nil)
		}
	}
}

func TestMissingBoundSecretViewIsStillCorruption(t *testing.T) {
	state := seededState(t)
	delete(state.Secrets.Scopes["core"], "WAN_PASSWORD")
	if _, err := publicSnapshot(state); err == nil {
		t.Fatal("bound reference lost its secret without a corruption error")
	}
}

func TestUnresolvedReferenceOrdinaryEdits(t *testing.T) {
	for _, bind := range []bool{false, true} {
		state := seededState(t)
		for i := range state.Config.Links {
			if state.Config.Links[i].Password != nil {
				state.Config.Links[i].Password.Unresolved = true
			}
		}
		var target link
		for _, l := range state.Config.Links {
			if l.ID == pppID {
				target = l
			}
		}
		raw, _ := json.Marshal(target)
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		value["name"] = "renamed-ppp"
		delete(value, "password")
		if bind {
			value["password"] = map[string]any{"secret": "WAN_PASSWORD"}
		}
		raw, _ = json.Marshal(value)
		expected := Version("7")
		input, err := ObjectChange("replace", "links", pppID, &expected, raw)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := prepareChanges(&state, input, "8", fixedTime)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range prepared.State.Config.Links {
			if l.ID == pppID && l.Password.Unresolved == bind {
				t.Fatalf("edited reference bind=%t unresolved=%t", bind, l.Password.Unresolved)
			}
			if l.ID == secondPPP && !l.Password.Unresolved {
				t.Fatal("editing one reference rebound another")
			}
		}
	}
}

func TestUnresolvedMissingSecretIsReadable(t *testing.T) {
	state := seededState(t)
	for i := range state.Config.Links {
		if state.Config.Links[i].Password != nil {
			state.Config.Links[i].Password.Unresolved = true
		}
	}
	delete(state.Secrets.Scopes["core"], "WAN_PASSWORD")
	view, err := publicSnapshot(state)
	if err != nil {
		t.Fatal("explicitly unresolved reference made saved state unreadable", err)
	}
	var declaration struct {
		Links []struct {
			Password *struct {
				IsSet, Unresolved bool
				Version           Version
			}
		}
	}
	if err = json.Unmarshal(view.Value, &declaration); err != nil {
		t.Fatal(err)
	}
	for _, l := range declaration.Links {
		if l.Password != nil && (l.Password.IsSet || !l.Password.Unresolved || l.Password.Version != "0") {
			t.Fatal("missing unresolved value has false credential metadata")
		}
	}
}

func TestPublicViewSchemaKeepsUnresolved(t *testing.T) {
	compiler, err := newSchemaCompiler(map[string][]byte{schemaURI: SchemaBundle})
	if err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(schemaURI + "#/$defs/declarationView")
	if err != nil {
		t.Fatal(err)
	}
	{
		state := seededState(t)
		for i := range state.Config.Links {
			if state.Config.Links[i].Password != nil {
				state.Config.Links[i].Password.Unresolved = true
			}
		}
		view, err := publicSnapshot(state)
		if err != nil {
			t.Fatal(err)
		}
		instance, err := decodeStrictLimit(view.Value, documentLimit)
		if err != nil {
			t.Fatal(err)
		}
		err = schema.Validate(instance)
		if err != nil {
			t.Fatalf("unresolved public view refused: %v", err)
		}
	}
}
