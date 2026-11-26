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

func TestLLDPAPI(t *testing.T) {
	orig := readLLDP
	t.Cleanup(func() { readLLDP = orig })

	h := newHandler(nil, nil)

	// Empty neighbor table.
	readLLDP = func(context.Context) ([]host.LLDPNeighbor, error) { return nil, nil }
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, rootRequest(http.MethodGet, "/api/v1/status/lldp"))
	if rec.Code != http.StatusOK {
		t.Fatalf("empty GET: status %d: %s", rec.Code, rec.Body)
	}
	var body lldpResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Items == nil || len(body.Items) != 0 {
		t.Fatalf("want empty non-nil items: %v %s", err, rec.Body)
	}

	// HEAD returns 200 with empty body.
	head := httptest.NewRecorder()
	h.ServeHTTP(head, rootRequest(http.MethodHead, "/api/v1/status/lldp"))
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %q", head.Code, head.Body)
	}

	// POST returns 405.
	post := httptest.NewRecorder()
	h.ServeHTTP(post, rootRequest(http.MethodPost, "/api/v1/status/lldp"))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: want 405, got %d", post.Code)
	}

	// Populated neighbor table.
	readLLDP = func(context.Context) ([]host.LLDPNeighbor, error) {
		return []host.LLDPNeighbor{{
			IfIndex:         2,
			Interface:       "enp1s0",
			ChassisID:       "52:54:00:aa:bb:10",
			PortID:          "gi1/0/24",
			PortDescription: "Uplink",
			SystemName:      "core-sw",
			Capabilities:    []string{"bridge", "router"},
		}}, nil
	}
	pop := httptest.NewRecorder()
	h.ServeHTTP(pop, rootRequest(http.MethodGet, "/api/v1/status/lldp"))
	if pop.Code != http.StatusOK {
		t.Fatalf("populated GET: %d %s", pop.Code, pop.Body)
	}
	if err := json.Unmarshal(pop.Body.Bytes(), &body); err != nil || len(body.Items) != 1 || body.Items[0].SystemName != "core-sw" {
		t.Fatalf("populated body: %v %s", err, pop.Body)
	}

	// Reader failure returns 503 INVENTORY_UNAVAILABLE.
	readLLDP = func(context.Context) ([]host.LLDPNeighbor, error) { return nil, errors.New("networkd down") }
	fail := httptest.NewRecorder()
	h.ServeHTTP(fail, rootRequest(http.MethodGet, "/api/v1/status/lldp"))
	if fail.Code != http.StatusServiceUnavailable {
		t.Fatalf("reader error: want 503, got %d", fail.Code)
	}
}
