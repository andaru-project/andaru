package main

import (
	"encoding/json"
	"errors"
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
	var links, segments []config.Operation
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
	if a.DNS != nil {
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
	var ops []config.Operation
	for _, zone := range []string{"wan", "lan"} {
		if zones[zone] {
			ops = append(ops, config.Operation{Op: "create", Collection: "zones", LocalID: zone, Value: json.RawMessage(`{"name":"` + zone + `"}`)})
		}
	}
	return append(append(ops, links...), segments...), nil
}
