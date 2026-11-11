package generation

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
)

// filterEntry is a generation's packet filter: an nft script loaded from the generation directory, never placed in /run.
const filterEntry = "nft:inet/andaru"

var tableComment = regexp.MustCompile(`comment "([^"]+)"`)

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
			return nil
		}
	}
	return fmt.Errorf("the loaded packet filter is not this generation's")
}

// dropFilter removes table inet andaru; a table already gone is success.
func (r Realizer) dropFilter(ctx context.Context) error {
	if _, err := r.Run(ctx, "/usr/sbin/nft", "-j", "list", "table", "inet", "andaru"); err != nil {
		return nil
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
	if err := place(); err != nil {
		return err
	}
	return r.dropFilter(ctx)
}
