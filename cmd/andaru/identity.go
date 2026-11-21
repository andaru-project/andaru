package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const identityResponseLimit = 1 << 20

var identityEUID = os.Geteuid

type identityRequestSpec struct {
	method, path, kind, ifMatch, warning string
	payload                              []byte
	mutation, created                   bool
}

type identityUser struct {
	ID          string   `json:"id"`
	Login       string   `json:"login"`
	DisplayName string   `json:"displayName"`
	Kind        string   `json:"kind"`
	Enabled     bool     `json:"enabled"`
	RoleIDs     []string `json:"roleIds"`
	Revision    int64    `json:"revision"`
	CreatedAt   int64    `json:"createdAt"`
	UpdatedAt   int64    `json:"updatedAt"`
}

type identityToken struct {
	Selector               string     `json:"selector"`
	AccountID              string     `json:"accountId"`
	Name                   string     `json:"name"`
	Scope                  []string   `json:"scope"`
	CreatedAt              time.Time  `json:"createdAt"`
	ExpiresAt              *time.Time `json:"expiresAt,omitempty"`
	NoExpiry               bool       `json:"noExpiry"`
	RevokedAt              *time.Time `json:"revokedAt,omitempty"`
	LastUsedAt             *time.Time `json:"lastUsedAt,omitempty"`
	RevokeOnPasswordChange bool       `json:"revokeOnPasswordChange"`
	Revision               uint64     `json:"revision"`
}

type operationMetadata struct {
	ReasonCode        string `json:"reasonCode,omitempty"`
	Changed           string `json:"changed,omitempty"`
	ScopeCount        string `json:"scopeCount,omitempty"`
	SecretUnavailable string `json:"secretUnavailable,omitempty"`
}

type identityOperation struct {
	OperationID  string            `json:"operationId"`
	Result       string            `json:"result"`
	Target       string            `json:"target"`
	Metadata     *operationMetadata `json:"metadata"`
	AuditPending bool              `json:"auditPending,omitempty"`
}

type identityTokenIssue struct {
	identityOperation
	Selector          string         `json:"selector"`
	Secret            string         `json:"secret,omitempty"`
	SecretUnavailable bool           `json:"secretUnavailable"`
	Token             *identityToken `json:"token,omitempty"`
}

type identityUserPage struct {
	Items      []identityUser `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

type identityTokenPage struct {
	Items      []identityToken `json:"items"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

type identityAuditMetadata struct {
	ReasonCode         string `json:"reasonCode,omitempty"`
	Changed            string `json:"changed,omitempty"`
	ScopeCount         string `json:"scopeCount,omitempty"`
	SecretUnavailable  string `json:"secretUnavailable,omitempty"`
	Category           string `json:"category,omitempty"`
	ExpiredGeneral     string `json:"expiredGeneral,omitempty"`
	ExpiredProtected   string `json:"expiredProtected,omitempty"`
	CountPrunedGeneral string `json:"countPrunedGeneral,omitempty"`
	Watermark          string `json:"watermark,omitempty"`
}

type identityAuditEvent struct {
	ID uint64    `json:"id"`
	At time.Time `json:"at"`
	Actor struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"actor"`
	Source      string                `json:"source"`
	Action      string                `json:"action"`
	Target      string                `json:"target"`
	Result      string                `json:"result"`
	OperationID string                `json:"operationId,omitempty"`
	Category    string                `json:"category"`
	Count       uint64                `json:"count"`
	Metadata    identityAuditMetadata `json:"metadata"`
}

type identityAuditPage struct {
	Items      []identityAuditEvent `json:"items"`
	NextCursor string               `json:"nextCursor,omitempty"`
}

func runIdentity(ctx context.Context, socket string, args []string, stdout, stderr io.Writer) int {
	spec, err := parseIdentityCommand(args)
	if err != nil {
		return ctlUsageError(stderr, args[0])
	}
	if identityEUID() != 0 {
		fmt.Fprintln(stderr, "identity administration requires root")
		return 1
	}
	if spec.kind == "bootstrap" {
		password, promptErr := readNewIdentityPassword(ctx, "New administrator password: ", "Repeat new administrator password: ")
		if promptErr != nil {
			fmt.Fprintln(stderr, "administrator bootstrap requires two matching passwords from a controlling terminal")
			return 1
		}
		confirmation, confirmErr := readIdentityTerminalValue(ctx, "Type BOOTSTRAP to create the first administrator: ", 32)
		if confirmErr != nil || confirmation != "BOOTSTRAP" {
			fmt.Fprintln(stderr, "administrator bootstrap cancelled; type BOOTSTRAP exactly on a controlling terminal")
			return 1
		}
		var body map[string]any
		if json.Unmarshal(spec.payload, &body) != nil {
			return 1
		}
		body["password"] = password
		spec.payload, err = json.Marshal(body)
		defer clear(spec.payload)
		if err != nil {
			fmt.Fprintln(stderr, "could not construct administrator bootstrap request")
			return 1
		}
		spec.kind = "operation"
	}
	if spec.kind == "password" {
		password, promptErr := readNewIdentityPassword(ctx, "New password: ", "Repeat new password: ")
		if promptErr != nil {
			fmt.Fprintln(stderr, "password reset requires two matching passwords from a controlling terminal")
			return 1
		}
		var body map[string]any
		if json.Unmarshal(spec.payload, &body) != nil {
			return 1
		}
		body["newPassword"] = password
		spec.payload, err = json.Marshal(body)
		defer clear(spec.payload)
		if err != nil {
			fmt.Fprintln(stderr, "could not construct password reset request")
			return 1
		}
		spec.kind = "operation"
	}
	code := executeIdentityRequest(ctx, socket, spec, stdout, stderr)
	if code == 0 && spec.warning != "" {
		fmt.Fprintln(stderr, spec.warning)
	}
	return code
}

func parseIdentityCommand(args []string) (identityRequestSpec, error) {
	if len(args) < 2 {
		return identityRequestSpec{}, errors.New("missing command")
	}
	switch args[0] {
	case "users":
		return parseUsersCommand(args[1:])
	case "tokens":
		return parseTokensCommand(args[1:])
	case "audit":
		if args[1] != "list" {
			return identityRequestSpec{}, errors.New("unknown audit command")
		}
		path, err := parseIdentityPage("audit list", "/api/v1/audit/events", args[2:], 100, nil)
		return identityRequestSpec{method: http.MethodGet, path: path, kind: "audit"}, err
	default:
		return identityRequestSpec{}, errors.New("unknown identity command")
	}
}

func parseUsersCommand(args []string) (identityRequestSpec, error) {
	if len(args) == 0 {
		return identityRequestSpec{}, errors.New("missing users command")
	}
	switch args[0] {
	case "list":
		path, err := parseIdentityPage("users list", "/api/v1/users", args[1:], 100, nil)
		return identityRequestSpec{method: http.MethodGet, path: path, kind: "users"}, err
	case "get":
		if len(args) != 2 || !validIdentityAccountID(args[1]) {
			return identityRequestSpec{}, errors.New("invalid user ID")
		}
		return identityRequestSpec{method: http.MethodGet, path: "/api/v1/users/" + url.PathEscape(args[1]), kind: "user"}, nil
	case "bootstrap-admin":
		fs, present, err := identityFlags("users bootstrap-admin", args[1:])
		if err != nil {
			return identityRequestSpec{}, err
		}
		login := fs.String("login", "", "")
		displayName := fs.String("display-name", "", "")
		if err = fs.Parse(args[1:]); err != nil || fs.NArg() != 0 || !present["login"] || !boundedIdentityText(*login, 64) || !boundedIdentityText(*displayName, 512) {
			return identityRequestSpec{}, errors.New("invalid bootstrap options")
		}
		payload, _ := json.Marshal(map[string]any{"login": *login, "displayName": *displayName})
		return identityRequestSpec{method: http.MethodPost, path: "/api/v1/users/bootstrap-admin", kind: "bootstrap", payload: payload, mutation: true, created: true}, nil
	case "create":
		return parseUserCreate(args[1:])
	case "update":
		return parseUserUpdate(args[1:])
	case "disable", "delete", "password-reset":
		return parseUserCASCommand(args[0], args[1:])
	default:
		return identityRequestSpec{}, errors.New("unknown users command")
	}
}

func parseUserCreate(args []string) (identityRequestSpec, error) {
	fs, present, err := identityFlags("users create", args)
	if err != nil {
		return identityRequestSpec{}, err
	}
	login := fs.String("login", "", "")
	displayName := fs.String("display-name", "", "")
	kind := fs.String("kind", "", "")
	roles := fs.String("roles", "", "")
	revision := fs.Int64("revision", -1, "")
	disabled := fs.Bool("disabled", false, "")
	if err = fs.Parse(args); err != nil || fs.NArg() != 0 || !present["login"] || !present["kind"] || !present["revision"] || *revision < 0 || !boundedIdentityText(*login, 64) || !boundedIdentityText(*displayName, 512) || (*kind != "human" && *kind != "service") {
		return identityRequestSpec{}, errors.New("invalid create options")
	}
	roleIDs, err := parseIdentityCSV(*roles, 10_002, 64)
	if err != nil {
		return identityRequestSpec{}, err
	}
	body := map[string]any{"login": *login, "displayName": *displayName, "kind": *kind, "enabled": !*disabled, "roleIds": roleIDs, "expectedRevision": *revision}
	payload, _ := json.Marshal(body)
	return identityRequestSpec{method: http.MethodPost, path: "/api/v1/users", kind: "operation", payload: payload, mutation: true, created: true}, nil
}

func parseUserUpdate(args []string) (identityRequestSpec, error) {
	if len(args) == 0 || !validIdentityAccountID(args[0]) {
		return identityRequestSpec{}, errors.New("invalid user ID")
	}
	fs, present, err := identityFlags("users update", args[1:])
	if err != nil {
		return identityRequestSpec{}, err
	}
	login := fs.String("login", "", "")
	displayName := fs.String("display-name", "", "")
	kind := fs.String("kind", "", "")
	roles := fs.String("roles", "", "")
	enabled := fs.String("enabled", "", "")
	revision := fs.Int64("revision", -1, "")
	if err = fs.Parse(args[1:]); err != nil || fs.NArg() != 0 || !present["revision"] || *revision < 0 {
		return identityRequestSpec{}, errors.New("invalid update options")
	}
	body := map[string]any{"expectedRevision": *revision}
	if present["login"] {
		if !boundedIdentityText(*login, 64) {
			return identityRequestSpec{}, errors.New("invalid login")
		}
		body["login"] = *login
	}
	if present["display-name"] {
		if !boundedIdentityText(*displayName, 512) {
			return identityRequestSpec{}, errors.New("invalid display name")
		}
		body["displayName"] = *displayName
	}
	if present["kind"] {
		if *kind != "human" && *kind != "service" {
			return identityRequestSpec{}, errors.New("invalid kind")
		}
		body["kind"] = *kind
	}
	if present["roles"] {
		roleIDs, parseErr := parseIdentityCSV(*roles, 10_002, 64)
		if parseErr != nil {
			return identityRequestSpec{}, parseErr
		}
		body["roleIds"] = roleIDs
	}
	if present["enabled"] {
		value, parseErr := strconv.ParseBool(*enabled)
		if parseErr != nil || strconv.FormatBool(value) != *enabled {
			return identityRequestSpec{}, errors.New("invalid enabled value")
		}
		body["enabled"] = value
	}
	if len(body) == 1 {
		return identityRequestSpec{}, errors.New("empty update")
	}
	payload, _ := json.Marshal(body)
	return identityRequestSpec{method: http.MethodPatch, path: "/api/v1/users/" + url.PathEscape(args[0]), kind: "operation", payload: payload, mutation: true}, nil
}

func parseUserCASCommand(verb string, args []string) (identityRequestSpec, error) {
	if len(args) == 0 || !validIdentityAccountID(args[0]) {
		return identityRequestSpec{}, errors.New("invalid user ID")
	}
	fs, _, err := identityFlags("users "+verb, args[1:])
	if err != nil {
		return identityRequestSpec{}, err
	}
	revision := fs.Int64("revision", -1, "")
	if err = fs.Parse(args[1:]); err != nil || fs.NArg() != 0 || *revision < 0 {
		return identityRequestSpec{}, errors.New("invalid revision")
	}
	path := "/api/v1/users/" + url.PathEscape(args[0])
	spec := identityRequestSpec{path: path, kind: "operation", mutation: true}
	switch verb {
	case "disable":
		spec.method = http.MethodPatch
		spec.payload, _ = json.Marshal(map[string]any{"enabled": false, "expectedRevision": *revision})
	case "delete":
		spec.method, spec.ifMatch = http.MethodDelete, strconv.FormatInt(*revision, 10)
	case "password-reset":
		spec.method, spec.kind = http.MethodPut, "password"
		spec.path += "/password"
		spec.payload, _ = json.Marshal(map[string]any{"expectedRevision": *revision})
	}
	return spec, nil
}

func parseTokensCommand(args []string) (identityRequestSpec, error) {
	if len(args) == 0 {
		return identityRequestSpec{}, errors.New("missing tokens command")
	}
	switch args[0] {
	case "list":
		fs, present, err := identityFlags("tokens list", args[1:])
		if err != nil {
			return identityRequestSpec{}, err
		}
		accountID := fs.String("account-id", "", "")
		cursor := fs.String("cursor", "", "")
		limit := fs.Int("limit", 50, "")
		if err = fs.Parse(args[1:]); err != nil || fs.NArg() != 0 || !present["account-id"] || !validIdentityAccountID(*accountID) || *limit < 1 || *limit > 50 || len(*cursor) > 128 {
			return identityRequestSpec{}, errors.New("invalid token list options")
		}
		query := url.Values{"accountId": {*accountID}, "limit": {strconv.Itoa(*limit)}}
		if *cursor != "" {
			query.Set("cursor", *cursor)
		}
		return identityRequestSpec{method: http.MethodGet, path: "/api/v1/tokens?" + query.Encode(), kind: "tokens"}, nil
	case "create":
		return parseTokenCreate(args[1:])
	case "revoke":
		if len(args) != 2 || !validIdentitySelector(args[1]) {
			return identityRequestSpec{}, errors.New("invalid selector")
		}
		return identityRequestSpec{method: http.MethodDelete, path: "/api/v1/tokens/" + url.PathEscape(args[1]), kind: "operation", mutation: true}, nil
	default:
		return identityRequestSpec{}, errors.New("unknown tokens command")
	}
}

func parseTokenCreate(args []string) (identityRequestSpec, error) {
	fs, present, err := identityFlags("tokens create", args)
	if err != nil {
		return identityRequestSpec{}, err
	}
	accountID := fs.String("account-id", "", "")
	name := fs.String("name", "", "")
	scope := fs.String("scope", "", "")
	expiresAt := fs.String("expires-at", "", "")
	noExpiry := fs.Bool("no-expiry", false, "")
	revoke := fs.String("revoke-on-password-change", "", "")
	if err = fs.Parse(args); err != nil || fs.NArg() != 0 || !present["account-id"] || !present["name"] || !present["scope"] || !validIdentityAccountID(*accountID) || !boundedIdentityText(*name, 512) || present["expires-at"] && *noExpiry {
		return identityRequestSpec{}, errors.New("invalid token options")
	}
	scopes, err := parseIdentityCSV(*scope, 64, 64)
	if err != nil || len(scopes) == 0 {
		return identityRequestSpec{}, errors.New("invalid token scope")
	}
	body := map[string]any{"accountId": *accountID, "name": *name, "scope": scopes}
	if present["expires-at"] {
		expires, parseErr := time.Parse(time.RFC3339, *expiresAt)
		if parseErr != nil {
			return identityRequestSpec{}, parseErr
		}
		body["expiresAt"] = expires
	}
	if *noExpiry {
		body["noExpiry"] = true
	}
	if present["revoke-on-password-change"] {
		value, parseErr := strconv.ParseBool(*revoke)
		if parseErr != nil || strconv.FormatBool(value) != *revoke {
			return identityRequestSpec{}, errors.New("invalid revocation option")
		}
		body["revokeOnPasswordChange"] = value
	}
	payload, _ := json.Marshal(body)
	spec := identityRequestSpec{method: http.MethodPost, path: "/api/v1/tokens", kind: "token-issue", payload: payload, mutation: true, created: true}
	if *noExpiry {
		spec.warning = "warning: this token does not expire; revoke it explicitly when it is no longer needed"
	}
	return spec, nil
}

func parseIdentityPage(name, path string, args []string, maximum int, additional func(*flag.FlagSet, map[string]bool)) (string, error) {
	fs, present, err := identityFlags(name, args)
	if err != nil {
		return "", err
	}
	cursor := fs.String("cursor", "", "")
	limit := fs.Int("limit", min(50, maximum), "")
	if additional != nil {
		additional(fs, present)
	}
	if err = fs.Parse(args); err != nil || fs.NArg() != 0 || *limit < 1 || *limit > maximum || len(*cursor) > 128 {
		return "", errors.New("invalid page options")
	}
	query := url.Values{"limit": {strconv.Itoa(*limit)}}
	if *cursor != "" {
		query.Set("cursor", *cursor)
	}
	return path + "?" + query.Encode(), nil
}

func identityFlags(name string, args []string) (*flag.FlagSet, map[string]bool, error) {
	present := make(map[string]bool)
	for _, arg := range args {
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		key := strings.TrimPrefix(strings.SplitN(arg, "=", 2)[0], "--")
		if key == "" || present[key] {
			return nil, nil, errors.New("empty or repeated option")
		}
		present[key] = true
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs, present, nil
}

func parseIdentityCSV(raw string, maximum, itemBytes int) ([]string, error) {
	if raw == "" {
		return []string{}, nil
	}
	if len(raw) > 16<<10 {
		return nil, errors.New("list exceeds its size limit")
	}
	items := strings.Split(raw, ",")
	if len(items) > maximum {
		return nil, errors.New("list exceeds its item limit")
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item == "" || len(item) > itemBytes || !utf8.ValidString(item) {
			return nil, errors.New("invalid list item")
		}
		if _, ok := seen[item]; ok {
			return nil, errors.New("duplicate list item")
		}
		seen[item] = struct{}{}
	}
	return items, nil
}

func boundedIdentityText(value string, maximumBytes int) bool {
	return len(value) <= maximumBytes && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validIdentityAccountID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.Version() == 4 && parsed.String() == id
}

func validIdentitySelector(selector string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(selector)
	return err == nil && len(decoded) == 16 && base64.RawURLEncoding.EncodeToString(decoded) == selector
}

func readIdentityTerminalValue(ctx context.Context, prompt string, maximum int) (string, error) {
	value, err := readBackupPassphrase(ctx, prompt)
	if err != nil || len(value) > maximum || !boundedIdentityText(value, maximum) {
		return "", errors.New("invalid terminal input")
	}
	return value, nil
}

func readNewIdentityPassword(ctx context.Context, firstPrompt, secondPrompt string) (string, error) {
	first, err := readIdentityTerminalValue(ctx, firstPrompt, 512)
	if err != nil {
		return "", err
	}
	second, err := readIdentityTerminalValue(ctx, secondPrompt, 512)
	if err != nil || first != second {
		return "", errors.New("passwords do not match")
	}
	return first, nil
}

func executeIdentityRequest(ctx context.Context, socket string, spec identityRequestSpec, stdout, stderr io.Writer) int {
	client, transport := unixHTTPClient(socket, 5*time.Second)
	defer transport.CloseIdleConnections()
	if spec.mutation {
		client.Timeout = 15 * time.Second
	}
	request, err := http.NewRequestWithContext(ctx, spec.method, "http://unix"+spec.path, bytes.NewReader(spec.payload))
	if err != nil {
		fmt.Fprintln(stderr, "could not construct local identity request")
		return 1
	}
	if spec.payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if spec.ifMatch != "" {
		request.Header.Set("If-Match", `"`+spec.ifMatch+`"`)
	}
	operationID := ""
	if spec.mutation {
		operationID, err = newIdentityOperationID()
		if err != nil {
			fmt.Fprintln(stderr, "could not create a local identity operation ID")
			return 1
		}
		request.Header.Set("Idempotency-Key", operationID)
	}
	response, err := client.Do(request)
	if err != nil {
		if spec.mutation {
			writeIdentityUnknown(stderr, spec, operationID)
		} else {
			fmt.Fprintln(stderr, "could not reach the local engine")
		}
		return 1
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, identityResponseLimit+1))
	if err != nil || len(body) > identityResponseLimit {
		if spec.mutation {
			writeIdentityUnknown(stderr, spec, operationID)
		} else {
			fmt.Fprintln(stderr, "engine identity response exceeded its read/size budget")
		}
		return 1
	}
	if response.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		if spec.mutation {
			writeIdentityUnknown(stderr, spec, operationID)
		} else {
			fmt.Fprintf(stderr, "engine returned HTTP %d with an invalid response type\n", response.StatusCode)
		}
		return 1
	}
	if !identitySuccessStatus(spec, response.StatusCode) {
		if validAPIError(body) {
			fmt.Fprintln(stderr, string(body))
		} else if spec.mutation {
			writeIdentityUnknown(stderr, spec, operationID)
		} else {
			fmt.Fprintf(stderr, "engine returned HTTP %d\n", response.StatusCode)
		}
		return 1
	}
	if !validIdentityResponse(spec.kind, body) {
		if spec.mutation {
			writeIdentityUnknown(stderr, spec, operationID)
		} else {
			fmt.Fprintln(stderr, "engine returned an invalid "+identityResponseName(spec.kind)+" response")
		}
		return 1
	}
	if _, err = stdout.Write(body); err != nil {
		if spec.mutation {
			writeIdentityCommittedOutputFailure(stderr, spec, operationID)
		} else {
			fmt.Fprintln(stderr, "could not write engine response")
		}
		return 1
	}
	if len(body) == 0 || body[len(body)-1] != '\n' {
		if _, err = io.WriteString(stdout, "\n"); err != nil {
			if spec.mutation {
				writeIdentityCommittedOutputFailure(stderr, spec, operationID)
			} else {
				fmt.Fprintln(stderr, "could not finish engine response")
			}
			return 1
		}
	}
	return 0
}

func writeIdentityUnknown(stderr io.Writer, spec identityRequestSpec, operationID string) {
	if spec.kind == "token-issue" {
		fmt.Fprintf(stderr, "token operation %s response unavailable; a token may exist but its secret is unrecoverable; inspect tokens and revoke or recreate before retrying\n", operationID)
		return
	}
	fmt.Fprintf(stderr, "identity operation %s response unavailable; outcome may be unknown; inspect current identity state before retrying\n", operationID)
}

func writeIdentityCommittedOutputFailure(stderr io.Writer, spec identityRequestSpec, operationID string) {
	if spec.kind == "token-issue" {
		fmt.Fprintf(stderr, "token operation %s committed but its secret could not be written completely; revoke and recreate the token\n", operationID)
		return
	}
	fmt.Fprintf(stderr, "identity operation %s committed but its response could not be written; inspect current identity state\n", operationID)
}

func newIdentityOperationID() (string, error) {
	var random [16]byte
	if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
		return "", err
	}
	return "cli-" + hex.EncodeToString(random[:]), nil
}

func identitySuccessStatus(spec identityRequestSpec, status int) bool {
	if !spec.mutation {
		return status == http.StatusOK
	}
	if status == http.StatusAccepted {
		return true
	}
	if spec.created {
		return status == http.StatusOK || status == http.StatusCreated
	}
	return status == http.StatusOK
}

func identityResponseName(kind string) string {
	return map[string]string{"users": "users", "user": "user", "tokens": "tokens", "audit": "audit", "operation": "operation", "token-issue": "token issue"}[kind]
}

func validIdentityResponse(kind string, raw []byte) bool {
	switch kind {
	case "users":
		var page identityUserPage
		if decodeStrict(raw, &page) != nil || page.Items == nil || len(page.Items) > 100 || len(page.NextCursor) > 128 {
			return false
		}
		for _, user := range page.Items {
			if !validIdentityUser(user) {
				return false
			}
		}
		return true
	case "user":
		var user identityUser
		return decodeStrict(raw, &user) == nil && validIdentityUser(user)
	case "tokens":
		var page identityTokenPage
		if decodeStrict(raw, &page) != nil || page.Items == nil || len(page.Items) > 50 || len(page.NextCursor) > 128 {
			return false
		}
		for _, token := range page.Items {
			if !validIdentityToken(token) {
				return false
			}
		}
		return true
	case "audit":
		var page identityAuditPage
		return decodeStrict(raw, &page) == nil && page.Items != nil && len(page.Items) <= 100 && len(page.NextCursor) <= 128
	case "operation":
		var operation identityOperation
		return decodeStrict(raw, &operation) == nil && validIdentityOperation(operation)
	case "token-issue":
		var issue identityTokenIssue
		if decodeStrict(raw, &issue) != nil || !validIdentityOperation(issue.identityOperation) || !validIdentitySelector(issue.Selector) || issue.Target != issue.Selector {
			return false
		}
		if issue.SecretUnavailable {
			return issue.Secret == ""
		}
		secret, err := base64.RawURLEncoding.DecodeString(issue.Secret)
		return err == nil && len(secret) == 32 && base64.RawURLEncoding.EncodeToString(secret) == issue.Secret
	default:
		return false
	}
}

func validIdentityUser(user identityUser) bool {
	return validIdentityAccountID(user.ID) && boundedIdentityText(user.Login, 64) && boundedIdentityText(user.DisplayName, 512) && (user.Kind == "human" || user.Kind == "service") && user.RoleIDs != nil && len(user.RoleIDs) <= 10_002 && user.Revision >= 0 && user.CreatedAt >= 0 && user.UpdatedAt >= 0
}

func validIdentityToken(token identityToken) bool {
	return validIdentitySelector(token.Selector) && validIdentityAccountID(token.AccountID) && boundedIdentityText(token.Name, 512) && token.Scope != nil
}

func validIdentityOperation(operation identityOperation) bool {
	return operation.OperationID != "" && len(operation.OperationID) <= 128 && operation.Result == "success" && operation.Target != "" && operation.Metadata != nil
}
