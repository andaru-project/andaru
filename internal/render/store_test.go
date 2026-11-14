package render

import (
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/generation"
)

// Every file a fixture renders is one the generation store accepts, as the apply hands them over: a rendered path
// the store refuses fails every apply that renders it.
func TestRenderedFilesStorable(t *testing.T) {
	names, err := fs.Glob(testdata, "testdata/*.links.json")
	if err != nil || len(names) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, path := range names {
		name := strings.TrimSuffix(filepath.Base(path), ".links.json")
		t.Run(name, func(t *testing.T) {
			files := render(t, fixture(t, name)).Files
			// The apply refuses an enabled PPPoE link (APPLY_UNSUPPORTED), so its files never reach the store.
			if slices.ContainsFunc(files, func(f File) bool { return strings.HasPrefix(f.Path, "/run/andaru/ppp/") }) {
				t.Skip("an enabled PPPoE link")
			}
			var m generation.Manifest
			contents := map[string][]byte{}
			for _, f := range files {
				m.Files = append(m.Files, generation.FileEntry{Path: f.Path, Mode: f.Mode})
				contents[f.Path] = []byte(f.Real())
			}
			if _, err := generation.Create(t.TempDir(), m, contents); err != nil {
				t.Fatal(err)
			}
		})
	}
}
