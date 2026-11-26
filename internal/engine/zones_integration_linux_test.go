//go:build integration

package engine

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestZoneColorRoundTrip(t *testing.T) {
	h, _ := configurationFixture(t)

	// Create zone with explicit color.
	w := configCall(h, http.MethodPost, "/api/v1/config/zones", `{"name":"DMZ","description":"Public servers","color":"violet"}`, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("create zone = %d %s", w.Code, w.Body.String())
	}
	created := apiObject(t, w)
	id := valueID(t, created)
	var obj map[string]any
	if err := json.Unmarshal(created.Value, &obj); err != nil {
		t.Fatal(err)
	}
	if obj["color"] != "violet" {
		t.Fatalf("created color = %v, want violet", obj["color"])
	}

	// Update zone color.
	w = configCall(h, http.MethodPut, "/api/v1/config/zones/"+id, `{"name":"DMZ","description":"Public servers","color":"teal"}`, `"`+id+`.`+string(created.Version)+`"`)
	if w.Code != http.StatusOK {
		t.Fatalf("update zone color = %d %s", w.Code, w.Body.String())
	}
	updated := apiObject(t, w)
	obj = nil
	if err := json.Unmarshal(updated.Value, &obj); err != nil {
		t.Fatal(err)
	}
	if obj["color"] != "teal" {
		t.Fatalf("updated color = %v, want teal", obj["color"])
	}

	// Clear zone color back to Automatic (omitted).
	w = configCall(h, http.MethodPut, "/api/v1/config/zones/"+id, `{"name":"DMZ","description":"Public servers"}`, `"`+id+`.`+string(updated.Version)+`"`)
	if w.Code != http.StatusOK {
		t.Fatalf("clear zone color = %d %s", w.Code, w.Body.String())
	}
	cleared := apiObject(t, w)
	obj = nil
	if err := json.Unmarshal(cleared.Value, &obj); err != nil {
		t.Fatal(err)
	}
	if _, exists := obj["color"]; exists {
		t.Fatalf("expected color to be cleared/omitted, got %v", obj["color"])
	}

	// Reject empty string and unknown color with 422.
	for _, bad := range []string{`{"name":"BadEmpty","color":""}`, `{"name":"BadName","color":"chartreuse"}`} {
		w = configCall(h, http.MethodPost, "/api/v1/config/zones", bad, "")
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422 for %s, got %d %s", bad, w.Code, w.Body.String())
		}
	}
}
