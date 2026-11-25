package authz

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	AdministratorRoleID = "administrator"
	ReadOnlyRoleID      = "read-only"
)

type Role struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Description  string       `json:"description,omitempty"`
	Capabilities []Capability `json:"capabilities"`
	BuiltIn      string       `json:"builtIn,omitempty"`
}

type Snapshot struct {
	Digest       string
	Roles        []Role
	Capabilities map[string][]Capability
}

var roleIDPattern = regexp.MustCompile(`^rol_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func BuiltIns() []Role {
	return []Role{
		{ID: AdministratorRoleID, Name: "Administrator", Capabilities: Catalog(), BuiltIn: AdministratorRoleID},
		{ID: ReadOnlyRoleID, Name: "Read-only", Capabilities: []Capability{ConfigurationRead, NotificationsRead, StatusRead}, BuiltIn: ReadOnlyRoleID},
	}
}

func ValidateRoles(custom []Role) error {
	if len(custom) > 10000 {
		return errors.New("too many custom roles")
	}
	ids := make(map[string]bool, len(custom))
	names := make(map[string]bool, len(custom))
	for _, role := range custom {
		nameKey := strings.ToLower(role.Name)
		if !roleIDPattern.MatchString(role.ID) || role.BuiltIn != "" || role.ID == AdministratorRoleID || role.ID == ReadOnlyRoleID {
			return errors.New("invalid custom role identity")
		}
		if !utf8.ValidString(role.Name) || utf8.RuneCountInString(role.Name) < 1 || utf8.RuneCountInString(role.Name) > 128 || strings.IndexFunc(role.Name, unicode.IsControl) >= 0 ||
			!utf8.ValidString(role.Description) || utf8.RuneCountInString(role.Description) > 4096 || strings.IndexFunc(role.Description, unicode.IsControl) >= 0 {
			return errors.New("invalid custom role text")
		}
		if strings.EqualFold(role.Name, "Administrator") || strings.EqualFold(role.Name, "Read-only") {
			return errors.New("custom role shadows a built-in")
		}
		if ids[role.ID] || names[nameKey] {
			return errors.New("duplicate custom role identity")
		}
		ids[role.ID], names[nameKey] = true, true
		for i, capability := range role.Capabilities {
			if !slices.Contains(catalog, capability) || slices.Contains(role.Capabilities[:i], capability) {
				return errors.New("unknown or duplicate capability")
			}
		}
	}
	return nil
}

// Resolve is the union of the capabilities of the given roles, sorted; unknown role IDs grant nothing.
func (s Snapshot) Resolve(roleIDs []string) []Capability {
	set := make(map[Capability]struct{})
	for _, roleID := range roleIDs {
		for _, capability := range s.Capabilities[roleID] {
			set[capability] = struct{}{}
		}
	}
	result := make([]Capability, 0, len(set))
	for capability := range set {
		result = append(result, capability)
	}
	slices.Sort(result)
	return result
}

// Digest fingerprints the custom roles a configuration defines. The built-in roles are left out: they come with
// the software, so an upgrade that changes them changes no configuration's authorization.
func Digest(custom []Role) (string, error) {
	if err := ValidateRoles(custom); err != nil {
		return "", err
	}
	roles := cloneRoles(custom)
	for i := range roles {
		slices.Sort(roles[i].Capabilities)
	}
	slices.SortFunc(roles, func(a, b Role) int { return strings.Compare(a.ID, b.ID) })
	raw, err := json.Marshal(roles)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func NewSnapshot(custom []Role) (Snapshot, error) {
	digest, err := Digest(custom)
	if err != nil {
		return Snapshot{}, err
	}
	roles := cloneRoles(custom)
	slices.SortFunc(roles, func(a, b Role) int { return strings.Compare(a.ID, b.ID) })
	capabilities := make(map[string][]Capability, len(roles)+2)
	// Accounts hold built-in roles too: the bootstrap administrator holds administrator.
	for _, role := range BuiltIns() {
		slices.Sort(role.Capabilities)
		capabilities[role.ID] = role.Capabilities
	}
	for i := range roles {
		slices.Sort(roles[i].Capabilities)
		capabilities[roles[i].ID] = slices.Clone(roles[i].Capabilities)
	}
	return Snapshot{Digest: digest, Roles: roles, Capabilities: capabilities}, nil
}

func cloneRoles(roles []Role) []Role {
	result := make([]Role, len(roles))
	copy(result, roles)
	for i := range result {
		result[i].Capabilities = slices.Clone(result[i].Capabilities)
	}
	return result
}
