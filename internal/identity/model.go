package identity

import (
	"context"
	"errors"
	"time"

	"github.com/andaru-project/andaru/internal/authz"
)

const (
	LayoutVersion       = 2
	MaxAccounts         = 10_000
	MaxRoleAssignments  = 10_002
	MaxAccountPageSize  = 100
	MaxTokenPageSize    = 50
	MaxLoginLimitRows   = 10_000
	MaxSourceLimitRows  = 50_000
	MaxTokensPerAccount = 50
	MaxTokenIssuesYear  = 100
)

var (
	ErrClosed         = errors.New("identity store is closed")
	ErrNotFound       = errors.New("identity account not found")
	ErrConflict       = errors.New("identity revision conflict")
	ErrInvalid        = errors.New("invalid identity account")
	ErrInvariant      = errors.New("identity administrator invariant violated")
	ErrAccountLimit   = errors.New("identity account limit reached")
	ErrDuplicate      = errors.New("identity account already exists")
	ErrAuthentication = errors.New("identity authentication failed")
	ErrRateLimited    = errors.New("identity login temporarily limited")
	ErrUnauthorized   = errors.New("identity operation not authorized")
	ErrTokenLimit     = errors.New("identity token limit reached")
)

type AccountKind string

const (
	Human   AccountKind = "human"
	Service AccountKind = "service"
)

type Role string

const Administrator Role = "administrator"

type Account struct {
	ID          string
	Login       string
	DisplayName string
	Kind        AccountKind
	Enabled     bool
	RoleIDs     []string
	CreatedAt   int64
	UpdatedAt   int64
}
type AccountPage struct {
	Items      []Account
	NextCursor string
}
type TokenPage struct {
	Items      []Token
	NextCursor string
}

type CreateAccountInput struct {
	Login       string
	DisplayName string
	Kind        AccountKind
	Enabled     bool
	RoleIDs     []string
}

type UpdateAccountInput struct {
	Login       *string
	DisplayName *string
	Kind        *AccountKind
	Enabled     *bool
	RoleIDs     *[]string
}

type CredentialInput struct{ Password []byte }

type TokenInput struct {
	Name   string
	Scopes []string
}

type SessionInput struct{ AccountID string }

type TokenIssue struct {
	AccountID              string
	Name                   string
	Scope                  []authz.Capability
	ExpiresAt              *time.Time
	NoExpiry               bool
	RevokeOnPasswordChange *bool
}

type Token struct {
	Selector               string
	AccountID              string
	Name                   string
	Scope                  []authz.Capability
	CreatedAt              time.Time
	ExpiresAt              *time.Time
	NoExpiry               bool
	RevokedAt              *time.Time
	LastUsedAt             *time.Time
	RevokeOnPasswordChange bool
	PasswordEpoch          uint64
	Revision               uint64
}

type IssuedToken struct {
	Selector string
	Secret   string
	Metadata Token
}

type LoginResult struct {
	Account       Account
	SessionCookie string
	CSRFSecret    string
	ExpiresAt     time.Time
}

type Session struct {
	Selector   string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

type Principal struct {
	Account            Account
	Actor              authz.Actor
	Capabilities       []authz.Capability
	Token              *Token
	SessionSelector    string
	AuthorizationEpoch uint64
	SessionExpiresAt    time.Time
}

// OperationIntent binds a state-changing transaction to a verified actor and audit intent.
type OperationIntent struct {
	OperationID string
	RequestHash string
	Actor       authz.Actor
	Source      string
	Action      string
	Target      string
	Category    string
}

// OperationRecord is the durable, credential-free result of an identity transaction.
type OperationRecord struct {
	OperationID   string
	RequestHash   string
	Actor         authz.Actor
	Source        string
	Action        string
	Target        string
	OutcomeTarget string
	Category      string
	Revision      int64
	Result        string
	Metadata      map[string]string
}

// Store owns the private identity database.
type Store interface {
	Close() error
	Revision(context.Context) (int64, error)
	HashRequest(context.Context, []byte) (string, error)
	LookupOperation(context.Context, string) (OperationRecord, bool, error)
	BootstrapAdmin(context.Context, string, string, []byte) (Account, int64, error)
	CreateAccount(context.Context, CreateAccountInput, int64) (Account, int64, error)
	UpdateAccount(context.Context, string, int64, UpdateAccountInput) (Account, int64, error)
	SetPassword(context.Context, string, int64, []byte) (int64, error)
	GetAccount(context.Context, string) (Account, error)
	ListAccounts(context.Context, string, int) (AccountPage, error)
	DeleteAccount(context.Context, string, int64) error
	DisableAccount(context.Context, string, int64) error
	ValidateAdministratorInvariant(context.Context) error
	VerifyPassword(context.Context, string, []byte) (bool, error)
	IssueToken(context.Context, authz.Actor, []authz.Capability, []authz.Capability, TokenIssue) (IssuedToken, error)
	IssueTokenAtRevision(context.Context, int64, authz.Actor, []authz.Capability, []authz.Capability, TokenIssue) (IssuedToken, error)
	ListTokens(context.Context, string) ([]Token, error)
	GetToken(context.Context, string) (Token, error)
	ListTokensPage(context.Context, string, string, int) (TokenPage, error)
	RevokeToken(context.Context, string) error
	AuthenticatePassword(context.Context, string, string, string, uint64) (LoginResult, error)
	AuthenticatePasswordWithIntent(context.Context, string, string, string, uint64, func(Account) error) (LoginResult, error)
	AuthenticateSession(context.Context, string, string, uint64, bool) (Principal, error)
	RotateCSRF(context.Context, string, string, uint64) (string, error)
	AuthenticateToken(context.Context, string, string, authz.Snapshot) (Principal, error)
	// ValidateCSRF checks a synchronizer token after AuthenticateSession validates its epochs.
	ValidateCSRF(context.Context, string, string, string, string) error
	Logout(context.Context, string, string) error
	LogoutWithIntent(context.Context, string, string, func(string) error) error
	ListSessions(context.Context, string, uint64) ([]Session, error)
	RevokeSession(context.Context, string, string) error
	RevokeSessionWithIntent(context.Context, string, string, func() error) error
	RevokeAllSessions(context.Context) (int64, error)
}
