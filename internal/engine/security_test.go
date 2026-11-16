package engine

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/authz"
)

func TestRouteCapabilityTableMatchesPublishedRoutes(t *testing.T) {
	if err := validateRouteCapabilities(openAPIDocument, engineRoutes); err != nil {
		t.Fatal(err)
	}
}

func TestRouteCapabilityValidationRefusesUnclassifiedRoute(t *testing.T) {
	document := []byte(`{"openapi":"3.1.0","paths":{"/api/v1/new":{"get":{}}}}`)
	if err := validateRouteCapabilities(document, nil); err == nil || !strings.Contains(err.Error(), "/api/v1/new get") {
		t.Fatalf("unclassified route error = %v", err)
	}
}

func TestActorComesOnlyFromVerifiedContext(t *testing.T) {
	request := httptest.NewRequest("GET", "/api/v1/status/links", nil)
	request.Header.Set("X-Andaru-Actor", "account:attacker")
	request.Header.Set("X-Andaru-Source", "198.51.100.9")
	if _, ok := actorFromRequest(request); ok {
		t.Fatal("headers created an actor")
	}

	request = request.WithContext(context.WithValue(request.Context(), peerKey{}, peerIdentity{uid: 0, verified: true}))
	request.Header.Set("X-Andaru-Actor", "account:attacker")
	request.Header.Set("X-Andaru-Source", "198.51.100.9")
	got, ok := actorFromRequest(request)
	if !ok || got.Actor != (authz.Actor{Kind: authz.ActorSystem, ID: "root"}) || got.Source != "unix:uid:0" {
		t.Fatalf("root peer actor = %+v, verified=%t", got, ok)
	}
}
