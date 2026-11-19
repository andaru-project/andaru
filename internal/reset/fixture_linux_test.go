package reset

import (
	"bytes"
	"os"
	"testing"
)

// The reset tests embed their own copy of the configuration fixture, since the root suites run without the source
// tree. In the source tree the two must stay the same.
func TestNetworkFixtureMatchesConfigs(t *testing.T) {
	want, err := os.ReadFile("../config/testdata/network.json")
	if err != nil {
		t.Skip("no source tree:", err)
	}
	if !bytes.Equal(networkFixture, want) {
		t.Fatal("internal/reset/testdata/network.json differs from internal/config/testdata/network.json")
	}
}

// The idle check reads generation state as the check it replaced did: root-owned, not writable by group or others,
// and bounded in size.
func TestReadStrictModesAndLimit(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root-owned files")
	}
	dir := t.TempDir()
	write := func(name string, mode os.FileMode, data []byte) string {
		path := dir + "/" + name
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	var v struct{ State string }
	for _, mode := range []os.FileMode{0o600, 0o640, 0o400} {
		if err := readStrict(write("ok", mode, []byte(`{"State":"confirmed"}`)), &v); err != nil {
			t.Errorf("mode %o refused: %v", mode, err)
		}
		os.Remove(dir + "/ok")
	}
	for _, mode := range []os.FileMode{0o620, 0o602} {
		if err := readStrict(write("w", mode, []byte(`{"State":"confirmed"}`)), &v); err == nil {
			t.Errorf("mode %o accepted", mode)
		}
		os.Remove(dir + "/w")
	}
	if err := readStrict(write("big", 0o600, bytes.Repeat([]byte(" "), metadataLimit+1)), &v); err == nil {
		t.Error("an oversized file was read")
	}
}
