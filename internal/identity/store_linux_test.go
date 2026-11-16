package identity

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRestartAndSchema(t *testing.T) {
	if os.Geteuid()!=0 { t.Skip("requires root") }
	dir:=filepath.Join(t.TempDir(),"state")
	s,err:=Open(context.Background(),dir); if err!=nil { t.Fatal(err) }
	rev,err:=s.Revision(context.Background()); if err!=nil || rev!=0 { t.Fatalf("revision %d, %v",rev,err) }
	if err=s.Close(); err!=nil { t.Fatal(err) }
	s,err=Open(context.Background(),dir); if err!=nil { t.Fatal(err) }; defer s.Close()
	var app,version int
	if err=s.db.QueryRow("PRAGMA application_id").Scan(&app); err!=nil || app!=applicationID { t.Fatalf("app=%x err=%v",app,err) }
	if err=s.db.QueryRow("PRAGMA user_version").Scan(&version); err!=nil || version!=LayoutVersion { t.Fatalf("version=%d err=%v",version,err) }
	for _,table:=range []string{"accounts","tombstones","account_roles","password_credentials","api_tokens","sessions","login_limits","metadata","revisions","operations"} {
		var n int
		if err=s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?",table).Scan(&n); err!=nil || n!=1 { t.Fatalf("table %s: n=%d err=%v",table,n,err) }
	}
}

func TestOpenRefusesWrongApplication(t *testing.T) {
	if os.Geteuid()!=0 { t.Skip("requires root") }
	dir:=filepath.Join(t.TempDir(),"state")
	first,err:=Open(context.Background(),dir); if err!=nil { t.Fatal(err) }; first.Close()
	db,err:=Open(context.Background(),dir); if err!=nil {t.Fatal(err)}
	if _,err=db.db.Exec("PRAGMA application_id=123"); err!=nil {t.Fatal(err)}; db.Close()
	if _,err=Open(context.Background(),dir); err==nil {t.Fatal("accepted database for another application")}
}
