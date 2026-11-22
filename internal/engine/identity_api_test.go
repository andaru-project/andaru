//go:build integration

package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
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
	coordinator := newSecurityCoordinator(identities, audits, stateDir)
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
		// Byte-exact names: a browser, unlike Go's decoder, does not read "Kind" as "kind".
		if !strings.Contains(response.Body.String(), `"actor":{"kind":"system","id":"root"}`) {
			t.Fatalf("audit actor is not in the contract's form: %s", response.Body)
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
		// A read of a page and an export of the log are each their own event.
		if event.Action == audit.ActionAuditQueried && event.Result == audit.ResultSuccess && event.Count == 1 {
			queries++
			found["export"] = found["export"] || event.Target == audit.AuditExportTarget
		}
	}
	if !found["account"] || !found["token"] || !found["export"] || queries != 2 {
		t.Fatalf("actual generated creation targets or audited reads missing: creations=%+v queryCount=%d", found, queries)
	}
	// order=desc reads newest first, and its cursor pages towards older events.
	var newest auditPage
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, rootRequest(http.MethodGet, "/api/v1/audit/events?order=desc&limit=1"))
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &newest) != nil || len(newest.Items) != 1 || newest.Items[0].ID != events[len(events)-1].ID || newest.NextCursor != strconv.FormatUint(newest.Items[0].ID, 10) {
		t.Fatalf("newest page status=%d body=%s", response.Code, response.Body)
	}
	var older auditPage
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, rootRequest(http.MethodGet, "/api/v1/audit/events?order=desc&limit=1&cursor="+newest.NextCursor))
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &older) != nil || len(older.Items) != 1 || older.Items[0].ID != events[len(events)-2].ID {
		t.Fatalf("older page status=%d body=%s", response.Code, response.Body)
	}
	var oldest auditPage
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, rootRequest(http.MethodGet, "/api/v1/audit/events?order=asc&limit=1"))
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &oldest) != nil || len(oldest.Items) != 1 || oldest.Items[0].ID != events[0].ID {
		t.Fatalf("oldest page status=%d body=%s", response.Code, response.Body)
	}
	for _, path := range []string{"/api/v1/audit/events?order=sideways", "/api/v1/audit/events?order=desc&cursor=9223372036854775808"} {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, rootRequest(http.MethodGet, path))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d", path, response.Code)
		}
	}
	// A query refused as invalid is not recorded as the store failing.
	after, err := audits.ListEventsBefore(context.Background(), 0, 1)
	if err != nil || len(after) != 1 || after[0].Result == audit.ResultUnavailable {
		t.Fatalf("newest event after refused queries = %+v, %v", after, err)
	}
}

func TestBootstrapAdminAPIIsRootOnlyAtomicAndAudited(t *testing.T) {
	identities, audits, _, handler := identityTestServices(t)
	body := `{"login":"rootadmin","displayName":"Root","password":"unique-admin-password-41"}`

	nonRoot := httptest.NewRequest(http.MethodPost, "/api/v1/users/bootstrap-admin", strings.NewReader(body))
	nonRoot.Header.Set("Content-Type", "application/json")
	nonRoot.Header.Set("Idempotency-Key", "bootstrap-non-root")
	nonRoot = nonRoot.WithContext(withVerifiedPrincipal(nonRoot.Context(), identity.Principal{
		Account:      identity.Account{ID: "12345678-1234-4234-8234-123456789abc"},
		Actor:        authz.Actor{Kind: authz.ActorAccount, ID: "12345678-1234-4234-8234-123456789abc"},
		Capabilities: []authz.Capability{authz.UsersManage},
	}, "127.0.0.1"))
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, nonRoot)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("non-root bootstrap status=%d body=%s", denied.Code, denied.Body)
	}

	unknown := identityRequest(handler, http.MethodPost, "/api/v1/users/bootstrap-admin", strings.TrimSuffix(body, "}")+`,"roleIds":["administrator"]}`, "bootstrap-unknown")
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown bootstrap field status=%d body=%s", unknown.Code, unknown.Body)
	}
	created := identityRequest(handler, http.MethodPost, "/api/v1/users/bootstrap-admin", body, "bootstrap-admin-1")
	if created.Code != http.StatusCreated {
		t.Fatalf("bootstrap status=%d body=%s", created.Code, created.Body)
	}
	var result operationResponse
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil || result.Target == "" || strings.Contains(created.Body.String(), "password") {
		t.Fatalf("bootstrap response=%s err=%v", created.Body, err)
	}
	account, err := identities.GetAccount(context.Background(), result.Target)
	if err != nil || account.Login != "rootadmin" || !account.Enabled || !slices.Equal(account.RoleIDs, []string{authz.AdministratorRoleID}) {
		t.Fatalf("bootstrap account=%+v err=%v", account, err)
	}
	valid, err := identities.VerifyPassword(context.Background(), account.ID, []byte("unique-admin-password-41"))
	if err != nil || !valid {
		t.Fatalf("bootstrap credential valid=%t err=%v", valid, err)
	}
	record, found, err := identities.LookupOperation(context.Background(), "bootstrap-admin-1")
	if err != nil || !found || record.OutcomeTarget != account.ID {
		t.Fatalf("bootstrap operation=%+v found=%t err=%v", record, found, err)
	}
	events, err := audits.ListEvents(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	audited := false
	for _, event := range events {
		audited = audited || event.Action == audit.ActionAccountCreated && event.Target == account.ID && event.Actor == (authz.Actor{Kind: authz.ActorSystem, ID: "root"})
	}
	if !audited {
		t.Fatal("bootstrap creation audit outcome missing")
	}
	second := identityRequest(handler, http.MethodPost, "/api/v1/users/bootstrap-admin", `{"login":"otheradmin","password":"another-unique-password-42"}`, "bootstrap-admin-2")
	if second.Code != http.StatusPreconditionFailed {
		t.Fatalf("second bootstrap status=%d body=%s", second.Code, second.Body)
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
	coordinator := newSecurityCoordinator(identities, audits, stateDir)
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
	record, found, err := identities.LookupOperation(ctx, intent.OperationID)
	if err != nil || !found || record.Target != "" || record.OutcomeTarget != account.ID {
		t.Fatalf("authoritative creation record = %+v, found=%t err=%v", record, found, err)
	}
	if err = audits.Close(); err != nil {
		t.Fatal(err)
	}
	audits, err = audit.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer audits.Close()
	coordinator = newSecurityCoordinator(identities, audits, stateDir)
	if err = audits.Reconcile(ctx, coordinator); err != nil {
		t.Fatal(err)
	}
	events, err := audits.ListEvents(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	intentFound, outcomeFound := false, false
	for _, event := range events {
		if event.Action != audit.ActionAccountCreated || event.OperationID != intent.OperationID {
			continue
		}
		switch event.Result {
		case audit.ResultPending:
			if event.Target != "" {
				t.Fatalf("creation intent target = %q, want empty", event.Target)
			}
			intentFound = true
		case audit.ResultSuccess:
			if event.Target != account.ID {
				t.Fatalf("reconciled creation target = %q, want generated account ID %q", event.Target, account.ID)
			}
			outcomeFound = true
		}
	}
	if !intentFound || !outcomeFound {
		t.Fatalf("reconciliation did not preserve empty intent and generated outcome target: intent=%t outcome=%t", intentFound, outcomeFound)
	}
	replay, err := audits.Begin(ctx, intent)
	if err != nil || !replay.Replay || replay.Outcome == nil || replay.Outcome.Target != account.ID {
		t.Fatalf("reconciled replay = %+v, error=%v", replay, err)
	}
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

func TestDeletingAnAccountRemovesItsPreferencesButAReplayDoesNot(t *testing.T) {
	identities, _, coordinator, handler := identityTestServices(t)
	if _, _, err := identities.BootstrapAdmin(context.Background(), "rootadmin", "Root", []byte("unique-admin-password-41")); err != nil {
		t.Fatal(err)
	}
	created := identityRequest(handler, http.MethodPost, "/api/v1/users", `{"login":"worker","displayName":"Worker","kind":"service","enabled":true,"roleIds":["read-only"],"expectedRevision":1}`, "account-create-1")
	var account operationResponse
	if err := json.Unmarshal(created.Body.Bytes(), &account); err != nil || created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s err=%v", created.Code, created.Body, err)
	}
	stored := func() string {
		t.Helper()
		document, err := readPreferences(coordinator.stateDir, account.Target)
		if err != nil {
			t.Fatal(err)
		}
		return string(document)
	}
	remove := func() *httptest.ResponseRecorder {
		request := rootRequest(http.MethodDelete, "/api/v1/users/"+account.Target)
		request.Body = http.NoBody
		request.Header.Set("Idempotency-Key", "account-delete-1")
		request.Header.Set("If-Match", `"2"`)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	if err := writePreferences(coordinator.stateDir, account.Target, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if response := remove(); response.Code != http.StatusOK || stored() != "{}" {
		t.Fatalf("deleting the account left its preferences: status=%d body=%s stored=%s", response.Code, response.Body, stored())
	}
	// A restore may bring the account back under its ID; a replayed delete answers for the old one and removes nothing.
	if err := writePreferences(coordinator.stateDir, account.Target, []byte(`{"a":2}`)); err != nil {
		t.Fatal(err)
	}
	if response := remove(); response.Code != http.StatusOK || stored() != `{"a":2}` {
		t.Fatalf("a replayed delete removed preferences: status=%d body=%s stored=%s", response.Code, response.Body, stored())
	}
}
