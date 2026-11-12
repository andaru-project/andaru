package generation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/andaru-project/andaru/internal/host"
)

// filterEntry is a generation's packet filter: an nft script loaded from the generation directory, never placed in /run.
const filterEntry = "nft:inet/andaru"

// ipv6Forwarding is the kernel's global IPv6 switch, under the realizer's root: networkd's per-interface
// IPv6Forwarding= does not route IPv6 by itself. It is on exactly while Andaru's filter is loaded.
const ipv6Forwarding = "/proc/sys/net/ipv6/conf/all/forwarding"

// ipv4Forwarding is the IPv4 switch for every interface at once; networkd only ever turns it on.
const ipv4Forwarding = "/proc/sys/net/ipv4/conf/all/forwarding"

var (
	tableComment  = regexp.MustCompile(`comment "([^"]+)"`)
	interfaceLine = regexp.MustCompile(`(?m)^# interface (\S+) (\S+)$`)
)

// filter returns the generation's script and its table comment; ok is false for a generation without a filter.
func (r Realizer) filter(g Generation) (path, comment string, ok bool, err error) {
	for _, f := range g.Files {
		if f.Path != filterEntry {
			continue
		}
		data, err := g.content(r.dir(), f)
		if err != nil {
			return "", "", true, err
		}
		m := tableComment.FindSubmatch(data)
		if m == nil {
			return "", "", true, fmt.Errorf("generation %d: its filter has no table comment", g.ID)
		}
		return filepath.Join(r.dir(), strconv.Itoa(g.ID), "files", filepath.Base(f.Path)), string(m[1]), true, nil
	}
	return "", "", false, nil
}

// loadFilter replaces table inet andaru with the generation's in one transaction, then checks the kernel has it.
func (r Realizer) loadFilter(ctx context.Context, path, comment string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("load the packet filter: %w", err)
	}
	renamed, err := r.currentNames(ctx, data)
	if err != nil {
		return fmt.Errorf("load the packet filter: %w", err)
	}
	if !bytes.Equal(renamed, data) {
		f, err := os.CreateTemp(r.dir(), "filter-*.nft")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		_, err = f.Write(renamed)
		if err = errors.Join(err, f.Close()); err != nil {
			return err
		}
		path = f.Name()
	}
	if _, err := r.Run(ctx, "/usr/sbin/nft", "-f", path); err != nil {
		return fmt.Errorf("load the packet filter: %w", err)
	}
	out, err := r.Run(ctx, "/usr/sbin/nft", "-j", "list", "table", "inet", "andaru")
	if err != nil {
		return fmt.Errorf("read back the packet filter: %w", err)
	}
	var listing struct {
		Nftables []struct {
			Table *struct{ Comment string } `json:"table"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(out, &listing); err != nil {
		return fmt.Errorf("read back the packet filter: %w", err)
	}
	for _, item := range listing.Nftables {
		if item.Table != nil && item.Table.Comment == comment {
			return r.sysctl(ipv6Forwarding, "1\n")
		}
	}
	return fmt.Errorf("the loaded packet filter is not this generation's")
}

// currentNames rewrites the interface names in a filter to what the kernel calls each card now, found by its
// permanent MAC: a reboot can name a card differently from when the filter was rendered (its .link file not applied).
func (r Realizer) currentNames(ctx context.Context, script []byte) ([]byte, error) {
	cards := interfaceLine.FindAllSubmatch(script, -1)
	if len(cards) == 0 {
		return script, nil
	}
	out, err := r.Run(ctx, "/usr/bin/ip", "-d", "-j", "address", "show")
	if err != nil {
		return nil, err
	}
	links, err := host.ParseLinks(out)
	if err != nil {
		return nil, err
	}
	var pairs []string
	for n, c := range cards {
		was, mac := string(c[1]), string(c[2])
		// Virtual links inherit their parent's MAC; only the card itself carries its name.
		i := slices.IndexFunc(links, func(l host.LinkStatus) bool { return l.Kind == "" && strings.EqualFold(l.PermanentAddress, mac) })
		switch {
		case i < 0:
			// Fail closed: ':' is invalid in an interface name, so the absent card's rules match nothing.
			// Numbered, not named after it: nft refuses names over 15 characters.
			pairs = append(pairs, `"`+was+`"`, `"gone:`+strconv.Itoa(n)+`"`)
		case links[i].Name != was:
			pairs = append(pairs, `"`+was+`"`, `"`+links[i].Name+`"`)
		}
	}
	// Names appear only quoted; one Replacer swaps two cards' names without clobbering.
	return []byte(strings.NewReplacer(pairs...).Replace(string(script))), nil
}

// sysctl writes a kernel switch; one that does not exist (IPv6 on a kernel booted without it) has nothing to set.
func (r Realizer) sysctl(path, value string) error {
	f, err := os.OpenFile(filepath.Join(r.Root, path), os.O_WRONLY|os.O_TRUNC, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = f.WriteString(value)
	return errors.Join(err, f.Close())
}

// dropFilter removes table inet andaru; a table already gone is success.
func (r Realizer) dropFilter(ctx context.Context) error {
	loaded, err := r.tableLoaded(ctx)
	if err != nil || !loaded {
		return err
	}
	return r.deleteTable(ctx)
}

// tableLoaded reports whether table inet andaru exists. Without nft installed Andaru never loaded one.
func (r Realizer) tableLoaded(ctx context.Context) (bool, error) {
	_, err := r.Run(ctx, "/usr/sbin/nft", "-j", "list", "table", "inet", "andaru")
	// nft reports an absent table as ENOENT on stderr, which the runner puts in its error; anything else may leave it loaded.
	if err != nil && (errors.Is(err, fs.ErrNotExist) || strings.Contains(err.Error(), "Error: No such file or directory")) {
		return false, nil
	}
	return err == nil, err
}

// deleteTable switches forwarding off and removes the loaded table.
func (r Realizer) deleteTable(ctx context.Context) error {
	// Forwarding was Andaru's, switched on with its filter; it goes before the filter does.
	for _, path := range []string{ipv4Forwarding, ipv6Forwarding} {
		if err := r.sysctl(path, "0\n"); err != nil {
			return err
		}
	}
	_, err := r.Run(ctx, "/usr/sbin/nft", "delete", "table", "inet", "andaru")
	return err
}

// toward moves the host to target's files, tightening first and loosening last: a target with a filter
// has it loaded before its files; without one (or the baseline, nil) the files go first and the table last.
func (r Realizer) toward(ctx context.Context, target *Generation, place func() error) error {
	if target != nil {
		path, comment, ok, err := r.filter(*target)
		if err != nil {
			return err
		}
		if ok {
			if err := r.loadFilter(ctx, path, comment); err != nil {
				return err
			}
			return place()
		}
	}
	// nft must answer before any file moves, or the files could end up under a filter that cannot be removed.
	loaded, err := r.tableLoaded(ctx)
	if err != nil {
		return err
	}
	if err := place(); err != nil || !loaded {
		return err
	}
	return r.deleteTable(ctx)
}
