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
	// MQTT 5 only: older clients cannot be told that a durable publish was refused.
	if _, ok := a.grants[string(cl.Properties.Username)]; !ok || cl.Properties.ProtocolVersion != 5 {
		return false
	}
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
