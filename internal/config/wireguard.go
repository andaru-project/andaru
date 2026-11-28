package config

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// vpnSettings are the native VPN interfaces Andaru renders for systemd-networkd.
type vpnSettings struct {
	WireGuard []wireguardInterface `json:"wireguard,omitempty"`
	IPsec     []ipsecConnection    `json:"ipsec,omitempty"`
}

// wireguardInterface is one WireGuard interface. PublicKey is derived from the private key whenever a write sets
// or binds it, so it is never taken from a client.
type wireguardInterface struct {
	identity
	Ifname              string          `json:"ifname"`
	Enabled             bool            `json:"enabled"`
	Zone                string          `json:"zone,omitempty"`
	ListenPort          int             `json:"listenPort"`
	AllowListenPort     *bool           `json:"allowListenPort,omitempty"`
	AllowListenSegments []string        `json:"allowListenSegments,omitempty"`
	MTU                 int             `json:"mtu,omitempty"`
	Addresses           []string        `json:"addresses,omitempty"`
	PrivateKeyRef       secretReference `json:"privateKeyRef"`
	PublicKey           string          `json:"publicKey,omitempty"`
	Peers               []wireguardPeer `json:"peers,omitempty"`
}

type wireguardPeer struct {
	identity
	Enabled             bool             `json:"enabled"`
	PublicKey           string           `json:"publicKey"`
	PresharedKeyRef     *secretReference `json:"presharedKeyRef,omitempty"`
	AllowedIPs          []string         `json:"allowedIps,omitempty"`
	Endpoint            string           `json:"endpoint,omitempty"`
	PersistentKeepalive int              `json:"persistentKeepalive,omitempty"`
	RouteAllowedIPs     *bool            `json:"routeAllowedIps,omitempty"`
}

const defaultWireGuardMTU = 1420

func (w *wireguardInterface) secrets() []channelSecret {
	out := []channelSecret{{"/privateKeyRef", &w.PrivateKeyRef}}
	for i := range w.Peers {
		if w.Peers[i].PresharedKeyRef != nil {
			out = append(out, channelSecret{fmt.Sprintf("/peers/%d/presharedKeyRef", i), w.Peers[i].PresharedKeyRef})
		}
	}
	return out
}

// wireguardSecretFields are the secret references a written interface holds. Only those present are listed: a peer's
// left-out reference was settled by prepareWireGuard, by peer ID rather than by position.
func wireguardSecretFields(value map[string]any) []secretField {
	out := []secretField{{"/privateKeyRef", value, "privateKeyRef"}}
	peers, _ := value["peers"].([]any)
	for i, p := range peers {
		if peer, ok := p.(map[string]any); ok && peer["presharedKeyRef"] != nil {
			out = append(out, secretField{fmt.Sprintf("/peers/%d/presharedKeyRef", i), peer, "presharedKeyRef"})
		}
	}
	return out
}

// secretRefMap renders a stored secret reference back into a write-side map, keeping any unresolved marker.
func secretRefMap(ref *secretReference) map[string]any {
	m := map[string]any{"secret": ref.Secret}
	if ref.Unresolved {
		m["unresolved"] = true
	}
	return m
}

// prepareWireGuard settles a written interface before its secrets are read: peers without an ID are given one, a
// private key left out or written as {} keeps the stored one, a pre-shared key written as {} keeps the peer's (by
// peer ID) while one left out is dropped, and publicKey is the stored one until checkWireGuardKeys derives it.
func prepareWireGuard(value map[string]any, old any, create bool) (string, error) {
	before, _ := old.(*wireguardInterface)
	empty := func(v any) bool { m, ok := v.(map[string]any); return v == nil || ok && len(m) == 0 }
	keptPrivate := false
	if empty(value["privateKeyRef"]) {
		if create || before == nil {
			return "/privateKeyRef", errors.New("A private key is required.")
		}
		value["privateKeyRef"] = secretRefMap(&before.PrivateKeyRef)
		keptPrivate = true
	} else if before != nil {
		if m, ok := value["privateKeyRef"].(map[string]any); ok {
			if _, hasSet := m["set"]; !hasSet && m["secret"] == before.PrivateKeyRef.Secret {
				keptPrivate = true
			}
		}
	}
	delete(value, "publicKey")
	if keptPrivate && before != nil && before.PublicKey != "" {
		value["publicKey"] = before.PublicKey
	}
	peers, _ := value["peers"].([]any)
	for _, p := range peers {
		peer, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if _, has := peer["id"]; !has {
			id, err := newID("vpn.wireguard.peers")
			if err != nil {
				return "", err
			}
			peer["id"] = id
		}
		if ref, has := peer["presharedKeyRef"]; has && empty(ref) {
			delete(peer, "presharedKeyRef")
			if before == nil {
				continue
			}
			if j := slices.IndexFunc(before.Peers, func(o wireguardPeer) bool { return o.ID == peer["id"] }); j >= 0 && before.Peers[j].PresharedKeyRef != nil {
				peer["presharedKeyRef"] = secretRefMap(before.Peers[j].PresharedKeyRef)
			}
		}
	}
	return "", nil
}

// wireguardKey decodes a WireGuard key: exactly 32 bytes in standard base64, in its one spelling.
func wireguardKey(s string) ([]byte, bool) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(raw) != 32 || base64.StdEncoding.EncodeToString(raw) != s {
		return nil, false
	}
	return raw, true
}

// WireGuardPublicKey derives the base64 public key of a base64 WireGuard private key.
func WireGuardPublicKey(private string) (string, error) {
	raw, ok := wireguardKey(private)
	if !ok || !slices.ContainsFunc(raw, func(b byte) bool { return b != 0 }) {
		return "", errors.New("expected a non-zero 32-byte key in base64")
	}
	key, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

// validEndpointHost accepts a unicast/non-4in6 IP address (bracketed iff IPv6 when bracketed) or a DNS hostname.
func validEndpointHost(host string) bool {
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return a.Zone() == "" && !a.Is4In6()
	}
	// Hostname: dot-separated ASCII labels of [A-Za-z0-9-], no leading/trailing hyphen or empty label.
	if strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// validEndpoint accepts host:port or [IPv6]:port with a canonical port of 1 to 65535 and no space/control/format chars.
func validEndpoint(s string) bool {
	if strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
		return false
	}
	host, port, err := net.SplitHostPort(s)
	n, perr := strconv.Atoi(port)
	if err != nil || perr != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port || host == "" || strings.ContainsAny(host, "[]") {
		return false
	}
	bracketed := strings.HasPrefix(s, "[")
	if a, err := netip.ParseAddr(host); err == nil {
		return a.Zone() == "" && !a.Is4In6() && bracketed == a.Is6()
	}
	return !bracketed && !strings.Contains(host, ":") && validEndpointHost(host)
}

func validateWireGuard(d *declaration) []FieldError {
	var out []FieldError
	bad := func(path, msg string) {
		if len(out) < 100 {
			out = append(out, FieldError{path, msg})
		}
	}
	zones := map[string]bool{}
	for _, z := range d.Zones {
		zones[z.ID] = true
	}
	segments := map[string]bool{}
	for _, s := range d.Segments {
		segments[s.ID] = true
	}
	seen := map[string]bool{}
	for i := range d.VPN.WireGuard {
		w := &d.VPN.WireGuard[i]
		p := fmt.Sprintf("/vpn/wireguard/%d", i)
		if seen[w.ID] {
			bad(p+"/id", "Duplicate object identity.")
		}
		if invalidText(w.Name, true) {
			bad(p+"/name", "Control or format characters are not allowed.")
		}
		if seen["name:"+w.Name] {
			bad(p+"/name", "Name already exists in this collection.")
		}
		if seen["if:"+w.Ifname] {
			bad(p+"/ifname", "Another interface has this name.")
		}
		if w.Enabled {
			portKey := fmt.Sprintf("port:%d", w.ListenPort)
			if seen[portKey] {
				bad(p+"/listenPort", "Another enabled WireGuard interface uses this listen port.")
			}
			seen[portKey] = true
		}
		seen[w.ID], seen["name:"+w.Name], seen["if:"+w.Ifname] = true, true, true
		if w.Zone != "" && !zones[w.Zone] {
			bad(p+"/zone", "Unknown zone.")
		} else if w.Zone == "" && w.Enabled {
			bad(p+"/zone", "An enabled interface needs a zone.")
		}
		for j, s := range w.AllowListenSegments {
			if !segments[s] {
				bad(fmt.Sprintf("%s/allowListenSegments/%d", p, j), "Expected a segment of this configuration.")
			} else if slices.Index(w.AllowListenSegments, s) != j {
				bad(fmt.Sprintf("%s/allowListenSegments/%d", p, j), "Duplicate segment.")
			}
		}
		if len(w.Addresses) == 0 && w.Enabled {
			bad(p+"/addresses", "An enabled interface needs an address.")
		}
		addresses := map[netip.Prefix]bool{}
		for j, a := range w.Addresses {
			x, err := netip.ParsePrefix(a)
			switch {
			case err != nil || x.Addr().Is4In6() || x.Addr().Zone() != "" || !validUnicast(x.Addr()):
				bad(fmt.Sprintf("%s/addresses/%d", p, j), "Expected a unicast IPv4 or IPv6 address with its prefix length.")
			case addresses[x]:
				bad(fmt.Sprintf("%s/addresses/%d", p, j), "Duplicate address.")
			}
			addresses[x] = true
		}
		if _, ok := wireguardKey(w.PublicKey); w.PublicKey != "" && !ok {
			bad(p+"/publicKey", "Expected a 32-byte key in base64.")
		}
		peerSeen := map[string]bool{}
		routed := map[netip.Prefix]bool{}
		for j := range w.Peers {
			peer := &w.Peers[j]
			pp := fmt.Sprintf("%s/peers/%d", p, j)
			if peerSeen[peer.ID] {
				bad(pp+"/id", "Duplicate peer identity.")
			}
			if invalidText(peer.Name, true) {
				bad(pp+"/name", "Control or format characters are not allowed.")
			}
			if peerSeen["name:"+peer.Name] {
				bad(pp+"/name", "Another peer of this interface has this name.")
			}
			if _, ok := wireguardKey(peer.PublicKey); !ok {
				bad(pp+"/publicKey", "Expected a 32-byte key in base64.")
			} else if peerSeen["key:"+peer.PublicKey] {
				bad(pp+"/publicKey", "Another peer of this interface has this public key.")
			} else if peer.PublicKey == w.PublicKey {
				bad(pp+"/publicKey", "A peer cannot have the interface's own public key.")
			}
			peerSeen[peer.ID], peerSeen["name:"+peer.Name], peerSeen["key:"+peer.PublicKey] = true, true, true
			if len(peer.AllowedIPs) == 0 && peer.Enabled {
				bad(pp+"/allowedIps", "An enabled peer needs an allowed IP prefix.")
			}
			var endpointAddr netip.Addr
			if peer.Endpoint != "" {
				if !validEndpoint(peer.Endpoint) {
					bad(pp+"/endpoint", "Expected host:port or [IPv6]:port with a port of 1 to 65535.")
				} else if host, _, err := net.SplitHostPort(peer.Endpoint); err == nil {
					if a, err := netip.ParseAddr(host); err == nil {
						endpointAddr = a
					}
				}
			}
			routeAllowed := peer.RouteAllowedIPs == nil || *peer.RouteAllowedIPs
			peerPrefixes := map[netip.Prefix]bool{}
			for k, a := range peer.AllowedIPs {
				x, err := netip.ParsePrefix(a)
				if err != nil || x.Addr().Is4In6() || x.Addr().Zone() != "" {
					bad(fmt.Sprintf("%s/allowedIps/%d", pp, k), "Expected an IPv4 or IPv6 prefix in CIDR notation.")
					continue
				}
				x = x.Masked()
				if peerPrefixes[x] {
					bad(fmt.Sprintf("%s/allowedIps/%d", pp, k), "Duplicate allowed IP prefix.")
					continue
				}
				peerPrefixes[x] = true
				// WireGuard routes a prefix to one peer: a second enabled peer would silently take it over.
				if peer.Enabled && routed[x] {
					bad(fmt.Sprintf("%s/allowedIps/%d", pp, k), "Another enabled peer of this interface has this prefix.")
				}
				if peer.Enabled && routeAllowed && endpointAddr.IsValid() && x.Contains(endpointAddr) {
					bad(fmt.Sprintf("%s/allowedIps/%d", pp, k), "Allowed IP prefix covers the peer's own endpoint while routeAllowedIps is enabled; disable routeAllowedIps or narrow allowedIps.")
				}
				if peer.Enabled {
					routed[x] = true
				}
			}
		}
	}
	return out
}

// canonicalizeWireGuard stores one spelling of a validated interface: defaults set, addresses in their one form and
// allowed IPs masked.
func canonicalizeWireGuard(w *wireguardInterface) {
	yes := true
	if w.AllowListenPort == nil {
		w.AllowListenPort = &yes
	}
	if w.MTU == 0 {
		w.MTU = defaultWireGuardMTU
	}
	if len(w.AllowListenSegments) == 0 {
		w.AllowListenSegments = nil
	}
	slices.Sort(w.AllowListenSegments)
	for i, a := range w.Addresses {
		if x, err := netip.ParsePrefix(a); err == nil {
			w.Addresses[i] = x.String()
		}
	}
	if len(w.Peers) == 0 {
		w.Peers = nil
	}
	slices.SortFunc(w.Peers, func(a, b wireguardPeer) int { return strings.Compare(a.ID, b.ID) })
	for i := range w.Peers {
		peer := &w.Peers[i]
		if peer.RouteAllowedIPs == nil {
			t := true
			peer.RouteAllowedIPs = &t
		}
		for j, a := range peer.AllowedIPs {
			if x, err := netip.ParsePrefix(a); err == nil {
				peer.AllowedIPs[j] = x.Masked().String()
			}
		}
	}
}

// checkWireGuardKeys checks the keys an interface written in this batch, or whose secret it changed, holds; it
// derives publicKey from the private key. derive false only checks the stored publicKey, as a restore does.
func checkWireGuardKeys(d *declaration, secrets secretDocument, written map[objectKey][]byte, changed map[string]bool, derive bool) []FieldError {
	var out []FieldError
	value := func(id string, ref *secretReference) (string, bool) {
		if ref == nil || ref.Unresolved {
			return "", false
		}
		_, w := written[objectKey{"vpn.wireguard", id}]
		if !w && !changed["core\x00"+ref.Secret] {
			return "", false
		}
		return secrets.Scopes["core"][ref.Secret].Value, true
	}
	for i := range d.VPN.WireGuard {
		w := &d.VPN.WireGuard[i]
		p := fmt.Sprintf("/vpn/wireguard/%d", i)
		if v, ok := value(w.ID, &w.PrivateKeyRef); ok {
			public, err := WireGuardPublicKey(v)
			switch {
			case err != nil:
				out = append(out, FieldError{p + "/privateKeyRef", "Expected the secret to hold a non-zero 32-byte key in base64."})
				continue
			case derive:
				w.PublicKey = public
			case w.PublicKey != public:
				out = append(out, FieldError{p + "/publicKey", "The public key is not the private key's."})
			}
			for j := range w.Peers {
				if w.Peers[j].PublicKey == w.PublicKey {
					out = append(out, FieldError{fmt.Sprintf("%s/peers/%d/publicKey", p, j), "A peer cannot have the interface's own public key."})
				}
			}
		}
		for j := range w.Peers {
			if v, ok := value(w.ID, w.Peers[j].PresharedKeyRef); ok {
				if _, valid := wireguardKey(v); !valid {
					out = append(out, FieldError{fmt.Sprintf("%s/peers/%d/presharedKeyRef", p, j), "Expected the secret to hold a 32-byte key in base64."})
				}
			}
		}
	}
	return out
}

// remapWireGuardPeerPaths translates sorted peer indices in checkWireGuardKeys errors back to each peer's submitted order.
func remapWireGuardPeerPaths(errs []FieldError, d *declaration, writes map[objectKey][]byte) []FieldError {
	if len(errs) == 0 {
		return errs
	}
	for i := range errs {
		rest, ok := strings.CutPrefix(errs[i].Path, "/vpn/wireguard/")
		if !ok {
			continue
		}
		parts := strings.SplitN(rest, "/", 4)
		if len(parts) < 3 || parts[1] != "peers" {
			continue
		}
		wi, err1 := strconv.Atoi(parts[0])
		pi, err2 := strconv.Atoi(parts[2])
		if err1 != nil || err2 != nil || wi < 0 || wi >= len(d.VPN.WireGuard) || pi < 0 || pi >= len(d.VPN.WireGuard[wi].Peers) {
			continue
		}
		w := &d.VPN.WireGuard[wi]
		raw, ok := writes[objectKey{"vpn.wireguard", w.ID}]
		if !ok {
			continue
		}
		var submitted struct {
			Peers []struct {
				ID string `json:"id"`
			} `json:"peers"`
		}
		if json.Unmarshal(raw, &submitted) != nil {
			continue
		}
		orig := slices.IndexFunc(submitted.Peers, func(p struct {
			ID string `json:"id"`
		}) bool {
			return p.ID == w.Peers[pi].ID
		})
		if orig >= 0 && orig != pi {
			suffix := ""
			if len(parts) == 4 {
				suffix = "/" + parts[3]
			}
			errs[i].Path = fmt.Sprintf("/vpn/wireguard/%d/peers/%d%s", wi, orig, suffix)
		}
	}
	return errs
}

// checkAllWireGuardKeys checks every interface's resolved keys, as a restore must.
func checkAllWireGuardKeys(d *declaration, secrets secretDocument) []FieldError {
	written := map[objectKey][]byte{}
	for _, w := range d.VPN.WireGuard {
		written[objectKey{"vpn.wireguard", w.ID}] = nil
	}
	return checkWireGuardKeys(d, secrets, written, nil, false)
}

// unresolvedWireGuard is where an enabled interface would run without a key: its own, or an enabled peer's.
func unresolvedWireGuard(d *declaration) []FieldError {
	var out []FieldError
	for i := range d.VPN.WireGuard {
		w := &d.VPN.WireGuard[i]
		if !w.Enabled {
			continue
		}
		if w.PrivateKeyRef.Unresolved {
			out = append(out, FieldError{
				Path:    fmt.Sprintf("/vpn/wireguard/%d/privateKeyRef", i),
				Message: "Secret " + w.PrivateKeyRef.Secret + " is unresolved; explicitly bind or set it before applying.",
			})
		}
		for j := range w.Peers {
			p := &w.Peers[j]
			if p.Enabled && p.PresharedKeyRef != nil && p.PresharedKeyRef.Unresolved {
				out = append(out, FieldError{
					Path:    fmt.Sprintf("/vpn/wireguard/%d/peers/%d/presharedKeyRef", i, j),
					Message: "Secret " + p.PresharedKeyRef.Secret + " is unresolved; explicitly bind or set it before applying.",
				})
			}
		}
	}
	return out
}

// realizedWireGuardSecrets are the keys the enabled interfaces and their enabled peers render.
func realizedWireGuardSecrets(d *declaration, secrets secretDocument, into map[string]string) {
	for i := range d.VPN.WireGuard {
		w := &d.VPN.WireGuard[i]
		if !w.Enabled {
			continue
		}
		if !w.PrivateKeyRef.Unresolved {
			into[w.PrivateKeyRef.Secret] = secrets.Scopes["core"][w.PrivateKeyRef.Secret].Value
		}
		for j := range w.Peers {
			p := &w.Peers[j]
			if p.Enabled && p.PresharedKeyRef != nil && !p.PresharedKeyRef.Unresolved {
				into[p.PresharedKeyRef.Secret] = secrets.Scopes["core"][p.PresharedKeyRef.Secret].Value
			}
		}
	}
}

// WireGuardInterface is a saved interface's public part, for its status and its peers' configuration.
type WireGuardInterface struct {
	ID, Name, Ifname, PublicKey string
	Enabled                     bool
	ListenPort                  int
	Addresses                   []string
	Peers                       []WireGuardPeer
}

// WireGuardPeer is a saved peer's public part. PresharedKey says the peer has one, Unresolved that it is not stored.
type WireGuardPeer struct {
	ID, Name, PublicKey, Endpoint string
	Enabled                       bool
	AllowedIPs                    []string
	PersistentKeepalive           int
	PresharedKey, Unresolved      bool
}

// ErrPresharedKeyUnavailable means the peer has no pre-shared key, or one a restore left unresolved.
var ErrPresharedKeyUnavailable = errors.New("the peer's pre-shared key is not set")

// WireGuardInterfaces returns the saved head's WireGuard interfaces, keys left out.
func (s *Store) WireGuardInterfaces(ctx context.Context) (result []WireGuardInterface, err error) {
	err = s.read(ctx, "", nil, func(state savedState, _ VersionInfo) error {
		for _, w := range state.Config.VPN.WireGuard {
			out := WireGuardInterface{ID: w.ID, Name: w.Name, Ifname: w.Ifname, PublicKey: w.PublicKey, Enabled: w.Enabled, ListenPort: w.ListenPort, Addresses: slices.Clone(w.Addresses)}
			for _, p := range w.Peers {
				peer := WireGuardPeer{ID: p.ID, Name: p.Name, PublicKey: p.PublicKey, Endpoint: p.Endpoint, Enabled: p.Enabled, AllowedIPs: slices.Clone(p.AllowedIPs), PersistentKeepalive: p.PersistentKeepalive}
				if p.PresharedKeyRef != nil {
					peer.PresharedKey, peer.Unresolved = true, p.PresharedKeyRef.Unresolved
				}
				out.Peers = append(out.Peers, peer)
			}
			result = append(result, out)
		}
		return nil
	})
	return
}

// ErrWireGuardKeyUnresolved means the interface's private key is unresolved, so no public key is available.
var ErrWireGuardKeyUnresolved = errors.New("the WireGuard interface private key is unresolved")

// ErrPeerNotFound means the named WireGuard peer does not exist on the interface.
var ErrPeerNotFound = errors.New("the WireGuard peer does not exist")

// WireGuardPeerExport atomically reads one interface, one peer, and optionally its pre-shared key from a single saved head.
func (s *Store) WireGuardPeerExport(ctx context.Context, id, peerID string, includePSK bool) (iface WireGuardInterface, peer WireGuardPeer, psk string, err error) {
	err = s.read(ctx, "", nil, func(state savedState, _ VersionInfo) error {
		v, ok := objects(&state.Config)[objectKey{"vpn.wireguard", id}].(*wireguardInterface)
		if !ok {
			return fault("NOT_FOUND", "", "Object does not exist.")
		}
		j := slices.IndexFunc(v.Peers, func(p wireguardPeer) bool { return p.ID == peerID })
		if j < 0 {
			return ErrPeerNotFound
		}
		if v.PublicKey == "" {
			return ErrWireGuardKeyUnresolved
		}
		p := v.Peers[j]
		if includePSK {
			ref := p.PresharedKeyRef
			if ref == nil || ref.Unresolved {
				return ErrPresharedKeyUnavailable
			}
			psk = state.Secrets.Scopes["core"][ref.Secret].Value
		}
		iface = WireGuardInterface{ID: v.ID, Name: v.Name, Ifname: v.Ifname, PublicKey: v.PublicKey, Enabled: v.Enabled, ListenPort: v.ListenPort, Addresses: slices.Clone(v.Addresses)}
		peer = WireGuardPeer{ID: p.ID, Name: p.Name, PublicKey: p.PublicKey, Endpoint: p.Endpoint, Enabled: p.Enabled, AllowedIPs: slices.Clone(p.AllowedIPs), PersistentKeepalive: p.PersistentKeepalive}
		if p.PresharedKeyRef != nil {
			peer.PresharedKey, peer.Unresolved = true, p.PresharedKeyRef.Unresolved
		}
		return nil
	})
	return
}
