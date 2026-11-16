package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/securefs"
	"golang.org/x/sys/unix"
)

var emergencyReceiptMu sync.Mutex

type Store struct {
	db                     *sql.DB
	lock                   *os.File
	stateDir               string
	storageFailure         atomic.Bool
	closeOnce              sync.Once
	closeErr               error
	startupMu              sync.RWMutex
	startupOpen            atomic.Bool
	reconciliationRequired bool
}

func Open(ctx context.Context, stateDir string) (*Store, error) {
	if os.Geteuid() != 0 || !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir {
		return nil, errors.New("audit store requires root and a canonical absolute state directory")
	}
	if err := securefs.CheckAncestors(filepath.Dir(stateDir)); err != nil {
		return nil, err
	}
	if err := os.Mkdir(stateDir, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	if err := privateDirectory(stateDir); err != nil {
		return nil, err
	}
	lock, err := acquireLock(filepath.Join(stateDir, "audit.lock"))
	if err != nil {
		return nil, err
	}
	db, err := openDatabase(ctx, stateDir)
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	var pending int64
	if err = db.QueryRowContext(ctx, "SELECT count(*) FROM operations WHERE state='pending'").Scan(&pending); err != nil {
		_ = db.Close()
		_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		_ = lock.Close()
		return nil, err
	}
	s := &Store{db: db, lock: lock, stateDir: stateDir, reconciliationRequired: pending > 0}
	s.startupOpen.Store(true)
	return s, nil
}

func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		if err := s.db.Close(); err != nil {
			s.closeErr = err
		}
		if err := unix.Flock(int(s.lock.Fd()), unix.LOCK_UN); err != nil && s.closeErr == nil {
			s.closeErr = err
		}
		if err := s.lock.Close(); err != nil && s.closeErr == nil {
			s.closeErr = err
		}
	})
	return s.closeErr
}
func (s *Store) Append(ctx context.Context, event Event) (Event, error) {
	if err := s.beginMutation(); err != nil {
		return Event{}, err
	}
	defer s.endMutation()
	return s.append(ctx, event, false)
}

// AppendSafety records the typed event in audit.db and an independent bounded receipt.
// Callers must continue safety recovery even when this method reports an error.
func (s *Store) AppendSafety(ctx context.Context, event Event) error {
	prepared, err := validateEvent(event, time.Now().UTC(), true)
	if err != nil {
		return err
	}
	receiptErr := WriteEmergencyReceipt(s.stateDir, prepared)
	if err = s.beginMutation(); err != nil {
		return errors.Join(receiptErr, err)
	}
	defer s.endMutation()
	if receiptErr != nil {
		s.storageFailure.Store(true)
		s.markStorageFailure(ctx)
	}
	_, databaseErr := s.append(ctx, prepared, true)
	return errors.Join(receiptErr, databaseErr)
}

func (s *Store) append(ctx context.Context, event Event, safety bool) (Event, error) {
	now := time.Now().UTC()
	prepared, err := validateEvent(event, now, safety)
	if err != nil {
		return Event{}, err
	}
	healthErr := s.requireHealthy(ctx)
	if healthErr != nil && !safety {
		return Event{}, healthErr
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, s.storageError(ctx, err)
	}
	if actionRules[prepared.Action].coalesces {
		coalesced, found, e := coalesceFailure(ctx, tx, prepared, now)
		if e != nil {
			return Event{}, s.abortStorage(ctx, tx, e)
		}
		if found {
			if e = pruneAfterAppend(ctx, tx, now); e != nil {
				return Event{}, s.abortStorage(ctx, tx, e)
			}
			if e = tx.Commit(); e != nil {
				return Event{}, s.storageError(ctx, e)
			}
			return coalesced, nil
		}
	}
	ok, e := capacityAvailable(ctx, tx, prepared.Category, 1, now)
	if e != nil {
		return Event{}, s.abortStorage(ctx, tx, e)
	}
	if !ok {
		if e = s.recordSaturation(ctx, tx, prepared.Category, now); e != nil {
			return Event{}, s.abortStorage(ctx, tx, e)
		}
		if e = tx.Commit(); e != nil {
			return Event{}, s.storageError(ctx, e)
		}
		return Event{}, errors.Join(saturationError(prepared.Category), healthErr)
	}
	id, e := insertEvent(ctx, tx, prepared)
	if e != nil {
		return Event{}, s.abortStorage(ctx, tx, e)
	}
	prepared.ID = id
	prepared.Count = 1
	if e = pruneAfterAppend(ctx, tx, now); e != nil {
		return Event{}, s.abortStorage(ctx, tx, e)
	}
	if e = clearSaturation(ctx, tx, prepared.Category); e != nil {
		return Event{}, s.abortStorage(ctx, tx, e)
	}
	if e = tx.Commit(); e != nil {
		return Event{}, s.storageError(ctx, e)
	}
	return prepared, healthErr
}

func (s *Store) Begin(ctx context.Context, intent Intent) (RecordedOperation, error) {
	if err := validateIntent(intent); err != nil {
		return RecordedOperation{}, err
	}
	if err := s.beginMutation(); err != nil {
		return RecordedOperation{}, err
	}
	defer s.endMutation()
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RecordedOperation{}, s.storageError(ctx, err)
	}
	var oldHash, state, actorKind, actorID, source, oldAction, target, oldCategory string
	var oldOutcome Result
	err = tx.QueryRowContext(ctx, `SELECT request_hash,state,result,actor_kind,actor_id,source,action,target,category FROM operations WHERE operation_id=?`, intent.OperationID).
		Scan(&oldHash, &state, &oldOutcome, &actorKind, &actorID, &source, &oldAction, &target, &oldCategory)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return RecordedOperation{}, s.abortStorage(ctx, tx, err)
	}
	if exists {
		if oldHash != intent.RequestHash ||
			(authz.Actor{Kind: authz.ActorKind(actorKind), ID: actorID} != intent.Actor) ||
			source != intent.Source || Action(oldAction) != intent.Action || target != intent.Target || Category(oldCategory) != intent.Category {
			_ = tx.Rollback()
			return RecordedOperation{}, ErrOperationConflict
		}
		switch state {
		case "pending":
			_ = tx.Rollback()
			return RecordedOperation{OperationID: intent.OperationID, Pending: true}, ErrOperationInFlight
		case "completed":
			outcome, e := readOutcome(ctx, tx, intent.OperationID, oldOutcome, intent.Action)
			if e != nil {
				if errors.Is(e, ErrIntegrity) {
					return RecordedOperation{}, s.abortIntegrity(ctx, tx)
				}
				return RecordedOperation{}, s.abortStorage(ctx, tx, e)
			}
			if e = tx.Commit(); e != nil {
				return RecordedOperation{}, s.storageError(ctx, e)
			}
			return RecordedOperation{OperationID: intent.OperationID, Replay: true, Outcome: &outcome}, nil
		case "not_committed":
			// Reconciliation proved no authoritative mutation committed; the same request may retry.
		default:
			return RecordedOperation{}, s.abortIntegrity(ctx, tx)
		}
	}
	var integrityFailure, storageFailure int
	if err = tx.QueryRowContext(ctx, `SELECT integrity_failure,storage_failure FROM health WHERE id=1`).Scan(&integrityFailure, &storageFailure); err != nil {
		return RecordedOperation{}, s.abortStorage(ctx, tx, err)
	}
	if integrityFailure != 0 {
		return RecordedOperation{}, s.abortIntegrity(ctx, tx)
	}
	if storageFailure != 0 || s.storageFailure.Load() {
		_ = tx.Rollback()
		return RecordedOperation{}, ErrStorage
	}
	ok, err := capacityAvailable(ctx, tx, intent.Category, 2, now)
	if err != nil {
		return RecordedOperation{}, s.abortStorage(ctx, tx, err)
	}
	if !ok {
		if err = s.recordSaturation(ctx, tx, intent.Category, now); err != nil {
			return RecordedOperation{}, s.abortStorage(ctx, tx, err)
		}
		if err = tx.Commit(); err != nil {
			return RecordedOperation{}, s.storageError(ctx, err)
		}
		return RecordedOperation{}, saturationError(intent.Category)
	}
	intentEvent := Event{
		At:          now,
		Actor:       intent.Actor,
		Source:      intent.Source,
		Action:      intent.Action,
		Target:      intent.Target,
		Result:      ResultPending,
		OperationID: intent.OperationID,
		Category:    intent.Category,
	}
	intentEventID, err := insertEvent(ctx, tx, intentEvent)
	if err != nil {
		return RecordedOperation{}, s.abortStorage(ctx, tx, err)
	}
	if exists {
		_, err = tx.ExecContext(ctx, `DELETE FROM operation_metadata WHERE operation_id=?`, intent.OperationID)
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE operations SET state='pending',intent_event_id=?,outcome_event_id=NULL,result='',updated_at=? WHERE operation_id=?`, intentEventID, now.UnixMilli(), intent.OperationID)
		}
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO operations(operation_id,request_hash,actor_kind,actor_id,source,action,target,category,state,intent_event_id,outcome_event_id,result,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,'pending',?,NULL,'',?,?)`, intent.OperationID, intent.RequestHash, intent.Actor.Kind, intent.Actor.ID, intent.Source, intent.Action, intent.Target, intent.Category, intentEventID, now.UnixMilli(), now.UnixMilli())
	}
	if err != nil {
		return RecordedOperation{}, s.abortStorage(ctx, tx, err)
	}
	if err = pruneAfterAppend(ctx, tx, now); err != nil {
		return RecordedOperation{}, s.abortStorage(ctx, tx, err)
	}
	if err = clearSaturation(ctx, tx, intent.Category); err != nil {
		return RecordedOperation{}, s.abortStorage(ctx, tx, err)
	}
	if err = tx.Commit(); err != nil {
		return RecordedOperation{}, s.storageError(ctx, err)
	}
	return RecordedOperation{OperationID: intent.OperationID, Started: true}, nil
}

func (s *Store) Finish(ctx context.Context, outcome Outcome) error {
	if err := s.beginMutation(); err != nil {
		return err
	}
	defer s.endMutation()
	return s.finish(ctx, outcome)
}

func (s *Store) finish(ctx context.Context, outcome Outcome) error {
	if !validOperationID(outcome.OperationID) || !validResult(outcome.Result) || outcome.Result == ResultPending || validateMetadata(outcome.Metadata) != nil {
		return ErrInvalid
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return s.storageError(ctx, err)
	}
	var state, action, category, actorKind, actorID, source, target string
	var existing Result
	if err = tx.QueryRowContext(ctx, `SELECT state,action,category,result,actor_kind,actor_id,source,target FROM operations WHERE operation_id=?`, outcome.OperationID).
		Scan(&state, &action, &category, &existing, &actorKind, &actorID, &source, &target); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			_ = tx.Rollback()
			return ErrOperationConflict
		}
		return s.abortStorage(ctx, tx, err)
	}
	if err = validateOutcome(outcome, Action(action)); err != nil {
		_ = tx.Rollback()
		return err
	}
	if state != "pending" {
		if (state == "completed" || state == "not_committed") && existing == outcome.Result {
			stored, readErr := readOutcome(ctx, tx, outcome.OperationID, existing, Action(action))
			if errors.Is(readErr, ErrIntegrity) {
				return s.abortIntegrity(ctx, tx)
			}
			if readErr != nil {
				return s.abortStorage(ctx, tx, readErr)
			}
			if stored.Target == outcome.Target && equalMetadata(stored.Metadata, outcome.Metadata) {
				if err = tx.Commit(); err != nil {
					return s.storageError(ctx, err)
				}
				return nil
			}
		}
		_ = tx.Rollback()
		return ErrOperationConflict
	}
	opAction := Action(action)
	opCategory := Category(category)
	event := Event{
		At:          now,
		Actor:       authz.Actor{Kind: authz.ActorKind(actorKind), ID: actorID},
		Source:      source,
		Action:      opAction,
		Target:      target,
		Result:      outcome.Result,
		OperationID: outcome.OperationID,
		Category:    opCategory,
		Metadata:    cloneMetadata(outcome.Metadata),
	}
	if isCreationAction(opAction) {
		event.Target = outcome.Target
	}
	eventID, err := insertEvent(ctx, tx, event)
	if err != nil {
		return s.abortStorage(ctx, tx, err)
	}
	newState := "completed"
	if outcome.Result == ResultNotCommitted {
		newState = "not_committed"
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET state=?,outcome_event_id=?,result=?,updated_at=? WHERE operation_id=?`, newState, eventID, outcome.Result, now.UnixMilli(), outcome.OperationID); err != nil {
		return s.abortStorage(ctx, tx, err)
	}
	if err = replaceOperationMetadata(ctx, tx, outcome.OperationID, outcome.Metadata); err != nil {
		return s.abortStorage(ctx, tx, err)
	}
	if err = pruneAfterAppend(ctx, tx, now); err != nil {
		return s.abortStorage(ctx, tx, err)
	}
	if err = clearSaturation(ctx, tx, opCategory); err != nil {
		return s.abortStorage(ctx, tx, err)
	}
	if err = tx.Commit(); err != nil {
		return s.storageError(ctx, err)
	}
	return nil
}

func (s *Store) ListEvents(ctx context.Context, after uint64, limit int) ([]Event, error) {
	if after > uint64(1<<63-1) {
		return nil, ErrInvalid
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > maxEventPage {
		return nil, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,at,actor_kind,actor_id,source,action,target,result,operation_id,category,coalesced_count
		FROM events WHERE id>? ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil, s.storageError(ctx, err)
	}
	events := make([]Event, 0, limit)
	for rows.Next() {
		var event Event
		var id, at, count int64
		var actorKind, action, result, category string
		if err = rows.Scan(&id, &at, &actorKind, &event.Actor.ID, &event.Source, &action, &event.Target, &result, &event.OperationID, &category, &count); err != nil {
			rows.Close()
			return nil, s.storageError(ctx, err)
		}
		event.ID = uint64(id)
		event.At = time.UnixMilli(at).UTC()
		event.Actor.Kind = authz.ActorKind(actorKind)
		event.Action = Action(action)
		event.Result = Result(result)
		event.Category = Category(category)
		event.Count = uint64(count)
		events = append(events, event)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, s.storageError(ctx, err)
	}
	if err = rows.Close(); err != nil {
		return nil, s.storageError(ctx, err)
	}
	for i := range events {
		events[i].Metadata, err = readEventMetadata(ctx, s.db, events[i].ID)
		if err != nil {
			return nil, s.storageError(ctx, err)
		}
	}
	return events, nil
}

func openDatabase(ctx context.Context, stateDir string) (*sql.DB, error) {
	finalPath := filepath.Join(stateDir, "audit.db")
	_, statErr := os.Lstat(finalPath)
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, statErr
	}
	fresh := os.IsNotExist(statErr)
	path := finalPath
	published := false
	if fresh {
		tmp, err := os.CreateTemp(stateDir, ".audit.db-init-")
		if err != nil {
			return nil, err
		}
		path = tmp.Name()
		if err = tmp.Close(); err != nil {
			_ = os.Remove(path)
			return nil, err
		}
		defer func() {
			if !published {
				removeDatabaseFiles(path)
			}
		}()
	} else {
		if err := checkPrivateDBFiles(finalPath); err != nil {
			return nil, err
		}
		if err := probeDatabase(ctx, finalPath); err != nil {
			return nil, err
		}
	}
	if err := checkPrivateDBFiles(path); err != nil {
		return nil, err
	}
	db, err := openSQL(ctx, path)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*sql.DB, error) {
		_ = db.Close()
		return nil, err
	}
	var appID, version int
	if err = db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&appID); err != nil {
		return fail(err)
	}
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if fresh {
		if appID != 0 || version != 0 {
			return fail(errors.New("fresh audit database has invalid identifiers"))
		}
		tx, txErr := db.BeginTx(ctx, nil)
		if txErr != nil {
			return fail(txErr)
		}
		if _, txErr = tx.ExecContext(ctx, schemaSQL()); txErr == nil {
			_, txErr = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id=%d", applicationID))
		}
		if txErr == nil {
			_, txErr = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", LayoutVersion))
		}
		if txErr != nil {
			_ = tx.Rollback()
			return fail(txErr)
		}
		if txErr = tx.Commit(); txErr != nil {
			return fail(txErr)
		}
	} else if appID != applicationID {
		return fail(errors.New("audit database application or layout mismatch"))
	} else if version != LayoutVersion {
		return fail(errors.New("audit database application or layout mismatch"))
	}
	if err = verifySchema(ctx, db); err != nil {
		return fail(err)
	}
	if err = verifyPragmas(ctx, db); err != nil {
		return fail(err)
	}
	if err = checkPrivateDBFiles(path); err != nil {
		return fail(err)
	}
	if fresh {
		if err = checkpointStagingWAL(ctx, db, path); err != nil {
			return fail(err)
		}
		if err = db.Close(); err != nil {
			return nil, err
		}
		if err = unix.Renameat2(unix.AT_FDCWD, path, unix.AT_FDCWD, finalPath, unix.RENAME_NOREPLACE); err != nil {
			return nil, err
		}
		published = true
		dir, syncErr := os.Open(stateDir)
		if syncErr == nil {
			syncErr = dir.Sync()
			if closeErr := dir.Close(); syncErr == nil {
				syncErr = closeErr
			}
		}
		if syncErr != nil {
			return nil, syncErr
		}
		db, err = openSQL(ctx, finalPath)
		if err != nil {
			return nil, err
		}
		if err = db.PingContext(ctx); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	if err = verifySchema(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err = verifyPragmas(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err = checkPrivateDBFiles(finalPath); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func openSQL(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", databaseURI(path, false))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func probeDatabase(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", databaseURI(path, true))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = db.PingContext(ctx); err != nil {
		return err
	}
	var appID, version int
	if err = db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&appID); err != nil {
		return err
	}
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if appID != applicationID || version != LayoutVersion {
		return errors.New("audit database application or layout mismatch")
	}
	return nil
}

func checkpointStagingWAL(ctx context.Context, db *sql.DB, path string) error {
	var busy, frames, checkpointed int
	if err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &checkpointed); err != nil {
		return err
	}
	if busy != 0 || frames < 0 || checkpointed != frames {
		return errors.New("audit staging WAL checkpoint incomplete")
	}
	wal := path + "-wal"
	st, err := os.Lstat(wal)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !privateFile(st) || st.Size() != 0 {
		return errors.New("audit staging WAL still contains database state")
	}
	return nil
}

func acquireLock(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	st, err := file.Stat()
	if err != nil || !privateFile(st) {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("unsafe audit lock file")
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New("audit store is already open")
	}
	return file, nil
}

func privateDirectory(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.IsDir() || !securefs.RootOwned(st) || st.Mode().Perm() != 0700 {
		return errors.New("audit state directory must be root-owned mode 0700")
	}
	return nil
}

func privateFile(st os.FileInfo) bool {
	stat, ok := st.Sys().(*syscall.Stat_t)
	return ok && st.Mode().IsRegular() && st.Mode().Perm() == 0600 && stat.Uid == 0 && stat.Nlink == 1
}

func checkPrivateDBFiles(path string) error {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		member := path + suffix
		st, err := os.Lstat(member)
		if os.IsNotExist(err) && suffix != "" {
			continue
		}
		if err != nil {
			return err
		}
		if !privateFile(st) {
			return fmt.Errorf("unsafe audit database member %s", filepath.Base(member))
		}
	}
	return nil
}

func removeDatabaseFiles(path string) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		_ = os.Remove(path + suffix)
	}
}

func insertEvent(ctx context.Context, tx *sql.Tx, event Event) (uint64, error) {
	result, err := tx.ExecContext(ctx, `INSERT INTO events(at,actor_kind,actor_id,source,action,target,result,operation_id,category,coalesced_count)
		VALUES(?,?,?,?,?,?,?,?,?,1)`, event.At.UnixMilli(), event.Actor.Kind, event.Actor.ID, event.Source, event.Action, event.Target, event.Result, event.OperationID, event.Category)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil || id < 1 {
		if err != nil {
			return 0, err
		}
		return 0, errors.New("audit event ID was not allocated")
	}
	if err = insertMetadata(ctx, tx, id, event.Metadata); err != nil {
		return 0, err
	}
	column := "total_inserted_general"
	if event.Category == CategoryProtected {
		column = "total_inserted_protected"
	}
	if _, err = tx.ExecContext(ctx, "UPDATE retention SET "+column+"="+column+"+1 WHERE id=1"); err != nil {
		return 0, err
	}
	return uint64(id), nil
}

func insertMetadata(ctx context.Context, tx *sql.Tx, eventID int64, metadata map[string]string) error {
	for key, value := range metadata {
		if _, err := tx.ExecContext(ctx, "INSERT INTO event_metadata(event_id,key,value) VALUES(?,?,?)", eventID, key, value); err != nil {
			return err
		}
	}
	return nil
}

func coalesceFailure(ctx context.Context, tx *sql.Tx, event Event, now time.Time) (Event, bool, error) {
	if event.OperationID != "" {
		return Event{}, false, nil
	}
	var id, count int64
	err := tx.QueryRowContext(ctx, `SELECT id,coalesced_count FROM events
		WHERE action=? AND actor_kind=? AND actor_id=? AND source=? AND result=? AND target=? AND category=? AND operation_id='' AND at>=?
		ORDER BY id DESC LIMIT 1`, event.Action, event.Actor.Kind, event.Actor.ID, event.Source, event.Result, event.Target, event.Category, now.Add(-coalesceWindow).UnixMilli()).Scan(&id, &count)
	if errors.Is(err, sql.ErrNoRows) {
		return Event{}, false, nil
	}
	if err != nil {
		return Event{}, false, err
	}
	metadata, err := readEventMetadata(ctx, tx, uint64(id))
	if err != nil {
		return Event{}, false, err
	}
	if count < 1 || count == int64(^uint64(0)>>1) {
		return Event{}, false, errors.New("invalid coalesced event count")
	}
	if _, err = tx.ExecContext(ctx, "UPDATE events SET at=?,coalesced_count=coalesced_count+1 WHERE id=?", now.UnixMilli(), id); err != nil {
		return Event{}, false, err
	}
	event.ID = uint64(id)
	event.At = now
	event.Count = uint64(count + 1)
	event.Metadata = metadata
	return event, true, nil
}

func readEventMetadata(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, eventID uint64) (map[string]string, error) {
	rows, err := queryer.QueryContext(ctx, "SELECT key,value FROM event_metadata WHERE event_id=? ORDER BY key", eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	metadata := map[string]string{}
	for rows.Next() {
		var key, value string
		if err = rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		if _, exists := metadata[key]; exists {
			return nil, ErrIntegrity
		}
		metadata[key] = value
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if validateMetadata(metadata) != nil {
		return nil, ErrIntegrity
	}
	return metadata, nil
}

func readOutcome(ctx context.Context, tx *sql.Tx, operationID string, result Result, action Action) (Outcome, error) {
	if result == "" || !validResult(result) || result == ResultPending {
		return Outcome{}, ErrIntegrity
	}
	var target string
	if err := tx.QueryRowContext(ctx, `SELECT e.target FROM operations o JOIN events e ON e.id=o.outcome_event_id WHERE o.operation_id=?`, operationID).Scan(&target); err != nil {
		return Outcome{}, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT key,value FROM operation_metadata WHERE operation_id=? ORDER BY key", operationID)
	if err != nil {
		return Outcome{}, err
	}
	defer rows.Close()
	metadata := map[string]string{}
	for rows.Next() {
		var key, value string
		if err = rows.Scan(&key, &value); err != nil {
			return Outcome{}, err
		}
		metadata[key] = value
	}
	if err = rows.Err(); err != nil {
		return Outcome{}, err
	}
	outcome := Outcome{OperationID: operationID, Result: result, Target: target, Metadata: metadata}
	if validateOutcome(outcome, action) != nil {
		return Outcome{}, ErrIntegrity
	}
	return outcome, nil
}

func replaceOperationMetadata(ctx context.Context, tx *sql.Tx, operationID string, metadata map[string]string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM operation_metadata WHERE operation_id=?", operationID); err != nil {
		return err
	}
	for key, value := range metadata {
		if _, err := tx.ExecContext(ctx, "INSERT INTO operation_metadata(operation_id,key,value) VALUES(?,?,?)", operationID, key, value); err != nil {
			return err
		}
	}
	return nil
}

func equalMetadata(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func cloneMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}
	clone := make(map[string]string, len(metadata))
	for key, value := range metadata {
		clone[key] = value
	}
	return clone
}

// WriteEmergencyReceipt appends a bounded receipt without opening audit.db.
// Recovery callers can use it when Open or database writes fail, then continue recovery.
func WriteEmergencyReceipt(stateDir string, event Event) error {
	if os.Geteuid() != 0 || !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir {
		return ErrInvalid
	}
	if err := securefs.CheckAncestors(filepath.Dir(stateDir)); err != nil {
		return wrapStorage(err)
	}
	if err := privateDirectory(stateDir); err != nil {
		return wrapStorage(err)
	}
	prepared, err := validateEvent(event, time.Now().UTC(), true)
	if err != nil {
		return err
	}
	return writeEmergencyReceipt(stateDir, prepared)
}

func writeEmergencyReceipt(stateDir string, event Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return ErrInvalid
	}
	data = append(data, '\n')
	emergencyReceiptMu.Lock()
	defer emergencyReceiptMu.Unlock()
	lock, err := acquireLock(filepath.Join(stateDir, "audit.receipt.lock"))
	if err != nil {
		return wrapStorage(err)
	}
	defer lock.Close()
	path := filepath.Join(stateDir, emergencyReceiptName)
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_APPEND|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return wrapStorage(err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	st, err := file.Stat()
	if err != nil || !privateFile(st) {
		if err != nil {
			return wrapStorage(err)
		}
		return errors.New("unsafe emergency audit receipt")
	}
	if st.Size()+int64(len(data)) > maxReceiptBytes {
		return ErrReceiptFull
	}
	written, err := file.Write(data)
	if err != nil {
		return wrapStorage(err)
	}
	if written != len(data) {
		return wrapStorage(errors.New("short emergency receipt write"))
	}
	if err = file.Sync(); err != nil {
		return wrapStorage(err)
	}
	return nil
}

func (s *Store) beginMutation() error {
	s.startupMu.RLock()
	if s.reconciliationRequired {
		s.startupMu.RUnlock()
		return ErrReconciliationRequired
	}
	s.startupOpen.Store(false)
	return nil
}

func (s *Store) endMutation() {
	s.startupMu.RUnlock()
}

func (s *Store) requireHealthy(ctx context.Context) error {
	health, err := s.Health(ctx)
	if err != nil {
		return err
	}
	if health.IntegrityFailure {
		return ErrIntegrity
	}
	if health.StorageFailure {
		return ErrStorage
	}
	return nil
}

func (s *Store) abortStorage(ctx context.Context, tx *sql.Tx, err error) error {
	_ = tx.Rollback()
	return s.storageError(ctx, err)
}

func (s *Store) storageError(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	s.storageFailure.Store(true)
	s.markStorageFailure(ctx)
	return wrapStorage(err)
}

func (s *Store) abortIntegrity(ctx context.Context, tx *sql.Tx) error {
	_ = tx.Rollback()
	s.setIntegrityFailure(ctx)
	return ErrIntegrity
}

func (s *Store) recordSaturation(ctx context.Context, tx *sql.Tx, category Category, now time.Time) error {
	column, reason := "general_saturated", ReasonGeneralSaturated
	if category == CategoryProtected {
		column, reason = "protected_saturated", ReasonProtectedSaturated
	}
	var wasSet int
	if err := tx.QueryRowContext(ctx, "SELECT "+column+" FROM health WHERE id=1").Scan(&wasSet); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE health SET "+column+"=1 WHERE id=1"); err != nil {
		return err
	}
	if wasSet != 0 {
		return nil
	}
	ok, err := capacityAvailable(ctx, tx, CategoryProtected, 1, now)
	if err != nil || !ok {
		return err
	}
	event := Event{
		At:       now,
		Actor:    authz.Actor{Kind: authz.ActorSystem, ID: "root"},
		Source:   "local",
		Action:   ActionAuditHealth,
		Result:   ResultFailure,
		Category: CategoryProtected,
		Metadata: map[string]string{MetadataReasonCode: reason, MetadataCategory: string(category)},
	}
	if _, err = insertEvent(ctx, tx, event); err != nil {
		return err
	}
	return pruneAfterAppend(ctx, tx, now)
}

func (s *Store) markStorageFailure(ctx context.Context) {
	_, _ = s.db.ExecContext(ctx, "UPDATE health SET storage_failure=1 WHERE id=1")
}
