package bus

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

type listenerKind uint8

const (
	listenerAuth  listenerKind = iota // Options.Listeners (opt.Grants + opt.Authenticate)
	listenerPeer                      // Options.PeerListeners (opt.Grants + opt.PeerUser)
	listenerToken                     // Options.TokenListeners (b.clients + constant-time token)
)

type dynamicClient struct {
	token  string
	grants Grants
}

var (
	addonSlugPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

	// Defense-in-depth mirror of internal/addon manifest bus rules: because leading wildcards (+, #)
	// are forbidden, comparing the first topic level is exact for every <root>/# forbidden namespace.
	forbiddenAddonPublishRoots = []string{
		"notification", "delivery", "alias-fetch", "alias-result",
		"config", "apply", "notifications",
	}
	forbiddenAddonSubscribeRoots = []string{
		"notification", "delivery", "alias-fetch",
	}
)

func validAddonUser(user string) bool {
	slug, ok := strings.CutPrefix(user, "addon.")
	return ok && addonSlugPattern.MatchString(slug)
}

func validAddonFilter(user, filter string, publish bool) bool {
	if !validFilter(filter) || len(filter) > MaxTopic {
		return false
	}
	first, _, _ := strings.Cut(filter, "/")
	if first == "+" || first == "#" {
		return false
	}
	if publish {
		return filter == "notification/"+user || !slices.Contains(forbiddenAddonPublishRoots, first)
	}
	return !slices.Contains(forbiddenAddonSubscribeRoots, first)
}

// SetClient registers or updates a dynamic token-authenticated add-on client ("addon.<slug>") on TokenListeners,
// closing any existing TokenListener connections for user so rotated tokens or changed grants take effect at once.
func (b *Bus) SetClient(user, token string, grants Grants) error {
	if !validAddonUser(user) {
		return fmt.Errorf("bus: invalid add-on client user %q", user)
	}
	if token == "" || strings.ContainsRune(token, 0) {
		return errors.New("bus: add-on client token must be non-empty without NUL bytes")
	}
	for _, filter := range grants.Publish {
		if !validAddonFilter(user, filter, true) {
			return fmt.Errorf("bus: %s publish grant %q is not allowed", user, filter)
		}
	}
	for _, filter := range grants.Subscribe {
		if !validAddonFilter(user, filter, false) {
			return fmt.Errorf("bus: %s subscribe grant %q is not allowed", user, filter)
		}
	}
	b.mu.Lock()
	select {
	case <-b.closed:
		b.mu.Unlock()
		return errors.New("bus: closed")
	default:
	}
	b.clients[user] = dynamicClient{
		token: token,
		grants: Grants{
			Publish:   slices.Clone(grants.Publish),
			Subscribe: slices.Clone(grants.Subscribe),
		},
	}
	toClose := b.tokenConnsLocked(user)
	b.mu.Unlock()
	for _, c := range toClose {
		c.close()
	}
	return nil
}

// RemoveClient revokes a dynamic add-on client and immediately closes any existing TokenListener connections for user.
func (b *Bus) RemoveClient(user string) {
	b.mu.Lock()
	delete(b.clients, user)
	toClose := b.tokenConnsLocked(user)
	b.mu.Unlock()
	for _, c := range toClose {
		c.close()
	}
}

// tokenConnsLocked returns active TokenListener connections authenticated as user. Caller holds b.mu.
func (b *Bus) tokenConnsLocked(user string) []*conn {
	var out []*conn
	for c := range b.conns {
		if c.kind == listenerToken && c.user == user {
			out = append(out, c)
		}
	}
	return out
}
