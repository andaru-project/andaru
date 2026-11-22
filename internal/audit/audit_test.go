package audit

import (
	"context"
	"database/sql"
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

func ageEvent(t *testing.T, s *Store, id uint64, at time.Time) {
	t.Helper()
	if _, err := s.db.Exec("UPDATE events SET at=? WHERE id=?", at.UnixMilli(), id); err != nil {
		t.Fatal(err)
	}
}

func testTarget(action Action) string {
	switch action {
	case ActionLoginSuccess, ActionAccountCreated, ActionAccountUpdated, ActionAccountDisabled, ActionAccountDeleted, ActionPasswordChanged:
		return "00000000-0000-4000-8000-000000000001"
	case ActionRoleChanged:
		return "rol_00000000-0000-4000-8000-000000000001"
	case ActionSessionLogout, ActionSessionRevoked:
		return base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	case ActionTokenCreated, ActionTokenRevoked:
		return base64.RawURLEncoding.EncodeToString(make([]byte, 16))
	case ActionConfigurationSaved, ActionConfigurationApplied, ActionConfigurationConfirmed, ActionConfigurationRollback:
		return "1"
	case ActionBackupRestored:
		return strings.Repeat("a", 32)
	default:
		return ""
	}
}

func testEvent(action Action, category Category, result Result) Event {
	return Event{
		Actor:    authz.Actor{Kind: authz.ActorSystem, ID: "root"},
		Source:   "unix",
		Action:   action,
		Target:   testTarget(action),
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
		Target:      testTarget(action),
		RequestHash: strings.Repeat("a", 64),
		Category:    category,
	}
}

func TestAuditActorAndTargetIdentifiersAreTyped(t *testing.T) {
	const accountID = "00000000-0000-4000-8000-000000000001"
	tokenSelector := base64.RawURLEncoding.EncodeToString(make([]byte, 16))
	sessionSelector := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	validCases := []struct {
		name   string
		actor  authz.Actor
		action Action
		target string
	}{
		{"root account target", authz.Actor{Kind: authz.ActorSystem, ID: "root"}, ActionAccountUpdated, accountID},
		{"account actor", authz.Actor{Kind: authz.ActorAccount, ID: accountID}, ActionAccountUpdated, accountID},
		{"token actor", authz.Actor{Kind: authz.ActorToken, ID: tokenSelector}, ActionTokenRevoked, tokenSelector},
		{"role target", authz.Actor{Kind: authz.ActorSystem, ID: "root"}, ActionRoleChanged, "rol_00000000-0000-4000-8000-000000000001"},
		{"built-in role target", authz.Actor{Kind: authz.ActorSystem, ID: "root"}, ActionRoleChanged, "administrator"},
		{"session selector target", authz.Actor{Kind: authz.ActorAccount, ID: accountID}, ActionSessionRevoked, sessionSelector},
		{"apply target", authz.Actor{Kind: authz.ActorSystem, ID: "root"}, ActionConfigurationApplied, "123456789"},
		{"backup target", authz.Actor{Kind: authz.ActorSystem, ID: "root"}, ActionBackupRestored, strings.Repeat("a", 32)},
		{"empty target", authz.Actor{Kind: authz.ActorSystem, ID: "root"}, ActionLoginFailure, ""},
	}
	for _, tc := range validCases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateFields(tc.actor, "unix", tc.action, tc.target, actionRules[tc.action].category); err != nil {
				t.Fatalf("valid public identifier rejected: %v", err)
			}
		})
	}

	secret := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for _, actor := range []authz.Actor{
		{Kind: authz.ActorSystem, ID: secret},
		{Kind: authz.ActorAccount, ID: secret},
		{Kind: authz.ActorToken, ID: secret},
	} {
		t.Run("reject secret actor "+string(actor.Kind), func(t *testing.T) {
			if err := validateFields(actor, "unix", ActionAccountUpdated, accountID, CategoryProtected); err == nil {
				t.Fatal("credential-shaped actor ID accepted")
			}
		})
	}
	for _, tc := range []struct {
		name   string
		action Action
		target string
	}{
		{"unknown system actor", ActionAccountUpdated, accountID},
		{"account mismatch", ActionAccountUpdated, "administrator"},
		{"role mismatch", ActionRoleChanged, accountID},
		{"token selector mismatch", ActionTokenRevoked, sessionSelector},
		{"session selector mismatch", ActionSessionRevoked, tokenSelector},
		{"apply mismatch", ActionConfigurationApplied, accountID},
		{"backup mismatch", ActionBackupRestored, "1"},
		{"missing required target", ActionAccountUpdated, ""},
		{"unexpected target", ActionLoginFailure, accountID},
		{"secret target", ActionAccountUpdated, secret},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor := authz.Actor{Kind: authz.ActorSystem, ID: "root"}
			if tc.name == "unknown system actor" {
				actor.ID = "unknown"
			}
			if err := validateFields(actor, "unix", tc.action, tc.target, actionRules[tc.action].category); err == nil {
				t.Fatal("invalid actor/target contract accepted")
			}
		})
	}
}

func TestValidateEventActorIDsByKind(t *testing.T) {
	accountID := "00000000-0000-4000-8000-000000000001"
	tokenSelector := base64.RawURLEncoding.EncodeToString(make([]byte, 16))
	secret := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for _, tc := range []struct {
		name      string
		actor     authz.Actor
		wantValid bool
	}{
		{"known system actor", authz.Actor{Kind: authz.ActorSystem, ID: "root"}, true},
		{"account UUID actor", authz.Actor{Kind: authz.ActorAccount, ID: accountID}, true},
		{"token selector actor", authz.Actor{Kind: authz.ActorToken, ID: tokenSelector}, true},
		{"system secret", authz.Actor{Kind: authz.ActorSystem, ID: secret}, false},
		{"account secret", authz.Actor{Kind: authz.ActorAccount, ID: secret}, false},
		{"token secret", authz.Actor{Kind: authz.ActorToken, ID: secret}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := Event{
				Actor:    tc.actor,
				Source:   "unix",
				Action:   ActionTokenCreated,
				Target:   tokenSelector,
				Result:   ResultSuccess,
				Category: CategoryGeneral,
			}
			_, err := validateEvent(event, time.Now(), false)
			if tc.wantValid && err != nil {
				t.Fatalf("valid actor rejected: %v", err)
			}
			if !tc.wantValid && !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid actor error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestCreationTargetsComeFromSuccessfulOutcomes(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	for _, tc := range []struct {
		action   Action
		category Category
		target   string
	}{
		{ActionAccountCreated, CategoryProtected, "00000000-0000-4000-8000-000000000001"},
		{ActionTokenCreated, CategoryGeneral, base64.RawURLEncoding.EncodeToString(make([]byte, 16))},
	} {
		intent := testIntent("create-"+string(tc.action), tc.action, tc.category)
		intent.Target = ""
		if _, err := s.Begin(ctx, intent); err != nil {
			t.Fatalf("Begin %s without generated target: %v", tc.action, err)
		}
		outcome := Outcome{OperationID: intent.OperationID, Result: ResultSuccess, Target: tc.target}
		if err := s.Finish(ctx, outcome); err != nil {
			t.Fatalf("Finish %s with generated target: %v", tc.action, err)
		}
		events, err := s.ListEvents(ctx, 0, 100)
		if err != nil || len(events) == 0 || events[len(events)-1].Target != tc.target {
			t.Fatalf("creation event target for %s = %+v, %v", tc.action, events, err)
		}
		replay, err := s.Begin(ctx, intent)
		if err != nil || !replay.Replay || replay.Outcome == nil || replay.Outcome.Target != tc.target {
			t.Fatalf("creation replay target for %s = %+v, %v", tc.action, replay, err)
		}
	}
	report, err := s.Verify(ctx)
	if err != nil || !report.Valid || report.EventCount != 4 {
		t.Fatalf("integrity after creation outcomes = %+v, %v", report, err)
	}
}

func TestCreationOutcomeRequiresValidPublicTargetOnlyOnSuccess(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	for i, tc := range []struct {
		name    string
		action  Action
		result  Result
		target  string
		invalid bool
	}{
		{"missing success target", ActionAccountCreated, ResultSuccess, "", true},
		{"invalid account target", ActionAccountCreated, ResultSuccess, "not-an-account-id", true},
		{"credential-shaped account target", ActionAccountCreated, ResultSuccess, base64.RawURLEncoding.EncodeToString(make([]byte, 32)), true},
		{"credential-shaped token target", ActionTokenCreated, ResultSuccess, base64.RawURLEncoding.EncodeToString(make([]byte, 32)), true},
		{"failed creation target", ActionAccountCreated, ResultFailure, "00000000-0000-4000-8000-000000000001", true},
		{"not committed creation target", ActionAccountCreated, ResultNotCommitted, "00000000-0000-4000-8000-000000000001", true},
		{"failed creation without target", ActionAccountCreated, ResultFailure, "", false},
		{"not committed creation without target", ActionAccountCreated, ResultNotCommitted, "", false},
		{"pending creation with target", ActionAccountCreated, ResultPending, "00000000-0000-4000-8000-000000000001", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			intent := testIntent(fmt.Sprintf("creation-check-%d", i), tc.action, actionRules[tc.action].category)
			intent.Target = ""
			if _, err := s.Begin(ctx, intent); err != nil {
				t.Fatalf("Begin creation: %v", err)
			}
			err := s.Finish(ctx, Outcome{
				OperationID: intent.OperationID,
				Result:      tc.result,
				Target:      tc.target,
			})
			if tc.invalid && !errors.Is(err, ErrInvalid) {
				t.Fatalf("Finish error = %v, want ErrInvalid", err)
			}
			if !tc.invalid && err != nil {
				t.Fatalf("valid outcome rejected: %v", err)
			}
		})
	}
}

func TestCreationFinishRejectsCredentialTargetForEveryResult(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	credential := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for i, result := range []Result{
		ResultPending, ResultSuccess, ResultFailure, ResultDenied, ResultCommitted,
		ResultNotCommitted, ResultUnavailable, ResultRecovered, ResultRolledBack,
	} {
		t.Run(string(result), func(t *testing.T) {
			intent := testIntent(fmt.Sprintf("credential-target-%d", i), ActionAccountCreated, CategoryProtected)
			intent.Target = ""
			before, err := s.ListEvents(ctx, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Begin(ctx, intent); err != nil {
				t.Fatalf("Begin creation: %v", err)
			}
			err = s.Finish(ctx, Outcome{OperationID: intent.OperationID, Result: result, Target: credential})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Finish error = %v, want ErrInvalid", err)
			}
			events, err := s.ListEvents(ctx, 0, 100)
			if err != nil || len(events) != len(before)+1 {
				t.Fatalf("event count after rejected Finish = %d, prior %d, error %v", len(events), len(before), err)
			}
			pending := events[len(events)-1]
			if pending.OperationID != intent.OperationID || pending.Action != ActionAccountCreated || pending.Result != ResultPending || pending.Target != "" {
				t.Fatalf("event after rejected Finish = %+v", pending)
			}
		})
	}
}

func TestCreationFinishAllowsEmptyTargetResultsAndPreservesReplayIntegrity(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	for i, result := range []Result{
		ResultFailure, ResultDenied, ResultCommitted, ResultNotCommitted,
		ResultUnavailable, ResultRecovered, ResultRolledBack,
	} {
		t.Run(string(result), func(t *testing.T) {
			intent := testIntent(fmt.Sprintf("empty-target-%d", i), ActionAccountCreated, CategoryProtected)
			intent.Target = ""
			if _, err := s.Begin(ctx, intent); err != nil {
				t.Fatalf("Begin creation: %v", err)
			}
			if err := s.Finish(ctx, Outcome{OperationID: intent.OperationID, Result: result}); err != nil {
				t.Fatalf("Finish empty target: %v", err)
			}
			if result == ResultNotCommitted {
				retry, err := s.Begin(ctx, intent)
				if err != nil || !retry.Started || retry.Replay {
					t.Fatalf("not-committed retry = %+v, %v", retry, err)
				}
			} else {
				replay, err := s.Begin(ctx, intent)
				if err != nil || !replay.Replay || replay.Outcome == nil || replay.Outcome.Result != result || replay.Outcome.Target != "" {
					t.Fatalf("empty-target replay = %+v, %v", replay, err)
				}
			}
			report, err := s.Verify(ctx)
			if err != nil || !report.Valid {
				t.Fatalf("integrity = %+v, %v", report, err)
			}
		})
	}
}

func TestFailedCreationOutcomeHasNoTargetThroughRetryAndReplay(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("audit database tests require root")
	}
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	intent := testIntent("failed-create-replay", ActionAccountCreated, CategoryProtected)
	intent.Target = ""
	if _, err = s.Begin(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(ctx, Outcome{OperationID: intent.OperationID, Result: ResultFailure}); err != nil {
		t.Fatal(err)
	}
	replay, err := s.Begin(ctx, intent)
	if err != nil || !replay.Replay || replay.Outcome == nil || replay.Outcome.Target != "" || replay.Outcome.Result != ResultFailure {
		t.Fatalf("failed creation replay = %+v, %v", replay, err)
	}
	events, err := s.ListEvents(ctx, 0, 100)
	if err != nil || len(events) != 2 || events[1].Target != "" || events[1].Result != ResultFailure {
		t.Fatalf("failed creation events = %+v, %v", events, err)
	}
	report, err := s.Verify(ctx)
	if err != nil || !report.Valid {
		t.Fatalf("failed creation integrity = %+v, %v", report, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAuthoritativeAbsenceAllowsEmptyTargetCreationRetry(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("audit database tests require root")
	}
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	intent := testIntent("absent-create-retry", ActionAccountCreated, CategoryProtected)
	intent.Target = ""
	if _, err = s.Begin(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Reconcile(ctx, emptyOperationSource{}); err != nil {
		t.Fatal(err)
	}
	events, err := s.ListEvents(ctx, 0, 100)
	if err != nil || len(events) != 2 || events[1].Result != ResultNotCommitted || events[1].Target != "" {
		t.Fatalf("authoritative absence events = %+v, %v", events, err)
	}
	retry, err := s.Begin(ctx, intent)
	if err != nil || !retry.Started || retry.Replay {
		t.Fatalf("creation retry after authoritative absence = %+v, %v", retry, err)
	}
	if err = s.Finish(ctx, Outcome{OperationID: intent.OperationID, Result: ResultFailure}); err != nil {
		t.Fatal(err)
	}
	replay, err := s.Begin(ctx, intent)
	if err != nil || !replay.Replay || replay.Outcome == nil || replay.Outcome.Target != "" || replay.Outcome.Result != ResultFailure {
		t.Fatalf("creation retry replay = %+v, %v", replay, err)
	}
	report, err := s.Verify(ctx)
	if err != nil || !report.Valid {
		t.Fatalf("creation retry integrity = %+v, %v", report, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNonCreationIntentStillRequiresTarget(t *testing.T) {
	s, _ := openTestStore(t)
	intent := testIntent("empty-update-target", ActionAccountUpdated, CategoryProtected)
	intent.Target = ""
	if _, err := s.Begin(context.Background(), intent); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty non-creation target error = %v, want ErrInvalid", err)
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

func TestUnsupportedAuditSchemaVersionsFailClosed(t *testing.T) {
	for _, version := range []int{0, LayoutVersion + 1} {
		t.Run(fmt.Sprintf("version-%d", version), func(t *testing.T) {
			s, dir := openTestStore(t)
			if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version=%d", version)); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(context.Background(), dir); err == nil {
				t.Fatalf("opened unsupported schema version %d", version)
			}
		})
	}
}

func TestAuditSchemaLayoutMismatchFailsClosed(t *testing.T) {
	s, dir := openTestStore(t)
	if _, err := s.db.Exec("DROP INDEX events_coalesce"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), dir); err == nil {
		t.Fatal("opened database with an incomplete layout")
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

func TestListEventsRejectsInvalidCursorWithoutLatchingStorageFailure(t *testing.T) {
	s, _ := openTestStore(t)
	if _, err := s.ListEvents(context.Background(), ^uint64(0), 10); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid cursor error = %v", err)
	}
	if _, err := s.ListEvents(context.Background(), 0, 10); err != nil {
		t.Fatalf("healthy ListEvents after invalid cursor = %v", err)
	}
	if s.storageFailure.Load() {
		t.Fatal("invalid cursor latched storage failure")
	}
}

func TestStorageErrorLatchesWhenContextAlsoCancels(t *testing.T) {
	s, _ := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := s.storageError(ctx, errors.New("disk I/O error"))
	if !errors.Is(err, ErrStorage) || errors.Is(err, context.Canceled) {
		t.Fatalf("storage error with canceled context = %v", err)
	}
	if !s.storageFailure.Load() {
		t.Fatal("storage failure was not latched")
	}
}

func TestCanceledTransactionRollbackDoesNotLatchStorageFailure(t *testing.T) {
	s, _ := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancel()

	if _, err = tx.ExecContext(ctx, "INSERT INTO events(at,actor_kind,actor_id,source,action,target,result,operation_id,category,count) VALUES(?,?,?,?,?,?,?,?,?,?)",
		time.Now().UnixMilli(), "system", "root", "local", ActionAccountUpdated, "target", ResultSuccess, "", CategoryProtected, 1); err == nil {
		t.Fatal("write unexpectedly succeeded after cancellation")
	}
	if err = tx.Commit(); !errors.Is(err, sql.ErrTxDone) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Commit error = %v, want cancellation artifact after automatic rollback", err)
	}
	if classified := s.storageError(ctx, err); !errors.Is(classified, context.Canceled) {
		t.Fatalf("canceled transaction error = %v, want context.Canceled", classified)
	}
	if s.storageFailure.Load() {
		t.Fatal("automatic rollback latched storage failure")
	}
	if _, err = s.Begin(context.Background(), testIntent("healthy-after-rollback", ActionAccountUpdated, CategoryProtected)); err != nil {
		t.Fatalf("healthy Begin after rollback = %v", err)
	}
	if _, err = s.Append(context.Background(), testEvent(ActionAccountUpdated, CategoryProtected, ResultSuccess)); err != nil {
		t.Fatalf("healthy Append after rollback = %v", err)
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
	first, err := s.Append(ctx, event)
	if err != nil {
		t.Fatal(err)
	}
	expired, err := s.Append(ctx, testEvent(ActionLoginSuccess, CategoryGeneral, ResultSuccess))
	if err != nil {
		t.Fatal(err)
	}
	ageEvent(t, s, expired.ID, time.Now().Add(-retentionPeriod-time.Second))
	if _, err := s.Append(ctx, event); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM events WHERE id=?", expired.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("expired event count = %d, err=%v", remaining, err)
	}
	got, err := s.ListEvents(ctx, 0, 10)
	if err != nil || len(got) != 1 || got[0].ID != first.ID || got[0].Count != 2 {
		t.Fatalf("events after coalesced prune = %+v, %v", got, err)
	}
	report, err := s.Verify(ctx)
	if err != nil || !report.Valid {
		t.Fatalf("integrity report after coalesced prune = %+v, %v", report, err)
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
	ageEvent(t, s, expired.ID, time.Now().UTC().Add(-366*24*time.Hour))
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
	ageEvent(t, s, expired.ID, time.Now().UTC().Add(-366*24*time.Hour))
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

func TestAppendSafetyWritesReceiptBeforePendingStartupGate(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("audit database tests require root")
	}
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	intent := testIntent("request-safety-pending", ActionAccountUpdated, CategoryProtected)
	if _, err = s.Begin(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	err = s.AppendSafety(ctx, testEvent(ActionRecoveryCompleted, CategoryProtected, ResultRecovered))
	if !errors.Is(err, ErrReconciliationRequired) {
		t.Fatalf("safety append before startup reconciliation = %v", err)
	}
	if _, err = os.Stat(filepath.Join(dir, emergencyReceiptName)); err != nil {
		t.Fatalf("receipt was suppressed by pending startup intent: %v", err)
	}
}

func TestAppendSafetyReturnsWhileReconciliationLookupIsBlocked(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("audit database tests require root")
	}
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	intent := testIntent("request-safety-reconcile", ActionAccountUpdated, CategoryProtected)
	if _, err = s.Begin(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	lookupStarted := make(chan struct{})
	releaseLookup := make(chan struct{})
	reconcileDone := make(chan error, 1)
	go func() {
		reconcileDone <- s.Reconcile(ctx, blockingOperationSource{started: lookupStarted, release: releaseLookup})
	}()
	<-lookupStarted

	safetyDone := make(chan error, 1)
	go func() {
		safetyDone <- s.AppendSafety(ctx, testEvent(ActionRecoveryCompleted, CategoryProtected, ResultRecovered))
	}()
	receiptPath := filepath.Join(dir, emergencyReceiptName)
	deadline := time.After(2 * time.Second)
	for {
		if _, err = os.Stat(receiptPath); err == nil {
			break
		} else if !os.IsNotExist(err) {
			close(releaseLookup)
			t.Fatalf("checking emergency receipt: %v", err)
		}
		select {
		case <-deadline:
			close(releaseLookup)
			t.Fatal("reconciliation lookup suppressed the receipt attempt")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case err = <-safetyDone:
		if !errors.Is(err, ErrReconciliationRequired) {
			close(releaseLookup)
			t.Fatalf("safety append while reconciliation was blocked = %v, want reconciliation error", err)
		}
	case <-time.After(2 * time.Second):
		close(releaseLookup)
		t.Fatal("safety append waited for reconciliation lookup")
	}

	close(releaseLookup)
	if err = <-reconcileDone; err != nil {
		t.Fatalf("reconciliation failed: %v", err)
	}
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

type blockingOperationSource struct {
	started chan struct{}
	release <-chan struct{}
}

func (s blockingOperationSource) LookupOperation(context.Context, string) (OperationRecord, bool, error) {
	close(s.started)
	<-s.release
	return OperationRecord{}, false, nil
}

func (emptyOperationSource) LookupOperation(context.Context, string) (OperationRecord, bool, error) {
	return OperationRecord{}, false, nil
}

type staticOperationSource struct{ record OperationRecord }

func (s staticOperationSource) LookupOperation(_ context.Context, id string) (OperationRecord, bool, error) {
	return s.record, s.record.OperationID == id, nil
}

// A screen that polls the log reads it again and again; those reads make one event that counts them.
func TestAuditQueriesCoalesce(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	event := testEvent(ActionAuditQueried, CategoryGeneral, ResultSuccess)
	for range 3 {
		if _, err := s.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListEvents(ctx, 0, 100)
	if err != nil || len(got) != 1 || got[0].Count != 3 {
		t.Fatalf("events = %+v, %v", got, err)
	}
}

func TestListEventsBeforeReadsNewestFirst(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	for _, action := range []Action{ActionSessionLogout, ActionAuditQueried, ActionSessionLogout, ActionAuditHealth} {
		category := CategoryGeneral
		if action == ActionAuditHealth {
			category = CategoryProtected
		}
		if _, err := s.Append(ctx, testEvent(action, category, ResultSuccess)); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ListEventsBefore(ctx, 0, 2)
	if err != nil || len(first) != 2 || first[0].ID != 4 || first[1].ID != 3 {
		t.Fatalf("newest page = %+v, %v", first, err)
	}
	rest, err := s.ListEventsBefore(ctx, first[1].ID, 10)
	if err != nil || len(rest) != 2 || rest[0].ID != 2 || rest[1].ID != 1 {
		t.Fatalf("older page = %+v, %v", rest, err)
	}
	if _, err := s.ListEventsBefore(ctx, 1<<63, 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("out of range cursor: %v", err)
	}
}
