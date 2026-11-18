package generation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Ownership is what a factory reset carries across the wipe so the fresh tree still owns host networking:
// the takeover, the baseline kind, whose nftables mask it is, and ifupdown's interfaces with their dhcpcd DUIDs.
// It holds none of the pre-Andaru files, so no hand-back to them is possible afterwards.
type Ownership struct {
	Takeover       bool              `json:"takeover"`
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
	if !p.Takeover {
		return Ownership{}, nil
	}
	if !captured || m.Kind == "" {
		return Ownership{}, errors.New("the takeover's baseline is missing; ownership cannot be carried over")
	}
	o := Ownership{Takeover: true, Kind: m.Kind, NftablesMasked: m.NftablesMasked}
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
	if o.Kind != "" {
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
func (r Realizer) resetTree() bool {
	var m baselineManifest
	_, err := readJSON(filepath.Join(r.baselineDir(), "manifest.json"), &m)
	return err == nil && m.Reset
}

const denyAllComment = "andaru deny-all"

// denyAll replaces table inet andaru with one that drops all input except loopback and all forwarding.
// It is the filter of every reset state without a confirmed generation: nothing is reachable but the console.
func (r Realizer) denyAll(ctx context.Context) error {
	script := "table inet andaru\ndelete table inet andaru\ntable inet andaru {\n\tcomment \"" + denyAllComment + "\"\n" +
		"\tchain input {\n\t\ttype filter hook input priority filter; policy drop;\n\t\tiif \"lo\" accept\n\t}\n" +
		"\tchain forward {\n\t\ttype filter hook forward priority filter; policy drop;\n\t}\n}\n"
	if err := os.MkdirAll(r.dir(), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(r.dir(), "deny-all-*.nft")
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
		return fmt.Errorf("load the deny-all filter: %w", err)
	}
	return nil
}
