package generation

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func script(sum string) string {
	return "table inet andaru {}\ndelete table inet andaru\ntable inet andaru {\n\tcomment \"sha256:" + sum + "\"\n}\n"
}

// filtered is a generation's files plus its packet filter.
func filtered(files map[string]string, sum string) map[string]string {
	out := map[string]string{filterEntry: script(sum)}
	for k, v := range files {
		out[k] = v
	}
	return out
}

func (b *bench) index(prefix string) int {
	return slices.IndexFunc(b.net.argv, func(a string) bool { return strings.HasPrefix(a, prefix) })
}

func (b *bench) lastIndex(prefix string) int {
	for i := len(b.net.argv) - 1; i >= 0; i-- {
		if strings.HasPrefix(b.net.argv[i], prefix) {
			return i
		}
	}
	return -1
}

func TestActivateLoadsFilterFirst(t *testing.T) {
	b := newBench(t)
	id := b.gen(false, filtered(one, "aa"))
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if load, reload := b.index("nft -f"), b.index("networkctl reload"); load < 0 || load > reload {
		t.Fatalf("filter not loaded before the files: %q", b.net.argv)
	}
	if b.net.table != "sha256:aa" || b.read(id).State.State != Awaiting {
		t.Fatalf("table %q state %+v", b.net.table, b.read(id).State)
	}
	if b.exists(netDir + "/andaru") {
		t.Fatal("the filter entry was placed as a file")
	}
}

func TestActivateFilterFailureMovesNothing(t *testing.T) {
	b := newBench(t)
	b.net.fail = "nft -f"
	id := b.gen(false, filtered(one, "aa"))
	if err := b.r.Activate(t.Context(), id); err == nil {
		t.Fatal("activated without its filter")
	}
	if b.exists(netDir+"/50-andaru-eth0.network") || b.read(id).State.State != Failed {
		t.Fatalf("files moved or state %+v", b.read(id).State)
	}
}

func TestVerifyChecksTableComment(t *testing.T) {
	b := newBench(t)
	b.net.tableComment = "sha256:something-else"
	id := b.gen(false, filtered(one, "aa"))
	if err := b.r.Activate(t.Context(), id); err == nil || !strings.Contains(b.read(id).State.Reason, "filter") {
		t.Fatalf("a foreign table passed verification: %v %+v", err, b.read(id).State)
	}
}

func TestRollbackToBaselineDeletesTableLast(t *testing.T) {
	b := newBench(t)
	id := b.gen(false, filtered(one, "aa"))
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.net.argv = nil
	if err := b.r.Rollback(t.Context(), id, "operator"); err != nil {
		t.Fatal(err)
	}
	if reload, drop := b.index("networkctl reload"), b.index("nft delete table inet andaru"); reload < 0 || drop < reload {
		t.Fatalf("the table went before the files: %q", b.net.argv)
	}
	if b.net.table != "" {
		t.Fatal("table left loaded on the baseline")
	}
}

func TestRollbackToGenerationLoadsItsFilterFirst(t *testing.T) {
	b := newBench(t)
	first := b.gen(false, filtered(one, "11"))
	if err := b.r.Activate(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	second := b.gen(false, filtered(two, "22"))
	if err := b.r.Activate(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	b.net.argv = nil
	if err := b.r.Rollback(t.Context(), second, "operator"); err != nil {
		t.Fatal(err)
	}
	if load, reload := b.index("nft -f"), b.index("networkctl reload"); load < 0 || load > reload || !strings.Contains(b.net.argv[load], filepath.Join("1", "files")) {
		t.Fatalf("the previous filter was not loaded first: %q", b.net.argv)
	}
	if b.net.table != "sha256:11" {
		t.Fatalf("table %q", b.net.table)
	}
}

func TestBootFallsBackWhenTheFilterDoesNotLoad(t *testing.T) {
	b := newBench(t)
	first := b.gen(false, filtered(one, "11"))
	for _, id := range []int{first} {
		if err := b.r.Activate(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		if err := b.r.Confirm(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	second := b.gen(false, filtered(two, "22"))
	if err := b.r.Activate(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	b.reboot()
	b.net.table = ""
	b.net.fail = "nft -f " + filepath.Join(b.dir, "2")
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	p := b.pointers()
	if p.Live == nil || *p.Live != first || b.net.table != "sha256:11" {
		t.Fatalf("live %v table %q", p.Live, b.net.table)
	}
	b.wantDisk(one)
}

var _ = errors.New
