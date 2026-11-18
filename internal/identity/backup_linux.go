package identity

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"

	"github.com/andaru-project/andaru/internal/authz"
)

const maxBackupDocumentSize = 4 << 20

type BackupDocuments struct {
	IdentityRevision int64
	Users            json.RawMessage
	UserCredentials  json.RawMessage
}

type RestoreDocuments struct {
	Users, UserCredentials json.RawMessage
}

type RestoreCounts struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
	Deleted int `json:"deleted"`
}

type RecoveryAdministrator struct {
	ID    string `json:"id"`
	Login string `json:"login"`
}

type RestorePreview struct {
	ExpectedIdentityRevision int64
	UserChanges              RestoreCounts
	CredentialChanges        RestoreCounts
	MissingRoleIDs           []string
	RequiresRootAdministrator bool
	RecoveryAdministrators   []RecoveryAdministrator
}

type RestoreResult struct {
	IdentityRevision int64
}

type RootAdministrator struct {
	AccountID  string
	Login      string
	DisplayName string
	Password   []byte
}

type BackupStore interface {
	ExportBackup(context.Context, bool) (BackupDocuments, error)
	PreviewRestore(context.Context, RestoreDocuments) (RestorePreview, error)
	PrepareRestore(context.Context, int64, RestoreDocuments, *RootAdministrator) (*PreparedRestore, error)
}

type backupAccount struct {
	ID          string      `json:"id"`
	Login       string      `json:"login"`
	DisplayName string      `json:"displayName"`
	Kind        AccountKind `json:"kind"`
	Enabled     bool        `json:"enabled"`
	RoleIDs     []string    `json:"roleIds"`
	CreatedAt   int64       `json:"createdAt"`
	UpdatedAt   int64       `json:"updatedAt"`
}

type backupTombstone struct {
	AccountID string `json:"accountId"`
	DeletedAt int64  `json:"deletedAt"`
}

type usersBackupDocument struct {
	SchemaVersion    int               `json:"schemaVersion"`
	IdentityRevision int64             `json:"identityRevision"`
	Accounts         []backupAccount   `json:"accounts"`
	Tombstones       []backupTombstone `json:"tombstones"`
}

type backupPassword struct {
	AccountID       string `json:"accountId"`
	Salt            []byte `json:"salt"`
	Hash            []byte `json:"hash"`
	AlgorithmVersion int    `json:"algorithmVersion"`
	MemoryKiB       int     `json:"memoryKiB"`
	TimeCost        int     `json:"timeCost"`
	Parallelism     int     `json:"parallelism"`
	UpdatedAt       int64   `json:"updatedAt"`
}

type backupToken struct {
	Selector               []byte             `json:"selector"`
	AccountID              string             `json:"accountId"`
	SecretHash             []byte             `json:"secretHash"`
	Name                   string             `json:"name"`
	Scopes                 []authz.Capability `json:"scopes"`
	CreatedAt              int64              `json:"createdAt"`
	ExpiresAt              *int64             `json:"expiresAt"`
	RevokedAt              *int64             `json:"revokedAt"`
	LastUsedAt             *int64             `json:"lastUsedAt"`
	RevokeOnPasswordChange bool               `json:"revokeOnPasswordChange"`
}

type credentialsBackupDocument struct {
	SchemaVersion    int              `json:"schemaVersion"`
	IdentityRevision int64            `json:"identityRevision"`
	Passwords        []backupPassword `json:"passwords"`
	Tokens           []backupToken    `json:"tokens"`
}

func (s *store) ExportBackup(ctx context.Context, includeCredentials bool) (result BackupDocuments, err error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='revision'").Scan(&result.IdentityRevision); err != nil {
		return BackupDocuments{}, err
	}
	users, err := readUsersBackup(ctx, tx, result.IdentityRevision)
	if err != nil {
		return BackupDocuments{}, err
	}
	result.Users, err = json.Marshal(users)
	if err != nil || len(result.Users) > maxBackupDocumentSize {
		if err == nil {
			err = ErrAccountLimit
		}
		return BackupDocuments{}, err
	}
	if includeCredentials {
		credentials, readErr := readCredentialsBackup(ctx, tx, result.IdentityRevision)
		if readErr != nil {
			return BackupDocuments{}, readErr
		}
		result.UserCredentials, err = json.Marshal(credentials)
		if err != nil || len(result.UserCredentials) > maxBackupDocumentSize {
			if err == nil {
				err = ErrTokenLimit
			}
			return BackupDocuments{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return BackupDocuments{}, err
	}
	return result, nil
}

func ValidateBackupDocuments(input RestoreDocuments) error {
	if input.Users == nil && input.UserCredentials == nil {
		return nil
	}
	users, credentials, err := decodeRestoreDocuments(input)
	if err != nil {
		return err
	}
	if users == nil {
		return ErrInvalid
	}
	if credentials != nil {
		return validateCredentialBindings(*users, *credentials)
	}
	return nil
}

func readUsersBackup(ctx context.Context, q accountQueryer, revision int64) (usersBackupDocument, error) {
	document := usersBackupDocument{SchemaVersion: 1, IdentityRevision: revision, Accounts: []backupAccount{}, Tombstones: []backupTombstone{}}
	rows, err := q.QueryContext(ctx, "SELECT id,login,display_name,kind,enabled,created_at,updated_at FROM accounts ORDER BY id")
	if err != nil {
		return document, err
	}
	for rows.Next() {
		var account backupAccount
		var enabled int
		if err = rows.Scan(&account.ID, &account.Login, &account.DisplayName, &account.Kind, &enabled, &account.CreatedAt, &account.UpdatedAt); err != nil {
			rows.Close()
			return document, err
		}
		account.Enabled = enabled == 1
		account.RoleIDs, err = accountRoleIDs(ctx, q, account.ID)
		if err != nil {
			rows.Close()
			return document, err
		}
		document.Accounts = append(document.Accounts, account)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return document, err
	}
	if err = rows.Close(); err != nil {
		return document, err
	}
	rows, err = q.QueryContext(ctx, "SELECT account_id,deleted_at FROM tombstones ORDER BY account_id")
	if err != nil {
		return document, err
	}
	defer rows.Close()
	for rows.Next() {
		var tombstone backupTombstone
		if err = rows.Scan(&tombstone.AccountID, &tombstone.DeletedAt); err != nil {
			return document, err
		}
		document.Tombstones = append(document.Tombstones, tombstone)
	}
	return document, rows.Err()
}

func readCredentialsBackup(ctx context.Context, q accountQueryer, revision int64) (credentialsBackupDocument, error) {
	document := credentialsBackupDocument{SchemaVersion: 1, IdentityRevision: revision, Passwords: []backupPassword{}, Tokens: []backupToken{}}
	rows, err := q.QueryContext(ctx, `SELECT account_id,salt,hash,algorithm_version,memory_kib,time_cost,parallelism,updated_at FROM password_credentials ORDER BY account_id`)
	if err != nil {
		return document, err
	}
	for rows.Next() {
		var password backupPassword
		if err = rows.Scan(&password.AccountID, &password.Salt, &password.Hash, &password.AlgorithmVersion, &password.MemoryKiB, &password.TimeCost, &password.Parallelism, &password.UpdatedAt); err != nil {
			rows.Close()
			return document, err
		}
		document.Passwords = append(document.Passwords, password)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return document, err
	}
	if err = rows.Close(); err != nil {
		return document, err
	}
	rows, err = q.QueryContext(ctx, `SELECT selector,account_id,secret_hash,name,scopes,created_at,expires_at,revoked_at,last_used_at,revoke_on_password_change FROM api_tokens ORDER BY account_id,selector`)
	if err != nil {
		return document, err
	}
	defer rows.Close()
	for rows.Next() {
		var token backupToken
		var scopes string
		var expiry, revoked, used sql.NullInt64
		var revoke int
		if err = rows.Scan(&token.Selector, &token.AccountID, &token.SecretHash, &token.Name, &scopes, &token.CreatedAt, &expiry, &revoked, &used, &revoke); err != nil {
			return document, err
		}
		token.Scopes, err = decodeTokenScope(scopes)
		if err != nil {
			return document, err
		}
		token.ExpiresAt, token.RevokedAt, token.LastUsedAt = nullableInt(expiry), nullableInt(revoked), nullableInt(used)
		token.RevokeOnPasswordChange = revoke == 1
		document.Tokens = append(document.Tokens, token)
	}
	return document, rows.Err()
}

func nullableInt(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}


func missingRoles(document usersBackupDocument, known []string) []string {
	missing := make(map[string]bool)
	for _, account := range document.Accounts {
		for _, roleID := range account.RoleIDs {
			if roleID != authz.AdministratorRoleID && roleID != authz.ReadOnlyRoleID && !slices.Contains(known, roleID) {
				missing[roleID] = true
			}
		}
	}
	result := make([]string, 0, len(missing))
	for id := range missing {
		result = append(result, id)
	}
	slices.Sort(result)
	return result
}
