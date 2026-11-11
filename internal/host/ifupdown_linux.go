package host

import (
	"cmp"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// DHCPIdentity is how dhcpcd identifies the host to DHCP servers; networkd presents the same so leases survive.
type DHCPIdentity struct {
	DUID     []byte // dhcpcd's DUID, type bytes first
	Hostname string // the stanza's hostname option, "" if none
}

// IfupdownInterface is one interface ifupdown configures.
type IfupdownInterface struct {
	Detected
	Hotplug  bool          // allow-hotplug; false means auto (or neither)
	DHCP     bool          // inet dhcp
	Identity *DHCPIdentity // set when DHCP
	// KernelSLAAC: no inet6 stanza, but the kernel took a SLAAC address; it says nothing about DNS servers.
	KernelSLAAC bool
}

const (
	ifupdownDir     = "/etc/network"
	dhcpcdConf      = "/etc/dhcpcd.conf"
	dhcpcdDUID      = "/var/lib/dhcpcd/duid"
	sourceDepth     = 8
	identityUnknown = "DHCP_IDENTITY_UNKNOWN"
)

// ifupdown's own rule for files a source-directory includes.
var sourceDirName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

type stanza struct {
	file, name, family, method string
	options                    map[string]string
}

type ifupdownFiles struct {
	root          string
	auto, hotplug map[string]bool
	stanzas       []*stanza
	seen          map[string]bool
	problems      []Problem
}

func (f *ifupdownFiles) problem(file, key, message string) {
	f.problems = append(f.problems, Problem{File: file, Key: key, Message: message})
}

// read parses one file and everything it sources. Only the shapes the Debian installer writes are accepted.
func (f *ifupdownFiles) read(path string, depth int) {
	if depth > sourceDepth {
		f.problem(path, "source", "Sources nest more than 8 levels deep.")
		return
	}
	// Only a file sourcing one of the files that led to it loops; reading one file twice does not.
	if f.seen[path] {
		f.problem(path, "source", "This file sources itself through its includes, which would loop.")
		return
	}
	f.seen[path] = true
	defer delete(f.seen, path)
	info, err := os.Lstat(f.root + path)
	if depth == 0 && errors.Is(err, fs.ErrNotExist) {
		f.problem(path, "", "ifupdown's configuration file does not exist.")
		return
	}
	if err != nil || !info.Mode().IsRegular() {
		f.problem(path, "source", "Only regular files can be sourced, not symbolic links or anything else.")
		return
	}
	data, err := os.ReadFile(f.root + path)
	if err != nil {
		f.problem(path, "", err.Error())
		return
	}
	if strings.Contains(filepath.Base(path), "cloud-init") {
		f.problem(path, "", "cloud-init wrote this file and would write it again.")
	}
	var current *stanza
	text := strings.ReplaceAll(string(data), "\\\n", " ")
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		words := strings.Fields(line)
		switch words[0] {
		case "auto", "allow-auto":
			for _, n := range words[1:] {
				f.auto[n] = true
			}
			current = nil
		case "allow-hotplug":
			for _, n := range words[1:] {
				f.hotplug[n] = true
			}
			current = nil
		case "source", "source-directory":
			current = nil
			if len(words) != 2 {
				f.problem(path, words[0], "Unreadable line: "+line)
				continue
			}
			// A relative path is relative to the directory of the file naming it, and either form may be a glob.
			target := words[1]
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(path), target)
			}
			found, _ := filepath.Glob(f.root + target)
			var matches []string
			for _, m := range found {
				m = strings.TrimPrefix(m, f.root)
				if words[0] == "source" {
					matches = append(matches, m)
					continue
				}
				entries, err := os.ReadDir(f.root + m)
				if err != nil {
					f.problem(m, "source-directory", err.Error())
				}
				for _, e := range entries {
					if sourceDirName.MatchString(e.Name()) {
						matches = append(matches, filepath.Join(m, e.Name()))
					}
				}
			}
			slices.Sort(matches)
			for _, m := range matches {
				f.read(m, depth+1)
			}
		case "iface":
			if len(words) != 4 {
				f.problem(path, "iface", "Unreadable line: "+line)
				current = nil
				continue
			}
			current = &stanza{file: path, name: words[1], family: words[2], method: words[3], options: map[string]string{}}
			f.stanzas = append(f.stanzas, current)
		default:
			if current == nil {
				f.problem(path, words[0], "This ifupdown setting cannot be taken over yet.")
				continue
			}
			current.options[words[0]] = strings.Join(words[1:], " ")
		}
	}
}

// ReadIfupdown reads the interfaces ifupdown configures. It accepts only what the Debian installer writes:
// physical interfaces with inet dhcp or static and inet6 auto or static, with dhcpcd as the DHCP client.
// links and defaultRoutes are the live inventory.
func ReadIfupdown(root string, links []LinkStatus, defaultRoutes []string) ([]IfupdownInterface, []Problem) {
	f := &ifupdownFiles{root: root, auto: map[string]bool{}, hotplug: map[string]bool{}, seen: map[string]bool{}}
	f.read(ifupdownDir+"/interfaces", 0)
	for _, unit := range []string{"dhcpcd.service", "NetworkManager.service"} {
		path := "/etc/systemd/system/multi-user.target.wants/" + unit
		if _, err := os.Lstat(root + path); err == nil {
			f.problem(path, "", unit+" is enabled and would configure the network beside Andaru.")
		}
	}

	byName := map[string]*IfupdownInterface{}
	var names []string
	inet6 := map[string]bool{}
	families := map[string]bool{}
	for _, s := range f.stanzas {
		if s.name == "lo" {
			if s.family != "inet" || s.method != "loopback" || len(s.options) > 0 {
				f.problem(s.file, "iface lo", "Only the plain loopback stanza is supported.")
			}
			continue
		}
		i := byName[s.name]
		if i == nil {
			at := slices.IndexFunc(links, func(l LinkStatus) bool { return l.Name == s.name })
			switch {
			case at < 0:
				f.problem(s.file, "iface "+s.name, "There is no interface "+s.name+" now.")
				continue
			case links[at].Kind != "" || links[at].PermanentAddress == "":
				f.problem(s.file, "iface "+s.name, s.name+" is not a physical interface; only physical interfaces can be taken over yet.")
				continue
			case !strings.EqualFold(links[at].LinkAddress, links[at].PermanentAddress):
				f.problem(s.file, "iface "+s.name, "The MAC address of "+s.name+" was changed; Andaru matches the permanent one and does not change it.")
				continue
			}
			l := links[at]
			i = &IfupdownInterface{Detected: Detected{File: s.file, Name: s.name, PermanentMAC: strings.ToLower(l.PermanentAddress), MTU: l.MTU,
				IPv4: Addressing{Mode: "none"}, IPv6: Addressing{Mode: "none"}, DefaultRoute: slices.Contains(defaultRoutes, s.name)}}
			byName[s.name] = i
			names = append(names, s.name)
		}
		inet6[s.name] = inet6[s.name] || s.family == "inet6"
		// ifupdown applies several stanzas of one family together; one address per family is all Andaru takes from it.
		if families[s.name+" "+s.family] {
			f.problem(s.file, "iface "+s.name, "A second "+s.family+" stanza for "+s.name+" cannot be taken over yet.")
			continue
		}
		families[s.name+" "+s.family] = true
		allowed := []string{"address", "gateway"}
		switch s.family + " " + s.method {
		case "inet dhcp":
			i.IPv4.Mode, i.DHCP, allowed = "dhcp", true, []string{"hostname"}
			if h := s.options["hostname"]; h != "" {
				if !validHostname(h) {
					f.problem(s.file, "hostname", "networkd can only send a DNS host name, not "+strconv.Quote(h)+".")
				}
				i.Identity = &DHCPIdentity{Hostname: h}
			}
		case "inet static", "inet6 static":
			family := &i.IPv4
			if s.family == "inet6" {
				family = &i.IPv6
			}
			p, err := netip.ParsePrefix(s.options["address"])
			g, gerr := netip.ParseAddr(s.options["gateway"])
			switch {
			case err != nil || p.Addr().Is4() != (s.family == "inet"):
				f.problem(s.file, "address", "A static stanza needs an address with a prefix length, like 192.0.2.10/24.")
			case s.options["gateway"] != "" && (gerr != nil || g.Is4() != (s.family == "inet")):
				f.problem(s.file, "gateway", "Unreadable gateway: "+s.options["gateway"])
			default:
				*family = Addressing{Mode: "static", Addresses: []string{p.String()}, Gateway: s.options["gateway"]}
			}
		case "inet6 auto":
			i.IPv6.Mode, allowed = "slaac", nil
		default:
			f.problem(s.file, "iface "+s.name, "inet "+s.family+" "+s.method+" cannot be taken over yet.")
			continue
		}
		for key := range s.options {
			if !slices.Contains(allowed, key) {
				f.problem(s.file, key, "This ifupdown option cannot be taken over yet.")
			}
		}
		// The interface's file is where its addressing is; a separate allow-hotplug line does not count.
		i.File = s.file
	}

	var out []IfupdownInterface
	for _, name := range names {
		i := byName[name]
		if !f.hotplug[name] && !f.auto[name] {
			// Only a hand-run ifup raises it; neither networking.service nor udev would on a hand-back.
			f.problem(i.File, "iface "+name, name+" is neither auto nor allow-hotplug, so ifupdown does not raise it by itself.")
			continue
		}
		i.Hotplug = f.hotplug[name] && !f.auto[name]
		// Without an inet6 stanza ifupdown leaves router advertisements on, so the kernel may hold a SLAAC address.
		at := slices.IndexFunc(links, func(l LinkStatus) bool { return l.Name == name })
		if !inet6[name] && slices.ContainsFunc(links[at].Addresses, func(a AddressStatus) bool {
			return a.Family == "ipv6" && a.Scope == "global" && a.PrefixLength != 128
		}) {
			i.IPv6.Mode, i.KernelSLAAC = "slaac", true
		}
		if i.DHCP {
			f.identity(i)
		}
		out = append(out, *i)
	}
	slices.SortFunc(f.problems, func(a, b Problem) int {
		return cmp.Or(strings.Compare(a.File, b.File), strings.Compare(a.Key, b.Key), strings.Compare(a.Message, b.Message))
	})
	return out, f.problems
}

// identity checks that dhcpcd runs for the interface with only its default identity settings, and reads the DUID.
func (f *ifupdownFiles) identity(i *IfupdownInterface) {
	unknown := func(file, message string) {
		f.problems = append(f.problems, Problem{File: file, Code: identityUnknown, Message: message})
	}
	if !DhcpcdRunning(f.root, i.Name) {
		unknown("/run/dhcpcd/"+i.Name+".pid", "No dhcpcd is running for "+i.Name+"; its DHCP identity cannot be kept.")
	}
	conf, err := os.ReadFile(f.root + dhcpcdConf)
	if err != nil {
		unknown(dhcpcdConf, "dhcpcd's configuration cannot be read: "+err.Error())
	} else {
		duid := false
		for line := range strings.SplitSeq(string(conf), "\n") {
			words := strings.Fields(line)
			if len(words) == 0 || strings.HasPrefix(words[0], "#") {
				continue
			}
			switch words[0] {
			case "duid":
				duid = true
			case "hostname":
				// With a name dhcpcd sends it instead of the machine's; the stanza's own option still wins.
				if len(words) > 1 && !validHostname(strings.Join(words[1:], " ")) {
					f.problem(dhcpcdConf, "hostname", "networkd can only send a DNS host name, not "+strconv.Quote(strings.Join(words[1:], " "))+".")
				} else if len(words) > 1 && (i.Identity == nil || i.Identity.Hostname == "") {
					if i.Identity == nil {
						i.Identity = &DHCPIdentity{}
					}
					i.Identity.Hostname = words[1]
				}
			case "clientid", "iaid", "anonymous", "interface":
				unknown(dhcpcdConf, "dhcpcd.conf sets "+words[0]+", so the identity dhcpcd uses is not the one Andaru can reproduce.")
			}
		}
		if !duid {
			unknown(dhcpcdConf, "dhcpcd.conf does not set duid, so the identity dhcpcd uses is not the one Andaru can reproduce.")
		}
	}
	data, err := os.ReadFile(f.root + dhcpcdDUID)
	raw, herr := hex.DecodeString(strings.ReplaceAll(strings.TrimSpace(string(data)), ":", ""))
	switch {
	case err != nil:
		unknown(dhcpcdDUID, "dhcpcd's DUID cannot be read: "+err.Error())
	case herr != nil || len(raw) < 3 || len(raw) > 130:
		unknown(dhcpcdDUID, "dhcpcd's DUID is not a DUID.")
	default:
		if i.Identity == nil {
			i.Identity = &DHCPIdentity{}
		}
		i.Identity.DUID = raw
	}
}

// hostnameLabel is one label of a DNS host name (RFC 1123), the only form networkd's Hostname= takes.
var hostnameLabel = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

func validHostname(name string) bool {
	if len(name) > 253 {
		return false
	}
	for label := range strings.SplitSeq(strings.TrimSuffix(name, "."), ".") {
		if !hostnameLabel.MatchString(label) {
			return false
		}
	}
	return true
}

// DhcpcdRunning reports whether dhcpcd's pid file for nic names a live dhcpcd.
func DhcpcdRunning(root, nic string) bool {
	data, err := os.ReadFile(root + "/run/dhcpcd/" + nic + ".pid")
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return false
	}
	comm, err := os.ReadFile(root + "/proc/" + strconv.Itoa(pid) + "/comm")
	return err == nil && strings.TrimSpace(string(comm)) == "dhcpcd"
}
