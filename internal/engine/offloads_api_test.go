package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/andaru-project/andaru/internal/host"
)

func TestOffloadsAPI(t *testing.T) {
	orig := readOffloads
	origRoot := offloadsRoot
	offloadsRoot = t.TempDir()
	t.Cleanup(func() {
		readOffloads = orig
		offloadsRoot = origRoot
	})
	if err := os.MkdirAll(filepath.Join(offloadsRoot, "run/andaru-offloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(offloadsRoot, "run/andaru-offloads/defaults.json"), []byte(`{"52:54:00:00:00:01":{"generic-receive-offload":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHandler(nil, nil)

	readOffloads = func(context.Context) ([]host.DeviceOffloads, error) {
		return []host.DeviceOffloads{{Interface: "wan", PermanentAddress: "52:54:00:00:00:01",
			Offloads: map[string]host.OffloadState{
				"largeReceive":    {Fixed: true},
				"genericReceive":  {Active: false},
				"tcpSegmentation": {Active: false},
			}}}, nil
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, rootRequest(http.MethodGet, "/api/v1/status/offloads"))
	var body offloadsResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || len(body.Items) != 1 {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body)
	}
	validResponse(t, "OffloadsResponse", rec.Body.Bytes())
	gro := body.Items[0].Offloads["genericReceive"]
	if gro.Active || gro.Default == nil || !*gro.Default {
		t.Fatalf("genericReceive = %+v, want active=false default=true", gro)
	}
	lro := body.Items[0].Offloads["largeReceive"]
	if !lro.Fixed || lro.Default == nil || *lro.Default {
		t.Fatalf("largeReceive = %+v, want fixed=true default=false", lro)
	}
	// tcpSegmentation was absent from the recorded defaults, so default is omitted (not null) on the wire.
	if tso := body.Items[0].Offloads["tcpSegmentation"]; tso.Default != nil {
		t.Fatalf("tcpSegmentation = %+v, want nil default", tso)
	}
	var raw struct {
		Items []struct {
			Offloads map[string]map[string]any `json:"offloads"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, present := raw.Items[0].Offloads["tcpSegmentation"]["default"]; present {
		t.Fatalf("tcpSegmentation serialized default key: %s", rec.Body)
	}

	head := httptest.NewRecorder()
	h.ServeHTTP(head, rootRequest(http.MethodHead, "/api/v1/status/offloads"))
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %q", head.Code, head.Body)
	}

	readOffloads = func(context.Context) ([]host.DeviceOffloads, error) { return nil, errors.New("no ethtool") }
	fail := httptest.NewRecorder()
	h.ServeHTTP(fail, rootRequest(http.MethodGet, "/api/v1/status/offloads"))
	if fail.Code != http.StatusServiceUnavailable {
		t.Fatalf("reader error: want 503, got %d", fail.Code)
	}
}
