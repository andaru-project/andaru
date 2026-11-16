package identity

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/authz"
)

func splitCredential(t *testing.T, value string) (string, string) {
	t.Helper()
	parts := strings.Split(value, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		t.Fatalf("credential is not selector.secret")
	}
	return parts[0], parts[1]
}

func issueTestToken(s *store, accountID string, scopes []authz.Capability, revokeOnPasswordChange *bool) (IssuedToken, error) {
	issuerCaps := append(append([]authz.Capability(nil), scopes...), authz.TokensManageSelf)
	return s.IssueToken(context.Background(), authz.Actor{Kind: authz.ActorAccount, ID: accountID}, issuerCaps, scopes, TokenIssue{
		AccountID: accountID, Name: "automation", Scope: scopes, RevokeOnPasswordChange: revokeOnPasswordChange,
	})
}

func TestTokenScopeExpiryRevocationAndRestart(t *testing.T) {
	ctx := context.Background()
	s, stateDir := openTestStore(t)
	admin, _, err := s.BootstrapAdmin(ctx, "rootadmin", "Administrator", []byte("unique-admin-password-41"))
	if err != nil {
		t.Fatal(err)
	}
	read := []authz.Capability{authz.ConfigurationRead}
	issued, err := issueTestToken(s, admin.ID, read, nil)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Selector == "" || issued.Secret == "" || issued.Metadata.ExpiresAt == nil || issued.Metadata.NoExpiry || !issued.Metadata.RevokeOnPasswordChange {
		t.Fatalf("default token metadata was incomplete: %+v", issued.Metadata)
	}
	if got := issued.Metadata.ExpiresAt.Sub(time.Now()); got < 89*24*time.Hour || got > 91*24*time.Hour {
		t.Fatalf("default token lifetime = %s", got)
	}
	selectorBytes, err := base64.RawURLEncoding.DecodeString(issued.Selector)
	if err != nil || len(selectorBytes) != 16 {
		t.Fatalf("selector is not a 128-bit value: %v", err)
	}
	secretBytes, err := base64.RawURLEncoding.DecodeString(issued.Secret)
	if err != nil || len(secretBytes) != 32 {
		t.Fatalf("secret is not a 256-bit value: %v", err)
	}
	wantHash := sha256.Sum256(secretBytes)
	var storedHash []byte
	if err = s.db.QueryRowContext(ctx, "SELECT secret_hash FROM api_tokens WHERE selector=?", selectorBytes).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if string(storedHash) != string(wantHash[:]) || strings.Contains(fmt.Sprint(issued.Metadata), issued.Secret) {
		t.Fatal("token secret was not stored only as its SHA-256 hash")
	}
	roles := authz.Snapshot{Capabilities: map[string][]authz.Capability{authz.AdministratorRoleID: {authz.ConfigurationRead}}}
	principal, err := s.AuthenticateToken(ctx, issued.Selector, issued.Secret, roles)
	if err != nil || len(principal.Capabilities) != 1 || principal.Capabilities[0] != authz.ConfigurationRead {
		t.Fatalf("token authentication=%+v err=%v", principal, err)
	}
	roleID := "rol_12345678-1234-4234-8234-123456789abc"
	revision, err := s.Revision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	scopedUser, _, err := s.CreateAccount(ctx, CreateAccountInput{Login: "scopeduser", Kind: Human, Enabled: true, RoleIDs: []string{roleID}}, revision)
	if err != nil {
		t.Fatal(err)
	}
	scopedToken, err := s.IssueToken(ctx, authz.Actor{Kind: authz.ActorSystem, ID: "root"}, read, read, TokenIssue{AccountID: scopedUser.ID, Name: "scoped", Scope: read})
	if err != nil {
		t.Fatal(err)
	}
	scopedRoles := authz.Snapshot{Capabilities: map[string][]authz.Capability{roleID: {authz.ConfigurationRead}}}
	principal, err = s.AuthenticateToken(ctx, scopedToken.Selector, scopedToken.Secret, scopedRoles)
	if err != nil || len(principal.Capabilities) != 1 || principal.Capabilities[0] != authz.ConfigurationRead {
		t.Fatalf("scoped token authentication=%+v err=%v", principal, err)
	}
	shrunk := authz.Snapshot{Capabilities: map[string][]authz.Capability{roleID: {authz.StatusRead}}}
	principal, err = s.AuthenticateToken(ctx, scopedToken.Selector, scopedToken.Secret, shrunk)
	if err != nil || len(principal.Capabilities) != 0 {
		t.Fatalf("token gained capabilities after target authorization changed: %+v err=%v", principal, err)
	}
	revision, err = s.Revision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DisableAccount(ctx, scopedUser.ID, revision); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticateToken(ctx, scopedToken.Selector, scopedToken.Secret, scopedRoles); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("disabled account token authenticated: %v", err)
	}
	revision, err = s.Revision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	service, revision, err := s.CreateAccount(ctx, CreateAccountInput{Login: "tokenservice", Kind: Service, Enabled: true, RoleIDs: []string{roleID}}, revision)
	if err != nil {
		t.Fatal(err)
	}
	serviceToken, err := s.IssueToken(ctx, authz.Actor{Kind: authz.ActorSystem, ID: "root"}, read, read, TokenIssue{
		AccountID: service.ID, Name: "service", Scope: read,
	})
	if err != nil || serviceToken.Metadata.RevokeOnPasswordChange {
		t.Fatalf("service token metadata=%+v err=%v", serviceToken.Metadata, err)
	}
	principal, err = s.AuthenticateToken(ctx, serviceToken.Selector, serviceToken.Secret, scopedRoles)
	if err != nil || principal.Account.Kind != Service {
		t.Fatalf("service token authentication=%+v err=%v", principal, err)
	}
	revision, err = s.Revision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	deleteUser, revision, err := s.CreateAccount(ctx, CreateAccountInput{Login: "deleteuser", Kind: Human, Enabled: true, RoleIDs: []string{roleID}}, revision)
	if err != nil {
		t.Fatal(err)
	}
	deleteToken, err := s.IssueToken(ctx, authz.Actor{Kind: authz.ActorSystem, ID: "root"}, read, read, TokenIssue{
		AccountID: deleteUser.ID, Name: "delete-target", Scope: read,
	})
	if err != nil {
		t.Fatal(err)
	}
	revision, err = s.Revision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteAccount(ctx, deleteUser.ID, revision); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticateToken(ctx, deleteToken.Selector, deleteToken.Secret, scopedRoles); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("deleted account token authenticated: %v", err)
	}
	if _, err = s.IssueToken(ctx, authz.Actor{Kind: authz.ActorAccount, ID: admin.ID}, []authz.Capability{authz.ConfigurationWrite, authz.TokensManageSelf}, read, TokenIssue{
		AccountID: admin.ID, Name: "escalation", Scope: []authz.Capability{authz.ConfigurationWrite},
	}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("scope beyond target was accepted: %v", err)
	}
	selfCaps := []authz.Capability{authz.ConfigurationRead, authz.TokensManageSelf}
	noExpiry, err := s.IssueToken(ctx, authz.Actor{Kind: authz.ActorAccount, ID: admin.ID}, selfCaps, read, TokenIssue{
		AccountID: admin.ID, Name: "no-expiry", Scope: read, NoExpiry: true,
	})
	if err != nil || noExpiry.Metadata.ExpiresAt != nil || !noExpiry.Metadata.NoExpiry {
		t.Fatalf("explicit no-expiry token=%+v err=%v", noExpiry.Metadata, err)
	}
	if _, err = s.IssueToken(ctx, authz.Actor{Kind: authz.ActorAccount, ID: admin.ID}, selfCaps, read, TokenIssue{
		AccountID: admin.ID, Name: "too-long", Scope: read, ExpiresAt: func() *time.Time { v := time.Now().Add(366 * 24 * time.Hour); return &v }(),
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("token expiration over one year accepted: %v", err)
	}
	if _, err = s.AuthenticateToken(ctx, issued.Selector, "wrong-secret", roles); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("wrong token secret error=%v", err)
	}
	if _, err = s.AuthenticateToken(ctx, strings.Repeat("A", 1<<20), issued.Secret, roles); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("oversized token selector accepted: %v", err)
	}
	if _, err = s.AuthenticateToken(ctx, issued.Selector, strings.Repeat("A", 1<<20), roles); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("oversized token secret accepted: %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.AuthenticateToken(ctx, issued.Selector, issued.Secret, roles); err != nil {
		t.Fatalf("token did not survive restart: %v", err)
	}
	recentUsed := time.Now().Unix() - 60
	if _, err = s.db.ExecContext(ctx, "UPDATE api_tokens SET last_used_at=? WHERE selector=?", recentUsed, selectorBytes); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticateToken(ctx, issued.Selector, issued.Secret, roles); err != nil {
		t.Fatal(err)
	}
	var recentObserved int64
	if err = s.db.QueryRowContext(ctx, "SELECT last_used_at FROM api_tokens WHERE selector=?", selectorBytes).Scan(&recentObserved); err != nil || recentObserved != recentUsed {
		t.Fatalf("recent last-used timestamp was rewritten: got=%d want=%d err=%v", recentObserved, recentUsed, err)
	}
	staleUsed := time.Now().Unix() - 3601
	if _, err = s.db.ExecContext(ctx, "UPDATE api_tokens SET last_used_at=? WHERE selector=?", staleUsed, selectorBytes); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticateToken(ctx, issued.Selector, issued.Secret, roles); err != nil {
		t.Fatal(err)
	}
	var updated int64
	if err = s.db.QueryRowContext(ctx, "SELECT last_used_at FROM api_tokens WHERE selector=?", selectorBytes).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	if updated <= staleUsed {
		t.Fatalf("stale last-used timestamp did not advance: old=%d updated=%d", staleUsed, updated)
	}
	if _, err = s.AuthenticateToken(ctx, issued.Selector, issued.Secret, roles); err != nil {
		t.Fatal(err)
	}
	var throttled int64
	if err = s.db.QueryRowContext(ctx, "SELECT last_used_at FROM api_tokens WHERE selector=?", selectorBytes).Scan(&throttled); err != nil || throttled != updated {
		t.Fatalf("hourly last-used write was not throttled: %d -> %d err=%v", updated, throttled, err)
	}
	noRevoke := false
	retained, err := issueTestToken(s, admin.ID, read, &noRevoke)
	if err != nil {
		t.Fatal(err)
	}
	revision, err = s.Revision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetPassword(ctx, admin.ID, revision, []byte("changed-admin-password-2026")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticateToken(ctx, issued.Selector, issued.Secret, roles); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("default token survived password change: %v", err)
	}
	if _, err = s.AuthenticateToken(ctx, retained.Selector, retained.Secret, roles); err != nil {
		t.Fatalf("explicitly retained token was revoked: %v", err)
	}
	listed, err := s.ListTokens(ctx, admin.ID)
	if err != nil || len(listed) != 3 {
		t.Fatalf("listed tokens=%d err=%v", len(listed), err)
	}
}

func TestTokenActiveAndAnnualLimits(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	admin, _, err := s.BootstrapAdmin(ctx, "rootadmin", "Administrator", []byte("unique-admin-password-41"))
	if err != nil {
		t.Fatal(err)
	}
	read := []authz.Capability{authz.ConfigurationRead}
	active := make([]IssuedToken, 0, 50)
	for i := range 50 {
		issued, issueErr := issueTestToken(s, admin.ID, read, nil)
		if issueErr != nil {
			t.Fatalf("issue %d: %v", i, issueErr)
		}
		active = append(active, issued)
	}
	if _, err = issueTestToken(s, admin.ID, read, nil); !errors.Is(err, ErrTokenLimit) {
		t.Fatalf("51st active token: %v", err)
	}
	if err = s.RevokeToken(ctx, active[0].Selector); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticateToken(ctx, active[0].Selector, active[0].Secret, authz.Snapshot{}); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("revoked token authenticated: %v", err)
	}
	for _, issued := range active {
		if err = s.RevokeToken(ctx, issued.Selector); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 50 {
		issued, issueErr := issueTestToken(s, admin.ID, read, nil)
		if issueErr != nil {
			t.Fatalf("annual issue %d: %v", i, issueErr)
		}
		if issueErr = s.RevokeToken(ctx, issued.Selector); issueErr != nil {
			t.Fatal(issueErr)
		}
	}
	if _, err = issueTestToken(s, admin.ID, read, nil); !errors.Is(err, ErrTokenLimit) {
		t.Fatalf("101st annual token: %v", err)
	}
}

func TestSessionEpochsTimeoutEvictionCSRFAndRestart(t *testing.T) {
	ctx := context.Background()
	s, stateDir := openTestStore(t)
	_, revision, err := s.BootstrapAdmin(ctx, "rootadmin", "Administrator", []byte("unique-admin-password-41"))
	if err != nil {
		t.Fatal(err)
	}
	user, revision, err := s.CreateAccount(ctx, CreateAccountInput{Login: "operator1", DisplayName: "Operator", Kind: Human, Enabled: true}, revision)
	if err != nil {
		t.Fatal(err)
	}
	revision, err = s.SetPassword(ctx, user.ID, revision, []byte("violet-lemur-session-key-2026"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.AuthenticatePassword(ctx, "operator1", "violet-lemur-session-key-2026", "192.0.2.10", 7)
	if err != nil {
		t.Fatal(err)
	}
	sessionSelector, sessionSecret := splitCredential(t, first.SessionCookie)
	csrfSelector, csrfSecret := splitCredential(t, first.CSRFSecret)
	idBytes, err := base64.RawURLEncoding.DecodeString(sessionSelector)
	if err != nil || len(idBytes) != 32 {
		t.Fatalf("session selector is not 256-bit: %v", err)
	}
	secretBytes, err := base64.RawURLEncoding.DecodeString(sessionSecret)
	if err != nil || len(secretBytes) != 32 {
		t.Fatalf("session secret is not 256-bit: %v", err)
	}
	csrfIDBytes, err := base64.RawURLEncoding.DecodeString(csrfSelector)
	if err != nil || len(csrfIDBytes) != 32 {
		t.Fatalf("CSRF selector is not 256-bit: %v", err)
	}
	csrfBytes, err := base64.RawURLEncoding.DecodeString(csrfSecret)
	if err != nil || len(csrfBytes) != 32 {
		t.Fatalf("CSRF secret is not 256-bit: %v", err)
	}
	var sessionHash, csrfHash []byte
	if err = s.db.QueryRowContext(ctx, "SELECT session_hash,csrf_hash FROM sessions WHERE session_selector=?", idBytes).Scan(&sessionHash, &csrfHash); err != nil {
		t.Fatal(err)
	}
	wantSessionHash, wantCSRFHash := sha256.Sum256(secretBytes), sha256.Sum256(csrfBytes)
	if string(sessionHash) != string(wantSessionHash[:]) || string(csrfHash) != string(wantCSRFHash[:]) {
		t.Fatal("session or CSRF secret was not stored only as its SHA-256 hash")
	}
	wrongSessionSecretBytes := append([]byte(nil), secretBytes...)
	wrongSessionSecretBytes[0] ^= 0xff
	wrongSessionSecret := base64.RawURLEncoding.EncodeToString(wrongSessionSecretBytes)
	if _, err = s.AuthenticateSession(ctx, sessionSelector, wrongSessionSecret, 7); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("wrong session secret authenticated: %v", err)
	}
	if _, err = s.AuthenticateSession(ctx, strings.Repeat("A", 1<<20), sessionSecret, 7); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("oversized session selector accepted: %v", err)
	}
	if _, err = s.AuthenticateSession(ctx, sessionSelector, strings.Repeat("A", 1<<20), 7); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("oversized session secret accepted: %v", err)
	}
	if _, err = s.AuthenticateSession(ctx, sessionSelector, sessionSecret, 7); err != nil {
		t.Fatalf("wrong secret revoked the legitimate session: %v", err)
	}
	if err = s.ValidateCSRF(ctx, sessionSelector, sessionSecret, csrfSelector, csrfSecret); err != nil {
		t.Fatalf("valid CSRF credential rejected: %v", err)
	}
	if err = s.ValidateCSRF(ctx, sessionSelector, sessionSecret, csrfSelector, "wrong-secret"); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("wrong CSRF credential: %v", err)
	}
	rotatedCSRF, err := s.RotateCSRF(ctx, sessionSelector, sessionSecret, 7)
	if err != nil {
		t.Fatal(err)
	}
	rotatedSelector, rotatedSecret := splitCredential(t, rotatedCSRF)
	if err = s.ValidateCSRF(ctx, sessionSelector, sessionSecret, csrfSelector, csrfSecret); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("rotated CSRF credential remained valid: %v", err)
	}
	if err = s.ValidateCSRF(ctx, sessionSelector, sessionSecret, rotatedSelector, rotatedSecret); err != nil {
		t.Fatalf("rotated CSRF credential rejected: %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.AuthenticateSession(ctx, sessionSelector, sessionSecret, 7); err != nil {
		t.Fatalf("session did not survive restart: %v", err)
	}
	if err = s.Logout(ctx, sessionSelector, sessionSecret); err != nil {
		t.Fatal(err)
	}
	if err = s.Logout(ctx, sessionSelector, sessionSecret); err != nil {
		t.Fatalf("logout was not idempotent: %v", err)
	}
	if _, err = s.AuthenticateSession(ctx, sessionSelector, sessionSecret, 7); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("logged-out session authenticated: %v", err)
	}
	epochLogin, err := s.AuthenticatePassword(ctx, "operator1", "violet-lemur-session-key-2026", "192.0.2.11", 7)
	if err != nil {
		t.Fatal(err)
	}
	epochSelector, epochSecret := splitCredential(t, epochLogin.SessionCookie)
	if _, err = s.AuthenticateSession(ctx, epochSelector, epochSecret, 8); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("authorization epoch change did not invalidate session: %v", err)
	}
	if _, err = s.AuthenticateSession(ctx, epochSelector, epochSecret, 7); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("session survived authorization epoch change: %v", err)
	}
	for i := range 6 {
		result, loginErr := s.AuthenticatePassword(ctx, "operator1", "violet-lemur-session-key-2026", fmt.Sprintf("198.51.100.%d", i+1), 7)
		if loginErr != nil {
			t.Fatalf("login %d: %v", i+1, loginErr)
		}
		if i == 0 {
			first = result
		}
	}
	oldSelector, oldSecret := splitCredential(t, first.SessionCookie)
	if _, err = s.AuthenticateSession(ctx, oldSelector, oldSecret, 7); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("oldest session survived sixth login: %v", err)
	}
	var active int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM sessions WHERE account_id=?", user.ID).Scan(&active); err != nil || active != 5 {
		t.Fatalf("active sessions=%d err=%v", active, err)
	}
	idle, err := s.AuthenticatePassword(ctx, "operator1", "violet-lemur-session-key-2026", "203.0.113.1", 7)
	if err != nil {
		t.Fatal(err)
	}
	idleSelector, idleSecret := splitCredential(t, idle.SessionCookie)
	idleBytes, _ := base64.RawURLEncoding.DecodeString(idleSelector)
	if _, err = s.db.ExecContext(ctx, "UPDATE sessions SET last_seen_at=unixepoch()-1801 WHERE session_selector=?", idleBytes); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticateSession(ctx, idleSelector, idleSecret, 7); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("idle session authenticated: %v", err)
	}
	absolute, err := s.AuthenticatePassword(ctx, "operator1", "violet-lemur-session-key-2026", "203.0.113.2", 7)
	if err != nil {
		t.Fatal(err)
	}
	absoluteSelector, absoluteSecret := splitCredential(t, absolute.SessionCookie)
	absoluteBytes, _ := base64.RawURLEncoding.DecodeString(absoluteSelector)
	if _, err = s.db.ExecContext(ctx, "UPDATE sessions SET expires_at=unixepoch()-1 WHERE session_selector=?", absoluteBytes); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticateSession(ctx, absoluteSelector, absoluteSecret, 7); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("absolute-expired session authenticated: %v", err)
	}
	passwordChangeLogin, err := s.AuthenticatePassword(ctx, "operator1", "violet-lemur-session-key-2026", "203.0.113.5", 7)
	if err != nil {
		t.Fatal(err)
	}
	passwordChangeSelector, passwordChangeSecret := splitCredential(t, passwordChangeLogin.SessionCookie)
	revision, err = s.Revision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetPassword(ctx, user.ID, revision, []byte("violet-lemur-changed-key-2026")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticateSession(ctx, passwordChangeSelector, passwordChangeSecret, 7); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("password change retained a live session: %v", err)
	}
	newLogin, err := s.AuthenticatePassword(ctx, "operator1", "violet-lemur-changed-key-2026", "203.0.113.3", 7)
	if err != nil {
		t.Fatal(err)
	}
	roles := []string{authz.ReadOnlyRoleID}
	revision, err = s.Revision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.UpdateAccount(ctx, user.ID, revision, UpdateAccountInput{RoleIDs: &roles}); err != nil {
		t.Fatal(err)
	}
	newSelector, newSecret := splitCredential(t, newLogin.SessionCookie)
	if _, err = s.AuthenticateSession(ctx, newSelector, newSecret, 7); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("role assignment change retained a session: %v", err)
	}
	newLogin, err = s.AuthenticatePassword(ctx, "operator1", "violet-lemur-changed-key-2026", "203.0.113.4", 7)
	if err != nil {
		t.Fatal(err)
	}
	newSelector, newSecret = splitCredential(t, newLogin.SessionCookie)
	revision, err = s.Revision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DisableAccount(ctx, user.ID, revision); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticateSession(ctx, newSelector, newSecret, 7); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("disabled account session authenticated: %v", err)
	}
}

func TestPasswordLoginBucketsIPv6AndCancellation(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	_, revision, err := s.BootstrapAdmin(ctx, "rootadmin", "Administrator", []byte("unique-admin-password-41"))
	if err != nil {
		t.Fatal(err)
	}
	passwordless, revision, err := s.CreateAccount(ctx, CreateAccountInput{Login: "passwordless", DisplayName: "", Kind: Human, Enabled: true}, revision)
	if err != nil {
		t.Fatal(err)
	}
	disabled, revision, err := s.CreateAccount(ctx, CreateAccountInput{Login: "disabled01", DisplayName: "", Kind: Human, Enabled: true}, revision)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DisableAccount(ctx, disabled.ID, revision); err != nil {
		t.Fatal(err)
	}
	revision, err = s.Revision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	service, _, err := s.CreateAccount(ctx, CreateAccountInput{Login: "service01", DisplayName: "", Kind: Service, Enabled: true}, revision)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err = verifyPasswordHash(ctx, dummyPasswordHash, []byte("incorrect-password-2026")); err != nil {
		t.Fatal(err)
	}
	dummyDuration := time.Since(started)
	for _, login := range []string{"ROOTADMIN", "missing01", passwordless.Login, disabled.Login, service.Login} {
		started = time.Now()
		if _, err = s.AuthenticatePassword(ctx, login, "incorrect-password-2026", "192.0.2.20", 1); !errors.Is(err, ErrAuthentication) {
			t.Fatalf("non-generic login result for %q: %v", login, err)
		}
		if elapsed := time.Since(started); elapsed < dummyDuration/10 {
			t.Fatalf("login %q skipped dummy Argon timing class: %s, dummy=%s", login, elapsed, dummyDuration)
		}
	}
	var sessions int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM sessions").Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("failed password flow created a session: count=%d err=%v", sessions, err)
	}
	rawLoginDigest := sha256.Sum256([]byte("rootadmin"))
	var rawLoginRows int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM login_limits WHERE bucket_hash=?", rawLoginDigest[:]).Scan(&rawLoginRows); err != nil || rawLoginRows != 0 {
		t.Fatalf("unkeyed login digest stored: rows=%d err=%v", rawLoginRows, err)
	}
	loginHash := s.loginBucketHash("rootadmin")
	var failures int
	if err = s.db.QueryRowContext(ctx, "SELECT failures FROM login_limits WHERE bucket_hash=?", loginHash[:]).Scan(&failures); err != nil || failures != 1 {
		t.Fatalf("canonical login bucket failures=%d err=%v", failures, err)
	}
	var sourceExpiry int64
	if err = s.db.QueryRowContext(ctx, "SELECT max(expires_at) FROM source_limits").Scan(&sourceExpiry); err != nil || sourceExpiry < time.Now().Unix()+23*60*60 {
		t.Fatalf("source failure row did not retain 24-hour expiry: expires=%d err=%v", sourceExpiry, err)
	}
	if _, err = s.AuthenticatePassword(ctx, "rootadmin", "unique-admin-password-41", "192.0.2.21", 1); err != nil {
		t.Fatalf("successful login: %v", err)
	}
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM login_limits WHERE bucket_hash=?", loginHash[:]).Scan(&failures); err != nil || failures != 0 {
		t.Fatalf("success did not clear login bucket: rows=%d err=%v", failures, err)
	}
	var sourceRows int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM source_limits").Scan(&sourceRows); err != nil || sourceRows == 0 {
		t.Fatalf("successful login erased source abuse history: rows=%d err=%v", sourceRows, err)
	}
	for i := range 5 {
		login := fmt.Sprintf("v6guest%02d", i)
		if _, err = s.AuthenticatePassword(ctx, login, "incorrect-password-2026", fmt.Sprintf("2001:db8:abcd:12::%x", i+1), 1); err == nil {
			t.Fatalf("unknown account %q authenticated", login)
		}
	}
	if _, err = s.AuthenticatePassword(ctx, "v6guest99", "incorrect-password-2026", "2001:db8:abcd:12::ffff", 1); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("IPv6 /64 rotation bypassed source backoff: %v", err)
	}
	for i := range 5 {
		login := fmt.Sprintf("v4guest%02d", i)
		if _, err = s.AuthenticatePassword(ctx, login, "incorrect-password-2026", fmt.Sprintf("198.51.100.%d", i+1), 1); err == nil {
			t.Fatalf("unknown account %q authenticated", login)
		}
	}
	if _, err = s.AuthenticatePassword(ctx, "v4guest99", "incorrect-password-2026", "198.51.100.99", 1); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("unrelated IPv4 addresses were aggregated: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.AuthenticatePassword(cancelled, "cancelled", "incorrect-password-2026", "203.0.113.10", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled login error=%v", err)
	}
}

func TestRollingFailureWindowBoundaryTimestamps(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	loginHash := s.loginBucketHash("boundary")
	var sources [2]sourceBucket
	base := int64(1_000_000)
	tests := []struct {
		offset int64
		count  int
		start  int64
		locked bool
	}{
		{0, 1, 0, false},
		{898, 2, 0, false},
		{899, 3, 0, false},
		{900, 3, 898, false},
		{901, 4, 898, false},
		{902, 5, 898, true},
	}
	for _, test := range tests {
		now := base + test.offset
		if err := s.beginLogin(ctx, loginHash, sources, 0, now); err != nil {
			t.Fatalf("begin at %d: %v", test.offset, err)
		}
		if err := s.recordLoginFailure(ctx, loginHash, sources, 0, now); err != nil {
			t.Fatalf("failure at %d: %v", test.offset, err)
		}
		var failures, level int
		var windowStarted, backoffUntil int64
		var encoded []byte
		if err := s.db.QueryRowContext(ctx, "SELECT failures,failure_times,window_started_at,backoff_level,backoff_until FROM login_limits WHERE bucket_hash=?", loginHash[:]).Scan(&failures, &encoded, &windowStarted, &level, &backoffUntil); err != nil {
			t.Fatal(err)
		}
		if _, err := decodeFailureTimes(encoded, failures, windowStarted); err != nil {
			t.Fatalf("failure timestamps at %d: %v", test.offset, err)
		}
		expectedStart := base + test.start
		if failures != test.count || windowStarted != expectedStart || (backoffUntil > now) != test.locked {
			t.Fatalf("failure at %d: count=%d start=%d backoff=%d", test.offset, failures, windowStarted, backoffUntil)
		}
		if test.locked && (level != 1 || backoffUntil != now+8) {
			t.Fatalf("fifth trailing failure backoff: level=%d until=%d", level, backoffUntil)
		}
	}
	expiredAt := base + 1803
	if err := s.beginLogin(ctx, loginHash, sources, 0, expiredAt); err != nil {
		t.Fatalf("expired window begin: %v", err)
	}
	if err := s.recordLoginFailure(ctx, loginHash, sources, 0, expiredAt); err != nil {
		t.Fatalf("expired window failure: %v", err)
	}
	var failures, level int
	var windowStarted, backoffUntil int64
	var encoded []byte
	if err := s.db.QueryRowContext(ctx, "SELECT failures,failure_times,window_started_at,backoff_level,backoff_until FROM login_limits WHERE bucket_hash=?", loginHash[:]).Scan(&failures, &encoded, &windowStarted, &level, &backoffUntil); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeFailureTimes(encoded, failures, windowStarted); err != nil {
		t.Fatal(err)
	}
	if failures != 1 || level != 0 || backoffUntil != 0 || windowStarted != expiredAt {
		t.Fatalf("expired rolling window did not reset: count=%d level=%d start=%d backoff=%d", failures, level, windowStarted, backoffUntil)
	}
}

func TestLoginBackoffThresholdCapAndRollingExpiry(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	if _, _, err := s.BootstrapAdmin(ctx, "rootadmin", "Administrator", []byte("unique-admin-password-41")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	windowStart := now - 600
	loginHash := s.loginBucketHash("threshold")
	initialEncodedTimes := encodeFailureTimes(failureTimes{windowStart, windowStart + 150, windowStart + 300, windowStart + 450})
	if _, err := s.db.ExecContext(ctx, `INSERT INTO login_limits(bucket_hash,failures,failure_times,window_started_at,expires_at,backoff_level,backoff_until)
		VALUES(?,4,?,?,?,0,0)`, loginHash[:], initialEncodedTimes, windowStart, now+900); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticatePassword(ctx, "threshold", "incorrect-password-2026", "203.0.113.11", 1); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("fifth failed login result=%v", err)
	}
	var observedWindow int64
	if err := s.db.QueryRowContext(ctx, "SELECT window_started_at FROM login_limits WHERE bucket_hash=?", loginHash[:]).Scan(&observedWindow); err != nil {
		t.Fatal(err)
	}
	if observedWindow != windowStart {
		t.Fatalf("failure moved rolling window start: got=%d want=%d", observedWindow, windowStart)
	}
	if _, err := s.AuthenticatePassword(ctx, "threshold", "incorrect-password-2026", "203.0.113.12", 1); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("login-key backoff did not follow five failures: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE login_limits SET failures=5,window_started_at=?,expires_at=?,backoff_level=9,backoff_until=?
		WHERE bucket_hash=?`, windowStart, now+900, now-1, loginHash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticatePassword(ctx, "threshold", "incorrect-password-2026", "203.0.113.13", 1); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("capped backoff failure result=%v", err)
	}
	var level int
	var backoffUntil int64
	if err := s.db.QueryRowContext(ctx, "SELECT backoff_level,backoff_until FROM login_limits WHERE bucket_hash=?", loginHash[:]).Scan(&level, &backoffUntil); err != nil {
		t.Fatal(err)
	}
	if level != 10 || backoffUntil-time.Now().Unix() > 900 || backoffUntil-time.Now().Unix() <= 0 {
		t.Fatalf("exponential delay did not cap at 15 minutes: level=%d until=%d", level, backoffUntil)
	}
	expiredTimes := encodeFailureTimes(failureTimes{now - 2000, now - 1900, now - 1800, now - 1700, now - 1600})
	if _, err := s.db.ExecContext(ctx, "UPDATE login_limits SET failure_times=?,window_started_at=?,expires_at=unixepoch()+900,backoff_until=unixepoch()-1 WHERE bucket_hash=?", expiredTimes, expiredTimes[0], loginHash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticatePassword(ctx, "threshold", "incorrect-password-2026", "203.0.113.14", 1); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("expired rolling window remained locked: %v", err)
	}
	var failures int
	if err := s.db.QueryRowContext(ctx, "SELECT failures,backoff_level FROM login_limits WHERE bucket_hash=?", loginHash[:]).Scan(&failures, &level); err != nil || failures != 1 || level != 0 {
		t.Fatalf("expired rolling window did not reset: failures=%d level=%d err=%v", failures, level, err)
	}
}

func TestLimiterRowsExpireSaturateAndSurviveRestart(t *testing.T) {
	ctx := context.Background()
	s, stateDir := openTestStore(t)
	_, _, err := s.BootstrapAdmin(ctx, "rootadmin", "Administrator", []byte("unique-admin-password-41"))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	loginFixtureNow := time.Now().Unix()
	loginFixtureTimes := encodeFailureTimes(failureTimes{loginFixtureNow})
	loginInsert, err := tx.PrepareContext(ctx, "INSERT INTO login_limits(bucket_hash,failures,failure_times,window_started_at,expires_at,backoff_level,backoff_until) VALUES(?,1,?,?,?,0,0)")
	if err != nil {
		t.Fatal(err)
	}
	for i := range MaxLoginLimitRows {
		bucket := make([]byte, 32)
		binary.BigEndian.PutUint64(bucket[24:], uint64(i+1))
		if _, err = loginInsert.ExecContext(ctx, bucket, loginFixtureTimes, loginFixtureNow, loginFixtureNow+3600); err != nil {
			t.Fatal(err)
		}
	}
	if err = loginInsert.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticatePassword(ctx, "newlogin", "incorrect-password-2026", "192.0.2.88", 1); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("login-key saturation was not bounded: %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.AuthenticatePassword(ctx, "newlogin", "incorrect-password-2026", "192.0.2.88", 1); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("login overload did not survive restart: %v", err)
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE login_limits SET expires_at=unixepoch()-1 WHERE bucket_hash=(SELECT min(bucket_hash) FROM login_limits)"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticatePassword(ctx, "newlogin", "incorrect-password-2026", "192.0.2.88", 1); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("expired login row did not release overload: %v", err)
	}
	if _, err = s.db.ExecContext(ctx, "DELETE FROM login_limits"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, "DELETE FROM source_limits"); err != nil {
		t.Fatal(err)
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	sourceFixtureNow := time.Now().Unix()
	sourceFixtureTimes := encodeFailureTimes(failureTimes{sourceFixtureNow})
	sourceInsert, err := tx.PrepareContext(ctx, "INSERT INTO source_limits(bucket_hash,bucket_kind,failures,failure_times,window_started_at,backoff_level,backoff_until,last_seen_at,expires_at) VALUES(?, 'exact', 1, ?, ?, 0, 0, ?, ?)")
	if err != nil {
		t.Fatal(err)
	}
	for i := range MaxSourceLimitRows {
		bucket := make([]byte, 32)
		binary.BigEndian.PutUint64(bucket[24:], uint64(i+1))
		if _, err = sourceInsert.ExecContext(ctx, bucket, sourceFixtureTimes, sourceFixtureNow, sourceFixtureNow, sourceFixtureNow+86400); err != nil {
			t.Fatal(err)
		}
	}
	if err = sourceInsert.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticatePassword(ctx, "otherlogin", "incorrect-password-2026", "198.51.100.88", 1); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("source saturation was not bounded: %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.AuthenticatePassword(ctx, "otherlogin", "incorrect-password-2026", "198.51.100.88", 1); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("source overload did not survive restart: %v", err)
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE source_limits SET expires_at=unixepoch()-1 WHERE bucket_hash=(SELECT min(bucket_hash) FROM source_limits)"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticatePassword(ctx, "otherlogin", "incorrect-password-2026", "198.51.100.88", 1); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("expired source row did not release overload: %v", err)
	}
}
