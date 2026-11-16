package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/identity"
)

type routeCapability struct {
	Method     string
	Path       string
	Capability authz.Capability
}

type requestActor struct {
	Actor        authz.Actor
	Source       string
	AccountID    string
	Capabilities []authz.Capability
	Root         bool
}

type authenticatedActorKey struct{}

var engineRoutes = buildEngineRoutes()

func buildEngineRoutes() []routeCapability {
	var routes []routeCapability
	add := func(capability authz.Capability, path string, methods ...string) {
		for _, method := range methods {
			routes = append(routes, routeCapability{Method: method, Path: path, Capability: capability})
		}
	}
	read := []string{http.MethodGet, http.MethodHead}
	add(authz.StatusRead, "/api/v1/status/prefixes", read...)
	add(authz.StatusRead, "/api/v1/status/links", read...)
	add(authz.StatusRead, "/api/v1/openapi.json", read...)
	add(authz.BackupsCreate, "/api/v1/backups", http.MethodPost)
	add(authz.BackupsRestore, "/api/v1/restores/preview", http.MethodPost)
	add(authz.BackupsRestore, "/api/v1/restores", http.MethodPost)
	add(authz.ConfigurationRead, "/api/v1/schema", read...)
	add(authz.ConfigurationRead, "/api/v1/config", read...)
	add(authz.ConfigurationRead, "/api/v1/config/{collection}", read...)
	add(authz.ConfigurationWrite, "/api/v1/config/{collection}", http.MethodPost)
	add(authz.ConfigurationRead, "/api/v1/config/{collection}/{id}", read...)
	add(authz.ConfigurationWrite, "/api/v1/config/{collection}/{id}", http.MethodPut, http.MethodDelete)
	add(authz.ConfigurationWrite, "/api/v1/config/changes", http.MethodPost)
	add(authz.ConfigurationRead, "/api/v1/versions", read...)
	add(authz.ConfigurationRead, "/api/v1/versions/{id}", read...)
	add(authz.ConfigurationRead, "/api/v1/preview/network", read...)
	add(authz.ConfigurationRead, "/api/v1/applies", read...)
	add(authz.ConfigurationApply, "/api/v1/applies", http.MethodPost)
	add(authz.ConfigurationRead, "/api/v1/applies/{id}", read...)
	add(authz.ConfigurationConfirm, "/api/v1/applies/{id}/confirmation", http.MethodPost)
	add(authz.ConfigurationApply, "/api/v1/applies/{id}/rollback", http.MethodPost)
	add(authz.ConfigurationRead, "/api/v1/takeover/proposal", read...)
	add(authz.ConfigurationWrite, "/api/v1/preview/network/draft", http.MethodPost)
	add(authz.ConfigurationRead, "/api/v1/config/policy/{chain}", read...)
	add(authz.ConfigurationWrite, "/api/v1/config/policy/{chain}", http.MethodPost)
	add(authz.ConfigurationRead, "/api/v1/config/policy/{chain}/{id}", read...)
	add(authz.ConfigurationWrite, "/api/v1/config/policy/{chain}/{id}", http.MethodPut, http.MethodDelete)
	add(authz.ConfigurationWrite, "/api/v1/config/policy/{chain}/order", http.MethodPut)
	add(authz.ConfigurationRead, "/api/v1/config/management", read...)
	add(authz.ConfigurationWrite, "/api/v1/config/management", http.MethodPut, http.MethodDelete)
	add(authz.ConfigurationRead, "/api/v1/config/nat/outbound", read...)
	add(authz.ConfigurationWrite, "/api/v1/config/nat/outbound", http.MethodPut, http.MethodDelete)
	add(authz.ConfigurationRead, "/api/v1/config/dns", read...)
	add(authz.ConfigurationWrite, "/api/v1/config/dns", http.MethodPut, http.MethodDelete)
	add(authz.ConfigurationRead, "/api/v1/config/nat/{list}", read...)
	add(authz.ConfigurationWrite, "/api/v1/config/nat/{list}", http.MethodPost)
	add(authz.ConfigurationRead, "/api/v1/config/nat/{list}/{id}", read...)
	add(authz.ConfigurationWrite, "/api/v1/config/nat/{list}/{id}", http.MethodPut, http.MethodDelete)
	add(authz.ConfigurationWrite, "/api/v1/config/nat/{list}/order", http.MethodPut)
	add(authz.UsersRead, "/api/v1/capabilities", read...)
	add(authz.UsersRead, "/api/v1/roles", read...)
	add(authz.UsersRead, "/api/v1/users", read...)
	add(authz.UsersManage, "/api/v1/users", http.MethodPost)
	add(authz.UsersRead, "/api/v1/users/{id}", read...)
	add(authz.UsersManage, "/api/v1/users/{id}", http.MethodPatch, http.MethodDelete)
	add(authz.UsersManage, "/api/v1/users/{id}/password", http.MethodPut)
	add(authz.TokensManageSelf, "/api/v1/tokens", read...)
	add(authz.TokensManageSelf, "/api/v1/tokens", http.MethodPost)
	add(authz.TokensManageSelf, "/api/v1/tokens/{selector}", http.MethodDelete)
	add(authz.AuditRead, "/api/v1/audit/events", read...)
	add(authz.AuditRead, "/api/v1/audit/export", read...)
	return routes
}

func validateRouteCapabilities(document []byte, routes []routeCapability) error {
	var contract struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(document, &contract); err != nil {
		return fmt.Errorf("decode OpenAPI routes: %w", err)
	}
	if contract.Paths == nil {
		return fmt.Errorf("OpenAPI document has no paths")
	}
	known := authz.Catalog()
	classified := make(map[string]authz.Capability, len(routes))
	for _, route := range routes {
		key := route.Method + " " + route.Path
		if !slices.Contains(known, route.Capability) {
			return fmt.Errorf("route %s has unknown capability %q", key, route.Capability)
		}
		if _, exists := classified[key]; exists {
			return fmt.Errorf("route %s has multiple capability declarations", key)
		}
		classified[key] = route.Capability
	}
	contractRoutes := make(map[string]struct{})
	for path, operations := range contract.Paths {
		for method := range operations {
			method = strings.ToUpper(method)
			if method == "PARAMETERS" || strings.HasPrefix(method, "X-") {
				continue
			}
			switch method {
			case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			default:
				return fmt.Errorf("OpenAPI route %s %s is not a supported HTTP method", path, strings.ToLower(method))
			}
			key := method + " " + path
			contractRoutes[key] = struct{}{}
			if _, exists := classified[key]; !exists {
				return fmt.Errorf("unclassified route %s %s", path, strings.ToLower(method))
			}
		}
	}
	for key := range classified {
		if _, exists := contractRoutes[key]; !exists {
			return fmt.Errorf("route %s is absent from OpenAPI", key)
		}
	}
	return nil
}

func routeForRequest(method, path string) (routeCapability, bool) {
	method = strings.ToUpper(method)
	best, bestScore, found := routeCapability{}, -1, false
	for _, route := range engineRoutes {
		if route.Method != method || !routePathMatches(route.Path, path) {
			continue
		}
		score := routeSpecificity(route.Path)
		if score > bestScore {
			best, bestScore, found = route, score, true
		}
	}
	return best, found
}

func routePathMatches(pattern, path string) bool {
	patternParts := strings.Split(strings.Trim(pattern, "/"), "/")
	pathParts := strings.Split(strings.Trim(path, "/"), "/")
	if len(patternParts) != len(pathParts) {
		return false
	}
	for i, part := range patternParts {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			if pathParts[i] == "" {
				return false
			}
			continue
		}
		if part != pathParts[i] {
			return false
		}
	}
	return true
}

func routeSpecificity(pattern string) int {
	score := 0
	for _, part := range strings.Split(strings.Trim(pattern, "/"), "/") {
		if !strings.HasPrefix(part, "{") {
			score += 2
		}
		score++
	}
	return score
}

func actorFromRequest(r *http.Request) (requestActor, bool) {
	peer, ok := r.Context().Value(peerKey{}).(peerIdentity)
	if ok && peer.verified && peer.uid == 0 {
		return requestActor{Actor: authz.Actor{Kind: authz.ActorSystem, ID: "root"}, Source: "unix:uid:0", Capabilities: authz.Catalog(), Root: true}, true
	}
	principal, ok := r.Context().Value(authenticatedActorKey{}).(requestActor)
	if !ok || principal.Root || !validRequestActor(principal) {
		return requestActor{}, false
	}
	return principal, true
}

// withVerifiedPrincipal is called only after identity store authentication succeeds.
func withVerifiedPrincipal(ctx context.Context, principal identity.Principal, source string) context.Context {
	trusted := requestActor{Actor: principal.Actor, Source: source, AccountID: principal.Account.ID, Capabilities: slices.Clone(principal.Capabilities)}
	if validRequestActor(trusted) {
		return context.WithValue(ctx, authenticatedActorKey{}, trusted)
	}
	return ctx
}

func validRequestActor(actor requestActor) bool {
	if !validSource(actor.Source) {
		return false
	}
	switch actor.Actor.Kind {
	case authz.ActorAccount:
		if !validAccountActorID(actor.Actor.ID) || actor.AccountID != actor.Actor.ID {
			return false
		}
	case authz.ActorToken:
		if !validTokenActorID(actor.Actor.ID) || !validAccountActorID(actor.AccountID) {
			return false
		}
	default:
		return false
	}
	known := authz.Catalog()
	for _, capability := range actor.Capabilities {
		if !slices.Contains(known, capability) {
			return false
		}
	}
	return true
}

func validAccountActorID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' || id[14] != '4' {
		return false
	}
	for i, char := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return id[19] == '8' || id[19] == '9' || id[19] == 'a' || id[19] == 'b'
}

func validTokenActorID(id string) bool {
	if len(id) != 22 {
		return false
	}
	for _, char := range id {
		if !(char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func validSource(source string) bool {
	if source == "unix" || source == "local" {
		return true
	}
	if strings.HasPrefix(source, "unix:uid:") {
		value := strings.TrimPrefix(source, "unix:uid:")
		if value == "0" {
			return true
		}
		parsed, err := strconv.ParseUint(value, 10, 32)
		return err == nil && parsed > 0 && strconv.FormatUint(parsed, 10) == value
	}
	address, err := netip.ParseAddr(source)
	return err == nil && address.String() == source
}

func hasCapability(actor requestActor, required authz.Capability) bool {
	if actor.Root {
		return true
	}
	if slices.Contains(actor.Capabilities, required) {
		return true
	}
	return required == authz.TokensManageSelf && slices.Contains(actor.Capabilities, authz.TokensManageAll)
}
func capabilitiesFor(actor requestActor) []authz.Capability {
	if actor.Root {
		return authz.Catalog()
	}
	return actor.Capabilities
}

func authorizeRequest(w http.ResponseWriter, r *http.Request) (requestActor, bool) {
	actor, ok := actorFromRequest(r)
	if !ok {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "A verified root, session, or token identity is required.")
		return requestActor{}, false
	}
	route, ok := routeForRequest(r.Method, r.URL.Path)
	if !ok {
		if actor.Root {
			return actor, true
		}
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
		return requestActor{}, false
	}
	if !hasCapability(actor, route.Capability) {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "The verified actor lacks the required capability.")
		return requestActor{}, false
	}
	return actor, true
}

type securityCoordinator struct {
	identities identity.Store
	audits     *audit.Store
}

type operationExecution struct {
	Outcome      audit.Outcome
	Value        any
	Replay       bool
	Committed    bool
	AuditPending bool
	MutationErr  error
}

func newSecurityCoordinator(identities identity.Store, audits *audit.Store) *securityCoordinator {
	return &securityCoordinator{identities: identities, audits: audits}
}

func (c *securityCoordinator) LookupOperation(ctx context.Context, operationID string) (audit.OperationRecord, bool, error) {
	record, found, err := c.identities.LookupOperation(ctx, operationID)
	if err != nil || !found {
		return audit.OperationRecord{}, found, err
	}
	return audit.OperationRecord{
		OperationID: record.OperationID,
		RequestHash: record.RequestHash,
		Actor:       record.Actor,
		Source:      record.Source,
		Action:      audit.Action(record.Action),
		Target:      record.Target,
		Category:    audit.Category(record.Category),
		Outcome:     audit.Outcome{
			OperationID: record.OperationID,
			Result:      audit.Result(record.Result),
			Target:      record.OutcomeTarget,
			Metadata:    record.Metadata,
		},
	}, true, nil
}

func (c *securityCoordinator) execute(
	ctx context.Context,
	actor requestActor,
	operationID string,
	fingerprint []byte,
	action audit.Action,
	category audit.Category,
	target string,
	mutate func(context.Context) (string, any, error),
) (operationExecution, error) {
	requestHash, err := c.identities.HashRequest(ctx, fingerprint)
	if err != nil {
		return operationExecution{}, err
	}
	intent := audit.Intent{
		OperationID: operationID,
		Actor:       actor.Actor,
		Source:      actor.Source,
		Action:      action,
		Target:      target,
		RequestHash: requestHash,
		Category:    category,
	}
	recorded, err := c.audits.Begin(ctx, intent)
	if err != nil {
		return operationExecution{}, err
	}
	if recorded.Replay {
		if recorded.Outcome == nil {
			return operationExecution{}, audit.ErrIntegrity
		}
		outcome := *recorded.Outcome
		if action == audit.ActionTokenCreated {
			outcome.Metadata = cloneStringMap(outcome.Metadata)
			outcome.Metadata[audit.MetadataSecretUnavailable] = "true"
		}
		return operationExecution{Outcome: outcome, Replay: true, Committed: true}, nil
	}
	operationCtx := identity.WithOperation(ctx, identity.OperationIntent{
		OperationID: operationID,
		RequestHash: requestHash,
		Actor:       actor.Actor,
		Source:      actor.Source,
		Action:      string(action),
		Target:      target,
		Category:    string(category),
	})
	createdTarget, value, mutationErr := mutate(operationCtx)
	if mutationErr != nil {
		outcome := audit.Outcome{OperationID: operationID, Result: audit.ResultNotCommitted}
		finishErr := c.audits.Finish(ctx, outcome)
		return operationExecution{Outcome: outcome, MutationErr: mutationErr, AuditPending: finishErr != nil}, nil
	}
	outcome := audit.Outcome{OperationID: operationID, Result: audit.ResultSuccess}
	if action == audit.ActionAccountCreated || action == audit.ActionTokenCreated {
		outcome.Target = createdTarget
	}
	if action == audit.ActionTokenCreated {
		outcome.Metadata = map[string]string{audit.MetadataSecretUnavailable: "false"}
	}
	finishErr := c.audits.Finish(ctx, outcome)
	return operationExecution{Outcome: outcome, Value: value, Committed: true, AuditPending: finishErr != nil}, nil
}

