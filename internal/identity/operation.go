package identity

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"github.com/andaru-project/andaru/internal/authz"
)

type operationContextKey struct{}

type operationState struct {
	intent OperationIntent
	target string
}

var (
	operationIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:@%/-]{1,128}$`)
	operationHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	operationAccountIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

// WithOperation carries an audit intent into the identity transaction that commits it.
func WithOperation(ctx context.Context, intent OperationIntent) context.Context {
	return context.WithValue(ctx, operationContextKey{}, &operationState{intent: intent, target: intent.Target})
}

// HashRequest uses the private per-store key so low-entropy request secrets are not dictionary-checkable.
func (s *store) HashRequest(ctx context.Context, body []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, s.limiterKey[:])
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func setOperationTarget(ctx context.Context, target string) {
	if state, ok := ctx.Value(operationContextKey{}).(*operationState); ok {
		state.target = target
	}
}

func validateOperationIntent(intent OperationIntent) error {
	if !operationIDPattern.MatchString(intent.OperationID) || !operationHashPattern.MatchString(intent.RequestHash) || !validOperationActor(intent.Actor) || !validOperationSource(intent.Source) {
		return ErrInvalid
	}
	category := "protected"
	switch intent.Action {
	case "account.created":
		if intent.Target != "" {
			return ErrInvalid
		}
	case "account.updated", "account.disabled", "account.deleted", "role.changed", "password.changed":
		if !operationAccountIDPattern.MatchString(intent.Target) {
			return ErrInvalid
		}
	case "token.created":
		category = "general"
		if intent.Target != "" {
			return ErrInvalid
		}
	case "token.revoked":
		if _, err := decodeTokenSelector(intent.Target); err != nil {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	if intent.Category != category || len(intent.Target) > 128 {
		return ErrInvalid
	}
	return nil
}

func validOperationTarget(action, target string) bool {
	switch action {
	case "account.created":
		return operationAccountIDPattern.MatchString(target)
	case "token.created":
		_, err := decodeTokenSelector(target)
		return err == nil
	default:
		return target == ""
	}
}

func validOperationActor(actor authz.Actor) bool {
	switch actor.Kind {
	case authz.ActorSystem:
		return actor.ID == "root"
	case authz.ActorAccount:
		return operationAccountIDPattern.MatchString(actor.ID)
	case authz.ActorToken:
		_, err := decodeTokenSelector(actor.ID)
		return err == nil
	default:
		return false
	}
}

func validOperationSource(source string) bool {
	if source == "unix" || source == "local" {
		return true
	}
	if strings.HasPrefix(source, "unix:uid:") {
		value := strings.TrimPrefix(source, "unix:uid:")
		return value == "0" || value != "" && value[0] != '0' && strings.Trim(value, "0123456789") == ""
	}
	address, err := netip.ParseAddr(source)
	return err == nil && address.String() == source
}

func persistOperation(ctx context.Context, tx *sql.Tx, revision int64) error {
	state, ok := ctx.Value(operationContextKey{}).(*operationState)
	if !ok {
		return nil
	}
	intent := state.intent
	if err := validateOperationIntent(intent); err != nil {
		return err
	}
	target := ""
	if intent.Action == "account.created" || intent.Action == "token.created" {
		target = state.target
	} else if state.target != intent.Target {
		return ErrInvalid
	}
	if !validOperationTarget(intent.Action, target) {
		return ErrInvalid
	}
	requestHash, err := hex.DecodeString(intent.RequestHash)
	if err != nil || len(requestHash) != sha256.Size {
		return ErrInvalid
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO operations(operation_id,idempotency_key,request_hash,revision,created_at,actor_kind,actor_id,source,action,target,outcome_target,category,result,metadata)
		VALUES(?,?,?,?,unixepoch(),?,?,?,?,?,?,?,'success','{}')`, intent.OperationID, intent.OperationID, requestHash, revision,
		intent.Actor.Kind, intent.Actor.ID, intent.Source, intent.Action, intent.Target, target, intent.Category)
	if err != nil {
		return fmt.Errorf("record identity operation: %w", err)
	}
	return nil
}

func (s *store) LookupOperation(ctx context.Context, operationID string) (OperationRecord, bool, error) {
	if !operationIDPattern.MatchString(operationID) {
		return OperationRecord{}, false, ErrInvalid
	}
	var record OperationRecord
	var idempotencyKey, actorKind, actorID, action, target, outcomeTarget, category, result, encodedMetadata string
	var requestHash []byte
	err := s.db.QueryRowContext(ctx, `SELECT operation_id,idempotency_key,request_hash,revision,actor_kind,actor_id,source,action,target,outcome_target,category,result,metadata
		FROM operations WHERE operation_id=?`, operationID).Scan(&record.OperationID, &idempotencyKey, &requestHash, &record.Revision,
		&actorKind, &actorID, &record.Source, &action, &target, &outcomeTarget, &category, &result, &encodedMetadata)
	if errors.Is(err, sql.ErrNoRows) {
		return OperationRecord{}, false, nil
	}
	if err != nil {
		return OperationRecord{}, false, err
	}
	if idempotencyKey != record.OperationID || len(requestHash) != sha256.Size || record.Revision < 1 {
		return OperationRecord{}, false, errors.New("invalid stored identity operation")
	}
	record.RequestHash = hex.EncodeToString(requestHash)
	record.Actor = authz.Actor{Kind: authz.ActorKind(actorKind), ID: actorID}
	record.Action, record.Target, record.OutcomeTarget, record.Category, record.Result = action, target, outcomeTarget, category, result
	if err = json.Unmarshal([]byte(encodedMetadata), &record.Metadata); err != nil || record.Metadata == nil {
		return OperationRecord{}, false, errors.New("invalid stored identity operation metadata")
	}
	if record.Result != "success" || validateOperationIntent(OperationIntent{
		OperationID: record.OperationID, RequestHash: record.RequestHash, Actor: record.Actor,
		Source: record.Source, Action: record.Action, Target: record.Target, Category: record.Category,
	}) != nil || !validOperationTarget(record.Action, record.OutcomeTarget) || len(record.Metadata) != 0 {
		return OperationRecord{}, false, errors.New("invalid stored identity operation")
	}
	return record, true, nil
}

func migrateIdentityV1(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM operations").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("cannot migrate identity operations without authoritative outcomes")
	}
	if _, err = tx.ExecContext(ctx, "DROP TABLE operations"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, operationTableDDL); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", LayoutVersion)); err != nil {
		return err
	}
	return tx.Commit()
}
