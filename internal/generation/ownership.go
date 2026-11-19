package generation

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/andaru-project/andaru/internal/kept"
)

// Ownership is what a factory reset carries across the wipe so the fresh tree still owns host networking:
// the takeover, the baseline kind, whose nftables mask it is, and ifupdown's interfaces with their dhcpcd DUIDs.
// It holds none of the pre-Andaru files, so no hand-back to them is possible afterwards.
type Ownership struct {
	Takeover bool `json:"takeover"`
	// Live records that a generation ran, so the reset brings a network up even on a host never taken over.
	Live           bool              `json:"live,omitempty"`
	Kind           string            `json:"kind,omitempty"`
	NftablesMasked bool              `json:"nftablesMasked,omitempty"`
	Ifupdown       *IfupdownBaseline `json:"ifupdown,omitempty"`
}

// ReadOwnership reads the record from the live tree. A host Andaru never took over has an empty record.
func (r Realizer) ReadOwnership() (Ownership, error) {
	p, err := readPointers(r.dir())
	if err != nil {
		return Ownership{}, err
	}
	var m baselineManifest
	captured, err := readJSON(filepath.Join(r.baselineDir(), "manifest.json"), &m)
	if err != nil {
		return Ownership{}, err
	}
	// A tree an earlier reset wrote ran Andaru's network even with nothing live now: it stays fenced.
	ran := p.Live != nil || captured && m.Reset
	if !p.Takeover {
		return Ownership{Live: ran}, nil
	}
	if !captured || m.Kind == "" {
		return Ownership{}, errors.New("the takeover's baseline is missing; ownership cannot be carried over")
	}
	o := Ownership{Takeover: true, Live: ran, Kind: m.Kind, NftablesMasked: m.NftablesMasked}
	if m.Kind == "ifupdown" {
		if m.Ifupdown == nil {
			return Ownership{}, errors.New("the ifupdown baseline lists no interfaces")
		}
		for _, i := range m.Ifupdown.Interfaces {
			if i.DHCP && (i.Identity == nil || len(i.Identity.DUID) == 0) {
				return Ownership{}, fmt.Errorf("the ifupdown baseline has no DUID for %s", i.Name)
			}
		}
		b := *m.Ifupdown
		b.Connected = []string{}
		o.Ifupdown = &b
	}
	return o, nil
}

// WriteOwnership writes the record into an empty generations directory: pointers with only the takeover, and a
// baseline manifest with no files, marked as a reset tree.
func (r Realizer) WriteOwnership(o Ownership) error {
	if err := os.MkdirAll(r.dir(), 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(r.dir())
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() != "lock" {
			return fmt.Errorf("ownership goes only into an empty generations directory; %s exists", e.Name())
		}
	}
	// A host whose generation ran without the takeover flag still gets the marker, so its boots stay fenced.
	if o.Kind != "" || o.Live {
		if err := os.Mkdir(r.baselineDir(), 0o700); err != nil {
			return err
		}
		m := baselineManifest{Kind: o.Kind, Files: []baselineFile{}, Ifupdown: o.Ifupdown, NftablesMasked: o.NftablesMasked, Reset: true}
		if err := writeJSON(filepath.Join(r.baselineDir(), "manifest.json"), m); err != nil {
			return err
		}
	}
	return r.writePointers("ownership:pointers", Pointers{Takeover: o.Takeover})
}

// resetTree reports a tree a factory reset wrote: no generation may hand the host back to its pre-Andaru setup.
// A manifest that exists but cannot be read counts as one: only its absence proves an ordinary tree.
func (r Realizer) resetTree() bool {
	var m baselineManifest
	_, err := readJSON(filepath.Join(r.baselineDir(), "manifest.json"), &m)
	return err != nil || m.Reset
}

// handsBack reports a tree whose baseline is the host's own pre-Andaru configuration, with its own firewall.
func (r Realizer) handsBack() bool {
	var m baselineManifest
	captured, err := readJSON(filepath.Join(r.baselineDir(), "manifest.json"), &m)
	return err == nil && captured && !m.Reset
}

const denyAllComment = "andaru deny-all"

// DenyAll replaces table inet andaru with one that drops all input except loopback and all forwarding.
// It is the filter of every reset state without a confirmed generation: nothing is reachable but the console.
func (r Realizer) DenyAll(ctx context.Context) error {
	return r.loadStatic(ctx, denyAllComment, "", "deny-all")
}

// factoryFilter is DenyAll that still lets a box never set up take DHCPv4, DHCPv6 and SLAAC on its factory network.
func (r Realizer) factoryFilter(ctx context.Context) error {
	return r.loadStatic(ctx, "andaru factory", "\t\tct state established,related accept\n"+
		"\t\tudp sport 67 udp dport 68 accept\n\t\tudp sport 547 udp dport 546 accept\n"+
		"\t\ticmpv6 type { nd-neighbor-solicit, nd-neighbor-advert, nd-router-advert } accept\n", "factory")
}

// loadStatic replaces table inet andaru with a drop-all one whose input chain also runs accept, and switches forwarding off.
func (r Realizer) loadStatic(ctx context.Context, comment, accept, what string) error {
	script := "table inet andaru\ndelete table inet andaru\ntable inet andaru {\n\tcomment \"" + comment + "\"\n" +
		"\tchain input {\n\t\ttype filter hook input priority filter; policy drop;\n\t\tiif \"lo\" accept\n" + accept + "\t}\n" +
		"\tchain forward {\n\t\ttype filter hook forward priority filter; policy drop;\n\t}\n}\n"
	// Under /run, never the state tree: during a reset that tree may be missing, and must not be created.
	f, err := os.CreateTemp(filepath.Join(r.Root, "/run"), "andaru-"+what+"-*.nft")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(script)
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	if err = errors.Join(r.sysctl(ipv4Forwarding, "0\n"), r.sysctl(ipv6Forwarding, "0\n")); err != nil {
		return err
	}
	if _, err := r.Run(ctx, "/usr/sbin/nft", "-f", f.Name()); err != nil {
		return fmt.Errorf("load the %s filter: %w", what, err)
	}
	return nil
}

// catchAllPath is render.UnclassifiedPath; render's tests import this package, so it cannot import render.
const catchAllPath = "/run/systemd/network/51-andaru-unclassified.network"

// catchAll is render.Unclassified, which a test keeps equal: it keeps every en* link no generation names down.
var catchAll = placed{path: catchAllPath, mode: 0o644, uid: -1,
	data: []byte("[Match]\nName=en*\n\n[Link]\nActivationPolicy=down\nRequiredForOnline=no\n\n[Network]\nLinkLocalAddressing=no\nDHCP=no\nIPv6AcceptRA=no\n")}

// setUp reports a box that was set up: a kept copy exists. Only their absence proves a box never set up;
// any other error counts as set up, so the factory network's DHCP never reaches WAN by mistake.
func (r Realizer) setUp() bool {
	root := cmp.Or(r.KeptRoot, filepath.Join(r.Root, kept.Root))
	for _, name := range []kept.Name{"recovery", "last-lan", "first"} {
		if _, err := os.Lstat(filepath.Join(root, string(name))); !errors.Is(err, fs.ErrNotExist) {
			return true
		}
	}
	return false
}

// bareFilter loads the filter of a host on which no generation runs: deny-all once set up, else the factory filter.
func (r Realizer) bareFilter(ctx context.Context) error {
	if r.setUp() {
		return r.DenyAll(ctx)
	}
	return r.factoryFilter(ctx)
}

// fence leaves a host on which no generation runs with its bare filter and, once set up, the catch-all:
// DHCPv4 uses packet sockets that bypass the filter, so only the catch-all keeps WAN from taking a lease.
func (r Realizer) fence(ctx context.Context) error {
	if err := r.bareFilter(ctx); err != nil || !r.setUp() {
		return err
	}
	return r.place(nil, []placed{catchAll})
}

// Fence is boot while a factory reset blocks it. denyAll, unless the journal says Andaru never ran the network,
// keeps deny-all even on a box never set up.
func (r Realizer) Fence(ctx context.Context, denyAll bool) error {
	if denyAll && !r.setUp() {
		return r.DenyAll(ctx)
	}
	return r.fence(ctx)
}

// Teardown removes the live generation from the host before a factory reset wipes the state tree: its files under
// /run, the devices it made, the services it ran, and its filter, which becomes deny-all. A host with no live
// generation and no takeover runs nothing of Andaru's, so nothing there is touched. It may run again after a crash.
func (r Realizer) Teardown(ctx context.Context) error {
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	p, err := readPointers(r.dir())
	if err != nil {
		return err
	}
	if p.Live == nil && !p.Takeover {
		return nil
	}
	// Paths only, so a live generation whose stored files are damaged still comes off; with no live generation,
	// a taken-over host runs its captured baseline, and those files go too.
	set, err := r.live(p)
	if err != nil {
		return err
	}
	// Fenced first: if the teardown stops half-way, the host is behind deny-all for the retry.
	if err := r.DenyAll(ctx); err != nil {
		return err
	}
	// The catch-all stays: the reset ends behind deny-all, where WAN must not take a lease either.
	keep := []placed{catchAll}
	if err := r.place(set, keep); err != nil {
		return err
	}
	return r.settle(ctx, set, keep)
}
