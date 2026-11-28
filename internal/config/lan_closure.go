package config

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
)

// managementLAN copies the management segment, its zone, and its link ancestry.
// An uplink — static IPv4 gateway, DHCPv4, or a PPPoE link — is refused. Same
// three conditions as renderer.uplink.
func managementLAN(d declaration, secrets map[string]string) ([]byte, error) {
	if d.Management == nil || d.Management.Segment == "" {
		return nil, errors.New("NO_MANAGEMENT")
	}
	var seg *segment
	for i := range d.Segments {
		if d.Segments[i].ID == d.Management.Segment {
			seg = &d.Segments[i]
			break
		}
	}
	if seg == nil {
		return nil, errors.New("NO_MANAGEMENT")
	}
	links := map[string]link{}
	for _, l := range d.Links {
		links[l.ID] = l
	}
	current := links[seg.Link]
	if seg.Addressing.IPv4.Gateway != "" || seg.Addressing.IPv4.Mode == "dhcp" || current.Kind == "pppoe" {
		return nil, errors.New("UPLINK_MANAGEMENT")
	}
	var ancestry []link
	seen := map[string]bool{}
	queue := []string{seg.Link}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		item := links[id]
		ancestry = append(ancestry, item)
		if item.Parent != "" {
			queue = append(queue, item.Parent)
		}
		if item.Members != nil {
			queue = append(queue, (*item.Members)...)
		}
	}
	// A group's members come along, so the copy validates on its own.
	byID := map[string]address{}
	for _, item := range d.Aliases.Addresses {
		byID[item.ID] = item
	}
	wanted := map[string]bool{}
	pending := slices.Clone(d.Management.AllowedSources)
	for len(pending) > 0 {
		id := pending[0]
		pending = pending[1:]
		if wanted[id] {
			continue
		}
		wanted[id] = true
		pending = append(pending, byID[id].Members...)
	}
	var addresses []address
	for _, item := range d.Aliases.Addresses {
		if wanted[item.ID] {
			addresses = append(addresses, item)
		}
	}
	var zones []zone
	for _, z := range d.Zones {
		if z.ID == seg.Zone {
			zones = append(zones, z)
		}
	}
	bound := map[string]string{}
	for _, l := range ancestry {
		if l.Password == nil {
			continue
		}
		if value, ok := secrets[l.Password.Secret]; ok {
			bound[l.Password.Secret] = value
		}
	}
	return json.Marshal(struct {
		Kind          string            `json:"kind"`
		SchemaVersion int               `json:"schemaVersion"`
		Management    management        `json:"management"`
		Links         []link            `json:"links"`
		Segments      []segment         `json:"segments"`
		Zones         []zone            `json:"zones"`
		Addresses     []address         `json:"addresses"`
		Secrets       map[string]string `json:"secrets"`
	}{"lan-closure", 1, *d.Management, ancestry, []segment{keptSegment(*seg)}, zones, addresses, bound})
}

// LANClosure is the management closure for one saved declaration. The engine
// writes it beside a generation; disaster boot reads that file, not the store.
func LANClosure(value []byte, secrets map[string]string) ([]byte, error) {
	var d declaration
	if err := json.Unmarshal(value, &d); err != nil {
		return nil, err
	}
	return managementLAN(d, secrets)
}

// Directory is the state directory this store opened.
func (s *Store) Directory() string { return s.root }

// ManagementLAN returns the management closure for one saved version. An empty
// version is the saved head.
func (s *Store) ManagementLAN(ctx context.Context, version string) ([]byte, error) {
	id := Version(version)
	if version != "" {
		if _, err := ParseVersion(version); err != nil {
			return nil, err
		}
	}
	var raw []byte
	err := s.read(ctx, id, nil, func(state savedState, _ VersionInfo) error {
		secrets := map[string]string{}
		for name, secret := range state.Secrets.Scopes["core"] {
			secrets[name] = secret.Value
		}
		var e error
		raw, e = managementLAN(state.Config, secrets)
		return e
	})
	return raw, err
}

// KeptDeclaration turns a kept copy into the documents a fresh store restores as its first saved version.
// It accepts recovery.json, a whole declaration with its secrets, or a lan-closure, which becomes a declaration
// holding only the management segment, its link ancestry, its zone and its allowed sources.
func KeptDeclaration(body []byte) (RestoreDocuments, error) {
	var doc struct {
		Kind        string            `json:"kind"`
		Declaration json.RawMessage   `json:"declaration"`
		Secrets     map[string]string `json:"secrets"`
		Management  *management       `json:"management"`
		Links       []link            `json:"links"`
		Segments    []segment         `json:"segments"`
		Zones       []zone            `json:"zones"`
		Addresses   []address         `json:"addresses"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return RestoreDocuments{}, err
	}
	declarationBody := doc.Declaration
	kinds := map[string]string{} // a kept secret's kind, when it is not a link's password
	switch doc.Kind {
	case "lan-closure":
		for i := range doc.Segments {
			// A delegated prefix comes from an uplink, which a LAN copy never holds.
			if doc.Segments[i].Addressing.IPv6.Mode == "delegated" {
				doc.Segments[i].Addressing.IPv6 = ipv6{Mode: "none"}
			}
		}
		if doc.Management != nil {
			doc.Management.Certificate = "" // the copy holds no certificate: the web interface serves its bootstrap one
		}
		d := declaration{
			SchemaVersion: schemaVersion, Management: doc.Management,
			Links: nonNil(doc.Links), Segments: nonNil(doc.Segments), Zones: nonNil(doc.Zones),
			Aliases: aliasSet{Addresses: nonNil(doc.Addresses), Ports: []portAlias{}},
		}
		// The recorder refused an uplink; a copy edited afterwards is refused here the same way.
		if _, err := managementLAN(d, nil); err != nil {
			return RestoreDocuments{}, err
		}
		for _, seg := range d.Segments {
			// Without an address of its own, nobody could reach the management segment it restores.
			if a := seg.Addressing; len(a.IPv4.Addresses) == 0 && len(a.IPv6.Addresses) == 0 {
				return RestoreDocuments{}, errors.New("NO_ADDRESS: the kept management segment has no address of its own")
			}
		}
		var err error
		if declarationBody, err = json.Marshal(d); err != nil {
			return RestoreDocuments{}, err
		}
	case "":
		if len(declarationBody) == 0 {
			return RestoreDocuments{}, errors.New("kept copy holds no declaration")
		}
		// The engine stores recovery/ from the public view, which adds the built-in roles; a stored declaration
		// holds only custom ones.
		var d map[string]json.RawMessage
		var err error
		if err = json.Unmarshal(declarationBody, &d); err != nil {
			return RestoreDocuments{}, err
		}
		// The copy keeps the network reachable, and holds only the links' secrets: notification channels, whose
		// secrets it lacks, are set up again after the recovery.
		delete(d, "notifications")
		// Nor does it hold private keys: the web interface serves its bootstrap certificate, and add-ons lose the
		// certificates they named, until the certificates are set up again.
		delete(d, "pki")
		delete(d, "acme") // its orders name the certificates and accounts the copy does not hold
		if raw, ok := d["management"]; ok {
			var m map[string]json.RawMessage
			if err := json.Unmarshal(raw, &m); err != nil {
				return RestoreDocuments{}, err
			}
			delete(m, "certificate")
			if d["management"], err = json.Marshal(m); err != nil {
				return RestoreDocuments{}, err
			}
		}
		if raw, ok := d["addons"]; ok {
			var addons []map[string]any
			if err := json.Unmarshal(raw, &addons); err != nil {
				return RestoreDocuments{}, err
			}
			for _, a := range addons {
				settings, _ := a["settings"].(map[string]any)
				stripped := false
				for k, v := range settings {
					if id, ok := v.(string); ok && certificateIDPattern.MatchString(id) {
						delete(settings, k)
						stripped = true
					}
				}
				if stripped {
					// Keep the sanitized recovery copy restorable when a manifest marks the stripped certificate-reference required.
					a["enabled"] = false
				}
			}
			if d["addons"], err = json.Marshal(addons); err != nil {
				return RestoreDocuments{}, err
			}
		}
		// A view shows a link's password with its state; a declaration holds only the reference.
		if raw, ok := d["links"]; ok {
			var links []map[string]json.RawMessage
			if err := json.Unmarshal(raw, &links); err != nil {
				return RestoreDocuments{}, err
			}
			for _, l := range links {
				var ref secretReference
				if pw, ok := l["password"]; ok {
					if err := json.Unmarshal(pw, &ref); err != nil {
						return RestoreDocuments{}, err
					}
					if l["password"], err = json.Marshal(ref); err != nil {
						return RestoreDocuments{}, err
					}
				}
			}
			if d["links"], err = json.Marshal(links); err != nil {
				return RestoreDocuments{}, err
			}
		}
		// The copy keeps WireGuard up, for remote access to a recovered host: each key reference loses its view state,
		// and one the copy does not hold (a disabled interface's or peer's) is left unresolved.
		if raw, ok := d["vpn"]; ok {
			var vpn struct {
				WireGuard []map[string]any `json:"wireguard"`
			}
			if err := json.Unmarshal(raw, &vpn); err != nil {
				return RestoreDocuments{}, err
			}
			reference := func(parent map[string]any, field string) {
				view, ok := parent[field].(map[string]any)
				if !ok {
					return
				}
				name, _ := view["secret"].(string)
				ref := map[string]any{"secret": name}
				if _, held := doc.Secrets[name]; !held {
					ref["unresolved"] = true
				}
				kinds[name] = "wireguard"
				parent[field] = ref
			}
			for _, w := range vpn.WireGuard {
				reference(w, "privateKeyRef")
				peers, _ := w["peers"].([]any)
				for _, p := range peers {
					if peer, ok := p.(map[string]any); ok {
						reference(peer, "presharedKeyRef")
					}
				}
			}
			if d["vpn"], err = json.Marshal(vpn); err != nil {
				return RestoreDocuments{}, err
			}
			if declarationBody, err = json.Marshal(d); err != nil {
				return RestoreDocuments{}, err
			}
		}
		if raw, ok := d["roles"]; ok {
			var roles []map[string]json.RawMessage
			if err := json.Unmarshal(raw, &roles); err != nil {
				return RestoreDocuments{}, err
			}
			roles = slices.DeleteFunc(roles, func(r map[string]json.RawMessage) bool { _, builtIn := r["builtIn"]; return builtIn })
			// Schema 3 requires the key, so a copy kept under it keeps an empty list.
			if len(roles) == 0 && string(d["schemaVersion"]) != "3" {
				delete(d, "roles")
			} else if d["roles"], err = json.Marshal(roles); err != nil {
				return RestoreDocuments{}, err
			}
			if declarationBody, err = json.Marshal(d); err != nil {
				return RestoreDocuments{}, err
			}
		}
	default:
		return RestoreDocuments{}, errors.New("unknown kept copy kind " + doc.Kind)
	}
	core := make(map[string]backupSecretEntry, len(doc.Secrets))
	for name, value := range doc.Secrets {
		core[name] = backupSecretEntry{Kind: cmp.Or(kinds[name], "password"), Value: value}
	}
	secrets, err := canonical(backupSecretDocument{SchemaVersion: 1, Scopes: map[string]map[string]backupSecretEntry{"core": core}})
	if err != nil {
		return RestoreDocuments{}, err
	}
	docs := RestoreDocuments{Configuration: declarationBody, ConfigurationSecrets: secrets}
	// A copy that needs decisions could only restore with its provisional choices, so it is refused before a reset
	// stops anything (settings safe mode).
	decoded, err := decodeBackupDocuments(docs)
	if err == nil && decoded.needsDecisions {
		err = errors.New("the kept copy needs decisions to read in this Andaru")
	}
	return docs, err
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// keptSegment is the management segment as a kept copy carries it. The copy keeps no DHCPv6 server, so an
// advertisement that sends clients to one becomes plain SLAAC: a reset apply refuses the original.
func keptSegment(seg segment) segment {
	// An incomplete legacy form is left for validation to refuse: mode() reads all three of its fields.
	if ra := seg.Addressing.IPv6.RA; ra != nil && (ra.Mode != "" || ra.Enabled != nil && ra.Managed != nil && ra.OtherInfo != nil) && slices.Contains([]string{"managed", "assisted", "stateless"}, ra.mode()) {
		kept := *ra
		kept.Mode, kept.Enabled, kept.Managed, kept.OtherInfo = "unmanaged", nil, nil, nil
		seg.Addressing.IPv6.RA = &kept
	}
	return seg
}
