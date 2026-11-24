package engine

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/identity"
)

const (
	setupCodeAlphabet      = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	setupCodeLength        = 16
	setupBootstrapLimit    = 8 << 10
	setupMaxFailedAttempts = 5
	setupBackoffWindow     = 15 * time.Minute
	setupMaxTrackedSources = 64
	hostnameBodyLimit      = 1024
)

var (
	errSetupCodeInvalid     = errors.New("invalid setup code")
	errSetupCodeRateLimited = errors.New("too many failed setup code attempts")
	errSetupCodeConsumed    = errors.New("setup code already consumed")
)

type setupFailureBucket struct {
	count       int
	windowStart time.Time
}

// setupCodeStore holds the one-time console setup code used to unlock WebUI bootstrap before any
// administrator account exists. The code is persisted with mode 0600 in <stateDir>/setup-code and
// removed as soon as an administrator is created.
type setupCodeStore struct {
	stateDir string
	now      func() time.Time

	mu       sync.Mutex
	code     string // normalized 16-char Crockford Base32
	consumed bool
	failures map[string]*setupFailureBucket
}

func newSetupCodeStore(stateDir string) *setupCodeStore {
	return &setupCodeStore{stateDir: stateDir, now: time.Now}
}

func (s *setupCodeStore) path() string {
	return filepath.Join(s.stateDir, "setup-code")
}

func formatSetupCode(norm string) string {
	if len(norm) != setupCodeLength {
		return norm
	}
	return norm[0:4] + "-" + norm[4:8] + "-" + norm[8:12] + "-" + norm[12:16]
}

func normalizeSetupCode(raw string) string {
	var b strings.Builder
	b.Grow(setupCodeLength)
	for _, r := range strings.ToUpper(raw) {
		switch r {
		case '-', ' ', '\t', '\r', '\n':
			continue
		case 'O':
			b.WriteByte('0')
		case 'I', 'L':
			b.WriteByte('1')
		default:
			if r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' {
				b.WriteRune(r)
			} else {
				return ""
			}
		}
	}
	return b.String()
}

func generateSetupCode() (string, error) {
	var raw [setupCodeLength]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	out := make([]byte, setupCodeLength)
	for i, b := range raw {
		out[i] = setupCodeAlphabet[int(b)&31]
	}
	return string(out), nil
}

func (s *setupCodeStore) regenerateLocked() (string, error) {
	norm, err := generateSetupCode()
	if err != nil {
		return "", err
	}
	formatted := formatSetupCode(norm)
	if err := os.MkdirAll(s.stateDir, 0o700); err != nil {
		return "", err
	}
	if err := writeFileAtomic(s.path(), []byte(formatted+"\n"), 0o600); err != nil {
		return "", err
	}
	s.code = norm
	s.failures = nil
	return formatted, nil
}

// Current returns the active formatted setup code, reading from disk or generating one if absent.
func (s *setupCodeStore) Current() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.consumed {
		return "", errSetupCodeConsumed
	}
	if len(s.code) == setupCodeLength {
		return formatSetupCode(s.code), nil
	}
	if data, err := os.ReadFile(s.path()); err == nil {
		if norm := normalizeSetupCode(string(data)); len(norm) == setupCodeLength {
			s.code = norm
			return formatSetupCode(norm), nil
		}
	}
	return s.regenerateLocked()
}

// Rotate generates and persists a fresh setup code and resets per-source failure counters.
func (s *setupCodeStore) Rotate() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.consumed {
		return "", errSetupCodeConsumed
	}
	return s.regenerateLocked()
}

// Verify checks candidate against the active setup code in constant time with per-source rate limiting.
func (s *setupCodeStore) Verify(source, candidate string) error {
	if _, err := s.Current(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.consumed {
		return errSetupCodeConsumed
	}
	now := s.now()
	if s.failures == nil {
		s.failures = make(map[string]*setupFailureBucket)
	}
	bucket, ok := s.failures[source]
	if ok && now.Sub(bucket.windowStart) >= setupBackoffWindow {
		delete(s.failures, source)
		ok = false
	}
	if ok && bucket.count >= setupMaxFailedAttempts {
		return errSetupCodeRateLimited
	}
	norm := normalizeSetupCode(candidate)
	if len(norm) != setupCodeLength || subtle.ConstantTimeCompare([]byte(norm), []byte(s.code)) != 1 {
		if !ok {
			if len(s.failures) >= setupMaxTrackedSources {
				for k, v := range s.failures {
					if now.Sub(v.windowStart) >= setupBackoffWindow {
						delete(s.failures, k)
					}
				}
				if len(s.failures) >= setupMaxTrackedSources {
					return errSetupCodeRateLimited
				}
			}
			bucket = &setupFailureBucket{windowStart: now}
			s.failures[source] = bucket
		}
		bucket.count++
		return errSetupCodeInvalid
	}
	return nil
}

// Clear removes the setup code from memory and disk while holding the lock and marks it consumed.
func (s *setupCodeStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.code = ""
	s.consumed = true
	s.failures = nil
	_ = os.Remove(s.path())
}

type setupCodeResponse struct {
	SetupRequired bool   `json:"setupRequired"`
	SetupCode     string `json:"setupCode,omitempty"`
}

// newSetupCodeHandler serves GET/POST /api/v1/setup/code on the root-only local Unix socket so
// the local console (`andaru ctl setup-code`) can display or rotate the one-time WebUI setup code.
func newSetupCodeHandler(identities identity.Store, codes *setupCodeStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, ok := mutationActor(w, r)
		if !ok {
			return
		}
		if !actor.Root {
			writeError(w, r, http.StatusForbidden, "FORBIDDEN", "The setup code is available only on the local root console.")
			return
		}
		if !noQuery(w, r) || !requireNoBody(w, r) {
			return
		}
		revision, err := identities.Revision(r.Context())
		if err != nil {
			writeError(w, r, http.StatusServiceUnavailable, "IDENTITY_UNAVAILABLE", "Identity status is unavailable.")
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			if revision > 0 {
				codes.Clear()
				writeJSON(w, r, http.StatusOK, setupCodeResponse{SetupRequired: false})
				return
			}
			code, err := codes.Current()
			if err != nil {
				slog.ErrorContext(r.Context(), "setup code unavailable", "error", err)
				writeError(w, r, http.StatusServiceUnavailable, "SETUP_UNAVAILABLE", "The setup code could not be prepared.")
				return
			}
			writeJSON(w, r, http.StatusOK, setupCodeResponse{SetupRequired: true, SetupCode: code})
		case http.MethodPost:
			if revision > 0 {
				codes.Clear()
				writeError(w, r, http.StatusConflict, "ALREADY_CONFIGURED", "An administrator account already exists.")
				return
			}
			code, err := codes.Rotate()
			if err != nil {
				slog.ErrorContext(r.Context(), "setup code rotation failed", "error", err)
				writeError(w, r, http.StatusServiceUnavailable, "SETUP_UNAVAILABLE", "The setup code could not be rotated.")
				return
			}
			writeJSON(w, r, http.StatusOK, setupCodeResponse{SetupRequired: true, SetupCode: code})
		default:
			methodNotAllowed(w, r, "GET, HEAD, POST")
		}
	})
}

type webSetupBootstrapRequest struct {
	SetupCode   string `json:"setupCode"`
	Login       string `json:"login"`
	DisplayName string `json:"displayName"`
	Password    string `json:"password"`
}

func randomOperationID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "setup-" + hex.EncodeToString(buf[:]), nil
}

func (h *webAPI) setupBootstrap(w http.ResponseWriter, r *http.Request, source string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, "POST")
		return
	}
	if !noQuery(w, r) || !h.requireOrigin(w, r) {
		return
	}
	if cookieCredentialPresent(r) || len(r.Header.Values("Authorization")) != 0 {
		writeError(w, r, http.StatusBadRequest, "AMBIGUOUS_CREDENTIALS", "Setup bootstrap requests cannot carry an existing credential.")
		return
	}
	revision, err := h.identities.Revision(r.Context())
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "IDENTITY_UNAVAILABLE", "Identity status is unavailable.")
		return
	}
	if revision > 0 {
		h.setupCodes.Clear()
		writeError(w, r, http.StatusConflict, "ALREADY_CONFIGURED", "An administrator account already exists.")
		return
	}
	raw, request, ok := decodeIdentityBody[webSetupBootstrapRequest](w, r, setupBootstrapLimit, "setupCode", "login", "displayName", "password")
	if !ok {
		return
	}
	defer clear(raw)
	if strings.TrimSpace(request.SetupCode) == "" || strings.TrimSpace(request.Login) == "" || request.Password == "" || len(request.Password) > 512 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Setup code, login, and password are required.")
		return
	}
	if err := h.setupCodes.Verify(source, request.SetupCode); err != nil {
		if errors.Is(err, errSetupCodeConsumed) {
			writeError(w, r, http.StatusConflict, "ALREADY_CONFIGURED", "An administrator account already exists.")
			return
		}
		if errors.Is(err, errSetupCodeRateLimited) {
			if _, auditErr := h.coordinator.audits.Append(r.Context(), audit.Event{
				Actor: authz.Actor{Kind: authz.ActorSystem, ID: "root"}, Source: source, Action: audit.ActionLoginBackoff,
				Result: audit.ResultFailure, Category: audit.CategoryGeneral,
				Metadata: map[string]string{audit.MetadataReasonCode: audit.ReasonRateLimited},
			}); auditErr != nil {
				writeError(w, r, http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE", "Setup verification is temporarily unavailable.")
				return
			}
			writeError(w, r, http.StatusTooManyRequests, "RATE_LIMITED", "Too many failed setup code attempts; wait or rotate the code on the console.")
			return
		}
		if errors.Is(err, errSetupCodeInvalid) {
			if _, auditErr := h.coordinator.audits.Append(r.Context(), audit.Event{
				Actor: authz.Actor{Kind: authz.ActorSystem, ID: "root"}, Source: source, Action: audit.ActionLoginFailure,
				Result: audit.ResultFailure, Category: audit.CategoryGeneral,
				Metadata: map[string]string{audit.MetadataReasonCode: audit.ReasonInvalidCredentials},
			}); auditErr != nil {
				writeError(w, r, http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE", "Setup verification is temporarily unavailable.")
				return
			}
			writeError(w, r, http.StatusUnauthorized, "INVALID_SETUP_CODE", "The setup code does not match the code shown on the appliance console.")
			return
		}
		writeError(w, r, http.StatusServiceUnavailable, "SETUP_UNAVAILABLE", "Setup verification is temporarily unavailable.")
		return
	}

	// Verify live authorization is readable before creating the administrator so bootstrap never
	// creates an account that cannot immediately sign in.
	if _, _, err := h.readLiveAuthorization(r.Context()); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "AUTHENTICATION_UNAVAILABLE", "Live authorization is not ready yet; retry once the engine finishes starting.")
		return
	}

	opID, err := randomOperationID()
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "SETUP_UNAVAILABLE", "Setup verification is temporarily unavailable.")
		return
	}
	actor := requestActor{
		Actor:  authz.Actor{Kind: authz.ActorSystem, ID: "root"},
		Source: source,
	}
	execution, execErr := h.coordinator.execute(r.Context(), actor, opID, raw, audit.ActionAccountCreated, audit.CategoryProtected, "", func(ctx context.Context) (string, any, error) {
		password := []byte(request.Password)
		account, _, bootErr := h.identities.BootstrapAdmin(ctx, request.Login, request.DisplayName, password)
		clear(password)
		return account.ID, nil, bootErr
	})
	if execErr != nil {
		slog.ErrorContext(r.Context(), "web setup bootstrap audit/lock failed", "error", execErr)
		writeError(w, r, http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE", "Administrator creation is temporarily unavailable.")
		return
	}
	if execution.MutationErr != nil {
		if errors.Is(execution.MutationErr, identity.ErrConflict) || errors.Is(execution.MutationErr, identity.ErrDuplicate) {
			h.setupCodes.Clear()
			writeError(w, r, http.StatusConflict, "ALREADY_CONFIGURED", "An administrator account already exists.")
			return
		}
		if errors.Is(execution.MutationErr, identity.ErrPasswordPolicy) || errors.Is(execution.MutationErr, identity.ErrInvalid) {
			writeError(w, r, http.StatusUnprocessableEntity, "VALIDATION_FAILED", execution.MutationErr.Error())
			return
		}
		slog.ErrorContext(r.Context(), "web setup bootstrap failed", "error", execution.MutationErr)
		writeError(w, r, http.StatusServiceUnavailable, "IDENTITY_UNAVAILABLE", "Administrator creation failed.")
		return
	}

	h.setupCodes.Clear()
	password := request.Password
	request.Password = ""
	h.authenticateAndStartSession(w, r, source, request.Login, password, http.StatusCreated)
}

// ValidateHostname checks that name is a valid RFC 1123 single-label appliance hostname.
func ValidateHostname(name string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	if len(trimmed) == 0 || len(trimmed) > 63 {
		return "", errors.New("hostname must be 1 to 63 characters")
	}
	if trimmed[0] == '-' || trimmed[len(trimmed)-1] == '-' {
		return "", errors.New("hostname cannot start or end with a hyphen")
	}
	for i := 0; i < len(trimmed); i++ {
		c := trimmed[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return "", errors.New("hostname may contain only lowercase letters, digits, and hyphens")
		}
	}
	return trimmed, nil
}

func runSetHostnameUnit(ctx context.Context, hostname string) error {
	out, err := exec.CommandContext(ctx, "/usr/bin/systemd-run",
		"--quiet", "--collect", "--wait",
		"--unit=andaru-realize-set-hostname", "-p", "RuntimeMaxSec=30",
		andaruBinary, "realize", "set-hostname", hostname,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemd-run: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

type hostnameRequest struct {
	Hostname string `json:"hostname"`
}

func newHostnameHandler(setHostname func(ctx context.Context, hostname string) error) http.Handler {
	if setHostname == nil {
		setHostname = runSetHostnameUnit
	}
	var mu sync.Mutex
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w, r, "POST")
			return
		}
		if !noQuery(w, r) {
			return
		}
		_, req, ok := decodeIdentityBody[hostnameRequest](w, r, hostnameBodyLimit, "hostname")
		if !ok {
			return
		}
		normalized, err := ValidateHostname(req.Hostname)
		if err != nil {
			writeError(w, r, http.StatusUnprocessableEntity, "VALIDATION_FAILED", err.Error())
			return
		}
		mu.Lock()
		err = setHostname(r.Context(), normalized)
		mu.Unlock()
		if err != nil {
			slog.WarnContext(r.Context(), "set hostname failed", "hostname", normalized, "error", err)
			writeError(w, r, http.StatusBadGateway, "HOSTNAME_FAILED", "Could not apply the new hostname.")
			return
		}
		writeJSON(w, r, http.StatusOK, map[string]string{"hostname": normalized})
	})
}
