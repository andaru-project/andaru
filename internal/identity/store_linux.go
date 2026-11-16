package identity

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/securefs"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

type store struct {
	db         *sql.DB
	limiterKey [32]byte
}

func Open(ctx context.Context, stateDir string) (*store, error) {
	if os.Geteuid() != 0 || !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir {
		return nil, errors.New("identity store requires root and canonical absolute state directory")
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
	path := filepath.Join(stateDir, "identity.db")
	_, err := os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	fresh := os.IsNotExist(err)
	if fresh {
		fd, e := unix.Open(path, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
		if e != nil {
			return nil, e
		}
		unix.Close(fd)
	}
	if err = checkPrivateDBFiles(path); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	q := url.Values{"mode": {"rw"}, "_pragma": {"busy_timeout(2000)", "foreign_keys(1)", "journal_mode(WAL)", "synchronous(FULL)", "secure_delete(1)"}}
	uri := (&url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}).String()
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	fail := func(e error) (*store, error) { db.Close(); return nil, e }
	if err = db.PingContext(ctx); err != nil {
		return fail(err)
	}
	if err = checkPrivateDBFiles(path); err != nil {
		return fail(err)
	}
	var app, version int
	if err = db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&app); err != nil {
		return fail(err)
	}
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if fresh {
		if app != 0 || version != 0 {
			return fail(errors.New("fresh identity database has invalid identifiers"))
		}
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return fail(e)
		}
		_, e = tx.ExecContext(ctx, schema)
		if e == nil {
			_, e = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id=%d", applicationID))
		}
		if e == nil {
			_, e = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", LayoutVersion))
		}
		if e != nil {
			tx.Rollback()
			return fail(e)
		}
		if e = tx.Commit(); e != nil {
			return fail(e)
		}
	} else if app != applicationID || version != LayoutVersion {
		return fail(errors.New("identity database application or layout mismatch"))
	}
	if err = verifySchema(ctx, db); err != nil {
		return fail(err)
	}
	if err = verifyPragmas(ctx, db); err != nil {
		return fail(err)
	}
	key, err := loadLimiterKey(ctx, db, false)
	if err != nil {
		return fail(err)
	}
	if err = checkPrivateDBFiles(path); err != nil {
		return fail(err)
	}
	return &store{db: db, limiterKey: key}, nil
}

func checkPrivateDBFiles(path string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		p := path + suffix
		st, err := os.Lstat(p)
		if os.IsNotExist(err) && suffix != "" {
			continue
		}
		if err != nil {
			return err
		}
		if !privateFile(st) {
			return fmt.Errorf("unsafe identity database file %s", filepath.Base(p))
		}
	}
	return nil
}
func verifyPragmas(ctx context.Context, db *sql.DB) error {
	var journal string
	var syncMode, foreign, secure int
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&syncMode); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreign); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, "PRAGMA secure_delete").Scan(&secure); err != nil {
		return err
	}
	if journal != "wal" || syncMode != 2 || foreign != 1 || secure != 1 {
		return errors.New("identity SQLite pragmas mismatch")
	}
	return nil
}
func verifySchema(ctx context.Context, db *sql.DB) error {
	for _, name := range []string{"accounts", "tombstones", "account_roles", "password_credentials", "api_tokens", "sessions", "login_limits", "source_limits", "limiter_key", "limiter_overload", "metadata", "revisions", "operations"} {
		var n int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("identity schema missing table %s", name)
		}
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM limiter_key WHERE id=1 AND length(secret)=32").Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return errors.New("identity limiter key missing or invalid")
	}
	return nil
}

func (s *store) Close() error { return s.db.Close() }
func (s *store) Revision(ctx context.Context) (int64, error) {
	var revision int64
	err := s.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='revision'").Scan(&revision)
	return revision, err
}
func privateDirectory(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.IsDir() || !securefs.RootOwned(st) || st.Mode().Perm() != 0700 {
		return errors.New("identity state directory must be root-owned 0700")
	}
	return nil
}
func privateFile(st os.FileInfo) bool {
	if st == nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || !securefs.RootOwned(st) {
		return false
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}
func (s *store) BootstrapAdmin(ctx context.Context, login, displayName string, password []byte) (Account, int64, error) {
	var err error
	login, err = canonicalLogin(login)
	if err != nil {
		return Account{}, 0, err
	}
	if err = validateAccountFields(login, displayName, Human); err != nil {
		return Account{}, 0, err
	}
	encoded, err := hashPasswordWithPolicy(ctx, password, login, displayName)
	if err != nil {
		return Account{}, 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, 0, err
	}
	defer tx.Rollback()
	var count int
	var revision int64
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM accounts").Scan(&count); err != nil {
		return Account{}, 0, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='revision'").Scan(&revision); err != nil {
		return Account{}, 0, err
	}
	if count != 0 || revision != 0 {
		return Account{}, 0, ErrConflict
	}
	now := time.Now().Unix()
	account := Account{ID: uuid.NewString(), Login: login, DisplayName: displayName, Kind: Human, Enabled: true, RoleIDs: []string{authz.AdministratorRoleID}, CreatedAt: now, UpdatedAt: now}
	if err = insertAccount(ctx, tx, account); err != nil {
		return Account{}, 0, accountWriteError(err)
	}
	if err = insertAccountRoles(ctx, tx, account.ID, account.RoleIDs); err != nil {
		return Account{}, 0, err
	}
	if err = insertCredential(ctx, tx, account.ID, encoded, now); err != nil {
		return Account{}, 0, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES(?,0)", "password_epoch:"+account.ID); err != nil {
		return Account{}, 0, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES(?,0)", "account_epoch:"+account.ID); err != nil {
		return Account{}, 0, err
	}
	revision, err = finishMutation(ctx, tx)
	if err != nil {
		return Account{}, 0, err
	}
	return account, revision, nil
}

func (s *store) CreateAccount(ctx context.Context, input CreateAccountInput, expectedRevision int64) (Account, int64, error) {
	var err error
	input.Login, err = canonicalLogin(input.Login)
	if err != nil {
		return Account{}, 0, err
	}
	input.RoleIDs, err = canonicalRoleIDs(input.RoleIDs)
	if err != nil {
		return Account{}, 0, err
	}
	if err = validateAccountFields(input.Login, input.DisplayName, input.Kind); err != nil {
		return Account{}, 0, err
	}
	tx, err := s.beginMutation(ctx, expectedRevision)
	if err != nil {
		return Account{}, 0, err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM accounts").Scan(&count); err != nil {
		return Account{}, 0, err
	}
	if count >= MaxAccounts {
		return Account{}, 0, ErrAccountLimit
	}
	now := time.Now().Unix()
	account := Account{ID: uuid.NewString(), Login: input.Login, DisplayName: input.DisplayName, Kind: input.Kind, Enabled: input.Enabled, RoleIDs: input.RoleIDs, CreatedAt: now, UpdatedAt: now}
	if err = insertAccount(ctx, tx, account); err != nil {
		return Account{}, 0, accountWriteError(err)
	}
	if err = insertAccountRoles(ctx, tx, account.ID, account.RoleIDs); err != nil {
		return Account{}, 0, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES(?,0)", "password_epoch:"+account.ID); err != nil {
		return Account{}, 0, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES(?,0)", "account_epoch:"+account.ID); err != nil {
		return Account{}, 0, err
	}
	revision, err := finishMutation(ctx, tx)
	if err != nil {
		return Account{}, 0, err
	}
	return account, revision, nil
}

func (s *store) UpdateAccount(ctx context.Context, id string, expectedRevision int64, input UpdateAccountInput) (Account, int64, error) {
	tx, err := s.beginMutation(ctx, expectedRevision)
	if err != nil {
		return Account{}, 0, err
	}
	defer tx.Rollback()
	account, err := accountByID(ctx, tx, id)
	if err != nil {
		return Account{}, 0, err
	}
	if input.Login != nil {
		account.Login, err = canonicalLogin(*input.Login)
		if err != nil {
			return Account{}, 0, err
		}
	}
	if input.DisplayName != nil {
		account.DisplayName = *input.DisplayName
	}
	if input.Kind != nil {
		account.Kind = *input.Kind
	}
	if input.Enabled != nil {
		account.Enabled = *input.Enabled
	}
	rolesChanged := false
	if input.RoleIDs != nil {
		roleIDs, err := canonicalRoleIDs(*input.RoleIDs)
		if err != nil {
			return Account{}, 0, err
		}
		rolesChanged = !slices.Equal(account.RoleIDs, roleIDs)
		if rolesChanged {
			if _, err = tx.ExecContext(ctx, "DELETE FROM account_roles WHERE account_id=?", id); err != nil {
				return Account{}, 0, err
			}
			if err = insertAccountRoles(ctx, tx, id, roleIDs); err != nil {
				return Account{}, 0, err
			}
		}
		account.RoleIDs = roleIDs
	}
	if err = validateAccountFields(account.Login, account.DisplayName, account.Kind); err != nil {
		return Account{}, 0, err
	}
	if account.Kind == Service {
		var hasPassword int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM password_credentials WHERE account_id=?", id).Scan(&hasPassword); err != nil {
			return Account{}, 0, err
		}
		if hasPassword != 0 {
			return Account{}, 0, ErrInvalid
		}
	}
	account.UpdatedAt = time.Now().Unix()
	if _, err = tx.ExecContext(ctx, "UPDATE accounts SET login=?,display_name=?,kind=?,enabled=?,updated_at=? WHERE id=?", account.Login, account.DisplayName, account.Kind, account.Enabled, account.UpdatedAt, id); err != nil {
		return Account{}, 0, accountWriteError(err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE metadata SET value=value+1 WHERE key=?", "account_epoch:"+id); err != nil {
		return Account{}, 0, err
	}
	if rolesChanged && account.Enabled {
		if _, err = tx.ExecContext(ctx, "DELETE FROM sessions WHERE account_id=?", id); err != nil {
			return Account{}, 0, err
		}
	}
	if !account.Enabled {
		if err = revokeAccountAuth(ctx, tx, id, account.UpdatedAt, false); err != nil {
			return Account{}, 0, err
		}
	}
	revision, err := finishMutation(ctx, tx)
	if err != nil {
		return Account{}, 0, err
	}
	return account, revision, nil
}

func (s *store) SetPassword(ctx context.Context, id string, expectedRevision int64, password []byte) (int64, error) {
	account, err := s.GetAccount(ctx, id)
	if err != nil {
		return 0, err
	}
	if account.Kind != Human {
		return 0, ErrInvalid
	}
	encoded, err := hashPasswordWithPolicy(ctx, password, account.Login, account.DisplayName)
	if err != nil {
		return 0, err
	}
	tx, err := s.beginMutation(ctx, expectedRevision)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	current, err := accountByID(ctx, tx, id)
	if err != nil {
		return 0, err
	}
	if current.Kind != Human {
		return 0, ErrInvalid
	}
	if current.Login != account.Login || current.DisplayName != account.DisplayName {
		return 0, ErrConflict
	}
	now := time.Now().Unix()
	if err = insertCredential(ctx, tx, id, encoded, now); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE accounts SET updated_at=? WHERE id=?", now, id); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE metadata SET value=value+1 WHERE key=?", "password_epoch:"+id); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE metadata SET value=value+1 WHERE key=?", "account_epoch:"+id); err != nil {
		return 0, err
	}
	if err = revokeAccountAuth(ctx, tx, id, now, true); err != nil {
		return 0, err
	}
	return finishMutation(ctx, tx)
}

func (s *store) GetAccount(ctx context.Context, id string) (Account, error) {
	return accountByID(ctx, s.db, id)
}

func (s *store) ListAccounts(ctx context.Context, afterLogin string, limit int) (AccountPage, error) {
	if limit < 1 || limit > MaxAccountPageSize {
		return AccountPage{}, ErrInvalid
	}
	cursor := ""
	if afterLogin != "" {
		var err error
		cursor, err = canonicalLogin(afterLogin)
		if err != nil {
			return AccountPage{}, err
		}
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,login,display_name,kind,enabled,created_at,updated_at FROM accounts WHERE login>? ORDER BY login LIMIT ?", cursor, limit+1)
	if err != nil {
		return AccountPage{}, err
	}
	accounts := make([]Account, 0, limit+1)
	for rows.Next() {
		var account Account
		var enabled int
		if err = rows.Scan(&account.ID, &account.Login, &account.DisplayName, &account.Kind, &enabled, &account.CreatedAt, &account.UpdatedAt); err != nil {
			rows.Close()
			return AccountPage{}, err
		}
		account.Enabled = enabled == 1
		accounts = append(accounts, account)
	}
	err = rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return AccountPage{}, err
	}
	hasNext := len(accounts) > limit
	if hasNext {
		accounts = accounts[:limit]
	}
	page := AccountPage{Items: accounts}
	if len(accounts) == 0 {
		return page, nil
	}
	if hasNext {
		page.NextCursor = accounts[len(accounts)-1].Login
	}
	indices := make(map[string]int, len(accounts))
	var query strings.Builder
	query.WriteString("SELECT account_id,role_id FROM account_roles WHERE account_id IN (")
	args := make([]any, len(accounts))
	for i := range accounts {
		if i != 0 {
			query.WriteByte(',')
		}
		query.WriteByte('?')
		args[i] = accounts[i].ID
		accounts[i].RoleIDs = make([]string, 0)
		indices[accounts[i].ID] = i
	}
	query.WriteString(") ORDER BY account_id,role_id")
	roleRows, err := s.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return AccountPage{}, err
	}
	for roleRows.Next() {
		var accountID, roleID string
		if err = roleRows.Scan(&accountID, &roleID); err != nil {
			roleRows.Close()
			return AccountPage{}, err
		}
		accounts[indices[accountID]].RoleIDs = append(accounts[indices[accountID]].RoleIDs, roleID)
	}
	err = roleRows.Err()
	if closeErr := roleRows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return AccountPage{}, err
	}
	return page, nil
}

func (s *store) DeleteAccount(ctx context.Context, id string, expectedRevision int64) error {
	tx, err := s.beginMutation(ctx, expectedRevision)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = accountByID(ctx, tx, id); err != nil {
		return err
	}
	now := time.Now().Unix()
	if _, err = tx.ExecContext(ctx, "INSERT INTO tombstones(account_id,deleted_at) VALUES(?,?)", id, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM accounts WHERE id=?", id); err != nil {
		return err
	}
	_, err = finishMutation(ctx, tx)
	return err
}

func (s *store) DisableAccount(ctx context.Context, id string, expectedRevision int64) error {
	disabled := false
	_, _, err := s.UpdateAccount(ctx, id, expectedRevision, UpdateAccountInput{Enabled: &disabled})
	return err
}

func (s *store) VerifyPassword(ctx context.Context, id string, candidate []byte) (bool, error) {
	return s.verifyPasswordForAccount(ctx, id, nil, candidate)
}

func (s *store) verifyPasswordForAccount(ctx context.Context, id string, known *Account, candidate []byte) (bool, error) {
	var enabled int
	var kind AccountKind
	if known != nil {
		kind = known.Kind
		if known.Enabled {
			enabled = 1
		}
	} else {
		if id == "" {
			return verifyPasswordHash(ctx, dummyPasswordHash, candidate)
		}
		err := s.db.QueryRowContext(ctx, "SELECT enabled,kind FROM accounts WHERE id=?", id).Scan(&enabled, &kind)
		if err == sql.ErrNoRows {
			return verifyPasswordHash(ctx, dummyPasswordHash, candidate)
		}
		if err != nil {
			return false, err
		}
	}
	if enabled != 1 || kind != Human {
		return verifyPasswordHash(ctx, dummyPasswordHash, candidate)
	}
	var salt, digest []byte
	var memory, timeCost, parallel, version int
	err := s.db.QueryRowContext(ctx, `SELECT salt,hash,memory_kib,time_cost,parallelism,algorithm_version FROM password_credentials WHERE account_id=?`, id).
		Scan(&salt, &digest, &memory, &timeCost, &parallel, &version)
	if err == sql.ErrNoRows {
		return verifyPasswordHash(ctx, dummyPasswordHash, candidate)
	}
	if err != nil {
		return false, err
	}
	encoded := []byte(fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", version, memory, timeCost, parallel, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(digest)))
	ok, err := verifyPasswordHash(ctx, encoded, candidate)
	if err != nil || !ok || !hashNeedsUpgrade(encoded) {
		return ok, err
	}
	rehash, err := hashPassword(ctx, candidate)
	if err != nil {
		return false, err
	}
	s.rehashCredential(ctx, id, salt, digest, rehash)
	return true, nil
}

func (s *store) rehashCredential(ctx context.Context, id string, oldSalt, oldHash, encoded []byte) {
	p, err := parsePasswordHash(encoded)
	if err != nil {
		return
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE password_credentials SET salt=?,hash=?,algorithm_version=?,memory_kib=?,time_cost=?,parallelism=?,updated_at=unixepoch()
		WHERE account_id=? AND salt=? AND hash=?`, p.salt, p.hash, argonVersion, p.memory, p.time, p.threads, id, oldSalt, oldHash)
	if err != nil {
		return
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return
	}
	_, _ = finishMutation(ctx, tx)
}

var dummyPasswordHash = []byte("$argon2id$v=19$m=65536,t=3,p=1$MDEyMzQ1Njc4OWFiY2RlZg$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
var customRoleIDPattern = regexp.MustCompile(`^rol_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func canonicalLogin(login string) (string, error) {
	if len(login) < 3 || len(login) > 64 {
		return "", ErrInvalid
	}
	var canonical []byte
	for i := 0; i < len(login); i++ {
		c := login[i]
		if c >= 'A' && c <= 'Z' {
			if canonical == nil {
				canonical = []byte(login)
			}
			c += 'a' - 'A'
			canonical[i] = c
		}
		if i == 0 && !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return "", ErrInvalid
		}
		if i > 0 && !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", rune(c))) {
			return "", ErrInvalid
		}
	}
	if canonical == nil {
		return login, nil
	}
	return string(canonical), nil
}

func validateAccountFields(login, display string, kind AccountKind) error {
	if len(login) < 3 || len(login) > 64 || !utf8.ValidString(display) || utf8.RuneCountInString(display) > 128 || strings.IndexByte(display, 0) >= 0 || (kind != Human && kind != Service) {
		return ErrInvalid
	}
	if !(login[0] >= 'a' && login[0] <= 'z' || login[0] >= '0' && login[0] <= '9') {
		return ErrInvalid
	}
	for i := 1; i < len(login); i++ {
		c := login[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", rune(c))) {
			return ErrInvalid
		}
	}
	return nil
}

func insertAccount(ctx context.Context, tx *sql.Tx, a Account) error {
	var tombstoned int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM tombstones WHERE account_id=?", a.ID).Scan(&tombstoned); err != nil {
		return err
	}
	if tombstoned != 0 {
		return ErrConflict
	}
	enabled := 0
	if a.Enabled {
		enabled = 1
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO accounts(id,login,display_name,kind,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?)", a.ID, a.Login, a.DisplayName, a.Kind, enabled, a.CreatedAt, a.UpdatedAt)
	return err
}

type accountQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func accountByID(ctx context.Context, q accountQueryer, id string) (Account, error) {
	var a Account
	var enabled int
	err := q.QueryRowContext(ctx, "SELECT id,login,display_name,kind,enabled,created_at,updated_at FROM accounts WHERE id=?", id).Scan(&a.ID, &a.Login, &a.DisplayName, &a.Kind, &enabled, &a.CreatedAt, &a.UpdatedAt)
	if err == sql.ErrNoRows {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, err
	}
	a.Enabled = enabled == 1
	a.RoleIDs, err = accountRoleIDs(ctx, q, id)
	return a, err
}

func accountRoleIDs(ctx context.Context, q accountQueryer, id string) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT role_id FROM account_roles WHERE account_id=? ORDER BY role_id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var roleID string
		if err = rows.Scan(&roleID); err != nil {
			return nil, err
		}
		ids = append(ids, roleID)
	}
	return ids, rows.Err()
}

func (s *store) beginMutation(ctx context.Context, expected int64) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	var current int64
	if err = tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='revision'").Scan(&current); err != nil {
		tx.Rollback()
		return nil, err
	}
	if current != expected {
		tx.Rollback()
		return nil, ErrConflict
	}
	return tx, nil
}

func validateAdministratorInvariant(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) error {
	var valid int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM accounts a JOIN account_roles r ON r.account_id=a.id AND r.role_id=?
		JOIN password_credentials c ON c.account_id=a.id WHERE a.kind='human' AND a.enabled=1`, authz.AdministratorRoleID).Scan(&valid)
	if err != nil {
		return err
	}
	if valid < 1 {
		return ErrInvariant
	}
	return nil
}

func canonicalRoleIDs(roleIDs []string) ([]string, error) {
	if len(roleIDs) > MaxRoleAssignments {
		return nil, ErrInvalid
	}
	ids := slices.Clone(roleIDs)
	slices.Sort(ids)
	for i, id := range ids {
		if id != authz.AdministratorRoleID && id != authz.ReadOnlyRoleID && !customRoleIDPattern.MatchString(id) {
			return nil, ErrInvalid
		}
		if i > 0 && ids[i-1] == id {
			return nil, ErrInvalid
		}
	}
	return ids, nil
}

func insertAccountRoles(ctx context.Context, tx *sql.Tx, accountID string, roleIDs []string) error {
	for _, roleID := range roleIDs {
		if _, err := tx.ExecContext(ctx, "INSERT INTO account_roles(account_id,role_id) VALUES(?,?)", accountID, roleID); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) ValidateAdministratorInvariant(ctx context.Context) error {
	return validateAdministratorInvariant(ctx, s.db)
}

func finishMutation(ctx context.Context, tx *sql.Tx) (int64, error) {
	if err := validateAdministratorInvariant(ctx, tx); err != nil {
		return 0, err
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, "SELECT value+1 FROM metadata WHERE key='revision'").Scan(&revision); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE metadata SET value=? WHERE key='revision'", revision); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO revisions(revision,created_at) VALUES(?,unixepoch())", revision); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return revision, nil
}

func insertCredential(ctx context.Context, tx *sql.Tx, id string, encoded []byte, now int64) error {
	p, err := parsePasswordHash(encoded)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO password_credentials(account_id,salt,hash,algorithm_version,memory_kib,time_cost,parallelism,updated_at)
		VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(account_id) DO UPDATE SET salt=excluded.salt,hash=excluded.hash,algorithm_version=excluded.algorithm_version,memory_kib=excluded.memory_kib,time_cost=excluded.time_cost,parallelism=excluded.parallelism,updated_at=excluded.updated_at`,
		id, p.salt, p.hash, argonVersion, p.memory, p.time, p.threads, now)
	return err
}

func revokeAccountAuth(ctx context.Context, tx *sql.Tx, id string, now int64, passwordOnly bool) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE account_id=?", id); err != nil {
		return err
	}
	query := "UPDATE api_tokens SET revoked_at=? WHERE account_id=? AND revoked_at IS NULL"
	if passwordOnly {
		query += " AND revoke_on_password_change=1"
	}
	_, err := tx.ExecContext(ctx, query, now, id)
	return err
}

func accountWriteError(err error) error {
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return ErrDuplicate
	}
	return err
}
