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
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/andaru-project/andaru/internal/authz"
)

const (
	tokenSelectorBytes = 16
	tokenSecretBytes   = 32
	maxTokenNameRunes  = 128
	maxTokenScopeBytes = 1024
)
var (
	tokenCapabilities = authz.Catalog()
	tokenBuiltInRoles = authz.BuiltIns()
)

func canonicalCapabilities(caps []authz.Capability) ([]authz.Capability, error) {
	if len(caps) > len(tokenCapabilities) {
		return nil, ErrInvalid
	}
	result := slices.Clone(caps)
	slices.Sort(result)
	for i, capability := range result {
		if !slices.Contains(tokenCapabilities, capability) || i > 0 && result[i-1] == capability {
			return nil, ErrInvalid
		}
	}
	return result, nil
}

func encodeTokenScope(scope []authz.Capability) string {
	if len(scope) == 0 {
		return ""
	}
	size := len(scope) - 1
	for _, capability := range scope {
		size += len(capability)
	}
	var encoded strings.Builder
	encoded.Grow(size)
	for i, capability := range scope {
		if i > 0 {
			encoded.WriteByte('\n')
		}
		encoded.WriteString(string(capability))
	}
	return encoded.String()
}

func decodeTokenScope(encoded string) ([]authz.Capability, error) {
	if len(encoded) > maxTokenScopeBytes {
		return nil, ErrInvalid
	}
	if encoded == "" {
		return []authz.Capability{}, nil
	}
	parts := strings.Split(encoded, "\n")
	result := make([]authz.Capability, len(parts))
	for i, part := range parts {
		result[i] = authz.Capability(part)
	}
	return canonicalCapabilities(result)
}

func (s *store) IssueToken(ctx context.Context, actor authz.Actor, issuerCaps, targetCaps []authz.Capability, input TokenIssue) (IssuedToken, error) {
	if err := ctx.Err(); err != nil {
		return IssuedToken{}, err
	}
	if input.AccountID == "" || !utf8.ValidString(input.Name) || utf8.RuneCountInString(input.Name) < 1 || utf8.RuneCountInString(input.Name) > maxTokenNameRunes || strings.IndexByte(input.Name, 0) >= 0 || input.NoExpiry && input.ExpiresAt != nil {
		return IssuedToken{}, ErrInvalid
	}
	scope, err := canonicalCapabilities(input.Scope)
	if err != nil {
		return IssuedToken{}, err
	}
	for _, capability := range scope {
		if !slices.Contains(issuerCaps, capability) || !slices.Contains(targetCaps, capability) {
			return IssuedToken{}, ErrUnauthorized
		}
	}
	issuerAccountID := actor.ID
	switch actor.Kind {
	case authz.ActorSystem:
		issuerAccountID = ""
	case authz.ActorAccount:
		if actor.ID == "" {
			return IssuedToken{}, ErrUnauthorized
		}
	case authz.ActorToken:
		actorSelector, selectorErr := decodeTokenSelector(actor.ID)
		if selectorErr != nil {
			return IssuedToken{}, ErrUnauthorized
		}
		var revoked, expiry sql.NullInt64
		err = s.db.QueryRowContext(ctx, `SELECT t.account_id,t.revoked_at,t.expires_at FROM api_tokens t
			JOIN accounts a ON a.id=t.account_id WHERE t.selector=? AND a.enabled=1`, actorSelector).
			Scan(&issuerAccountID, &revoked, &expiry)
		if err == sql.ErrNoRows || revoked.Valid || expiry.Valid && expiry.Int64 <= time.Now().Unix() {
			return IssuedToken{}, ErrUnauthorized
		}
		if err != nil {
			return IssuedToken{}, err
		}
	default:
		return IssuedToken{}, ErrUnauthorized
	}
	canManageAll := actor.Kind == authz.ActorSystem || slices.Contains(issuerCaps, authz.TokensManageAll)
	self := actor.Kind != authz.ActorSystem && issuerAccountID == input.AccountID
	if !canManageAll && !self {
		return IssuedToken{}, ErrUnauthorized
	}
	if self && !canManageAll && !slices.Contains(issuerCaps, authz.TokensManageSelf) {
		return IssuedToken{}, ErrUnauthorized
	}

	now := time.Now()
	nowUnix := now.Unix()
	var expiresAt *time.Time
	if input.ExpiresAt != nil {
		if input.NoExpiry || input.ExpiresAt.Unix() <= nowUnix || input.ExpiresAt.After(now.Add(365*24*time.Hour)) {
			return IssuedToken{}, ErrInvalid
		}
		expires := time.Unix(input.ExpiresAt.Unix(), 0).UTC()
		expiresAt = &expires
	} else if !input.NoExpiry {
		expires := now.Add(90 * 24 * time.Hour).UTC().Truncate(time.Second)
		expiresAt = &expires
	}
	revokeOnPasswordChange := true
	if input.RevokeOnPasswordChange != nil {
		revokeOnPasswordChange = *input.RevokeOnPasswordChange
	}
	var selector [tokenSelectorBytes]byte
	var secret [tokenSecretBytes]byte
	if _, err = io.ReadFull(rand.Reader, selector[:]); err != nil {
		return IssuedToken{}, err
	}
	if _, err = io.ReadFull(rand.Reader, secret[:]); err != nil {
		return IssuedToken{}, err
	}
	defer clear(secret[:])
	secretHash := sha256.Sum256(secret[:])

	revision, err := s.Revision(ctx)
	if err != nil {
		return IssuedToken{}, err
	}
	tx, err := s.beginMutation(ctx, revision)
	if err != nil {
		return IssuedToken{}, err
	}
	defer tx.Rollback()
	target, err := accountByID(ctx, tx, input.AccountID)
	if err != nil {
		return IssuedToken{}, err
	}
	if actor.Kind == authz.ActorToken {
		actorSelector, selectorErr := decodeTokenSelector(actor.ID)
		if selectorErr != nil {
			return IssuedToken{}, ErrUnauthorized
		}
		var currentIssuerID string
		err = tx.QueryRowContext(ctx, `SELECT t.account_id FROM api_tokens t JOIN accounts a ON a.id=t.account_id
			WHERE t.selector=? AND t.revoked_at IS NULL AND (t.expires_at IS NULL OR t.expires_at>?) AND a.enabled=1`, actorSelector, nowUnix).Scan(&currentIssuerID)
		if err == sql.ErrNoRows {
			return IssuedToken{}, ErrUnauthorized
		}
		if err != nil {
			return IssuedToken{}, err
		}
		if currentIssuerID != issuerAccountID {
			return IssuedToken{}, ErrUnauthorized
		}
	}
	if !target.Enabled {
		return IssuedToken{}, ErrUnauthorized
	}
	if target.Kind == Service {
		if input.RevokeOnPasswordChange != nil {
			return IssuedToken{}, ErrInvalid
		}
		revokeOnPasswordChange = false
	}
	if self && !canManageAll && target.Kind != Human {
		return IssuedToken{}, ErrUnauthorized
	}
	var active, issued int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM api_tokens WHERE account_id=? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>?)", input.AccountID, nowUnix).Scan(&active); err != nil {
		return IssuedToken{}, err
	}
	if active >= MaxTokensPerAccount {
		return IssuedToken{}, ErrTokenLimit
	}
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM api_tokens WHERE account_id=? AND created_at>=?", input.AccountID, nowUnix-365*24*60*60).Scan(&issued); err != nil {
		return IssuedToken{}, err
	}
	if issued >= MaxTokenIssuesYear {
		return IssuedToken{}, ErrTokenLimit
	}
	passwordEpoch, err := readEpoch(ctx, tx, "password_epoch:"+input.AccountID)
	if err != nil {
		return IssuedToken{}, err
	}
	nextRevision := uint64(revision + 1)
	var expiry any
	if expiresAt != nil {
		expiry = expiresAt.Unix()
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO api_tokens(selector,account_id,secret_hash,name,scopes,created_at,expires_at,revoke_on_password_change,password_epoch,revision)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, selector[:], input.AccountID, secretHash[:], input.Name, encodeTokenScope(scope), nowUnix, expiry, revokeOnPasswordChange, passwordEpoch, nextRevision); err != nil {
		return IssuedToken{}, err
	}
	committedRevision, err := finishMutation(ctx, tx)
	if err != nil {
		return IssuedToken{}, err
	}
	if uint64(committedRevision) != nextRevision {
		return IssuedToken{}, ErrConflict
	}
	selectorText := base64.RawURLEncoding.EncodeToString(selector[:])
	secretText := base64.RawURLEncoding.EncodeToString(secret[:])
	return IssuedToken{
		Selector: selectorText,
		Secret: secretText,
		Metadata: Token{
			Selector: selectorText,
			AccountID: input.AccountID,
			Name: input.Name,
			Scope: slices.Clone(scope),
			CreatedAt: time.Unix(nowUnix, 0).UTC(),
			ExpiresAt: expiresAt,
			NoExpiry: expiresAt == nil,
			RevokeOnPasswordChange: revokeOnPasswordChange,
			PasswordEpoch: passwordEpoch,
			Revision: nextRevision,
		},
	}, nil
}

func decodeTokenSelector(selector string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(selector)
	if err != nil || len(decoded) != tokenSelectorBytes {
		return nil, ErrAuthentication
	}
	return decoded, nil
}

func decodeTokenSecret(secret string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil || len(decoded) != tokenSecretBytes {
		clear(decoded)
		return nil, ErrAuthentication
	}
	return decoded, nil
}

func tokenFromColumns(selector []byte, accountID, name, encodedScope string, created int64, expiry, revoked, used sql.NullInt64, revoke int, passwordEpoch, revision int64) (Token, error) {
	if len(selector) != tokenSelectorBytes || created < 0 || passwordEpoch < 0 || revision < 0 || expiry.Valid && expiry.Int64 < 0 || revoked.Valid && revoked.Int64 < 0 || used.Valid && used.Int64 < 0 {
		return Token{}, errors.New("invalid stored identity token")
	}
	scope, err := decodeTokenScope(encodedScope)
	if err != nil {
		return Token{}, err
	}
	token := Token{
		Selector: base64.RawURLEncoding.EncodeToString(selector),
		AccountID: accountID,
		Name: name,
		Scope: scope,
		CreatedAt: time.Unix(created, 0).UTC(),
		NoExpiry: !expiry.Valid,
		RevokeOnPasswordChange: revoke == 1,
		PasswordEpoch: uint64(passwordEpoch),
		Revision: uint64(revision),
	}
	if expiry.Valid {
		value := time.Unix(expiry.Int64, 0).UTC()
		token.ExpiresAt = &value
	}
	if revoked.Valid {
		value := time.Unix(revoked.Int64, 0).UTC()
		token.RevokedAt = &value
	}
	if used.Valid {
		value := time.Unix(used.Int64, 0).UTC()
		token.LastUsedAt = &value
	}
	return token, nil
}

func readToken(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, selector []byte) (Token, []byte, bool, error) {
	var accountID, name, scope string
	var secretHash []byte
	var created int64
	var expiry, revoked, used sql.NullInt64
	var revoke int
	var passwordEpoch, revision int64
	err := q.QueryRowContext(ctx, `SELECT account_id,secret_hash,name,scopes,created_at,expires_at,revoked_at,last_used_at,revoke_on_password_change,password_epoch,revision
		FROM api_tokens WHERE selector=?`, selector).
		Scan(&accountID, &secretHash, &name, &scope, &created, &expiry, &revoked, &used, &revoke, &passwordEpoch, &revision)
	if err != nil {
		return Token{}, nil, false, err
	}
	if len(secretHash) != sha256.Size {
		return Token{}, nil, false, errors.New("invalid stored identity token")
	}
	token, err := tokenFromColumns(selector, accountID, name, scope, created, expiry, revoked, used, revoke, passwordEpoch, revision)
	if err != nil {
		return Token{}, nil, false, err
	}
	return token, secretHash, revoked.Valid, nil
}

func (s *store) ListTokens(ctx context.Context, accountID string) ([]Token, error) {
	if _, err := s.GetAccount(ctx, accountID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT selector,account_id,name,scopes,created_at,expires_at,revoked_at,last_used_at,revoke_on_password_change,password_epoch,revision
		FROM api_tokens WHERE account_id=? ORDER BY created_at DESC,selector`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Token, 0)
	for rows.Next() {
		var selector []byte
		var tokenAccountID, name, scope string
		var created int64
		var expiry, revoked, used sql.NullInt64
		var revoke int
		var passwordEpoch, revision int64
		if err = rows.Scan(&selector, &tokenAccountID, &name, &scope, &created, &expiry, &revoked, &used, &revoke, &passwordEpoch, &revision); err != nil {
			return nil, err
		}
		token, scanErr := tokenFromColumns(selector, tokenAccountID, name, scope, created, expiry, revoked, used, revoke, passwordEpoch, revision)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, token)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *store) RevokeToken(ctx context.Context, selector string) error {
	selectorBytes, err := decodeTokenSelector(selector)
	if err != nil {
		return ErrNotFound
	}
	revision, err := s.Revision(ctx)
	if err != nil {
		return err
	}
	tx, err := s.beginMutation(ctx, revision)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var revoked sql.NullInt64
	err = tx.QueryRowContext(ctx, "SELECT revoked_at FROM api_tokens WHERE selector=?", selectorBytes).Scan(&revoked)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if revoked.Valid {
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, "UPDATE api_tokens SET revoked_at=? WHERE selector=? AND revoked_at IS NULL", time.Now().Unix(), selectorBytes); err != nil {
		return err
	}
	_, err = finishMutation(ctx, tx)
	return err
}

func (s *store) AuthenticateToken(ctx context.Context, selector, secret string, roles authz.Snapshot) (Principal, error) {
	if err := ctx.Err(); err != nil {
		return Principal{}, err
	}
	selectorBytes, err := decodeTokenSelector(selector)
	if err != nil {
		return Principal{}, ErrAuthentication
	}
	secretBytes, err := decodeTokenSecret(secret)
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
	token, storedHash, revoked, err := readToken(ctx, tx, selectorBytes)
	if err == sql.ErrNoRows {
		return Principal{}, ErrAuthentication
	}
	if err != nil {
		return Principal{}, err
	}
	if subtle.ConstantTimeCompare(providedHash[:], storedHash) != 1 || revoked || token.ExpiresAt != nil && !token.ExpiresAt.After(time.Now()) {
		return Principal{}, ErrAuthentication
	}
	account, err := accountByID(ctx, tx, token.AccountID)
	if errors.Is(err, ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrAuthentication
	}
	if err != nil {
		return Principal{}, err
	}
	if !account.Enabled {
		return Principal{}, ErrAuthentication
	}
	if token.RevokeOnPasswordChange {
		currentEpoch, epochErr := readEpoch(ctx, tx, "password_epoch:"+account.ID)
		if epochErr != nil {
			return Principal{}, epochErr
		}
		if currentEpoch != token.PasswordEpoch {
			return Principal{}, ErrAuthentication
		}
	}
	capabilities := intersectCapabilities(token.Scope, effectiveCapabilities(account.RoleIDs, roles))
	now := time.Now().Unix()
	if token.LastUsedAt == nil || token.LastUsedAt.Unix() <= now-3600 {
		result, updateErr := tx.ExecContext(ctx, "UPDATE api_tokens SET last_used_at=? WHERE selector=? AND revoked_at IS NULL AND (last_used_at IS NULL OR last_used_at<=?)", now, selectorBytes, now-3600)
		if updateErr != nil {
			return Principal{}, updateErr
		}
		if changed, updateErr := result.RowsAffected(); updateErr != nil {
			return Principal{}, updateErr
		} else if changed == 1 {
			used := time.Unix(now, 0).UTC()
			token.LastUsedAt = &used
		}
	}
	if err = tx.Commit(); err != nil {
		return Principal{}, err
	}
	return Principal{Account: account, Actor: authz.Actor{Kind: authz.ActorToken, ID: selector}, Capabilities: capabilities, Token: &token}, nil
}

func effectiveCapabilities(roleIDs []string, snapshot authz.Snapshot) []authz.Capability {
	result := make([]authz.Capability, 0, len(tokenCapabilities))
	for _, roleID := range roleIDs {
		var capabilities []authz.Capability
		for _, role := range tokenBuiltInRoles {
			if role.ID == roleID {
				capabilities = role.Capabilities
				break
			}
		}
		if capabilities == nil {
			capabilities = snapshot.Capabilities[roleID]
		}
		for _, capability := range capabilities {
			if !slices.Contains(result, capability) {
				result = append(result, capability)
			}
		}
	}
	slices.Sort(result)
	return result
}

func intersectCapabilities(scope, current []authz.Capability) []authz.Capability {
	result := make([]authz.Capability, 0, min(len(scope), len(current)))
	for _, capability := range scope {
		if slices.Contains(current, capability) {
			result = append(result, capability)
		}
	}
	return result
}
