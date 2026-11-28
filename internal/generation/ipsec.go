package generation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	ipsecDir       = "/run/andaru-ipsec"
	ipsecConf      = ipsecDir + "/swanctl.conf"
	strongswanUnit = "strongswan.service"
	swanctl        = "/usr/bin/swanctl"
	restorecon     = "/usr/sbin/restorecon"
)

// ipsecCredential is a CA or certificate swanctl.conf names, or a certificate's key, in the directories swanctl --file
// loads beside it.
var ipsecCredential = regexp.MustCompile(`^` + ipsecDir + `/(x509/crt_` + uuidPattern + `\.pem|x509ca/ca_` + uuidPattern + `\.pem|private/crt_` + uuidPattern + `\.key)$`)

const uuidPattern = `[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`

// ipsecConnName is a connection ID, which names its connection, child and IKE SAs in swanctl.conf.
var ipsecConnName = regexp.MustCompile(`^ips_` + uuidPattern + `$`)

// ValidIPsecConnection says whether name is a connection swanctl.conf may hold.
func ValidIPsecConnection(name string) bool { return ipsecConnName.MatchString(name) }

func isIPsecFile(path string) bool { return path == ipsecConf || ipsecCredential.MatchString(path) }

// ipsecConnections are the connections a rendered swanctl.conf declares: the sections one level into connections.
func ipsecConnections(conf []byte) []string {
	var out []string
	for line := range strings.SplitSeq(string(conf), "\n") {
		if name, ok := strings.CutSuffix(line, " {"); ok && strings.Count(name, "\t") == 1 && ipsecConnName.MatchString(name[1:]) {
			out = append(out, name[1:])
		}
	}
	return out
}

// ipsecCredentialDirs makes the directories swanctl --file scans beside swanctl.conf, and labels them for strongSwan
// where SELinux is installed.
func (r Realizer) ipsecCredentialDirs(ctx context.Context) error {
	for _, dir := range []string{"x509", "x509ca", "private"} {
		if err := os.MkdirAll(r.Root+ipsecDir+"/"+dir, 0o700); err != nil {
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

// ipsec starts strongSwan on the connections to declares, or reloads them into a running one, and ends the SAs of
// connections it no longer declares; when to declares none, strongSwan stops and its credentials go.
func (r Realizer) ipsec(ctx context.Context, from, to []placed) error {
	conf, want := find(to, ipsecConf)
	_, had := find(from, ipsecConf)
	if !want {
		if !had {
			return nil
		}
		if _, err := r.Run(ctx, "/usr/bin/systemctl", "stop", strongswanUnit); err != nil {
			return fmt.Errorf("stop strongSwan: %w", err)
		}
		for _, dir := range []string{"x509", "x509ca", "private"} {
			if err := os.RemoveAll(r.Root + ipsecDir + "/" + dir); err != nil {
				return err
			}
		}
		return nil
	}
	if err := r.ipsecCredentialDirs(ctx); err != nil {
		return err
	}
	if _, err := r.Run(ctx, "/usr/bin/systemctl", "is-active", "--quiet", strongswanUnit); err != nil {
		// Its drop-in loads swanctl.conf once charon runs.
		if _, err := r.Run(ctx, "/usr/bin/systemctl", "start", strongswanUnit); err != nil {
			return fmt.Errorf("start strongSwan: %w", err)
		}
		return nil
	}
	if _, err := r.Run(ctx, swanctl, "--load-all", "--clear", "--file", ipsecConf, "--noprompt"); err != nil {
		return fmt.Errorf("load the IPsec connections: %w", err)
	}
	// --clear unloads a removed connection, but its SAs stay up until they are ended.
	out, err := r.Run(ctx, swanctl, "--list-sas", "--pretty")
	if err != nil {
		slog.Warn("list the IPsec SAs", "error", err)
		return nil
	}
	sas, err := ParseSwanctlSAs(out)
	if err != nil {
		slog.Warn("read the IPsec SAs", "error", err)
		return nil
	}
	keep := ipsecConnections(conf.data)
	var ended []string
	for _, sa := range sas {
		if !slices.Contains(keep, sa.Name) && !slices.Contains(ended, sa.Name) {
			ended = append(ended, sa.Name)
			if _, err := r.Run(ctx, swanctl, "--terminate", "--ike", sa.Name, "--timeout", "5"); err != nil {
				slog.Warn("end the SAs of a removed IPsec connection", "connection", sa.Name, "error", err)
			}
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

// parseVici reads swanctl --pretty output: "name {" opens a section and "}" ends it, "key = [" opens a list of one
// item per line that "]" ends, and "key = value" is a value. Messages are the top-level sections.
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
		case strings.HasSuffix(line, " = ["):
			list = strings.TrimSuffix(line, " = [")
			top.lists[list] = []string{}
		case strings.HasSuffix(line, " {"):
			kid := &viciNode{name: strings.TrimSuffix(line, " {"), values: map[string]string{}, lists: map[string][]string{}}
			top.kids = append(top.kids, kid)
			stack = append(stack, kid)
		default:
			key, value, ok := strings.Cut(line, " = ")
			if !ok {
				return nil, fmt.Errorf("swanctl output line %d: unexpected %q", n+1, line)
			}
			top.values[key] = value
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
