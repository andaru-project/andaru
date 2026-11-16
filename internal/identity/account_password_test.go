package identity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"golang.org/x/crypto/argon2"
	"time"
)

func openTestStore(t *testing.T) (*store, string) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("identity store requires root")
	}
	stateDir := filepath.Join(t.TempDir(), "state")
	s, err := Open(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, stateDir
}

func TestBootstrapCreateUpdateAndRestart(t *testing.T) {
	ctx := context.Background()
	s, stateDir := openTestStore(t)
	admin, rev, err := s.BootstrapAdmin(ctx, "rootadmin", "Administrator", []byte("unique-admin-password-41"))
	if err != nil { t.Fatal(err) }
	if admin.Kind != Human || !admin.Enabled || rev != 1 { t.Fatalf("admin=%+v rev=%d", admin, rev) }
	if ok, err := s.VerifyPassword(ctx, admin.ID, []byte("unique-admin-password-41")); err != nil || !ok { t.Fatalf("verify=%v err=%v", ok, err) }
	user, rev, err := s.CreateAccount(ctx, CreateAccountInput{Login:"operator1", DisplayName:"Operator", Kind:Human, Enabled:true}, rev)
	if err != nil { t.Fatal(err) }
	display := "Operator Two"
	updated, rev, err := s.UpdateAccount(ctx, user.ID, rev, UpdateAccountInput{DisplayName:&display})
	if err != nil || updated.DisplayName != "Operator Two" { t.Fatalf("updated=%+v rev=%d err=%v", updated, rev, err) }
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.GetAccount(ctx, user.ID)
	if err != nil || got.DisplayName != "Operator Two" {
		t.Fatalf("restart account=%+v err=%v", got, err)
	}
	accounts, err := s.ListAccounts(ctx)
	if err != nil || len(accounts) != 2 { t.Fatalf("list accounts=%d err=%v",len(accounts),err) }
}
func TestOptionalDisplayAndCustomRolesSurviveRestart(t *testing.T) {
	ctx := context.Background()
	s, stateDir := openTestStore(t)
	admin, revision, err := s.BootstrapAdmin(ctx,"rootadmin","",[]byte("unique-admin-password-41"))
	if err != nil { t.Fatal(err) }
	roleA := "rol_12345678-1234-4234-8234-123456789abc"
	roleB := "rol_22345678-1234-4234-8234-123456789abc"
	if _, _, err = s.CreateAccount(ctx,CreateAccountInput{Login:"invalidrole",DisplayName:"",Kind:Human,RoleIDs:[]string{"not-a-role"}},revision); !errors.Is(err,ErrInvalid) { t.Fatalf("invalid role id: %v",err) }
	user, revision, err := s.CreateAccount(ctx,CreateAccountInput{Login:"roleuser",DisplayName:"",Kind:Human,Enabled:true,RoleIDs:[]string{roleA}},revision)
	if err != nil || user.DisplayName != "" || len(user.RoleIDs) != 1 || user.RoleIDs[0] != roleA { t.Fatalf("create account=%+v err=%v",user,err) }
	sessionHash := make([]byte,32)
	sessionHash[0] = 1
	if _, err = s.db.ExecContext(ctx,"INSERT INTO sessions(session_hash,csrf_hash,account_id,auth_epoch,created_at,last_seen_at,expires_at) VALUES(?,?,?,?,?,?,?)",sessionHash,make([]byte,32),user.ID,0,1,1,9999999999); err != nil { t.Fatal(err) }
	roleIDs := []string{roleB}
	user, revision, err = s.UpdateAccount(ctx,user.ID,revision,UpdateAccountInput{RoleIDs:&roleIDs})
	if err != nil || len(user.RoleIDs) != 1 || user.RoleIDs[0] != roleB { t.Fatalf("updated roles=%v err=%v",user.RoleIDs,err) }
	var sessions int
	if err = s.db.QueryRowContext(ctx,"SELECT count(*) FROM sessions WHERE account_id=?",user.ID).Scan(&sessions); err != nil { t.Fatal(err) }
	if sessions != 0 { t.Fatalf("role assignment retained %d sessions",sessions) }
	removeAdminRole := []string{roleA}
	if _, _, err = s.UpdateAccount(ctx,admin.ID,revision,UpdateAccountInput{RoleIDs:&removeAdminRole}); !errors.Is(err,ErrInvariant) { t.Fatalf("custom role replaced recovery administrator: %v",err) }
	if err = s.ValidateAdministratorInvariant(ctx); err != nil { t.Fatal(err) }
	if err = s.Close(); err != nil { t.Fatal(err) }
	s, err = Open(ctx,stateDir)
	if err != nil { t.Fatal(err) }
	defer s.Close()
	got, err := s.GetAccount(ctx,user.ID)
	if err != nil || got.DisplayName != "" || len(got.RoleIDs) != 1 || got.RoleIDs[0] != roleB { t.Fatalf("restarted account=%+v err=%v",got,err) }
	gotAdmin, err := s.GetAccount(ctx,admin.ID)
	if err != nil || len(gotAdmin.RoleIDs) != 1 || gotAdmin.RoleIDs[0] != "administrator" { t.Fatalf("admin roles=%v err=%v",gotAdmin.RoleIDs,err) }
}

func TestAccountValidationTombstoneCASAndInvariant(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	admin, rev, err := s.BootstrapAdmin(ctx, "rootadmin", "Administrator", []byte("unique-admin-password-41"))
	if err != nil { t.Fatal(err) }
	for _, login := range []string{"ab", "bad space", ".admin", "_admin", "-admin", "İadmin", "x" + strings.Repeat("a", 64)} {
		if _, _, err = s.CreateAccount(ctx, CreateAccountInput{Login:login, DisplayName:"bad", Kind:Human, Enabled:true}, rev); err == nil { t.Fatalf("accepted login %q", login) }
	}
	service, rev, err := s.CreateAccount(ctx, CreateAccountInput{Login:"service1", DisplayName:"Service", Kind:Service, Enabled:true}, rev)
	if err != nil { t.Fatal(err) }
	if _, err = s.SetPassword(ctx, service.ID, rev, []byte("unique-service-password-41")); err == nil { t.Fatal("service account accepted password") }
	human, rev, err := s.CreateAccount(ctx, CreateAccountInput{Login:"operator1",DisplayName:"Operator",Kind:Human,Enabled:true},rev)
	if err != nil { t.Fatal(err) }
	if rev, err = s.SetPassword(ctx,human.ID,rev,[]byte("unique-account-secret-2026")); err != nil { t.Fatal(err) }
	serviceKind := Service
	if _, _, err = s.UpdateAccount(ctx,human.ID,rev,UpdateAccountInput{Kind:&serviceKind}); !errors.Is(err,ErrInvalid) { t.Fatalf("human with password changed to service: %v",err) }
	if err = s.DeleteAccount(ctx, admin.ID, rev); !errors.Is(err, ErrInvariant) { t.Fatalf("deleting last administrator: %v", err) }
	if _, _, err = s.CreateAccount(ctx, CreateAccountInput{Login:"another1", DisplayName:"Operator", Kind:Human, Enabled:true}, rev-1); !errors.Is(err, ErrConflict) { t.Fatalf("stale revision: %v", err) }
	if err = s.DeleteAccount(ctx, human.ID, rev); err != nil { t.Fatal(err) }
	reused, nextRev, err := s.CreateAccount(ctx, CreateAccountInput{Login:"operator1", DisplayName:"Reused", Kind:Human, Enabled:true}, rev+1)
	if err != nil { t.Fatalf("explicit login reuse: %v", err) }
	if reused.ID == human.ID || nextRev != rev+2 { t.Fatalf("login reuse rebound account id: old=%s new=%s", human.ID, reused.ID) }
	if ok, err := s.VerifyPassword(ctx,reused.ID,[]byte("unique-account-secret-2026")); err != nil || ok { t.Fatalf("reused login revived credential: ok=%v err=%v",ok,err) }
	if err = s.DisableAccount(ctx, admin.ID, nextRev); !errors.Is(err, ErrInvariant) { t.Fatalf("disabling last administrator: %v", err) }
}
func TestLoginCanonicalization(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	_, revision, err := s.BootstrapAdmin(ctx, "rootadmin", "Administrator", []byte("unique-admin-password-41"))
	if err != nil { t.Fatal(err) }
	account, _, err := s.CreateAccount(ctx, CreateAccountInput{Login:"OPERATOR1",DisplayName:"Operator",Kind:Human,Enabled:true},revision)
	if err != nil || account.Login != "operator1" { t.Fatalf("login=%q err=%v",account.Login,err) }
}
func TestAccountLimitAtDatabaseBoundary(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	_, revision, err := s.BootstrapAdmin(ctx, "rootadmin", "Administrator", []byte("unique-admin-password-41"))
	if err != nil { t.Fatal(err) }
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { t.Fatal(err) }
	for i := 1; i < MaxAccounts; i++ {
		_, err = tx.ExecContext(ctx, "INSERT INTO accounts(id,login,display_name,kind,enabled,created_at,updated_at) VALUES(?,?,?,'service',1,1,1)", fmt.Sprintf("fixture-%d",i),fmt.Sprintf("user%05d",i),"Fixture")
		if err != nil { tx.Rollback(); t.Fatal(err) }
	}
	if err = tx.Commit(); err != nil { t.Fatal(err) }
	if _, _, err = s.CreateAccount(ctx, CreateAccountInput{Login:"lastone",DisplayName:"Last",Kind:Service,Enabled:true},revision); !errors.Is(err,ErrAccountLimit) { t.Fatalf("limit result: %v",err) }
}

func TestStoreExposesReusableAdministratorInvariant(t *testing.T) {
	s, _ := openTestStore(t)
	if err := s.ValidateAdministratorInvariant(context.Background()); !errors.Is(err,ErrInvariant) { t.Fatalf("empty store invariant: %v",err) }
}
func TestPasswordMutationEpochAndTransactionalRevocationHooks(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	_, rev, err := s.BootstrapAdmin(ctx, "rootadmin", "Administrator", []byte("unique-admin-password-41"))
	if err != nil { t.Fatal(err) }
	user, rev, err := s.CreateAccount(ctx, CreateAccountInput{Login:"operator1",DisplayName:"Operator",Kind:Human,Enabled:true},rev)
	if err != nil { t.Fatal(err) }
	if _, err = s.db.ExecContext(ctx, "INSERT INTO sessions(session_hash,csrf_hash,account_id,auth_epoch,created_at,last_seen_at,expires_at) VALUES(?,?,?,?,?,?,?)",make([]byte,32),make([]byte,32),user.ID,0,1,1,9999999999); err != nil { t.Fatal(err) }
	for _, revoke := range []bool{true,false} {
		selector := make([]byte,16)
		selector[0] = byte(len(selector))+byte(boolToInt(revoke))
		revokeInt := boolToInt(revoke)
		if _, err = s.db.ExecContext(ctx, "INSERT INTO api_tokens(selector,account_id,secret_hash,name,scopes,created_at,revoke_on_password_change) VALUES(?,?,?,?,?,?,?)",selector,user.ID,make([]byte,32),"test","[]",1,revokeInt); err != nil { t.Fatal(err) }
	}
	rev, err = s.SetPassword(ctx,user.ID,rev,[]byte("changed-passphrase-2026"))
	if err != nil { t.Fatal(err) }
	var sessions, revoked, active, epoch int
	if err = s.db.QueryRowContext(ctx,"SELECT count(*) FROM sessions WHERE account_id=?",user.ID).Scan(&sessions); err != nil { t.Fatal(err) }
	if err = s.db.QueryRowContext(ctx,"SELECT count(*) FROM api_tokens WHERE account_id=? AND revoked_at IS NOT NULL AND revoke_on_password_change=1",user.ID).Scan(&revoked); err != nil { t.Fatal(err) }
	if err = s.db.QueryRowContext(ctx,"SELECT count(*) FROM api_tokens WHERE account_id=? AND revoked_at IS NULL",user.ID).Scan(&active); err != nil { t.Fatal(err) }
	if err = s.db.QueryRowContext(ctx,"SELECT value FROM metadata WHERE key=?", "password_epoch:"+user.ID).Scan(&epoch); err != nil { t.Fatal(err) }
	if sessions != 0 || revoked != 1 || active != 1 || epoch != 1 { t.Fatalf("sessions=%d revoked=%d active=%d epoch=%d revision=%d",sessions,revoked,active,epoch,rev) }
	if ok, err := s.VerifyPassword(ctx,user.ID,[]byte("changed-passphrase-2026")); err != nil || !ok { t.Fatalf("new password verify=%v err=%v",ok,err) }
}

func boolToInt(value bool) int {
	if value { return 1 }
	return 0
}


func TestPasswordPolicyAndHashing(t *testing.T) {
	for _, password := range [][]byte{[]byte("short"), []byte(strings.Repeat("a",129)), []byte(strings.Repeat("é",257)), []byte(strings.Repeat("\xff",15)), []byte("nul\x00password-is-long-44"), []byte("password123456789")} {
		if err := validatePassword(password, "operator1", "Operator"); err == nil { t.Fatalf("accepted password %q", password) }
	}
	if err := validatePassword([]byte("password-contains-operator1-2026"), "operator1", "Someone"); err == nil { t.Fatal("accepted login in password") }
	if err := validatePassword([]byte("password-contains-alice-2026"), "operator1", "Alice"); err == nil { t.Fatal("accepted display name in password") }
	if err := validatePassword([]byte(strings.Repeat("🙂",128)), "operator1", "Operator"); err != nil { t.Fatalf("rejected 128 code points / 512 bytes: %v", err) }
	if err := validatePassword([]byte(" password-is-trimmed-44 "), "operator1", "Operator"); err != nil { t.Fatalf("whitespace should be preserved: %v", err) }
	if err := validatePassword([]byte("sufficiently-unique-passphrase-82"), "operator1", "Operator"); err != nil { t.Fatal(err) }
	encoded, err := hashPasswordWithPolicy(context.Background(), []byte("sufficiently-unique-passphrase-82"), "operator1", "Operator")
	if err != nil { t.Fatal(err) }
	ok, err := verifyPasswordHash(context.Background(), encoded, []byte("sufficiently-unique-passphrase-82"))
	if err != nil || !ok { t.Fatalf("verify=%v err=%v", ok, err) }
	ok, err = verifyPasswordHash(context.Background(), encoded, []byte("another-unique-passphrase-90"))
	if err != nil || ok { t.Fatalf("wrong password verify=%v err=%v", ok, err) }
	if !strings.HasPrefix(string(encoded), "$argon2id$v=19$m=65536,t=3,p=1$") { t.Fatalf("unexpected Argon2 parameters: %q", encoded[:min(len(encoded), 40)]) }
	if _, err = verifyPasswordHash(context.Background(), []byte("$argon2id$v=19$m=999999999,t=99,p=99$bad$bad"), []byte("candidate-password-123")); err == nil { t.Fatal("accepted corrupt high-cost hash") }
}
func TestSuccessfulVerificationRehashesLowCostCredentialByCAS(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	admin, rev, err := s.BootstrapAdmin(ctx,"rootadmin","Administrator",[]byte("unique-admin-password-41"))
	if err != nil { t.Fatal(err) }
	salt := []byte("0123456789abcdef")
	digest := argon2.IDKey([]byte("unique-admin-password-41"),salt,1,8192,1,32)
	if _, err = s.db.ExecContext(ctx,"UPDATE password_credentials SET salt=?,hash=?,memory_kib=8192,time_cost=1,parallelism=1 WHERE account_id=?",salt,digest,admin.ID); err != nil { t.Fatal(err) }
	if ok, err := s.VerifyPassword(ctx,admin.ID,[]byte("unique-admin-password-41")); err != nil || !ok { t.Fatalf("verify=%v err=%v",ok,err) }
	var memory, timeCost, parallel int
	if err = s.db.QueryRowContext(ctx,"SELECT memory_kib,time_cost,parallelism FROM password_credentials WHERE account_id=?",admin.ID).Scan(&memory,&timeCost,&parallel); err != nil { t.Fatal(err) }
	next, err := s.Revision(ctx)
	if err != nil { t.Fatal(err) }
	if memory != argonMemory || timeCost != argonTime || parallel != argonThreads || next != rev+1 { t.Fatalf("rehash costs=%d/%d/%d revision=%d",memory,timeCost,parallel,next) }
}

func waitForQueued(t *testing.T, g *hashAdmission, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		g.mu.Lock()
		waiting := g.queued
		g.mu.Unlock()
		if waiting == count { return }
		if time.Now().After(deadline) { t.Fatal("admission waiter was not enqueued") }
		runtime.Gosched()
	}
}

func TestAdmissionRejectsExcessQueueAndHonorsCancellation(t *testing.T) {
	g := newHashAdmission(2, 1)
	releases := make([]func(), 2)
	for i := range releases {
		releases[i], _ = g.acquire(context.Background())
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := g.acquire(ctx); result <- err }()
	waitForQueued(t, g, 1)
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) { t.Fatalf("cancellation: %v", err) }
	for _, release := range releases { release() }

	g = newHashAdmission(2, 1)
	for i := range releases { releases[i], _ = g.acquire(context.Background()) }
	waitResult := make(chan func(), 1)
	go func() { release, err := g.acquire(context.Background()); if err == nil { waitResult <- release } }()
	waitForQueued(t, g, 1)
	if _, err := g.acquire(context.Background()); !errors.Is(err, ErrHashOverloaded) { t.Fatalf("full queue: %v", err) }
	for _, release := range releases { release() }
	(<-waitResult)()
	g = newHashAdmission(1, 1)
	g.wait = time.Millisecond
	release, err := g.acquire(context.Background())
	if err != nil { t.Fatal(err) }
	if _, err = g.acquire(context.Background()); !errors.Is(err, ErrHashTimeout) { t.Fatalf("timeout: %v", err) }
	release()
}
