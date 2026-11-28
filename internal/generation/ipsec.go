package generation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	ipsecDir              = "/run/andaru-ipsec"
	ipsecConf             = ipsecDir + "/swanctl.conf"
	ipsecReloadPending    = ipsecDir + "/.reload-pending"
	ipsecTerminatePending = ipsecDir + "/.terminate-pending"
	strongswanUnit        = "strongswan.service"
	swanctl               = "/usr/bin/swanctl"
	restorecon            = "/usr/sbin/restorecon"
)

// ipsecCredential is a CA, intermediate chain certificate, leaf certificate or certificate key in the directories
// swanctl --file loads beside swanctl.conf.
var ipsecCredential = regexp.MustCompile(`^` + ipsecDir + `/(x509/crt_` + uuidPattern + `\.pem|x509ca/(ca_` + uuidPattern + `|crt_` + uuidPattern + `-chain-[0-9]+)\.pem|private/crt_` + uuidPattern + `\.key)$`)

const uuidPattern = `[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`

// ipsecConnName is a connection ID, which names its connection, child and IKE SAs in swanctl.conf.
var ipsecConnName = regexp.MustCompile(`^ips_` + uuidPattern + `$`)

// ValidIPsecConnection says whether name is a connection swanctl.conf may hold.
func ValidIPsecConnection(name string) bool { return ipsecConnName.MatchString(name) }

func isIPsecFile(path string) bool { return path == ipsecConf || ipsecCredential.MatchString(path) }

// ipsecConnectionFingerprints returns a per-connection digest covering its swanctl.conf connection, pool and secret
// blocks plus the digests of any credential files it references, along with each connection's start_action.
func ipsecConnectionFingerprints(files []placed) (digests map[string]string, startActions map[string]string) {
	digests = map[string]string{}
	startActions = map[string]string{}
	conf, ok := find(files, ipsecConf)
	if !ok {
		return digests, startActions
	}
	bySHA := map[string]string{}
	for _, f := range files {
		if isIPsecFile(f.path) {
			if f.sha256 != "" {
				bySHA[f.path] = f.sha256
			} else {
				sum := sha256.Sum256(f.data)
				bySHA[f.path] = hex.EncodeToString(sum[:])
			}
		}
	}
	sections := map[string][]string{}
	var topSection, blockConn string
	depth := 0
	for line := range strings.SplitSeq(string(conf.data), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case depth == 0 && strings.HasSuffix(trimmed, " {"):
			topSection = strings.TrimSuffix(trimmed, " {")
			depth = 1
		case depth == 1 && trimmed == "}":
			topSection = ""
			depth = 0
		case depth == 1 && strings.HasSuffix(trimmed, " {"):
			name := strings.TrimSuffix(trimmed, " {")
			blockConn = ipsecBlockOwner(topSection, name)
			depth = 2
			if blockConn != "" {
				sections[blockConn] = append(sections[blockConn], topSection+":"+line)
			}
		case depth >= 2:
			if blockConn != "" {
				sections[blockConn] = append(sections[blockConn], line)
				if act, ok := strings.CutPrefix(trimmed, "start_action = "); ok {
					startActions[blockConn] = act
				}
			}
			if strings.HasSuffix(trimmed, " {") {
				depth++
			} else if trimmed == "}" {
				depth--
				if depth == 1 {
					blockConn = ""
				}
			}
		}
	}
	for id, lines := range sections {
		h := sha256.New()
		for _, line := range lines {
			h.Write([]byte(line))
			h.Write([]byte{'\n'})
			trimmed := strings.TrimSpace(line)
			for _, prefix := range []string{"certs = ", "cacerts = "} {
				if refPath, ok := strings.CutPrefix(trimmed, prefix); ok {
					if digest, exists := bySHA[refPath]; exists {
						h.Write([]byte(refPath + "=" + digest + "\n"))
					}
					if certID, ok := strings.CutPrefix(refPath, ipsecDir+"/x509/"); ok {
						certID = strings.TrimSuffix(certID, ".pem")
						keyPath := ipsecDir + "/private/" + certID + ".key"
						if digest, exists := bySHA[keyPath]; exists {
							h.Write([]byte(keyPath + "=" + digest + "\n"))
						}
						for n := 0; ; n++ {
							chainPath := fmt.Sprintf("%s/x509ca/%s-chain-%d.pem", ipsecDir, certID, n)
							digest, exists := bySHA[chainPath]
							if !exists {
								break
							}
							h.Write([]byte(chainPath + "=" + digest + "\n"))
						}
					}
				}
			}
		}
		digests[id] = hex.EncodeToString(h.Sum(nil))
	}
	return digests, startActions
}

// ipsecBlockOwner maps a top-level swanctl.conf subsection name back to its ips_<uuid> connection ID.
func ipsecBlockOwner(section, name string) string {
	switch section {
	case "connections":
		if ipsecConnName.MatchString(name) {
			return name
		}
	case "pools":
		if i := strings.LastIndexByte(name, '-'); i > 0 && ipsecConnName.MatchString(name[:i]) {
			return name[:i]
		}
	case "secrets":
		if rest, ok := strings.CutPrefix(name, "ike-"); ok && ipsecConnName.MatchString(rest) {
			return rest
		}
		if rest, ok := strings.CutPrefix(name, "eap-"); ok {
			if i := strings.LastIndexByte(rest, '-'); i > 0 && ipsecConnName.MatchString(rest[:i]) {
				return rest[:i]
			}
		}
	}
	return ""
}

// ipsecCredentialDirs makes the directories swanctl --file scans beside swanctl.conf, enforces 0700 permissions,
// and labels them for strongSwan where SELinux is installed.
func (r Realizer) ipsecCredentialDirs(ctx context.Context) error {
	if err := os.MkdirAll(r.Root+ipsecDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(r.Root+ipsecDir, 0o700); err != nil {
		return err
	}
	for _, dir := range []string{"x509", "x509ca", "private"} {
		full := r.Root + ipsecDir + "/" + dir
		if err := os.MkdirAll(full, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(full, 0o700); err != nil {
			return err
		}
	}
	if _, err := os.Stat(r.Root + restorecon); err == nil {
		if _, err := r.Run(ctx, restorecon, "-RF", ipsecDir); err != nil {
			return fmt.Errorf("label %s: %w", ipsecDir, err)
		}
	}
	return nil
}

// ipsecRuntimeEntries are what Andaru places under ipsecDir; tmpfiles.d owns the directory itself.
var ipsecRuntimeEntries = []string{ipsecConf, ipsecReloadPending, ipsecTerminatePending, ipsecDir + "/x509", ipsecDir + "/x509ca", ipsecDir + "/private"}

// removeIPsecRuntimeFiles wipes swanctl.conf, pending markers, and credential directories under ipsecDir.
func (r Realizer) removeIPsecRuntimeFiles() error {
	for _, path := range ipsecRuntimeEntries {
		if err := os.RemoveAll(r.Root + path); err != nil {
			return err
		}
	}
	return nil
}

// cleanupIPsecOnTeardownIfPresent stops strongSwan (including queued or activating starts) and wipes /run/andaru-ipsec
// files when Andaru placed IPsec files on disk, without touching hosts where Andaru never ran IPsec.
func (r Realizer) cleanupIPsecOnTeardownIfPresent(ctx context.Context) error {
	// The directory alone proves nothing: tmpfiles.d creates it at boot on every host.
	if !slices.ContainsFunc(ipsecRuntimeEntries, func(path string) bool {
		_, err := os.Lstat(r.Root + path)
		return !errors.Is(err, fs.ErrNotExist)
	}) {
		return nil
	}
	if _, err := r.Run(ctx, "/usr/bin/systemctl", "stop", strongswanUnit); err != nil {
		return fmt.Errorf("stop strongSwan: %w", err)
	}
	return r.removeIPsecRuntimeFiles()
}

func (r Realizer) readPendingTerminations() ([]string, error) {
	raw, err := os.ReadFile(r.Root + ipsecTerminatePending)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("read pending IPsec terminations: %w", err)
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil, fmt.Errorf("read pending IPsec terminations: %w", err)
	}
	return ids, nil
}

func (r Realizer) writePendingTerminations(ids []string) error {
	path := r.Root + ipsecTerminatePending
	if len(ids) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	raw, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return writeAtomic(path, append(raw, '\n'), 0o600)
}

// ipsec starts strongSwan on the connections to declares, or reloads them into a running one, and ends the SAs of
// removed or changed connections; when to declares none, strongSwan stops and its credentials go.
func (r Realizer) ipsec(ctx context.Context, from, to []placed) error {
	_, want := find(to, ipsecConf)
	_, had := find(from, ipsecConf)
	if !want {
		if !had {
			return r.cleanupIPsecOnTeardownIfPresent(ctx)
		}
		if _, err := r.Run(ctx, "/usr/bin/systemctl", "stop", strongswanUnit); err != nil {
			return fmt.Errorf("stop strongSwan: %w", err)
		}
		return r.removeIPsecRuntimeFiles()
	}
	if err := r.ipsecCredentialDirs(ctx); err != nil {
		return err
	}
	if _, err := r.Run(ctx, "/usr/bin/systemctl", "is-active", "--quiet", strongswanUnit); err != nil {
		// Its drop-in loads swanctl.conf once charon runs.
		if _, err := r.Run(ctx, "/usr/bin/systemctl", "start", strongswanUnit); err != nil {
			return fmt.Errorf("start strongSwan: %w", err)
		}
		_ = os.Remove(r.Root + ipsecReloadPending)
		_ = os.Remove(r.Root + ipsecTerminatePending)
		return nil
	}
	// An unreadable marker may hide SAs that must end; ending every SA is safe, failing every apply is not.
	pending, err := r.readPendingTerminations()
	endAll := err != nil
	if endAll {
		slog.Warn("end every IPsec SA: the pending terminations are unreadable", "error", err)
	}
	// Without the replaced swanctl.conf's content no change is provable, so none is assumed absent.
	if old, ok := find(from, ipsecConf); ok && old.data == nil {
		endAll = true
		slog.Warn("end every IPsec SA: the replaced connections are unreadable")
	}
	if _, err := r.Run(ctx, swanctl, "--load-all", "--clear", "--file", ipsecConf, "--noprompt"); err != nil {
		return fmt.Errorf("load the IPsec connections: %w", err)
	}
	// --clear unloads a removed connection and updates changed configs for future handshakes, but live SAs stay up
	// (including on stale xfrmIfId, shifted reqid, or rotated secrets) until they are ended.
	oldFP, _ := ipsecConnectionFingerprints(from)
	newFP, newStart := ipsecConnectionFingerprints(to)
	pendingSet := map[string]bool{}
	for _, id := range pending {
		pendingSet[id] = true
	}
	for id, oldDigest := range oldFP {
		if newDigest, kept := newFP[id]; !kept || newDigest != oldDigest {
			pendingSet[id] = true
		}
	}
	// Under endAll a partial record would replace the unreadable marker and lose the intent before the SAs are known.
	if len(pendingSet) > 0 && !endAll {
		if err := r.writePendingTerminations(slices.Sorted(maps.Keys(pendingSet))); err != nil {
			return err
		}
	}
	out, err := r.Run(ctx, swanctl, "--list-sas", "--pretty")
	if err != nil {
		return fmt.Errorf("list the IPsec SAs: %w", err)
	}
	sas, err := ParseSwanctlSAs(out)
	if err != nil {
		return fmt.Errorf("read the IPsec SAs: %w", err)
	}
	if endAll {
		for _, sa := range sas {
			pendingSet[sa.Name] = true
		}
		if err := r.writePendingTerminations(slices.Sorted(maps.Keys(pendingSet))); err != nil {
			return err
		}
	}
	var ended []string
	var reinit []string
	failedOwners := map[string]bool{}
	var termErr error
	for _, sa := range sas {
		_, kept := newFP[sa.Name]
		if (!kept || endAll || pendingSet[sa.Name]) && !slices.Contains(ended, sa.Name) {
			ended = append(ended, sa.Name)
			// --force deletes the SA locally without waiting on the peer, so a dead peer cannot fail the apply.
			if _, err := r.Run(ctx, swanctl, "--terminate", "--ike", sa.Name, "--force", "--timeout", "5"); err != nil {
				failedOwners[sa.Name] = true
				if termErr == nil {
					termErr = fmt.Errorf("end the SAs of IPsec connection %s: %w", sa.Name, err)
				}
				continue
			}
			if kept && newStart[sa.Name] == "start" {
				reinit = append(reinit, sa.Name)
			}
		}
	}
	if err := r.writePendingTerminations(slices.Sorted(maps.Keys(failedOwners))); err != nil && termErr == nil {
		termErr = err
	}
	if termErr != nil {
		return termErr
	}
	_ = os.Remove(r.Root + ipsecReloadPending)
	for _, id := range reinit {
		if _, err := r.Run(ctx, swanctl, "--initiate", "--child", id, "--timeout", "5"); err != nil {
			slog.Warn("re-initiate a changed IPsec connection", "connection", id, "error", err)
		}
	}
	return nil
}

// bootIPsec starts strongSwan behind the boot that placed its files: strongswan.service is ordered after
// andaru-network-boot, so it is queued, not waited for.
func (r Realizer) bootIPsec(ctx context.Context, set []placed) error {
	if _, ok := find(set, ipsecConf); !ok {
		return nil
	}
	if err := r.ipsecCredentialDirs(ctx); err != nil {
		return err
	}
	_, err := r.Run(ctx, "/usr/bin/systemctl", "start", "--no-block", strongswanUnit)
	return err
}

// IPsecStatus is what strongSwan reports: whether it runs, the connections it holds, and their SAs.
type IPsecStatus struct {
	ServiceActive bool      `json:"serviceActive"`
	Connections   []string  `json:"connections"`
	SAs           []IPsecSA `json:"sas"`
}

// IPsecSA is one IKE SA; Name is its connection's.
type IPsecSA struct {
	Name           string         `json:"name"`
	UniqueID       string         `json:"uniqueId"`
	State          string         `json:"state"`
	LocalHost      string         `json:"localHost"`
	RemoteHost     string         `json:"remoteHost"`
	EstablishedSec int64          `json:"establishedSec"`
	ChildSAs       []IPsecChildSA `json:"childSAs"`
}

type IPsecChildSA struct {
	Name     string   `json:"name"`
	State    string   `json:"state"`
	LocalTS  []string `json:"localTs"`
	RemoteTS []string `json:"remoteTs"`
	BytesIn  uint64   `json:"bytesIn"`
	BytesOut uint64   `json:"bytesOut"`
	RekeySec int64    `json:"rekeySec"`
}

// ReadIPsecStatus asks systemd whether strongSwan runs and, when it does, swanctl for its connections and SAs. It
// takes no lock: it changes nothing.
func ReadIPsecStatus(ctx context.Context, run func(ctx context.Context, name string, args ...string) ([]byte, error)) (IPsecStatus, error) {
	status := IPsecStatus{Connections: []string{}, SAs: []IPsecSA{}}
	// is-active exits non-zero for anything but active; what it prints says which.
	out, _ := run(ctx, "/usr/bin/systemctl", "is-active", strongswanUnit)
	if strings.TrimSpace(string(out)) != "active" {
		return status, nil
	}
	status.ServiceActive = true
	out, err := run(ctx, swanctl, "--list-conns", "--pretty")
	if err != nil {
		return status, err
	}
	conns, err := parseVici(out)
	if err != nil {
		return status, err
	}
	for _, event := range conns.kids {
		if event.name != "list-conn event" {
			continue
		}
		for _, c := range event.kids {
			status.Connections = append(status.Connections, c.name)
		}
	}
	if out, err = run(ctx, swanctl, "--list-sas", "--pretty"); err != nil {
		return status, err
	}
	status.SAs, err = ParseSwanctlSAs(out)
	return status, err
}

// viciNode is a section of swanctl's pretty-printed VICI messages: its values, its lists and its subsections in order.
type viciNode struct {
	name   string
	values map[string]string
	lists  map[string][]string
	kids   []*viciNode
}

// cutViciKV parses "key = value" or an empty-valued "key =" (after TrimSpace stripped the trailing space) before
// section-header matching so a value containing " {" or " = [" (such as a remote peer's DN) cannot spoof a section.
func cutViciKV(line string) (key, val string, ok bool) {
	if k, v, found := strings.Cut(line, " = "); found {
		return k, v, true
	}
	if k, found := strings.CutSuffix(line, " ="); found {
		return k, "", true
	}
	return "", "", false
}

// parseVici reads swanctl --pretty output: "name {" opens a section and "}" ends it, "key = [" opens a list of one
// item per line that "]" ends, and "key = value" (or "key =") is a value. Messages are the top-level sections.
func parseVici(data []byte) (*viciNode, error) {
	root := &viciNode{values: map[string]string{}, lists: map[string][]string{}}
	stack := []*viciNode{root}
	list := ""
	for n, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		top := stack[len(stack)-1]
		switch {
		case line == "":
		case list != "":
			if line == "]" {
				list = ""
			} else {
				top.lists[list] = append(top.lists[list], line)
			}
		case line == "}":
			if len(stack) == 1 {
				return nil, fmt.Errorf("swanctl output line %d: unbalanced }", n+1)
			}
			stack = stack[:len(stack)-1]
		default:
			if key, val, ok := cutViciKV(line); ok {
				if val == "[" {
					list = key
					top.lists[key] = []string{}
				} else {
					top.values[key] = val
				}
			} else if name, ok := strings.CutSuffix(line, " {"); ok {
				kid := &viciNode{name: name, values: map[string]string{}, lists: map[string][]string{}}
				top.kids = append(top.kids, kid)
				stack = append(stack, kid)
			} else {
				return nil, fmt.Errorf("swanctl output line %d: unexpected %q", n+1, line)
			}
		}
	}
	if len(stack) != 1 || list != "" {
		return nil, errors.New("swanctl output ends inside a section")
	}
	return root, nil
}

// ParseSwanctlSAs reads swanctl --list-sas --pretty: a list-sa event per IKE SA, named by its connection, with its
// CHILD_SAs under child-sas.
func ParseSwanctlSAs(data []byte) ([]IPsecSA, error) {
	root, err := parseVici(data)
	if err != nil {
		return nil, err
	}
	number := func(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }
	unsigned := func(s string) uint64 { n, _ := strconv.ParseUint(s, 10, 64); return n }
	out := []IPsecSA{}
	for _, event := range root.kids {
		if event.name != "list-sa event" {
			continue
		}
		for _, ike := range event.kids {
			sa := IPsecSA{Name: ike.name, UniqueID: ike.values["uniqueid"], State: ike.values["state"], LocalHost: ike.values["local-host"],
				RemoteHost: ike.values["remote-host"], EstablishedSec: number(ike.values["established"]), ChildSAs: []IPsecChildSA{}}
			for _, section := range ike.kids {
				if section.name != "child-sas" {
					continue
				}
				for _, child := range section.kids {
					sa.ChildSAs = append(sa.ChildSAs, IPsecChildSA{Name: child.values["name"], State: child.values["state"],
						LocalTS: nonNil(child.lists["local-ts"]), RemoteTS: nonNil(child.lists["remote-ts"]),
						BytesIn: unsigned(child.values["bytes-in"]), BytesOut: unsigned(child.values["bytes-out"]), RekeySec: number(child.values["rekey-time"])})
				}
			}
			out = append(out, sa)
		}
	}
	return out, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ControlIPsec initiates a connection's CHILD_SA, which brings its IKE SA up first, or terminates its IKE SA with its
// CHILD_SAs. It takes no lock: strongSwan's own state is all it changes.
func ControlIPsec(ctx context.Context, run func(ctx context.Context, name string, args ...string) ([]byte, error), action, name string) error {
	if !ValidIPsecConnection(name) {
		return fmt.Errorf("invalid IPsec connection %q", name)
	}
	switch action {
	case "initiate":
		_, err := run(ctx, swanctl, "--initiate", "--child", name, "--timeout", "15")
		return err
	case "terminate":
		_, err := run(ctx, swanctl, "--terminate", "--ike", name, "--timeout", "15")
		return err
	}
	return fmt.Errorf("unknown IPsec action %q", action)
}

// ipsecKey is a certificate's private key under private/.
func ipsecKey(path string) bool {
	return strings.HasPrefix(path, ipsecDir+"/private/") && ipsecCredential.MatchString(path)
}
