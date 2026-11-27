package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/backup"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/host"
	"github.com/andaru-project/andaru/internal/identity"
	"github.com/andaru-project/andaru/internal/jsonprofile"
	"github.com/andaru-project/andaru/internal/render"
)

const (
	backupPath         = "/api/v1/backups"
	restorePreviewPath = "/api/v1/restores/preview"
	restorePath        = "/api/v1/restores"
	backupOptionsMax   = 64 << 10
	restoreBodyMax     = backup.MaxArchiveSize + backupOptionsMax + 16<<10
)

var buildVersion = "development"

// Every version this engine saves says which Andaru wrote it.
func init() { config.Writer = buildVersion }

type backupCreateOptions struct {
	IncludeSecrets     *bool  `json:"includeSecrets"`
	IncludeCredentials *bool  `json:"includeCredentials,omitempty"`
	Passphrase         string `json:"passphrase,omitempty"`
	Password           string `json:"password,omitempty"`
}

type rootAdministratorOptions struct {
	AccountID   string `json:"accountId,omitempty"`
	Login       string `json:"login,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Password    string `json:"password"`
}

type restoreOptions struct {
	Passphrase               string                    `json:"passphrase,omitempty"`
	Stores                   []string                  `json:"stores,omitempty"`
	OperationID              string                    `json:"operationId,omitempty"`
	ExpectedSavedVersion     string                    `json:"expectedSavedVersion,omitempty"`
	ExpectedIdentityRevision *int64                    `json:"expectedIdentityRevision,omitempty"`
	RootAdministrator        *rootAdministratorOptions `json:"rootAdministrator,omitempty"`
}

type restoreCounts struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
	Deleted int `json:"deleted"`
}

type hardwareFinding struct {
	Code    string `json:"code"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

type restorePreviewResponse struct {
	State                     string                           `json:"state"`
	Manifest                  backup.Manifest                  `json:"manifest"`
	ExpectedSavedVersion      config.Version                   `json:"expectedSavedVersion,omitempty"`
	ExpectedIdentityRevision  int64                            `json:"expectedIdentityRevision"`
	ConfigurationChanges      restoreCounts                    `json:"configurationChanges"`
	SecretChanges             restoreCounts                    `json:"secretChanges"`
	UserChanges               restoreCounts                    `json:"userChanges"`
	CredentialChanges         restoreCounts                    `json:"credentialChanges"`
	UnresolvedSecrets         []string                         `json:"unresolvedSecrets"`
	BlockingSecrets           []string                         `json:"blockingSecrets"`
	MissingRoleIDs            []string                         `json:"missingRoleIds"`
	RequiresRootAdministrator bool                             `json:"requiresRootAdministrator"`
	RecoveryAdministrators    []identity.RecoveryAdministrator `json:"recoveryAdministrators"`
	HardwareFindings          []hardwareFinding                `json:"hardwareFindings"`
}

type inspectionResponse struct {
	State    string          `json:"state"`
	Manifest backup.Manifest `json:"manifest"`
}

type restoreSavedResponse struct {
	State             string         `json:"state"`
	SavedVersion      config.Version `json:"savedVersion,omitempty"`
	IdentityRevision  int64          `json:"identityRevision,omitempty"`
	UnresolvedSecrets []string       `json:"unresolvedSecrets"`
	Replayed          bool           `json:"replayed"`
}

type backupHandler struct {
	store      *config.Store
	identities coordinatedIdentityStore
	security   *securityCoordinator
	restores   *restoreCoordinator
	stateDir   string
	readLinks  func(context.Context) ([]host.LinkStatus, error)
	expensive  sync.Mutex
}

func newBackupHandler(store *config.Store, identities coordinatedIdentityStore, security *securityCoordinator, stateDir string, readLinks func(context.Context) ([]host.LinkStatus, error)) *backupHandler {
	return &backupHandler{store: store, identities: identities, security: security, restores: newRestoreCoordinator(store, identities, security, stateDir), stateDir: stateDir, readLinks: readLinks}
}

func isBackupPath(path string) bool {
	return path == backupPath || path == restorePreviewPath || path == restorePath
}

func (h *backupHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !isBackupPath(r.URL.Path) {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed.")
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Query parameters are not supported.")
		return
	}
	if r.URL.Path == backupPath {
		h.create(w, r)
		return
	}
	h.restore(w, r, r.URL.Path == restorePreviewPath)
}

func (h *backupHandler) create(w http.ResponseWriter, r *http.Request) {
	if !jsonContentType(r.Header.Get("Content-Type")) {
		writeError(w, r, http.StatusUnsupportedMediaType, "UNSUPPORTED_CONTENT", "Content-Type must be application/json.")
		return
	}
	raw, err := readRequestBody(w, r, backupOptionsMax)
	if err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "Backup options exceed 64 KiB.")
		return
	}
	var options backupCreateOptions
	if !strictJSON(raw, &options, "includeSecrets", "includeCredentials", "passphrase", "password") || options.IncludeSecrets == nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid backup options.")
		return
	}
	includeCredentials := options.IncludeCredentials != nil && *options.IncludeCredentials
	sensitive := *options.IncludeSecrets || includeCredentials
	if sensitive && backup.ValidatePassphrase(options.Passphrase) != nil || !sensitive && (options.Passphrase != "" || options.Password != "") {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "A valid passphrase is required only when secrets or credentials are included.")
		return
	}
	if !h.expensive.TryLock() {
		writeError(w, r, http.StatusConflict, "BACKUP_BUSY", "Another backup or restore operation is running.")
		return
	}
	defer h.expensive.Unlock()
	actor, ok := actorFromRequest(r)
	if !ok {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "A verified root or human session is required.")
		return
	}
	if sensitive && !actor.Root && (actor.Actor.Kind != "account" || !hasCapability(actor, "backups.exportSecrets") || options.Password == "") {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "Secret export requires a human session, export capability, and password reauthentication.")
		return
	}
	var exportIntent audit.Intent
	if sensitive {
		var operationID [16]byte
		if _, err = rand.Read(operationID[:]); err != nil {
			writeError(w, r, http.StatusServiceUnavailable, "STORE_UNAVAILABLE", "Secret export could not create an audit operation.")
			return
		}
		requestHash, hashErr := h.identities.HashRequest(r.Context(), raw)
		if hashErr != nil {
			writeError(w, r, http.StatusServiceUnavailable, "STORE_UNAVAILABLE", "Secret export could not create an audit operation.")
			return
		}
		exportIntent = audit.Intent{OperationID: hex.EncodeToString(operationID[:]), Actor: actor.Actor, Source: actor.Source, Action: audit.ActionSecretExported, RequestHash: requestHash, Category: audit.CategoryProtected}
	}

	var documents config.BackupDocuments
	var identityDocuments identity.BackupDocuments
	var observed generation.Observation
	captured := time.Time{}
	password := []byte(options.Password)
	defer clear(password)
	auditStarted, auditFinished := false, false
	defer func() {
		if auditStarted && !auditFinished {
			_ = h.security.audits.Finish(context.WithoutCancel(r.Context()), audit.Outcome{OperationID: exportIntent.OperationID, Result: audit.ResultNotCommitted})
		}
	}()
	err = generation.Observe(r.Context(), generation.Dir(h.stateDir), func(value generation.Observation) error {
		observed = value
		return h.security.withAuthorizationLock(r.Context(), func() error {
			if sensitive && !actor.Root {
				valid, verifyErr := h.identities.VerifyPassword(r.Context(), actor.AccountID, password)
				if verifyErr != nil {
					return verifyErr
				}
				if !valid {
					return identity.ErrAuthentication
				}
			}
			if sensitive {
				if _, beginErr := h.security.audits.Begin(r.Context(), exportIntent); beginErr != nil {
					return beginErr
				}
				auditStarted = true
			}
			var exportErr error
			documents, exportErr = h.store.Export(r.Context(), *options.IncludeSecrets)
			if exportErr == nil {
				identityDocuments, exportErr = h.identities.ExportBackup(r.Context(), includeCredentials)
			}
			captured = time.Now().UTC()
			return exportErr
		})
	})
	if errors.Is(err, identity.ErrAuthentication) {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "Secret export requires a human session, export capability, and password reauthentication.")
		return
	}
	if err != nil {
		if errors.Is(err, audit.ErrOperationConflict) || errors.Is(err, audit.ErrOperationInFlight) || errors.Is(err, audit.ErrGeneralSaturated) || errors.Is(err, audit.ErrProtectedSaturated) || errors.Is(err, audit.ErrStorage) || errors.Is(err, audit.ErrIntegrity) || errors.Is(err, audit.ErrReconciliationRequired) {
			writeSecurityError(w, r, err)
		} else {
			writeError(w, r, http.StatusServiceUnavailable, "STORE_UNAVAILABLE", "Backup capture is unavailable; inspect the engine journal.")
		}
		return
	}
	realized, err := realizes(r.Context(), h.store.RealizationInput, &documents.SavedVersion, config.Version(observed.LiveSavedVersion))
	if err != nil {
		slog.WarnContext(r.Context(), "compare the saved version with the live generation for the backup", "error", err)
	}
	encoded, err := backup.Encode(r.Context(), backup.Source{
		AndaruVersion: buildVersion,
		SavedVersion:  string(documents.SavedVersion),
		CapturedAt:    captured,
		Network:       backup.NetworkObservation{Live: observed.Live, Pending: observed.Pending, LiveSavedVersion: observed.LiveSavedVersion},
		Realized:      realized,
	}, backup.Documents{Configuration: documents.Configuration, ConfigurationSecrets: documents.ConfigurationSecrets, Users: identityDocuments.Users, UserCredentials: identityDocuments.UserCredentials}, options.Passphrase)
	if err != nil {
		writeBackupError(w, r, err)
		return
	}
	if sensitive {
		if finishErr := h.security.audits.Finish(r.Context(), audit.Outcome{OperationID: exportIntent.OperationID, Result: audit.ResultSuccess}); finishErr != nil {
			writeSecurityError(w, r, finishErr)
			return
		}
		auditFinished = true
	}
	contentType := "application/zip"
	if sensitive {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Andaru-Saved-Version", string(documents.SavedVersion))
	w.Header().Set("Andaru-Identity-Revision", fmt.Sprint(identityDocuments.IdentityRevision))
	w.Header().Set("Andaru-Captured-At", captured.Format(time.RFC3339Nano))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func (h *backupHandler) restore(w http.ResponseWriter, r *http.Request, preview bool) {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		writeError(w, r, http.StatusUnsupportedMediaType, "UNSUPPORTED_CONTENT", "Content-Type must be multipart/form-data.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, restoreBodyMax)
	archiveBytes, optionsBytes, err := readRestoreParts(multipart.NewReader(r.Body, params["boundary"]))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) || errors.Is(err, errArchiveLimit) {
			code := "REQUEST_TOO_LARGE"
			if errors.Is(err, errArchiveLimit) {
				code = "ARCHIVE_LIMIT"
			}
			writeError(w, r, http.StatusRequestEntityTooLarge, code, "Restore request exceeds its limit.")
			return
		}
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid restore multipart body.")
		return
	}
	var options restoreOptions
	if !strictJSON(optionsBytes, &options, "passphrase", "stores", "operationId", "expectedSavedVersion", "expectedIdentityRevision", "rootAdministrator") {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid restore options.")
		return
	}
	if !validStores(options.Stores, preview) || preview && options.RootAdministrator != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "INVALID_RESTORE", "Restore store selection must be nonempty, unique, and supported.")
		return
	}
	if !h.expensive.TryLock() {
		writeError(w, r, http.StatusConflict, "BACKUP_BUSY", "Another backup or restore operation is running.")
		return
	}
	defer h.expensive.Unlock()

	archive, err := backup.Decode(r.Context(), archiveBytes, options.Passphrase)
	if err != nil {
		writeBackupError(w, r, err)
		return
	}
	allConfig := config.RestoreDocuments{Configuration: archive.Documents.Configuration, ConfigurationSecrets: archive.Documents.ConfigurationSecrets}
	if err = config.ValidateBackupDocuments(allConfig); err != nil {
		writeConfigurationError(w, r, err)
		return
	}
	if err = identity.ValidateBackupDocuments(identity.RestoreDocuments{Users: archive.Documents.Users, UserCredentials: archive.Documents.UserCredentials}); err != nil {
		writeIdentityError(w, r, err)
		return
	}
	if len(options.Stores) == 0 {
		writeJSON(w, r, http.StatusOK, inspectionResponse{State: "inspection", Manifest: archive.Manifest})
		return
	}
	selected, ok := selectDocuments(archive.Documents, options.Stores)
	if !ok {
		writeError(w, r, http.StatusUnprocessableEntity, "UNSUPPORTED_SCOPE", "A selected store is not present in the archive.")
		return
	}
	configInput := config.RestoreDocuments{Configuration: selected.Configuration, ConfigurationSecrets: selected.ConfigurationSecrets}
	identityInput := identity.RestoreDocuments{Users: selected.Users, UserCredentials: selected.UserCredentials}
	if preview {
		response := restorePreviewResponse{State: "restore-preview", Manifest: archive.Manifest, UnresolvedSecrets: []string{}, BlockingSecrets: []string{}, MissingRoleIDs: []string{}, RecoveryAdministrators: []identity.RecoveryAdministrator{}, HardwareFindings: []hardwareFinding{}}
		if configInput.Configuration != nil || configInput.ConfigurationSecrets != nil {
			plan, planErr := h.store.PreviewRestore(r.Context(), configInput)
			if planErr != nil {
				writeConfigurationError(w, r, planErr)
				return
			}
			response.ExpectedSavedVersion = plan.ExpectedSavedVersion
			response.ConfigurationChanges, response.SecretChanges = counts(plan.ConfigurationChanges), counts(plan.SecretChanges)
			response.UnresolvedSecrets, response.BlockingSecrets = nonNil(plan.UnresolvedSecrets), nonNil(plan.BlockingSecrets)
			response.HardwareFindings, err = h.hardware(r.Context(), plan.Configuration)
			if err != nil {
				writeError(w, r, http.StatusServiceUnavailable, "INVENTORY_UNAVAILABLE", "Interface inventory is unavailable; inspect the engine journal.")
				return
			}
		}
		if identityInput.Users != nil || identityInput.UserCredentials != nil {
			plan, planErr := h.identities.PreviewRestore(r.Context(), identityInput)
			if planErr != nil {
				writeIdentityError(w, r, planErr)
				return
			}
			response.ExpectedIdentityRevision = plan.ExpectedIdentityRevision
			response.UserChanges, response.CredentialChanges = identityCounts(plan.UserChanges), identityCounts(plan.CredentialChanges)
			knownRoles, rolesErr := h.restoreRoleIDs(r.Context(), configInput.Configuration)
			if rolesErr != nil {
				writeConfigurationError(w, r, rolesErr)
				return
			}
			response.MissingRoleIDs = missingSelectedRoles(plan.MissingRoleIDs, knownRoles)
			response.RequiresRootAdministrator = plan.RequiresRootAdministrator
			response.RecoveryAdministrators = plan.RecoveryAdministrators
		}
		writeJSON(w, r, http.StatusOK, response)
		return
	}
	if options.OperationID == "" || (configInput.Configuration != nil || configInput.ConfigurationSecrets != nil) && options.ExpectedSavedVersion == "" || (identityInput.Users != nil || identityInput.UserCredentials != nil) && options.ExpectedIdentityRevision == nil {
		writeError(w, r, http.StatusPreconditionRequired, "PRECONDITION_REQUIRED", "Operation ID and selected-store revisions are required.")
		return
	}
	actor, ok := actorFromRequest(r)
	if !ok {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "A verified actor is required.")
		return
	}
	var rootAdministrator *identity.RootAdministrator
	if options.RootAdministrator != nil {
		if !actor.Root {
			writeError(w, r, http.StatusForbidden, "FORBIDDEN", "Only the local root console may create a recovery administrator during restore.")
			return
		}
		password := []byte(options.RootAdministrator.Password)
		defer clear(password)
		rootAdministrator = &identity.RootAdministrator{AccountID: options.RootAdministrator.AccountID, Login: options.RootAdministrator.Login, DisplayName: options.RootAdministrator.DisplayName, Password: password}
	}
	fingerprint := sha256.New()
	_, _ = fingerprint.Write(archiveBytes)
	_, _ = fingerprint.Write([]byte{0})
	_, _ = fingerprint.Write(optionsBytes)
	expectedIdentityRevision := int64(0)
	if options.ExpectedIdentityRevision != nil {
		expectedIdentityRevision = *options.ExpectedIdentityRevision
	}
	result, err := h.restores.Execute(r.Context(), coordinatedRestoreRequest{
		OperationID: options.OperationID, Fingerprint: fingerprint.Sum(nil), Actor: actor.Actor, Source: actor.Source, Target: archive.Manifest.BackupID,
		ExpectedSavedVersion: config.Version(options.ExpectedSavedVersion), ExpectedIdentityRevision: expectedIdentityRevision,
		Configuration: configInput, Identity: identityInput, RootAdministrator: rootAdministrator,
	})
	if err != nil {
		writeRestoreError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, restoreSavedResponse{State: "saved", SavedVersion: result.SavedVersion, IdentityRevision: result.IdentityRevision, UnresolvedSecrets: nonNil(result.UnresolvedSecrets), Replayed: result.Replayed})
}

var errArchiveLimit = errors.New("archive part exceeds limit")

func readRestoreParts(reader *multipart.Reader) ([]byte, []byte, error) {
	var archive, options []byte
	seen := map[string]bool{}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		name := part.FormName()
		if (name != "archive" && name != "options") || seen[name] {
			part.Close()
			return nil, nil, errors.New("unexpected multipart part")
		}
		seen[name] = true
		limit := backupOptionsMax
		if name == "archive" {
			limit = backup.MaxArchiveSize
		}
		data, err := io.ReadAll(io.LimitReader(part, int64(limit)+1))
		part.Close()
		if err != nil {
			return nil, nil, err
		}
		if len(data) > limit {
			return nil, nil, errArchiveLimit
		}
		if name == "archive" {
			archive = data
		} else {
			options = data
		}
	}
	if !seen["archive"] || !seen["options"] || len(archive) == 0 || len(options) == 0 {
		return nil, nil, errors.New("missing multipart part")
	}
	return archive, options, nil
}

func (h *backupHandler) hardware(ctx context.Context, declaration json.RawMessage) ([]hardwareFinding, error) {
	if declaration == nil {
		return []hardwareFinding{}, nil
	}
	links, err := h.readLinks(ctx)
	if err != nil {
		return nil, err
	}
	findings, err := render.BindingFindings(declaration, links)
	if err != nil {
		return nil, err
	}
	out := make([]hardwareFinding, 0, len(findings))
	for _, finding := range findings {
		out = append(out, hardwareFinding{Code: finding.Code, Path: finding.Path, Message: finding.Message})
	}
	return out, nil
}

func (h *backupHandler) restoreRoleIDs(ctx context.Context, selected json.RawMessage) ([]string, error) {
	raw := selected
	if raw == nil {
		snapshot, err := h.store.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		raw = snapshot.Value
	}
	var declaration struct {
		Roles []struct {
			ID string `json:"id"`
		} `json:"roles"`
	}
	if err := json.Unmarshal(raw, &declaration); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(declaration.Roles))
	for _, role := range declaration.Roles {
		ids = append(ids, role.ID)
	}
	return ids, nil
}

func missingSelectedRoles(referenced, known []string) []string {
	missing := make([]string, 0, len(referenced))
	for _, id := range referenced {
		if !slices.Contains(known, id) {
			missing = append(missing, id)
		}
	}
	return missing
}

func selectDocuments(documents backup.Documents, stores []string) (backup.Documents, bool) {
	var selected backup.Documents
	for _, store := range stores {
		switch store {
		case "configuration":
			if documents.Configuration == nil {
				return backup.Documents{}, false
			}
			selected.Configuration = documents.Configuration
		case "configurationSecrets":
			if documents.ConfigurationSecrets == nil {
				return backup.Documents{}, false
			}
			selected.ConfigurationSecrets = documents.ConfigurationSecrets
		case "users":
			if documents.Users == nil {
				return backup.Documents{}, false
			}
			selected.Users = documents.Users
		case "userCredentials":
			if documents.UserCredentials == nil {
				return backup.Documents{}, false
			}
			selected.UserCredentials = documents.UserCredentials
		}
	}
	return selected, true
}

func validStores(stores []string, preview bool) bool {
	if len(stores) == 0 {
		return preview
	}
	if len(stores) > 4 {
		return false
	}
	seen := map[string]bool{}
	for _, store := range stores {
		if seen[store] || !slices.Contains([]string{"configuration", "configurationSecrets", "users", "userCredentials"}, store) {
			return false
		}
		seen[store] = true
	}
	return true
}

func counts(value config.RestoreCounts) restoreCounts {
	return restoreCounts{Created: value.Created, Updated: value.Updated, Deleted: value.Deleted}
}
func identityCounts(value identity.RestoreCounts) restoreCounts {
	return restoreCounts{Created: value.Created, Updated: value.Updated, Deleted: value.Deleted}
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func strictJSON(raw []byte, target any, allowed ...string) bool {
	if len(raw) == 0 || !jsonprofile.Valid(raw, false) {
		return false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return false
	}
	for key := range object {
		if !slices.Contains(allowed, key) {
			return false
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil && decoder.Decode(&struct{}{}) == io.EOF
}

func jsonContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && mediaType == "application/json"
}

func readRequestBody(w http.ResponseWriter, r *http.Request, limit int) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, int64(limit))
	return io.ReadAll(r.Body)
}

func writeBackupError(w http.ResponseWriter, r *http.Request, err error) {
	var archiveError *backup.Error
	if !errors.As(err, &archiveError) {
		writeError(w, r, http.StatusServiceUnavailable, "STORE_UNAVAILABLE", "Backup processing is unavailable.")
		return
	}
	status := http.StatusUnprocessableEntity
	message := "Archive content is not supported or could not be authenticated."
	switch archiveError.Code {
	case "ARCHIVE_LIMIT":
		status, message = http.StatusRequestEntityTooLarge, "Archive exceeds its limit."
	case "INVALID_ARCHIVE":
		status, message = http.StatusBadRequest, "Archive is malformed."
	}
	code := archiveError.Code
	if code == "PASSPHRASE_INVALID" || code == "DECRYPTION_FAILED" {
		message = "The archive passphrase is invalid or decryption failed."
	}
	writeError(w, r, status, code, message)
}

func writeRestoreError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errRestoreOperationConflict):
		writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Operation ID conflicts with a different recorded restore.")
	case errors.Is(err, errJournalNewer):
		writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Operation ID belongs to a restore a newer Andaru recorded.")
	case errors.Is(err, errRestoreAbandoned):
		writeError(w, r, http.StatusConflict, "RESTORE_ABANDONED", "This restore never settled and was set aside; start a new one.")
	case errors.Is(err, errRestoreStateConflict):
		writeError(w, r, http.StatusServiceUnavailable, "RESTORE_RECONCILIATION_REQUIRED", "The restore outcome cannot be reconciled safely.")
	case errors.Is(err, audit.ErrOperationConflict), errors.Is(err, audit.ErrOperationInFlight), errors.Is(err, audit.ErrGeneralSaturated), errors.Is(err, audit.ErrProtectedSaturated), errors.Is(err, audit.ErrStorage), errors.Is(err, audit.ErrIntegrity), errors.Is(err, audit.ErrReconciliationRequired):
		writeSecurityError(w, r, err)
	case errors.Is(err, identity.ErrInvalid), errors.Is(err, identity.ErrNotFound), errors.Is(err, identity.ErrConflict), errors.Is(err, identity.ErrDuplicate), errors.Is(err, identity.ErrInvariant), errors.Is(err, identity.ErrAccountLimit), errors.Is(err, identity.ErrTokenLimit):
		writeIdentityError(w, r, err)
	default:
		writeConfigurationError(w, r, err)
	}
}
