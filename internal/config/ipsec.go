package config

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"unicode"
)

// ipsecConnection is one strongSwan IKEv2 connection. A route-based or road-warrior connection runs over its own
// XFRM interface; a policy-based one is matched by its kernel policies' reqid.
type ipsecConnection struct {
	identity
	Enabled         bool      `json:"enabled"`
	Mode            string    `json:"mode"`
	Ifname          string    `json:"ifname,omitempty"`
	XFRMIfID        int       `json:"xfrmIfId,omitempty"`
	Zone            string    `json:"zone,omitempty"`
	TunnelAddresses []string  `json:"tunnelAddresses,omitempty"`
	LocalSegment    string    `json:"localSegment,omitempty"`
	LocalAddress    string    `json:"localAddress,omitempty"`
	RemoteAddress   string    `json:"remoteAddress,omitempty"`
	IKEVersion      int       `json:"ikeVersion"`
	Proposals       []string  `json:"proposals,omitempty"`
	ESPProposals    []string  `json:"espProposals,omitempty"`
	Auth            ipsecAuth `json:"auth"`
	LocalSubnets    []string  `json:"localSubnets,omitempty"`
	RemoteSubnets   []string  `json:"remoteSubnets,omitempty"`
	VirtualPool     []string  `json:"virtualPool,omitempty"`
	DPDDelaySeconds *int      `json:"dpdDelaySeconds,omitempty"`
	StartAction     string    `json:"startAction,omitempty"`
	NATTraversal    *bool     `json:"natTraversal,omitempty"`
}

type ipsecAuth struct {
	LocalType   string           `json:"localType"`
	RemoteType  string           `json:"remoteType"`
	LocalID     string           `json:"localId"`
	RemoteID    string           `json:"remoteId,omitempty"`
	PSKRef      *secretReference `json:"pskRef,omitempty"`
	LocalCertID string           `json:"localCertId,omitempty"`
	CAID        string           `json:"caId,omitempty"`
	EAPUsers    []ipsecEAPUser   `json:"eapUsers,omitempty"`
}

type ipsecEAPUser struct {
	Username    string          `json:"username"`
	PasswordRef secretReference `json:"passwordRef"`
}

const defaultIPsecDPD = 30

// xfrm says the connection runs over its own XFRM interface, which routes and zones name.
func (c *ipsecConnection) xfrm() bool { return c.Mode == "route-based" || c.Mode == "road-warrior" }

func (c *ipsecConnection) secrets() []channelSecret {
	var out []channelSecret
	if c.Auth.PSKRef != nil {
		out = append(out, channelSecret{"/auth/pskRef", c.Auth.PSKRef})
	}
	for i := range c.Auth.EAPUsers {
		out = append(out, channelSecret{fmt.Sprintf("/auth/eapUsers/%d/passwordRef", i), &c.Auth.EAPUsers[i].PasswordRef})
	}
	return out
}

// ipsecSecretFields are the secret references a written connection holds, once prepareIPsec settled the kept ones.
func ipsecSecretFields(value map[string]any) []secretField {
	auth, _ := value["auth"].(map[string]any)
	if auth == nil {
		return nil
	}
	var out []secretField
	if auth["pskRef"] != nil {
		out = append(out, secretField{"/auth/pskRef", auth, "pskRef"})
	}
	users, _ := auth["eapUsers"].([]any)
	for i, u := range users {
		if user, ok := u.(map[string]any); ok && user["passwordRef"] != nil {
			out = append(out, secretField{fmt.Sprintf("/auth/eapUsers/%d/passwordRef", i), user, "passwordRef"})
		}
	}
	return out
}

// prepareIPsec settles a written connection before its secrets are read: a pskRef written as {} keeps the stored
// one, and an EAP user's passwordRef written as {} keeps that user's (by username). Left out, pskRef is cleared.
func prepareIPsec(value map[string]any, old any) (string, error) {
	before, _ := old.(*ipsecConnection)
	auth, _ := value["auth"].(map[string]any)
	if auth == nil {
		return "", nil
	}
	empty := func(v any) bool { m, ok := v.(map[string]any); return ok && len(m) == 0 }
	if empty(auth["pskRef"]) {
		delete(auth, "pskRef")
		if before != nil && before.Auth.PSKRef != nil {
			auth["pskRef"] = secretRefMap(before.Auth.PSKRef)
		} else {
			return "/auth/pskRef", errors.New("No pre-shared key is stored to keep; set one.")
		}
	}
	users, _ := auth["eapUsers"].([]any)
	for i, u := range users {
		user, ok := u.(map[string]any)
		if !ok || !empty(user["passwordRef"]) {
			continue
		}
		delete(user, "passwordRef")
		j := -1
		if before != nil {
			j = slices.IndexFunc(before.Auth.EAPUsers, func(e ipsecEAPUser) bool { return e.Username == user["username"] })
		}
		if j < 0 {
			return fmt.Sprintf("/auth/eapUsers/%d/passwordRef", i), errors.New("No password is stored for this user to keep; set one.")
		}
		user["passwordRef"] = secretRefMap(&before.Auth.EAPUsers[j].PasswordRef)
	}
	return "", nil
}

// swanText is a value safe inside a quoted swanctl.conf string: printable, without spaces, quotes, braces, comments
// or escapes, so no value can end its string or open a section.
func swanText(s string, limit int) bool {
	if s == "" || len(s) > limit {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || strings.ContainsRune(`{}"'#\`, r)
	})
}

// validIPsecProposal rejects malformed hyphenated proposals and weak or deprecated algorithms
// (DES, 3DES, Blowfish, CAST, NULL, MD5, SHA1, MODP768/1024/1536, ECP192, NONE).
func validIPsecProposal(proposal string) bool {
	if proposal == "" {
		return false
	}
	tokens := strings.Split(proposal, "-")
	for _, tok := range tokens {
		if tok == "" {
			return false
		}
		switch tok {
		case "des", "3des", "null", "none", "modpnone",
			"md5", "prfmd5", "sha1", "prfsha1",
			"modp768", "modp1024", "modp1536", "modp1024s160", "modp2048s224", "modp2048s256", "ecp192":
			return false
		}
		for _, prefix := range []string{"des", "3des", "blowfish", "cast", "null", "md5", "prfmd5", "sha1", "prfsha1"} {
			if strings.HasPrefix(tok, prefix) {
				return false
			}
		}
	}
	return true
}

func validateIPsec(d *declaration) []FieldError {
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
	cas := map[string]bool{}
	for _, c := range d.PKI.CAs {
		cas[c.ID] = true
	}
	certs := certificateIndex(d)
	seen := map[string]bool{}
	seenEAP := map[string]bool{}
	pskPairs := map[string]string{}
	for i := range d.VPN.IPsec {
		c := &d.VPN.IPsec[i]
		p := fmt.Sprintf("/vpn/ipsec/%d", i)
		if seen[c.ID] {
			bad(p+"/id", "Duplicate object identity.")
		}
		if invalidText(c.Name, true) {
			bad(p+"/name", "Control or format characters are not allowed.")
		}
		if seen["name:"+c.Name] {
			bad(p+"/name", "Name already exists in this collection.")
		}
		seen[c.ID], seen["name:"+c.Name] = true, true
		if c.xfrm() {
			if c.Ifname == "" {
				bad(p+"/ifname", "A route-based or road-warrior connection needs an interface name.")
			} else if seen["if:"+c.Ifname] {
				bad(p+"/ifname", "Another interface has this name.")
			}
			if c.XFRMIfID == 0 {
				bad(p+"/xfrmIfId", "A route-based or road-warrior connection needs an XFRM interface ID.")
			} else if seen[fmt.Sprintf("xfrm:%d", c.XFRMIfID)] {
				bad(p+"/xfrmIfId", "Another connection has this XFRM interface ID.")
			}
			seen["if:"+c.Ifname], seen[fmt.Sprintf("xfrm:%d", c.XFRMIfID)] = true, true
		} else {
			if c.Ifname != "" {
				bad(p+"/ifname", "A policy-based connection has no interface.")
			}
			if c.XFRMIfID != 0 {
				bad(p+"/xfrmIfId", "A policy-based connection has no XFRM interface.")
			}
			if len(c.TunnelAddresses) > 0 {
				bad(p+"/tunnelAddresses", "A policy-based connection has no interface to hold addresses.")
			}
		}
		if c.Zone != "" && !zones[c.Zone] {
			bad(p+"/zone", "Unknown zone.")
		} else if c.Zone == "" && c.Enabled {
			bad(p+"/zone", "An enabled connection needs a zone.")
		}
		if c.LocalSegment != "" && !segments[c.LocalSegment] {
			bad(p+"/localSegment", "Expected a segment of this configuration.")
		}
		if c.LocalAddress != "" {
			if a, err := netip.ParseAddr(c.LocalAddress); err != nil || a.Zone() != "" || a.Is4In6() || !validUnicast(a) {
				bad(p+"/localAddress", "Expected a unicast IPv4 or IPv6 address.")
			}
		}
		switch {
		case c.Mode == "road-warrior" && (c.RemoteAddress == "" || c.RemoteAddress == "%any"):
		case c.RemoteAddress == "":
			bad(p+"/remoteAddress", "A site-to-site connection needs the remote gateway's address or name.")
		case c.RemoteAddress == "%any":
			bad(p+"/remoteAddress", "Only a road-warrior connection accepts any remote address.")
		default:
			if a, err := netip.ParseAddr(c.RemoteAddress); err == nil {
				if a.Zone() != "" || a.Is4In6() || !validUnicast(a) {
					bad(p+"/remoteAddress", "Expected a unicast IPv4 or IPv6 address.")
				}
			} else if !validEndpointHost(c.RemoteAddress) {
				bad(p+"/remoteAddress", "Expected an IPv4 or IPv6 address or a DNS name.")
			}
		}
		for j, prop := range c.Proposals {
			if !validIPsecProposal(prop) {
				bad(fmt.Sprintf("%s/proposals/%d", p, j), "Weak, deprecated, or malformed IKE proposal.")
			}
		}
		for j, prop := range c.ESPProposals {
			if !validIPsecProposal(prop) {
				bad(fmt.Sprintf("%s/espProposals/%d", p, j), "Weak, deprecated, or malformed ESP proposal.")
			}
		}
		if c.Mode == "road-warrior" && c.StartAction != "" && c.StartAction != "none" {
			bad(p+"/startAction", "A road-warrior connection waits for its clients: its start action is none.")
		}
		prefixes := func(field string, list []string, host, pool bool) {
			masked := map[netip.Prefix]bool{}
			for j, s := range list {
				x, err := netip.ParsePrefix(s)
				fp := fmt.Sprintf("%s/%s/%d", p, field, j)
				switch {
				case err != nil || x.Addr().Is4In6() || x.Addr().Zone() != "":
					bad(fp, "Expected an IPv4 or IPv6 prefix in CIDR notation.")
					continue
				case host && !validUnicast(x.Addr()):
					bad(fp, "Expected a unicast IPv4 or IPv6 address with its prefix length.")
					continue
				case pool && (x.Bits() == 0 || x.Addr().IsLoopback() || x.Addr().IsMulticast() || x.Addr().IsLinkLocalUnicast() || x.Addr().IsLinkLocalMulticast()):
					bad(fp, "Virtual pool must be a routable non-default unicast prefix.")
					continue
				}
				key := x.Masked()
				if host {
					key = x
				}
				// Duplicates are refused after masking, so the stored list stays unique.
				if masked[key] {
					bad(fp, "Duplicate prefix.")
				}
				masked[key] = true
			}
		}
		prefixes("tunnelAddresses", c.TunnelAddresses, true, false)
		prefixes("localSubnets", c.LocalSubnets, false, false)
		prefixes("remoteSubnets", c.RemoteSubnets, false, false)
		prefixes("virtualPool", c.VirtualPool, false, true)
		switch c.Mode {
		case "policy-based":
			if len(c.LocalSubnets) == 0 {
				bad(p+"/localSubnets", "A policy-based connection needs its local subnets.")
			}
			if len(c.RemoteSubnets) == 0 {
				bad(p+"/remoteSubnets", "A policy-based connection needs its remote subnets.")
			}
		case "road-warrior":
			if len(c.VirtualPool) == 0 {
				bad(p+"/virtualPool", "A road-warrior connection needs a pool of client addresses.")
			}
			if len(c.RemoteSubnets) > 0 {
				bad(p+"/remoteSubnets", "A road-warrior client's traffic selector is the address it is given.")
			}
		}
		if c.Mode != "road-warrior" && len(c.VirtualPool) > 0 {
			bad(p+"/virtualPool", "Only a road-warrior connection hands out addresses.")
		}
		a := &c.Auth
		ap := p + "/auth"
		if !swanText(a.LocalID, 255) {
			bad(ap+"/localId", "Expected an identity without spaces, quotes, braces, # or backslashes.")
		}
		if a.RemoteID != "" && !swanText(a.RemoteID, 255) {
			bad(ap+"/remoteId", "Expected an identity without spaces, quotes, braces, # or backslashes.")
		}
		psk := a.LocalType == "psk" || a.RemoteType == "psk"
		if psk && a.PSKRef == nil {
			bad(ap+"/pskRef", "Pre-shared key authentication needs a pre-shared key.")
		} else if !psk && a.PSKRef != nil {
			bad(ap+"/pskRef", "A pre-shared key is used only with psk authentication.")
		} else if psk && a.LocalID != "" && a.PSKRef != nil {
			pairKey := a.LocalID + "\x00" + a.RemoteID
			if prev, exists := pskPairs[pairKey]; exists && prev != a.PSKRef.Secret {
				field := "/remoteId"
				if a.RemoteID == "" {
					field = "/localId"
				}
				bad(ap+field, "Another PSK connection already uses this identity pair with a different secret.")
			}
			pskPairs[pairKey] = a.PSKRef.Secret
		}
		if a.LocalType == "cert" {
			if a.LocalCertID == "" {
				bad(ap+"/localCertId", "Certificate authentication needs the local certificate.")
			} else if certs[a.LocalCertID] == nil {
				bad(ap+"/localCertId", "Expected a certificate of this configuration.")
			}
		} else if a.LocalCertID != "" {
			bad(ap+"/localCertId", "A local certificate is used only with cert authentication.")
		}
		if a.RemoteType == "cert" {
			if a.CAID == "" {
				bad(ap+"/caId", "Certificate authentication needs the CA that signs the remote's certificate.")
			} else if !cas[a.CAID] {
				bad(ap+"/caId", "Expected a certificate authority of this configuration.")
			}
		} else if a.CAID != "" {
			bad(ap+"/caId", "A CA is used only when the remote authenticates with a certificate.")
		}
		if a.RemoteType == "eap-mschapv2" {
			if len(a.EAPUsers) == 0 {
				bad(ap+"/eapUsers", "EAP-MSCHAPv2 authentication needs at least one user.")
			}
		} else if len(a.EAPUsers) > 0 {
			bad(ap+"/eapUsers", "EAP users are used only with eap-mschapv2 authentication.")
		}
		for j, u := range a.EAPUsers {
			up := fmt.Sprintf("%s/eapUsers/%d/username", ap, j)
			if !swanText(u.Username, 64) {
				bad(up, "Expected a user name without spaces, quotes, braces, # or backslashes.")
			} else if seenEAP[u.Username] {
				bad(up, "Another EAP user already has this username.")
			}
			seenEAP[u.Username] = true
		}
	}
	return out
}

// canonicalizeIPsec stores one spelling of a validated connection: defaults set and prefixes masked.
func canonicalizeIPsec(c *ipsecConnection) {
	if c.DPDDelaySeconds == nil {
		dpd := defaultIPsecDPD
		c.DPDDelaySeconds = &dpd
	}
	if c.StartAction == "" {
		c.StartAction = map[bool]string{true: "none", false: "trap"}[c.Mode == "road-warrior"]
	}
	if c.NATTraversal == nil {
		yes := true
		c.NATTraversal = &yes
	}
	if c.RemoteAddress == "%any" {
		c.RemoteAddress = ""
	}
	if a, err := netip.ParseAddr(c.RemoteAddress); err == nil {
		c.RemoteAddress = a.String()
	}
	if a, err := netip.ParseAddr(c.LocalAddress); err == nil {
		c.LocalAddress = a.String()
	}
	for i, s := range c.TunnelAddresses {
		if x, err := netip.ParsePrefix(s); err == nil {
			c.TunnelAddresses[i] = x.String()
		}
	}
	for _, list := range [][]string{c.LocalSubnets, c.RemoteSubnets, c.VirtualPool} {
		for i, s := range list {
			if x, err := netip.ParsePrefix(s); err == nil {
				list[i] = x.Masked().String()
			}
		}
	}
	for _, list := range []*[]string{&c.TunnelAddresses, &c.LocalSubnets, &c.RemoteSubnets, &c.VirtualPool, &c.Proposals, &c.ESPProposals} {
		if len(*list) == 0 {
			*list = nil
		}
	}
	if len(c.Auth.EAPUsers) == 0 {
		c.Auth.EAPUsers = nil
	}
}

// checkIPsecSecrets refuses an empty pre-shared key or EAP password on a connection written in this batch
// (or on every connection when written is nil, as during a backup restore).
func checkIPsecSecrets(d *declaration, secrets secretDocument, written map[objectKey][]byte) []FieldError {
	var out []FieldError
	for i := range d.VPN.IPsec {
		c := &d.VPN.IPsec[i]
		if written != nil {
			if _, ok := written[objectKey{"vpn.ipsec", c.ID}]; !ok {
				continue
			}
		}
		for _, s := range c.secrets() {
			if !s.ref.Unresolved && secrets.Scopes["core"][s.ref.Secret].Value == "" {
				out = append(out, FieldError{fmt.Sprintf("/vpn/ipsec/%d%s", i, s.path), "Expected a non-empty secret."})
			}
		}
	}
	return out
}

// unresolvedIPsec is where an enabled connection would run without a secret: its pre-shared key, an EAP user's
// password, or its certificate's private key.
func unresolvedIPsec(d *declaration) []FieldError {
	var out []FieldError
	certs := certificateIndex(d)
	for i := range d.VPN.IPsec {
		c := &d.VPN.IPsec[i]
		if !c.Enabled {
			continue
		}
		for _, s := range c.secrets() {
			if s.ref.Unresolved {
				out = append(out, FieldError{fmt.Sprintf("/vpn/ipsec/%d%s", i, s.path), "Secret " + s.ref.Secret + " is unresolved; explicitly bind or set it before applying."})
			}
		}
		if x := certs[c.Auth.LocalCertID]; x != nil && x.KeyRef.Unresolved {
			out = append(out, FieldError{fmt.Sprintf("/vpn/ipsec/%d/auth/localCertId", i), "The private key of certificate " + x.Name + " is unresolved; set it before applying."})
		}
	}
	return out
}

// realizedIPsecSecrets are the pre-shared keys and EAP passwords, and the certificate keys, enabled connections render.
func realizedIPsecSecrets(d *declaration, secrets secretDocument, certs map[string]*pkiCertificate, into map[string]string, addCertKey func(id, key string)) {
	for i := range d.VPN.IPsec {
		c := &d.VPN.IPsec[i]
		if !c.Enabled {
			continue
		}
		for _, s := range c.secrets() {
			if !s.ref.Unresolved {
				into[s.ref.Secret] = secrets.Scopes["core"][s.ref.Secret].Value
			}
		}
		if x := certs[c.Auth.LocalCertID]; x != nil && !x.KeyRef.Unresolved {
			addCertKey(x.ID, secrets.Scopes["core"][x.KeyRef.Secret].Value)
		}
	}
}

// IPsecConnection is a saved connection's public part, for its status and its initiate and terminate actions.
type IPsecConnection struct {
	ID, Name, Mode string
	Enabled        bool
}

// IPsecConnections returns the saved head's IPsec connections, secrets left out.
func (s *Store) IPsecConnections(ctx context.Context) (result []IPsecConnection, err error) {
	err = s.read(ctx, "", nil, func(state savedState, _ VersionInfo) error {
		for _, c := range state.Config.VPN.IPsec {
			result = append(result, IPsecConnection{ID: c.ID, Name: c.Name, Mode: c.Mode, Enabled: c.Enabled})
		}
		return nil
	})
	return
}
