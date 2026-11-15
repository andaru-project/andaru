package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/backup"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func backupErrorSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(jsonschema.SchemeURLLoader{})
	for uri, raw := range map[string][]byte{
		"https://andaru.invalid/api/v1/openapi.json": openAPIDocument,
		"https://andaru.invalid/api/v1/schema":       config.SchemaBundle,
	} {
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource(uri, document); err != nil {
			t.Fatal(err)
		}
	}
	schema, err := compiler.Compile("https://andaru.invalid/api/v1/openapi.json#/components/schemas/ErrorEnvelope")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func assertBackupErrorContract(t *testing.T, schema *jsonschema.Schema, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("error response: %d %v %s", response.Code, response.Header(), response.Body)
	}
	var envelope errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != code || envelope.Error.Message == "" || envelope.Error.Details == nil {
		t.Fatalf("error envelope: %s", response.Body)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(response.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(value); err != nil {
		t.Fatalf("response does not match ErrorEnvelope: %v\n%s", err, response.Body)
	}
}

func TestBackupErrorEnvelopeContract(t *testing.T) {
	schema := backupErrorSchema(t)
	for _, tc := range []struct {
		code   string
		status int
	}{
		{"INVALID_ARCHIVE", http.StatusBadRequest},
		{"ARCHIVE_LIMIT", http.StatusRequestEntityTooLarge},
		{"PASSPHRASE_INVALID", http.StatusUnprocessableEntity},
		{"DECRYPTION_FAILED", http.StatusUnprocessableEntity},
		{"UNSUPPORTED_ENCRYPTION", http.StatusUnprocessableEntity},
		{"UNSUPPORTED_ARCHIVE", http.StatusUnprocessableEntity},
		{"SECRET_ENCRYPTION_REQUIRED", http.StatusUnprocessableEntity},
		{"ENCRYPTION_FAILED", http.StatusUnprocessableEntity},
		{"UNSUPPORTED_SCHEMA", http.StatusUnprocessableEntity},
	} {
		t.Run(tc.code, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeBackupError(response, rootRequest(http.MethodPost, restorePath), fmt.Errorf("private archive context: %w", &backup.Error{Code: tc.code}))
			assertBackupErrorContract(t, schema, response, tc.status, tc.code)
			if strings.Contains(response.Body.String(), "private archive context") {
				t.Fatal("internal error context was exposed")
			}
		})
	}
	response := httptest.NewRecorder()
	writeBackupError(response, rootRequest(http.MethodPost, backupPath), errors.New("private storage failure"))
	assertBackupErrorContract(t, schema, response, http.StatusServiceUnavailable, "STORE_UNAVAILABLE")
	if strings.Contains(response.Body.String(), "private storage failure") {
		t.Fatal("internal storage error was exposed")
	}
	for _, tc := range []struct {
		code   string
		status int
	}{
		{"INVALID_REQUEST", http.StatusBadRequest},
		{"REQUEST_TOO_LARGE", http.StatusRequestEntityTooLarge},
		{"VALIDATION_FAILED", http.StatusUnprocessableEntity},
		{"PRECONDITION_REQUIRED", http.StatusPreconditionRequired},
		{"PRECONDITION_FAILED", http.StatusPreconditionFailed},
		{"STORE_UNAVAILABLE", http.StatusServiceUnavailable},
	} {
		t.Run("configuration/"+tc.code, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeConfigurationError(response, rootRequest(http.MethodPost, restorePath), &config.Fault{Code: tc.code})
			assertBackupErrorContract(t, schema, response, tc.status, tc.code)
		})
	}
}

func TestBackupRequestErrorContract(t *testing.T) {
	schema := backupErrorSchema(t)
	for _, path := range []string{backupPath, restorePreviewPath, restorePath} {
		t.Run(path+"/content-type", func(t *testing.T) {
			response := httptest.NewRecorder()
			(&backupHandler{}).ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}")))
			assertBackupErrorContract(t, schema, response, http.StatusUnsupportedMediaType, "UNSUPPORTED_CONTENT")
		})
	}
	source := backup.Source{AndaruVersion: "test", SavedVersion: "0", CapturedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	archive, err := backup.Encode(context.Background(), source, backup.Documents{Configuration: json.RawMessage(`{"schemaVersion":1,"links":[],"segments":[],"zones":[]}`)}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, options, code string
		archive                   []byte
		status                    int
		busy                      bool
	}{
		{"busy-create", backupPath, `{"includeSecrets":false}`, "BACKUP_BUSY", nil, http.StatusConflict, true},
		{"busy-preview", restorePreviewPath, `{}`, "BACKUP_BUSY", archive, http.StatusConflict, true},
		{"busy-restore", restorePath, `{"stores":["configuration"]}`, "BACKUP_BUSY", archive, http.StatusConflict, true},
		{"invalid-store", restorePath, `{"stores":["unknown"]}`, "INVALID_RESTORE", archive, http.StatusUnprocessableEntity, false},
		{"missing-scope", restorePreviewPath, `{"stores":["configurationSecrets"]}`, "UNSUPPORTED_SCOPE", archive, http.StatusUnprocessableEntity, false},
		{"invalid-archive", restorePreviewPath, `{}`, "INVALID_ARCHIVE", []byte("not a ZIP"), http.StatusBadRequest, false},
		{"plaintext-passphrase", restorePreviewPath, `{"passphrase":"private passphrase"}`, "PASSPHRASE_INVALID", archive, http.StatusUnprocessableEntity, false},
		{"unsupported-encryption", restorePreviewPath, `{"passphrase":"private passphrase"}`, "UNSUPPORTED_ENCRYPTION", []byte("age-encryption.org/v99\n"), http.StatusUnprocessableEntity, false},
		{"options-limit", restorePreviewPath, strings.Repeat("x", backupOptionsMax+1), "ARCHIVE_LIMIT", archive, http.StatusRequestEntityTooLarge, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := &backupHandler{}
			if tc.busy {
				handler.expensive.Lock()
				defer handler.expensive.Unlock()
			}
			body := bytes.NewBufferString(tc.options)
			contentType := "application/json"
			if tc.path != backupPath {
				body.Reset()
				writer := multipart.NewWriter(body)
				for _, part := range []struct {
					name string
					data []byte
				}{{"archive", tc.archive}, {"options", []byte(tc.options)}} {
					field, err := writer.CreateFormField(part.name)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := field.Write(part.data); err != nil {
						t.Fatal(err)
					}
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				contentType = writer.FormDataContentType()
			}
			request := httptest.NewRequest(http.MethodPost, tc.path, body)
			request.Header.Set("Content-Type", contentType)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertBackupErrorContract(t, schema, response, tc.status, tc.code)
			if strings.Contains(response.Body.String(), "private passphrase") {
				t.Fatal("passphrase was exposed in the error response")
			}
		})
	}
}
