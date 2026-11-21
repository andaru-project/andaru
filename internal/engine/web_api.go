package engine

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/identity"
)

const (
	WebSourceHeader       = "X-Andaru-Source"
	webSessionCookie      = "__Host-andaru_session"
	webCSRFHeader         = "X-Andaru-CSRF"
	webSessionIdleSeconds = 30 * 60
	webLoginBodyLimit     = 2 << 10
)

var (
	errWebAuditUnavailable  = errors.New("web security audit unavailable")
	errAmbiguousCredentials = errors.New("ambiguous web credentials")
)

type webAPI struct {
	identities    identity.Store
	configuration *config.Store
	coordinator   *securityCoordinator
	rootAPI       http.Handler
	stateDir      string
	origins       map[string]struct{}
	hosts         map[string]struct{}
}

type webLoginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

type webAccountView struct {
	ID          string `json:"id"`
	Login       string `json:"login"`
	DisplayName string `json:"displayName"`
}

type webSessionResponse struct {
	Account            webAccountView     `json:"account"`
	Capabilities       []authz.Capability `json:"capabilities"`
	CSRFToken          string             `json:"csrfToken"`
	ExpiresAt          time.Time          `json:"expiresAt"`
	IdleTimeoutSeconds int                `json:"idleTimeoutSeconds"`
}

type webSessionView struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Current    bool      `json:"current"`
}

type webSessionPage struct {
	Items []webSessionView `json:"items"`
}

type webStartupStatus struct {
	SetupRequired bool `json:"setupRequired"`
}

func newWebAPI(identities identity.Store, configuration *config.Store, coordinator *securityCoordinator, rootAPI http.Handler, stateDir string, configuredOrigins []string) (http.Handler, error) {
	origins, hosts, err := managementOrigins(configuredOrigins)
	if err != nil {
		return nil, err
	}
	return &webAPI{
		identities: identities, configuration: configuration, coordinator: coordinator, rootAPI: rootAPI,
		stateDir: stateDir, origins: origins, hosts: hosts,
	}, nil
}

func managementOrigins(configured []string) (map[string]struct{}, map[string]struct{}, error) {
	if len(configured) == 0 {
		return nil, nil, errors.New("at least one exact HTTPS management origin is required")
	}
	origins := make(map[string]struct{}, len(configured))
	hosts := make(map[string]struct{}, len(configured))
	for _, raw := range configured {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.String() != raw {
			return nil, nil, errors.New("management origins must be exact HTTPS origins without paths, queries, fragments, or credentials")
		}
		if _, duplicate := origins[raw]; duplicate {
			return nil, nil, errors.New("management origins must be unique")
		}
		origins[raw] = struct{}{}
		hosts[parsed.Host] = struct{}{}
	}
	return origins, hosts, nil
}

func (h *webAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	peer, ok := r.Context().Value(peerKey{}).(peerIdentity)
	if !ok || !peer.verified || !peer.web {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "The web peer is not verified.")
		return
	}
	sourceValues := r.Header.Values(WebSourceHeader)
	if len(sourceValues) != 1 || !validSource(sourceValues[0]) || strings.HasPrefix(sourceValues[0], "unix") || sourceValues[0] == "local" {
		writeError(w, r, http.StatusBadRequest, "INVALID_SOURCE", "Verified client source metadata is required.")
		return
	}
	source := sourceValues[0]
	r.Header.Del(WebSourceHeader)
	if _, ok = h.hosts[r.Host]; !ok {
		writeError(w, r, http.StatusMisdirectedRequest, "INVALID_HOST", "The request Host is not configured for management.")
		return
	}
	if r.URL.Path == "/api/v1/startup-status" {
		h.startupStatus(w, r)
		return
	}
	if r.URL.Path == "/api/v1/session" {
		h.session(w, r, source)
		return
	}
	if r.URL.Path == "/api/v1/session/preferences" {
		h.preferences(w, r, source)
		return
	}
	if r.URL.Path == "/api/v1/sessions" || strings.HasPrefix(r.URL.Path, "/api/v1/sessions/") {
		h.sessions(w, r, source)
		return
	}
	if strings.Contains(r.URL.Path, "/bootstrap") {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	h.protected(w, r, source)
}

func (h *webAPI) startupStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, r, "GET, HEAD")
		return
	}
	if !noQuery(w, r) || !requireNoBody(w, r) {
		return
	}
	revision, err := h.identities.Revision(r.Context())
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "IDENTITY_UNAVAILABLE", "Identity status is unavailable.")
		return
	}
	writeJSON(w, r, http.StatusOK, webStartupStatus{SetupRequired: revision == 0})
}

func (h *webAPI) session(w http.ResponseWriter, r *http.Request, source string) {
	switch r.Method {
	case http.MethodPost:
		h.login(w, r, source)
	case http.MethodGet:
		h.currentSession(w, r, source)
	case http.MethodDelete:
		h.logout(w, r, source)
	default:
		methodNotAllowed(w, r, "GET, POST, DELETE")
	}
}

func (h *webAPI) login(w http.ResponseWriter, r *http.Request, source string) {
	if !noQuery(w, r) || !h.requireOrigin(w, r) {
		return
	}
	if cookieCredentialPresent(r) || len(r.Header.Values("Authorization")) != 0 {
		writeError(w, r, http.StatusBadRequest, "AMBIGUOUS_CREDENTIALS", "Login requests cannot carry an existing credential.")
		return
	}
	_, request, ok := decodeIdentityBody[webLoginRequest](w, r, webLoginBodyLimit, "login", "password")
	if !ok {
		return
	}
	snapshot, epoch, err := h.readLiveAuthorization(r.Context())
	if err != nil {
		h.loginError(w, r, source, err)
		return
	}
	result, err := h.identities.AuthenticatePasswordWithIntent(r.Context(), request.Login, request.Password, source, epoch, func(account identity.Account) error {
		_, auditErr := h.coordinator.audits.Append(r.Context(), audit.Event{
			Actor: authz.Actor{Kind: authz.ActorAccount, ID: account.ID}, Source: source, Action: audit.ActionLoginSuccess,
			Target: account.ID, Result: audit.ResultSuccess, Category: audit.CategoryGeneral,
		})
		if auditErr != nil {
			return errors.Join(errWebAuditUnavailable, auditErr)
		}
		return nil
	})
	request.Password = ""
	if err != nil {
		h.loginError(w, r, source, err)
		return
	}
	confirmed, confirmedEpoch, err := h.readLiveAuthorization(r.Context())
	if err != nil || confirmedEpoch != epoch {
		if selector, secret, ok := splitCredential(result.SessionCookie); ok {
			_ = h.identities.Logout(r.Context(), selector, secret)
		}
		if err == nil {
			err = authz.ErrTransitionInProgress
		}
		h.loginError(w, r, source, err)
		return
	}
	snapshot = confirmed
	capabilities := snapshot.Resolve(result.Account.RoleIDs)
	setSessionCookie(w, result.SessionCookie, result.ExpiresAt)
	writeJSON(w, r, http.StatusOK, sessionResponse(result.Account, capabilities, result.CSRFSecret, result.ExpiresAt))
}

func (h *webAPI) loginError(w http.ResponseWriter, r *http.Request, source string, loginErr error) {
	if errors.Is(loginErr, errWebAuditUnavailable) {
		writeError(w, r, http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE", "Login is temporarily unavailable.")
		return
	}
	action, reason, status := audit.ActionLoginFailure, audit.ReasonInvalidCredentials, http.StatusUnauthorized
	if errors.Is(loginErr, identity.ErrRateLimited) {
		action, reason, status = audit.ActionLoginBackoff, audit.ReasonRateLimited, http.StatusTooManyRequests
	} else if !errors.Is(loginErr, identity.ErrAuthentication) {
		writeError(w, r, http.StatusServiceUnavailable, "AUTHENTICATION_UNAVAILABLE", "Login is temporarily unavailable.")
		return
	}
	if _, err := h.coordinator.audits.Append(r.Context(), audit.Event{
		Actor: authz.Actor{Kind: authz.ActorSystem, ID: "root"}, Source: source, Action: action,
		Result: audit.ResultFailure, Category: audit.CategoryGeneral,
		Metadata: map[string]string{audit.MetadataReasonCode: reason},
	}); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE", "Login is temporarily unavailable.")
		return
	}
	writeError(w, r, status, "AUTHENTICATION_FAILED", "The login or password is invalid, or login is temporarily unavailable.")
}

func (h *webAPI) currentSession(w http.ResponseWriter, r *http.Request, source string) {
	if !noQuery(w, r) || !requireNoBody(w, r) {
		return
	}
	principal, _, secret, snapshot, epoch, ok := h.cookiePrincipal(w, r, source)
	if !ok {
		return
	}
	csrf, err := h.identities.RotateCSRF(r.Context(), principal.SessionSelector, secret, epoch)
	if err != nil {
		if errors.Is(err, identity.ErrAuthentication) {
			writeAuthenticationFailure(w, r)
		} else {
			writeError(w, r, http.StatusServiceUnavailable, "IDENTITY_UNAVAILABLE", "Session refresh is temporarily unavailable.")
		}
		return
	}
	writeJSON(w, r, http.StatusOK, sessionResponse(principal.Account, snapshot.Resolve(principal.Account.RoleIDs), csrf, principal.SessionExpiresAt))
}

// preferences serves what the web UI keeps for the signed-in account, such as its dashboard layout. The engine
// stores the document and acts on nothing in it, so it is outside the saved configuration: no apply, no generation.
// Any signed-in human may read and replace their own; no capability governs it.
func (h *webAPI) preferences(w http.ResponseWriter, r *http.Request, source string) {
	var accountID string
	switch r.Method {
	case http.MethodGet:
		if !noQuery(w, r) || !requireNoBody(w, r) {
			return
		}
		principal, _, _, _, _, ok := h.cookiePrincipal(w, r, source)
		if !ok {
			return
		}
		accountID = principal.Account.ID
	case http.MethodPut:
		if !noQuery(w, r) || !h.requireOrigin(w, r) {
			return
		}
		principal, selector, secret, _, _, ok := h.cookiePrincipal(w, r, source)
		if !ok || !h.validateCSRF(w, r, selector, secret) {
			return
		}
		if !jsonContentType(r.Header.Get("Content-Type")) {
			writeError(w, r, http.StatusUnsupportedMediaType, "UNSUPPORTED_CONTENT", "Preferences must be application/json.")
			return
		}
		document, err := readRequestBody(w, r, maxPreferencesBytes)
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, r, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "Preferences exceed 16 KiB.")
			return
		}
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "The request body could not be read.")
			return
		}
		accountID = principal.Account.ID
		if err = writePreferences(h.stateDir, accountID, document); errors.Is(err, errInvalidPreferences) {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Preferences must be one JSON object.")
			return
		} else if err != nil {
			slog.ErrorContext(r.Context(), "preferences not written", "error", err)
			writeError(w, r, http.StatusServiceUnavailable, "STORE_UNAVAILABLE", "Preferences are temporarily unavailable.")
			return
		}
	default:
		methodNotAllowed(w, r, "GET, PUT")
		return
	}
	// Both answer with the stored bytes, so a client shows what the box holds.
	document, err := readPreferences(h.stateDir, accountID)
	if err != nil {
		slog.ErrorContext(r.Context(), "preferences not read", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "STORE_UNAVAILABLE", "Preferences are temporarily unavailable.")
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(document)
}

func (h *webAPI) logout(w http.ResponseWriter, r *http.Request, source string) {
	if !noQuery(w, r) || !h.requireOrigin(w, r) {
		return
	}
	principal, selector, secret, _, _, err := h.readCookiePrincipal(r)
	if errors.Is(err, identity.ErrAuthentication) {
		clearSessionCookie(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if errors.Is(err, errAmbiguousCredentials) {
		writeError(w, r, http.StatusBadRequest, "AMBIGUOUS_CREDENTIALS", "Cookie and bearer credentials cannot be combined.")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "AUTHENTICATION_UNAVAILABLE", "Authentication is temporarily unavailable.")
		return
	}
	if !h.validateCSRF(w, r, selector, secret) {
		return
	}
	err = h.identities.LogoutWithIntent(r.Context(), selector, secret, func(target string) error {
		_, auditErr := h.coordinator.audits.Append(r.Context(), audit.Event{
			Actor: principal.Actor, Source: source, Action: audit.ActionSessionLogout, Target: target,
			Result: audit.ResultSuccess, Category: audit.CategoryGeneral,
		})
		if auditErr != nil {
			return errors.Join(errWebAuditUnavailable, auditErr)
		}
		return nil
	})
	if errors.Is(err, errWebAuditUnavailable) {
		writeError(w, r, http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE", "Logout is temporarily unavailable.")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "IDENTITY_UNAVAILABLE", "Logout is temporarily unavailable.")
		return
	}
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *webAPI) sessions(w http.ResponseWriter, r *http.Request, source string) {
	if r.URL.Path == "/api/v1/sessions" {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, r, "GET")
			return
		}
		if !noQuery(w, r) || !requireNoBody(w, r) {
			return
		}
		principal, selector, _, _, epoch, ok := h.cookiePrincipal(w, r, source)
		if !ok {
			return
		}
		items, err := h.identities.ListSessions(r.Context(), principal.Account.ID, epoch)
		if err != nil {
			writeError(w, r, http.StatusServiceUnavailable, "IDENTITY_UNAVAILABLE", "Sessions are temporarily unavailable.")
			return
		}
		views := make([]webSessionView, 0, len(items))
		for _, item := range items {
			views = append(views, webSessionView{ID: item.Selector, CreatedAt: item.CreatedAt, LastSeenAt: item.LastSeenAt, ExpiresAt: item.ExpiresAt, Current: item.Selector == selector})
		}
		writeJSON(w, r, http.StatusOK, webSessionPage{Items: views})
		return
	}
	if r.Method != http.MethodDelete {
		methodNotAllowed(w, r, "DELETE")
		return
	}
	if !noQuery(w, r) || !h.requireOrigin(w, r) {
		return
	}
	target := strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/")
	if target == "" || strings.Contains(target, "/") {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	principal, selector, secret, _, _, ok := h.cookiePrincipal(w, r, source)
	if !ok || !h.validateCSRF(w, r, selector, secret) {
		return
	}
	err := h.identities.RevokeSessionWithIntent(r.Context(), principal.Account.ID, target, func() error {
		_, auditErr := h.coordinator.audits.Append(r.Context(), audit.Event{
			Actor: principal.Actor, Source: source, Action: audit.ActionSessionRevoked, Target: target,
			Result: audit.ResultSuccess, Category: audit.CategoryGeneral,
		})
		if auditErr != nil {
			return errors.Join(errWebAuditUnavailable, auditErr)
		}
		return nil
	})
	if errors.Is(err, errWebAuditUnavailable) {
		writeError(w, r, http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE", "Session revocation is temporarily unavailable.")
		return
	}
	if err != nil {
		if errors.Is(err, identity.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Session not found.")
		} else {
			writeError(w, r, http.StatusServiceUnavailable, "IDENTITY_UNAVAILABLE", "Session revocation is temporarily unavailable.")
		}
		return
	}
	if target == selector {
		clearSessionCookie(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *webAPI) protected(w http.ResponseWriter, r *http.Request, source string) {
	route, found := routeForRequest(r.Method, r.URL.Path)
	if !found {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	cookiePresent := cookieCredentialPresent(r)
	authorizations := r.Header.Values("Authorization")
	if cookiePresent && len(authorizations) != 0 {
		writeError(w, r, http.StatusBadRequest, "AMBIGUOUS_CREDENTIALS", "Cookie and bearer credentials cannot be combined.")
		return
	}
	var principal identity.Principal
	var snapshot authz.Snapshot
	var sessionSelector, sessionSecret string
	var err error
	if cookiePresent {
		principal, sessionSelector, sessionSecret, snapshot, _, found = h.cookiePrincipal(w, r, source)
		if !found {
			return
		}
		principal.Capabilities = snapshot.Resolve(principal.Account.RoleIDs)
		if requestChangesState(r.Method) && (!h.requireOrigin(w, r) || !h.validateCSRF(w, r, sessionSelector, sessionSecret)) {
			return
		}
	} else {
		selector, secret, credentialOK := bearerCredential(authorizations)
		if !credentialOK {
			writeAuthenticationFailure(w, r)
			return
		}
		err = h.withLiveAuthorization(r.Context(), func(live authz.Snapshot, _ uint64) error {
			snapshot = live
			var authErr error
			principal, authErr = h.identities.AuthenticateToken(r.Context(), selector, secret, snapshot)
			return authErr
		})
		if err != nil {
			if errors.Is(err, identity.ErrAuthentication) {
				writeAuthenticationFailure(w, r)
			} else {
				writeError(w, r, http.StatusServiceUnavailable, "AUTHENTICATION_UNAVAILABLE", "Authentication is temporarily unavailable.")
			}
			return
		}
	}
	actor := requestActor{Actor: principal.Actor, Source: source, AccountID: principal.Account.ID, Capabilities: slices.Clone(principal.Capabilities)}
	if !hasCapability(actor, route.Capability) && !ownPasswordChange(actor, r.Method, r.URL.Path) {
		if _, err = h.coordinator.audits.Append(r.Context(), audit.Event{
			Actor: actor.Actor, Source: source, Action: audit.ActionPermissionDenied, Result: audit.ResultDenied,
			Category: audit.CategoryGeneral, Metadata: map[string]string{audit.MetadataReasonCode: audit.ReasonPermissionDenied},
		}); err != nil {
			writeError(w, r, http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE", "Authorization is temporarily unavailable.")
			return
		}
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "The verified actor lacks the required capability.")
		return
	}
	h.rootAPI.ServeHTTP(w, r.WithContext(withVerifiedPrincipal(r.Context(), principal, source)))
}

func (h *webAPI) cookiePrincipal(w http.ResponseWriter, r *http.Request, _ string) (identity.Principal, string, string, authz.Snapshot, uint64, bool) {
	principal, selector, secret, snapshot, epoch, err := h.readCookiePrincipal(r)
	if errors.Is(err, errAmbiguousCredentials) {
		writeError(w, r, http.StatusBadRequest, "AMBIGUOUS_CREDENTIALS", "Cookie and bearer credentials cannot be combined.")
		return identity.Principal{}, "", "", authz.Snapshot{}, 0, false
	}
	if errors.Is(err, identity.ErrAuthentication) {
		writeAuthenticationFailure(w, r)
		return identity.Principal{}, "", "", authz.Snapshot{}, 0, false
	}
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "AUTHENTICATION_UNAVAILABLE", "Authentication is temporarily unavailable.")
		return identity.Principal{}, "", "", authz.Snapshot{}, 0, false
	}
	return principal, selector, secret, snapshot, epoch, true
}

func (h *webAPI) readCookiePrincipal(r *http.Request) (identity.Principal, string, string, authz.Snapshot, uint64, error) {
	if len(r.Header.Values("Authorization")) != 0 {
		return identity.Principal{}, "", "", authz.Snapshot{}, 0, errAmbiguousCredentials
	}
	selector, secret, ok := sessionCredential(r)
	if !ok {
		return identity.Principal{}, "", "", authz.Snapshot{}, 0, identity.ErrAuthentication
	}
	var principal identity.Principal
	var snapshot authz.Snapshot
	var epoch uint64
	err := h.withLiveAuthorization(r.Context(), func(live authz.Snapshot, liveEpoch uint64) error {
		snapshot, epoch = live, liveEpoch
		var authErr error
		principal, authErr = h.identities.AuthenticateSession(r.Context(), selector, secret, epoch)
		return authErr
	})
	if err != nil {
		return identity.Principal{}, "", "", authz.Snapshot{}, 0, err
	}
	principal.Capabilities = snapshot.Resolve(principal.Account.RoleIDs)
	return principal, selector, secret, snapshot, epoch, nil
}

func (h *webAPI) withLiveAuthorization(ctx context.Context, fn func(authz.Snapshot, uint64) error) error {
	lock, err := authz.Acquire(ctx, h.stateDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, pending, err := lock.Pending(); err != nil {
		return err
	} else if pending {
		return authz.ErrTransitionInProgress
	}
	snapshot, epoch, err := liveAuthorizationLocked(ctx, h.stateDir, h.configuration, lock)
	if err != nil {
		return err
	}
	return fn(snapshot, epoch)
}

func (h *webAPI) readLiveAuthorization(ctx context.Context) (authz.Snapshot, uint64, error) {
	lock, err := authz.Acquire(ctx, h.stateDir)
	if err != nil {
		return authz.Snapshot{}, 0, err
	}
	defer lock.Close()
	if _, pending, err := lock.Pending(); err != nil {
		return authz.Snapshot{}, 0, err
	} else if pending {
		return authz.Snapshot{}, 0, authz.ErrTransitionInProgress
	}
	return liveAuthorizationLocked(ctx, h.stateDir, h.configuration, lock)
}

func (h *webAPI) validateCSRF(w http.ResponseWriter, r *http.Request, sessionSelector, sessionSecret string) bool {
	values := r.Header.Values(webCSRFHeader)
	if len(values) != 1 {
		writeError(w, r, http.StatusForbidden, "CSRF_FAILED", "A valid session CSRF token is required.")
		return false
	}
	selector, secret, ok := splitCredential(values[0])
	if !ok {
		writeError(w, r, http.StatusForbidden, "CSRF_FAILED", "A valid session CSRF token is required.")
		return false
	}
	err := h.identities.ValidateCSRF(r.Context(), sessionSelector, sessionSecret, selector, secret)
	if errors.Is(err, identity.ErrAuthentication) {
		writeError(w, r, http.StatusForbidden, "CSRF_FAILED", "A valid session CSRF token is required.")
		return false
	}
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "IDENTITY_UNAVAILABLE", "CSRF validation is temporarily unavailable.")
		return false
	}
	return true
}

func (h *webAPI) requireOrigin(w http.ResponseWriter, r *http.Request) bool {
	values := r.Header.Values("Origin")
	if len(values) != 1 {
		writeError(w, r, http.StatusForbidden, "ORIGIN_FAILED", "The request Origin is not trusted.")
		return false
	}
	if _, ok := h.origins[values[0]]; !ok {
		writeError(w, r, http.StatusForbidden, "ORIGIN_FAILED", "The request Origin is not trusted.")
		return false
	}
	return true
}

func sessionCredential(r *http.Request) (string, string, bool) {
	cookies := r.CookiesNamed(webSessionCookie)
	if len(cookies) != 1 {
		return "", "", false
	}
	return splitCredential(cookies[0].Value)
}

func cookieCredentialPresent(r *http.Request) bool {
	return len(r.CookiesNamed(webSessionCookie)) != 0
}

func bearerCredential(values []string) (string, string, bool) {
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return "", "", false
	}
	selector, secret, ok := splitCredential(strings.TrimPrefix(values[0], "Bearer "))
	if !ok || !canonicalRawURL(selector, 16) || !canonicalRawURL(secret, 32) {
		return "", "", false
	}
	return selector, secret, true
}

func canonicalRawURL(value string, size int) bool {
	if len(value) != base64.RawURLEncoding.EncodedLen(size) {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == size && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func splitCredential(value string) (string, string, bool) {
	selector, secret, ok := strings.Cut(value, ".")
	return selector, secret, ok && selector != "" && secret != "" && !strings.Contains(secret, ".")
}

func sessionResponse(account identity.Account, capabilities []authz.Capability, csrf string, expires time.Time) webSessionResponse {
	if capabilities == nil {
		capabilities = []authz.Capability{}
	}
	return webSessionResponse{
		Account:      webAccountView{ID: account.ID, Login: account.Login, DisplayName: account.DisplayName},
		Capabilities: capabilities, CSRFToken: csrf, ExpiresAt: expires.UTC(), IdleTimeoutSeconds: webSessionIdleSeconds,
	}
}

func setSessionCookie(w http.ResponseWriter, value string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: webSessionCookie, Value: value, Path: "/", Expires: expires.UTC(), Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: webSessionCookie, Value: "", Path: "/", Expires: time.Unix(1, 0).UTC(), MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}

func writeAuthenticationFailure(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusUnauthorized, "AUTHENTICATION_FAILED", "A valid session or bearer token is required.")
}

func requestChangesState(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}
