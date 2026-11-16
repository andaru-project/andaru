package audit

import (
	"context"
	"database/sql"
	"time"

	"github.com/andaru-project/andaru/internal/authz"
)

func capacityAvailable(ctx context.Context, tx *sql.Tx, category Category, additional int64, now time.Time) (bool, error) {
	var events, reservations, expired int64
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM events WHERE category=?", category).Scan(&events); err != nil {
		return false, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM operations WHERE category=? AND state='pending'", category).Scan(&reservations); err != nil {
		return false, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM events e WHERE e.category=? AND e.at<?
		AND NOT EXISTS(SELECT 1 FROM operations o WHERE o.intent_event_id=e.id AND o.state='pending')`, category, now.Add(-retentionPeriod).UnixMilli()).Scan(&expired); err != nil {
		return false, err
	}
	budget := categoryBudget(category)
	return budget > 0 && events-expired+reservations+additional <= budget, nil
}

func pruneAfterAppend(ctx context.Context, tx *sql.Tx, now time.Time) error {
	cutoff := now.Add(-retentionPeriod).UnixMilli()
	categories := [2]Category{CategoryGeneral, CategoryProtected}
	var maxIDs, counts [2]int64
	pruned := false
	for i, category := range categories {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(max(id),0),count(*) FROM events e
			WHERE e.category=? AND e.at<?
			AND NOT EXISTS(SELECT 1 FROM operations o WHERE o.intent_event_id=e.id AND o.state='pending')`, category, cutoff).Scan(&maxIDs[i], &counts[i]); err != nil {
			return err
		}
		pruned = pruned || counts[i] > 0
	}
	if pruned {
		if _, err := tx.ExecContext(ctx, `UPDATE retention SET last_at=?,last_expired_general=?,last_expired_protected=?,last_count_pruned_general=0 WHERE id=1`,
			now.UnixMilli(), counts[0], counts[1]); err != nil {
			return err
		}
	}
	for i, category := range categories {
		if counts[i] == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE id IN (
			SELECT e.id FROM events e WHERE e.category=? AND e.at<?
			AND NOT EXISTS(SELECT 1 FROM operations o WHERE o.intent_event_id=e.id AND o.state='pending'))`, category, cutoff); err != nil {
			return err
		}
		query := `UPDATE retention SET watermark_id=max(watermark_id,?),
			total_pruned_general=total_pruned_general+? WHERE id=1`
		args := []any{maxIDs[i], counts[i]}
		if category == CategoryProtected {
			query = `UPDATE retention SET watermark_id=max(watermark_id,?),
				total_pruned_protected=total_pruned_protected+? WHERE id=1`
		}
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM operations WHERE state!='pending' AND updated_at<?`, cutoff)
	return err
}

func clearSaturation(ctx context.Context, tx *sql.Tx, category Category) error {
	column := "general_saturated"
	if category == CategoryProtected {
		column = "protected_saturated"
	}
	_, err := tx.ExecContext(ctx, "UPDATE health SET "+column+"=0 WHERE id=1")
	return err
}

func (s *Store) Retention(ctx context.Context) (RetentionStatus, error) {
	var status RetentionStatus
	var watermark, totalGeneral, totalProtected, lastAt, expiredGeneral, expiredProtected, countPruned int64
	err := s.db.QueryRowContext(ctx, `SELECT watermark_id,total_pruned_general,total_pruned_protected,last_at,
		last_expired_general,last_expired_protected,last_count_pruned_general FROM retention WHERE id=1`).
		Scan(&watermark, &totalGeneral, &totalProtected, &lastAt, &expiredGeneral, &expiredProtected, &countPruned)
	if err != nil {
		return status, s.storageError(ctx, err)
	}
	if watermark < 0 || totalGeneral < 0 || totalProtected < 0 || lastAt < 0 || expiredGeneral < 0 || expiredProtected < 0 || countPruned < 0 {
		return status, ErrIntegrity
	}
	status.Watermark = uint64(watermark)
	status.TotalGeneralPruned = uint64(totalGeneral)
	status.TotalProtectedPruned = uint64(totalProtected)
	status.LastPruned = PruneSummary{
		Watermark:          status.Watermark,
		ExpiredGeneral:     uint64(expiredGeneral),
		ExpiredProtected:   uint64(expiredProtected),
		CountPrunedGeneral: uint64(countPruned),
	}
	if lastAt != 0 {
		status.LastPruned.At = time.UnixMilli(lastAt).UTC()
	}
	return status, nil
}

func (s *Store) Health(ctx context.Context) (Health, error) {
	var general, protected, storage, integrity int
	err := s.db.QueryRowContext(ctx, `SELECT general_saturated,protected_saturated,storage_failure,integrity_failure FROM health WHERE id=1`).
		Scan(&general, &protected, &storage, &integrity)
	if err != nil {
		return Health{}, s.storageError(ctx, err)
	}
	if (general|protected|storage|integrity)&^1 != 0 {
		return Health{}, ErrIntegrity
	}
	return Health{
		GeneralSaturated:   general == 1,
		ProtectedSaturated: protected == 1,
		StorageFailure:     storage == 1 || s.storageFailure.Load(),
		IntegrityFailure:   integrity == 1,
	}, nil
}

func (s *Store) Verify(ctx context.Context) (IntegrityReport, error) {
	report := IntegrityReport{Valid: true, SQLiteOK: true}
	rows, err := s.db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return report, s.storageError(ctx, err)
	}
	checks := 0
	for rows.Next() {
		var result string
		if err = rows.Scan(&result); err != nil {
			rows.Close()
			return report, s.storageError(ctx, err)
		}
		checks++
		if result != "ok" {
			report.SQLiteOK = false
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return report, s.storageError(ctx, err)
	}
	if err = rows.Close(); err != nil {
		return report, s.storageError(ctx, err)
	}
	if checks == 0 {
		report.SQLiteOK = false
	}
	status, err := s.Retention(ctx)
	if err != nil {
		return report, err
	}
	report.PrunedThrough = status.Watermark
	rows, err = s.db.QueryContext(ctx, `SELECT e.id,e.at,e.actor_kind,e.actor_id,e.source,e.action,e.target,e.result,e.operation_id,e.category,e.coalesced_count,m.key,m.value
		FROM events e LEFT JOIN event_metadata m ON m.event_id=e.id ORDER BY e.id,m.key`)
	if err != nil {
		return report, s.storageError(ctx, err)
	}
	var current Event
	metadata := map[string]string{}
	expected := status.Watermark + 1
	flush := func() {
		if current.ID == 0 {
			return
		}
		if current.ID > status.Watermark {
			if current.ID != expected {
				report.Gaps = append(report.Gaps, IDGap{After: expected - 1, Before: current.ID})
			}
			expected = current.ID + 1
		}
		current.Metadata = metadata
		if !validStoredEvent(current) {
			report.InvalidEvents++
		}
		report.EventCount++
	}
	for rows.Next() {
		var event Event
		var id, at, count int64
		var actorKind, action, result, category string
		var key, value sql.NullString
		if err = rows.Scan(&id, &at, &actorKind, &event.Actor.ID, &event.Source, &action, &event.Target, &result, &event.OperationID, &category, &count, &key, &value); err != nil {
			rows.Close()
			return report, s.storageError(ctx, err)
		}
		if id <= 0 || count <= 0 {
			report.InvalidEvents++
			continue
		}
		event.ID = uint64(id)
		event.At = time.UnixMilli(at).UTC()
		event.Actor.Kind = authz.ActorKind(actorKind)
		event.Action = Action(action)
		event.Result = Result(result)
		event.Category = Category(category)
		event.Count = uint64(count)
		if current.ID != event.ID {
			flush()
			current = event
			metadata = map[string]string{}
		}
		if key.Valid {
			metadata[key.String] = value.String
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return report, s.storageError(ctx, err)
	}
	if err = rows.Close(); err != nil {
		return report, s.storageError(ctx, err)
	}
	flush()
	var sequence int64
	if err = s.db.QueryRowContext(ctx, "SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name='events'),0)").Scan(&sequence); err != nil {
		return report, s.storageError(ctx, err)
	}
	if sequence < 0 {
		report.InvalidEvents++
	} else if uint64(sequence) > status.Watermark && uint64(sequence) >= expected {
		report.Gaps = append(report.Gaps, IDGap{After: expected - 1, Before: uint64(sequence) + 1})
	}
	var pending int64
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM operations WHERE state='pending'").Scan(&pending); err != nil {
		return report, s.storageError(ctx, err)
	}
	if pending < 0 {
		report.InvalidEvents++
	} else {
		report.PendingOperations = uint64(pending)
	}
	report.Valid = report.SQLiteOK && len(report.Gaps) == 0 && report.InvalidEvents == 0
	if !report.Valid {
		s.setIntegrityFailure(ctx)
	}
	return report, nil
}

func validStoredEvent(event Event) bool {
	if event.ID == 0 || event.Count == 0 || event.At.UnixMilli() < 0 || event.At.After(time.Now().Add(5*time.Minute)) ||
		(event.Result == ResultPending && event.OperationID == "") ||
		validateFields(event.Actor, event.Source, event.Action, event.Target, event.Category) != nil ||
		!validResult(event.Result) || !validOperationIDOptional(event.OperationID) || validateMetadata(event.Metadata) != nil {
		return false
	}
	return true
}

func (s *Store) setIntegrityFailure(ctx context.Context) {
	_, _ = s.db.ExecContext(ctx, "UPDATE health SET integrity_failure=1 WHERE id=1")
}
