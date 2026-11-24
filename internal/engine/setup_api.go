package engine

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/identity"
)

const (
	setupCodeAlphabet      = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	setupCodeLength        = 16
	setupBootstrapLimit    = 16 << 10
	setupMaxFailedAttempts = 5
	setupBackoffWindow     = 15 * time.Minute
	hostnameBodyLimit      = 1024
)

var (
	errSetupCodeInvalid     = errors.New("invalid setup code")
	errSetupCodeRateLimited = errors.New("too many failed setup code attempts")
)

// setupCodeStore holds the one-time console setup code used to unlock WebUI bootstrap before any
// administrator account exists. The code is persisted with mode 0600 in <stateDir>/setup-code and
// removed as soon as an administrator is created.
type setupCodeStore struct {
	stateDir string
	now      func() time.Time

	mu          sync.Mutex
	code        string // normalized 16-char Crockford Base32
	failures    int
	windowStart time.Time
}

func newSetupCodeStore(stateDir string) *setupCodeStore {
	return &setupCodeStore{stateDir: stateDir, now: time.Now}
}

func (s *setupCodeStore) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
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

func (s *setupCodeStore) writeFile(formatted string) error {
	if err := os.MkdirAll(s.stateDir, 0o700); err != nil {
		return err
	}
	tmp := s.path() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString(formatted + "\n")
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.path())
}

// Current returns the active formatted setup code, reading from disk or generating one if absent.
func (s *setupCodeStore) Current() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.code) == setupCodeLength {
		return formatSetupCode(s.code), nil
	}
	if data, err := os.ReadFile(s.path()); err == nil {
		if norm := normalizeSetupCode(string(data)); len(norm) == setupCodeLength {
			s.code = norm
			return formatSetupCode(norm), nil
		}
	}
	norm, err := generateSetupCode()
	if err != nil {
		return "", err
	}
	formatted := formatSetupCode(norm)
	if err := s.writeFile(formatted); err != nil {
		return "", err
	}
	s.code = norm
	return formatted, nil
}

// Rotate generates and persists a fresh setup code and resets the failure window.
func (s *setupCodeStore) Rotate() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	norm, err := generateSetupCode()
	if err != nil {
		return "", err
	}
	formatted := formatSetupCode(norm)
	if err := s.writeFile(formatted); err != nil {
		return "", err
	}
	s.code = norm
	s.failures = 0
	s.windowStart = time.Time{}
	return formatted, nil
}

// Verify checks candidate against the active setup code in constant time with rate limiting.
func (s *setupCodeStore) Verify(candidate string) error {
	if _, err := s.Current(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	if !s.windowStart.IsZero() && now.Sub(s.windowStart) >= setupBackoffWindow {
		s.failures = 0
		s.windowStart = time.Time{}
	}
	if s.failures >= setupMaxFailedAttempts {
		return errSetupCodeRateLimited
	}
	norm := normalizeSetupCode(candidate)
	if len(norm) != setupCodeLength || subtle.ConstantTimeCompare([]byte(norm), []byte(s.code)) != 1 {
		if s.windowStart.IsZero() {
			s.windowStart = now
		}
		s.failures++
		return errSetupCodeInvalid
	}
	return nil
}

// Clear removes the setup code from memory and disk once bootstrap completes.
func (s *setupCodeStore) Clear() {
	s.mu.Lock()
	s.code = ""
	s.failures = 0
	s.windowStart = time.Time{}
	s.mu.Unlock()
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
	SetupCode    string `json:"setupCode"`
	Login        string `json:"login"`
	DisplayName  string `json:"displayName"`
	Password     string `json:"password"`
	SSHPublicKey string `json:"sshPublicKey"`
}

func validateAuthorizedKey(raw string) ([]byte, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	if strings.ContainsAny(trimmed, "\r\n") {
		return nil, errors.New("SSH public key must be a single line")
	}
	pub, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(trimmed))
	if err != nil {
		return nil, errors.New("SSH public key is not a valid OpenSSH public key")
	}
	if len(options) > 0 {
		return nil, errors.New("SSH public key must not include authorized_keys options")
	}
	if strings.TrimSpace(string(rest)) != "" {
		return nil, errors.New("SSH public key must contain only one key")
	}
	line := strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(pub)), "\n")
	comment = strings.TrimSpace(comment)
	if comment != "" && !strings.ContainsAny(comment, "\r\n\t") && len(comment) <= 128 {
		line += " " + comment
	}
	return []byte(line + "\n"), nil
}

func writeInitialAuthorizedKey(stateDir string, keyLine []byte) error {
	if len(keyLine) == 0 {
		return nil
	}
	sshDir := filepath.Join(stateDir, "ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return err
	}
	target := filepath.Join(sshDir, "authorized_keys")
	tmp := target + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(keyLine)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, target)
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
	raw, request, ok := decodeIdentityBody[webSetupBootstrapRequest](w, r, setupBootstrapLimit, "setupCode", "login", "displayName", "password", "sshPublicKey")
	if !ok {
		return
	}
	defer clear(raw)
	if strings.TrimSpace(request.SetupCode) == "" || strings.TrimSpace(request.Login) == "" || request.Password == "" || len(request.Password) > 512 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Setup code, login, and password are required.")
		return
	}
	keyLine, keyErr := validateAuthorizedKey(request.SSHPublicKey)
	if keyErr != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "VALIDATION_FAILED", keyErr.Error())
		return
	}
	if err := h.setupCodes.Verify(request.SetupCode); err != nil {
		if errors.Is(err, errSetupCodeRateLimited) {
			_, _ = h.coordinator.audits.Append(r.Context(), audit.Event{
				Actor: authz.Actor{Kind: authz.ActorSystem, ID: "root"}, Source: source, Action: audit.ActionLoginBackoff,
				Result: audit.ResultFailure, Category: audit.CategoryGeneral,
				Metadata: map[string]string{audit.MetadataReasonCode: audit.ReasonRateLimited},
			})
			writeError(w, r, http.StatusTooManyRequests, "RATE_LIMITED", "Too many failed setup code attempts; wait or rotate the code on the console.")
			return
		}
		if errors.Is(err, errSetupCodeInvalid) {
			_, _ = h.coordinator.audits.Append(r.Context(), audit.Event{
				Actor: authz.Actor{Kind: authz.ActorSystem, ID: "root"}, Source: source, Action: audit.ActionLoginFailure,
				Result: audit.ResultFailure, Category: audit.CategoryGeneral,
				Metadata: map[string]string{audit.MetadataReasonCode: audit.ReasonInvalidCredentials},
			})
			writeError(w, r, http.StatusUnauthorized, "INVALID_SETUP_CODE", "The setup code does not match the code shown on the appliance console.")
			return
		}
		writeError(w, r, http.StatusServiceUnavailable, "SETUP_UNAVAILABLE", "Setup verification is temporarily unavailable.")
		return
	}

	var createdAccount identity.Account
	password := []byte(request.Password)
	defer clear(password)
	lockErr := h.coordinator.withAuthorizationLock(r.Context(), func() error {
		var bootErr error
		createdAccount, _, bootErr = h.identities.BootstrapAdmin(r.Context(), request.Login, request.DisplayName, password)
		return bootErr
	})
	if lockErr != nil {
		if errors.Is(lockErr, identity.ErrConflict) || errors.Is(lockErr, identity.ErrDuplicate) {
			h.setupCodes.Clear()
			writeError(w, r, http.StatusConflict, "ALREADY_CONFIGURED", "An administrator account already exists.")
			return
		}
		if errors.Is(lockErr, identity.ErrPasswordPolicy) || errors.Is(lockErr, identity.ErrInvalid) {
			writeError(w, r, http.StatusUnprocessableEntity, "VALIDATION_FAILED", lockErr.Error())
			return
		}
		slog.ErrorContext(r.Context(), "web setup bootstrap failed", "error", lockErr)
		writeError(w, r, http.StatusServiceUnavailable, "IDENTITY_UNAVAILABLE", "Administrator creation failed.")
		return
	}

	h.setupCodes.Clear()
	if err := writeInitialAuthorizedKey(h.stateDir, keyLine); err != nil {
		slog.WarnContext(r.Context(), "could not save initial SSH public key", "error", err)
	}
	_, _ = h.coordinator.audits.Append(r.Context(), audit.Event{
		Actor: authz.Actor{Kind: authz.ActorAccount, ID: createdAccount.ID}, Source: source, Action: audit.ActionAccountCreated,
		Target: createdAccount.ID, Result: audit.ResultSuccess, Category: audit.CategoryGeneral,
	})

	snapshot, epoch, err := h.readLiveAuthorization(r.Context())
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "AUTHENTICATION_UNAVAILABLE", "Administrator was created, but signing in failed; sign in on the login page.")
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
		writeError(w, r, http.StatusServiceUnavailable, "AUTHENTICATION_UNAVAILABLE", "Administrator was created, but signing in failed; sign in on the login page.")
		return
	}
	capabilities := snapshot.Resolve(result.Account.RoleIDs)
	setSessionCookie(w, result.SessionCookie, result.ExpiresAt)
	writeJSON(w, r, http.StatusCreated, sessionResponse(result.Account, capabilities, result.CSRFSecret, result.ExpiresAt))
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

type hostnameRunner interface {
	SetHostname(ctx context.Context, hostname string) error
}

type systemdHostnameRunner struct {
	exec func(ctx context.Context, name string, args ...string) error
}

func newSystemdHostnameRunner() hostnameRunner {
	return systemdHostnameRunner{
		exec: func(ctx context.Context, name string, args ...string) error {
			out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
			if err != nil {
				return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
			}
			return nil
		},
	}
}

func (s systemdHostnameRunner) SetHostname(ctx context.Context, hostname string) error {
	return s.exec(ctx, "/usr/bin/systemd-run",
		"--quiet", "--collect", "--wait",
		"--unit=andaru-set-hostname", "-p", "RuntimeMaxSec=30",
		andaruBinary, "realize", "set-hostname", hostname,
	)
}

type hostnameRequest struct {
	Hostname string `json:"hostname"`
}

func newHostnameHandler(runner hostnameRunner) http.Handler {
	if runner == nil {
		runner = newSystemdHostnameRunner()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed.")
			return
		}
		if !noQuery(w, r) {
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, hostnameBodyLimit+1))
		if err != nil || len(body) > hostnameBodyLimit {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid hostname request.")
			return
		}
		var req hostnameRequest
		dec := json.NewDecoder(strings.NewReader(string(body)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid hostname request.")
			return
		}
		normalized, err := ValidateHostname(req.Hostname)
		if err != nil {
			writeError(w, r, http.StatusUnprocessableEntity, "VALIDATION_FAILED", err.Error())
			return
		}
		if err := runner.SetHostname(r.Context(), normalized); err != nil {
			slog.WarnContext(r.Context(), "set hostname failed", "hostname", normalized, "error", err)
			writeError(w, r, http.StatusBadGateway, "HOSTNAME_FAILED", "Could not apply the new hostname.")
			return
		}
		writeJSON(w, r, http.StatusOK, map[string]string{"hostname": normalized})
	})
}
