// SPDX-License-Identifier: AGPL-3.0-or-later
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/host"
	npmodel "github.com/andaru-project/andaru/internal/netplane/model"
	"github.com/andaru-project/andaru/internal/netplane/topology"
	"github.com/andaru-project/andaru/internal/netplane/vlanflow"
	"github.com/andaru-project/andaru/internal/render"
)

func TestFleetSecretsEncryptedAtRest(t *testing.T) {
	dir := t.TempDir()
	fs, err := openFleetSecrets(dir)
	if err != nil {
		t.Fatalf("openFleetSecrets: %v", err)
	}
	const secretVal = "super-secret-api-token-999"
	if err := fs.Set("sw-core", map[string]string{"password": secretVal}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "fleet-secrets.enc"))
	if err != nil {
		t.Fatalf("read ciphertext: %v", err)
	}
	if bytes.Contains(raw, []byte(secretVal)) {
		t.Fatal("plaintext secret leaked into fleet-secrets.enc")
	}

	reopened, err := openFleetSecrets(dir)
	if err != nil {
		t.Fatalf("reopen fleetSecrets: %v", err)
	}
	if got := reopened.Get("sw-core")["password"]; got != secretVal {
		t.Fatalf("reopened secret = %q, want %q", got, secretVal)
	}
	if err := reopened.Delete("sw-core"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := reopened.Get("sw-core"); len(got) != 0 {
		t.Fatalf("expected deleted secrets to be empty, got %v", got)
	}
}

func TestBuildLocalSnapshotMapsHostAndConfig(t *testing.T) {
	const (
		bondID   = "lnk_00000000-0000-4000-8000-000000000003"
		vlanID   = "lnk_00000000-0000-4000-8000-000000000004"
	)
	bondKernel := render.KernelName("bond", bondID)
	vlanKernel := render.KernelName("vlan", vlanID)

	src := localHostSource{
		Hostname: func() string { return "andaru-gw" },
		ReadLinks: func(context.Context) ([]host.LinkStatus, error) {
			return []host.LinkStatus{
				{IfIndex: 1, Name: "lo", LinkType: "loopback", MTU: 65536, OperState: "UNKNOWN", Flags: []string{"UP", "LOOPBACK"}},
				{IfIndex: 2, Name: "enp1s0", LinkType: "ether", MTU: 1500, OperState: "UP", Flags: []string{"UP", "RUNNING"}, LinkAddress: "00:11:22:33:44:01", PermanentAddress: "00:11:22:33:44:01"},
				{IfIndex: 3, Name: "enp2s0", LinkType: "ether", MTU: 1500, OperState: "UP", Flags: []string{"UP", "RUNNING"}, LinkAddress: "00:11:22:33:44:02", PermanentAddress: "00:11:22:33:44:02"},
				// Virtual links inherit the physical NIC's MAC and must not shadow enp1s0 in hardware MAC lookup.
				{IfIndex: 4, Name: bondKernel, LinkType: "ether", Kind: "bond", MTU: 1500, OperState: "UP", Flags: []string{"UP", "RUNNING"}, LinkAddress: "00:11:22:33:44:01", PermanentAddress: "00:11:22:33:44:01"},
				{IfIndex: 5, Name: vlanKernel, LinkType: "ether", Kind: "vlan", MTU: 1500, OperState: "UP", Flags: []string{"UP", "RUNNING"}, LinkAddress: "00:11:22:33:44:01", PermanentAddress: "00:11:22:33:44:01", Addresses: []host.AddressStatus{
					{Family: "ipv4", Address: "10.10.10.1", PrefixLength: 24, Scope: "global"},
				}},
			}, nil
		},
		ReadConfig: func(context.Context) (config.SnapshotView, error) {
			return config.SnapshotView{
				State:        "saved",
				SavedVersion: "12",
				Value: []byte(`{
					"schemaVersion": 1,
					"links": [
						{"id": "lnk_00000000-0000-4000-8000-000000000001", "name": "uplink-a", "kind": "ethernet", "enabled": true, "permanentMac": "00:11:22:33:44:01"},
						{"id": "lnk_00000000-0000-4000-8000-000000000002", "name": "uplink-b", "kind": "ethernet", "enabled": true, "permanentMac": "00:11:22:33:44:02"},
						{"id": "` + bondID + `", "name": "Core LAG", "kind": "bond", "enabled": true, "members": ["lnk_00000000-0000-4000-8000-000000000001", "lnk_00000000-0000-4000-8000-000000000002"]},
						{"id": "` + vlanID + `", "name": "LAN VLAN 10", "kind": "vlan", "enabled": true, "parent": "` + bondID + `", "vlanId": 10}
					],
					"segments": [
						{"id": "seg_lan", "name": "LAN", "link": "` + vlanID + `", "zone": "zon_lan"}
					],
					"zones": [
						{"id": "zon_lan", "name": "lan"}
					],
					"rules": [{}, {}],
					"nat": {"portForwards": [{}], "oneToOne": []}
				}`),
			}, nil
		},
	}

	snap, err := buildLocalSnapshot(context.Background(), src, time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("buildLocalSnapshot: %v", err)
	}
	snap.Normalize()
	if len(snap.Devices) != 1 || snap.Devices[0].ID != fleetLocalDeviceID || snap.Devices[0].Name != "andaru-gw" || snap.Devices[0].MgmtIP != "10.10.10.1" {
		t.Fatalf("unexpected local device: %+v", snap.Devices)
	}
	if caps := snap.Devices[0].Capabilities; !slices.Contains(caps, npmodel.CapDiagPing) || !slices.Contains(caps, npmodel.CapDiagTraceroute) {
		t.Fatalf("local device offers no ping or traceroute: %v", caps)
	}
	if len(snap.LagGroups) != 1 || snap.LagGroups[0].Name != bondKernel || !slices.Equal(snap.LagGroups[0].MemberLinks, []string{"enp1s0", "enp2s0"}) {
		t.Fatalf("unexpected lag groups: %+v", snap.LagGroups)
	}
	foundBondVLAN := false
	for _, l := range snap.Links {
		if l.IfName == bondKernel && slices.Contains(l.TaggedVLANs, 10) {
			foundBondVLAN = true
		}
	}
	if !foundBondVLAN {
		t.Fatalf("expected %s to carry tagged VLAN 10, links = %+v", bondKernel, snap.Links)
	}
	if len(snap.Segments) != 1 || snap.Segments[0].ParentLink != bondKernel || snap.Segments[0].VLANID == nil || *snap.Segments[0].VLANID != 10 || snap.Segments[0].ZoneID != "zon_lan" || len(snap.Segments[0].Addresses) != 1 {
		t.Fatalf("unexpected segments: %+v", snap.Segments)
	}
	if len(snap.Zones) != 1 || !slices.Equal(snap.Zones[0].SegmentIDs, []string{"seg_lan"}) {
		t.Fatalf("unexpected zones: %+v", snap.Zones)
	}
	if len(snap.Firewalls) != 1 || *snap.Firewalls[0].PolicyRuleCount != 2 || *snap.Firewalls[0].NatRuleCount != 1 || snap.Firewalls[0].SavedVersion != "12" {
		t.Fatalf("unexpected firewall state: %+v", snap.Firewalls)
	}
}

func TestFleetAPIAuthzAndTargetLifecycle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	local := localHostSource{
		Hostname: func() string { return "andaru-box" },
		ReadLinks: func(context.Context) ([]host.LinkStatus, error) {
			return []host.LinkStatus{
				{IfIndex: 2, Name: "eth0", LinkType: "ether", MTU: 1500, OperState: "UP", Flags: []string{"UP", "RUNNING"}, LinkAddress: "00:11:22:33:44:55", Addresses: []host.AddressStatus{
					{Family: "ipv4", Address: "10.0.0.1", PrefixLength: 24, Scope: "global"},
				}},
			}, nil
		},
		ReadConfig: func(context.Context) (config.SnapshotView, error) {
			return config.SnapshotView{
				State:        "saved",
				SavedVersion: "3",
				Value: []byte(`{
					"schemaVersion": 1,
					"links": [
						{"id": "lnk_eth0", "name": "eth0", "kind": "ethernet", "enabled": true},
						{"id": "lnk_v20", "name": "eth0.20", "kind": "vlan", "enabled": true, "parent": "lnk_eth0", "vlanId": 20}
					],
					"segments": [{"id": "seg_iot", "name": "IoT", "link": "lnk_v20", "zone": "zon_iot"}],
					"zones": [{"id": "zon_iot", "name": "iot"}]
				}`),
			}, nil
		},
	}

	fleet, err := openFleetService(ctx, dir, local, nil)
	if err != nil {
		t.Fatalf("openFleetService: %v", err)
	}
	defer func() { _ = fleet.Close() }()

	handler := newHandlerWithFleet(local.ReadLinks, http.NotFoundHandler(), nil, nil, fleet)

	withRoot := func(r *http.Request) *http.Request {
		return r.WithContext(context.WithValue(r.Context(), peerKey{}, peerIdentity{uid: 0, verified: true}))
	}
	withCaps := func(r *http.Request, caps ...authz.Capability) *http.Request {
		const testAccountID = "00000000-0000-4000-8000-000000000001"
		return r.WithContext(context.WithValue(r.Context(), authenticatedActorKey{}, requestActor{
			Actor:        authz.Actor{Kind: authz.ActorAccount, ID: testAccountID},
			AccountID:    testAccountID,
			Source:       "127.0.0.1",
			Capabilities: caps,
		}))
	}

	// Read-only actor cannot create targets (403).
	req := withCaps(httptest.NewRequest(http.MethodPost, "/api/v1/fleet/targets", strings.NewReader(`{"id":"sw1","driver":"snmp","endpoint":"192.0.2.10"}`)), authz.StatusRead)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /api/v1/fleet/targets with StatusRead = %d, want 403", rec.Code)
	}

	// Trigger local host refresh and verify local Andaru appears in topology and VLAN flow.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, withRoot(httptest.NewRequest(http.MethodPost, "/api/v1/fleet/targets/local/poll", nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/fleet/targets/local/poll = %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, withCaps(httptest.NewRequest(http.MethodGet, "/api/v1/fleet/topology", nil), authz.StatusRead))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/fleet/topology = %d: %s", rec.Code, rec.Body.String())
	}
	var topo topology.Graph
	if err := json.Unmarshal(rec.Body.Bytes(), &topo); err != nil {
		t.Fatalf("decode topology: %v", err)
	}
	if len(topo.Nodes) != 1 || topo.Nodes[0].ID != fleetLocalDeviceID {
		t.Fatalf("expected local device in topology, got %+v", topo.Nodes)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, withCaps(httptest.NewRequest(http.MethodGet, "/api/v1/fleet/vlans/flow", nil), authz.StatusRead))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/fleet/vlans/flow = %d: %s", rec.Code, rec.Body.String())
	}
	var flow vlanflow.Flow
	if err := json.Unmarshal(rec.Body.Bytes(), &flow); err != nil {
		t.Fatalf("decode vlanflow: %v", err)
	}
	if len(flow.VLANs) != 1 || flow.VLANs[0].ID != 20 || len(flow.VLANs[0].Gateways) != 1 {
		t.Fatalf("expected VLAN 20 gateway from local Andaru host, got %+v", flow.VLANs)
	}

	// Diagnostics on the local box need configuration.write, and a bad request never reaches a tool.
	for _, tc := range []struct {
		caps []authz.Capability
		body string
		want int
	}{
		{[]authz.Capability{authz.StatusRead}, `{"kind":"ping","device_id":"andaru:local","target":"192.0.2.1"}`, http.StatusForbidden},
		{[]authz.Capability{authz.ConfigurationWrite}, `{"kind":"ping","device_id":"andaru:local","target":"-f"}`, http.StatusBadRequest},
		{[]authz.Capability{authz.ConfigurationWrite}, `{"kind":"ping","device_id":"andaru:local","target":"192.0.2.1","params":{"count":"11"}}`, http.StatusBadRequest},
		{[]authz.Capability{authz.ConfigurationWrite}, `{"kind":"traceroute","device_id":"andaru:local","target":"a.example; reboot"}`, http.StatusBadRequest},
		{[]authz.Capability{authz.ConfigurationWrite}, `{"kind":"cable_test","device_id":"andaru:local","target":"eth0"}`, http.StatusNotImplemented},
	} {
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, withCaps(httptest.NewRequest(http.MethodPost, "/api/v1/fleet/diagnostics", strings.NewReader(tc.body)), tc.caps...))
		if rec.Code != tc.want {
			t.Errorf("POST /api/v1/fleet/diagnostics %s with %v = %d, want %d: %s", tc.body, tc.caps, rec.Code, tc.want, rec.Body.String())
		}
	}

	// Upsert a remote target with encrypted credentials and verify secret never leaks in API response.
	const comm = "private-snmp-community-secret"
	payload := `{"id":"sw-lab","name":"Lab Switch","driver":"snmp","endpoint":"192.0.2.20","secrets":{"community":"` + comm + `"}}`
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, withRoot(httptest.NewRequest(http.MethodPost, "/api/v1/fleet/targets", strings.NewReader(payload))))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/fleet/targets = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), comm) {
		t.Fatal("secret leaked in POST /api/v1/fleet/targets response")
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, withRoot(httptest.NewRequest(http.MethodGet, "/api/v1/fleet/targets", nil)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"sw-lab"`) || strings.Contains(rec.Body.String(), comm) {
		t.Fatalf("GET /api/v1/fleet/targets = %d: %s", rec.Code, rec.Body.String())
	}

	// Built-in local target cannot be deleted.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, withRoot(httptest.NewRequest(http.MethodDelete, "/api/v1/fleet/targets/local", nil)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("DELETE /api/v1/fleet/targets/local = %d, want 409", rec.Code)
	}

	// Remote target can be deleted and its secret is removed.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, withRoot(httptest.NewRequest(http.MethodDelete, "/api/v1/fleet/targets/sw-lab", nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE /api/v1/fleet/targets/sw-lab = %d: %s", rec.Code, rec.Body.String())
	}
	if got := fleet.secrets.Get("sw-lab"); len(got) != 0 {
		t.Fatalf("expected sw-lab secret to be deleted, got %v", got)
	}

	// Reject unix:// endpoints, sub-5s poll intervals, and invalid target IDs.
	for _, bad := range []string{
		`{"id":"local-sock","driver":"andaru","endpoint":"unix:///run/andaru/engine.sock"}`,
		`{"id":"fast-poll","driver":"snmp","endpoint":"192.0.2.30","poll_interval":"100ms"}`,
		`{"id":"bad id?","driver":"snmp","endpoint":"192.0.2.30"}`,
	} {
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, withRoot(httptest.NewRequest(http.MethodPost, "/api/v1/fleet/targets", strings.NewReader(bad))))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST invalid target %s = %d, want 400", bad, rec.Code)
		}
	}

	// Deleting target A must NOT wipe disabled target B, and omitting secrets on update preserves them.
	for _, body := range []string{
		`{"id":"sw-a","driver":"snmp","endpoint":"192.0.2.31","secrets":{"community":"comm-a"}}`,
		`{"id":"sw-b","driver":"snmp","endpoint":"192.0.2.32","enabled":false,"secrets":{"community":"comm-b"}}`,
	} {
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, withRoot(httptest.NewRequest(http.MethodPost, "/api/v1/fleet/targets", strings.NewReader(body))))
		if rec.Code != http.StatusOK {
			t.Fatalf("POST target = %d: %s", rec.Code, rec.Body.String())
		}
	}
	// Update sw-b without secrets field: existing secret must be preserved.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, withRoot(httptest.NewRequest(http.MethodPost, "/api/v1/fleet/targets", strings.NewReader(`{"id":"sw-b","name":"Disabled Switch","driver":"snmp","endpoint":"192.0.2.32","enabled":false}`))))
	if rec.Code != http.StatusOK {
		t.Fatalf("update sw-b without secrets = %d: %s", rec.Code, rec.Body.String())
	}
	if got := fleet.secrets.Get("sw-b")["community"]; got != "comm-b" {
		t.Fatalf("sw-b secret after update = %q, want comm-b", got)
	}
	// Delete sw-a; sw-b must survive in both memory and SQLite.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, withRoot(httptest.NewRequest(http.MethodDelete, "/api/v1/fleet/targets/sw-a", nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE sw-a = %d: %s", rec.Code, rec.Body.String())
	}
	stored, err := fleet.store.ListTargets(ctx)
	if err != nil {
		t.Fatalf("ListTargets: %v", err)
	}
	foundB := false
	for _, st := range stored {
		if st.ID == "sw-b" {
			foundB = true
		}
	}
	if !foundB {
		t.Fatalf("disabled target sw-b was wiped when sw-a was deleted: %+v", stored)
	}

	// Fleet unavailable fallback returns 503 without taking down core engine.
	unavail := newHandlerWithFleet(local.ReadLinks, http.NotFoundHandler(), nil, nil, fleetUnavailableHandler())
	rec = httptest.NewRecorder()
	unavail.ServeHTTP(rec, withRoot(httptest.NewRequest(http.MethodGet, "/api/v1/fleet/topology", nil)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("fleet unavailable status = %d, want 503", rec.Code)
	}
}
