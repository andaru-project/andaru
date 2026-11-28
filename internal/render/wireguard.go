package render

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

type wireguardRef struct {
	Secret     string `json:"secret"`
	Unresolved bool   `json:"unresolved"`
}

type wireguardPeer struct {
	ID                  string        `json:"id"`
	Enabled             bool          `json:"enabled"`
	PublicKey           string        `json:"publicKey"`
	PresharedKeyRef     *wireguardRef `json:"presharedKeyRef"`
	AllowedIPs          []string      `json:"allowedIps"`
	Endpoint            string        `json:"endpoint"`
	PersistentKeepalive int           `json:"persistentKeepalive"`
	RouteAllowedIPs     *bool         `json:"routeAllowedIps"`
}

type wireguardInterface struct {
	ID                  string          `json:"id"`
	Ifname              string          `json:"ifname"`
	Enabled             bool            `json:"enabled"`
	Zone                string          `json:"zone"`
	ListenPort          int             `json:"listenPort"`
	AllowListenPort     *bool           `json:"allowListenPort"`
	AllowListenSegments []string        `json:"allowListenSegments"`
	MTU                 int             `json:"mtu"`
	Addresses           []string        `json:"addresses"`
	PrivateKeyRef       wireguardRef    `json:"privateKeyRef"`
	Peers               []wireguardPeer `json:"peers"`
}

// keyOwner lets systemd-networkd read a WireGuard key and nobody else but root.
const keyOwner = "root:systemd-network"

// wireguards are the enabled interfaces whose name no other kind of device holds.
func (r *renderer) wireguards() []wireguardInterface {
	var out []wireguardInterface
	for _, w := range r.d.VPN.WireGuard {
		if w.Enabled && !r.taken(w.Ifname) {
			out = append(out, w)
		}
	}
	return out
}

// taken says a device other than a WireGuard one already has the name.
func (r *renderer) taken(name string) bool {
	for _, l := range r.in.Links {
		if l.Name == name && l.Kind != "wireguard" {
			return true
		}
	}
	return false
}

// wireguard renders each enabled interface: its device and keys, and its .network with the routes through it.
func (r *renderer) wireguard() {
	for i, w := range r.d.VPN.WireGuard {
		path := fmt.Sprintf("/vpn/wireguard/%d", i)
		if !w.Enabled {
			continue
		}
		if r.taken(w.Ifname) {
			r.find("error", "NAME_COLLISION", path+"/ifname", "Another device already has the interface name "+w.Ifname+".")
			continue
		}
		base := networkDir + w.Ifname
		r.secretFile(base+".key", path+"/privateKeyRef", w.PrivateKeyRef, []string{path})
		netdev := []section{
			{"NetDev", []string{"Name=" + w.Ifname, "Kind=wireguard", fmt.Sprintf("MTUBytes=%d", w.MTU)}},
			{"WireGuard", []string{fmt.Sprintf("ListenPort=%d", w.ListenPort), "PrivateKeyFile=" + base + ".key"}},
		}
		var routed []netip.Prefix
		for j, p := range w.Peers {
			if !p.Enabled {
				continue
			}
			lines := []string{"PublicKey=" + p.PublicKey}
			if p.PresharedKeyRef != nil {
				file := base + "-" + p.ID + ".psk"
				r.secretFile(file, fmt.Sprintf("%s/peers/%d/presharedKeyRef", path, j), *p.PresharedKeyRef, []string{path})
				lines = append(lines, "PresharedKeyFile="+file)
			}
			lines = append(lines, "AllowedIPs="+strings.Join(p.AllowedIPs, ","))
			if p.Endpoint != "" {
				lines = append(lines, "Endpoint="+p.Endpoint)
			}
			if p.PersistentKeepalive > 0 {
				lines = append(lines, fmt.Sprintf("PersistentKeepalive=%d", p.PersistentKeepalive))
			}
			netdev = append(netdev, section{"WireGuardPeer", lines})
			if p.RouteAllowedIPs == nil || *p.RouteAllowedIPs {
				for _, a := range p.AllowedIPs {
					if x, err := netip.ParsePrefix(a); err == nil {
						routed = append(routed, x)
					}
				}
			}
		}
		r.file(base+".netdev", "0644", ini(netdev...), []string{path})

		network := []string{}
		for _, a := range w.Addresses {
			network = append(network, "Address="+a)
		}
		network = append(network, "IPv4Forwarding=yes", "IPv6Forwarding=yes")
		sections := []section{{"Match", []string{"Name=" + w.Ifname}}, {"Network", network}}
		static := r.routes("wireguard", w.ID)
		slices.SortFunc(routed, func(a, b netip.Prefix) int { return a.Compare(b) })
		for _, x := range slices.Compact(routed) {
			// A static route to the same destination says how; the peer's prefix then adds nothing.
			if !slices.ContainsFunc(static, func(s section) bool { return s.lines[0] == "Destination="+x.String() }) {
				sections = append(sections, section{"Route", []string{"Destination=" + x.String()}})
			}
		}
		sources := []string{path}
		if len(static) > 0 {
			sources = append(sources, "/routes")
		}
		r.file(base+".network", "0644", ini(append(sections, static...)...), sources)
	}
}

// secretFile renders a WireGuard key file, shown redacted. A key the input does not hold is an error: the interface
// would come up without it.
func (r *renderer) secretFile(path, field string, ref wireguardRef, sources []string) {
	value, ok := r.in.Secrets[ref.Secret]
	if !ok || ref.Unresolved {
		r.find("error", "SECRET_UNAVAILABLE", field, "Secret "+ref.Secret+" is not available; set it before applying.")
	}
	slices.Sort(sources)
	r.result.Files = append(r.result.Files, File{Path: path, Mode: "0640", Owner: keyOwner, Secret: true,
		Content: redacted + "\n", real: value + "\n", Sources: sources})
}

// wireguardListeners are the input rules that let peers reach each interface's port: from the segments it names,
// or from every uplink.
func (r *renderer) wireguardListeners() []string {
	var out []string
	for _, w := range r.wireguards() {
		if w.AllowListenPort != nil && !*w.AllowListenPort {
			continue
		}
		if len(w.AllowListenSegments) == 0 {
			out = append(out, fmt.Sprintf("iifname @uplinks udp dport %d accept", w.ListenPort))
			continue
		}
		var names []string
		for _, id := range w.AllowListenSegments {
			for _, s := range r.d.Segments {
				if s.ID == id && r.usable[s.Link] {
					names = append(names, r.name[s.Link])
				}
			}
		}
		slices.Sort(names)
		if names = slices.Compact(names); len(names) > 0 {
			out = append(out, fmt.Sprintf("iifname { %s } udp dport %d accept", strings.Join(quoted(names), ", "), w.ListenPort))
		}
	}
	return out
}
