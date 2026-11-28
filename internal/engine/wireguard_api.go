package engine

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/andaru-project/andaru/internal/audit"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
)

const (
	wireguardStatusPath = "/api/v1/status/vpn/wireguard"
	wireguardAPIPrefix  = "/api/v1/vpn/wireguard/"
	wireguardStatusWait = 20 * time.Second
)

// wireguardAPI serves the WireGuard key generator, the peers' configuration export and the interfaces' status.
type wireguardAPI struct {
	store    *config.Store
	audits   *audit.Store
	stateDir string
	// status runs andaru realize wg-status into file, which the engine created inside stateDir.
	status func(ctx context.Context, file string) error
}

func (a *wireguardAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	only := func(methods ...string) bool {
		if slices.Contains(methods, r.Method) {
			return true
		}
		w.Header().Set("Allow", strings.Join(methods, ", "))
		writeError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed.")
		return false
	}
	if r.URL.Path == wireguardStatusPath {
		if only(http.MethodGet, http.MethodHead) {
			a.statusView(w, r)
		}
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, wireguardAPIPrefix), "/")
	switch {
	case len(parts) == 1 && parts[0] == "keypair":
		if only(http.MethodPost) {
			a.keypair(w, r)
		}
	case len(parts) == 4 && parts[0] != "" && parts[1] == "peers" && parts[2] != "" && parts[3] == "config":
		if only(http.MethodPost) {
			a.peerConfig(w, r, parts[0], parts[2])
		}
	default:
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.")
	}
}

// decodeBody reads a small JSON object into v, refusing unknown fields and trailing tokens; an empty body is {}.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	raw, err := readRequestBody(w, r, 4<<10)
	if err == nil && len(bytes.TrimSpace(raw)) > 0 {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if err = d.Decode(v); err == nil {
			if err = d.Decode(&struct{}{}); err == io.EOF {
				err = nil
			} else if err == nil {
				err = errors.New("trailing data")
			}
		}
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Expected a JSON object with the documented fields.")
		return false
	}
	return true
}

// newWireGuardKey is a clamped X25519 private key, as wg genkey makes one.
func newWireGuardKey() (*ecdh.PrivateKey, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	b[0] &= 248
	b[31] = b[31]&127 | 64
	return ecdh.X25519().NewPrivateKey(b[:])
}

// keypair generates a key pair, and a pre-shared key when asked; nothing is saved.
func (a *wireguardAPI) keypair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IncludePSK bool `json:"includePsk"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	key, err := newWireGuardKey()
	var psk [32]byte
	if err == nil && req.IncludePSK {
		_, err = rand.Read(psk[:])
	}
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "KEY_GENERATION_FAILED", "Key generation failed.")
		return
	}
	out := map[string]string{"privateKey": base64.StdEncoding.EncodeToString(key.Bytes()), "publicKey": base64.StdEncoding.EncodeToString(key.PublicKey().Bytes())}
	if req.IncludePSK {
		out["presharedKey"] = base64.StdEncoding.EncodeToString(psk[:])
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r, http.StatusOK, out)
}

// formatClientEndpoint validates an optional host[:port] or [IPv6][:port] endpoint override and appends listenPort when omitted.
func formatClientEndpoint(raw string, listenPort int) (string, bool) {
	if raw == "" {
		return fmt.Sprintf("<ENDPOINT>:%d", listenPort), true
	}
	if len(raw) > 261 || strings.ContainsFunc(raw, func(c rune) bool { return unicode.IsSpace(c) || unicode.IsControl(c) || unicode.Is(unicode.Cf, c) }) {
		return "", false
	}
	host := raw
	port := listenPort
	bracketed := false
	if strings.HasPrefix(raw, "[") {
		if h, p, err := net.SplitHostPort(raw); err == nil {
			n, perr := strconv.Atoi(p)
			if perr != nil || n < 1 || n > 65535 || strconv.Itoa(n) != p {
				return "", false
			}
			host, port, bracketed = h, n, true
		} else if strings.HasSuffix(raw, "]") {
			host, bracketed = raw[1:len(raw)-1], true
		} else {
			return "", false
		}
	} else if strings.Count(raw, ":") == 1 {
		h, p, err := net.SplitHostPort(raw)
		if err != nil {
			return "", false
		}
		n, perr := strconv.Atoi(p)
		if perr != nil || n < 1 || n > 65535 || strconv.Itoa(n) != p {
			return "", false
		}
		host, port = h, n
	}
	if host == "" || strings.ContainsAny(host, "[]") {
		return "", false
	}
	if a, err := netip.ParseAddr(host); err == nil {
		if a.Zone() != "" || a.Is4In6() {
			return "", false
		}
		if bracketed && !a.Is6() {
			return "", false
		}
		if a.Is6() {
			return fmt.Sprintf("[%s]:%d", a.String(), port), true
		}
		return fmt.Sprintf("%s:%d", a.String(), port), true
	}
	if bracketed || strings.Contains(host, ":") || len(host) > 253 || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return "", false
	}
	for label := range strings.SplitSeq(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return "", false
			}
		}
	}
	return fmt.Sprintf("%s:%d", host, port), true
}

// peerConfig is a peer's wg-quick configuration, its private key left as a placeholder the client fills in.
func (a *wireguardAPI) peerConfig(w http.ResponseWriter, r *http.Request, id, peerID string) {
	var req struct {
		Endpoint   string   `json:"endpoint"`
		DNS        []string `json:"dns"`
		IncludePSK bool     `json:"includePsk"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if _, ok := formatClientEndpoint(req.Endpoint, 51820); !ok {
		invalidField("/endpoint", "Expected a host name or address, with a valid port (1 to 65535) or without.").write(w, r)
		return
	}
	if len(req.DNS) > 8 {
		invalidField("/dns", "Expected at most 8 DNS server addresses.").write(w, r)
		return
	}
	for i, s := range req.DNS {
		if a, err := netip.ParseAddr(s); err != nil || a.Zone() != "" || a.Is4In6() {
			invalidField(fmt.Sprintf("/dns/%d", i), "Expected an IPv4 or IPv6 address.").write(w, r)
			return
		}
	}
	ifaces, err := a.store.WireGuardInterfaces(r.Context())
	if err != nil {
		writeConfigurationError(w, r, err)
		return
	}
	i := slices.IndexFunc(ifaces, func(x config.WireGuardInterface) bool { return x.ID == id })
	if i < 0 {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "The WireGuard interface does not exist.")
		return
	}
	iface := ifaces[i]
	j := slices.IndexFunc(iface.Peers, func(p config.WireGuardPeer) bool { return p.ID == peerID })
	if j < 0 {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "The peer does not exist.")
		return
	}
	peer := iface.Peers[j]
	if iface.PublicKey == "" {
		writeError(w, r, http.StatusConflict, "WIREGUARD_KEY_UNRESOLVED", "The interface's private key is unresolved; set it first.")
		return
	}
	endpoint, _ := formatClientEndpoint(req.Endpoint, iface.ListenPort)
	var b strings.Builder
	b.WriteString("[Interface]\nPrivateKey = <PRIVATE_KEY>\n")
	if len(peer.AllowedIPs) > 0 {
		b.WriteString("Address = " + strings.Join(peer.AllowedIPs, ", ") + "\n")
	}
	if len(req.DNS) > 0 {
		b.WriteString("DNS = " + strings.Join(req.DNS, ", ") + "\n")
	}
	b.WriteString("\n[Peer]\nPublicKey = " + iface.PublicKey + "\n")
	if req.IncludePSK {
		actor, ok := actorFromRequest(r)
		if !ok {
			writeError(w, r, http.StatusForbidden, "FORBIDDEN", "A verified actor is required.")
			return
		}
		// The intent is durable before the key is read; an export audit storage cannot record does not happen.
		finish, err := beginAudit(r.Context(), a.audits, actor, audit.ActionWireGuardPSKExported, id)
		if err != nil {
			writeError(w, r, http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE", "Security audit storage is unavailable.")
			return
		}
		psk, err := a.store.WireGuardPresharedKey(r.Context(), id, peerID)
		if err != nil {
			finish(audit.ResultFailure)
			if errors.Is(err, config.ErrPresharedKeyUnavailable) {
				writeError(w, r, http.StatusConflict, "PRESHARED_KEY_UNAVAILABLE", "The peer has no pre-shared key, or it is unresolved.")
			} else {
				writeConfigurationError(w, r, err)
			}
			return
		}
		finish(audit.ResultSuccess)
		b.WriteString("PresharedKey = " + psk + "\n")
	}
	var allowed []string
	seenAllowed := map[string]bool{}
	for _, s := range iface.Addresses {
		if x, err := netip.ParsePrefix(s); err == nil {
			masked := x.Masked().String()
			if !seenAllowed[masked] {
				seenAllowed[masked] = true
				allowed = append(allowed, masked)
			}
		}
	}
	if len(allowed) > 0 {
		b.WriteString("AllowedIPs = " + strings.Join(allowed, ", ") + "\n")
	}
	b.WriteString("Endpoint = " + endpoint + "\n")
	if peer.PersistentKeepalive > 0 {
		b.WriteString("PersistentKeepalive = " + strconv.Itoa(peer.PersistentKeepalive) + "\n")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+iface.Ifname+"-"+peer.ID+`.conf"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}

type wireguardPeerStatus struct {
	ID                string     `json:"id,omitempty"`
	Name              string     `json:"name,omitempty"`
	PublicKey         string     `json:"publicKey"`
	Endpoint          string     `json:"endpoint,omitempty"`
	LatestHandshakeAt *time.Time `json:"latestHandshakeAt,omitempty"`
	RxBytes           uint64     `json:"rxBytes"`
	TxBytes           uint64     `json:"txBytes"`
	Online            bool       `json:"online"`
	Applied           bool       `json:"applied"`
}

type wireguardStatus struct {
	ID         string                `json:"id"`
	Ifname     string                `json:"ifname"`
	PublicKey  string                `json:"publicKey"`
	ListenPort int                   `json:"listenPort"`
	Active     bool                  `json:"active"`
	Peers      []wireguardPeerStatus `json:"peers"`
}

// statusView joins what the kernel reports with the saved interfaces: each saved one, its running peers named
// where they are saved, and its saved enabled peers the kernel does not hold, as offline (applied: false).
func (a *wireguardAPI) statusView(w http.ResponseWriter, r *http.Request) {
	ifaces, err := a.store.WireGuardInterfaces(r.Context())
	if err != nil {
		writeConfigurationError(w, r, err)
		return
	}
	runtime, err := a.runtime(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "WireGuard status unavailable", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "STATUS_UNAVAILABLE", "The WireGuard status is unavailable; inspect the engine journal.")
		return
	}
	items := []wireguardStatus{}
	for _, iface := range ifaces {
		item := wireguardStatus{ID: iface.ID, Ifname: iface.Ifname, PublicKey: iface.PublicKey, ListenPort: iface.ListenPort, Peers: []wireguardPeerStatus{}}
		byKey := map[string]config.WireGuardPeer{}
		for _, p := range iface.Peers {
			byKey[p.PublicKey] = p
		}
		seen := map[string]bool{}
		if k := slices.IndexFunc(runtime.Interfaces, func(x generation.WireGuardRuntime) bool { return x.Ifname == iface.Ifname }); k >= 0 {
			live := runtime.Interfaces[k]
			item.Active = true
			item.PublicKey, item.ListenPort = live.PublicKey, live.ListenPort
			for _, p := range live.Peers {
				s := wireguardPeerStatus{PublicKey: p.PublicKey, Endpoint: p.Endpoint, RxBytes: p.RxBytes, TxBytes: p.TxBytes, Online: p.Online, Applied: true}
				if saved, ok := byKey[p.PublicKey]; ok {
					s.ID, s.Name = saved.ID, saved.Name
				}
				if p.LatestHandshake > 0 {
					at := time.Unix(p.LatestHandshake, 0).UTC()
					s.LatestHandshakeAt = &at
				}
				seen[p.PublicKey] = true
				item.Peers = append(item.Peers, s)
			}
		}
		for _, p := range iface.Peers {
			if p.Enabled && !seen[p.PublicKey] {
				item.Peers = append(item.Peers, wireguardPeerStatus{ID: p.ID, Name: p.Name, PublicKey: p.PublicKey, Endpoint: p.Endpoint})
			}
		}
		items = append(items, item)
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"items": items})
}

// runtime asks andaru realize for wg show all dump, through a file of the engine's own in its state directory.
func (a *wireguardAPI) runtime(ctx context.Context) (generation.WireGuardStatus, error) {
	var status generation.WireGuardStatus
	if a.status == nil || a.stateDir == "" {
		return status, errors.New("no realizer")
	}
	tmp, err := os.CreateTemp(a.stateDir, "wg-status-*.json")
	if err != nil {
		return status, err
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(path)
	ctx, cancel := context.WithTimeout(ctx, wireguardStatusWait)
	defer cancel()
	if err = a.status(ctx, path); err != nil {
		return status, err
	}
	raw, err := readFileNoFollow(path, 4<<20)
	if err == nil {
		err = json.Unmarshal(raw, &status)
	}
	return status, err
}
