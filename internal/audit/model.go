package audit

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/andaru-project/andaru/internal/authz"
)

const (
	LayoutVersion        = 2
	generalBudget        = 80_000
	protectedBudget      = 20_000
	maxEvents            = generalBudget + protectedBudget
	retentionPeriod      = 365 * 24 * time.Hour
	coalesceWindow       = time.Minute
	maxEventPage         = 100
	maxMetadataEntries   = 8
	maxReceiptBytes      = 64 * 1024
	emergencyReceiptName = "audit-emergency.receipt"
)

const applicationID = 0x414e4441

var (
	ErrInvalid                = errors.New("invalid audit record")
	ErrStorage                = errors.New("audit storage unavailable")
	ErrGeneralSaturated       = errors.New("general audit capacity exhausted")
	ErrProtectedSaturated     = errors.New("protected audit capacity exhausted")
	ErrOperationInFlight      = errors.New("operation is still in flight")
	ErrOperationConflict      = errors.New("operation ID conflicts with recorded request")
	ErrOperationMismatch      = errors.New("authoritative operation does not match audit intent")
	ErrOperationSource        = errors.New("authoritative operation source unavailable")
	ErrIntegrity              = errors.New("audit integrity check failed")
	ErrReceiptFull            = errors.New("emergency audit receipt is full")
	ErrReconciliationRequired = errors.New("audit reconciliation is required before mutation")
	ErrReconciliationComplete = errors.New("audit reconciliation is only allowed before mutation")
)

type Category string

type Action string

type Result string

const (
	CategoryGeneral   Category = "general"
	CategoryProtected Category = "protected"
)

const (
	ActionLoginSuccess           Action = "login.success"
	ActionLoginFailure           Action = "login.failure"
	ActionLoginBackoff           Action = "login.backoff"
	ActionSessionLogout          Action = "session.logout"
	ActionSessionRevoked         Action = "session.revoked"
	ActionPermissionDenied       Action = "permission.denied"
	ActionAccountCreated         Action = "account.created"
	ActionAccountUpdated         Action = "account.updated"
	ActionAccountDisabled        Action = "account.disabled"
	ActionAccountDeleted         Action = "account.deleted"
	ActionRoleChanged            Action = "role.changed"
	ActionPasswordChanged        Action = "password.changed"
	ActionTokenCreated           Action = "token.created"
	ActionTokenRevoked           Action = "token.revoked"
	ActionSecretExported         Action = "secret.exported"
	ActionConfigurationSaved     Action = "configuration.saved"
	ActionConfigurationApplied   Action = "configuration.applied"
	ActionConfigurationConfirmed Action = "configuration.confirmed"
	ActionConfigurationRollback  Action = "configuration.rollback"
	ActionBackupRestored         Action = "backup.restored"
	ActionSetupCompleted         Action = "setup.completed"
	ActionRecoveryCompleted      Action = "recovery.completed"
	ActionAuditQueried           Action = "audit.queried"
	ActionAuditHealth            Action = "audit.health"
	ActionAuditRetentionPruned   Action = "audit.retention_pruned"
)

const (
	ResultPending      Result = "pending"
	ResultSuccess      Result = "success"
	ResultFailure      Result = "failure"
	ResultDenied       Result = "denied"
	ResultCommitted    Result = "committed"
	ResultNotCommitted Result = "not_committed"
	ResultUnavailable  Result = "unavailable"
	ResultRecovered    Result = "recovered"
	ResultRolledBack   Result = "rolled_back"
)

const (
	MetadataReasonCode         = "reasonCode"
	MetadataChanged            = "changed"
	MetadataScopeCount         = "scopeCount"
	MetadataSecretUnavailable  = "secretUnavailable"
	MetadataCategory           = "category"
	MetadataExpiredGeneral     = "expiredGeneral"
	MetadataExpiredProtected   = "expiredProtected"
	MetadataCountPrunedGeneral = "countPrunedGeneral"
	MetadataWatermark          = "watermark"
)

const (
	ReasonInvalidCredentials = "invalid_credentials"
	ReasonAccountDisabled    = "account_disabled"
	ReasonRateLimited        = "rate_limited"
	ReasonPermissionDenied   = "permission_denied"
	ReasonStorageFailure     = "storage_failure"
	ReasonIntegrityFailure   = "integrity_failure"
	ReasonGeneralSaturated   = "general_saturated"
	ReasonProtectedSaturated = "protected_saturated"
	ReasonNotCommitted       = "not_committed"
)

type Event struct {
	ID          uint64
	At          time.Time
	Actor       authz.Actor
	Source      string
	Action      Action
	Target      string
	Result      Result
	OperationID string
	Category    Category
	Count       uint64
	Metadata    map[string]string
}

type Intent struct {
	OperationID string
	Actor       authz.Actor
	Source      string
	Action      Action
	Target      string
	RequestHash string
	Category    Category
}

type Outcome struct {
	OperationID string
	Result      Result
	Metadata    map[string]string
}

type RecordedOperation struct {
	OperationID string
	Started     bool
	Pending     bool
	Replay      bool
	Outcome     *Outcome
}

type OperationRecord struct {
	OperationID string
	RequestHash string
	Actor       authz.Actor
	Source      string
	Action      Action
	Target      string
	Category    Category
	Outcome     Outcome
}

// OperationSource returns only committed authoritative results; found=false means no mutation committed.
type OperationSource interface {
	LookupOperation(ctx context.Context, operationID string) (OperationRecord, bool, error)
}

type PruneSummary struct {
	ExpiredGeneral     uint64
	ExpiredProtected   uint64
	CountPrunedGeneral uint64
	Watermark          uint64
	At                 time.Time
}

type RetentionStatus struct {
	Watermark            uint64
	TotalGeneralPruned   uint64
	TotalProtectedPruned uint64
	LastPruned           PruneSummary
}

type Health struct {
	GeneralSaturated   bool
	ProtectedSaturated bool
	StorageFailure     bool
	IntegrityFailure   bool
}

type IDGap struct {
	After  uint64
	Before uint64
}

type IntegrityReport struct {
	Valid             bool
	SQLiteOK          bool
	PrunedThrough     uint64
	EventCount        uint64
	Gaps              []IDGap
	InvalidEvents     uint64
	PendingOperations uint64
}

type actionRule struct {
	category  Category
	coalesces bool
	safety    bool
}

var actionRules = map[Action]actionRule{
	ActionLoginSuccess:           {category: CategoryGeneral},
	ActionLoginFailure:           {category: CategoryGeneral, coalesces: true},
	ActionLoginBackoff:           {category: CategoryGeneral},
	ActionSessionLogout:          {category: CategoryGeneral},
	ActionSessionRevoked:         {category: CategoryGeneral},
	ActionPermissionDenied:       {category: CategoryGeneral},
	ActionAccountCreated:         {category: CategoryProtected},
	ActionAccountUpdated:         {category: CategoryProtected},
	ActionAccountDisabled:        {category: CategoryProtected},
	ActionAccountDeleted:         {category: CategoryProtected},
	ActionRoleChanged:            {category: CategoryProtected},
	ActionPasswordChanged:        {category: CategoryProtected},
	ActionTokenCreated:           {category: CategoryGeneral},
	ActionTokenRevoked:           {category: CategoryProtected},
	ActionSecretExported:         {category: CategoryProtected},
	ActionConfigurationSaved:     {category: CategoryGeneral},
	ActionConfigurationApplied:   {category: CategoryGeneral},
	ActionConfigurationConfirmed: {category: CategoryGeneral},
	ActionConfigurationRollback:  {category: CategoryProtected, safety: true},
	ActionBackupRestored:         {category: CategoryProtected},
	ActionSetupCompleted:         {category: CategoryProtected},
	ActionRecoveryCompleted:      {category: CategoryProtected, safety: true},
	ActionAuditQueried:           {category: CategoryGeneral},
	ActionAuditHealth:            {category: CategoryProtected},
	ActionAuditRetentionPruned:   {category: CategoryProtected},
}

func validateIntent(intent Intent) error {
	if !validOperationID(intent.OperationID) || !validRequestHash(intent.RequestHash) {
		return ErrInvalid
	}
	return validateFields(intent.Actor, intent.Source, intent.Action, intent.Target, intent.Category)
}

func validateFields(actor authz.Actor, source string, action Action, target string, category Category) error {
	rule, ok := actionRules[action]
	if !ok || category != rule.category || !validActor(actor) || !validSource(source) || !validTarget(action, target) {
		return ErrInvalid
	}
	if action == ActionLoginFailure && actor.Kind != authz.ActorSystem {
		return ErrInvalid
	}
	return nil
}

func validTarget(action Action, target string) bool {
	if target == "" {
		switch action {
		case ActionLoginFailure, ActionLoginBackoff, ActionPermissionDenied, ActionSecretExported,
			ActionSetupCompleted, ActionRecoveryCompleted, ActionAuditQueried, ActionAuditHealth, ActionAuditRetentionPruned:
			return true
		default:
			return false
		}
	}
	switch action {
	case ActionLoginSuccess, ActionAccountCreated, ActionAccountUpdated, ActionAccountDisabled, ActionAccountDeleted, ActionPasswordChanged:
		return accountIDPattern.MatchString(target)
	case ActionRoleChanged:
		return target == authz.AdministratorRoleID || target == authz.ReadOnlyRoleID || roleIDPattern.MatchString(target)
	case ActionSessionLogout, ActionSessionRevoked:
		return validSelector(target, 32)
	case ActionTokenCreated, ActionTokenRevoked:
		return validSelector(target, 16)
	case ActionConfigurationSaved, ActionConfigurationApplied, ActionConfigurationConfirmed, ActionConfigurationRollback:
		return len(target) <= 9 && canonicalPositiveUint(target)
	case ActionBackupRestored:
		return len(target) == 32 && strings.ToLower(target) == target && isHex(target)
	default:
		return false
	}
}

func validActor(actor authz.Actor) bool {
	switch actor.Kind {
	case authz.ActorSystem:
		return actor.ID == "root"
	case authz.ActorAccount:
		return accountIDPattern.MatchString(actor.ID)
	case authz.ActorToken:
		return validSelector(actor.ID, 16)
	default:
		return false
	}
}

func validSelector(value string, size int) bool {
	if len(value) != base64.RawURLEncoding.EncodedLen(size) {
		return false
	}
	selector, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(selector) == size && base64.RawURLEncoding.EncodeToString(selector) == value
}

func isHex(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

func canonicalPositiveUint(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	return canonicalUint(value)
}

var (
	accountIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	roleIDPattern    = regexp.MustCompile(`^rol_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

func validateEvent(event Event, now time.Time, safety bool) (Event, error) {
	rule := actionRules[event.Action]
	if event.ID != 0 || event.Count != 0 || event.Result == ResultPending ||
		event.Action == ActionLoginFailure && event.OperationID != "" ||
		validateFields(event.Actor, event.Source, event.Action, event.Target, event.Category) != nil ||
		!validResult(event.Result) || !validOperationIDOptional(event.OperationID) || validateMetadata(event.Metadata) != nil {
		return Event{}, ErrInvalid
	}
	if rule.safety != safety {
		return Event{}, ErrInvalid
	}
	if event.At.IsZero() {
		event.At = now
	} else {
		event.At = event.At.UTC()
		if event.At.Before(now.Add(-retentionPeriod)) || event.At.After(now.Add(5*time.Minute)) {
			return Event{}, ErrInvalid
		}
	}
	event.At = time.UnixMilli(event.At.UnixMilli()).UTC()
	return event, nil
}

func validateOutcome(outcome Outcome, action Action) error {
	if !validOperationID(outcome.OperationID) || !validResult(outcome.Result) || outcome.Result == ResultPending || validateMetadata(outcome.Metadata) != nil {
		return ErrInvalid
	}
	if _, ok := actionRules[action]; !ok {
		return ErrInvalid
	}
	return nil
}

func validateMetadata(metadata map[string]string) error {
	if len(metadata) > maxMetadataEntries {
		return ErrInvalid
	}
	for key, value := range metadata {
		if !safeText(value, 64) {
			return ErrInvalid
		}
		switch key {
		case MetadataReasonCode:
			if !validReason(value) {
				return ErrInvalid
			}
		case MetadataChanged, MetadataSecretUnavailable:
			if value != "true" && value != "false" {
				return ErrInvalid
			}
		case MetadataScopeCount, MetadataExpiredGeneral, MetadataExpiredProtected, MetadataCountPrunedGeneral, MetadataWatermark:
			if !canonicalUint(value) {
				return ErrInvalid
			}
		case MetadataCategory:
			if value != string(CategoryGeneral) && value != string(CategoryProtected) {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	}
	return nil
}

func validSource(source string) bool {
	if len(source) > 128 {
		return false
	}
	if source == "unix" || source == "local" {
		return true
	}
	if strings.HasPrefix(source, "unix:uid:") {
		return canonicalUint(strings.TrimPrefix(source, "unix:uid:"))
	}
	_, err := netip.ParseAddr(source)
	return err == nil
}

func validRequestHash(hash string) bool {
	if len(hash) != 64 || strings.ToLower(hash) != hash {
		return false
	}
	_, err := hex.DecodeString(hash)
	return err == nil
}

func validOperationID(id string) bool {
	return id != "" && validOperationIDOptional(id)
}

func validOperationIDOptional(id string) bool {
	return id == "" || safeText(id, 128)
}

func safeText(value string, max int) bool {
	if len(value) > max || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:@%/-", r)) {
			return false
		}
	}
	return true
}

func validResult(result Result) bool {
	switch result {
	case ResultPending, ResultSuccess, ResultFailure, ResultDenied, ResultCommitted, ResultNotCommitted, ResultUnavailable, ResultRecovered, ResultRolledBack:
		return true
	default:
		return false
	}
}

func validReason(value string) bool {
	switch value {
	case ReasonInvalidCredentials, ReasonAccountDisabled, ReasonRateLimited, ReasonPermissionDenied, ReasonStorageFailure, ReasonIntegrityFailure, ReasonGeneralSaturated, ReasonProtectedSaturated, ReasonNotCommitted:
		return true
	default:
		return false
	}
}

func canonicalUint(value string) bool {
	if value == "0" {
		return true
	}
	if value == "" || value[0] == '0' {
		return false
	}
	for _, b := range value {
		if b < '0' || b > '9' {
			return false
		}
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil
}

func uintString(value uint64) string { return strconv.FormatUint(value, 10) }

func categoryBudget(category Category) int64 {
	if category == CategoryGeneral {
		return generalBudget
	}
	if category == CategoryProtected {
		return protectedBudget
	}
	return 0
}

func saturationError(category Category) error {
	if category == CategoryProtected {
		return ErrProtectedSaturated
	}
	return ErrGeneralSaturated
}

func wrapStorage(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %v", ErrStorage, err)
}
