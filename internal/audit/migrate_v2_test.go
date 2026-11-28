package audit

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/authz"
)

// A LayoutVersion 2 audit database migrates to LayoutVersion 3, keeping its events and accepting the Slice 7
// certificate, ACME and VPN actions.
func TestAuditV2MigratesToV3AndAcceptsCertificateActions(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("audit store requires root")
	}
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "audit.db")
	if err := os.WriteFile(dbPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", databaseURI(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	v2Events := layoutDDL("events")
	cut := strings.Index(v2Events, ",'certificate.created'")
	end := strings.Index(v2Events, "'vpn.openvpn.profile_exported'") + len("'vpn.openvpn.profile_exported'")
	if cut < 0 || end < cut {
		t.Fatal("events DDL lacks the v3 actions")
	}
	v2Events = v2Events[:cut] + v2Events[end:]
	exec := func(stmt string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, stmt, args...); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	exec(v2Events)
	for _, obj := range layoutObjects[1:] {
		exec(obj.ddl)
	}
	exec(seedSchema)
	exec(`INSERT INTO events(id,at,actor_kind,actor_id,source,action,target,result,operation_id,category,coalesced_count) VALUES(7,?,'system','root','local','addon.staged','tailscale','success','','general',1)`, time.Now().UnixMilli())
	exec(`UPDATE retention SET total_inserted_general=7 WHERE id=1`)
	exec(`UPDATE sqlite_sequence SET seq=7 WHERE name='events'`)
	exec("PRAGMA application_id=1095648321")
	exec("PRAGMA user_version=2")
	if _, err := db.ExecContext(ctx, `INSERT INTO events(at,actor_kind,actor_id,source,action,target,result,operation_id,category,coalesced_count) VALUES(1,'system','root','local','certificate.created','','success','','general',1)`); err == nil {
		db.Close()
		t.Fatal("v2 events table accepted a v3 action")
	}
	_ = db.Close()
	for _, suf := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(dbPath + suf); err == nil {
			_ = os.Chmod(dbPath+suf, 0o600)
		}
	}

	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open v2 audit db: %v", err)
	}
	defer s.Close()
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 3 {
		t.Fatalf("user_version = %d, %v; want 3", version, err)
	}
	appended, err := s.Append(ctx, Event{
		Actor:    authz.Actor{Kind: authz.ActorSystem, ID: "root"},
		Source:   "local",
		Action:   ActionCertificateCreated,
		Target:   "crt_6c1f5d0e-2b3a-4c5d-8e9f-0a1b2c3d4e5f",
		Result:   ResultSuccess,
		Category: CategoryGeneral,
	})
	if err != nil {
		t.Fatalf("Append certificate.created after migration: %v", err)
	}
	if appended.ID != 8 {
		t.Fatalf("next event id = %d, want 8", appended.ID)
	}
	items, err := s.ListEvents(ctx, 0, 10)
	if err != nil || len(items) != 2 {
		t.Fatalf("events after migration = %d, %v; want 2", len(items), err)
	}
}

func TestCertificateActionTargets(t *testing.T) {
	for _, tc := range []struct {
		action Action
		target string
		ok     bool
	}{
		{ActionCertificateCreated, "ca_6c1f5d0e-2b3a-4c5d-8e9f-0a1b2c3d4e5f", true},
		{ActionCertificateImported, "crt_6c1f5d0e-2b3a-4c5d-8e9f-0a1b2c3d4e5f", true},
		{ActionCertificateDeleted, "tck_6c1f5d0e-2b3a-4c5d-8e9f-0a1b2c3d4e5f", true},
		{ActionCertificateDeleted, "aco_6c1f5d0e-2b3a-4c5d-8e9f-0a1b2c3d4e5f", false},
		{ActionCertificateCreated, "", false},
		{ActionACMEOrderTriggered, "aco_6c1f5d0e-2b3a-4c5d-8e9f-0a1b2c3d4e5f", true},
		{ActionWireGuardPSKExported, "wg_6c1f5d0e-2b3a-4c5d-8e9f-0a1b2c3d4e5f", true},
		{ActionIPsecInitiated, "ips_6c1f5d0e-2b3a-4c5d-8e9f-0a1b2c3d4e5f", true},
		{ActionOpenVPNProfileExported, "ovp_6c1f5d0e-2b3a-4c5d-8e9f-0a1b2c3d4e5f", true},
		{ActionOpenVPNDisconnected, "ovp_not-a-uuid", false},
	} {
		if got := validTarget(tc.action, tc.target); got != tc.ok {
			t.Errorf("validTarget(%s, %q) = %v, want %v", tc.action, tc.target, got, tc.ok)
		}
	}
}
