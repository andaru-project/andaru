package addon

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// DefaultCatalogDir is where the signed Andaru appliance image ships add-on manifests.
const DefaultCatalogDir = "/usr/share/andaru/addons"

// Catalog holds validated add-on manifests loaded from the catalog directory.
type Catalog struct {
	ordered []*Manifest
	byID    map[string]*Manifest
	bySlug  map[string]*Manifest
}

// EmptyCatalog returns an empty catalog with no add-ons.
func EmptyCatalog() *Catalog {
	return &Catalog{
		byID:   map[string]*Manifest{},
		bySlug: map[string]*Manifest{},
	}
}

// LoadCatalog loads and strictly validates every <dir>/<slug>/manifest.json.
// A non-existent directory returns an empty catalog and nil error.
func LoadCatalog(dir string) (*Catalog, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return EmptyCatalog(), nil
		}
		return nil, fmt.Errorf("read add-on catalog %s: %w", dir, err)
	}
	cat := EmptyCatalog()
	usedIfaces := map[string]string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		slug := entry.Name()
		manifestPath := filepath.Join(dir, slug, "manifest.json")
		raw, err := readBoundedFile(manifestPath, MaxManifestBytes)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", manifestPath, err)
		}
		m, err := ParseManifest(raw)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", manifestPath, err)
		}
		if m.Slug != slug {
			return nil, fmt.Errorf("%s: manifest slug %q does not match directory name %q", manifestPath, m.Slug, slug)
		}
		if prev, dup := cat.byID[m.ID]; dup {
			return nil, fmt.Errorf("%s: duplicate add-on id %q (already declared by %s)", manifestPath, m.ID, prev.Slug)
		}
		for _, ifc := range m.Interfaces {
			if prev, dup := usedIfaces[ifc.Name]; dup {
				return nil, fmt.Errorf("%s: interface %q already declared by add-on %q", manifestPath, ifc.Name, prev)
			}
			usedIfaces[ifc.Name] = m.Slug
		}
		cat.ordered = append(cat.ordered, m)
		cat.byID[m.ID] = m
		cat.bySlug[m.Slug] = m
	}
	return cat, nil
}

func readBoundedFile(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxBytes {
		return nil, fmt.Errorf("file exceeds %d bytes", maxBytes)
	}
	return raw, nil
}

// All returns all catalog manifests sorted by slug.
func (c *Catalog) All() []*Manifest {
	if c == nil {
		return nil
	}
	return slices.Clone(c.ordered)
}

// ByID looks up a manifest by its stable reverse-DNS id.
func (c *Catalog) ByID(id string) (*Manifest, bool) {
	if c == nil {
		return nil, false
	}
	m, ok := c.byID[id]
	return m, ok
}

// BySlug looks up a manifest by its short slug.
func (c *Catalog) BySlug(slug string) (*Manifest, bool) {
	if c == nil {
		return nil, false
	}
	m, ok := c.bySlug[slug]
	return m, ok
}
