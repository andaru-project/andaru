package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/andaru-project/andaru/internal/host"
)

func TestOffloadsAPI(t *testing.T) {
	orig := readOffloads
	t.Cleanup(func() { readOffloads = orig })
	h := newHandler(nil, nil)

	readOffloads = func(context.Context) ([]host.DeviceOffloads, error) {
		return []host.DeviceOffloads{{Interface: "wan", PermanentAddress: "52:54:00:00:00:01",
			Offloads: map[string]host.OffloadState{"largeReceive": {Fixed: true}, "genericReceive": {Active: true}}}}, nil
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, rootRequest(http.MethodGet, "/api/v1/status/offloads"))
	var body offloadsResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || len(body.Items) != 1 || !body.Items[0].Offloads["genericReceive"].Active {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body)
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
