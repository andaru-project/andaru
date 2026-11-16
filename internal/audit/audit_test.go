package audit

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/authz"
)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("audit database tests require root")
	}
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

func testEvent(action Action, category Category, result Result) Event {
	return Event{
		Actor:    authz.Actor{Kind: authz.ActorSystem, ID: "root"},
		Source:   "unix",
		Action:   action,
		Target:   "usr_00000000-0000-4000-8000-000000000001",
		Result:   result,
		Category: category,
	}
}

func testIntent(id string, action Action, category Category) Intent {
	return Intent{
		OperationID: id,
		Actor:       authz.Actor{Kind: authz.ActorSystem, ID: "root"},
		Source:      "unix",
		Action:      action,
		Target:      "usr_00000000-0000-4000-8000-000000000001",
		RequestHash: strings.Repeat("a", 64),
		Category:    category,
	}
}

func TestRootOnlyLayoutAndExclusiveOpen(t *testing.T) {
	s, dir := openTestStore(t)
	for _, path := range []string{filepath.Join(dir, "audit.db"), filepath.Join(dir, "audit.lock")} {
		info, err := os.Stat(path)
		if err != nil || !privateFile(info) {
			t.Fatalf("unsafe audit member %s: %v", filepath.Base(path), err)
		}
	}
	if err := privateDirectory(dir); err != nil {
		t.Fatal(err)
	}
	var app, version int
	if err := s.db.QueryRow("PRAGMA application_id").Scan(&app); err != nil || app != applicationID {
		t.Fatalf("application ID=%x, err=%v", app, err)
	}
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != LayoutVersion {
		t.Fatalf("layout version=%d, err=%v", version, err)
	}
	var journal string
	var syncMode, foreignKeys int
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("PRAGMA synchronous").Scan(&syncMode); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if journal != "wal" || syncMode != 2 || foreignKeys != 1 {
		t.Fatalf("SQLite pragmas: journal=%s sync=%d foreign_keys=%d", journal, syncMode, foreignKeys)
	}
	if _, err := Open(context.Background(), dir); err == nil {
		t.Fatal("second writer opened the locked audit store")
	}
}

func TestMetadataAllowlistRejectsSecretsAndArbitraryValues(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	bad := testEvent(ActionLoginFailure, CategoryGeneral, ResultFailure)
	bad.Target = ""
	bad.Metadata = map[string]string{"password": "do-not-store-this"}
	if _, err := s.Append(ctx, bad); !errors.Is(err, ErrInvalid) || strings.Contains(fmt.Sprint(err), "do-not-store-this") {
		t.Fatalf("secret metadata error = %v", err)
	}

	bad.Metadata = map[string]string{MetadataReasonCode: "do-not-store-this"}
	if _, err := s.Append(ctx, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("arbitrary reason was accepted: %v", err)
	}

	bad.Metadata = map[string]string{MetadataReasonCode: ReasonInvalidCredentials}
	if _, err := s.Append(ctx, bad); err != nil {
		t.Fatalf("allowlisted metadata rejected: %v", err)
	}
	tokenEvent := testEvent(ActionTokenCreated, CategoryGeneral, ResultSuccess)
	tokenEvent.Actor = authz.Actor{Kind: authz.ActorToken, ID: strings.Repeat("a", 43)}
	if _, err := s.Append(ctx, tokenEvent); !errors.Is(err, ErrInvalid) {
		t.Fatalf("token secret was accepted as an actor selector: %v", err)
	}
	tokenEvent.Actor.ID = base64.RawURLEncoding.EncodeToString(make([]byte, 16))
	if _, err := s.Append(ctx, tokenEvent); err != nil {
		t.Fatalf("public token selector was rejected: %v", err)
	}
}

func TestIntentOutcomeIdempotencyAndReplay(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	intent := testIntent("request-1", ActionAccountUpdated, CategoryProtected)

	started, err := s.Begin(ctx, intent)
	if err != nil || !started.Started || started.Replay {
		t.Fatalf("Begin = %+v, %v", started, err)
	}
	events, err := s.ListEvents(ctx, 0, 10)
	if err != nil || len(events) != 1 || events[0].Result != ResultPending || events[0].OperationID != intent.OperationID {
		t.Fatalf("intent was not recorded before outcome: %+v, %v", events, err)
	}
	if _, err = s.Begin(ctx, intent); !errors.Is(err, ErrOperationInFlight) {
		t.Fatalf("duplicate in-flight operation error = %v", err)
	}
	if err = s.Finish(ctx, Outcome{OperationID: intent.OperationID, Result: ResultSuccess, Metadata: map[string]string{MetadataChanged: "true"}}); err != nil {
		t.Fatal(err)
	}

	replay, err := s.Begin(ctx, intent)
	if err != nil || !replay.Replay || replay.Outcome == nil || replay.Outcome.Result != ResultSuccess || replay.Outcome.Metadata[MetadataChanged] != "true" {
		t.Fatalf("committed replay = %+v, %v", replay, err)
	}
	changed := intent
	changed.RequestHash = strings.Repeat("b", 64)
	if _, err = s.Begin(ctx, changed); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("changed request hash error = %v", err)
	}
}

func TestLoginFailuresCoalesceByVerifiedSource(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	event := testEvent(ActionLoginFailure, CategoryGeneral, ResultFailure)
	event.Target = ""
	event.Source = "192.0.2.10"
	event.Metadata = map[string]string{MetadataReasonCode: ReasonInvalidCredentials}
	if _, err := s.Append(ctx, event); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, event); err != nil {
		t.Fatal(err)
	}
	event.Source = "192.0.2.11"
	if _, err := s.Append(ctx, event); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListEvents(ctx, 0, 100)
	if err != nil || len(got) != 2 {
		t.Fatalf("events = %d, %v", len(got), err)
	}
	if got[0].Count != 2 || got[1].Count != 1 || got[0].Source == got[1].Source {
		t.Fatalf("coalesced events = %+v", got)
	}
}
func TestCanceledBeginDoesNotLatchStorageFailure(t *testing.T) {
	s, _ := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := s.Begin(ctx, testIntent("cancelled-1", ActionAccountUpdated, CategoryProtected)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Begin error = %v", err)
	}
	if _, err := s.Begin(context.Background(), testIntent("healthy-1", ActionAccountUpdated, CategoryProtected)); err != nil {
		t.Fatalf("healthy Begin after cancellation = %v", err)
	}
}

func TestLoginFailuresCoalesceAcrossAllowedReasons(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	event := testEvent(ActionLoginFailure, CategoryGeneral, ResultFailure)
	event.Target = ""
	event.Source = "192.0.2.10"
	for _, reason := range []string{ReasonInvalidCredentials, ReasonAccountDisabled, ReasonRateLimited} {
		event.Metadata = map[string]string{MetadataReasonCode: reason}
		if _, err := s.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.ListEvents(ctx, 0, 100)
	if err != nil || len(got) != 1 || got[0].Count != 3 || got[0].Metadata[MetadataReasonCode] != ReasonInvalidCredentials {
		t.Fatalf("alternating reasons were not coalesced with bounded first-reason metadata: %+v, %v", got, err)
	}
}

func TestCoalescedAppendPrunesExpiredEvents(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	event := testEvent(ActionLoginFailure, CategoryGeneral, ResultFailure)
	event.Target = ""
	event.Source = "192.0.2.10"
	event.Metadata = map[string]string{MetadataReasonCode: ReasonInvalidCredentials}
	if _, err := s.Append(ctx, event); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().Add(-retentionPeriod - time.Second).UnixMilli()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO events(at,actor_kind,actor_id,source,action,target,result,operation_id,category,coalesced_count)
		VALUES(?,?,?,?,?,?,?,?,?,1)`, cutoff, authz.ActorSystem, "root", "unix", ActionLoginSuccess, "", ResultSuccess, "", CategoryGeneral); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, event); err != nil {
		t.Fatal(err)
	}
	var expired int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM events WHERE at=?", cutoff).Scan(&expired); err != nil || expired != 0 {
		t.Fatalf("expired event count = %d, err=%v", expired, err)
	}
}



func TestCategoryBudgetsReserveProtectedRevocation(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	seedEvents(t, s, CategoryGeneral, generalBudget)

	if _, err := s.Append(ctx, testEvent(ActionLoginSuccess, CategoryGeneral, ResultSuccess)); !errors.Is(err, ErrGeneralSaturated) {
		t.Fatalf("general saturation error = %v", err)
	}
	health, err := s.Health(ctx)
	if err != nil || !health.GeneralSaturated || health.ProtectedSaturated {
		t.Fatalf("health = %+v, %v", health, err)
	}
	events, err := s.ListEvents(ctx, generalBudget, 10)
	if err != nil || len(events) != 1 || events[0].Action != ActionAuditHealth || events[0].Metadata[MetadataReasonCode] != ReasonGeneralSaturated {
		t.Fatalf("audit health event missing: %+v, %v", events, err)
	}
	if _, err = s.Append(ctx, testEvent(ActionTokenRevoked, CategoryProtected, ResultSuccess)); err != nil {
		t.Fatalf("protected revocation blocked by full general pool: %v", err)
	}
	intent := testIntent("revoke-1", ActionTokenRevoked, CategoryProtected)
	if _, err = s.Begin(ctx, intent); err != nil {
		t.Fatalf("protected operation blocked by full general pool: %v", err)
	}
}

func TestProtectedBudgetRefusesFreshProtectedEvents(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	seedEvents(t, s, CategoryProtected, protectedBudget)
	if _, err := s.Append(ctx, testEvent(ActionTokenRevoked, CategoryProtected, ResultSuccess)); !errors.Is(err, ErrProtectedSaturated) {
		t.Fatalf("protected saturation error = %v", err)
	}
	health, err := s.Health(ctx)
	if err != nil || !health.ProtectedSaturated {
		t.Fatalf("health = %+v, %v", health, err)
	}
}

func TestExpiredEventsPruneWithWatermark(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	expired, err := s.Append(ctx, testEvent(ActionLoginSuccess, CategoryGeneral, ResultSuccess))
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-366 * 24 * time.Hour).UnixMilli()
	if _, err = s.db.ExecContext(ctx, "UPDATE events SET at=? WHERE id=?", old, expired.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Append(ctx, testEvent(ActionLoginSuccess, CategoryGeneral, ResultSuccess)); err != nil {
		t.Fatal(err)
	}
	status, err := s.Retention(ctx)
	if err != nil || status.Watermark != expired.ID || status.LastPruned.ExpiredGeneral != 1 {
		t.Fatalf("retention = %+v, %v", status, err)
	}
	got, err := s.ListEvents(ctx, 0, 100)
	if err != nil || len(got) != 1 || got[0].ID <= status.Watermark {
		t.Fatalf("retained events = %+v, %v", got, err)
	}
	report, err := s.Verify(ctx)
	if err != nil || !report.Valid {
		t.Fatalf("integrity report after authorized pruning = %+v, %v", report, err)
	}
}

func TestVerifyReportsUnexplainedEventGaps(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	for range 3 {
		if _, err := s.Append(ctx, testEvent(ActionLoginSuccess, CategoryGeneral, ResultSuccess)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM events WHERE id=2"); err != nil {
		t.Fatal(err)
	}
	report, err := s.Verify(ctx)
	if err != nil || report.Valid || len(report.Gaps) != 1 || report.Gaps[0] != (IDGap{After: 1, Before: 3}) {
		t.Fatalf("integrity report = %+v, %v", report, err)
	}
	health, err := s.Health(ctx)
	if err != nil || !health.IntegrityFailure {
		t.Fatalf("health = %+v, %v", health, err)
	}
}

func TestVerifyConcurrentAppendUsesOneSnapshot(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	const appends = 40
	start := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		<-start
		for range appends {
			if _, err := s.Append(ctx, testEvent(ActionLoginSuccess, CategoryGeneral, ResultSuccess)); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	close(start)
	for range appends {
		report, err := s.Verify(ctx)
		if err != nil || !report.Valid {
			t.Fatalf("integrity report during concurrent append = %+v, %v", report, err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestVerifyDetectsRetainedProtectedDeletionAfterMixedAgePruning(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	protected, err := s.Append(ctx, testEvent(ActionTokenRevoked, CategoryProtected, ResultSuccess))
	if err != nil {
		t.Fatal(err)
	}
	expired, err := s.Append(ctx, testEvent(ActionLoginSuccess, CategoryGeneral, ResultSuccess))
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-366 * 24 * time.Hour).UnixMilli()
	if _, err = s.db.ExecContext(ctx, "UPDATE events SET at=? WHERE id=?", old, expired.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Append(ctx, testEvent(ActionLoginSuccess, CategoryGeneral, ResultSuccess)); err != nil {
		t.Fatal(err)
	}
	status, err := s.Retention(ctx)
	if err != nil || status.Watermark != expired.ID || status.Watermark < protected.ID {
		t.Fatalf("retention = %+v, %v", status, err)
	}
	if _, err = s.db.ExecContext(ctx, "DELETE FROM events WHERE id=?", protected.ID); err != nil {
		t.Fatal(err)
	}
	report, err := s.Verify(ctx)
	if err != nil || report.Valid || report.InvalidEvents == 0 {
		t.Fatalf("retained protected deletion was not reported: %+v, %v", report, err)
	}
}

func TestRestartReconciliationReplaysAuthoritativeCommit(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "state")
	if os.Geteuid() != 0 {
		t.Skip("audit database tests require root")
	}
	intent := testIntent("request-reconcile", ActionAccountUpdated, CategoryProtected)
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Begin(ctx, intent); err != nil {
		t.Fatal(err)
	}
	_ = s.Close() // The process stopped after the authoritative mutation committed.

	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	outcome := Outcome{OperationID: intent.OperationID, Result: ResultSuccess, Metadata: map[string]string{MetadataChanged: "true"}}
	source := staticOperationSource{record: OperationRecord{
		OperationID: intent.OperationID,
		RequestHash: intent.RequestHash,
		Actor:       intent.Actor,
		Source:      intent.Source,
		Action:      intent.Action,
		Target:      intent.Target,
		Category:    intent.Category,
		Outcome:     outcome,
	}}

	if _, err = s.Append(ctx, testEvent(ActionLoginSuccess, CategoryGeneral, ResultSuccess)); !errors.Is(err, ErrReconciliationRequired) {
		t.Fatalf("mutation before startup reconciliation = %v", err)
	}
	if err = s.Reconcile(ctx, source); err != nil {
		t.Fatal(err)
	}
	replay, err := s.Begin(ctx, intent)
	if err != nil || !replay.Replay || replay.Outcome == nil || replay.Outcome.Result != ResultSuccess {
		t.Fatalf("restart replay = %+v, %v", replay, err)
	}
	_ = s.Close()
}

func TestAuthoritativeAbsenceAllowsSameRequestRetry(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "state")
	if os.Geteuid() != 0 {
		t.Skip("audit database tests require root")
	}
	intent := testIntent("request-retry", ActionAccountUpdated, CategoryProtected)
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Begin(ctx, intent); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Reconcile(ctx, emptyOperationSource{}); err != nil {
		t.Fatal(err)
	}
	retry, err := s.Begin(ctx, intent)
	if err != nil || !retry.Started || retry.Replay {
		t.Fatalf("retry after authoritative absence = %+v, %v", retry, err)
	}
	_ = s.Close()
}

func TestReconcileCannotMarkPausedLiveMutationNotCommitted(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	intent := testIntent("request-paused", ActionAccountUpdated, CategoryProtected)
	if _, err := s.Begin(ctx, intent); err != nil {
		t.Fatal(err)
	}
	paused := make(chan struct{})
	resume := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		close(paused)
		<-resume
		finished <- s.Finish(ctx, Outcome{OperationID: intent.OperationID, Result: ResultSuccess})
	}()
	<-paused
	if err := s.Reconcile(ctx, emptyOperationSource{}); !errors.Is(err, ErrReconciliationComplete) {
		t.Fatalf("reconciliation during a live operation = %v", err)
	}
	var state string
	if err := s.db.QueryRowContext(ctx, "SELECT state FROM operations WHERE operation_id=?", intent.OperationID).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("paused operation state = %q, %v", state, err)
	}
	close(resume)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestEmergencyReceiptCanBeWrittenWithoutDatabase(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("audit receipt tests require root")
	}
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := WriteEmergencyReceipt(dir, testEvent(ActionRecoveryCompleted, CategoryProtected, ResultRecovered)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, emergencyReceiptName))
	if err != nil || !privateFile(info) {
		t.Fatalf("emergency receipt is missing or unsafe: %v", err)
	}
	if _, err = os.Lstat(filepath.Join(dir, "audit.db")); !os.IsNotExist(err) {
		t.Fatalf("emergency receipt created audit.db: %v", err)
	}
}

func TestReadOnlyStorageBlocksIntentButSafetyReceiptIsReportable(t *testing.T) {
	s, dir := openTestStore(t)
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(ctx, testIntent("readonly-1", ActionAccountUpdated, CategoryProtected)); !errors.Is(err, ErrStorage) {
		t.Fatalf("sensitive intent was not blocked: %v", err)
	}

	receiptErr := s.AppendSafety(ctx, testEvent(ActionRecoveryCompleted, CategoryProtected, ResultRecovered))
	if !errors.Is(receiptErr, ErrStorage) {
		t.Fatalf("safety storage failure was not reported: %v", receiptErr)
	}
	info, err := os.Stat(filepath.Join(dir, emergencyReceiptName))
	if err != nil || !privateFile(info) {
		t.Fatalf("best-effort receipt is missing or unsafe: %v", err)
	}
}

func TestSQLiteFullBlocksIntentAndSafetyRemainsBestEffort(t *testing.T) {
	s, dir := openTestStore(t)
	ctx := context.Background()
	var pages int
	if err := s.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA max_page_count=%d", pages+1)); err != nil {
		t.Fatal(err)
	}

	reachedFull := false
	for i := range 1000 {
		_, err := s.Begin(ctx, testIntent(fmt.Sprintf("full-%d", i), ActionAccountUpdated, CategoryProtected))
		if errors.Is(err, ErrStorage) {
			reachedFull = true
			break
		}
		if err != nil {
			t.Fatalf("sensitive intent failed before storage filled: %v", err)
		}
	}
	if !reachedFull {
		t.Fatal("bounded SQLite page limit did not make sensitive intent fail closed")
	}
	if err := s.AppendSafety(ctx, testEvent(ActionRecoveryCompleted, CategoryProtected, ResultRecovered)); !errors.Is(err, ErrStorage) {
		t.Fatalf("full-storage safety failure was not reportable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, emergencyReceiptName)); err != nil {
		t.Fatalf("external emergency receipt missing: %v", err)
	}
}

func seedEvents(t *testing.T, s *Store, category Category, count uint64) {
	t.Helper()
	if category != CategoryGeneral && category != CategoryProtected {
		t.Fatalf("invalid seed category %q", category)
	}
	action, result := string(ActionLoginSuccess), string(ResultSuccess)
	if category == CategoryProtected {
		action = string(ActionTokenRevoked)
	}
	res, err := s.db.Exec(`WITH d(n) AS (VALUES(0),(1),(2),(3),(4),(5),(6),(7),(8),(9)),
		seq(i) AS (SELECT a.n*10000+b.n*1000+c.n*100+d.n*10+e.n+1 FROM d a CROSS JOIN d b CROSS JOIN d c CROSS JOIN d d CROSS JOIN d e)
		INSERT INTO events(at,actor_kind,actor_id,source,action,target,result,operation_id,category,coalesced_count)
		SELECT ?, 'system','root','unix',?,'',?,'',?,1 FROM seq WHERE i<=?`, time.Now().UnixMilli(), action, result, category, count)
	if err != nil {
		t.Fatal(err)
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		t.Fatal(err)
	}
	column := "total_inserted_general"
	if category == CategoryProtected {
		column = "total_inserted_protected"
	}
	if _, err = s.db.Exec("UPDATE retention SET "+column+"="+column+"+?", inserted); err != nil {
		t.Fatal(err)
	}
}

type emptyOperationSource struct{}

func (emptyOperationSource) LookupOperation(context.Context, string) (OperationRecord, bool, error) {
	return OperationRecord{}, false, nil
}

type staticOperationSource struct{ record OperationRecord }

func (s staticOperationSource) LookupOperation(_ context.Context, id string) (OperationRecord, bool, error) {
	return s.record, s.record.OperationID == id, nil
}
