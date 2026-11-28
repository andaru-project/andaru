package render

import (
	"cmp"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
)

// IPsecDir holds strongSwan's swanctl.conf and the credentials beside it, which swanctl --file loads from there.
const IPsecDir = "/run/andaru-ipsec/"

// IPsecConfig is the swanctl.conf of the enabled IPsec connections.
const IPsecConfig = IPsecDir + "swanctl.conf"

type ipsecRef struct {
	Secret     string `json:"secret"`
	Unresolved bool   `json:"unresolved"`
}

type ipsecConnection struct {
	ID              string   `json:"id"`
	Enabled         bool     `json:"enabled"`
	Mode            string   `json:"mode"`
	Ifname          string   `json:"ifname"`
	XFRMIfID        int      `json:"xfrmIfId"`
	Zone            string   `json:"zone"`
	TunnelAddresses []string `json:"tunnelAddresses"`
	LocalSegment    string   `json:"localSegment"`
	LocalAddress    string   `json:"localAddress"`
	RemoteAddress   string   `json:"remoteAddress"`
	Proposals       []string `json:"proposals"`
	ESPProposals    []string `json:"espProposals"`
	Auth            struct {
		LocalType   string    `json:"localType"`
		RemoteType  string    `json:"remoteType"`
		LocalID     string    `json:"localId"`
		RemoteID    string    `json:"remoteId"`
		PSKRef      *ipsecRef `json:"pskRef"`
		LocalCertID string    `json:"localCertId"`
		CAID        string    `json:"caId"`
		EAPUsers    []struct {
			Username    string   `json:"username"`
			PasswordRef ipsecRef `json:"passwordRef"`
		} `json:"eapUsers"`
	} `json:"auth"`
	LocalSubnets    []string `json:"localSubnets"`
	RemoteSubnets   []string `json:"remoteSubnets"`
	VirtualPool     []string `json:"virtualPool"`
	DPDDelaySeconds *int     `json:"dpdDelaySeconds"`
	StartAction     string   `json:"startAction"`
	NATTraversal    *bool    `json:"natTraversal"`
}

func (c ipsecConnection) xfrm() bool { return c.Mode == "route-based" || c.Mode == "road-warrior" }

// ipsecs are the enabled connections, with the reqid each policy-based one's kernel policies carry: 100 and up, in
// the declaration's order, so the packet filter names the same policies swanctl.conf installs.
func (r *renderer) ipsecs() (out []ipsecConnection, reqids map[string]int) {
	reqids = map[string]int{}
	for _, c := range r.d.VPN.IPsec {
		if !c.Enabled || c.xfrm() && r.taken(c.Ifname, "xfrm") {
			continue
		}
		if !c.xfrm() {
			reqids[c.ID] = 100 + len(reqids)
		}
		out = append(out, c)
	}
	return out, reqids
}

// ipsec renders each enabled connection's XFRM interface and, when any is enabled, swanctl.conf with the credentials
// it names. The interface's routes are networkd's: charon installs none (install_routes_xfrmi = no).
func (r *renderer) ipsec() {
	conns, reqids := r.ipsecs()
	for i, c := range r.d.VPN.IPsec {
		if c.Enabled && c.xfrm() && r.taken(c.Ifname, "xfrm") {
			r.find("error", "NAME_COLLISION", fmt.Sprintf("/vpn/ipsec/%d/ifname", i), "Another device already has the interface name "+c.Ifname+".")
		}
	}
	if len(conns) == 0 {
		return
	}
	var connections, pools, secrets, shown []string
	secret := false
	credentials := map[string]bool{}
	for _, c := range conns {
		i := slices.IndexFunc(r.d.VPN.IPsec, func(x ipsecConnection) bool { return x.ID == c.ID })
		path := fmt.Sprintf("/vpn/ipsec/%d", i)
		if c.xfrm() {
			r.xfrmInterface(c, path)
		}
		conn := []string{"version = 2"}
		if c.LocalAddress != "" {
			conn = append(conn, "local_addrs = "+c.LocalAddress)
		}
		conn = append(conn, "remote_addrs = "+cmp.Or(c.RemoteAddress, "%any"))
		if len(c.Proposals) > 0 {
			conn = append(conn, "proposals = "+strings.Join(c.Proposals, ","))
		}
		if c.DPDDelaySeconds != nil && *c.DPDDelaySeconds > 0 {
			conn = append(conn, fmt.Sprintf("dpd_delay = %ds", *c.DPDDelaySeconds))
		}
		if c.xfrm() {
			conn = append(conn, fmt.Sprintf("if_id_in = %d", c.XFRMIfID), fmt.Sprintf("if_id_out = %d", c.XFRMIfID))
		}
		var poolNames []string
		for j, p := range c.VirtualPool {
			name := fmt.Sprintf("%s-%d", c.ID, j)
			poolNames = append(poolNames, name)
			pools = append(pools, block(name, "addrs = "+p))
		}
		if len(poolNames) > 0 {
			conn = append(conn, "pools = "+strings.Join(poolNames, ","))
		}
		local := []string{"auth = " + map[string]string{"psk": "psk", "cert": "pubkey"}[c.Auth.LocalType], "id = " + quote(c.Auth.LocalID)}
		if c.Auth.LocalType == "cert" {
			local = append(local, "certs = "+r.ipsecCertificate(c.Auth.LocalCertID, path, credentials))
		}
		remote := []string{"auth = " + map[string]string{"psk": "psk", "cert": "pubkey", "eap-mschapv2": "eap-mschapv2"}[c.Auth.RemoteType]}
		if c.Auth.RemoteID != "" {
			remote = append(remote, "id = "+quote(c.Auth.RemoteID))
		}
		if c.Auth.RemoteType == "eap-mschapv2" {
			remote = append(remote, "eap_id = %any")
		}
		if c.Auth.RemoteType == "cert" {
			remote = append(remote, "cacerts = "+r.ipsecCA(c.Auth.CAID, path, credentials))
		}
		everything := "0.0.0.0/0,::/0"
		localTS, remoteTS := everything, everything
		if len(c.LocalSubnets) > 0 {
			localTS = strings.Join(c.LocalSubnets, ",")
		}
		if len(c.RemoteSubnets) > 0 {
			remoteTS = strings.Join(c.RemoteSubnets, ",")
		}
		if c.Mode == "road-warrior" {
			remoteTS = "dynamic"
		}
		child := []string{"local_ts = " + localTS, "remote_ts = " + remoteTS}
		if len(c.ESPProposals) > 0 {
			child = append(child, "esp_proposals = "+strings.Join(c.ESPProposals, ","))
		}
		if reqid, ok := reqids[c.ID]; ok {
			child = append(child, fmt.Sprintf("reqid = %d", reqid))
		}
		child = append(child, "start_action = "+c.StartAction, "dpd_action = "+map[string]string{"trap": "trap", "start": "restart", "none": "clear"}[c.StartAction])
		conn = append(conn, block("local", local...), block("remote", remote...), block("children", block(c.ID, child...)))
		connections = append(connections, block(c.ID, conn...))

		add := func(name, field string, ref ipsecRef, ids ...string) {
			secret = true
			value, ok := r.in.Secrets[ref.Secret]
			if !ok || ref.Unresolved {
				r.find("error", "SECRET_UNAVAILABLE", path+field, "Secret "+ref.Secret+" is not available; set it before applying.")
			}
			// Base64, so no secret can end its value or open a section.
			lines := append(ids, "secret = 0s"+base64.StdEncoding.EncodeToString([]byte(value)))
			secrets = append(secrets, block(name, lines...))
			shown = append(shown, block(name, append(ids, "secret = "+redacted)...))
		}
		if ref := c.Auth.PSKRef; ref != nil {
			ids := []string{"id-local = " + quote(c.Auth.LocalID)}
			if c.Auth.RemoteID != "" {
				ids = append(ids, "id-remote = "+quote(c.Auth.RemoteID))
			}
			add("ike-"+c.ID, "/auth/pskRef", *ref, ids...)
		}
		for j, u := range c.Auth.EAPUsers {
			add(fmt.Sprintf("eap-%s-%d", c.ID, j), fmt.Sprintf("/auth/eapUsers/%d/passwordRef", j), u.PasswordRef, "id = "+quote(u.Username))
		}
	}
	head := "# Generated by Andaru; swanctl --load-all --clear --file " + IPsecConfig + " loads it.\n"
	conf := func(secrets []string) string {
		out := head + block("connections", connections...)
		if len(pools) > 0 {
			out += block("pools", pools...)
		}
		if len(secrets) > 0 {
			out += block("secrets", secrets...)
		}
		return out
	}
	r.result.Files = append(r.result.Files, File{Path: IPsecConfig, Mode: "0600", Secret: secret, Content: conf(shown), real: conf(secrets),
		Sources: []string{"/vpn"}})
}

// xfrmInterface renders a route-based or road-warrior connection's interface: its device, its addresses, the routes
// to its clients' pool and the static routes through it.
func (r *renderer) xfrmInterface(c ipsecConnection, path string) {
	base := networkDir + c.Ifname
	r.file(base+".netdev", "0644", ini(section{"NetDev", []string{"Name=" + c.Ifname, "Kind=xfrm"}},
		section{"Xfrm", []string{fmt.Sprintf("InterfaceId=%d", c.XFRMIfID), "Independent=yes"}}), []string{path})
	network := []string{}
	for _, a := range c.TunnelAddresses {
		network = append(network, "Address="+a)
	}
	network = append(network, "IPv4Forwarding=yes", "IPv6Forwarding=yes")
	sections := []section{{"Match", []string{"Name=" + c.Ifname}}, {"Network", network}}
	static := r.routes("ipsec", c.ID)
	for _, p := range c.VirtualPool {
		if !slices.ContainsFunc(static, func(s section) bool { return s.lines[0] == "Destination="+p }) {
			sections = append(sections, section{"Route", []string{"Destination=" + p}})
		}
	}
	sources := []string{path}
	if len(static) > 0 {
		sources = append(sources, "/routes")
	}
	r.file(base+".network", "0644", ini(append(sections, static...)...), sources)
}

// ipsecCertificate renders a local certificate with its chain, and its key, once, and returns the certificate's path.
func (r *renderer) ipsecCertificate(id, path string, done map[string]bool) string {
	file := IPsecDir + "x509/" + id + ".pem"
	if done[file] {
		return file
	}
	done[file] = true
	i := slices.IndexFunc(r.d.PKI.Certificates, func(c certificate) bool { return c.ID == id })
	if i < 0 {
		r.find("error", "CERTIFICATE_UNAVAILABLE", path+"/auth/localCertId", "Certificate "+id+" does not exist.")
		return file
	}
	c := r.d.PKI.Certificates[i]
	pem := strings.TrimRight(c.CertPEM, "\n") + "\n"
	if strings.TrimSpace(c.ChainPEM) != "" {
		pem += strings.TrimRight(c.ChainPEM, "\n") + "\n"
	}
	r.file(file, "0644", pem, []string{"/pki"})
	key, ok := r.in.CertificateKeys[id]
	if !ok {
		r.find("error", "SECRET_UNAVAILABLE", path+"/auth/localCertId", "The private key of certificate "+id+" is not available; set it before applying.")
	}
	r.result.Files = append(r.result.Files, File{Path: IPsecDir + "private/" + id + ".key", Mode: "0400", Secret: true,
		Content: redacted + "\n", real: strings.TrimRight(key, "\n") + "\n", Sources: []string{"/pki"}})
	return file
}

// ipsecCA renders a CA certificate once and returns its path.
func (r *renderer) ipsecCA(id, path string, done map[string]bool) string {
	file := IPsecDir + "x509ca/" + id + ".pem"
	if done[file] {
		return file
	}
	done[file] = true
	i := slices.IndexFunc(r.d.PKI.CAs, func(c certificate) bool { return c.ID == id })
	if i < 0 {
		r.find("error", "CERTIFICATE_UNAVAILABLE", path+"/auth/caId", "Certificate authority "+id+" does not exist.")
		return file
	}
	r.file(file, "0644", strings.TrimRight(r.d.PKI.CAs[i].CertPEM, "\n")+"\n", []string{"/pki"})
	return file
}

// ipsecListeners are the input rules that let peers reach IKE, NAT-T and ESP: on the local segment's interface when
// a connection names one, else on every uplink.
func (r *renderer) ipsecListeners() []string {
	conns, _ := r.ipsecs()
	var out []string
	for _, c := range conns {
		iface := "iifname @uplinks"
		if c.LocalSegment != "" {
			s, ok := r.segmentByID(c.LocalSegment)
			if !ok || !r.usable[s.Link] {
				continue
			}
			iface = fmt.Sprintf("iifname %q", r.name[s.Link])
		}
		ports := "udp dport { 500, 4500 }"
		if c.NATTraversal != nil && !*c.NATTraversal {
			ports = "udp dport 500"
		}
		out = append(out, iface+" "+ports+" accept", iface+" ip protocol esp accept", iface+" ip6 nexthdr esp accept")
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// block is one swanctl.conf section, its lines indented.
func block(name string, lines ...string) string {
	var b strings.Builder
	b.WriteString(name + " {\n")
	for _, l := range lines {
		for line := range strings.SplitSeq(strings.TrimRight(l, "\n"), "\n") {
			b.WriteString("\t" + line + "\n")
		}
	}
	b.WriteString("}\n")
	return b.String()
}

// quote is a swanctl.conf string; validation keeps quotes, braces and backslashes out of what it quotes.
func quote(s string) string { return `"` + s + `"` }

// ipsecPolicies are, by zone, the reqids of its policy-based connections: their traffic arrives and leaves on an
// uplink, so what puts it in its zone is the kernel policy it matches.
func ipsecPolicies(conns []ipsecConnection, reqids map[string]int) map[string][]int {
	out := map[string][]int{}
	for _, c := range conns {
		if reqid, ok := reqids[c.ID]; ok {
			out[c.Zone] = append(out[c.Zone], reqid)
		}
	}
	return out
}
