package engine

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/host"
	"github.com/andaru-project/andaru/internal/render"
)

const networkDir = "/run/systemd/network/"

// factoryFile is the image's DHCP on every en* link before setup; a link it configures can carry the session too.
const factoryFile = "/usr/lib/systemd/network/99-andaru-factory.network"

// gateInput is everything that decides whether a rendered generation may go live.
type gateInput struct {
	Declaration json.RawMessage
	Result      render.Result
	Links       []host.LinkStatus
	Managed     []host.Managed
	Facts       host.RealizerFacts
	// BootUnit reports andaru-network-boot.service enabled.
	BootUnit bool
	// Live is the generation in /run now, nil when nothing of Andaru's is; LiveFiles is its content by path.
	Live      *generation.Generation
	LiveFiles map[string][]byte
}

// applyFindings returns the findings that refuse this apply; none means it may proceed.
func applyFindings(in gateInput) []render.Finding {
	var out []render.Finding
	refuse := func(code, path, message string) {
		out = append(out, render.Finding{Severity: "error", Code: code, Path: path, Message: message})
	}
	if !in.BootUnit {
		// Without it a confirmed generation is not restored at boot.
		refuse("BOOT_UNIT_DISABLED", "andaru-network-boot.service", "Enable andaru-network-boot.service first: it restores the confirmed network configuration at every boot.")
	}
	for _, f := range in.Result.Findings {
		if f.Severity == "error" {
			out = append(out, f)
		}
	}

	var declared struct {
		Links []struct {
			Kind    string `json:"kind"`
			Enabled bool   `json:"enabled"`
		} `json:"links"`
		Segments []struct {
			ID         string `json:"id"`
			Addressing struct {
				IPv6 struct {
					Mode string `json:"mode"`
					RA   *struct {
						Mode                        string `json:"mode"`
						Enabled, Managed, OtherInfo bool   // the form saved before modes
					} `json:"ra"`
				} `json:"ipv6"`
			} `json:"addressing"`
		} `json:"segments"`
		DNS        json.RawMessage `json:"dns"`
		DHCP6      []dhcp6Gate     `json:"dhcp6"`
		Management struct {
			Segment string `json:"segment"`
		} `json:"management"`
	}
	// The renderer decoded the same bytes already; an undecodable declaration never gets here.
	_ = json.Unmarshal(in.Declaration, &declared)
	const filter = " It is not supported by the packet filter yet."
	for i, l := range declared.Links {
		if l.Kind == "pppoe" && l.Enabled {
			refuse("APPLY_UNSUPPORTED", fmt.Sprintf("/links/%d", i), "PPPoE links cannot be applied yet."+filter)
		}
	}
	for i, s := range declared.Segments {
		ra := s.Addressing.IPv6.RA
		advertised := ra != nil && ra.Mode != "disabled" && (ra.Mode != "" || ra.Enabled)
		served := slices.IndexFunc(declared.DHCP6, func(e dhcp6Gate) bool { return e.Segment == s.ID }) >= 0
		if ra != nil && !served && (slices.Contains([]string{"managed", "assisted", "stateless"}, ra.Mode) || ra.Mode == "" && ra.Enabled && (ra.Managed || ra.OtherInfo)) {
			refuse("APPLY_UNSUPPORTED", fmt.Sprintf("/segments/%d/addressing/ipv6/ra/mode", i), "This router advertisement mode sends clients to a DHCPv6 server: it needs a DHCPv6 entry for this segment.")
		} else if declared.Management.Segment == "" && advertised {
			// IPv6 forwarding stays off without the filter: clients would send everything to a router that drops it.
			refuse("APPLY_UNSUPPORTED", fmt.Sprintf("/segments/%d/addressing/ipv6/ra/mode", i), "Router advertisements need the packet filter, which needs a management segment. Set management.segment.")
		}
		if declared.Management.Segment == "" && s.Addressing.IPv6.Mode == "delegated" && !advertised {
			refuse("APPLY_UNSUPPORTED", fmt.Sprintf("/segments/%d/addressing/ipv6/mode", i), "A delegated prefix is routed, which needs the packet filter and so a management segment. Set management.segment.")
		}
	}

	var dns struct {
		VirtualAddresses []string `json:"virtualAddresses"`
	}
	_ = json.Unmarshal(declared.DNS, &dns)
	if declared.Management.Segment == "" && len(dns.VirtualAddresses) > 0 {
		// A virtual address is on no segment: only the filter decides who may ask the DNS service there.
		refuse("APPLY_UNSUPPORTED", "/dns/virtualAddresses", "The DNS service on virtual addresses needs the packet filter, which needs a management segment. Set management.segment.")
	}
	for i, e := range declared.DHCP6 {
		if declared.Management.Segment == "" && len(e.PrefixDelegation) > 0 && string(e.PrefixDelegation) != "null" {
			refuse("APPLY_UNSUPPORTED", fmt.Sprintf("/dhcp6/%d/prefixDelegation", i), "Delegated prefixes are routed, which needs the packet filter and so a management segment. Set management.segment.")
		}
	}

	rendered := func(path string) (render.File, bool) {
		i := slices.IndexFunc(in.Result.Files, func(f render.File) bool { return f.Path == path })
		if i < 0 {
			return render.File{}, false
		}
		return in.Result.Files[i], true
	}
	for _, m := range in.Managed {
		i := slices.IndexFunc(in.Links, func(l host.LinkStatus) bool { return l.Name == m.Name })
		// Virtual devices come and go with generations; only a physical interface can carry the operator's session away.
		if i < 0 || in.Links[i].Kind != "" || !strings.HasPrefix(m.NetworkFile, networkDir) && m.NetworkFile != factoryFile {
			continue
		}
		connected := slices.ContainsFunc(in.Links[i].Addresses, func(a host.AddressStatus) bool { return a.Scope == "global" })
		if _, ok := rendered(networkDir + "50-andaru-" + m.Name + ".network"); connected && !ok {
			refuse("WOULD_UNCONFIGURE", m.Name, "This interface has an address now and the saved configuration does not configure it; applying could cut off your session. Declare it as an enabled Ethernet link with a segment.")
		}
	}
	for _, path := range slices.Sorted(maps.Keys(in.LiveFiles)) {
		f, ok := rendered(path)
		if ok && strings.HasSuffix(path, ".netdev") && f.Real() != string(in.LiveFiles[path]) {
			refuse("NETDEV_CHANGE_UNSUPPORTED", path, "This device already exists and systemd-networkd does not change an existing device; changing it cannot be applied yet. Remove it in one apply and add it again in the next.")
		}
	}
	return out
}

// dhcp6Gate is what the gates read of a DHCPv6 entry.
type dhcp6Gate struct {
	Segment          string          `json:"segment"`
	PrefixDelegation json.RawMessage `json:"prefixDelegation"`
}
