package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
)

// adapterForm holds one adapter's answers as the form edits them.
type adapterForm struct {
	id, name              string
	zone, ip4, ip6        string
	addresses, gateway    string
	detectedIPv6, summary string
	addressed, required   bool
}

// formState is the answers in the shape the form fields bind to.
type formState struct {
	kept             []string
	adapters         []*adapterForm
	dnsMode          string
	servers, domains string
	timeout          string
	management       string // the link localId whose segment keeps SSH open
	// service: the host resolves through the DNS service, so "offered" means it resolves recursively.
	service bool
	offers  []*dhcpOffer
	dhcp    map[string]*bool // by the offer's segment reference
}

// dhcpOffer is one DHCP server the proposal offers, on the segment of link.
type dhcpOffer struct {
	segment, link, title string
}

func fields(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
}

func newFormState(p proposal, a answers) *formState {
	s := &formState{dnsMode: "offered", timeout: strconv.Itoa(a.Timeout), management: a.Management}
	if a.DNS != nil {
		s.dnsMode, s.servers, s.domains = "declared", strings.Join(a.DNS.Servers, ", "), strings.Join(a.DNS.SearchDomains, ", ")
	}
	s.service, s.dhcp = service(p), map[string]*bool{}
	linkOf := map[string]string{}
	for _, op := range p.Operations {
		if op.Collection == "segments" {
			linkOf["@"+op.LocalID] = strings.TrimPrefix(object(op.Value)["link"].(string), "@")
		}
	}
	for _, op := range p.DHCP {
		var v struct {
			Name, Segment string
			Ranges        []struct{ First, Last string }
		}
		_ = json.Unmarshal(op.Value, &v)
		if len(v.Ranges) == 0 {
			continue
		}
		serve := a.DHCP == nil || a.DHCP[v.Segment] // yes until the operator says otherwise
		s.dhcp[v.Segment] = &serve
		s.offers = append(s.offers, &dhcpOffer{v.Segment, linkOf[v.Segment],
			fmt.Sprintf("Serve DHCP on %s (%s–%s)?", printable(v.Name), v.Ranges[0].First, v.Ranges[0].Last)})
	}
	detected := defaultAnswers(p)
	for _, op := range p.Operations {
		if op.Collection != "links" {
			continue
		}
		id, answer, first := op.LocalID, a.Adapters[op.LocalID], detected.Adapters[op.LocalID]
		f := &adapterForm{id: id, name: answer.Name, zone: answer.Zone, ip4: answer.IPv4.Mode, ip6: answer.IPv6Mode,
			addresses: strings.Join(answer.IPv4.Addresses, ", "), gateway: answer.IPv4.Gateway, detectedIPv6: first.IPv6Mode,
		}
		if i := slices.IndexFunc(p.Adapters, func(x adapter) bool { return x.Name == answer.Name }); i >= 0 {
			f.summary, f.addressed, f.required = p.Adapters[i].Summary, p.Adapters[i].Addressed, p.Adapters[i].Required
		}
		if answer.Keep {
			s.kept = append(s.kept, id)
		}
		s.adapters = append(s.adapters, f)
	}
	return s
}

func (s *formState) answers(base answers) answers {
	a := answers{Adapters: map[string]adapterAnswer{}, Timeout: base.Timeout, Management: s.management}
	for _, f := range s.adapters {
		answer := base.Adapters[f.id]
		answer.Keep, answer.Zone, answer.IPv6Mode = slices.Contains(s.kept, f.id), f.zone, f.ip6
		answer.IPv4 = family{Mode: f.ip4}
		if f.ip4 == "static" {
			answer.IPv4.Addresses, answer.IPv4.Gateway = fields(f.addresses), strings.TrimSpace(f.gateway)
		}
		a.Adapters[f.id] = answer
	}
	if s.dnsMode == "declared" {
		a.DNS = &dnsAnswer{Servers: fields(s.servers), SearchDomains: fields(s.domains)}
	}
	if len(s.dhcp) > 0 {
		a.DHCP = map[string]bool{}
		for segment, serve := range s.dhcp {
			a.DHCP[segment] = *serve
		}
	}
	if n, err := strconv.Atoi(strings.TrimSpace(s.timeout)); err == nil {
		a.Timeout = n
	}
	return a
}

func validPrefixes(s string) error {
	list := fields(s)
	if len(list) == 0 {
		return errors.New("give at least one address, like 192.0.2.10/24")
	}
	for _, a := range list {
		if p, err := netip.ParsePrefix(a); err != nil || !p.Addr().Is4() {
			return fmt.Errorf("%q is not an IPv4 address with a prefix length", printable(a))
		}
	}
	return nil
}

func validGateway(s string) error {
	if s = strings.TrimSpace(s); s == "" {
		return nil
	}
	if a, err := netip.ParseAddr(s); err != nil || !a.Is4() {
		return fmt.Errorf("%q is not an IPv4 address", printable(s))
	}
	return nil
}

func validServers(s string) error {
	list := fields(s)
	if len(list) == 0 {
		return errors.New("give at least one server address")
	}
	for _, a := range list {
		if _, err := netip.ParseAddr(a); err != nil {
			return fmt.Errorf("%q is not an IP address", printable(a))
		}
	}
	return nil
}

// checkKept refuses an adapter selection that would cut the host off or strand an interface.
func (s *formState) checkKept(kept []string) error {
	if len(kept) == 0 {
		return errors.New("keep at least one adapter")
	}
	for _, f := range s.adapters {
		switch {
		case slices.Contains(kept, f.id):
		case f.required:
			return fmt.Errorf("%s is configured by ifupdown; Andaru masks ifupdown for every interface it configures, so keep it", printable(f.name))
		case f.addressed:
			return fmt.Errorf("%s has an address now; leaving it out would cut it off", printable(f.name))
		}
	}
	return nil
}

func (s *formState) questions() *huh.Form {
	var options []huh.Option[string]
	for _, f := range s.adapters {
		options = append(options, huh.NewOption(printable(f.name)+"  "+printable(f.summary), f.id).Selected(slices.Contains(s.kept, f.id)))
	}
	groups := []*huh.Group{huh.NewGroup(huh.NewMultiSelect[string]().
		Title("Network adapters to take over").
		Description("Detected from this host's current configuration. Space toggles, Enter continues.").
		Options(options...).Value(&s.kept).
		Validate(s.checkKept))}
	for _, f := range s.adapters {
		hidden := func() bool { return !slices.Contains(s.kept, f.id) }
		ip6 := []huh.Option[string]{huh.NewOption("none", "none"), huh.NewOption("SLAAC", "slaac"), huh.NewOption("DHCPv6", "dhcp6")}
		if f.detectedIPv6 == "static" {
			ip6 = append(ip6, huh.NewOption("static, as detected", "static"))
		}
		name := printable(f.name)
		groups = append(groups,
			huh.NewGroup(
				huh.NewSelect[string]().Title(name+": zone").Description("wan faces the provider; lan is everything else.").
					Options(huh.NewOption("wan", "wan"), huh.NewOption("lan", "lan")).Value(&f.zone),
				huh.NewSelect[string]().Title(name+": IPv4").
					Options(huh.NewOption("DHCP", "dhcp"), huh.NewOption("static", "static"), huh.NewOption("none", "none")).Value(&f.ip4),
			).WithHideFunc(hidden),
			huh.NewGroup(
				huh.NewInput().Title(name+": IPv4 addresses").Description("With prefix length, separated by commas.").Value(&f.addresses).Validate(validPrefixes),
				huh.NewInput().Title(name+": IPv4 gateway").Description("Empty for none.").Value(&f.gateway).Validate(validGateway),
			).WithHideFunc(func() bool { return hidden() || f.ip4 != "static" }),
			huh.NewGroup(
				huh.NewSelect[string]().Title(name+": IPv6").Options(ip6...).Value(&f.ip6),
			).WithHideFunc(hidden),
		)
	}
	offered := huh.NewOption("use what DHCP and router advertisements offer", "offered")
	if s.service {
		offered = huh.NewOption("resolve on its own (recursive)", "offered")
	}
	groups = append(groups,
		huh.NewGroup(huh.NewSelect[string]().Title("DNS servers for this host").
			Options(offered, huh.NewOption("these servers", "declared")).
			Value(&s.dnsMode)),
		huh.NewGroup(
			huh.NewInput().Title("DNS servers").Description("Tried in this order, separated by commas.").Value(&s.servers).Validate(validServers),
			huh.NewInput().Title("Search domains").Description("Optional, separated by commas.").Value(&s.domains),
		).WithHideFunc(func() bool { return s.dnsMode != "declared" }),
	)
	for _, o := range s.offers {
		groups = append(groups, huh.NewGroup(huh.NewConfirm().Title(o.title).Value(s.dhcp[o.segment])).
			WithHideFunc(func() bool { return !slices.Contains(s.kept, o.link) }))
	}
	groups = append(groups,
		huh.NewGroup(huh.NewSelect[string]().Title("Management adapter").
			Description("SSH stays open on this adapter whatever the rules say.").
			OptionsFunc(func() []huh.Option[string] {
				var options []huh.Option[string]
				for _, f := range s.adapters {
					if slices.Contains(s.kept, f.id) {
						// No zone in the label: the options refresh only when s.kept changes, so a zone would go stale.
						options = append(options, huh.NewOption(printable(f.name), f.id))
					}
				}
				return options
			}, &s.kept).Value(&s.management)),
		huh.NewGroup(huh.NewInput().Title("Seconds to confirm").
			Description("If you do not confirm from a new session in time, the previous configuration returns.").
			Value(&s.timeout).Validate(func(v string) error {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err != nil || n < 30 || n > 600 {
				return errors.New("between 30 and 600")
			}
			return nil
		})),
	)
	return huh.NewForm(groups...)
}

func describeFamily(f family) string {
	if f.Mode == "static" {
		out := "static " + strings.Join(f.Addresses, " ")
		if f.Gateway != "" {
			out += " via " + f.Gateway
		}
		return out
	}
	return f.Mode
}

// review prints what applying would do: per adapter, the exact files, and every finding.
func review(out io.Writer, s *formState, a answers, d draft, err error) {
	fmt.Fprintln(out, "\nReview — nothing has been changed yet.")
	for _, f := range s.adapters {
		answer := a.Adapters[f.id]
		fmt.Fprintf(out, "\n%s\n  now:   %s\n", printable(f.name), printable(f.summary))
		if !answer.Keep {
			fmt.Fprintln(out, "  after: left out")
			continue
		}
		fmt.Fprintf(out, "  after: zone %s, IPv4 %s, IPv6 %s\n", answer.Zone, printable(describeFamily(answer.IPv4)), answer.IPv6Mode)
	}
	switch {
	case s.service && (a.DNS == nil || len(a.DNS.Servers) == 0):
		fmt.Fprintln(out, "\nDNS service: resolves on its own (recursive); the host and the LAN use it")
	case s.service:
		fmt.Fprintf(out, "\nDNS service: forwards to %s; the host and the LAN use it\n", printable(strings.Join(a.DNS.Servers, ", ")))
	case a.DNS == nil:
		fmt.Fprintln(out, "\nDNS: what DHCP and router advertisements offer")
	default:
		fmt.Fprintf(out, "\nDNS: %s", printable(strings.Join(a.DNS.Servers, ", ")))
		if len(a.DNS.SearchDomains) > 0 {
			fmt.Fprintf(out, "; search %s", printable(strings.Join(a.DNS.SearchDomains, ", ")))
		}
		fmt.Fprintln(out)
	}
	for _, o := range s.offers {
		if a.DHCP[o.segment] && slices.Contains(s.kept, o.link) {
			fmt.Fprintf(out, "DHCP: %s\n", strings.TrimSuffix(strings.TrimPrefix(o.title, "Serve DHCP on "), "?"))
		}
	}
	for _, f := range s.adapters {
		if f.id == a.Management {
			fmt.Fprintf(out, "Management: SSH stays open on %s.\n", printable(f.name))
			if a.Adapters[f.id].Zone == "wan" {
				fmt.Fprintln(out, "  warning: that adapter faces the uplink, so SSH is reachable from that network; restrict it with management.allowedSources after the takeover.")
			}
		}
	}
	fmt.Fprintf(out, "Confirm within %d seconds.\n", a.Timeout)
	if err != nil {
		fmt.Fprintf(out, "\nThis cannot be saved:\n%s\n", err)
		return
	}
	for _, f := range d.Files {
		fmt.Fprintf(out, "\n### %s (%s)\n", printable(f.Path), printable(f.Mode))
		for line := range strings.Lines(f.Content) {
			fmt.Fprintln(out, printable(strings.TrimSuffix(line, "\n")))
		}
	}
	if len(d.Rules) > 0 {
		fmt.Fprintln(out, "\nRules, in the order they are tried:")
		for _, r := range d.Rules {
			fmt.Fprintf(out, "  %s %d. %s from %s to %s services %s  (%s)\n", r.Chain, r.Position, r.Action,
				printable(orAny(r.From)), printable(orAny(r.To)), printable(orAny(r.Services)), printable(r.Name))
		}
	}
	for _, f := range d.shownFindings() {
		fmt.Fprintln(out, "  "+findingLine(f))
	}
}

func (s *formState) reviewForm(choice *string, allowed bool) *huh.Form {
	options := []huh.Option[string]{huh.NewOption("Back: change the answers", "back"), huh.NewOption("Quit: change nothing", "quit")}
	if allowed {
		options = append(options, huh.NewOption("Apply: save, then apply with the rollback timer", "apply"))
	}
	return huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("What now?").Options(options...).Value(choice)))
}

// interactive asks the operator, pre-filled with what was detected, and applies only on an explicit choice.
func interactive(ctx context.Context, c takeoverClient, p proposal, timeout int, stdin io.Reader, stdout io.Writer, hold func()) int {
	if p.State != "ready" {
		fmt.Fprintln(stdout, stateAdvice(p.State))
		for _, f := range p.Findings {
			fmt.Fprintln(stdout, "  "+findingLine(f))
		}
		return 1
	}
	// A serial console reports a 0x0 size, which would leave the forms no room; assume the classic 80x24.
	size := tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
		if s, ok := msg.(tea.WindowSizeMsg); ok && (s.Width == 0 || s.Height == 0) {
			return tea.WindowSizeMsg{Width: 80, Height: 24}
		}
		return msg
	})
	run := func(f *huh.Form) error {
		return f.WithProgramOptions(size).WithInput(stdin).WithOutput(stdout).RunWithContext(ctx) // WithProgramOptions replaces earlier options
	}
	a := defaultAnswers(p)
	a.Timeout = timeout
	for {
		s := newFormState(p, a)
		if err := run(s.questions()); err != nil {
			fmt.Fprintln(stdout, "Nothing was changed.")
			return 1
		}
		a = s.answers(a)
		ops, err := batch(p, a)
		var d draft
		if err == nil {
			d, err = c.Draft(ctx, ops)
		}
		review(stdout, s, a, d, err)
		// Back is preselected: a stray Enter never applies.
		choice := "back"
		allowed := err == nil && len(errorFindings(d.ApplyFindings)) == 0
		if err := run(s.reviewForm(&choice, allowed)); err != nil {
			fmt.Fprintln(stdout, "Nothing was changed.")
			return 1
		}
		switch choice {
		case "quit":
			fmt.Fprintln(stdout, "Nothing was changed.")
			return 1
		case "apply":
			return commit(ctx, c, p, ops, a.Timeout, stdout, hold)
		}
	}
}

func orAny(names []string) string {
	if len(names) == 0 {
		return "any"
	}
	return strings.Join(names, ", ")
}
