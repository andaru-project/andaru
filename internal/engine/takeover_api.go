package engine

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"strings"

	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/host"
	"github.com/andaru-project/andaru/internal/render"
)

const (
	proposalPath = "/api/v1/takeover/proposal"
	draftPath    = "/api/v1/preview/network/draft"
)

var (
	errInventory = errors.New("host inventory unavailable")
	errRender    = errors.New("declaration could not be rendered")
	errLive      = errors.New("live generation unreadable")
)

// gatherGate collects everything the apply gates judge: the host, the rendered files and what is live now.
// The apply and the draft preview both use it, so a draft is judged exactly as the apply will be.
func gatherGate(ctx context.Context, read hostReader, dir string, pointers generation.Pointers, value json.RawMessage, secrets map[string]string, takeover bool) (gateInput, error) {
	gate := gateInput{Takeover: takeover, Declaration: value}
	var err error
	if gate.Links, err = read.Links(ctx); err == nil {
		gate.Facts, err = read.Facts()
	}
	if err == nil {
		gate.Managed, err = read.Managed(ctx)
	}
	if err == nil {
		gate.Baseline, err = read.Baseline()
	}
	if err != nil {
		return gate, errors.Join(errInventory, err)
	}
	if gate.fromIfupdown() {
		var routes []string
		if routes, err = read.Route(ctx); err == nil && gate.Facts.ResolvedInCharge {
			gate.CurrentDNS, _, err = read.Resolved(ctx)
		}
		if err != nil {
			return gate, errors.Join(errInventory, err)
		}
		gate.Ifupdown, gate.IfupdownProblems = host.ReadIfupdown(read.Root, gate.Links, routes)
	}
	identity, err := identities(dir, gate.Ifupdown)
	if err != nil {
		return gate, errors.Join(errLive, err)
	}
	if gate.Result, err = render.Network(render.Input{Declaration: value, Secrets: secrets, Links: gate.Links, Facts: gate.Facts, DHCPIdentity: identity}); err != nil {
		return gate, errors.Join(errRender, err)
	}
	if pointers.Live != nil {
		live, err := generation.Read(dir, *pointers.Live)
		if err == nil {
			gate.Live = &live
			gate.LiveFiles, err = generation.Contents(dir, live)
		}
		if err != nil {
			return gate, errors.Join(errLive, err)
		}
	}
	return gate, nil
}

// identities are the dhcpcd identities to render: those a takeover captured, else those of the host's ifupdown now.
func identities(dir string, live []host.IfupdownInterface) (map[string]host.DHCPIdentity, error) {
	kind, captured, err := generation.ReadBaselineKind(dir)
	if err != nil {
		return nil, err
	}
	if kind == "ifupdown" && captured != nil {
		live = captured.Interfaces
	}
	out := map[string]host.DHCPIdentity{}
	for _, i := range live {
		if i.Identity != nil && len(i.Identity.DUID) > 0 {
			out[i.PermanentMAC] = *i.Identity
		}
	}
	return out, nil
}

// previewIdentities gives the saved-version preview the identities an apply would render with.
func previewIdentities(read hostReader, dir string) func(context.Context, []host.LinkStatus) (map[string]host.DHCPIdentity, error) {
	return func(ctx context.Context, links []host.LinkStatus) (map[string]host.DHCPIdentity, error) {
		baseline, err := read.Baseline()
		if err != nil {
			return nil, err
		}
		var live []host.IfupdownInterface
		if (gateInput{Baseline: baseline}).fromIfupdown() {
			routes, err := read.Route(ctx)
			if err != nil {
				return nil, err
			}
			live, _ = host.ReadIfupdown(read.Root, links, routes)
		}
		return identities(dir, live)
	}
}

// newLiveHandler serves the routes that look at or change the running host.
func newLiveHandler(applies, takeover http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == proposalPath || r.URL.Path == draftPath {
			takeover.ServeHTTP(w, r)
			return
		}
		applies.ServeHTTP(w, r)
	})
}

func isLivePath(path string) bool {
	return path == proposalPath || path == draftPath || path == appliesPath || strings.HasPrefix(path, appliesPath+"/")
}

type takeoverHandler struct {
	head  func(context.Context, *config.Version) (config.RealizationInput, error)
	draft func(context.Context, config.Changes) (config.RealizationInput, error)
	read  hostReader
	dir   string
}

// newTakeoverHandler serves the assistant's two read-only routes: what the host does now, and what a batch would do.
func newTakeoverHandler(head func(context.Context, *config.Version) (config.RealizationInput, error), draft func(context.Context, config.Changes) (config.RealizationInput, error), read hostReader, stateDir string) http.Handler {
	return &takeoverHandler{head: head, draft: draft, read: read, dir: generation.Dir(stateDir)}
}

func (h *takeoverHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	want := http.MethodGet
	if r.URL.Path == draftPath {
		want = http.MethodPost
	}
	if r.Method != want && !(want == http.MethodGet && r.Method == http.MethodHead) {
		allow := want
		if want == http.MethodGet {
			allow = "GET, HEAD"
		}
		w.Header().Set("Allow", allow)
		writeError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed.")
		return
	}
	if r.URL.Path == draftPath {
		h.serveDraft(w, r)
		return
	}
	h.serveProposal(w, r)
}

type adapterView struct {
	Name         string `json:"name"`
	PermanentMac string `json:"permanentMac"`
	Summary      string `json:"summary"`
	// Addressed: the adapter has a global address now, so leaving it out would cut it off.
	Addressed bool `json:"addressed"`
	// Required: ifupdown configures it, and the takeover masks ifupdown for every interface.
	Required bool `json:"required"`
}

type proposalView struct {
	State        string             `json:"state"`
	SavedVersion config.Version     `json:"savedVersion"`
	Operations   []config.Operation `json:"operations"`
	Starter      []config.Operation `json:"starter"`
	Adapters     []adapterView      `json:"adapters"`
	Findings     []render.Finding   `json:"findings"`
}

func (h *takeoverHandler) serveProposal(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	view := proposalView{Operations: []config.Operation{}, Starter: starterOperations(), Adapters: []adapterView{}, Findings: []render.Finding{}}
	input, err := h.head(ctx, nil)
	if err != nil {
		writeConfigurationError(w, r, err)
		return
	}
	view.SavedVersion = input.SavedVersion
	pointers, _, err := generation.List(h.dir)
	if err != nil {
		slog.ErrorContext(ctx, "read generations", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "REALIZE_UNAVAILABLE", "The apply machinery is unavailable; inspect the engine journal.")
		return
	}
	var saved struct {
		Links      []json.RawMessage `json:"links"`
		Segments   []json.RawMessage `json:"segments"`
		Zones      []json.RawMessage `json:"zones"`
		Objects    json.RawMessage   `json:"objects"`
		Policy     json.RawMessage   `json:"policy"`
		NAT        json.RawMessage   `json:"nat"`
		Management json.RawMessage   `json:"management"`
	}
	_ = json.Unmarshal(input.Value, &saved)
	// A saved filter section counts too: the proposal would otherwise activate saved rules the review never shows, or collide with a saved singleton.
	filterSaved := len(saved.Objects)+len(saved.Policy)+len(saved.NAT)+len(saved.Management) > 0
	unsupported := func(path, message string) {
		view.State = "unsupported"
		view.Findings = append(view.Findings, render.Finding{Severity: "error", Code: "TAKEOVER_UNSUPPORTED_BASELINE", Path: path, Message: message})
	}
	switch {
	case pointers.Takeover || pointers.Pending != nil || pointers.Confirmed != nil:
		view.State = "taken-over"
	case len(saved.Links)+len(saved.Segments)+len(saved.Zones) > 0 || filterSaved:
		view.State = "saved-not-applied"
	}
	if view.State != "" {
		writeJSON(w, r, http.StatusOK, view)
		return
	}
	baseline, err := h.read.Baseline()
	var links []host.LinkStatus
	var managed []host.Managed
	var routes []string
	if err == nil {
		links, err = h.read.Links(ctx)
	}
	if err == nil {
		managed, err = h.read.Managed(ctx)
	}
	if err == nil {
		routes, err = h.read.Route(ctx)
	}
	if err != nil {
		slog.ErrorContext(ctx, "host inventory unavailable for the takeover proposal", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "INVENTORY_UNAVAILABLE", "Interface inventory is unavailable; inspect the engine journal.")
		return
	}
	switch {
	case baseline.Ifupdown && !baseline.IfupdownMasked && baseline.Netplan:
		unsupported("/sbin/ifup", "Both netplan and ifupdown configure this host; Andaru can take over one of them, not both.")
	case (gateInput{Baseline: baseline}).fromIfupdown():
	case !baseline.Netplan:
		unsupported("/usr/sbin/netplan", "Only a netplan-configured host can be taken over by the assistant.")
	}
	if view.State != "" {
		writeJSON(w, r, http.StatusOK, view)
		return
	}
	if len(routes) > 1 {
		unsupported("/run/systemd/network", "More than one interface has an IPv4 default route ("+strings.Join(routes, ", ")+"); only one uplink can be taken over yet.")
	}
	if view.State != "" {
		writeJSON(w, r, http.StatusOK, view)
		return
	}
	if (gateInput{Baseline: baseline}).fromIfupdown() {
		h.proposeIfupdown(w, r, view, links, managed, routes)
		return
	}
	found, problems := host.ReadNetplan(h.read.Root, links, managed, strings.Join(routes, ""))
	for _, p := range problems {
		message := p.Message
		if p.Key != "" {
			message = p.Key + ": " + message
		}
		unsupported(p.File, message)
	}
	if len(found) == 0 && len(problems) == 0 {
		unsupported("/run/systemd/network", "netplan configures no interface here.")
	}
	if view.State != "" {
		writeJSON(w, r, http.StatusOK, view)
		return
	}
	view.State, view.Operations, view.Adapters = "ready", proposalOperations(found, viaInterface(links, r)), adapters(found, links, false)
	writeJSON(w, r, http.StatusOK, view)
}

func create(collection, localID string, value any) config.Operation {
	raw, _ := json.Marshal(value)
	return config.Operation{Op: "create", Collection: collection, LocalID: localID, Value: raw}
}

func family(a host.Addressing) map[string]any {
	out := map[string]any{"mode": a.Mode}
	if a.Mode == "static" {
		out["addresses"] = a.Addresses
		if a.Gateway != "" {
			out["gateway"] = a.Gateway
		}
	}
	return out
}

// proposalOperations turns what the host configures into one change batch, with starter rules for the LAN,
// outbound NAT for the uplink, and management on the segment of via, the interface the operator's session uses.
// starterOperations are the LAN's starter services and rules, then outbound NAT for the uplinks.
// The proposal carries all of them, so a client that changes the zones can add what the detection left out.
func starterOperations() []config.Operation {
	return []config.Operation{
		create("services", "ping", map[string]any{"name": "ping", "protocol": "icmp", "types": []string{"echo-request"}}),
		create("services", "ping6", map[string]any{"name": "ping6", "protocol": "icmpv6", "types": []string{"echo-request"}}),
		create("services", "dnsUdp", map[string]any{"name": "dns-udp", "protocol": "udp", "ports": []string{"53"}}),
		create("services", "dnsTcp", map[string]any{"name": "dns-tcp", "protocol": "tcp", "ports": []string{"53"}}),
		create("services", "dns", map[string]any{"name": "dns", "protocol": "group", "members": []string{"@dnsUdp", "@dnsTcp"}}),
		create("policy.input", "lanToFirewall", starterRule("lan to firewall: ping and DNS", "@ping", "@ping6", "@dns")),
		create("policy.forward", "lanOut", starterRule("lan to any")),
		singleton("nat.outbound", map[string]any{"mode": "automatic"}),
	}
}

func proposalOperations(found []host.Detected, via string) []config.Operation {
	zones := map[string]bool{}
	var links, segments []config.Operation
	for i, n := range found {
		zone := "lan"
		if n.DefaultRoute {
			zone = "wan"
		}
		zones[zone] = true
		link := fmt.Sprintf("link%d", i+1)
		links = append(links, create("links", link, map[string]any{"name": n.Name, "kind": "ethernet", "mtu": n.MTU, "enabled": true, "permanentMac": n.PermanentMAC}))
		addressing := map[string]any{"ipv4": family(n.IPv4), "ipv6": family(n.IPv6)}
		if len(n.DNS) > 0 {
			dns := map[string]any{"servers": n.DNS}
			if len(n.Domains) > 0 {
				dns["searchDomains"] = n.Domains
			}
			addressing["dns"] = dns
		}
		segments = append(segments, create("segments", fmt.Sprintf("seg%d", i+1), map[string]any{"name": n.Name, "link": "@" + link, "zone": "@" + zone, "addressing": addressing}))
	}
	var ops []config.Operation
	for _, zone := range []string{"wan", "lan"} {
		if zones[zone] {
			ops = append(ops, create("zones", zone, map[string]any{"name": zone}))
		}
	}
	ops = slices.Concat(ops, links, segments)
	starter := starterOperations()
	if zones["lan"] {
		ops = append(ops, starter[:7]...)
	}
	if zones["wan"] {
		ops = append(ops, starter[7])
	}
	// Management stays where the operator is: their session's segment, else the LAN, else the uplink.
	management := slices.IndexFunc(found, func(n host.Detected) bool { return n.Name == via })
	if management < 0 {
		management = max(slices.IndexFunc(found, func(n host.Detected) bool { return !n.DefaultRoute }), 0)
	}
	if len(found) > 0 {
		ops = append(ops, singleton("management", map[string]any{"segment": fmt.Sprintf("@seg%d", management+1)}))
	}
	return ops
}

// viaInterface names the interface holding the address in ?via=, the local end of the operator's SSH session.
func viaInterface(links []host.LinkStatus, r *http.Request) string {
	via, err := netip.ParseAddr(r.URL.Query().Get("via"))
	if err != nil {
		return ""
	}
	for _, l := range links {
		if slices.ContainsFunc(l.Addresses, func(a host.AddressStatus) bool { return a.Address == via.Unmap().WithZone("").String() }) {
			return l.Name
		}
	}
	return ""
}

func starterRule(name string, services ...string) map[string]any {
	return map[string]any{"name": name, "enabled": true, "action": "accept", "from": []string{"@lan"}, "to": []string{}, "services": append([]string{}, services...), "log": false}
}

// singleton sets a configuration object that exists at most once; a takeover sets it for the first time.
func singleton(collection string, value any) config.Operation {
	raw, _ := json.Marshal(value)
	unset := config.Version("0")
	return config.Operation{Op: "replace", Collection: collection, ExpectedVersion: &unset, Value: raw}
}

func describe(a host.Addressing) string {
	switch a.Mode {
	case "static":
		return "static " + strings.Join(a.Addresses, " ")
	case "dhcp", "dhcp6":
		return strings.ToUpper(a.Mode)
	case "slaac":
		return "SLAAC"
	}
	return "none"
}

func adapters(found []host.Detected, links []host.LinkStatus, required bool) []adapterView {
	out := []adapterView{}
	for _, n := range found {
		summary := "IPv4 " + describe(n.IPv4) + ", IPv6 " + describe(n.IPv6)
		if n.DefaultRoute {
			summary += ", default route"
		}
		if len(n.DNS) > 0 {
			summary += ", DNS " + strings.Join(n.DNS, " ")
		}
		addressed := slices.ContainsFunc(links, func(l host.LinkStatus) bool {
			return l.Name == n.Name && slices.ContainsFunc(l.Addresses, func(a host.AddressStatus) bool { return a.Scope == "global" })
		})
		out = append(out, adapterView{Name: n.Name, PermanentMac: n.PermanentMAC, Summary: summary, Addressed: addressed, Required: required})
	}
	return out
}

type draftView struct {
	previewResponse
	ApplyFindings []render.Finding `json:"applyFindings"`
	// Rules is the policy as the review shows it: each rule with its position and its references by name.
	Rules []ruleRow `json:"rules"`
}

type ruleRow struct {
	Chain    string   `json:"chain"`
	Position int      `json:"position"`
	Name     string   `json:"name"`
	Enabled  bool     `json:"enabled"`
	Action   string   `json:"action"`
	From     []string `json:"from"`
	To       []string `json:"to"`
	Services []string `json:"services"`
	Log      bool     `json:"log"`
}

// ruleTable lists a declaration's rules in execution order with the names of what they reference.
func ruleTable(value json.RawMessage) []ruleRow {
	type named struct{ ID, Name string }
	var d struct {
		Zones   []named
		Objects struct{ Addresses, Services []named }
		Policy  struct {
			Input, Forward []struct {
				Name, Action       string
				Enabled, Log       bool
				From, To, Services []string
			}
		}
	}
	_ = json.Unmarshal(value, &d)
	names := map[string]string{}
	for _, list := range [][]named{d.Zones, d.Objects.Addresses, d.Objects.Services} {
		for _, n := range list {
			names[n.ID] = n.Name
		}
	}
	resolve := func(ids []string) []string {
		out := []string{}
		for _, id := range ids {
			out = append(out, cmp.Or(names[id], id))
		}
		return out
	}
	rows := []ruleRow{}
	for _, chain := range []string{"input", "forward"} {
		list := d.Policy.Input
		if chain == "forward" {
			list = d.Policy.Forward
		}
		for i, r := range list {
			rows = append(rows, ruleRow{Chain: chain, Position: i + 1, Name: r.Name, Enabled: r.Enabled, Action: r.Action,
				From: resolve(r.From), To: resolve(r.To), Services: resolve(r.Services), Log: r.Log})
		}
	}
	return rows
}

func (h *takeoverHandler) serveDraft(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil || len(body) > 1<<20 {
		writeError(w, r, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "Request body is too large.")
		return
	}
	changes, err := config.DecodeChanges(body)
	if err == nil {
		var input config.RealizationInput
		if input, err = h.draft(ctx, changes); err == nil {
			h.judge(w, r, input)
			return
		}
	}
	writeConfigurationError(w, r, err)
}

func (h *takeoverHandler) judge(w http.ResponseWriter, r *http.Request, input config.RealizationInput) {
	ctx := r.Context()
	pointers, _, err := generation.List(h.dir)
	if err == nil {
		var gate gateInput
		if gate, err = gatherGate(ctx, h.read, h.dir, pointers, input.Value, input.Secrets, !pointers.Takeover); err == nil {
			findings := applyFindings(gate)
			if findings == nil {
				findings = []render.Finding{}
			}
			if gate.Takeover && gate.fromIfupdown() {
				gate.Result.Findings = append(gate.Result.Findings, ifupdownLimits(gate.Ifupdown)...)
			}
			writeJSON(w, r, http.StatusOK, draftView{previewResponse{State: "draft", SavedVersion: input.SavedVersion, Result: gate.Result}, findings, ruleTable(input.Value)})
			return
		}
	}
	slog.ErrorContext(ctx, "draft could not be judged", "error", err)
	if errors.Is(err, errInventory) {
		writeError(w, r, http.StatusServiceUnavailable, "INVENTORY_UNAVAILABLE", "Interface inventory is unavailable; inspect the engine journal.")
		return
	}
	writeError(w, r, http.StatusServiceUnavailable, "REALIZE_UNAVAILABLE", "The apply machinery is unavailable; inspect the engine journal.")
}

// ifupdownLimits are what an operator accepts by taking over this ifupdown host; the assistant's review shows them.
func ifupdownLimits(ifupdown []host.IfupdownInterface) []render.Finding {
	limit := func(message string) render.Finding {
		return render.Finding{Severity: "warning", Code: "TAKEOVER_LIMIT", Message: message}
	}
	out := []render.Finding{limit("Scripts in /etc/network/if-up.d and if-down.d stop running once the takeover is confirmed.")}
	// dhcpcd derives private SLAAC addresses and networkd EUI-64 ones; the kernel's own SLAAC addresses do not change.
	if slices.ContainsFunc(ifupdown, func(i host.IfupdownInterface) bool { return i.IPv6.Mode == "slaac" && i.DHCP }) {
		out = append(out, limit("dhcpcd's SLAAC IPv6 addresses change at the takeover, so a session on one is cut; DHCP and DHCPv6 addresses are kept."))
	}
	return out
}

// proposeIfupdown proposes taking over what ifupdown configures, refusing as the apply gates would.
func (h *takeoverHandler) proposeIfupdown(w http.ResponseWriter, r *http.Request, view proposalView, links []host.LinkStatus, managed []host.Managed, routes []string) {
	ctx := r.Context()
	facts, err := h.read.Facts()
	if err != nil {
		slog.ErrorContext(ctx, "host inventory unavailable for the takeover proposal", "error", err)
		writeError(w, r, http.StatusServiceUnavailable, "INVENTORY_UNAVAILABLE", "Interface inventory is unavailable; inspect the engine journal.")
		return
	}
	found, problems := host.ReadIfupdown(h.read.Root, links, routes)
	view.Findings = append(view.Findings, ifupdownFindings(problems, facts, managed)...)
	if len(found) == 0 && len(view.Findings) == 0 {
		view.Findings = append(view.Findings, render.Finding{Severity: "error", Code: "TAKEOVER_UNSUPPORTED_BASELINE", Path: "/etc/network/interfaces", Message: "ifupdown configures no interface here."})
	}
	if len(view.Findings) > 0 {
		view.State = "unsupported"
		writeJSON(w, r, http.StatusOK, view)
		return
	}
	detected := make([]host.Detected, len(found))
	offered := false
	for i, f := range found {
		detected[i] = f.Detected
		offered = offered || f.DHCP || f.IPv6.Mode == "slaac" && !f.KernelSLAAC
	}
	if !offered {
		// Nothing offers servers after the takeover, so propose the ones in use; none leaves the choice to the operator.
		servers, domains, err := h.read.Resolved(ctx)
		if err != nil {
			slog.ErrorContext(ctx, "ask systemd-resolved for the host's servers", "error", err)
		}
		uplink := max(slices.IndexFunc(detected, func(d host.Detected) bool { return d.DefaultRoute }), 0)
		if len(servers) > 0 {
			detected[uplink].DNS, detected[uplink].Domains = servers, slices.DeleteFunc(domains, func(d string) bool { return strings.HasPrefix(d, "~") })
		}
	}
	view.State, view.Operations, view.Adapters = "ready", proposalOperations(detected, viaInterface(links, r)), adapters(detected, links, true)
	writeJSON(w, r, http.StatusOK, view)
}
