package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// RealizerFacts are host observations that decide whether rendered network
// files could work here. They are read-only and carry no configuration.
type RealizerFacts struct {
	// ForeignFiles are networkd files Andaru did not render, as sorted absolute paths.
	ForeignFiles []string
	PPPDaemon    bool
	PPPoEPlugin  bool
	// PPPDevice reports /dev/ppp; Debian cloud kernels ship without PPP.
	PPPDevice bool
	// ResolvedInCharge reports that systemd-resolved manages /etc/resolv.conf, so DNS= in a .network file is used.
	ResolvedInCharge bool
}

// ReadRealizerFacts inspects the host without changing it.
func ReadRealizerFacts() (RealizerFacts, error) {
	return readRealizerFacts("/etc/systemd/network", "/run/systemd/network", "/usr/sbin/pppd", "/usr/lib/pppd/*/pppoe.so", "/dev/ppp", "")
}

func readRealizerFacts(etcDir, runDir, pppd, pluginGlob, device, root string) (RealizerFacts, error) {
	facts := RealizerFacts{ForeignFiles: []string{}}
	for _, dir := range []string{etcDir, runDir} {
		entries, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return RealizerFacts{}, fmt.Errorf("list network configuration: %w", err)
		}
		for _, entry := range entries {
			name := entry.Name()
			base, dropInDir := strings.CutSuffix(name, ".d")
			if !slices.Contains([]string{".network", ".netdev", ".link"}, filepath.Ext(base)) {
				continue
			}
			if dropInDir {
				// networkd merges <file>.d/*.conf over the file from any directory, so a drop-in overrides even Andaru's own files.
				dropIns, err := filepath.Glob(filepath.Join(dir, name, "*.conf"))
				if err != nil {
					return RealizerFacts{}, fmt.Errorf("list network configuration: %w", err)
				}
				facts.ForeignFiles = append(facts.ForeignFiles, dropIns...)
				continue
			}
			// networkd reads /etc before /run, so anything there overrides Andaru; in /run only Andaru's prefix is its own.
			if !entry.IsDir() && (dir == etcDir || !strings.HasPrefix(name, "50-andaru-")) {
				facts.ForeignFiles = append(facts.ForeignFiles, filepath.Join(dir, name))
			}
		}
	}
	slices.Sort(facts.ForeignFiles)
	exists := func(path string) bool { _, err := os.Stat(path); return err == nil }
	plugins, _ := filepath.Glob(pluginGlob)
	facts.PPPDaemon, facts.PPPoEPlugin, facts.PPPDevice = exists(pppd), len(plugins) > 0, exists(device)
	facts.ResolvedInCharge = ResolvedInCharge(root)
	return facts, nil
}

// ResolvedInCharge reports whether <root>/etc/resolv.conf, with every symbolic link followed inside root,
// is a file systemd-resolved maintains under /run. networkd only hands DNS servers to resolved; nothing else
// reads them. resolved's static file under /usr/lib exists whether or not resolved runs, so it does not count.
func ResolvedInCharge(root string) bool {
	path := "/etc/resolv.conf"
	for range 8 {
		info, err := os.Lstat(root + path)
		if err != nil {
			return false
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return info.Mode().IsRegular() && strings.HasPrefix(path, "/run/systemd/resolve/")
		}
		target, err := os.Readlink(root + path)
		if err != nil {
			return false
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		path = filepath.Clean(target)
	}
	return false
}

// Managed is a link systemd-networkd configures and the file it took the configuration from.
type Managed struct {
	Name, NetworkFile string
	// State is networkd's administrative state; "configured" once the file has been applied.
	State string
}

// ParseManaged reads `networkctl list --json=short`, keeping only links bound to a .network file.
func ParseManaged(data []byte) ([]Managed, error) {
	var reply struct {
		Interfaces []struct {
			Name, NetworkFile, AdministrativeState string
		}
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		return nil, fmt.Errorf("decode networkd link list: %w", err)
	}
	if reply.Interfaces == nil {
		return nil, errors.New("networkd link list has no Interfaces")
	}
	managed := []Managed{}
	for _, link := range reply.Interfaces {
		if link.NetworkFile != "" {
			managed = append(managed, Managed{Name: link.Name, NetworkFile: link.NetworkFile, State: link.AdministrativeState})
		}
	}
	return managed, nil
}

// ReadManaged asks the running networkd which links it configures.
func ReadManaged(ctx context.Context) ([]Managed, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	data, err := readCommand(ctx, "/usr/bin/networkctl", "list", "--json=short")
	if err != nil {
		return nil, err
	}
	return ParseManaged(data)
}

// Baseline describes who configured the network before Andaru, as far as a takeover must know.
type Baseline struct {
	Netplan  bool
	Ifupdown bool
	// BootUnit reports that the unit which puts the confirmed generation into /run at boot is enabled.
	BootUnit bool
	// StrayYAML is netplan configuration outside /etc/netplan, which a takeover would not move away.
	StrayYAML []string
	// StaticDNS lists netplan-generated .network files that set DNS= or Domains=; a takeover must declare DNS for their interfaces.
	StaticDNS []string
}

// ReadBaseline inspects the host without changing it.
func ReadBaseline() (Baseline, error) { return readBaseline("") }

func readBaseline(root string) (Baseline, error) {
	exists := func(path string) bool { _, err := os.Stat(root + path); return err == nil }
	// A masked unit is a link to /dev/null, which exists but is no unit file; the mask may also sit beside the wants link.
	wanted, err := os.Stat(root + "/etc/systemd/system/sysinit.target.wants/andaru-network-boot.service")
	override, maskErr := os.Stat(root + "/etc/systemd/system/andaru-network-boot.service")
	b := Baseline{
		Netplan: exists("/usr/sbin/netplan"), Ifupdown: exists("/sbin/ifup"),
		BootUnit:  err == nil && wanted.Mode().IsRegular() && (maskErr != nil || override.Mode().IsRegular()),
		StrayYAML: []string{}, StaticDNS: []string{},
	}
	for _, dir := range []string{"/run/netplan", "/lib/netplan", "/usr/lib/netplan"} {
		found, err := filepath.Glob(root + dir + "/*.yaml")
		if err != nil {
			return Baseline{}, err
		}
		for _, path := range found {
			b.StrayYAML = append(b.StrayYAML, strings.TrimPrefix(path, root))
		}
	}
	generated, err := filepath.Glob(root + "/run/systemd/network/10-netplan-*.network")
	if err != nil {
		return Baseline{}, err
	}
	for _, path := range generated {
		data, err := os.ReadFile(path)
		if err != nil {
			return Baseline{}, fmt.Errorf("read baseline network file: %w", err)
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			if strings.HasPrefix(line, "DNS=") || strings.HasPrefix(line, "Domains=") {
				b.StaticDNS = append(b.StaticDNS, strings.TrimPrefix(path, root))
				break
			}
		}
	}
	slices.Sort(b.StrayYAML)
	slices.Sort(b.StaticDNS)
	return b, nil
}

var resolvedLink = regexp.MustCompile(`^Link \d+ \((.+)\):(.*)$`)

// ParseResolvedDNS reads `resolvectl dns`: the servers systemd-resolved uses per interface, in order.
// The Global line is ignored: Andaru configures servers per link only. A server in a form Andaru never
// writes (a port, a TLS name) becomes the zero Addr: it equals no declared server, and it cannot spoil
// the reading of another link.
func ParseResolvedDNS(data []byte) (map[string][]netip.Addr, error) {
	servers := map[string][]netip.Addr{}
	// current is the interface whose list is being read; "" inside the global list, "\x00" before any header.
	current := "\x00"
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := line
		switch {
		case strings.TrimSpace(line) == "":
			continue
		case strings.HasPrefix(line, "Global:"):
			current = ""
			continue
		case line[0] == ' ' || line[0] == '\t':
			// resolvectl wraps a long list onto indented lines.
			if current == "\x00" {
				return nil, fmt.Errorf("unexpected resolvectl line %q", line)
			}
		default:
			m := resolvedLink.FindStringSubmatch(line)
			if m == nil {
				return nil, fmt.Errorf("unexpected resolvectl line %q", line)
			}
			current, fields = m[1], m[2]
			servers[current] = []netip.Addr{}
		}
		if current == "" {
			continue
		}
		for _, field := range strings.Fields(fields) {
			a, _ := netip.ParseAddr(field)
			servers[current] = append(servers[current], a)
		}
	}
	return servers, nil
}

// ReadDefaultRoute names the interface carrying the IPv4 default route, or "" when there is none.
func ReadDefaultRoute(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	data, err := readCommand(ctx, "/usr/bin/ip", "-j", "route", "show", "default")
	if err != nil {
		return "", err
	}
	var routes []struct {
		Dev string `json:"dev"`
	}
	if err := json.Unmarshal(data, &routes); err != nil {
		return "", fmt.Errorf("decode default routes: %w", err)
	}
	if len(routes) == 0 {
		return "", nil
	}
	return routes[0].Dev, nil
}
