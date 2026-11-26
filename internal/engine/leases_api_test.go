package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLeasesStatus(t *testing.T) {
	root := t.TempDir()
	leasesRoot = root
	fixedNow := time.Date(2026, 6, 1, 13, 0, 0, 0, time.UTC)
	origNow := leasesNow
	leasesNow = func() time.Time { return fixedNow }
	t.Cleanup(func() {
		leasesRoot = ""
		leasesNow = origNow
	})

	h := newHandler(nil, http.NotFoundHandler())
	get := func() map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, rootRequest(http.MethodGet, "/api/v1/status/leases"))
		if rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		validResponse(t, "LeasesResponse", rec.Body.Bytes())
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return body
	}

	// Absent leases.json returns empty lists.
	empty := get()
	if len(empty["v4"].([]any)) != 0 || len(empty["v6"].([]any)) != 0 {
		t.Fatalf("want empty leases: %v", empty)
	}

	// HEAD returns 200 with no body; POST returns 405.
	head := httptest.NewRecorder()
	h.ServeHTTP(head, rootRequest(http.MethodHead, "/api/v1/status/leases"))
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD %d %q", head.Code, head.Body.String())
	}
	post := httptest.NewRecorder()
	h.ServeHTTP(post, rootRequest(http.MethodPost, "/api/v1/status/leases"))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST %d", post.Code)
	}

	// Snapshot with active + expired + v6 prefix leases.
	writeUnder(t, root, "/run/andaru-leases/leases.json", `{
		"v4": [
			{"segment":"seg_lan","address":"192.168.1.100","mac":"bc:24:11:26:08:ca","hostname":"client","state":"active","cltt":"2026-06-01T12:00:00Z","expiresAt":"2026-06-01T14:00:00Z"},
			{"segment":"seg_lan","address":"192.168.1.101","mac":"bc:24:11:26:08:cb","hostname":"stale","state":"active","cltt":"2026-06-01T10:00:00Z","expiresAt":"2026-06-01T12:00:00Z"}
		],
		"v6": [
			{"segment":"seg_lan","type":"prefix","address":"fd00:beef:1:100::/56","duid":"00:01:aa:bb","hostname":"router","state":"active","cltt":"2026-06-01T12:30:00Z","expiresAt":"2026-06-01T13:30:00Z"}
		]
	}`)
	body := get()
	v4 := body["v4"].([]any)
	if v4[0].(map[string]any)["state"] != "active" || v4[1].(map[string]any)["state"] != "expired" {
		t.Fatalf("unexpected v4 states: %v", v4)
	}
	v6 := body["v6"].([]any)
	if len(v6) != 1 || v6[0].(map[string]any)["type"] != "prefix" {
		t.Fatalf("unexpected v6: %v", v6)
	}

	// Corrupt snapshot reports 503.
	writeUnder(t, root, "/run/andaru-leases/leases.json", `{not-json`)
	bad := httptest.NewRecorder()
	h.ServeHTTP(bad, rootRequest(http.MethodGet, "/api/v1/status/leases"))
	if bad.Code != http.StatusServiceUnavailable {
		t.Fatalf("corrupt leases.json: want 503, got %d", bad.Code)
	}
}
