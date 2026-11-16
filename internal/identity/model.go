package identity

import (
	"context"
	"errors"
)

const (
	LayoutVersion = 1
	MaxAccounts = 10_000
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

type CredentialInput struct {
	Password []byte
}

type TokenInput struct {
	Name string
	Scopes []string
}

type SessionInput struct {
	AccountID string
}

var ErrClosed = errors.New("identity store is closed")

// Store owns the private identity database.
type Store interface {
	Close() error
	Revision(context.Context) (int64, error)
}
