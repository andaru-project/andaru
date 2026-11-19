//go:build integration

package engine

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/identity"
)

func TestApplyRefusesStaleIdentityRevisionBeforeActivation(t *testing.T) {
	b := newApplyBench(t)
	b.afterArm = func() {
		b.identities.mu.Lock()
		b.identities.revision++
		b.identities.mu.Unlock()
	}
	response := b.call(http.MethodPost, appliesPath, applyBody)
	assertAPIError(t, response, http.StatusPreconditionFailed, "IDENTITY_REVISION_CHANGED")
	if slices.Contains(b.calls, "activate 1") {
		t.Fatalf("stale candidate reached activation: %q", b.calls)
	}
}

func TestApplyBlocksAccountEditAcrossActivationGap(t *testing.T) {
	b := newApplyBench(t)
	var mutationErr error
	b.beforeVerb = func() {
		mutationErr = b.coordinator.withAuthorizationLock(t.Context(), func() error {
			b.identities.mu.Lock()
			b.identities.revision++
			b.identities.mu.Unlock()
			return nil
		})
	}
	b.apply(applyBody)
	if !errors.Is(mutationErr, authz.ErrTransitionInProgress) {
		t.Fatalf("concurrent identity mutation error = %v", mutationErr)
	}
	if revision, _ := b.identities.Revision(t.Context()); revision != 1 {
		t.Fatalf("identity revision changed during activation: %d", revision)
	}
}

func TestAuthorizationChangingConfirmationRequiresNewHumanAdministrator(t *testing.T) {
	b := newApplyBench(t)
	applied := b.apply(applyBody)
	accountID := "00000000-0000-4000-8000-000000000001"
	admin := identity.Account{ID: accountID, Kind: identity.Human, Enabled: true, RoleIDs: []string{authz.AdministratorRoleID}}
	service := identity.Account{ID: accountID, Kind: identity.Service, Enabled: true, RoleIDs: []string{authz.AdministratorRoleID}}
	b.identities.accounts[accountID] = admin
	capabilities := []authz.Capability{authz.ConfigurationConfirm}
	old := identity.Principal{Account: admin, Actor: authz.Actor{Kind: authz.ActorAccount, ID: accountID}, Capabilities: capabilities, SessionSelector: strings.Repeat("s", 43)}
	assertAPIError(t, b.callPrincipal(http.MethodPost, appliesPath+"/"+applied.ID+"/confirmation", old), http.StatusForbidden, "NEW_ADMIN_SESSION_REQUIRED")

	token := identity.Principal{Account: service, Actor: authz.Actor{Kind: authz.ActorToken, ID: strings.Repeat("A", 22)}, Capabilities: capabilities}
	assertAPIError(t, b.callPrincipal(http.MethodPost, appliesPath+"/"+applied.ID+"/confirmation", token), http.StatusForbidden, "NEW_ADMIN_SESSION_REQUIRED")

	serviceSession := identity.Principal{Account: service, Actor: authz.Actor{Kind: authz.ActorAccount, ID: accountID}, Capabilities: capabilities, SessionSelector: strings.Repeat("s", 43), AuthorizationEpoch: b.authorizationState().AuthorizationEpoch}
	assertAPIError(t, b.callPrincipal(http.MethodPost, appliesPath+"/"+applied.ID+"/confirmation", serviceSession), http.StatusForbidden, "NEW_ADMIN_SESSION_REQUIRED")

	fresh := identity.Principal{Account: admin, Actor: authz.Actor{Kind: authz.ActorAccount, ID: accountID}, Capabilities: capabilities, SessionSelector: strings.Repeat("s", 43), AuthorizationEpoch: b.authorizationState().AuthorizationEpoch}
	response := b.callPrincipal(http.MethodPost, appliesPath+"/"+applied.ID+"/confirmation", fresh)
	if response.Code != http.StatusOK {
		t.Fatalf("fresh administrator confirmation status=%d body=%s", response.Code, response.Body)
	}
}
