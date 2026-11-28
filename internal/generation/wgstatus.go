package generation

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// WireGuardOnline is how recent a peer's last handshake must be for it to count as connected: WireGuard renews
// a session every two minutes while traffic flows, so three minutes without one means the peer is gone.
const WireGuardOnline = 180 * time.Second

// WireGuardStatus is what the kernel reports of every WireGuard interface, private and pre-shared keys left out.
type WireGuardStatus struct {
	Interfaces []WireGuardRuntime `json:"interfaces"`
}

type WireGuardRuntime struct {
	Ifname     string                 `json:"ifname"`
	PublicKey  string                 `json:"publicKey"`
	ListenPort int                    `json:"listenPort"`
	Peers      []WireGuardPeerRuntime `json:"peers"`
}

type WireGuardPeerRuntime struct {
	PublicKey  string   `json:"publicKey"`
	Endpoint   string   `json:"endpoint,omitempty"`
	AllowedIPs []string `json:"allowedIps"`
	// LatestHandshake is Unix seconds; 0 is never.
	LatestHandshake int64  `json:"latestHandshake"`
	RxBytes         uint64 `json:"rxBytes"`
	TxBytes         uint64 `json:"txBytes"`
	Online          bool   `json:"online"`
}

// ReadWireGuardStatus runs wg show all dump. It takes no lock: it only reads the kernel.
func ReadWireGuardStatus(ctx context.Context, run func(ctx context.Context, name string, args ...string) ([]byte, error), now time.Time) (WireGuardStatus, error) {
	out, err := run(ctx, "/usr/bin/wg", "show", "all", "dump")
	if err != nil {
		return WireGuardStatus{}, err
	}
	return ParseWireGuardDump(out, now)
}

// ParseWireGuardDump reads wg show all dump: per interface a line of name, private key, public key, listen port and
// fwmark, then per peer a line of name, public key, pre-shared key, endpoint, allowed IPs, latest handshake, bytes
// received and sent, and keepalive, tab-separated, "(none)" for an empty value.
func ParseWireGuardDump(data []byte, now time.Time) (WireGuardStatus, error) {
	status := WireGuardStatus{Interfaces: []WireGuardRuntime{}}
	none := func(s string) string {
		if s == "(none)" {
			return ""
		}
		return s
	}
	for n, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		bad := fmt.Errorf("wg dump line %d: unexpected %d fields", n+1, len(f))
		switch len(f) {
		case 5:
			port, err := strconv.Atoi(f[3])
			if err != nil {
				return status, bad
			}
			status.Interfaces = append(status.Interfaces, WireGuardRuntime{Ifname: f[0], PublicKey: none(f[2]), ListenPort: port, Peers: []WireGuardPeerRuntime{}})
		case 9:
			last := len(status.Interfaces) - 1
			if last < 0 || status.Interfaces[last].Ifname != f[0] {
				return status, fmt.Errorf("wg dump line %d: a peer before its interface", n+1)
			}
			handshake, err1 := strconv.ParseInt(f[5], 10, 64)
			rx, err2 := strconv.ParseUint(f[6], 10, 64)
			tx, err3 := strconv.ParseUint(f[7], 10, 64)
			if err1 != nil || err2 != nil || err3 != nil {
				return status, bad
			}
			ips := []string{}
			if allowed := none(f[4]); allowed != "" {
				ips = strings.Split(allowed, ",")
			}
			age := now.Sub(time.Unix(handshake, 0))
			online := handshake > 0 && age >= 0 && age <= WireGuardOnline
			status.Interfaces[last].Peers = append(status.Interfaces[last].Peers, WireGuardPeerRuntime{PublicKey: f[1], Endpoint: none(f[3]), AllowedIPs: ips,
				LatestHandshake: handshake, RxBytes: rx, TxBytes: tx, Online: online})
		default:
			return status, bad
		}
	}
	return status, nil
}
