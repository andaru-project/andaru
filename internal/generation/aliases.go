package generation

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/andaru-project/andaru/internal/aliasfeed"
)

// dynamicSet is a set the filter declares without elements for an address alias fed from a URL or from host names,
// with the hash of the source it was rendered for: the renderer gives every other address set its elements.
var dynamicSet = regexp.MustCompile(`(?m)^\tset (adr_[0-9a-z_]+)_([46]) \{ type ipv[46]_addr; flags interval; auto-merge; comment "feed:([0-9a-f]{16})"; \}$`)

// feedSet is a set dynamicSet matched: its name and family, the hash of its source, and the alias it is for by the
// state directory, "" when no state file names it.
type feedSet struct{ name, family, id, hash string }

// feedSets returns each set script declares for an alias fed from a URL or from host names.
func feedSets(stateDir string, script []byte) []feedSet {
	sets := dynamicSet.FindAllSubmatch(script, -1)
	if len(sets) == 0 {
		return nil
	}
	// A set is named after its alias with every hyphen an underscore.
	ids := map[string]string{}
	for id := range aliasfeed.Sources(stateDir) {
		ids[strings.ReplaceAll(id, "-", "_")] = id
	}
	out := make([]feedSet, len(sets))
	for i, m := range sets {
		out[i] = feedSet{string(m[1]) + "_" + string(m[2]), string(m[2]), ids[string(m[1])], string(m[3])}
	}
	return out
}

// LiveFeeds returns the hashes of the sources the live generation's filter fills its alias sets from, by alias, for
// those with a state file: none without a live generation, or one without a filter.
func LiveFeeds(stateDir string) (map[string][]string, error) {
	dir := Dir(stateDir)
	p, err := readPointers(dir)
	if err != nil || p.Live == nil {
		return nil, err
	}
	g, err := read(dir, *p.Live, p)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, f := range g.Files {
		if f.Path != filterEntry {
			continue
		}
		data, err := g.content(dir, f)
		if err != nil {
			return nil, err
		}
		for _, s := range feedSets(stateDir, data) {
			if s.id != "" && !slices.Contains(out[s.id], s.hash) {
				out[s.id] = append(out[s.id], s.hash)
			}
		}
	}
	return out, nil
}

// aliasFill returns the nft commands that load each such set script declares with the last good members of the
// source its comment names, from that source's state file: a source without one, or one that cannot be read, has its
// sets flushed only, so they match nothing.
func aliasFill(stateDir string, script []byte) []byte {
	sets := feedSets(stateDir, script)
	if len(sets) == 0 {
		return nil
	}
	// Each source is read once, so its two families' sets hold the same version of its list.
	read := map[[2]string][2][]string{}
	var out strings.Builder
	for _, set := range sets {
		var list []string
		if set.id != "" {
			key := [2]string{set.id, set.hash}
			members, ok := read[key]
			if !ok {
				members[0], members[1] = readMembers(stateDir, set.id, set.hash)
				read[key] = members
			}
			if list = members[0]; set.family == "6" {
				list = members[1]
			}
		}
		out.WriteString("flush set inet andaru " + set.name + "\n")
		if len(list) > 0 {
			out.WriteString("add element inet andaru " + set.name + " { " + strings.Join(list, ", ") + " }\n")
		}
	}
	return []byte(out.String())
}

// readMembers reads a source's members by family, checked again: a state that does not read gives none, and is left
// for the engine to move aside.
func readMembers(stateDir, id, hash string) (v4, v6 []string) {
	s, err := aliasfeed.Read(stateDir, id, hash)
	if err != nil {
		slog.Warn("alias set not filled; it matches nothing", "alias", id, "error", err)
		return nil, nil
	}
	list, err := aliasfeed.Members(s.Members)
	if err != nil {
		slog.Warn("alias set not filled; it matches nothing", "alias", id, "error", err)
		return nil, nil
	}
	return aliasfeed.Split(list)
}

// FillAliases loads the last good members of each address alias fed from a URL or from host names into the live
// filter's sets, in one transaction. Without the table, or a live generation with a filter, there is nothing to fill.
func (r Realizer) FillAliases(ctx context.Context) error {
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if loaded, err := r.tableLoaded(ctx); err != nil || !loaded {
		return err
	}
	p, err := readPointers(r.dir())
	if err != nil || p.Live == nil {
		return err
	}
	g, err := read(r.dir(), *p.Live, p)
	if err != nil {
		return err
	}
	path, _, ok, err := r.filter(g)
	if err != nil || !ok {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	fill := aliasFill(r.StateDir, data)
	if len(fill) == 0 {
		return nil
	}
	return r.loadScript(ctx, fill)
}

// loadScript runs nft -f on a script written beside the generations.
func (r Realizer) loadScript(ctx context.Context, script []byte) error {
	f, err := os.CreateTemp(r.dir(), "filter-*.nft")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(script)
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	_, err = r.Run(ctx, "/usr/sbin/nft", "-f", f.Name())
	return err
}
