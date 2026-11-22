package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"io"
	"time"

	"github.com/andaru-project/andaru/internal/authz"
)

const (
	sessionSelectorBytes = 32
	sessionSecretBytes   = 32
	sessionIdleSeconds   = int64(30 * 60)
	sessionAbsolute      = 12 * time.Hour
	maxAccountSessions   = 5
)

type sessionCredentials struct {
	sessionSelector [sessionSelectorBytes]byte
	sessionSecret   [sessionSecretBytes]byte
	csrfSelector    [sessionSelectorBytes]byte
	csrfSecret      [sessionSecretBytes]byte
}

func createSessionCredentials() (sessionCredentials, error) {
	var credentials sessionCredentials
	if _, err := io.ReadFull(rand.Reader, credentials.sessionSelector[:]); err != nil {
		return sessionCredentials{}, err
	}
	if _, err := io.ReadFull(rand.Reader, credentials.sessionSecret[:]); err != nil {
		return sessionCredentials{}, err
	}
	if _, err := io.ReadFull(rand.Reader, credentials.csrfSelector[:]); err != nil {
		return sessionCredentials{}, err
	}
	if _, err := io.ReadFull(rand.Reader, credentials.csrfSecret[:]); err != nil {
		return sessionCredentials{}, err
	}
	return credentials, nil
}

func credentialPair(selector, secret []byte) string {
	return base64.RawURLEncoding.EncodeToString(selector) + "." + base64.RawURLEncoding.EncodeToString(secret)
}

func decodeSessionPart(value string) ([]byte, error) {
	if len(value) != base64.RawURLEncoding.EncodedLen(sessionSelectorBytes) {
		return nil, ErrAuthentication
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != sessionSelectorBytes {
		clear(decoded)
		return nil, ErrAuthentication
	}
	return decoded, nil
}

func readEpoch(ctx context.Context, q accountQueryer, key string) (uint64, error) {
	var value int64
	err := q.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key=?", key).Scan(&value)
	if err != nil {
		return 0, err
	}
	if value < 0 {
		return 0, errors.New("invalid identity epoch")
	}
	return uint64(value), nil
}

func accountByLogin(ctx context.Context, q accountQueryer, login string) (Account, error) {
	if login == "" {
		return Account{}, ErrNotFound
	}
	var id string
	err := q.QueryRowContext(ctx, "SELECT id FROM accounts WHERE login=?", login).Scan(&id)
	if err == sql.ErrNoRows {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, err
	}
	return accountByID(ctx, q, id)
}

func (s *store) AuthenticatePassword(ctx context.Context, login, password, source string, authorizationEpoch uint64) (LoginResult, error) {
	return s.AuthenticatePasswordWithIntent(ctx, login, password, source, authorizationEpoch, nil)
}

func (s *store) AuthenticatePasswordWithIntent(ctx context.Context, login, password, source string, authorizationEpoch uint64, beforeSession func(Account) error) (LoginResult, error) {
	if err := ctx.Err(); err != nil {
		return LoginResult{}, err
	}
	loginHash := s.loginBucketHash(login)
	sources, sourceCount, err := s.sourceBuckets(source)
	if err != nil {
		return LoginResult{}, ErrAuthentication
	}
	if err = s.beginLogin(ctx, loginHash, sources, sourceCount, time.Now().Unix()); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return LoginResult{}, err
		}
		return LoginResult{}, ErrRateLimited
	}
	canonical, canonicalErr := canonicalLogin(login)
	account, accountErr := Account{}, canonicalErr
	if canonicalErr == nil {
		account, accountErr = accountByLogin(ctx, s.db, canonical)
	}
	if accountErr != nil && !errors.Is(accountErr, ErrNotFound) && !errors.Is(accountErr, ErrInvalid) {
		return LoginResult{}, accountErr
	}
	accountID := ""
	var accountEpoch, passwordEpoch uint64
	if accountErr == nil {
		accountID = account.ID
		accountEpoch, err = readEpoch(ctx, s.db, "account_epoch:"+account.ID)
		if err != nil {
			return LoginResult{}, err
		}
		passwordEpoch, err = readEpoch(ctx, s.db, "password_epoch:"+account.ID)
		if err != nil {
			return LoginResult{}, err
		}
	}
	var candidate []byte
	if len(password) <= 512 {
		candidate = []byte(password)
	}
	var valid bool
	var verifyErr error
	if accountErr == nil {
		valid, verifyErr = s.verifyPasswordForAccount(ctx, accountID, &account, candidate)
	} else {
		valid, verifyErr = s.VerifyPassword(ctx, "", candidate)
	}
	clear(candidate)
	if verifyErr != nil {
		if errors.Is(verifyErr, ErrHashOverloaded) || errors.Is(verifyErr, ErrHashTimeout) {
			return LoginResult{}, ErrRateLimited
		}
		if errors.Is(verifyErr, context.Canceled) || errors.Is(verifyErr, context.DeadlineExceeded) {
			return LoginResult{}, verifyErr
		}
		return LoginResult{}, verifyErr
	}
	if !valid || accountErr != nil || account.Kind != Human || !account.Enabled {
		if err = s.recordLoginFailure(ctx, loginHash, sources, sourceCount, time.Now().Unix()); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return LoginResult{}, err
			}
			return LoginResult{}, ErrRateLimited
		}
		return LoginResult{}, ErrAuthentication
	}
	if err = s.recordLoginSuccess(ctx, loginHash, time.Now().Unix()); err != nil {
		return LoginResult{}, ErrRateLimited
	}
	if beforeSession != nil {
		if err = beforeSession(account); err != nil {
			return LoginResult{}, err
		}
	}
	return s.createSession(ctx, account.ID, accountEpoch, passwordEpoch, authorizationEpoch)
}

func (s *store) createSession(ctx context.Context, accountID string, accountEpoch, passwordEpoch, authorizationEpoch uint64) (LoginResult, error) {
	credentials, err := createSessionCredentials()
	if err != nil {
		return LoginResult{}, err
	}
	defer clear(credentials.sessionSecret[:])
	defer clear(credentials.csrfSecret[:])
	sessionHash := sha256.Sum256(credentials.sessionSecret[:])
	csrfHash := sha256.Sum256(credentials.csrfSecret[:])
	nowTime := time.Now()
	now := nowTime.Unix()
	createdAt := nowTime.UnixNano()
	expires := nowTime.Add(sessionAbsolute).UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LoginResult{}, err
	}
	defer tx.Rollback()
	account, err := accountByID(ctx, tx, accountID)
	if errors.Is(err, ErrNotFound) {
		return LoginResult{}, ErrAuthentication
	}
	if err != nil {
		return LoginResult{}, err
	}
	if !account.Enabled || account.Kind != Human {
		return LoginResult{}, ErrAuthentication
	}
	currentAccountEpoch, err := readEpoch(ctx, tx, "account_epoch:"+accountID)
	if err != nil {
		return LoginResult{}, err
	}
	currentPasswordEpoch, err := readEpoch(ctx, tx, "password_epoch:"+accountID)
	if err != nil {
		return LoginResult{}, err
	}
	if currentAccountEpoch != accountEpoch || currentPasswordEpoch != passwordEpoch {
		return LoginResult{}, ErrAuthentication
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE account_id=? AND
		(expires_at<=? OR last_seen_at<=? OR account_epoch<>? OR password_epoch<>? OR auth_epoch<>?)`, accountID, now, now-sessionIdleSeconds, accountEpoch, passwordEpoch, authorizationEpoch); err != nil {
		return LoginResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE account_id=? AND session_selector IN
		(SELECT session_selector FROM sessions WHERE account_id=? ORDER BY created_at DESC,session_selector DESC LIMIT -1 OFFSET ?)`, accountID, accountID, maxAccountSessions-1); err != nil {
		return LoginResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sessions(session_selector,session_hash,csrf_selector,csrf_hash,account_id,auth_epoch,account_epoch,password_epoch,created_at,last_seen_at,expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, credentials.sessionSelector[:], sessionHash[:], credentials.csrfSelector[:], csrfHash[:], accountID, authorizationEpoch, accountEpoch, passwordEpoch, createdAt, now, expires.Unix()); err != nil {
		return LoginResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{
		Account:       account,
		SessionCookie: credentialPair(credentials.sessionSelector[:], credentials.sessionSecret[:]),
		CSRFSecret:    credentialPair(credentials.csrfSelector[:], credentials.csrfSecret[:]),
		ExpiresAt:     expires,
	}, nil
}

// AuthenticateSession checks a browser session. A request that is not activity (a poll) leaves its idle time running.
func (s *store) AuthenticateSession(ctx context.Context, selector, secret string, authorizationEpoch uint64, activity bool) (Principal, error) {
	if err := ctx.Err(); err != nil {
		return Principal{}, err
	}
	selectorBytes, err := decodeSessionPart(selector)
	if err != nil {
		return Principal{}, ErrAuthentication
	}
	secretBytes, err := decodeSessionPart(secret)
	if err != nil {
		return Principal{}, ErrAuthentication
	}
	defer clear(secretBytes)
	providedHash := sha256.Sum256(secretBytes)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Principal{}, err
	}
	defer tx.Rollback()
	var storedHash []byte
	var accountID string
	var storedAuthEpoch, storedAccountEpoch, storedPasswordEpoch int64
	var lastSeen, expires int64
	err = tx.QueryRowContext(ctx, `SELECT session_hash,account_id,auth_epoch,account_epoch,password_epoch,last_seen_at,expires_at
		FROM sessions WHERE session_selector=?`, selectorBytes).Scan(&storedHash, &accountID, &storedAuthEpoch, &storedAccountEpoch, &storedPasswordEpoch, &lastSeen, &expires)
	if err == sql.ErrNoRows {
		return Principal{}, ErrAuthentication
	}
	if err != nil {
		return Principal{}, err
	}
	now := time.Now().Unix()
	if len(storedHash) != sha256.Size || storedAuthEpoch < 0 || storedAccountEpoch < 0 || storedPasswordEpoch < 0 || expires <= now || lastSeen <= now-sessionIdleSeconds {
		if _, err = tx.ExecContext(ctx, "DELETE FROM sessions WHERE session_selector=?", selectorBytes); err != nil {
			return Principal{}, err
		}
		if err = tx.Commit(); err != nil {
			return Principal{}, err
		}
		return Principal{}, ErrAuthentication
	}
	if subtle.ConstantTimeCompare(providedHash[:], storedHash) != 1 {
		return Principal{}, ErrAuthentication
	}
	account, err := accountByID(ctx, tx, accountID)
	if errors.Is(err, ErrNotFound) {
		return Principal{}, ErrAuthentication
	}
	if err != nil {
		return Principal{}, err
	}
	accountEpoch, err := readEpoch(ctx, tx, "account_epoch:"+accountID)
	if err != nil {
		return Principal{}, err
	}
	passwordEpoch, err := readEpoch(ctx, tx, "password_epoch:"+accountID)
	if err != nil {
		return Principal{}, err
	}
	if !account.Enabled || account.Kind != Human || uint64(storedAuthEpoch) != authorizationEpoch || uint64(storedAccountEpoch) != accountEpoch || uint64(storedPasswordEpoch) != passwordEpoch {
		if _, err = tx.ExecContext(ctx, "DELETE FROM sessions WHERE session_selector=?", selectorBytes); err != nil {
			return Principal{}, err
		}
		if err = tx.Commit(); err != nil {
			return Principal{}, err
		}
		return Principal{}, ErrAuthentication
	}
	if activity {
		if _, err = tx.ExecContext(ctx, "UPDATE sessions SET last_seen_at=? WHERE session_selector=?", now, selectorBytes); err != nil {
			return Principal{}, err
		}
		if err = tx.Commit(); err != nil {
			return Principal{}, err
		}
	}
	return Principal{
		Account: account, Actor: authz.Actor{Kind: authz.ActorAccount, ID: accountID}, SessionSelector: selector,
		AuthorizationEpoch: authorizationEpoch, SessionExpiresAt: time.Unix(expires, 0).UTC(),
	}, nil
}

func (s *store) ValidateCSRF(ctx context.Context, sessionSelector, sessionSecret, csrfSelector, csrfSecret string) error {
	selectorBytes, err := decodeSessionPart(sessionSelector)
	if err != nil {
		return ErrAuthentication
	}
	sessionSecretBytes, err := decodeSessionPart(sessionSecret)
	if err != nil {
		return ErrAuthentication
	}
	defer clear(sessionSecretBytes)
	csrfSelectorBytes, err := decodeSessionPart(csrfSelector)
	if err != nil {
		return ErrAuthentication
	}
	csrfSecretBytes, err := decodeSessionPart(csrfSecret)
	if err != nil {
		return ErrAuthentication
	}
	defer clear(csrfSecretBytes)
	providedSessionHash := sha256.Sum256(sessionSecretBytes)
	providedCSRFHash := sha256.Sum256(csrfSecretBytes)
	var storedSessionHash, storedCSRFSelector, storedCSRFHash []byte
	var lastSeen, expires int64
	err = s.db.QueryRowContext(ctx, `SELECT session_hash,csrf_selector,csrf_hash,last_seen_at,expires_at
		FROM sessions WHERE session_selector=?`, selectorBytes).Scan(&storedSessionHash, &storedCSRFSelector, &storedCSRFHash, &lastSeen, &expires)
	if err == sql.ErrNoRows {
		return ErrAuthentication
	}
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	if len(storedSessionHash) != sha256.Size || len(storedCSRFSelector) != sessionSelectorBytes || len(storedCSRFHash) != sha256.Size ||
		subtle.ConstantTimeCompare(providedSessionHash[:], storedSessionHash) != 1 ||
		subtle.ConstantTimeCompare(csrfSelectorBytes, storedCSRFSelector) != 1 ||
		subtle.ConstantTimeCompare(providedCSRFHash[:], storedCSRFHash) != 1 || expires <= now || lastSeen <= now-sessionIdleSeconds {
		return ErrAuthentication
	}
	return nil
}

func (s *store) RotateCSRF(ctx context.Context, sessionSelector, sessionSecret string, authorizationEpoch uint64) (string, error) {
	if _, err := s.AuthenticateSession(ctx, sessionSelector, sessionSecret, authorizationEpoch, true); err != nil {
		return "", err
	}
	selectorBytes, err := decodeSessionPart(sessionSelector)
	if err != nil {
		return "", ErrAuthentication
	}
	secretBytes, err := decodeSessionPart(sessionSecret)
	if err != nil {
		return "", ErrAuthentication
	}
	defer clear(secretBytes)
	sessionHash := sha256.Sum256(secretBytes)
	var csrfSelector [sessionSelectorBytes]byte
	var csrfSecret [sessionSecretBytes]byte
	if _, err = io.ReadFull(rand.Reader, csrfSelector[:]); err != nil {
		return "", err
	}
	if _, err = io.ReadFull(rand.Reader, csrfSecret[:]); err != nil {
		return "", err
	}
	defer clear(csrfSecret[:])
	csrfHash := sha256.Sum256(csrfSecret[:])
	now := time.Now().Unix()
	result, err := s.db.ExecContext(ctx, `UPDATE sessions SET csrf_selector=?,csrf_hash=?,last_seen_at=?
		WHERE session_selector=? AND session_hash=? AND expires_at>? AND last_seen_at>?`, csrfSelector[:], csrfHash[:], now, selectorBytes, sessionHash[:], now, now-sessionIdleSeconds)
	if err != nil {
		return "", err
	}
	if rows, err := result.RowsAffected(); err != nil {
		return "", err
	} else if rows != 1 {
		return "", ErrAuthentication
	}
	return credentialPair(csrfSelector[:], csrfSecret[:]), nil
}

func (s *store) Logout(ctx context.Context, selector, secret string) error {
	return s.LogoutWithIntent(ctx, selector, secret, nil)
}

func (s *store) LogoutWithIntent(ctx context.Context, selector, secret string, beforeDelete func(string) error) error {
	selectorBytes, err := decodeSessionPart(selector)
	if err != nil {
		return nil
	}
	secretBytes, err := decodeSessionPart(secret)
	if err != nil {
		return nil
	}
	defer clear(secretBytes)
	providedHash := sha256.Sum256(secretBytes)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var storedHash []byte
	err = tx.QueryRowContext(ctx, "SELECT session_hash FROM sessions WHERE session_selector=?", selectorBytes).Scan(&storedHash)
	if err == sql.ErrNoRows {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if len(storedHash) == sha256.Size && subtle.ConstantTimeCompare(providedHash[:], storedHash) == 1 {
		if beforeDelete != nil {
			if err = beforeDelete(selector); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM sessions WHERE session_selector=?", selectorBytes); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *store) ListSessions(ctx context.Context, accountID string, authorizationEpoch uint64) ([]Session, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	account, err := accountByID(ctx, tx, accountID)
	if err != nil {
		return nil, err
	}
	if !account.Enabled || account.Kind != Human {
		return nil, ErrAuthentication
	}
	accountEpoch, err := readEpoch(ctx, tx, "account_epoch:"+accountID)
	if err != nil {
		return nil, err
	}
	passwordEpoch, err := readEpoch(ctx, tx, "password_epoch:"+accountID)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE account_id=? AND
		(expires_at<=? OR last_seen_at<=? OR account_epoch<>? OR password_epoch<>? OR auth_epoch<>?)`,
		accountID, now, now-sessionIdleSeconds, accountEpoch, passwordEpoch, authorizationEpoch); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT session_selector,created_at,last_seen_at,expires_at
		FROM sessions WHERE account_id=? ORDER BY created_at DESC,session_selector DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sessions := []Session{}
	for rows.Next() {
		var selector []byte
		var createdAt, lastSeenAt, expiresAt int64
		if err = rows.Scan(&selector, &createdAt, &lastSeenAt, &expiresAt); err != nil {
			return nil, err
		}
		if len(selector) != sessionSelectorBytes {
			return nil, errors.New("invalid stored session selector")
		}
		sessions = append(sessions, Session{
			Selector:   base64.RawURLEncoding.EncodeToString(selector),
			CreatedAt:  time.Unix(0, createdAt).UTC(),
			LastSeenAt: time.Unix(lastSeenAt, 0).UTC(),
			ExpiresAt:  time.Unix(expiresAt, 0).UTC(),
		})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return sessions, nil
}

func (s *store) RevokeSession(ctx context.Context, accountID, selector string) error {
	return s.RevokeSessionWithIntent(ctx, accountID, selector, nil)
}

func (s *store) RevokeSessionWithIntent(ctx context.Context, accountID, selector string, beforeDelete func() error) error {
	selectorBytes, err := decodeSessionPart(selector)
	if err != nil {
		return ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var storedAccountID string
	err = tx.QueryRowContext(ctx, "SELECT account_id FROM sessions WHERE session_selector=?", selectorBytes).Scan(&storedAccountID)
	if err == sql.ErrNoRows || storedAccountID != accountID {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if beforeDelete != nil {
		if err = beforeDelete(); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE account_id=? AND session_selector=?", accountID, selectorBytes)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrNotFound
	}
	return tx.Commit()
}
