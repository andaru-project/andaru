package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"slices"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/backup"
	"github.com/andaru-project/andaru/internal/config"
)

const backupUsage = "usage: andaru ctl [--socket PATH] backup create --output FILE [--include-secrets] | backup inspect --input FILE | backup restore --input FILE --stores configuration[,configurationSecrets]"

type backupCommand struct {
	verb, input, output string
	stores              []string
	includeSecrets      bool
}

type restoreOptions struct {
	Passphrase           string   `json:"passphrase,omitempty"`
	Stores               []string `json:"stores,omitempty"`
	ExpectedSavedVersion string   `json:"expectedSavedVersion,omitempty"`
}

type changeCounts struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
	Deleted int `json:"deleted"`
}

type hardwareFinding struct {
	Code, Path, Message string
}

type restorePreview struct {
	State                string            `json:"state"`
	Manifest             backup.Manifest   `json:"manifest"`
	ExpectedSavedVersion string            `json:"expectedSavedVersion"`
	ConfigurationChanges changeCounts      `json:"configurationChanges"`
	SecretChanges        changeCounts      `json:"secretChanges"`
	UnresolvedSecrets    []string          `json:"unresolvedSecrets"`
	BlockingSecrets      []string          `json:"blockingSecrets"`
	HardwareFindings     []hardwareFinding `json:"hardwareFindings"`
}

type restoreReply struct {
	State             string   `json:"state"`
	SavedVersion      string   `json:"savedVersion"`
	UnresolvedSecrets []string `json:"unresolvedSecrets"`
}

type backupErrorEnvelope struct {
	Error struct {
		Code    string             `json:"code"`
		Message string             `json:"message"`
		Details *[]json.RawMessage `json:"details"`
	} `json:"error"`
}

func parseBackupCommand(args []string) (backupCommand, error) {
	if len(args) == 0 {
		return backupCommand{}, errors.New("missing command")
	}
	cmd := backupCommand{verb: args[0]}
	seen := map[string]bool{}
	for i := 1; i < len(args); i++ {
		name := args[i]
		if seen[name] {
			return backupCommand{}, errors.New("repeated option")
		}
		seen[name] = true
		switch name {
		case "--include-secrets":
			if cmd.verb != "create" {
				return backupCommand{}, errors.New("unknown option")
			}
			cmd.includeSecrets = true
		case "--input", "--output", "--stores":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return backupCommand{}, errors.New("missing option value")
			}
			value := args[i+1]
			i++
			switch name {
			case "--input":
				cmd.input = value
			case "--output":
				cmd.output = value
			case "--stores":
				if value == "" {
					return backupCommand{}, errors.New("empty stores")
				}
				cmd.stores = strings.Split(value, ",")
				if len(cmd.stores) > 2 {
					return backupCommand{}, errors.New("invalid stores")
				}
				for j, store := range cmd.stores {
					if !slices.Contains([]string{"configuration", "configurationSecrets"}, store) || slices.Contains(cmd.stores[:j], store) {
						return backupCommand{}, errors.New("invalid stores")
					}
				}
			default:
			}
		default:
			return backupCommand{}, errors.New("unknown option")
		}
	}
	switch cmd.verb {
	case "create":
		if cmd.output == "" || cmd.input != "" || len(cmd.stores) != 0 {
			return backupCommand{}, errors.New("invalid create arguments")
		}
	case "inspect":
		if cmd.input == "" || cmd.output != "" || cmd.includeSecrets || len(cmd.stores) != 0 {
			return backupCommand{}, errors.New("invalid inspect arguments")
		}
	case "restore":
		if cmd.input == "" || cmd.output != "" || cmd.includeSecrets || len(cmd.stores) == 0 {
			return backupCommand{}, errors.New("invalid restore arguments")
		}
	default:
		return backupCommand{}, errors.New("unknown command")
	}
	return cmd, nil
}

func runBackup(ctx context.Context, socket string, args []string, _ io.Reader, stdout, stderr io.Writer) int {
	cmd, err := parseBackupCommand(args)
	if err != nil {
		fmt.Fprintln(stderr, backupUsage)
		return 2
	}
	if cmd.verb == "create" {
		return backupCreate(ctx, socket, cmd, stdout, stderr)
	}
	raw, err := readBackupFile(cmd.input)
	if err != nil {
		fmt.Fprintln(stderr, "backup input could not be read safely within its size limit")
		return 1
	}
	if cmd.verb == "restore" && !backupTerminalAvailable() {
		fmt.Fprintln(stderr, "restore confirmation requires a controlling terminal")
		return 1
	}
	passphrase := ""
	if bytes.HasPrefix(raw, []byte("age-encryption.org/")) {
		passphrase, err = readBackupPassphrase(ctx, "Backup passphrase: ")
		if err != nil {
			fmt.Fprintln(stderr, "encrypted backups require a controlling terminal and a valid passphrase")
			return 1
		}
	}
	options := restoreOptions{Passphrase: passphrase}
	if cmd.verb == "restore" {
		options.Stores = cmd.stores
	}
	preview, previewRaw, err := requestRestorePreview(ctx, socket, raw, options)
	if err != nil {
		writeBackupError(stderr, err)
		return 1
	}
	if cmd.verb == "inspect" {
		if err = writeBackupJSON(stdout, previewRaw); err != nil {
			fmt.Fprintln(stderr, "could not write backup response")
			return 1
		}
		return 0
	}
	previewOutput, err := json.Marshal(struct {
		restorePreview
		SelectedStores    []string `json:"selectedStores"`
		NetworkingApplied bool     `json:"networkingApplied"`
	}{restorePreview: preview, SelectedStores: slices.Clone(cmd.stores), NetworkingApplied: false})
	if err != nil || writeBackupJSON(stdout, previewOutput) != nil {
		fmt.Fprintln(stderr, "could not write backup response")
		return 1
	}
	confirmation, err := readBackupPassphrase(ctx, "Type RESTORE to replace the selected saved stores (networking is not applied): ")
	if err != nil || confirmation != "RESTORE" {
		fmt.Fprintln(stderr, "restore cancelled; type RESTORE exactly on a controlling terminal")
		return 1
	}
	options.ExpectedSavedVersion = preview.ExpectedSavedVersion
	replyRaw, err := requestRestoreCommit(ctx, socket, raw, options)
	if err != nil {
		writeBackupError(stderr, err)
		return 1
	}
	if err = writeBackupJSON(stdout, replyRaw); err != nil {
		fmt.Fprintln(stderr, "could not write backup response")
		return 1
	}
	return 0
}

func backupCreate(ctx context.Context, socket string, cmd backupCommand, stdout, stderr io.Writer) int {
	passphrase := ""
	var err error
	if cmd.includeSecrets {
		passphrase, err = readNewBackupPassphrase(ctx)
		if err != nil {
			fmt.Fprintln(stderr, "secret backups require a controlling terminal and two matching valid passphrases")
			return 1
		}
	}
	payload, _ := json.Marshal(struct {
		IncludeSecrets bool   `json:"includeSecrets"`
		Passphrase     string `json:"passphrase,omitempty"`
	}{cmd.includeSecrets, passphrase})
	client, transport := unixHTTPClient(socket, 160*time.Second)
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/api/v1/backups", bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintln(stderr, "could not construct local backup request")
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(stderr, "could not reach the local engine; no backup file was published")
		return 1
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		writeHTTPBackupError(stderr, response)
		return 1
	}
	wantType := "application/zip"
	if cmd.includeSecrets {
		wantType = "application/octet-stream"
	}
	if response.Header.Get("Content-Type") != wantType || response.Header.Get("Cache-Control") != "no-store" {
		fmt.Fprintln(stderr, "engine returned invalid backup response metadata")
		return 1
	}
	savedVersion := response.Header.Get("Andaru-Saved-Version")
	capturedAt := response.Header.Get("Andaru-Captured-At")
	captured, timeErr := time.Parse(time.RFC3339Nano, capturedAt)
	if !validVersion(config.Version(savedVersion)) || timeErr != nil || captured.Format(time.RFC3339Nano) != capturedAt {
		fmt.Fprintln(stderr, "engine returned invalid backup source metadata")
		return 1
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, backup.MaxArchiveSize+1))
	if err != nil || len(raw) == 0 || len(raw) > backup.MaxArchiveSize {
		fmt.Fprintln(stderr, "engine backup response exceeded its read/size budget")
		return 1
	}
	archive, err := backup.Decode(ctx, raw, passphrase)
	if err != nil || archive.Manifest.SavedVersion != savedVersion || !archive.Manifest.CreatedAt.Equal(captured) || slices.Contains(archive.Manifest.Scopes, "configurationSecrets") != cmd.includeSecrets {
		fmt.Fprintln(stderr, "engine returned an invalid backup archive")
		return 1
	}
	path, err := publishBackupFile(cmd.output, raw)
	if err != nil {
		fmt.Fprintln(stderr, "backup output could not be published durably without replacing a file")
		return 1
	}
	status, _ := json.Marshal(struct {
		State        string `json:"state"`
		Path         string `json:"path"`
		SavedVersion string `json:"savedVersion"`
		CapturedAt   string `json:"capturedAt"`
	}{"created", path, savedVersion, capturedAt})
	if _, err = stdout.Write(append(status, '\n')); err != nil {
		fmt.Fprintln(stderr, "could not write backup status")
		return 1
	}
	return 0
}

func multipartBackup(raw []byte, options restoreOptions) ([]byte, string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="archive"; filename="backup"`)
	header.Set("Content-Type", "application/octet-stream")
	part, err := writer.CreatePart(header)
	if err == nil {
		_, err = part.Write(raw)
	}
	if err == nil {
		header = make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="options"`)
		header.Set("Content-Type", "application/json")
		part, err = writer.CreatePart(header)
	}
	if err == nil {
		var encoded []byte
		encoded, err = json.Marshal(options)
		if err == nil && len(encoded) <= 64<<10 {
			_, err = part.Write(encoded)
		}
	}
	if err == nil {
		err = writer.Close()
	}
	return body.Bytes(), writer.FormDataContentType(), err
}

type backupResponseError struct {
	message string
}

func (e *backupResponseError) Error() string { return e.message }

func requestRestorePreview(ctx context.Context, socket string, raw []byte, options restoreOptions) (restorePreview, []byte, error) {
	body, contentType, err := multipartBackup(raw, options)
	if err != nil {
		return restorePreview{}, nil, &backupResponseError{message: "could not construct restore preview request"}
	}
	response, err := doBackupRequest(ctx, socket, "/api/v1/restores/preview", body, contentType)
	if err != nil {
		return restorePreview{}, nil, err
	}
	defer response.Body.Close()
	data, err := readJSONBackupResponse(response)
	if err != nil {
		return restorePreview{}, nil, err
	}
	var preview restorePreview
	if options.Stores == nil {
		var inspection struct {
			State    string          `json:"state"`
			Manifest backup.Manifest `json:"manifest"`
		}
		if decodeStrict(data, &inspection) != nil || inspection.State != "inspection" || !validManifest(inspection.Manifest) {
			return restorePreview{}, nil, &backupResponseError{message: "engine returned an invalid inspection response"}
		}
		preview.State, preview.Manifest = inspection.State, inspection.Manifest
	} else if decodeStrict(data, &preview) != nil || preview.State != "restore-preview" || !validManifest(preview.Manifest) || !validVersion(config.Version(preview.ExpectedSavedVersion)) || preview.UnresolvedSecrets == nil || preview.BlockingSecrets == nil || preview.HardwareFindings == nil || !validRestorePreview(preview) {
		return restorePreview{}, nil, &backupResponseError{message: "engine returned an invalid restore preview response"}
	}
	return preview, data, nil
}

func requestRestoreCommit(ctx context.Context, socket string, raw []byte, options restoreOptions) ([]byte, error) {
	body, contentType, err := multipartBackup(raw, options)
	if err != nil {
		return nil, &backupResponseError{message: "could not construct restore request"}
	}
	response, err := doBackupRequest(ctx, socket, "/api/v1/restores", body, contentType)
	if err != nil {
		return nil, unknownRestoreOutcome()
	}
	defer response.Body.Close()
	data, err := readJSONBackupResponse(response)
	if err != nil {
		if response.StatusCode == http.StatusOK {
			return nil, unknownRestoreOutcome()
		}
		return nil, err
	}
	var reply restoreReply
	if decodeStrict(data, &reply) != nil || reply.State != "saved" || !validVersion(config.Version(reply.SavedVersion)) || reply.UnresolvedSecrets == nil {
		return nil, unknownRestoreOutcome()
	}
	return data, nil
}

func unknownRestoreOutcome() error {
	return &backupResponseError{message: "restore response unavailable; outcome may be unknown; inspect the current saved head before retrying"}
}

type backupHTTPBody struct {
	io.ReadCloser
	transport *http.Transport
}

func (body *backupHTTPBody) Close() error {
	err := body.ReadCloser.Close()
	body.transport.CloseIdleConnections()
	return err
}

func doBackupRequest(ctx context.Context, socket, path string, body []byte, contentType string) (*http.Response, error) {
	client, transport := unixHTTPClient(socket, 160*time.Second)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix"+path, bytes.NewReader(body))
	if err != nil {
		transport.CloseIdleConnections()
		return nil, &backupResponseError{message: "could not construct local restore request"}
	}
	request.Header.Set("Content-Type", contentType)
	response, err := client.Do(request)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, &backupResponseError{message: "could not reach the local engine"}
	}
	response.Body = &backupHTTPBody{ReadCloser: response.Body, transport: transport}
	return response, nil
}

func readJSONBackupResponse(response *http.Response) ([]byte, error) {
	if response.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		return nil, &backupResponseError{message: fmt.Sprintf("engine returned HTTP %d with an invalid response type", response.StatusCode)}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil || len(data) > responseLimit {
		return nil, &backupResponseError{message: "engine response exceeded its read/size budget"}
	}
	if response.StatusCode != http.StatusOK {
		if validAPIError(data) {
			return nil, &backupResponseError{message: string(data)}
		}
		return nil, &backupResponseError{message: fmt.Sprintf("engine returned HTTP %d", response.StatusCode)}
	}
	return data, nil
}

func writeHTTPBackupError(stderr io.Writer, response *http.Response) {
	data, _ := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if response.Header.Get("Content-Type") == "application/json; charset=utf-8" && validAPIError(data) {
		fmt.Fprintln(stderr, string(data))
		return
	}
	fmt.Fprintf(stderr, "engine returned HTTP %d\n", response.StatusCode)
}

func writeBackupError(stderr io.Writer, err error) { fmt.Fprintln(stderr, err) }

func validAPIError(data []byte) bool {
	var body backupErrorEnvelope
	return decodeStrict(data, &body) == nil && body.Error.Code != "" && body.Error.Message != "" && body.Error.Details != nil
}

func writeBackupJSON(writer io.Writer, data []byte) error {
	n, err := writer.Write(data)
	if err != nil || n != len(data) {
		if err != nil {
			return err
		}
		return io.ErrShortWrite
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		_, err = io.WriteString(writer, "\n")
	}
	return err
}

func decodeStrict(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func validManifest(m backup.Manifest) bool {
	if m.FormatVersion != 1 || len(m.BackupID) != 32 || m.AndaruVersion == "" || len(m.AndaruVersion) > 128 || !validVersion(config.Version(m.SavedVersion)) || m.CreatedAt.IsZero() || m.CreatedAt.Location() != time.UTC || len(m.Scopes) == 0 || m.Payloads == nil || m.SavedButUnappliedAreas == nil {
		return false
	}
	if _, err := hex.DecodeString(m.BackupID); err != nil {
		return false
	}
	for _, r := range m.AndaruVersion {
		if r < 33 || r > 126 {
			return false
		}
	}
	if len(m.Scopes) > 2 || m.Scopes[0] != "configuration" || (len(m.Scopes) == 2 && m.Scopes[1] != "configurationSecrets") || len(m.Payloads) != len(m.Scopes) {
		return false
	}
	for _, scope := range m.Scopes {
		name := "config.json"
		if scope == "configurationSecrets" {
			name = "configuration-secrets.json"
		}
		payload, ok := m.Payloads[name]
		if !ok || payload.SchemaVersion < 1 || payload.SchemaVersion > 2 || payload.Size <= 0 || payload.Size > backup.MaxDocumentSize || len(payload.SHA256) != 64 {
			return false
		}
		if _, err := hex.DecodeString(payload.SHA256); err != nil {
			return false
		}
	}
	if m.Network.Live == nil {
		if m.Network.LiveSavedVersion != "" {
			return false
		}
	} else if *m.Network.Live <= 0 || !validVersion(config.Version(m.Network.LiveSavedVersion)) {
		return false
	}
	if m.Network.Pending != nil && *m.Network.Pending <= 0 {
		return false
	}
	return len(m.SavedButUnappliedAreas) == 0 || (len(m.SavedButUnappliedAreas) == 1 && m.SavedButUnappliedAreas[0] == "network")
}

func validRestorePreview(preview restorePreview) bool {
	for _, counts := range []changeCounts{preview.ConfigurationChanges, preview.SecretChanges} {
		if counts.Created < 0 || counts.Updated < 0 || counts.Deleted < 0 {
			return false
		}
	}
	for _, values := range [][]string{preview.UnresolvedSecrets, preview.BlockingSecrets} {
		for i, value := range values {
			if value == "" || slices.Contains(values[:i], value) {
				return false
			}
		}
	}
	for _, finding := range preview.HardwareFindings {
		if finding.Code == "" || finding.Path == "" || finding.Message == "" {
			return false
		}
	}
	return true
}
