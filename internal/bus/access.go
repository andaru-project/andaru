package bus

import (
	"bytes"
	"strings"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

// access gates connecting, publishing and subscribing by Grants. The broker
// checks a subscription's filter when it is made and every topic it delivers.
type access struct {
	mqtt.HookBase
	grants       map[string]Grants
	authenticate func(username, password []byte) bool
}

func (*access) ID() string { return "access" }

func (*access) Provides(b byte) bool {
	return bytes.Contains([]byte{mqtt.OnConnectAuthenticate, mqtt.OnACLCheck}, []byte{b})
}

func (a *access) OnConnectAuthenticate(cl *mqtt.Client, pk packets.Packet) bool {
	user := string(cl.Properties.Username)
	// MQTT 5 only: older clients cannot be told that a durable publish was refused.
	if _, ok := a.grants[user]; !ok || cl.Properties.ProtocolVersion != 5 {
		return false
	}
	// A session belongs to its user: the client ID is the user name, or the user name, a slash and more. A client
	// taking another user's ID would take its queued messages. No ID holds a colon, which separates the client ID
	// in the store's keys.
	if cl.ID != user && !strings.HasPrefix(cl.ID, user+"/") || strings.Contains(cl.ID, ":") {
		return false
	}
	// A Will is published by the broker, past the publish grants; nothing needs one.
	if pk.Connect.WillFlag {
		return false
	}
	// The store keeps what it holds for a client by topic, and an outbound alias leaves the topic out.
	cl.Properties.Props.TopicAliasMaximum = 0
	if cl.Net.Listener == engineListener {
		return true
	}
	return a.authenticate != nil && a.authenticate(pk.Connect.Username, pk.Connect.Password)
}

func (a *access) OnACLCheck(cl *mqtt.Client, topic string, write bool) bool {
	g := a.grants[string(cl.Properties.Username)]
	filters := g.Subscribe
	if write {
		filters = g.Publish
	}
	for _, f := range filters {
		if covers(f, topic) {
			return true
		}
	}
	return false
}

// covers reports whether grant matches every topic that filter can match.
// filter is a topic or a subscription filter with MQTT wildcards.
func covers(grant, filter string) bool {
	g, f := strings.Split(grant, "/"), strings.Split(filter, "/")
	for i, level := range g {
		if level == "#" {
			return true
		}
		if i >= len(f) {
			return false
		}
		switch {
		case level == "+":
			if f[i] == "#" {
				return false
			}
		case f[i] != level:
			return false
		}
	}
	return len(f) == len(g)
}
