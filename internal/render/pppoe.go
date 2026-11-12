package render

import (
	"fmt"
	"regexp"
	"strings"
)

const redacted = "<redacted>"

// pppd copies user and password into 256-byte buffers and silently truncates.
const credentialLimit = 255

// A name outside this set would need different quoting in a unit, a device unit name and a pppd word.
var plainInterfaceName = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)

// quoteOption writes one pppd options-file word: inside quotes only \ and " need escaping.
func quoteOption(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// deviceUnit names the device unit of an interface the way systemd-escape --path does.
func deviceUnit(ifname string) string {
	var b strings.Builder
	for i := 0; i < len(ifname); i++ {
		c := ifname[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == ':' || c == '_' || c == '.' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, `\x%02x`, c)
		}
	}
	return "sys-subsystem-net-devices-" + b.String() + ".device"
}

// visitPPPoE decides whether a PPPoE link can be handed to pppd at all.
func (r *renderer) visitPPPoE(i int, named bool) {
	l := r.d.Links[i]
	if !l.Enabled {
		return
	}
	parent := r.visit(l.Parent)
	si, ok := r.segment[l.ID]
	if !ok {
		r.find("warning", "UNSUPPORTED_COMBINATION", linkPath(i, ""), "Not rendered: a PPPoE link needs a segment to say which protocols to negotiate.")
		return
	}
	if !parent {
		r.find("warning", "DEPENDENCY_UNAVAILABLE", linkPath(i, "parent"), "Not rendered: the parent link is unavailable.")
		return
	}
	supported := named
	if !plainInterfaceName.MatchString(r.name[l.Parent]) {
		r.find("error", "INTERFACE_NAME_UNSUPPORTED", linkPath(i, "parent"), "Not rendered: the parent interface's kernel name contains characters that cannot be passed to pppd safely.")
		supported = false
	}
	a := r.d.Segments[si].Addressing
	base := fmt.Sprintf("/segments/%d/addressing", si)
	if a.IPv6.Mode == "static" || a.IPv6.Mode == "delegated" {
		r.find("error", "UNSUPPORTED_COMBINATION", base+"/ipv6/mode", "Not rendered: IPv6 on a PPPoE segment can be none, slaac or dhcp6.")
		supported = false
	}
	if a.IPv6.RA.mode() != "disabled" {
		r.find("error", "UNSUPPORTED_COMBINATION", base+"/ipv6/ra", "Not rendered: router advertisements cannot be sent on a PPPoE segment.")
		supported = false
	}
	if a.IPv4.Mode == "none" && a.IPv6.Mode == "none" {
		r.find("error", "UNSUPPORTED_COMBINATION", base, "Not rendered: a PPPoE segment needs IPv4 or IPv6.")
		supported = false
	}
	r.rendered[l.ID], r.usable[l.ID] = supported, supported
}

// pppoe renders the pppd options, the supervising unit and the .network that
// makes networkd leave pppd's addresses alone while it handles IPv6.
func (r *renderer) pppoe(i int) {
	l := r.d.Links[i]
	name, parent := r.name[l.ID], r.name[l.Parent]
	si := r.segment[l.ID]
	a := r.d.Segments[si].Addressing
	password := ""
	if l.Password != nil {
		password = r.in.Secrets[l.Password.Secret]
	}
	if len(l.Username) > credentialLimit {
		r.find("error", "CREDENTIAL_TOO_LONG", linkPath(i, "username"), "pppd truncates user names longer than 255 bytes; authentication would fail.")
	}
	if len(password) > credentialLimit {
		r.find("error", "CREDENTIAL_TOO_LONG", linkPath(i, "password"), "pppd truncates passwords longer than 255 bytes; authentication would fail.")
	}
	options := func(passwordWord string) string {
		lines := []string{
			"plugin pppoe.so", "nic-" + parent, "ifname " + name, "linkname " + name,
			"user " + quoteOption(l.Username), "password " + passwordWord,
			"noauth", "nolog", "hide-password", "persist", "maxfail 0", "holdoff 10",
			"lcp-echo-interval 15", "lcp-echo-failure 4",
			fmt.Sprintf("mtu %d", l.MTU), fmt.Sprintf("mru %d", l.MTU),
		}
		if a.IPv4.Mode == "ppp" {
			lines = append(lines, "noipdefault", "defaultroute")
		} else {
			lines = append(lines, "noip")
		}
		if a.IPv6.Mode != "none" {
			lines = append(lines, "+ipv6", "ipv6cp-use-persistent")
		} else {
			lines = append(lines, "noipv6")
		}
		return strings.Join(lines, "\n") + "\n"
	}
	sources := []string{linkPath(i, ""), fmt.Sprintf("/segments/%d", si)}
	optionsPath := "/run/andaru/ppp/" + name + ".options"
	r.result.Files = append(r.result.Files, File{Path: optionsPath, Mode: "0600", Secret: true,
		Content: options(quoteOption(redacted)), real: options(quoteOption(password)), Sources: sources})

	device := deviceUnit(parent)
	r.file("/run/systemd/system/andaru-pppoe-"+name+".service", "0644", ini(
		section{"Unit", []string{
			"Description=Andaru PPPoE session " + name,
			"BindsTo=" + device,
			"Wants=systemd-networkd.service",
			"After=" + device + " systemd-networkd.service",
		}},
		section{"Service", []string{
			"Type=notify",
			// The device unit is active before networkd has raised the parent's MTU; pppd would negotiate 1492.
			"ExecStartPre=/usr/lib/systemd/systemd-networkd-wait-online --interface=" + parent + ":carrier --timeout=60",
			"ExecStart=/usr/sbin/pppd file " + optionsPath + " nodetach up_sdnotify",
			"ExecReload=/bin/kill -HUP $MAINPID",
			"SuccessExitStatus=5",
			"Restart=on-failure",
			"RestartSec=5",
		}}), sources)

	sections, _ := r.addressing(l.ID, true)
	// KeepConfiguration stops networkd removing the address and default route pppd installs.
	sections[0].lines = append([]string{"KeepConfiguration=yes"}, sections[0].lines...)
	r.file(networkDir+name+".network", "0644", ini(append([]section{
		{"Match", []string{"Name=" + name, "Type=ppp"}},
		{"Link", []string{"RequiredForOnline=no"}},
	}, sections...)...), sources)
}
