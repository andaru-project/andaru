package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
)

// adapterForm holds one adapter's answers as the form edits them.
type adapterForm struct {
	id, name              string
	zone, ip4, ip6        string
	addresses, gateway    string
	detectedIPv6, summary string
	addressed             bool
}

// formState is the answers in the shape the form fields bind to.
type formState struct {
	kept             []string
	adapters         []*adapterForm
	dnsMode          string
	servers, domains string
	timeout          string
}

func fields(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
}

func newFormState(p proposal, a answers) *formState {
	s := &formState{dnsMode: "offered", timeout: strconv.Itoa(a.Timeout)}
	if a.DNS != nil {
		s.dnsMode, s.servers, s.domains = "declared", strings.Join(a.DNS.Servers, ", "), strings.Join(a.DNS.SearchDomains, ", ")
	}
	detected := defaultAnswers(p)
	for _, op := range p.Operations {
		if op.Collection != "links" {
			continue
		}
		id, answer, first := op.LocalID, a.Adapters[op.LocalID], detected.Adapters[op.LocalID]
		f := &adapterForm{id: id, name: answer.Name, zone: answer.Zone, ip4: answer.IPv4.Mode, ip6: answer.IPv6Mode,
			addresses: strings.Join(answer.IPv4.Addresses, ", "), gateway: answer.IPv4.Gateway, detectedIPv6: first.IPv6Mode,
			addressed: first.IPv4.Mode != "none" || first.IPv6Mode != "none"}
		if i := slices.IndexFunc(p.Adapters, func(x adapter) bool { return x.Name == answer.Name }); i >= 0 {
			f.summary = p.Adapters[i].Summary
		}
		if answer.Keep {
			s.kept = append(s.kept, id)
		}
		s.adapters = append(s.adapters, f)
	}
	return s
}

func (s *formState) answers(base answers) answers {
	a := answers{Adapters: map[string]adapterAnswer{}, Timeout: base.Timeout}
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

func (s *formState) questions() *huh.Form {
	var options []huh.Option[string]
	for _, f := range s.adapters {
		options = append(options, huh.NewOption(printable(f.name)+"  "+printable(f.summary), f.id).Selected(slices.Contains(s.kept, f.id)))
	}
	groups := []*huh.Group{huh.NewGroup(huh.NewMultiSelect[string]().
		Title("Network adapters to take over").
		Description("Detected from this host's current configuration. Space toggles, Enter continues.").
		Options(options...).Value(&s.kept).
		Validate(func(kept []string) error {
			if len(kept) == 0 {
				return errors.New("keep at least one adapter")
			}
			for _, f := range s.adapters {
				if f.addressed && !slices.Contains(kept, f.id) {
					return fmt.Errorf("%s has an address now; leaving it out would cut it off", printable(f.name))
				}
			}
			return nil
		}))}
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
	groups = append(groups,
		huh.NewGroup(huh.NewSelect[string]().Title("DNS servers for this host").
			Options(huh.NewOption("use what DHCP and router advertisements offer", "offered"), huh.NewOption("these servers", "declared")).
			Value(&s.dnsMode)),
		huh.NewGroup(
			huh.NewInput().Title("DNS servers").Description("Tried in this order, separated by commas.").Value(&s.servers).Validate(validServers),
			huh.NewInput().Title("Search domains").Description("Optional, separated by commas.").Value(&s.domains),
		).WithHideFunc(func() bool { return s.dnsMode != "declared" }),
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
	if a.DNS == nil {
		fmt.Fprintln(out, "\nDNS: what DHCP and router advertisements offer")
	} else {
		fmt.Fprintf(out, "\nDNS: %s", printable(strings.Join(a.DNS.Servers, ", ")))
		if len(a.DNS.SearchDomains) > 0 {
			fmt.Fprintf(out, "; search %s", printable(strings.Join(a.DNS.SearchDomains, ", ")))
		}
		fmt.Fprintln(out)
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
	run := func(f *huh.Form) error { return f.WithInput(stdin).WithOutput(stdout).RunWithContext(ctx) }
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
