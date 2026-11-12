package main

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/andaru-project/andaru/internal/config"
)

// family is one address family as the operator chose it.
type family struct {
	Mode      string
	Addresses []string
	Gateway   string
}

type adapterAnswer struct {
	Keep bool
	Name string
	// Zone is wan or lan.
	Zone     string
	IPv4     family
	IPv6Mode string
}

type dnsAnswer struct{ Servers, SearchDomains []string }

// answers is everything the operator decides. DNS nil means: use what DHCP and router advertisements offer.
type answers struct {
	Adapters map[string]adapterAnswer // by the link's localId
	DNS      *dnsAnswer
	Timeout  int
	// Management is the link localId whose segment keeps SSH open whatever the rules say.
	Management string
	// DHCP is the operator's yes to each DHCP offer, by the offer's segment reference.
	DHCP map[string]bool
}

// service reports a proposal that resolves through the DNS service, whose upstreams are then the host's servers.
func service(p proposal) bool {
	return slices.ContainsFunc(p.Operations, func(op config.Operation) bool { return op.Collection == "dns" })
}

type segmentValue struct {
	Addressing struct {
		IPv4 struct {
			Mode      string   `json:"mode"`
			Addresses []string `json:"addresses"`
			Gateway   string   `json:"gateway"`
		} `json:"ipv4"`
		IPv6 struct {
			Mode string `json:"mode"`
		} `json:"ipv6"`
		DNS *struct {
			Servers       []string `json:"servers"`
			SearchDomains []string `json:"searchDomains"`
		} `json:"dns"`
	} `json:"addressing"`
	Link string `json:"link"`
	Zone string `json:"zone"`
}

// defaultAnswers are the detected values: accepting them reproduces the proposal exactly.
func defaultAnswers(p proposal) answers {
	a := answers{Adapters: map[string]adapterAnswer{}, Timeout: 120}
	names := map[string]string{}
	for _, op := range p.Operations {
		if op.Collection == "links" {
			var v struct{ Name string }
			_ = json.Unmarshal(op.Value, &v)
			names[op.LocalID] = v.Name
		}
	}
	linkOf := map[string]string{}
	management := ""
	for _, op := range p.Operations {
		switch op.Collection {
		case "segments":
			var v segmentValue
			_ = json.Unmarshal(op.Value, &v)
			linkOf[op.LocalID] = strings.TrimPrefix(v.Link, "@")
		case "management":
			var v struct{ Segment string }
			_ = json.Unmarshal(op.Value, &v)
			management = strings.TrimPrefix(v.Segment, "@")
		case "dns":
			var v struct {
				Upstreams     []struct{ Address string }
				SearchDomains []string
			}
			_ = json.Unmarshal(op.Value, &v)
			if len(v.Upstreams)+len(v.SearchDomains) > 0 {
				a.DNS = &dnsAnswer{SearchDomains: v.SearchDomains}
				for _, u := range v.Upstreams {
					a.DNS.Servers = append(a.DNS.Servers, u.Address)
				}
			}
		}
	}
	a.Management = linkOf[management]
	for _, op := range p.Operations {
		if op.Collection != "segments" {
			continue
		}
		var v segmentValue
		_ = json.Unmarshal(op.Value, &v)
		link := strings.TrimPrefix(v.Link, "@")
		ip4 := v.Addressing.IPv4
		a.Adapters[link] = adapterAnswer{Keep: true, Name: names[link], Zone: strings.TrimPrefix(v.Zone, "@"),
			IPv4: family{ip4.Mode, ip4.Addresses, ip4.Gateway}, IPv6Mode: v.Addressing.IPv6.Mode}
		if d := v.Addressing.DNS; d != nil {
			a.DNS = &dnsAnswer{d.Servers, d.SearchDomains}
		}
	}
	return a
}

func object(raw json.RawMessage) map[string]any {
	var v map[string]any
	_ = json.Unmarshal(raw, &v)
	return v
}

func operation(op config.Operation, value map[string]any) config.Operation {
	op.Value, _ = json.Marshal(value)
	return op
}

// batch turns the proposal and the operator's answers into the change batch to save.
func batch(p proposal, a answers) ([]config.Operation, error) {
	var links, segments, filter []config.Operation
	segmentOf := map[string]string{}
	zones := map[string]bool{}
	dnsTarget := -1
	for _, op := range p.Operations {
		switch op.Collection {
		case "links":
			if a.Adapters[op.LocalID].Keep {
				links = append(links, op)
			}
		case "segments":
			v := object(op.Value)
			link := strings.TrimPrefix(v["link"].(string), "@")
			answer, ok := a.Adapters[link]
			if !ok || !answer.Keep {
				continue
			}
			zones[answer.Zone] = true
			v["zone"] = "@" + answer.Zone
			addressing := v["addressing"].(map[string]any)
			ip4 := map[string]any{"mode": answer.IPv4.Mode}
			if answer.IPv4.Mode == "static" {
				ip4["addresses"] = answer.IPv4.Addresses
				if answer.IPv4.Gateway != "" {
					ip4["gateway"] = answer.IPv4.Gateway
				}
			}
			addressing["ipv4"] = ip4
			// Static IPv6 addresses are kept as detected; only the mode can change.
			if ip6 := addressing["ipv6"].(map[string]any); ip6["mode"] != answer.IPv6Mode {
				addressing["ipv6"] = map[string]any{"mode": answer.IPv6Mode}
			}
			segmentOf[link] = op.LocalID
			_, hadDNS := addressing["dns"]
			delete(addressing, "dns")
			if hadDNS || (dnsTarget < 0 && answer.Zone == "wan") {
				dnsTarget = len(segments)
			}
			segments = append(segments, operation(op, v))
		}
	}
	if len(segments) == 0 {
		return nil, errors.New("at least one adapter must be kept")
	}
	if a.DNS != nil && !service(p) {
		if dnsTarget < 0 {
			dnsTarget = 0
		}
		v := object(segments[dnsTarget].Value)
		dns := map[string]any{"servers": a.DNS.Servers}
		if len(a.DNS.SearchDomains) > 0 {
			dns["searchDomains"] = a.DNS.SearchDomains
		}
		v["addressing"].(map[string]any)["dns"] = dns
		segments[dnsTarget] = operation(segments[dnsTarget], v)
	}
	// Starter objects the detection left out (no LAN or no uplink detected) join when the answers add that zone.
	proposed := map[string]bool{}
	for _, op := range p.Operations {
		proposed[op.Collection+"/"+op.LocalID] = true
	}
	extra := slices.DeleteFunc(slices.Clone(p.Starter), func(op config.Operation) bool { return proposed[op.Collection+"/"+op.LocalID] })
	for _, op := range slices.Concat(p.Operations, extra) {
		switch op.Collection {
		case "services":
			// The starter services serve only the LAN's starter rules.
			if zones["lan"] {
				filter = append(filter, op)
			}
		case "policy.input", "policy.forward":
			// A starter rule for a zone no adapter is in any more would reference nothing.
			var v struct{ From, To []string }
			_ = json.Unmarshal(op.Value, &v)
			if !slices.ContainsFunc(append(v.From, v.To...), func(ref string) bool {
				zone, isZone := strings.CutPrefix(ref, "@")
				return isZone && (zone == "wan" || zone == "lan") && !zones[zone]
			}) {
				filter = append(filter, op)
			}
		case "nat.outbound":
			if zones["wan"] {
				filter = append(filter, op)
			}
		case "dns":
			// The service serves the LAN segments still kept; the operator's servers are its upstreams.
			v := object(op.Value)
			served := []any{}
			for _, ref := range v["segments"].([]any) {
				if slices.Contains(slices.Collect(maps.Values(segmentOf)), strings.TrimPrefix(ref.(string), "@")) {
					served = append(served, ref)
				}
			}
			v["segments"] = served
			delete(v, "upstreams")
			delete(v, "searchDomains")
			v["mode"] = "recursive"
			if a.DNS != nil && len(a.DNS.Servers) > 0 {
				var upstreams []any
				for _, s := range a.DNS.Servers {
					upstreams = append(upstreams, map[string]any{"address": s, "protocol": "do53"})
				}
				v["mode"], v["upstreams"] = "forward", upstreams
			}
			if a.DNS != nil && len(a.DNS.SearchDomains) > 0 {
				v["searchDomains"] = a.DNS.SearchDomains
			}
			filter = append(filter, operation(op, v))
		case "management":
			segment, kept := segmentOf[a.Management]
			if !kept {
				return nil, errors.New("the management adapter must be kept")
			}
			v := object(op.Value)
			v["segment"] = "@" + segment
			filter = append(filter, operation(op, v))
		}
	}
	for _, op := range p.DHCP {
		segment := strings.TrimPrefix(object(op.Value)["segment"].(string), "@")
		if a.DHCP["@"+segment] && slices.Contains(slices.Collect(maps.Values(segmentOf)), segment) {
			filter = append(filter, op)
		}
	}
	var ops []config.Operation
	for _, zone := range []string{"wan", "lan"} {
		if zones[zone] {
			ops = append(ops, config.Operation{Op: "create", Collection: "zones", LocalID: zone, Value: json.RawMessage(`{"name":"` + zone + `"}`)})
		}
	}
	return slices.Concat(ops, links, segments, filter), nil
}
