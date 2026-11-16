package audit

import (
	"context"
	"errors"

	"github.com/andaru-project/andaru/internal/authz"
)

const reconcileBatch = 100

type pendingOperation struct {
	intent Intent
}

func (s *Store) Reconcile(ctx context.Context, source OperationSource) error {
	if source == nil {
		return ErrInvalid
	}
	s.startupMu.Lock()
	defer s.startupMu.Unlock()
	if !s.startupOpen.Load() {
		return ErrReconciliationComplete
	}
	if err := s.reconcile(ctx, source); err != nil {
		return err
	}
	s.reconciliationRequired = false
	s.startupOpen.Store(false)
	return nil
}

func (s *Store) reconcile(ctx context.Context, source OperationSource) error {
	cursor := ""

	for {
		rows, err := s.db.QueryContext(ctx, `SELECT operation_id,request_hash,actor_kind,actor_id,source,action,target,category
			FROM operations WHERE state='pending' AND operation_id>? ORDER BY operation_id LIMIT ?`, cursor, reconcileBatch)
		if err != nil {
			return s.storageError(ctx, err)
		}
		pending := make([]pendingOperation, 0, reconcileBatch)
		for rows.Next() {
			var intent Intent
			var actorKind, action, category string
			if err = rows.Scan(&intent.OperationID, &intent.RequestHash, &actorKind, &intent.Actor.ID, &intent.Source, &action, &intent.Target, &category); err != nil {
				rows.Close()
				return s.storageError(ctx, err)
			}
			intent.Actor.Kind = authz.ActorKind(actorKind)
			intent.Action = Action(action)
			intent.Category = Category(category)
			if validateIntent(intent) != nil {
				rows.Close()
				s.setIntegrityFailure(ctx)
				return ErrIntegrity
			}
			pending = append(pending, pendingOperation{intent: intent})
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return s.storageError(ctx, err)
		}
		if err = rows.Close(); err != nil {
			return s.storageError(ctx, err)
		}
		if len(pending) == 0 {
			return nil
		}
		for _, item := range pending {
			intent := item.intent
			cursor = intent.OperationID
			record, found, sourceErr := source.LookupOperation(ctx, intent.OperationID)
			if sourceErr != nil {
				return ErrOperationSource
			}
			if !found {
				if err = s.finish(ctx, Outcome{
					OperationID: intent.OperationID,
					Result:      ResultNotCommitted,
					Metadata:    map[string]string{MetadataReasonCode: ReasonNotCommitted},
				}); err != nil {
					return err
				}
				continue
			}
			if !matchesIntent(record, intent) {
				s.setIntegrityFailure(ctx)
				return ErrOperationMismatch
			}
			if err = s.finish(ctx, record.Outcome); err != nil {
				if errors.Is(err, ErrInvalid) {
					s.setIntegrityFailure(ctx)
					return ErrOperationMismatch
				}
				return err
			}
		}
	}
}

func matchesIntent(record OperationRecord, intent Intent) bool {
	if record.OperationID != intent.OperationID || record.RequestHash != intent.RequestHash || record.Actor != intent.Actor || record.Source != intent.Source || record.Action != intent.Action || record.Target != intent.Target || record.Category != intent.Category {
		return false
	}
	return record.Outcome.OperationID == intent.OperationID && record.Outcome.Result != ResultNotCommitted && validateIntent(Intent{
		OperationID: record.OperationID,
		RequestHash: record.RequestHash,
		Actor:       record.Actor,
		Source:      record.Source,
		Action:      record.Action,
		Target:      record.Target,
		Category:    record.Category,
	}) == nil && validateOutcome(record.Outcome, record.Action) == nil
}
