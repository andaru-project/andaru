package authz

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
