package identity

import (
	"context"
	"errors"
)

const (
	LayoutVersion = 1
	MaxAccounts = 10_000
)


var (
	ErrClosed = errors.New("identity store is closed")
	ErrNotFound = errors.New("identity account not found")
	ErrConflict = errors.New("identity revision conflict")
	ErrInvalid = errors.New("invalid identity account")
	ErrInvariant = errors.New("identity administrator invariant violated")
	ErrAccountLimit = errors.New("identity account limit reached")
	ErrDuplicate = errors.New("identity account already exists")
)

type AccountKind string
const (
	Human AccountKind = "human"
	Service AccountKind = "service"
)

type Role string
const Administrator Role = "administrator"

type Account struct {
	ID string
	Login string
	DisplayName string
	Kind AccountKind
	Enabled bool
	CreatedAt int64
	UpdatedAt int64
}

type CreateAccountInput struct {
	Login string
	DisplayName string
	Kind AccountKind
	Enabled bool
}

type UpdateAccountInput struct {
	Login *string
	DisplayName *string
	Kind *AccountKind
	Enabled *bool
}

type CredentialInput struct { Password []byte }
type TokenInput struct { Name string; Scopes []string }
type SessionInput struct { AccountID string }

// Store owns the private identity database.
type Store interface {
	Close() error
	Revision(context.Context) (int64, error)
	BootstrapAdmin(context.Context, string, string, []byte) (Account, int64, error)
	CreateAccount(context.Context, CreateAccountInput, int64) (Account, int64, error)
	UpdateAccount(context.Context, string, int64, UpdateAccountInput) (Account, int64, error)
	SetPassword(context.Context, string, int64, []byte) (int64, error)
	GetAccount(context.Context, string) (Account, error)
	ListAccounts(context.Context) ([]Account, error)
	DeleteAccount(context.Context, string, int64) error
	DisableAccount(context.Context, string, int64) error
	ValidateAdministratorInvariant(context.Context) error
	VerifyPassword(context.Context, string, []byte) (bool, error)
}
 
