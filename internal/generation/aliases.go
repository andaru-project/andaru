package generation

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"

	"github.com/andaru-project/andaru/internal/aliasfeed"
)

// dynamicSet is a set the filter declares without elements for an address alias fed from a URL or from host names:
// the renderer gives every other address set its elements.
var dynamicSet = regexp.MustCompile(`(?m)^\tset (adr_[0-9a-z_]+)_([46]) \{ type ipv[46]_addr; flags interval; auto-merge; \}$`)

// aliasFill returns the nft commands that load each such set script declares with the alias's last good members,
// from its state file: a set whose alias has none, or one that cannot be read, is flushed only.
func aliasFill(stateDir string, script []byte) []byte {
	sets := dynamicSet.FindAllSubmatch(script, -1)
	if len(sets) == 0 {
		return nil
	}
	// A set is named after its alias with every hyphen an underscore.
	ids := map[string]string{}
	entries, _ := os.ReadDir(aliasfeed.Dir(stateDir))
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), ".json"); ok && e.Type().IsRegular() {
			ids[strings.ReplaceAll(id, "-", "_")] = id
		}
	}
	var out strings.Builder
	for _, m := range sets {
		alias, family := string(m[1]), string(m[2])
		var list []string
		if id, ok := ids[alias]; ok {
			v4, v6 := readMembers(aliasfeed.Path(stateDir, id))
			list = map[string][]string{"4": v4, "6": v6}[family]
		}
		set := alias + "_" + family
		out.WriteString("flush set inet andaru " + set + "\n")
		if len(list) > 0 {
			out.WriteString("add element inet andaru " + set + " { " + strings.Join(list, ", ") + " }\n")
		}
	}
	return []byte(out.String())
}

// readMembers reads a state file's members by family, checked again: a file that does not read gives none.
func readMembers(path string) (v4, v6 []string) {
	data, err := os.ReadFile(path)
	var s aliasfeed.State
	if err != nil || json.Unmarshal(data, &s) != nil {
		return nil, nil
	}
	list, err := aliasfeed.Members(s.Members)
	if err != nil {
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
