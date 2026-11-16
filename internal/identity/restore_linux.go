package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/jsonprofile"
	"github.com/google/uuid"
)

type PreparedRestore struct {
	tx          *sql.Tx
	preview     RestorePreview
	result      RestoreResult
	target      BackupDocuments
	closed      bool
	committed   bool
}

func (p *PreparedRestore) Preview() RestorePreview { return p.preview }
func (p *PreparedRestore) TargetDocuments() BackupDocuments { return p.target }

func (p *PreparedRestore) Publish(ctx context.Context) (RestoreResult, error) {
	if p == nil || p.closed || p.committed || ctx.Err() != nil {
		return RestoreResult{}, ErrClosed
	}
	if err := p.tx.Commit(); err != nil {
		return RestoreResult{}, err
	}
	p.committed = true
	return p.result, nil
}

func (p *PreparedRestore) Close() error {
	if p == nil || p.closed {
		return nil
	}
	p.closed = true
	if p.committed {
		return nil
	}
	return p.tx.Rollback()
}

func (s *store) PreviewRestore(ctx context.Context, input RestoreDocuments) (RestorePreview, error) {
	users, credentials, err := decodeRestoreDocuments(input)
	if err != nil {
		return RestorePreview{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RestorePreview{}, err
	}
	defer tx.Rollback()
	var revision int64
	if err = tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='revision'").Scan(&revision); err != nil {
		return RestorePreview{}, err
	}
	currentUsers, err := readUsersBackup(ctx, tx, revision)
	if err != nil {
		return RestorePreview{}, err
	}
	currentCredentials, err := readCredentialsBackup(ctx, tx, revision)
	if err != nil {
		return RestorePreview{}, err
	}
	candidateUsers, candidateCredentials, err := restoreCandidates(currentUsers, currentCredentials, users, credentials)
	if err != nil {
		return RestorePreview{}, err
	}
	preview := identityRestorePreview(revision, currentUsers, currentCredentials, candidateUsers, candidateCredentials)
	if err = tx.Commit(); err != nil {
		return RestorePreview{}, err
	}
	return preview, nil
}

func (s *store) PrepareRestore(ctx context.Context, expectedRevision int64, input RestoreDocuments, root *RootAdministrator) (*PreparedRestore, error) {
	users, credentials, err := decodeRestoreDocuments(input)
	if err != nil {
		return nil, err
	}
	tx, err := s.beginMutation(ctx, expectedRevision)
	if err != nil {
		return nil, err
	}
	prepared := &PreparedRestore{tx: tx}
	success := false
	defer func() {
		if !success {
			_ = prepared.Close()
		}
	}()
	currentUsers, err := readUsersBackup(ctx, tx, expectedRevision)
	if err != nil {
		return nil, err
	}
	currentCredentials, err := readCredentialsBackup(ctx, tx, expectedRevision)
	if err != nil {
		return nil, err
	}
	candidateUsers, candidateCredentials, err := restoreCandidates(currentUsers, currentCredentials, users, credentials)
	if err != nil {
		return nil, err
	}
	prepared.preview = identityRestorePreview(expectedRevision, currentUsers, currentCredentials, candidateUsers, candidateCredentials)
	if !hasRecoveryAdministrator(candidateUsers, candidateCredentials) {
		if root == nil {
			return nil, ErrInvariant
		}
		if err = addRootAdministrator(ctx, &candidateUsers, &candidateCredentials, root); err != nil {
			return nil, err
		}
	} else if root != nil {
		return nil, ErrInvalid
	}
	if err = validateCredentialBindings(candidateUsers, candidateCredentials); err != nil {
		return nil, err
	}
	nextRevision := expectedRevision + 1
	if nextRevision < 1 {
		return nil, ErrConflict
	}
	if err = applyRestore(ctx, tx, candidateUsers, candidateCredentials, users != nil, nextRevision); err != nil {
		return nil, err
	}
	if err = validateAdministratorInvariant(ctx, tx); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE metadata SET value=? WHERE key='revision'", nextRevision); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO revisions(revision,created_at) VALUES(?,unixepoch())", nextRevision); err != nil {
		return nil, err
	}
	if err = persistOperation(ctx, tx, nextRevision); err != nil {
		return nil, err
	}
	targetUsers, err := readUsersBackup(ctx, tx, nextRevision)
	if err != nil {
		return nil, err
	}
	targetCredentials, err := readCredentialsBackup(ctx, tx, nextRevision)
	if err != nil {
		return nil, err
	}
	prepared.target.IdentityRevision = nextRevision
	prepared.target.Users, err = json.Marshal(targetUsers)
	if err != nil {
		return nil, err
	}
	prepared.target.UserCredentials, err = json.Marshal(targetCredentials)
	if err != nil {
		return nil, err
	}
	prepared.result.IdentityRevision = nextRevision
	success = true
	return prepared, nil
}

func decodeRestoreDocuments(input RestoreDocuments) (*usersBackupDocument, *credentialsBackupDocument, error) {
	if input.Users == nil && input.UserCredentials == nil {
		return nil, nil, ErrInvalid
	}
	var users *usersBackupDocument
	if input.Users != nil {
		var document usersBackupDocument
		if err := decodeBackupJSON(input.Users, &document); err != nil || validateUsersBackup(document) != nil {
			return nil, nil, ErrInvalid
		}
		users = &document
	}
	var credentials *credentialsBackupDocument
	if input.UserCredentials != nil {
		var document credentialsBackupDocument
		if err := decodeBackupJSON(input.UserCredentials, &document); err != nil || validateCredentialsBackup(document) != nil {
			return nil, nil, ErrInvalid
		}
		credentials = &document
	}
	if users != nil && credentials != nil && users.IdentityRevision != credentials.IdentityRevision {
		return nil, nil, ErrConflict
	}
	return users, credentials, nil
}

func decodeBackupJSON(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > maxBackupDocumentSize || !jsonprofile.Valid(raw, false) {
		return ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return ErrInvalid
	}
	return nil
}

func validateUsersBackup(document usersBackupDocument) error {
	if document.SchemaVersion != 1 || document.IdentityRevision < 0 || document.Accounts == nil || document.Tombstones == nil || len(document.Accounts) > MaxAccounts || len(document.Tombstones) > 100_000 {
		return ErrInvalid
	}
	ids, logins := make(map[string]bool, len(document.Accounts)), make(map[string]bool, len(document.Accounts))
	last := ""
	for _, account := range document.Accounts {
		parsed, err := uuid.Parse(account.ID)
		roles, roleErr := canonicalRoleIDs(account.RoleIDs)
		if err != nil || parsed.Version() != 4 || parsed.String() != account.ID || account.ID <= last || ids[account.ID] || logins[account.Login] || account.RoleIDs == nil || roleErr != nil || !slices.Equal(roles, account.RoleIDs) || validateAccountFields(account.Login, account.DisplayName, account.Kind) != nil || account.CreatedAt < 0 || account.UpdatedAt < account.CreatedAt {
			return ErrInvalid
		}
		last, ids[account.ID], logins[account.Login] = account.ID, true, true
	}
	last = ""
	for _, tombstone := range document.Tombstones {
		parsed, err := uuid.Parse(tombstone.AccountID)
		if err != nil || parsed.Version() != 4 || parsed.String() != tombstone.AccountID || tombstone.AccountID <= last || ids[tombstone.AccountID] || tombstone.DeletedAt < 0 {
			return ErrInvalid
		}
		last = tombstone.AccountID
	}
	return nil
}

func validateCredentialsBackup(document credentialsBackupDocument) error {
	if document.SchemaVersion != 1 || document.IdentityRevision < 0 || document.Passwords == nil || document.Tokens == nil {
		return ErrInvalid
	}
	last := ""
	for _, password := range document.Passwords {
		if password.AccountID <= last || len(password.Salt) != 16 || len(password.Hash) != 32 || password.AlgorithmVersion != argonVersion || password.MemoryKiB < 8192 || password.MemoryKiB > 262144 || password.TimeCost < 1 || password.TimeCost > 10 || password.Parallelism < 1 || password.Parallelism > 8 || password.UpdatedAt < 0 {
			return ErrInvalid
		}
		last = password.AccountID
	}
	last, perAccount := "", make(map[string]int)
	catalog := authz.Catalog()
	for _, token := range document.Tokens {
		key := token.AccountID + "\x00" + base64.RawURLEncoding.EncodeToString(token.Selector)
		canonical, err := canonicalCapabilities(token.Scopes)
		if key <= last || len(token.Selector) != tokenSelectorBytes || len(token.SecretHash) != sha256.Size || token.Scopes == nil || !utf8.ValidString(token.Name) || len(token.Name) < 1 || len(token.Name) > 128 || strings.IndexByte(token.Name, 0) >= 0 || err != nil || !slices.Equal(canonical, token.Scopes) || token.CreatedAt < 0 || invalidOptionalTime(token.ExpiresAt) || invalidOptionalTime(token.RevokedAt) || invalidOptionalTime(token.LastUsedAt) {
			return ErrInvalid
		}
		for _, capability := range token.Scopes {
			if !slices.Contains(catalog, capability) {
				return ErrInvalid
			}
		}
		perAccount[token.AccountID]++
		if perAccount[token.AccountID] > MaxTokensPerAccount {
			return ErrTokenLimit
		}
		last = key
	}
	return nil
}

func invalidOptionalTime(value *int64) bool { return value != nil && *value < 0 }

func restoreCandidates(currentUsers usersBackupDocument, currentCredentials credentialsBackupDocument, users *usersBackupDocument, credentials *credentialsBackupDocument) (usersBackupDocument, credentialsBackupDocument, error) {
	candidateUsers, candidateCredentials := currentUsers, currentCredentials
	if users != nil {
		candidateUsers = *users
		candidateUsers.Accounts = slices.Clone(users.Accounts)
		candidateUsers.Tombstones = slices.Clone(users.Tombstones)
		currentTombstones := make(map[string]int64, len(currentUsers.Tombstones))
		for _, tombstone := range currentUsers.Tombstones {
			currentTombstones[tombstone.AccountID] = tombstone.DeletedAt
		}
		for _, account := range candidateUsers.Accounts {
			if _, tombstoned := currentTombstones[account.ID]; tombstoned {
				return usersBackupDocument{}, credentialsBackupDocument{}, ErrConflict
			}
		}
		for _, tombstone := range candidateUsers.Tombstones {
			if previous, exists := currentTombstones[tombstone.AccountID]; !exists || tombstone.DeletedAt < previous {
				currentTombstones[tombstone.AccountID] = tombstone.DeletedAt
			}
		}
		candidateUsers.Tombstones = candidateUsers.Tombstones[:0]
		for id, deletedAt := range currentTombstones {
			candidateUsers.Tombstones = append(candidateUsers.Tombstones, backupTombstone{AccountID: id, DeletedAt: deletedAt})
		}
		slices.SortFunc(candidateUsers.Tombstones, func(a, b backupTombstone) int { return strings.Compare(a.AccountID, b.AccountID) })
		candidateCredentials = credentialsBackupDocument{SchemaVersion: 1, IdentityRevision: users.IdentityRevision, Passwords: []backupPassword{}, Tokens: []backupToken{}}
	}
	if credentials != nil {
		candidateCredentials = *credentials
		candidateCredentials.Passwords = slices.Clone(credentials.Passwords)
		candidateCredentials.Tokens = slices.Clone(credentials.Tokens)
	}
	if err := validateCredentialBindings(candidateUsers, candidateCredentials); err != nil {
		return usersBackupDocument{}, credentialsBackupDocument{}, err
	}
	return candidateUsers, candidateCredentials, nil
}

func validateCredentialBindings(users usersBackupDocument, credentials credentialsBackupDocument) error {
	accounts := make(map[string]backupAccount, len(users.Accounts))
	for _, account := range users.Accounts {
		accounts[account.ID] = account
	}
	for _, password := range credentials.Passwords {
		account, exists := accounts[password.AccountID]
		if !exists {
			return ErrNotFound
		}
		if account.Kind != Human {
			return ErrInvalid
		}
	}
	for _, token := range credentials.Tokens {
		if _, exists := accounts[token.AccountID]; !exists {
			return ErrNotFound
		}
	}
	return nil
}

func identityRestorePreview(revision int64, beforeUsers usersBackupDocument, beforeCredentials credentialsBackupDocument, afterUsers usersBackupDocument, afterCredentials credentialsBackupDocument) RestorePreview {
	preview := RestorePreview{ExpectedIdentityRevision: revision, MissingRoleIDs: missingRoles(afterUsers, nil), RecoveryAdministrators: []RecoveryAdministrator{}}
	preview.UserChanges = accountCounts(beforeUsers.Accounts, afterUsers.Accounts)
	preview.CredentialChanges = credentialCounts(beforeCredentials, afterCredentials)
	preview.RequiresRootAdministrator = !hasRecoveryAdministrator(afterUsers, afterCredentials)
	for _, account := range afterUsers.Accounts {
		if account.Enabled && account.Kind == Human && slices.Contains(account.RoleIDs, authz.AdministratorRoleID) {
			preview.RecoveryAdministrators = append(preview.RecoveryAdministrators, RecoveryAdministrator{ID: account.ID, Login: account.Login})
		}
	}
	slices.SortFunc(preview.RecoveryAdministrators, func(a, b RecoveryAdministrator) int { return strings.Compare(a.Login, b.Login) })
	return preview
}

func accountCounts(before, after []backupAccount) RestoreCounts {
	old := make(map[string]backupAccount, len(before))
	for _, account := range before { old[account.ID] = account }
	var result RestoreCounts
	for _, account := range after {
		previous, exists := old[account.ID]
		if !exists { result.Created++ } else if !accountEqual(previous, account) { result.Updated++ }
		delete(old, account.ID)
	}
	result.Deleted = len(old)
	return result
}

func accountEqual(a, b backupAccount) bool {
	return a.ID == b.ID && a.Login == b.Login && a.DisplayName == b.DisplayName && a.Kind == b.Kind && a.Enabled == b.Enabled && a.CreatedAt == b.CreatedAt && a.UpdatedAt == b.UpdatedAt && slices.Equal(a.RoleIDs, b.RoleIDs)
}

func credentialCounts(before, after credentialsBackupDocument) RestoreCounts {
	old := make(map[string]string, len(before.Passwords)+len(before.Tokens))
	for _, password := range before.Passwords { raw, _ := json.Marshal(password); old["p:"+password.AccountID] = string(raw) }
	for _, token := range before.Tokens { raw, _ := json.Marshal(token); old["t:"+base64.RawURLEncoding.EncodeToString(token.Selector)] = string(raw) }
	var result RestoreCounts
	for _, item := range appendCredentialItems(after) {
		previous, exists := old[item.key]
		if !exists { result.Created++ } else if previous != item.value { result.Updated++ }
		delete(old, item.key)
	}
	result.Deleted = len(old)
	return result
}

type credentialItem struct{ key, value string }
func appendCredentialItems(document credentialsBackupDocument) []credentialItem {
	items := make([]credentialItem, 0, len(document.Passwords)+len(document.Tokens))
	for _, password := range document.Passwords { raw, _ := json.Marshal(password); items = append(items, credentialItem{"p:"+password.AccountID, string(raw)}) }
	for _, token := range document.Tokens { raw, _ := json.Marshal(token); items = append(items, credentialItem{"t:"+base64.RawURLEncoding.EncodeToString(token.Selector), string(raw)}) }
	return items
}

func hasRecoveryAdministrator(users usersBackupDocument, credentials credentialsBackupDocument) bool {
	passwords := make(map[string]bool, len(credentials.Passwords))
	for _, password := range credentials.Passwords { passwords[password.AccountID] = true }
	for _, account := range users.Accounts {
		if account.Enabled && account.Kind == Human && passwords[account.ID] && slices.Contains(account.RoleIDs, authz.AdministratorRoleID) { return true }
	}
	return false
}

func addRootAdministrator(ctx context.Context, users *usersBackupDocument, credentials *credentialsBackupDocument, root *RootAdministrator) error {
	if root == nil || len(root.Password) == 0 {
		return ErrInvariant
	}
	index := -1
	if root.AccountID != "" {
		for i := range users.Accounts { if users.Accounts[i].ID == root.AccountID { index = i; break } }
		if index < 0 { return ErrNotFound }
		account := users.Accounts[index]
		if !account.Enabled || account.Kind != Human || !slices.Contains(account.RoleIDs, authz.AdministratorRoleID) { return ErrInvalid }
	} else {
		if len(users.Accounts) >= MaxAccounts { return ErrAccountLimit }
		login, err := canonicalLogin(root.Login)
		if err != nil || validateAccountFields(login, root.DisplayName, Human) != nil { return ErrInvalid }
		for _, account := range users.Accounts { if account.Login == login { return ErrDuplicate } }
		now := time.Now().Unix()
		users.Accounts = append(users.Accounts, backupAccount{ID: uuid.NewString(), Login: login, DisplayName: root.DisplayName, Kind: Human, Enabled: true, RoleIDs: []string{authz.AdministratorRoleID}, CreatedAt: now, UpdatedAt: now})
		slices.SortFunc(users.Accounts, func(a, b backupAccount) int { return strings.Compare(a.ID, b.ID) })
		for i := range users.Accounts { if users.Accounts[i].Login == login { index = i; break } }
	}
	account := users.Accounts[index]
	encoded, err := hashPasswordWithPolicy(ctx, root.Password, account.Login, account.DisplayName)
	if err != nil { return err }
	parsed, err := parsePasswordHash(encoded)
	if err != nil { return err }
	password := backupPassword{AccountID: account.ID, Salt: parsed.salt, Hash: parsed.hash, AlgorithmVersion: argonVersion, MemoryKiB: int(parsed.memory), TimeCost: int(parsed.time), Parallelism: int(parsed.threads), UpdatedAt: time.Now().Unix()}
	kept := credentials.Passwords[:0]
	for _, existing := range credentials.Passwords { if existing.AccountID != account.ID { kept = append(kept, existing) } }
	credentials.Passwords = append(kept, password)
	slices.SortFunc(credentials.Passwords, func(a, b backupPassword) int { return strings.Compare(a.AccountID, b.AccountID) })
	return nil
}

func applyRestore(ctx context.Context, tx *sql.Tx, users usersBackupDocument, credentials credentialsBackupDocument, replaceUsers bool, revision int64) error {
	accountEpochs, passwordEpochs, err := readEpochs(ctx, tx)
	if err != nil { return err }
	if _, err = tx.ExecContext(ctx, "DELETE FROM sessions"); err != nil { return err }
	if replaceUsers {
		if _, err = tx.ExecContext(ctx, "DELETE FROM accounts"); err != nil { return err }
		if _, err = tx.ExecContext(ctx, "DELETE FROM metadata WHERE key LIKE 'account_epoch:%' OR key LIKE 'password_epoch:%'"); err != nil { return err }
		for _, tombstone := range users.Tombstones {
			if _, err = tx.ExecContext(ctx, "INSERT INTO tombstones(account_id,deleted_at) VALUES(?,?) ON CONFLICT(account_id) DO UPDATE SET deleted_at=min(deleted_at,excluded.deleted_at)", tombstone.AccountID, tombstone.DeletedAt); err != nil { return err }
		}
		for _, value := range users.Accounts {
			account := Account{ID: value.ID, Login: value.Login, DisplayName: value.DisplayName, Kind: value.Kind, Enabled: value.Enabled, RoleIDs: value.RoleIDs, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
			if err = insertAccount(ctx, tx, account); err != nil { return accountWriteError(err) }
			if err = insertAccountRoles(ctx, tx, account.ID, account.RoleIDs); err != nil { return err }
			if _, err = tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES(?,?)", "account_epoch:"+account.ID, accountEpochs[account.ID]+1); err != nil { return err }
			if _, err = tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES(?,?)", "password_epoch:"+account.ID, passwordEpochs[account.ID]+1); err != nil { return err }
		}
	} else {
		for _, value := range users.Accounts {
			if _, err = tx.ExecContext(ctx, "UPDATE metadata SET value=value+1 WHERE key IN (?,?)", "account_epoch:"+value.ID, "password_epoch:"+value.ID); err != nil { return err }
		}
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM password_credentials"); err != nil { return err }
	if _, err = tx.ExecContext(ctx, "DELETE FROM api_tokens"); err != nil { return err }
	for _, password := range credentials.Passwords {
		if _, err = tx.ExecContext(ctx, `INSERT INTO password_credentials(account_id,salt,hash,algorithm_version,memory_kib,time_cost,parallelism,updated_at) VALUES(?,?,?,?,?,?,?,?)`, password.AccountID, password.Salt, password.Hash, password.AlgorithmVersion, password.MemoryKiB, password.TimeCost, password.Parallelism, password.UpdatedAt); err != nil { return err }
	}
	for _, token := range credentials.Tokens {
		var expiry, revoked, used any
		if token.ExpiresAt != nil { expiry = *token.ExpiresAt }
		if token.RevokedAt != nil { revoked = *token.RevokedAt }
		if token.LastUsedAt != nil { used = *token.LastUsedAt }
		var passwordEpoch int64
		if err = tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key=?", "password_epoch:"+token.AccountID).Scan(&passwordEpoch); err != nil { return err }
		if _, err = tx.ExecContext(ctx, `INSERT INTO api_tokens(selector,account_id,secret_hash,name,scopes,created_at,expires_at,revoked_at,last_used_at,revoke_on_password_change,password_epoch,revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, token.Selector, token.AccountID, token.SecretHash, token.Name, encodeTokenScope(token.Scopes), token.CreatedAt, expiry, revoked, used, boolInteger(token.RevokeOnPasswordChange), passwordEpoch, revision); err != nil { return err }
	}
	return nil
}

func boolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}

func readEpochs(ctx context.Context, tx *sql.Tx) (map[string]int64, map[string]int64, error) {
	accounts, passwords := make(map[string]int64), make(map[string]int64)
	rows, err := tx.QueryContext(ctx, "SELECT key,value FROM metadata WHERE key LIKE 'account_epoch:%' OR key LIKE 'password_epoch:%'")
	if err != nil { return nil, nil, err }
	defer rows.Close()
	for rows.Next() {
		var key string
		var value int64
		if err = rows.Scan(&key, &value); err != nil { return nil, nil, err }
		if id, ok := strings.CutPrefix(key, "account_epoch:"); ok { accounts[id] = value } else if id, ok := strings.CutPrefix(key, "password_epoch:"); ok { passwords[id] = value }
	}
	return accounts, passwords, rows.Err()
}

func (s *store) RestoreMatches(ctx context.Context, target BackupDocuments) (bool, error) {
	current, err := s.ExportBackup(ctx, true)
	if err != nil { return false, err }
	return current.IdentityRevision == target.IdentityRevision && bytes.Equal(current.Users, target.Users) && bytes.Equal(current.UserCredentials, target.UserCredentials), nil
}

var _ BackupStore = (*store)(nil)
