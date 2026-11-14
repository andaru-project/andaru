package engine

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/host"
	"github.com/andaru-project/andaru/internal/render"
)

const networkDir = "/run/systemd/network/"

var netplanGenerated = regexp.MustCompile(`^/run/systemd/network/10-netplan-[^/]+$`)

// gateInput is everything that decides whether a rendered generation may go live.
type gateInput struct {
	Takeover    bool
	Declaration json.RawMessage
	Result      render.Result
	Links       []host.LinkStatus
	Managed     []host.Managed
	Facts       host.RealizerFacts
	Baseline    host.Baseline
	// Live is the generation in /run now, nil when nothing of Andaru's is; LiveFiles is its content by path.
	Live      *generation.Generation
	LiveFiles map[string][]byte
	// Ifupdown is what ifupdown configures on a host it still configures alone, with what its reader refused.
	Ifupdown         []host.IfupdownInterface
	IfupdownProblems []host.Problem
	// CurrentDNS is what systemd-resolved uses now, named when a takeover would leave the host without servers.
	CurrentDNS []string
}

// fromIfupdown reports a host that ifupdown, and nothing else, still configures: the Debian installer's.
// Once a generation is live Andaru runs the host, even if a failed confirmation left ifupdown unmasked.
func (in gateInput) fromIfupdown() bool {
	return in.ifupdownHost() && in.Live == nil
}

// ifupdownHost reports a host set up with ifupdown and no netplan, whose ifupdown is not masked.
func (in gateInput) ifupdownHost() bool {
	return in.Baseline.Ifupdown && !in.Baseline.IfupdownMasked && !in.Baseline.Netplan
}

// applyFindings returns the findings that refuse this apply; none means it may proceed.
func applyFindings(in gateInput) []render.Finding {
	var out []render.Finding
	refuse := func(code, path, message string) {
		out = append(out, render.Finding{Severity: "error", Code: code, Path: path, Message: message})
	}

	var unsupported []render.Finding
	baseline := func(path, message string) {
		unsupported = append(unsupported, render.Finding{Severity: "error", Code: "TAKEOVER_UNSUPPORTED_BASELINE", Path: path, Message: message})
	}
	if in.Baseline.Ifupdown && !in.Baseline.IfupdownMasked && in.Baseline.Netplan {
		baseline("/sbin/ifup", "Both netplan and ifupdown configure this host; Andaru can take over one of them, not both.")
	}
	if in.Takeover && in.fromIfupdown() {
		unsupported = append(unsupported, ifupdownFindings(in.IfupdownProblems, in.Facts, in.Managed)...)
		// networkd is stopped, so nothing binds these files now; started, it would apply them beside Andaru's.
		for _, path := range in.Facts.ForeignFiles {
			baseline(path, "This network configuration file was not written by Andaru and would take effect when the takeover starts systemd-networkd.")
		}
	}
	netplanFiles := 0
	if in.Takeover && !in.ifupdownHost() {
		if !in.Baseline.Netplan {
			baseline("/usr/sbin/netplan", "Only a netplan-configured host can be taken over; netplan is not installed.")
		}
		for _, path := range in.Baseline.StrayYAML {
			baseline(path, "netplan configuration outside /etc/netplan would keep generating network files after the takeover.")
		}
		// Through the DNS service the host keeps a resolver whatever netplan's files said.
		resolves := slices.ContainsFunc(in.Result.Files, func(f render.File) bool { return f.Path == "/run/systemd/resolved.conf.d/50-andaru.conf" })
		for _, path := range in.Baseline.StaticDNS {
			if resolves {
				break
			}
			// netplan names its file after its own id, so the interfaces come from networkd's binding, never from the name.
			var bound, missing []string
			for _, m := range in.Managed {
				if m.NetworkFile != path {
					continue
				}
				bound = append(bound, m.Name)
				declares := slices.ContainsFunc(in.Result.Files, func(f render.File) bool {
					return f.Path == networkDir+"50-andaru-"+m.Name+".network" && len(generation.DeclaredDNS([]byte(f.Real()))) > 0
				})
				if !declares {
					missing = append(missing, m.Name)
				}
			}
			if len(bound) > 0 && len(missing) == 0 {
				continue
			}
			switch {
			case len(bound) > 1:
				// Only one segment may declare DNS, so a file shared by several interfaces can never be satisfied.
				baseline(path, "This file sets DNS servers or search domains for several interfaces ("+strings.Join(bound, ", ")+"), which cannot be taken over yet.")
			case len(bound) == 0:
				baseline(path, "This file sets DNS servers or search domains but configures no interface now; the host could lose its resolver.")
			default:
				baseline(path, `This file sets DNS servers or search domains. Declare "dns" on the segment of `+bound[0]+`; without it the host would lose its resolver.`)
			}
		}
	}
	for _, path := range in.Facts.ForeignFiles {
		if strings.HasSuffix(path, ".netdev") && in.Takeover {
			// Andaru deletes only devices it created; one netplan made would linger now and be missing after a reboot.
			baseline(path, "netplan created this device; taking over a host with netplan-made bridges, bonds or VLANs is not supported yet.")
		} else if netplanGenerated.MatchString(path) {
			netplanFiles++
		} else if in.Takeover {
			baseline(path, "This network configuration file was not generated by netplan and would stay in effect after the takeover.")
		}
	}
	recognised := in.Takeover && len(unsupported) == 0
	if !in.Baseline.BootUnit {
		// Without it a confirmed generation is not restored at boot, and after a takeover nothing else configures the network.
		refuse("BOOT_UNIT_DISABLED", "andaru-network-boot.service", "Enable andaru-network-boot.service first: it restores the confirmed network configuration at every boot.")
	}

	for _, f := range in.Result.Findings {
		// A takeover replaces exactly these files, so they are not in the way.
		if f.Severity == "error" && !(f.Code == "FOREIGN_CONFIGURATION" && recognised) {
			out = append(out, f)
		}
	}
	out = append(out, unsupported...)
	switch {
	case in.Takeover && len(in.Facts.ForeignFiles) == 0 && !in.fromIfupdown():
		refuse("TAKEOVER_MISMATCH", "", "There is no earlier network configuration to take over; apply without --takeover.")
	case !in.Takeover && in.fromIfupdown():
		refuse("TAKEOVER_MISMATCH", "", "ifupdown still configures this host; apply with --takeover to replace it.")
	case !in.Takeover && netplanFiles > 0:
		refuse("TAKEOVER_MISMATCH", "", "netplan still configures this host; apply with --takeover to replace it.")
	}

	var declared struct {
		Links []struct {
			ID      string `json:"id"`
			Kind    string `json:"kind"`
			Enabled bool   `json:"enabled"`
		} `json:"links"`
		Segments []struct {
			ID         string `json:"id"`
			Link       string `json:"link"`
			Addressing struct {
				DNS  json.RawMessage `json:"dns"`
				IPv4 struct {
					Mode string `json:"mode"`
				} `json:"ipv4"`
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
		if i < 0 || in.Links[i].Kind != "" || !strings.HasPrefix(m.NetworkFile, networkDir) {
			continue
		}
		connected := slices.ContainsFunc(in.Links[i].Addresses, func(a host.AddressStatus) bool { return a.Scope == "global" })
		if _, ok := rendered(networkDir + "50-andaru-" + m.Name + ".network"); connected && !ok {
			refuse("WOULD_UNCONFIGURE", m.Name, "This interface has an address now and the saved configuration does not configure it; applying could cut off your session. Declare it as an enabled Ethernet link with a segment.")
		}
	}
	if in.Takeover && in.fromIfupdown() {
		enabled := map[string]bool{}
		for _, l := range declared.Links {
			enabled[l.ID] = l.Enabled
		}
		// Confirmation masks ifupdown for every interface, so one left out would never come up again.
		for _, i := range in.Ifupdown {
			if _, ok := rendered(networkDir + "50-andaru-" + i.Name + ".network"); !ok {
				refuse("WOULD_UNCONFIGURE", i.Name, "ifupdown configures this interface and the takeover masks ifupdown, so it would never come up again. Declare it as an enabled Ethernet link with a segment.")
			}
		}
		offered, declaresDNS := false, false
		for _, s := range declared.Segments {
			if !enabled[s.Link] {
				continue // a disabled link's segment is not rendered
			}
			offered = offered || s.Addressing.IPv4.Mode == "dhcp" || s.Addressing.IPv6.Mode == "slaac" || s.Addressing.IPv6.Mode == "dhcp6"
			declaresDNS = declaresDNS || len(s.Addressing.DNS) > 0
		}
		// The DNS service resolves for the host.
		if !offered && !declaresDNS && len(declared.DNS) == 0 {
			servers := "none"
			if len(in.CurrentDNS) > 0 {
				servers = strings.Join(in.CurrentDNS, ", ")
			}
			refuse("DNS_REQUIRED", "", `No interface takes DNS servers from DHCP or router advertisements, so declare "dns" on a segment; the host uses these now: `+servers+".")
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

// ifupdownFindings refuses what makes an ifupdown host impossible to take over safely; the proposal shares it.
func ifupdownFindings(problems []host.Problem, facts host.RealizerFacts, managed []host.Managed) []render.Finding {
	var out []render.Finding
	for _, p := range problems {
		code, message := cmp.Or(p.Code, "TAKEOVER_UNSUPPORTED_BASELINE"), p.Message
		if p.Key != "" {
			message = p.Key + ": " + message
		}
		out = append(out, render.Finding{Severity: "error", Code: code, Path: p.File, Message: message})
	}
	if !facts.ResolvedInCharge {
		out = append(out, render.Finding{Severity: "error", Code: "RESOLVED_NOT_IN_CHARGE", Path: "/etc/resolv.conf",
			Message: "After the takeover systemd-resolved is the host's resolver, and it is not in charge yet. Run: apt install systemd-resolved"})
	}
	for _, m := range managed {
		if !strings.HasPrefix(m.NetworkFile, networkDir+"50-andaru-") {
			out = append(out, render.Finding{Severity: "error", Code: "TAKEOVER_UNSUPPORTED_BASELINE", Path: m.NetworkFile,
				Message: "systemd-networkd configures " + m.Name + " from this file beside ifupdown."})
		}
	}
	return out
}

// dhcp6Gate is what the gates read of a DHCPv6 entry.
type dhcp6Gate struct {
	Segment          string          `json:"segment"`
	PrefixDelegation json.RawMessage `json:"prefixDelegation"`
}
