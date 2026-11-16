package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/identity"
)

const identityBodyLimit = 16 << 10

var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:@%/-]{1,128}$`)

type identityAPI struct {
	identities identity.Store
	configuration *config.Store
	coordinator *securityCoordinator
}

type operationResponse struct {
	OperationID string            `json:"operationId"`
	Result      string            `json:"result"`
	Target      string            `json:"target"`
	Metadata    map[string]string `json:"metadata"`
	AuditPending bool             `json:"auditPending,omitempty"`
}

type tokenIssueResponse struct {
	operationResponse
	Selector          string    `json:"selector"`
	Secret            string    `json:"secret,omitempty"`
	SecretUnavailable bool      `json:"secretUnavailable"`
	Token             *tokenView `json:"token,omitempty"`
}

type identityPage[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

type userView struct {
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

type roleView struct {
	ID           string               `json:"id"`
	Name         string               `json:"name"`
	Description  string               `json:"description,omitempty"`
	Capabilities []authz.Capability   `json:"capabilities"`
	BuiltIn      string               `json:"builtIn,omitempty"`
}

type tokenView struct {
	Selector               string              `json:"selector"`
	AccountID              string              `json:"accountId"`
	Name                   string              `json:"name"`
	Scope                  []authz.Capability  `json:"scope"`
	CreatedAt              time.Time           `json:"createdAt"`
	ExpiresAt              *time.Time          `json:"expiresAt,omitempty"`
	NoExpiry               bool                `json:"noExpiry"`
	RevokedAt              *time.Time          `json:"revokedAt,omitempty"`
	LastUsedAt             *time.Time          `json:"lastUsedAt,omitempty"`
	RevokeOnPasswordChange bool                `json:"revokeOnPasswordChange"`
	Revision               uint64              `json:"revision"`
}

type createUserRequest struct {
	Login            string                `json:"login"`
	DisplayName      string                `json:"displayName"`
	Kind             identity.AccountKind  `json:"kind"`
	Enabled          *bool                 `json:"enabled"`
	RoleIDs          []string              `json:"roleIds"`
	ExpectedRevision *int64                `json:"expectedRevision"`
}

type updateUserRequest struct {
	Login            *string               `json:"login"`
	DisplayName      *string               `json:"displayName"`
	Kind             *identity.AccountKind `json:"kind"`
	Enabled          *bool                 `json:"enabled"`
	RoleIDs          *[]string             `json:"roleIds"`
	ExpectedRevision *int64                `json:"expectedRevision"`
}

type passwordRequest struct {
	CurrentPassword  string `json:"currentPassword"`
	NewPassword      string `json:"newPassword"`
	ExpectedRevision *int64 `json:"expectedRevision"`
}

type tokenRequest struct {
	AccountID              string              `json:"accountId"`
	Name                   string              `json:"name"`
	Scope                  *[]authz.Capability `json:"scope"`
	ExpiresAt              *time.Time          `json:"expiresAt"`
	NoExpiry               bool                `json:"noExpiry"`
	RevokeOnPasswordChange *bool               `json:"revokeOnPasswordChange"`
}

func newIdentityAPI(identities identity.Store, configuration *config.Store, coordinator *securityCoordinator) http.Handler {
	return &identityAPI{identities: identities, configuration: configuration, coordinator: coordinator}
}

func (h *identityAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/v1/capabilities" {
		h.capabilities(w, r)
		return
	}
	if r.URL.Path == "/api/v1/roles" {
		h.roles(w, r)
		return
	}
	if r.URL.Path == "/api/v1/tokens" {
		h.tokens(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/users") {
		h.users(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/tokens/") {
		h.revokeToken(w, r)
		return
	}
	writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
}

func (h *identityAPI) capabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, r, "GET, HEAD")
		return
	}
	if !requireNoBody(w, r) {
		return
	}
	cursor, limit, _, ok := parsePageQuery(w, r, 100)
	if !ok {
		return
	}
	capabilities := authz.Catalog()
	start := 0
	if cursor != "" {
		index := slices.Index(capabilities, authz.Capability(cursor))
		if index < 0 {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid pagination cursor.")
			return
		}
		start = index + 1
	}
	end := min(start+limit, len(capabilities))
	items := slices.Clone(capabilities[start:end])
	page := identityPage[authz.Capability]{Items: items}
	if end < len(capabilities) && len(items) > 0 {
		page.NextCursor = string(items[len(items)-1])
	}
	writeJSON(w, r, http.StatusOK, page)
}

func (h *identityAPI) roles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, r, "GET, HEAD")
		return
	}
	if !requireNoBody(w, r) {
		return
	}
	cursor, limit, _, ok := parsePageQuery(w, r, 100)
	if !ok {
		return
	}
	roles, err := h.listRoles(r.Context())
	if err != nil {
		writeIdentityError(w, r, err)
		return
	}
	start := sortAfterRole(roles, cursor)
	if cursor != "" && start == 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid pagination cursor.")
		return
	}
	end := min(start+limit, len(roles))
	items := make([]roleView, 0, end-start)
	for _, role := range roles[start:end] {
		items = append(items, roleResponse(role))
	}
	page := identityPage[roleView]{Items: items}
	if end < len(roles) && len(items) > 0 {
		page.NextCursor = items[len(items)-1].ID
	}
	writeJSON(w, r, http.StatusOK, page)
}

func (h *identityAPI) users(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/users")
	if rest == "" {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			h.listUsers(w, r)
		case http.MethodPost:
			h.createUser(w, r)
		default:
			methodNotAllowed(w, r, "GET, HEAD, POST")
		}
		return
	}
	if !strings.HasPrefix(rest, "/") {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")
	if len(parts) == 1 && parts[0] != "" {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			h.getUser(w, r, parts[0])
		case http.MethodPatch:
			h.updateUser(w, r, parts[0])
		case http.MethodDelete:
			h.deleteUser(w, r, parts[0])
		default:
			methodNotAllowed(w, r, "GET, HEAD, PATCH, DELETE")
		}
		return
	}
	if len(parts) == 2 && parts[0] != "" && parts[1] == "password" {
		if r.Method != http.MethodPut {
			methodNotAllowed(w, r, "PUT")
			return
		}
		h.changePassword(w, r, parts[0])
		return
	}
	writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
}

func (h *identityAPI) listUsers(w http.ResponseWriter, r *http.Request) {
	if !requireNoBody(w, r) {
		return
	}
	cursor, limit, _, ok := parsePageQuery(w, r, identity.MaxAccountPageSize)
	if !ok {
		return
	}
	var page identity.AccountPage
	var revision int64
	stable := false
	for range 3 {
		before, err := h.identities.Revision(r.Context())
		if err != nil {
			writeIdentityError(w, r, err)
			return
		}
		page, err = h.identities.ListAccounts(r.Context(), cursor, limit)
		if err != nil {
			writeIdentityError(w, r, err)
			return
		}
		revision, err = h.identities.Revision(r.Context())
		if err != nil {
			writeIdentityError(w, r, err)
			return
		}
		if before == revision {
			stable = true
			break
		}
	}
	if !stable {
		writeError(w, r, http.StatusServiceUnavailable, "STORE_BUSY", "The identity store changed during the read; retry the request.")
		return
	}
	items := make([]userView, 0, len(page.Items))
	for _, account := range page.Items {
		items = append(items, viewUser(account, revision))
	}
	writeJSON(w, r, http.StatusOK, identityPage[userView]{Items: items, NextCursor: page.NextCursor})
}

func (h *identityAPI) getUser(w http.ResponseWriter, r *http.Request, id string) {
	if !noQuery(w, r) {
		return
	}
	if !requireNoBody(w, r) {
		return
	}
	var account identity.Account
	var revision int64
	stable := false
	for range 3 {
		before, err := h.identities.Revision(r.Context())
		if err != nil {
			writeIdentityError(w, r, err)
			return
		}
		account, err = h.identities.GetAccount(r.Context(), id)
		if err != nil {
			writeIdentityError(w, r, err)
			return
		}
		revision, err = h.identities.Revision(r.Context())
		if err != nil {
			writeIdentityError(w, r, err)
			return
		}
		if before == revision {
			stable = true
			break
		}
	}
	if !stable {
		writeError(w, r, http.StatusServiceUnavailable, "STORE_BUSY", "The identity store changed during the read; retry the request.")
		return
	}
	writeJSON(w, r, http.StatusOK, viewUser(account, revision))
}

func (h *identityAPI) createUser(w http.ResponseWriter, r *http.Request) {
	raw, request, ok := decodeIdentityBody[createUserRequest](w, r, identityBodyLimit, "login", "displayName", "kind", "enabled", "roleIds", "expectedRevision")
	if !ok {
		return
	}
	if !noQuery(w, r) {
		return
	}
	if request.ExpectedRevision == nil || *request.ExpectedRevision < 0 || request.Enabled == nil || request.Login == "" || request.Kind != identity.Human && request.Kind != identity.Service {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid user creation request.")
		return
	}
	if err := h.validateRoleAssignments(r.Context(), request.RoleIDs); err != nil {
		writeIdentityError(w, r, err)
		return
	}
	actor, ok := mutationActor(w, r)
	if !ok {
		return
	}
	operationID, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	input := identity.CreateAccountInput{Login: request.Login, DisplayName: request.DisplayName, Kind: request.Kind, Enabled: *request.Enabled, RoleIDs: request.RoleIDs}
	execution, ok := h.executeMutation(w, r, actor, operationID, raw, audit.ActionAccountCreated, audit.CategoryProtected, "", func(ctx context.Context) (string, any, error) {
		account, _, err := h.identities.CreateAccount(ctx, input, *request.ExpectedRevision)
		return account.ID, nil, err
	})
	if ok {
		writeOperationResult(w, r, execution, http.StatusCreated, "")
	}
}

func (h *identityAPI) updateUser(w http.ResponseWriter, r *http.Request, id string) {
	raw, request, ok := decodeIdentityBody[updateUserRequest](w, r, identityBodyLimit, "login", "displayName", "kind", "enabled", "roleIds", "expectedRevision")
	if !ok {
		return
	}
	if !noQuery(w, r) {
		return
	}
	if request.ExpectedRevision == nil || *request.ExpectedRevision < 0 || request.Login == nil && request.DisplayName == nil && request.Kind == nil && request.Enabled == nil && request.RoleIDs == nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid user update request.")
		return
	}
	if request.RoleIDs != nil {
		if err := h.validateRoleAssignments(r.Context(), *request.RoleIDs); err != nil {
			writeIdentityError(w, r, err)
			return
		}
	}
	actor, ok := mutationActor(w, r)
	if !ok {
		return
	}
	operationID, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	action := audit.ActionAccountUpdated
	if request.RoleIDs != nil && request.Login == nil && request.DisplayName == nil && request.Kind == nil && request.Enabled == nil {
		action = audit.ActionRoleChanged
	} else if request.Enabled != nil && !*request.Enabled && request.Login == nil && request.DisplayName == nil && request.Kind == nil && request.RoleIDs == nil {
		action = audit.ActionAccountDisabled
	}
	input := identity.UpdateAccountInput{Login: request.Login, DisplayName: request.DisplayName, Kind: request.Kind, Enabled: request.Enabled, RoleIDs: request.RoleIDs}
	execution, ok := h.executeMutation(w, r, actor, operationID, raw, action, audit.CategoryProtected, id, func(ctx context.Context) (string, any, error) {
		_, _, err := h.identities.UpdateAccount(ctx, id, *request.ExpectedRevision, input)
		return id, nil, err
	})
	if ok {
		writeOperationResult(w, r, execution, http.StatusOK, id)
	}
}

func (h *identityAPI) deleteUser(w http.ResponseWriter, r *http.Request, id string) {
	if !noQuery(w, r) {
		return
	}
	if !requireNoBody(w, r) {
		return
	}
	revision, ok := requiredIfMatch(w, r)
	if !ok {
		return
	}
	actor, ok := mutationActor(w, r)
	if !ok {
		return
	}
	operationID, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	execution, ok := h.executeMutation(w, r, actor, operationID, nil, audit.ActionAccountDeleted, audit.CategoryProtected, id, func(ctx context.Context) (string, any, error) {
		return id, nil, h.identities.DeleteAccount(ctx, id, revision)
	})
	if ok {
		writeOperationResult(w, r, execution, http.StatusOK, id)
	}
}

func (h *identityAPI) changePassword(w http.ResponseWriter, r *http.Request, id string) {
	raw, request, ok := decodeIdentityBody[passwordRequest](w, r, 4096, "currentPassword", "newPassword", "expectedRevision")
	if !ok {
		return
	}
	if !noQuery(w, r) {
		return
	}
	if request.ExpectedRevision == nil || *request.ExpectedRevision < 0 || request.NewPassword == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid password change request.")
		return
	}
	actor, ok := mutationActor(w, r)
	if !ok {
		return
	}
	target, err := h.identities.GetAccount(r.Context(), id)
	if err != nil {
		writeIdentityError(w, r, err)
		return
	}
	if slices.Contains(target.RoleIDs, authz.AdministratorRoleID) && !actor.Root {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "Built-in administrator passwords can only be reset from the root console.")
		return
	}
	if actor.Root {
		if request.CurrentPassword != "" {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Root password resets do not accept a current password.")
			return
		}
	} else {
		if request.CurrentPassword == "" {
			writeError(w, r, http.StatusUnauthorized, "AUTHENTICATION_FAILED", "Password authentication failed.")
			return
		}
		currentPassword := []byte(request.CurrentPassword)
		valid, verifyErr := h.identities.VerifyPassword(r.Context(), actor.AccountID, currentPassword)
		clear(currentPassword)
		if verifyErr != nil {
			writeIdentityError(w, r, verifyErr)
			return
		}
		if !valid {
			writeError(w, r, http.StatusUnauthorized, "AUTHENTICATION_FAILED", "Password authentication failed.")
			return
		}
	}
	operationID, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	execution, ok := h.executeMutation(w, r, actor, operationID, raw, audit.ActionPasswordChanged, audit.CategoryProtected, id, func(ctx context.Context) (string, any, error) {
		password := []byte(request.NewPassword)
		_, err := h.identities.SetPassword(ctx, id, *request.ExpectedRevision, password)
		clear(password)
		return id, nil, err
	})
	if ok {
		writeOperationResult(w, r, execution, http.StatusOK, id)
	}
}

func (h *identityAPI) tokens(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		h.listTokens(w, r)
	case http.MethodPost:
		h.createToken(w, r)
	default:
		methodNotAllowed(w, r, "GET, HEAD, POST")
	}
}

func (h *identityAPI) listTokens(w http.ResponseWriter, r *http.Request) {
	if !requireNoBody(w, r) {
		return
	}
	cursor, limit, values, ok := parsePageQuery(w, r, identity.MaxTokenPageSize, "accountId")
	if !ok {
		return
	}
	accountIDs := values["accountId"]
	if len(accountIDs) != 1 || !validAccountActorID(accountIDs[0]) {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "A valid accountId is required.")
		return
	}
	actor, ok := actorFromRequest(r)
	if !ok {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "A verified actor is required.")
		return
	}
	if !hasCapability(actor, authz.TokensManageAll) && actor.AccountID != accountIDs[0] {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "Tokens may only be listed for the acting account.")
		return
	}
	page, err := h.identities.ListTokensPage(r.Context(), accountIDs[0], cursor, limit)
	if err != nil {
		writeIdentityError(w, r, err)
		return
	}
	items := make([]tokenView, 0, len(page.Items))
	for _, token := range page.Items {
		items = append(items, viewToken(token))
	}
	writeJSON(w, r, http.StatusOK, identityPage[tokenView]{Items: items, NextCursor: page.NextCursor})
}

func (h *identityAPI) createToken(w http.ResponseWriter, r *http.Request) {
	raw, request, ok := decodeIdentityBody[tokenRequest](w, r, identityBodyLimit, "accountId", "name", "scope", "expiresAt", "noExpiry", "revokeOnPasswordChange")
	if !ok {
		return
	}
	if !noQuery(w, r) {
		return
	}
	if !validAccountActorID(request.AccountID) || request.Name == "" || request.Scope == nil || request.NoExpiry && request.ExpiresAt != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid token request.")
		return
	}
	actor, ok := mutationActor(w, r)
	if !ok {
		return
	}
	revision, err := h.identities.Revision(r.Context())
	if err != nil {
		writeIdentityError(w, r, err)
		return
	}
	account, err := h.identities.GetAccount(r.Context(), request.AccountID)
	if err != nil {
		writeIdentityError(w, r, err)
		return
	}
	roles, err := h.listRoles(r.Context())
	if err != nil {
		writeIdentityError(w, r, err)
		return
	}
	customRoles := make([]authz.Role, 0, len(roles))
	for _, role := range roles {
		if role.BuiltIn == "" {
			customRoles = append(customRoles, role)
		}
	}
	currentRevision, err := h.identities.Revision(r.Context())
	if err != nil {
		writeIdentityError(w, r, err)
		return
	}
	if currentRevision != revision {
		writeIdentityError(w, r, identity.ErrConflict)
		return
	}
	issuerCapabilities := capabilitiesFor(actor)
	operationID, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	issue := identity.TokenIssue{
		AccountID: request.AccountID, Name: request.Name, Scope: *request.Scope,
		ExpiresAt: request.ExpiresAt, NoExpiry: request.NoExpiry,
		RevokeOnPasswordChange: request.RevokeOnPasswordChange,
	}
	execution, ok := h.executeMutation(w, r, actor, operationID, raw, audit.ActionTokenCreated, audit.CategoryGeneral, "", func(ctx context.Context) (string, any, error) {
		issued, err := h.identities.IssueTokenAtRevision(ctx, revision, actor.Actor, issuerCapabilities, authz.Resolve(customRoles, account.RoleIDs), issue)
		return issued.Selector, issued, err
	})
	if !ok {
		return
	}
	result := operationResult(execution, "")
	response := tokenIssueResponse{operationResponse: result, SecretUnavailable: execution.Replay || execution.Outcome.Metadata[audit.MetadataSecretUnavailable] == "true"}
	response.Selector = result.Target
	if execution.Replay {
		response.Metadata[audit.MetadataSecretUnavailable] = "true"
		if token, err := h.identities.GetToken(r.Context(), result.Target); err == nil {
			view := viewToken(token)
			response.Token = &view
		}
	} else if issued, ok := execution.Value.(identity.IssuedToken); ok {
		response.Selector = issued.Selector
		response.Secret = issued.Secret
		view := viewToken(issued.Metadata)
		response.Token = &view
	}
	status := http.StatusCreated
	if execution.Replay {
		status = http.StatusOK
	}
	if execution.AuditPending {
		status = http.StatusAccepted
	}
	writeJSON(w, r, status, response)
}

func (h *identityAPI) revokeToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		methodNotAllowed(w, r, "DELETE")
		return
	}
	selector := strings.TrimPrefix(r.URL.Path, "/api/v1/tokens/")
	if selector == "" || strings.Contains(selector, "/") {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return
	}
	if !noQuery(w, r) {
		return
	}
	if !requireNoBody(w, r) {
		return
	}
	actor, ok := mutationActor(w, r)
	if !ok {
		return
	}
	token, err := h.identities.GetToken(r.Context(), selector)
	if err != nil {
		writeIdentityError(w, r, err)
		return
	}
	if !hasCapability(actor, authz.TokensManageAll) && actor.AccountID != token.AccountID {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "Tokens may only be revoked by their owner.")
		return
	}
	operationID, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	execution, ok := h.executeMutation(w, r, actor, operationID, nil, audit.ActionTokenRevoked, audit.CategoryProtected, selector, func(ctx context.Context) (string, any, error) {
		return selector, nil, h.identities.RevokeToken(ctx, selector)
	})
	if ok {
		writeOperationResult(w, r, execution, http.StatusOK, selector)
	}
}

func (h *identityAPI) executeMutation(w http.ResponseWriter, r *http.Request, actor requestActor, operationID string, body []byte, action audit.Action, category audit.Category, target string, mutate func(context.Context) (string, any, error)) (operationExecution, bool) {
	fingerprint := make([]byte, 0, len(r.Method)+len(r.URL.Path)+len(r.Header.Get("If-Match"))+len(body)+3)
	fingerprint = append(fingerprint, r.Method...)
	fingerprint = append(fingerprint, '\n')
	fingerprint = append(fingerprint, r.URL.Path...)
	fingerprint = append(fingerprint, '\n')
	fingerprint = append(fingerprint, r.Header.Get("If-Match")...)
	fingerprint = append(fingerprint, '\n')
	fingerprint = append(fingerprint, body...)
	execution, err := h.coordinator.execute(r.Context(), actor, operationID, fingerprint, action, category, target, mutate)
	if err != nil {
		writeSecurityError(w, r, err)
		return operationExecution{}, false
	}
	if execution.MutationErr != nil {
		writeIdentityError(w, r, execution.MutationErr)
		return operationExecution{}, false
	}
	return execution, true
}

func writeOperationResult(w http.ResponseWriter, r *http.Request, execution operationExecution, successStatus int, fallbackTarget string) {
	status := successStatus
	if execution.Replay {
		status = http.StatusOK
	}
	if execution.AuditPending {
		status = http.StatusAccepted
	}
	writeJSON(w, r, status, operationResult(execution, fallbackTarget))
}

func operationResult(execution operationExecution, fallbackTarget string) operationResponse {
	target := execution.Outcome.Target
	if target == "" {
		target = fallbackTarget
	}
	metadata := execution.Outcome.Metadata
	if metadata == nil {
		metadata = map[string]string{}
	}
	return operationResponse{
		OperationID: execution.Outcome.OperationID,
		Result: string(execution.Outcome.Result),
		Target: target,
		Metadata: cloneStringMap(metadata),
		AuditPending: execution.AuditPending,
	}
}

func mutationActor(w http.ResponseWriter, r *http.Request) (requestActor, bool) {
	actor, ok := actorFromRequest(r)
	if !ok {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "A verified actor is required.")
		return requestActor{}, false
	}
	return actor, true
}

func idempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 || !idempotencyKeyPattern.MatchString(values[0]) {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "A valid Idempotency-Key is required.")
		return "", false
	}
	return values[0], true
}

func decodeIdentityBody[T any](w http.ResponseWriter, r *http.Request, limit int, fields ...string) ([]byte, T, bool) {
	var request T
	if !jsonContentType(r.Header.Get("Content-Type")) {
		writeError(w, r, http.StatusUnsupportedMediaType, "UNSUPPORTED_CONTENT", "Content-Type must be application/json.")
		return nil, request, false
	}
	raw, err := readRequestBody(w, r, limit)
	if err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "Request body exceeds its limit.")
		return nil, request, false
	}
	if !strictJSON(raw, &request, fields...) {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body.")
		return nil, request, false
	}
	return raw, request, true
}

func parsePageQuery(w http.ResponseWriter, r *http.Request, maximum int, additional ...string) (string, int, url.Values, bool) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid query parameters.")
		return "", 0, nil, false
	}
	allowed := []string{"cursor", "limit"}
	allowed = append(allowed, additional...)
	for key, items := range values {
		if !slices.Contains(allowed, key) || len(items) != 1 || items[0] == "" {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Unknown, empty, or repeated query parameter.")
			return "", 0, nil, false
		}
	}
	cursor := values.Get("cursor")
	if len(cursor) > 128 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid pagination cursor.")
		return "", 0, nil, false
	}
	limit := min(50, maximum)
	if raw := values.Get("limit"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > maximum || strconv.Itoa(parsed) != raw {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid page size.")
			return "", 0, nil, false
		}
		limit = parsed
	}
	return cursor, limit, values, true
}

func requiredIfMatch(w http.ResponseWriter, r *http.Request) (int64, bool) {
	values := r.Header.Values("If-Match")
	if len(values) != 1 {
		writeError(w, r, http.StatusPreconditionRequired, "PRECONDITION_REQUIRED", "If-Match with the current identity revision is required.")
		return 0, false
	}
	tag := values[0]
	if len(tag) < 3 || tag[0] != '"' || tag[len(tag)-1] != '"' {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid If-Match revision.")
		return 0, false
	}
	revision, err := strconv.ParseInt(tag[1:len(tag)-1], 10, 64)
	if err != nil || revision < 0 || strconv.FormatInt(revision, 10) != tag[1:len(tag)-1] {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid If-Match revision.")
		return 0, false
	}
	return revision, true
}

func noQuery(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.RawQuery == "" {
		return true
	}
	writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Query parameters are not supported for this operation.")
	return false
}
func requireNoBody(w http.ResponseWriter, r *http.Request) bool {
	if r.Body == nil || r.Body == http.NoBody {
		return true
	}
	body, err := readRequestBody(w, r, 1)
	if err != nil || len(body) != 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Request bodies are not accepted for this operation.")
		return false
	}
	return true
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request, allow string) {
	w.Header().Set("Allow", allow)
	writeError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed.")
}

func (h *identityAPI) listRoles(ctx context.Context) ([]authz.Role, error) {
	roles := authz.BuiltIns()
	if h.configuration == nil {
		return roles, nil
	}
	custom, err := h.configuration.List(ctx, "roles")
	if err != nil {
		return nil, err
	}
	for _, item := range custom.Items {
		var role authz.Role
		if err = json.Unmarshal(item.Value, &role); err != nil {
			return nil, err
		}
		if role.BuiltIn == "" {
			roles = append(roles, role)
		}
	}
	slices.SortFunc(roles, func(a, b authz.Role) int { return strings.Compare(a.ID, b.ID) })
	return roles, nil
}

func (h *identityAPI) validateRoleAssignments(ctx context.Context, ids []string) error {
	roles, err := h.listRoles(ctx)
	if err != nil {
		return err
	}
	known := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		known[role.ID] = struct{}{}
	}
	for _, id := range ids {
		if _, ok := known[id]; !ok {
			return identity.ErrInvalid
		}
	}
	return nil
}

func roleResponse(role authz.Role) roleView {
	return roleView{ID: role.ID, Name: role.Name, Description: role.Description, Capabilities: slices.Clone(role.Capabilities), BuiltIn: role.BuiltIn}
}

func sortAfterRole(roles []authz.Role, cursor string) int {
	if cursor == "" {
		return 0
	}
	for i, role := range roles {
		if role.ID == cursor {
			return i + 1
		}
	}
	return 0
}

func viewUser(account identity.Account, revision int64) userView {
	roles := account.RoleIDs
	if roles == nil {
		roles = []string{}
	}
	return userView{ID: account.ID, Login: account.Login, DisplayName: account.DisplayName, Kind: string(account.Kind), Enabled: account.Enabled,
		RoleIDs: slices.Clone(roles), Revision: revision, CreatedAt: account.CreatedAt, UpdatedAt: account.UpdatedAt}
}

func viewToken(token identity.Token) tokenView {
	scope := token.Scope
	if scope == nil {
		scope = []authz.Capability{}
	}
	return tokenView{Selector: token.Selector, AccountID: token.AccountID, Name: token.Name, Scope: slices.Clone(scope), CreatedAt: token.CreatedAt,
		ExpiresAt: token.ExpiresAt, NoExpiry: token.NoExpiry, RevokedAt: token.RevokedAt, LastUsedAt: token.LastUsedAt,
		RevokeOnPasswordChange: token.RevokeOnPasswordChange, Revision: token.Revision}
}

func cloneStringMap(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func writeSecurityError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, audit.ErrOperationConflict):
		writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Idempotency-Key conflicts with a different recorded request.")
	case errors.Is(err, audit.ErrOperationInFlight):
		writeError(w, r, http.StatusConflict, "OPERATION_IN_FLIGHT", "This operation is still being resolved.")
	case errors.Is(err, audit.ErrGeneralSaturated), errors.Is(err, audit.ErrProtectedSaturated), errors.Is(err, audit.ErrStorage), errors.Is(err, audit.ErrIntegrity), errors.Is(err, audit.ErrReconciliationRequired):
		writeError(w, r, http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE", "The audit store cannot safely admit this operation.")
	default:
		writeError(w, r, http.StatusServiceUnavailable, "STORE_UNAVAILABLE", "The requested operation could not be safely completed.")
	}
}

func writeIdentityError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, identity.ErrInvalid):
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "The identity request is invalid.")
	case errors.Is(err, identity.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "The requested identity does not exist.")
	case errors.Is(err, identity.ErrConflict):
		writeError(w, r, http.StatusPreconditionFailed, "PRECONDITION_FAILED", "The identity revision has changed.")
	case errors.Is(err, identity.ErrDuplicate):
		writeError(w, r, http.StatusConflict, "ALREADY_EXISTS", "The requested identity already exists.")
	case errors.Is(err, identity.ErrInvariant):
		writeError(w, r, http.StatusConflict, "ADMINISTRATOR_REQUIRED", "The operation would remove the last enabled password-capable built-in administrator.")
	case errors.Is(err, identity.ErrAccountLimit), errors.Is(err, identity.ErrTokenLimit):
		writeError(w, r, http.StatusConflict, "IDENTITY_LIMIT", "The identity store limit has been reached.")
	case errors.Is(err, identity.ErrAuthentication):
		writeError(w, r, http.StatusUnauthorized, "AUTHENTICATION_FAILED", "Password authentication failed.")
	case errors.Is(err, identity.ErrUnauthorized):
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "The verified actor is not authorized for this identity operation.")
	case errors.Is(err, identity.ErrRateLimited):
		writeError(w, r, http.StatusTooManyRequests, "AUTHENTICATION_BUSY", "Authentication is temporarily unavailable.")
	default:
		writeError(w, r, http.StatusServiceUnavailable, "STORE_UNAVAILABLE", "The identity store is unavailable; inspect the engine journal.")
	}
}


