//go:build integration

package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/host"
	"github.com/andaru-project/andaru/internal/identity"
)

func identityTestServices(t *testing.T) (identity.Store, *audit.Store, *securityCoordinator, http.Handler) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("identity and audit stores require root")
	}
	stateDir := t.TempDir() + "/state"
	identities, err := identity.Open(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = identities.Close() })
	audits, err := audit.Open(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = audits.Close() })
	coordinator := newSecurityCoordinator(identities, audits)
	identityAPI := newIdentityAPI(identities, nil, coordinator)
	auditAPI := newAuditAPI(coordinator)
	handler := newHandlerWithAPIs(func(context.Context) ([]host.LinkStatus, error) { return []host.LinkStatus{}, nil }, http.NotFoundHandler(), identityAPI, auditAPI)
	return identities, audits, coordinator, handler
}

func TestIdentityAPICreationTargetsAndTokenReplay(t *testing.T) {
	identities, audits, _, handler := identityTestServices(t)
	admin, _, err := identities.BootstrapAdmin(context.Background(), "rootadmin", "Root", []byte("unique-admin-password-41"))
	if err != nil {
		t.Fatal(err)
	}

	bad := identityRequest(handler, http.MethodPost, "/api/v1/users", `{"login":"worker","kind":"service","enabled":true,"roleIds":["read-only"],"expectedRevision":1,"credential":"forbidden"}`, "bad-body")
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown body field status=%d body=%s", bad.Code, bad.Body)
	}
	stale := identityRequest(handler, http.MethodPost, "/api/v1/users", `{"login":"worker","displayName":"Worker","kind":"service","enabled":true,"roleIds":["read-only"],"expectedRevision":0}`, "stale-body")
	if stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale CAS status=%d body=%s", stale.Code, stale.Body)
	}

	userBody := `{"login":"worker","displayName":"Worker","kind":"service","enabled":true,"roleIds":["read-only"],"expectedRevision":1}`
	created := identityRequest(handler, http.MethodPost, "/api/v1/users", userBody, "account-create-1")
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body)
	}
	var accountResult operationResponse
	if err = json.Unmarshal(created.Body.Bytes(), &accountResult); err != nil || accountResult.Target == "" {
		t.Fatalf("account create result=%s err=%v", created.Body, err)
	}
	if !strings.Contains(accountResult.Target, "-") || strings.Contains(created.Body.String(), "password") || strings.Contains(created.Body.String(), "credential") {
		t.Fatalf("unsafe account response: %s", created.Body)
	}
	underScoped := `{"accountId":"` + accountResult.Target + `","name":"under-scoped","scope":["users.manage"],"noExpiry":true}`
	if denied := identityRequest(handler, http.MethodPost, "/api/v1/tokens", underScoped, "token-scope-denied"); denied.Code != http.StatusForbidden {
		t.Fatalf("target scope intersection status=%d body=%s", denied.Code, denied.Body)
	}

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, rootRequest(http.MethodGet, "/api/v1/users?limit=1"))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"nextCursor"`) || strings.Contains(list.Body.String(), `"secret"`) {
		t.Fatalf("bounded user list status=%d body=%s", list.Code, list.Body)
	}

	tokenBody := `{"accountId":"` + admin.ID + `","name":"automation","scope":["status.read"],"noExpiry":true}`
	issued := identityRequest(handler, http.MethodPost, "/api/v1/tokens", tokenBody, "token-create-1")
	if issued.Code != http.StatusCreated {
		t.Fatalf("token issue status=%d body=%s", issued.Code, issued.Body)
	}
	var tokenResult tokenIssueResponse
	if err = json.Unmarshal(issued.Body.Bytes(), &tokenResult); err != nil || tokenResult.Secret == "" || tokenResult.Target != tokenResult.Selector {
		t.Fatalf("one-time token response=%s err=%v", issued.Body, err)
	}
	tokenList := httptest.NewRecorder()
	handler.ServeHTTP(tokenList, rootRequest(http.MethodGet, "/api/v1/tokens?accountId="+admin.ID))
	if tokenList.Code != http.StatusOK || strings.Contains(tokenList.Body.String(), tokenResult.Secret) || !strings.Contains(tokenList.Body.String(), tokenResult.Selector) {
		t.Fatalf("credential-free token page status=%d body=%s", tokenList.Code, tokenList.Body)
	}
	replayed := identityRequest(handler, http.MethodPost, "/api/v1/tokens", tokenBody, "token-create-1")
	var replayResult tokenIssueResponse
	if replayed.Code != http.StatusOK || json.Unmarshal(replayed.Body.Bytes(), &replayResult) != nil || replayResult.Secret != "" || !replayResult.SecretUnavailable || replayResult.Target != tokenResult.Selector {
		t.Fatalf("token replay status=%d body=%s", replayed.Code, replayed.Body)
	}
	changed := identityRequest(handler, http.MethodPost, "/api/v1/tokens", strings.Replace(tokenBody, "automation", "changed", 1), "token-create-1")
	if changed.Code != http.StatusConflict {
		t.Fatalf("changed idempotency body status=%d body=%s", changed.Code, changed.Body)
	}

	if strings.Contains(issued.Body.String(), `"secretHash"`) || strings.Contains(issued.Body.String(), `"passwordHash"`) {
		t.Fatal("credential fields leaked in the token response")
	}
	for _, path := range []string{"/api/v1/audit/events?limit=100", "/api/v1/audit/export?limit=100"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, rootRequest(http.MethodGet, path))
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), tokenResult.Secret) {
			t.Fatalf("audited query %s status=%d body=%s", path, response.Code, response.Body)
		}
		if strings.Contains(path, "/export") && !strings.Contains(response.Header().Get("Content-Disposition"), "andaru-audit.json") {
			t.Fatalf("audit export missing attachment header: %v", response.Header())
		}
	}
	events, err := audits.ListEvents(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	queries := 0
	found := map[string]bool{}
	for _, event := range events {
		if event.Action == audit.ActionAccountCreated && event.Target == accountResult.Target && event.Actor == (authz.Actor{Kind: authz.ActorSystem, ID: "root"}) {
			found["account"] = true
		}
		if event.Action == audit.ActionTokenCreated && event.Target == tokenResult.Selector && event.Actor == (authz.Actor{Kind: authz.ActorSystem, ID: "root"}) {
			found["token"] = true
		}
		if event.Action == audit.ActionAuditQueried && event.Result == audit.ResultSuccess {
			queries++
		}
	}
	if !found["account"] || !found["token"] || queries != 2 {
		t.Fatalf("actual generated creation targets or audited reads missing: creations=%+v queryCount=%d", found, queries)
	}
}

func TestAuditReconcilesIdentityGeneratedCreationTarget(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("identity and audit stores require root")
	}
	ctx := context.Background()
	stateDir := t.TempDir() + "/state"
	identities, err := identity.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer identities.Close()
	_, revision, err := identities.BootstrapAdmin(ctx, "rootadmin", "Root", []byte("unique-admin-password-41"))
	if err != nil {
		t.Fatal(err)
	}
	audits, err := audit.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	coordinator := newSecurityCoordinator(identities, audits)
	body := []byte(`{"login":"worker","kind":"service"}`)
	hash, err := identities.HashRequest(ctx, body)
	if err != nil {
		t.Fatal(err)
	}
	intent := audit.Intent{
		OperationID: "reconcile-create-1", Actor: authz.Actor{Kind: authz.ActorSystem, ID: "root"}, Source: "unix:uid:0",
		Action: audit.ActionAccountCreated, Category: audit.CategoryProtected, RequestHash: hash,
	}
	if _, err = audits.Begin(ctx, intent); err != nil {
		t.Fatal(err)
	}
	account, _, err := identities.CreateAccount(identity.WithOperation(ctx, identity.OperationIntent{
		OperationID: intent.OperationID, RequestHash: hash, Actor: intent.Actor, Source: intent.Source,
		Action: string(intent.Action), Category: string(intent.Category),
	}), identity.CreateAccountInput{Login: "worker", Kind: identity.Service, Enabled: true, RoleIDs: []string{authz.ReadOnlyRoleID}}, revision)
	if err != nil {
		t.Fatal(err)
	}
	if err = audits.Close(); err != nil {
		t.Fatal(err)
	}
	audits, err = audit.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer audits.Close()
	coordinator = newSecurityCoordinator(identities, audits)
	if err = audits.Reconcile(ctx, coordinator); err != nil {
		t.Fatal(err)
	}
	events, err := audits.ListEvents(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Action == audit.ActionAccountCreated && event.OperationID == intent.OperationID {
			if event.Target != account.ID || event.Target == "" {
				t.Fatalf("reconciled creation target = %q, want generated account ID %q", event.Target, account.ID)
			}
			return
		}
	}
	t.Fatal("reconciliation did not append the committed identity creation outcome")
}

func identityRequest(handler http.Handler, method, path, body, operationID string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := rootRequest(method, path)
	request.Body = http.NoBody
	if body != "" {
		request = httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(rootRequest(method, path).Context())
		request.Header.Set("Content-Type", "application/json")
	}
	if operationID != "" {
		request.Header.Set("Idempotency-Key", operationID)
	}
	handler.ServeHTTP(recorder, request)
	return recorder
}
