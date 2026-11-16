package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/andaru-project/andaru/internal/backup"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/host"
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

type backupCreateOptions struct {
	IncludeSecrets *bool  `json:"includeSecrets"`
	Passphrase     string `json:"passphrase,omitempty"`
}

type restoreOptions struct {
	Passphrase           string   `json:"passphrase,omitempty"`
	Stores               []string `json:"stores,omitempty"`
	ExpectedSavedVersion string   `json:"expectedSavedVersion,omitempty"`
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
	State                string            `json:"state"`
	Manifest             backup.Manifest   `json:"manifest"`
	ExpectedSavedVersion config.Version    `json:"expectedSavedVersion"`
	ConfigurationChanges restoreCounts     `json:"configurationChanges"`
	SecretChanges        restoreCounts     `json:"secretChanges"`
	UnresolvedSecrets    []string          `json:"unresolvedSecrets"`
	BlockingSecrets      []string          `json:"blockingSecrets"`
	HardwareFindings     []hardwareFinding `json:"hardwareFindings"`
}

type inspectionResponse struct {
	State    string          `json:"state"`
	Manifest backup.Manifest `json:"manifest"`
}

type restoreSavedResponse struct {
	State             string         `json:"state"`
	SavedVersion      config.Version `json:"savedVersion"`
	UnresolvedSecrets []string       `json:"unresolvedSecrets"`
}

type backupHandler struct {
	store     *config.Store
	stateDir  string
	readLinks func(context.Context) ([]host.LinkStatus, error)
	expensive sync.Mutex
}

func newBackupHandler(store *config.Store, stateDir string, readLinks func(context.Context) ([]host.LinkStatus, error)) http.Handler {
	return &backupHandler{store: store, stateDir: stateDir, readLinks: readLinks}
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
	if !strictJSON(raw, &options, "includeSecrets", "passphrase") || options.IncludeSecrets == nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid backup options.")
		return
	}
	if (*options.IncludeSecrets && backup.ValidatePassphrase(options.Passphrase) != nil) || (!*options.IncludeSecrets && options.Passphrase != "") {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "A valid passphrase is required only when secrets are included.")
		return
	}
	if !h.expensive.TryLock() {
		writeError(w, r, http.StatusConflict, "BACKUP_BUSY", "Another backup or restore operation is running.")
		return
	}
	defer h.expensive.Unlock()

	var documents config.BackupDocuments
	var observed generation.Observation
	captured := time.Time{}
	err = generation.Observe(r.Context(), generation.Dir(h.stateDir), func(value generation.Observation) error {
		observed = value
		var exportErr error
		documents, exportErr = h.store.Export(r.Context(), *options.IncludeSecrets)
		captured = time.Now().UTC()
		return exportErr
	})
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORE_UNAVAILABLE", "Backup capture is unavailable; inspect the engine journal.")
		return
	}
	encoded, err := backup.Encode(r.Context(), backup.Source{
		AndaruVersion: buildVersion,
		SavedVersion:  string(documents.SavedVersion),
		CapturedAt:    captured,
		Network:       backup.NetworkObservation{Live: observed.Live, Pending: observed.Pending, LiveSavedVersion: observed.LiveSavedVersion},
	}, backup.Documents{Configuration: documents.Configuration, ConfigurationSecrets: documents.ConfigurationSecrets}, options.Passphrase)
	if err != nil {
		writeBackupError(w, r, err)
		return
	}
	contentType := "application/zip"
	if *options.IncludeSecrets {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Andaru-Saved-Version", string(documents.SavedVersion))
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
	if !strictJSON(optionsBytes, &options, "passphrase", "stores", "expectedSavedVersion") {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid restore options.")
		return
	}
	if !validStores(options.Stores, preview) {
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
	all := config.RestoreDocuments{Configuration: archive.Documents.Configuration, ConfigurationSecrets: archive.Documents.ConfigurationSecrets}
	if err = config.ValidateBackupDocuments(all); err != nil {
		writeConfigurationError(w, r, err)
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
	input := config.RestoreDocuments{Configuration: selected.Configuration, ConfigurationSecrets: selected.ConfigurationSecrets}
	if preview {
		plan, err := h.store.PreviewRestore(r.Context(), input)
		if err != nil {
			writeConfigurationError(w, r, err)
			return
		}
		findings, err := h.hardware(r.Context(), plan.Configuration)
		if err != nil {
			writeError(w, r, http.StatusServiceUnavailable, "INVENTORY_UNAVAILABLE", "Interface inventory is unavailable; inspect the engine journal.")
			return
		}
		writeJSON(w, r, http.StatusOK, restorePreviewResponse{
			State: "restore-preview", Manifest: archive.Manifest, ExpectedSavedVersion: plan.ExpectedSavedVersion,
			ConfigurationChanges: counts(plan.ConfigurationChanges), SecretChanges: counts(plan.SecretChanges),
			UnresolvedSecrets: nonNil(plan.UnresolvedSecrets), BlockingSecrets: nonNil(plan.BlockingSecrets), HardwareFindings: findings,
		})
		return
	}
	actor, ok := actorFromRequest(r)
	if !ok {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "A verified actor is required.")
		return
	}
	result, err := h.store.Restore(r.Context(), actor.Actor, config.Version(options.ExpectedSavedVersion), input)
	if err != nil {
		writeConfigurationError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, restoreSavedResponse{State: "saved", SavedVersion: result.SavedVersion, UnresolvedSecrets: nonNil(result.UnresolvedSecrets)})
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
		}
	}
	return selected, true
}

func validStores(stores []string, preview bool) bool {
	if len(stores) == 0 {
		return preview
	}
	if len(stores) > 2 {
		return false
	}
	seen := map[string]bool{}
	for _, store := range stores {
		if seen[store] || (store != "configuration" && store != "configurationSecrets") {
			return false
		}
		seen[store] = true
	}
	return true
}

func counts(value config.RestoreCounts) restoreCounts {
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
