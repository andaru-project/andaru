package authz

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"testing"
)

// Built-in roles ship with the software: an upgrade that changes them must not change any configuration's digest.
func TestDigestLeavesTheBuiltInRolesOut(t *testing.T) {
	want := sha256.Sum256([]byte("[]"))
	if got, err := Digest(nil); err != nil || got != hex.EncodeToString(want[:]) {
		t.Fatalf("Digest(nil) = %q, error=%v; want the digest of no roles", got, err)
	}
}

func TestCapabilityCatalogAndBuiltIns(t *testing.T) {
	catalog := Catalog()
	if len(catalog) == 0 {
		t.Fatal("empty capability catalog")
	}
	seen := map[Capability]bool{}
	for _, capability := range catalog {
		if capability == "" || seen[capability] {
			t.Fatalf("invalid or duplicate capability %q", capability)
		}
		seen[capability] = true
	}

	builtIns := BuiltIns()
	if len(builtIns) != 2 || builtIns[0].ID != AdministratorRoleID || builtIns[1].ID != ReadOnlyRoleID {
		t.Fatalf("unexpected built-ins: %+v", builtIns)
	}
	if !slices.Equal(builtIns[0].Capabilities, catalog) {
		t.Fatalf("administrator does not dynamically grant the catalog: %v", builtIns[0].Capabilities)
	}
	wantReadOnly := []Capability{ConfigurationRead, NotificationsRead, StatusRead}
	if !slices.Equal(builtIns[1].Capabilities, wantReadOnly) {
		t.Fatalf("read-only grants are not the explicit safe list: %v", builtIns[1].Capabilities)
	}
	catalog[0] = "mutated"
	builtIns[0].Capabilities[0] = "mutated"
	if Catalog()[0] == "mutated" || BuiltIns()[0].Capabilities[0] == "mutated" {
		t.Fatal("callers can mutate catalog or built-ins")
	}
}

func TestRoleValidationAndCanonicalDigest(t *testing.T) {
	first := []Role{
		{
			ID:           "rol_00000000-0000-4000-8000-000000000002",
			Name:         "Network operator",
			Description:  "May inspect and edit saved configuration.",
			Capabilities: []Capability{ConfigurationWrite, StatusRead, ConfigurationRead},
		},
		{ID: "rol_00000000-0000-4000-8000-000000000008", Name: "Observer", Capabilities: []Capability{StatusRead}},
	}
	permuted := []Role{
		{ID: first[1].ID, Name: first[1].Name, Capabilities: []Capability{StatusRead}},
		{
			ID:           first[0].ID,
			Name:         first[0].Name,
			Description:  first[0].Description,
			Capabilities: []Capability{ConfigurationRead, ConfigurationWrite, StatusRead},
		},
	}
	one, err := Digest(first)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Digest(permuted)
	if err != nil {
		t.Fatal(err)
	}
	if one != two || len(one) != 64 {
		t.Fatalf("digest is not canonical SHA-256: %q %q", one, two)
	}

	for name, roles := range map[string][]Role{
		"built-in id":   {{ID: AdministratorRoleID, Name: "Custom", Capabilities: []Capability{StatusRead}}},
		"built-in name": {{ID: "rol_00000000-0000-4000-8000-000000000003", Name: "Administrator", Capabilities: []Capability{StatusRead}}},
		"unknown cap":   {{ID: "rol_00000000-0000-4000-8000-000000000004", Name: "Custom", Capabilities: []Capability{"unknown.read"}}},
		"duplicate cap": {{ID: "rol_00000000-0000-4000-8000-000000000005", Name: "Custom", Capabilities: []Capability{StatusRead, StatusRead}}},
		"built-in flag": {{ID: "rol_00000000-0000-4000-8000-000000000006", Name: "Custom", BuiltIn: ReadOnlyRoleID, Capabilities: []Capability{StatusRead}}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateRoles(roles); err == nil {
				t.Fatal("accepted invalid custom role")
			}
		})
	}
}

func TestRoleResolve(t *testing.T) {
	custom := []Role{{ID: "rol_00000000-0000-4000-8000-000000000007", Name: "Operator", Capabilities: []Capability{ConfigurationWrite, StatusRead}}}
	snapshot, err := NewSnapshot(custom)
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Resolve([]string{custom[0].ID, ReadOnlyRoleID, "missing"}); !slices.Equal(got, []Capability{ConfigurationRead, ConfigurationWrite, NotificationsRead, StatusRead}) {
		t.Fatalf("unexpected resolved capabilities: %v", got)
	}
	if got := snapshot.Resolve([]string{AdministratorRoleID}); !slices.Equal(got, Catalog()) {
		t.Fatalf("administrator did not resolve dynamically: %v", got)
	}
}

func TestActorCanonicalLabel(t *testing.T) {
	accountID := "00000000-0000-4000-8000-000000000001"
	tokenID := "AAAAAAAAAAAAAAAAAAAAAA"
	for _, test := range []struct {
		actor Actor
		want  string
	}{
		{Actor{Kind: ActorSystem, ID: "root"}, "system.root"},
		{Actor{Kind: ActorAccount, ID: accountID}, "account:" + accountID},
		{Actor{Kind: ActorToken, ID: tokenID}, "token:" + tokenID},
	} {
		got, err := test.actor.CanonicalLabel()
		if err != nil || got != test.want {
			t.Fatalf("actor %+v label = %q, error=%v", test.actor, got, err)
		}
	}
	for _, actor := range []Actor{
		{}, {Kind: ActorSystem, ID: "0"}, {Kind: ActorAccount, ID: "00000000-0000-1000-8000-000000000001"},
		{Kind: ActorAccount, ID: "00000000-0000-4000-7000-000000000001"}, {Kind: ActorToken, ID: "short"},
	} {
		if label, err := actor.CanonicalLabel(); err == nil {
			t.Fatalf("invalid actor %+v serialized as %q", actor, label)
		}
	}
}

func TestSnapshotResolvesBuiltInRoles(t *testing.T) {
	snapshot, err := NewSnapshot(nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Catalog()
	slices.Sort(want)
	if got := snapshot.Resolve([]string{AdministratorRoleID}); !slices.Equal(got, want) {
		t.Fatalf("administrator resolves to %v, want the whole catalog %v", got, want)
	}
	if got := snapshot.Resolve([]string{ReadOnlyRoleID}); !slices.Equal(got, []Capability{ConfigurationRead, NotificationsRead, StatusRead}) {
		t.Fatalf("read-only resolves to %v", got)
	}
	if len(snapshot.Roles) != 0 {
		t.Fatalf("built-in roles leaked into the custom roles: %v", snapshot.Roles)
	}
}
