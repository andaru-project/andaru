package identity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
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
	if err != nil {
		t.Fatal(err)
	}
	if admin.Kind != Human || !admin.Enabled || rev != 1 {
		t.Fatalf("admin=%+v rev=%d", admin, rev)
	}
	if ok, err := s.VerifyPassword(ctx, admin.ID, []byte("unique-admin-password-41")); err != nil || !ok {
		t.Fatalf("verify=%v err=%v", ok, err)
	}
	user, rev, err := s.CreateAccount(ctx, CreateAccountInput{Login: "operator1", DisplayName: "Operator", Kind: Human, Enabled: true}, rev)
	if err != nil {
		t.Fatal(err)
	}
	display := "Operator Two"
	updated, rev, err := s.UpdateAccount(ctx, user.ID, rev, UpdateAccountInput{DisplayName: &display})
	if err != nil || updated.DisplayName != "Operator Two" {
		t.Fatalf("updated=%+v rev=%d err=%v", updated, rev, err)
	}
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
	page, err := s.ListAccounts(ctx, "", 1)
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("first account page=%+v err=%v", page, err)
	}
	lastPage, err := s.ListAccounts(ctx, page.NextCursor, 1)
	if err != nil || len(lastPage.Items) != 1 || lastPage.NextCursor != "" {
		t.Fatalf("last account page=%+v err=%v", lastPage, err)
	}
	if len(lastPage.Items[0].RoleIDs) != 1 || lastPage.Items[0].RoleIDs[0] != "administrator" {
		t.Fatalf("paged admin roles=%v", lastPage.Items[0].RoleIDs)
	}
	emptyPage, err := s.ListAccounts(ctx, lastPage.Items[0].Login, 1)
	if err != nil || len(emptyPage.Items) != 0 || emptyPage.NextCursor != "" {
		t.Fatalf("exhausted page=%+v err=%v", emptyPage, err)
	}
	if _, err = s.ListAccounts(ctx, "", 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero page size: %v", err)
	}
	if _, err = s.ListAccounts(ctx, "", MaxAccountPageSize+1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized page: %v", err)
	}
}
func TestOptionalDisplayAndCustomRolesSurviveRestart(t *testing.T) {
	ctx := context.Background()
	s, stateDir := openTestStore(t)
	admin, revision, err := s.BootstrapAdmin(ctx, "rootadmin", "", []byte("unique-admin-password-41"))
	if err != nil {
		t.Fatal(err)
	}
	roleA := "rol_12345678-1234-4234-8234-123456789abc"
	roleB := "rol_22345678-1234-4234-8234-123456789abc"
	if _, _, err = s.CreateAccount(ctx, CreateAccountInput{Login: "invalidrole", DisplayName: "", Kind: Human, RoleIDs: []string{"not-a-role"}}, revision); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid role id: %v", err)
	}
	user, revision, err := s.CreateAccount(ctx, CreateAccountInput{Login: "roleuser", DisplayName: "", Kind: Human, Enabled: true, RoleIDs: []string{roleA}}, revision)
	if err != nil || user.DisplayName != "" || len(user.RoleIDs) != 1 || user.RoleIDs[0] != roleA {
		t.Fatalf("create account=%+v err=%v", user, err)
	}
	sessionHash := make([]byte, 32)
	sessionHash[0] = 1
	sessionSelector := make([]byte, 32)
	sessionSelector[0] = 2
	csrfSelector := make([]byte, 32)
	csrfSelector[0] = 3
	if _, err = s.db.ExecContext(ctx, "INSERT INTO sessions(session_selector,session_hash,csrf_selector,csrf_hash,account_id,auth_epoch,account_epoch,password_epoch,created_at,last_seen_at,expires_at) VALUES(?,?,?,?,?,?,0,0,?,?,?)", sessionSelector, sessionHash, csrfSelector, make([]byte, 32), user.ID, 0, 1, 1, 9999999999); err != nil {
		t.Fatal(err)
	}
	roleIDs := []string{roleB}
	user, revision, err = s.UpdateAccount(ctx, user.ID, revision, UpdateAccountInput{RoleIDs: &roleIDs})
	if err != nil || len(user.RoleIDs) != 1 || user.RoleIDs[0] != roleB {
		t.Fatalf("updated roles=%v err=%v", user.RoleIDs, err)
	}
	var sessions int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM sessions WHERE account_id=?", user.ID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("role assignment retained %d sessions", sessions)
	}
	removeAdminRole := []string{roleA}
	if _, _, err = s.UpdateAccount(ctx, admin.ID, revision, UpdateAccountInput{RoleIDs: &removeAdminRole}); !errors.Is(err, ErrInvariant) {
		t.Fatalf("custom role replaced recovery administrator: %v", err)
	}
	if err = s.ValidateAdministratorInvariant(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.GetAccount(ctx, user.ID)
	if err != nil || got.DisplayName != "" || len(got.RoleIDs) != 1 || got.RoleIDs[0] != roleB {
		t.Fatalf("restarted account=%+v err=%v", got, err)
	}
	gotAdmin, err := s.GetAccount(ctx, admin.ID)
	if err != nil || len(gotAdmin.RoleIDs) != 1 || gotAdmin.RoleIDs[0] != "administrator" {
		t.Fatalf("admin roles=%v err=%v", gotAdmin.RoleIDs, err)
	}
}

func TestAccountValidationTombstoneCASAndInvariant(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	admin, rev, err := s.BootstrapAdmin(ctx, "rootadmin", "Administrator", []byte("unique-admin-password-41"))
	if err != nil {
		t.Fatal(err)
	}
	for _, login := range []string{"ab", "bad space", ".admin", "_admin", "-admin", "İadmin", "x" + strings.Repeat("a", 64)} {
		if _, _, err = s.CreateAccount(ctx, CreateAccountInput{Login: login, DisplayName: "bad", Kind: Human, Enabled: true}, rev); err == nil {
			t.Fatalf("accepted login %q", login)
		}
	}
	service, rev, err := s.CreateAccount(ctx, CreateAccountInput{Login: "service1", DisplayName: "Service", Kind: Service, Enabled: true}, rev)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetPassword(ctx, service.ID, rev, []byte("unique-service-password-41")); err == nil {
		t.Fatal("service account accepted password")
	}
	human, rev, err := s.CreateAccount(ctx, CreateAccountInput{Login: "operator1", DisplayName: "Operator", Kind: Human, Enabled: true}, rev)
	if err != nil {
		t.Fatal(err)
	}
	if rev, err = s.SetPassword(ctx, human.ID, rev, []byte("unique-account-secret-2026")); err != nil {
		t.Fatal(err)
	}
	serviceKind := Service
	if _, _, err = s.UpdateAccount(ctx, human.ID, rev, UpdateAccountInput{Kind: &serviceKind}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("human with password changed to service: %v", err)
	}
	if err = s.DeleteAccount(ctx, admin.ID, rev); !errors.Is(err, ErrInvariant) {
		t.Fatalf("deleting last administrator: %v", err)
	}
	if _, _, err = s.CreateAccount(ctx, CreateAccountInput{Login: "another1", DisplayName: "Operator", Kind: Human, Enabled: true}, rev-1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	if err = s.DeleteAccount(ctx, human.ID, rev); err != nil {
		t.Fatal(err)
	}
	reused, nextRev, err := s.CreateAccount(ctx, CreateAccountInput{Login: "operator1", DisplayName: "Reused", Kind: Human, Enabled: true}, rev+1)
	if err != nil {
		t.Fatalf("explicit login reuse: %v", err)
	}
	if reused.ID == human.ID || nextRev != rev+2 {
		t.Fatalf("login reuse rebound account id: old=%s new=%s", human.ID, reused.ID)
	}
	if ok, err := s.VerifyPassword(ctx, reused.ID, []byte("unique-account-secret-2026")); err != nil || ok {
		t.Fatalf("reused login revived credential: ok=%v err=%v", ok, err)
	}
	if err = s.DisableAccount(ctx, admin.ID, nextRev); !errors.Is(err, ErrInvariant) {
		t.Fatalf("disabling last administrator: %v", err)
	}
}
func TestLoginCanonicalization(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	_, revision, err := s.BootstrapAdmin(ctx, "rootadmin", "Administrator", []byte("unique-admin-password-41"))
	if err != nil {
		t.Fatal(err)
	}
	account, _, err := s.CreateAccount(ctx, CreateAccountInput{Login: "OPERATOR1", DisplayName: "Operator", Kind: Human, Enabled: true}, revision)
	if err != nil || account.Login != "operator1" {
		t.Fatalf("login=%q err=%v", account.Login, err)
	}
}
func TestAccountLimitAtDatabaseBoundary(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	_, revision, err := s.BootstrapAdmin(ctx, "rootadmin", "Administrator", []byte("unique-admin-password-41"))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < MaxAccounts; i++ {
		_, err = tx.ExecContext(ctx, "INSERT INTO accounts(id,login,display_name,kind,enabled,created_at,updated_at) VALUES(?,?,?,'service',1,1,1)", fmt.Sprintf("fixture-%d", i), fmt.Sprintf("user%05d", i), "Fixture")
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.CreateAccount(ctx, CreateAccountInput{Login: "lastone", DisplayName: "Last", Kind: Service, Enabled: true}, revision); !errors.Is(err, ErrAccountLimit) {
		t.Fatalf("limit result: %v", err)
	}
}

func TestAdmissionConcurrentWakeupsDoNotDeadlock(t *testing.T) {
	g := newHashAdmission(2, 32)
	for round := 0; round < 40; round++ {
		held := make([]func(), 2)
		for i := range held {
			var err error
			held[i], err = g.acquire(context.Background())
			if err != nil {
				t.Fatal(err)
			}
		}
		done := make(chan error, 32)
		start := make(chan struct{})
		for i := 0; i < 16; i++ {
			go func() {
				release, err := g.acquire(context.Background())
				if err == nil {
					release()
				}
				done <- err
			}()
		}
		waitForQueued(t, g, 16)
		for i := 0; i < 16; i++ {
			go func() {
				<-start
				release, err := g.acquire(context.Background())
				if err == nil {
					release()
				}
				done <- err
			}()
		}
		close(start)
		for _, release := range held {
			release()
		}
		deadline := time.After(time.Second)
		for i := 0; i < 32; i++ {
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("round %d admission: %v", round, err)
				}
			case <-deadline:
				t.Fatalf("round %d: queued/new callers deadlocked", round)
			}
		}
	}
}

func BenchmarkArgon2ProductionAdmission(b *testing.B) {
	password := []byte("fixed-policy-qualification-passphrase-2026")
	var peakActive atomic.Int64
	var elapsed atomic.Int64
	var next atomic.Int64
	const workers = 8
	b.ResetTimer()
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for {
				if next.Add(1) > int64(b.N) {
					return
				}
				started := time.Now()
				release, err := passwordAdmission.acquire(context.Background())
				if err != nil {
					b.Error(err)
					return
				}
				active := int64(len(passwordAdmission.permits))
				for old := peakActive.Load(); active > old && !peakActive.CompareAndSwap(old, active); old = peakActive.Load() {
				}
				_, err = derivePasswordHash(password)
				release()
				elapsed.Add(time.Since(started).Nanoseconds())
				if err != nil {
					b.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	b.StopTimer()
	b.ReportMetric(float64(elapsed.Load())/float64(b.N), "latency-ns/op")
	b.ReportMetric(float64(peakActive.Load()*argonMemory), "KiB-peak-estimate")
	b.ReportMetric(float64(peakActive.Load()), "max-active")
}
func TestArgon2ProductionPolicyQualification(t *testing.T) {
	if len(passwordAdmission.permits) != 0 {
		t.Fatal("password admission gate is not idle")
	}
	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)

	ready := make(chan struct{}, 2)
	startHash := make(chan struct{})
	results := make(chan struct {
		elapsed time.Duration
		err     error
	}, 2)
	var active atomic.Int32
	var peakActive atomic.Int32
	password := []byte("qualification-fixed-policy-passphrase-2026")
	for range 2 {
		go func() {
			release, err := passwordAdmission.acquire(context.Background())
			if err != nil {
				results <- struct {
					elapsed time.Duration
					err     error
				}{err: err}
				return
			}
			current := active.Add(1)
			for old := peakActive.Load(); current > old && !peakActive.CompareAndSwap(old, current); old = peakActive.Load() {
			}
			ready <- struct{}{}
			<-startHash
			started := time.Now()
			_, err = derivePasswordHash(password)
			elapsed := time.Since(started)
			active.Add(-1)
			release()
			results <- struct {
				elapsed time.Duration
				err     error
			}{elapsed: elapsed, err: err}
		}()
	}
	for range 2 {
		select {
		case <-ready:
		case <-time.After(15 * time.Second):
			close(startHash)
			t.Fatal("timed out waiting for two admitted Argon workers")
		}
	}
	if len(passwordAdmission.permits) != 2 || peakActive.Load() != 2 {
		close(startHash)
		t.Fatalf("admitted=%d peak-active=%d; want exactly 2", len(passwordAdmission.permits), peakActive.Load())
	}

	var peakHeap uint64
	var sampledAtTwo bool
	sampleHeap := func() {
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		if active.Load() == 2 {
			sampledAtTwo = true
			if stats.HeapAlloc > peakHeap {
				peakHeap = stats.HeapAlloc
			}
		}
	}
	ticker := time.NewTicker(100 * time.Microsecond)
	defer ticker.Stop()
	timeout := time.NewTimer(15 * time.Second)
	defer timeout.Stop()
	started := time.Now()
	close(startHash)
	var totalLatency time.Duration
	for completed := 0; completed < 2; {
		sampleHeap()
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatal(result.err)
			}
			totalLatency += result.elapsed
			completed++
		case <-ticker.C:
		case <-timeout.C:
			t.Fatal("timed out qualifying production Argon2 hashes")
		}
	}
	sampleHeap()
	if !sampledAtTwo {
		t.Fatal("did not sample process heap while both Argon hashes were active")
	}
	peakDelta := peakHeap - baseline.HeapAlloc
	if peakActive.Load() != 2 {
		t.Fatalf("observed max active=%d, want 2", peakActive.Load())
	}
	if peakDelta < uint64(argonMemory)*1024 {
		t.Fatalf("measured heap delta=%d bytes, below one Argon2 workspace", peakDelta)
	}
	t.Logf("Argon2 v%d m=%dKiB t=%d p=%d: max-active=%d peak-heap-delta=%dMiB mean-hash-latency=%s wall=%s",
		argonVersion, argonMemory, argonTime, argonThreads, peakActive.Load(), peakDelta/(1024*1024), totalLatency/2, time.Since(started))
}

func TestStoreExposesReusableAdministratorInvariant(t *testing.T) {
	s, _ := openTestStore(t)
	if err := s.ValidateAdministratorInvariant(context.Background()); !errors.Is(err, ErrInvariant) {
		t.Fatalf("empty store invariant: %v", err)
	}
}
func TestPasswordMutationEpochAndTransactionalRevocationHooks(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	_, rev, err := s.BootstrapAdmin(ctx, "rootadmin", "Administrator", []byte("unique-admin-password-41"))
	if err != nil {
		t.Fatal(err)
	}
	user, rev, err := s.CreateAccount(ctx, CreateAccountInput{Login: "operator1", DisplayName: "Operator", Kind: Human, Enabled: true}, rev)
	if err != nil {
		t.Fatal(err)
	}
	sessionSelector := make([]byte, 32)
	sessionSelector[0] = 3
	csrfSelector := make([]byte, 32)
	csrfSelector[0] = 4
	if _, err = s.db.ExecContext(ctx, "INSERT INTO sessions(session_selector,session_hash,csrf_selector,csrf_hash,account_id,auth_epoch,account_epoch,password_epoch,created_at,last_seen_at,expires_at) VALUES(?,?,?,?,?,?,0,0,?,?,?)", sessionSelector, make([]byte, 32), csrfSelector, make([]byte, 32), user.ID, 0, 1, 1, 9999999999); err != nil {
		t.Fatal(err)
	}
	for _, revoke := range []bool{true, false} {
		selector := make([]byte, 16)
		selector[0] = byte(len(selector)) + byte(boolToInt(revoke))
		revokeInt := boolToInt(revoke)
		if _, err = s.db.ExecContext(ctx, "INSERT INTO api_tokens(selector,account_id,secret_hash,name,scopes,created_at,revoke_on_password_change,password_epoch,revision) VALUES(?,?,?,?,?,?,?,0,0)", selector, user.ID, make([]byte, 32), "test", "[]", 1, revokeInt); err != nil {
			t.Fatal(err)
		}
	}
	rev, err = s.SetPassword(ctx, user.ID, rev, []byte("changed-passphrase-2026"))
	if err != nil {
		t.Fatal(err)
	}
	var sessions, revoked, active, epoch int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM sessions WHERE account_id=?", user.ID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM api_tokens WHERE account_id=? AND revoked_at IS NOT NULL AND revoke_on_password_change=1", user.ID).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM api_tokens WHERE account_id=? AND revoked_at IS NULL", user.ID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key=?", "password_epoch:"+user.ID).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 || revoked != 1 || active != 1 || epoch != 1 {
		t.Fatalf("sessions=%d revoked=%d active=%d epoch=%d revision=%d", sessions, revoked, active, epoch, rev)
	}
	if ok, err := s.VerifyPassword(ctx, user.ID, []byte("changed-passphrase-2026")); err != nil || !ok {
		t.Fatalf("new password verify=%v err=%v", ok, err)
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestPasswordPolicyAndHashing(t *testing.T) {
	for _, password := range [][]byte{[]byte("short"), []byte(strings.Repeat("a", 129)), []byte(strings.Repeat("é", 257)), []byte(strings.Repeat("\xff", 15)), []byte("nul\x00password-is-long-44"), []byte("password123456789")} {
		if err := validatePassword(password, "operator1", "Operator"); err == nil {
			t.Fatalf("accepted password %q", password)
		}
	}
	if err := validatePassword([]byte("password-contains-operator1-2026"), "operator1", "Someone"); err == nil {
		t.Fatal("accepted login in password")
	}
	if err := validatePassword([]byte("password-contains-alice-2026"), "operator1", "Alice"); err == nil {
		t.Fatal("accepted display name in password")
	}
	if err := validatePassword([]byte(strings.Repeat("🙂", 128)), "operator1", "Operator"); err != nil {
		t.Fatalf("rejected 128 code points / 512 bytes: %v", err)
	}
	if err := validatePassword([]byte(" password-is-trimmed-44 "), "operator1", "Operator"); err != nil {
		t.Fatalf("whitespace should be preserved: %v", err)
	}
	if err := validatePassword([]byte("sufficiently-unique-passphrase-82"), "operator1", "Operator"); err != nil {
		t.Fatal(err)
	}
	encoded, err := hashPasswordWithPolicy(context.Background(), []byte("sufficiently-unique-passphrase-82"), "operator1", "Operator")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := verifyPasswordHash(context.Background(), encoded, []byte("sufficiently-unique-passphrase-82"))
	if err != nil || !ok {
		t.Fatalf("verify=%v err=%v", ok, err)
	}
	ok, err = verifyPasswordHash(context.Background(), encoded, []byte("another-unique-passphrase-90"))
	if err != nil || ok {
		t.Fatalf("wrong password verify=%v err=%v", ok, err)
	}
	if !strings.HasPrefix(string(encoded), "$argon2id$v=19$m=65536,t=3,p=1$") {
		t.Fatalf("unexpected Argon2 parameters: %q", encoded[:min(len(encoded), 40)])
	}
	if _, err = verifyPasswordHash(context.Background(), []byte("$argon2id$v=19$m=999999999,t=99,p=99$bad$bad"), []byte("candidate-password-123")); err == nil {
		t.Fatal("accepted corrupt high-cost hash")
	}
}
func TestSuccessfulVerificationRehashesLowCostCredentialByCAS(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	admin, rev, err := s.BootstrapAdmin(ctx, "rootadmin", "Administrator", []byte("unique-admin-password-41"))
	if err != nil {
		t.Fatal(err)
	}
	salt := []byte("0123456789abcdef")
	digest := argon2.IDKey([]byte("unique-admin-password-41"), salt, 1, 8192, 1, 32)
	if _, err = s.db.ExecContext(ctx, "UPDATE password_credentials SET salt=?,hash=?,memory_kib=8192,time_cost=1,parallelism=1 WHERE account_id=?", salt, digest, admin.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.VerifyPassword(ctx, admin.ID, []byte("unique-admin-password-41")); err != nil || !ok {
		t.Fatalf("verify=%v err=%v", ok, err)
	}
	var memory, timeCost, parallel int
	if err = s.db.QueryRowContext(ctx, "SELECT memory_kib,time_cost,parallelism FROM password_credentials WHERE account_id=?", admin.ID).Scan(&memory, &timeCost, &parallel); err != nil {
		t.Fatal(err)
	}
	next, err := s.Revision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if memory != argonMemory || timeCost != argonTime || parallel != argonThreads || next != rev+1 {
		t.Fatalf("rehash costs=%d/%d/%d revision=%d", memory, timeCost, parallel, next)
	}
}

func waitForQueued(t *testing.T, g *hashAdmission, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		g.mu.Lock()
		waiting := g.queued
		g.mu.Unlock()
		if waiting == count {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("admission waiter was not enqueued")
		}
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
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	for _, release := range releases {
		release()
	}

	g = newHashAdmission(2, 1)
	for i := range releases {
		releases[i], _ = g.acquire(context.Background())
	}
	waitResult := make(chan func(), 1)
	go func() {
		release, err := g.acquire(context.Background())
		if err == nil {
			waitResult <- release
		}
	}()
	waitForQueued(t, g, 1)
	if _, err := g.acquire(context.Background()); !errors.Is(err, ErrHashOverloaded) {
		t.Fatalf("full queue: %v", err)
	}
	for _, release := range releases {
		release()
	}
	(<-waitResult)()
	g = newHashAdmission(1, 1)
	g.wait = time.Millisecond
	release, err := g.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.acquire(context.Background()); !errors.Is(err, ErrHashTimeout) {
		t.Fatalf("timeout: %v", err)
	}
	release()
}
