package authz

import "errors"

type ActorKind string

const (
	ActorSystem  ActorKind = "system"
	ActorAccount ActorKind = "account"
	ActorToken   ActorKind = "token"
)

type Actor struct {
	Kind ActorKind
	ID   string
}

// CanonicalLabel returns the bounded label persisted for a verified actor.
func (a Actor) CanonicalLabel() (string, error) {
	switch a.Kind {
	case ActorSystem:
		if a.ID == "root" {
			return "system.root", nil
		}
	case ActorAccount:
		if validAccountID(a.ID) {
			return "account:" + a.ID, nil
		}
	case ActorToken:
		if validTokenID(a.ID) {
			return "token:" + a.ID, nil
		}
	}
	return "", errors.New("invalid verified actor")
}

func validAccountID(id string) bool {
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

func validTokenID(id string) bool {
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
