package generation

import (
	"os"
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
	// networkd's per-interface IPv6Forwarding= does not route IPv6; only the global switch does.
	if b.forwarding() != "1\n" {
		t.Fatalf("IPv6 forwarding %q with the filter loaded", b.forwarding())
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
	// networkd leaves IPv4Forwarding= as it was when a file omits it, so nothing would stop routing unfiltered.
	v4, _ := os.ReadFile(filepath.Join(b.net.root, ipv4Forwarding))
	if b.net.table != "" || b.forwarding() != "0\n" || string(v4) != "0\n" {
		t.Fatalf("table %q, forwarding v6 %q v4 %q on the baseline", b.net.table, b.forwarding(), v4)
	}
}

func (b *bench) forwarding() string {
	data, _ := os.ReadFile(filepath.Join(b.net.root, ipv6Forwarding))
	return string(data)
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

func TestFilterFollowsRenamedAdapters(t *testing.T) {
	b := newBench(t)
	// Rendered while the card with this MAC was eth7 and another card (now absent) was eth9; the kernel now calls the first eth0.
	s := "# interface eth7 02:00:00:00:00:01\n# interface eth9 02:00:00:00:00:99\n" +
		strings.Replace(script("aa"), "\tcomment", "\tset uplinks { type ifname; elements = { \"eth7\", \"eth9\" }; }\n\tcomment", 1)
	id := b.gen(false, map[string]string{"50-andaru-eth0.network": uplinkDHCP, filterEntry: s})
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.net.script, `{ "eth0", "gone:1" }`) || b.net.table != "sha256:aa" {
		t.Fatalf("loaded %q, table %q", b.net.script, b.net.table)
	}
}

// The filter names interfaces, so it loads only once udev has given the cards their final names.
func TestBootLoadsFilterAfterRenaming(t *testing.T) {
	b := newBench(t)
	files := filtered(map[string]string{"50-andaru-eth0.network": uplinkDHCP, "50-andaru-eth0.link": "[Match]\nPermanentMACAddress=02:00:00:00:00:01\n\n[Link]\nName=eth0\n"}, "aa")
	id := b.gen(false, files)
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.reboot()
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	if trigger, load := b.index("udevadm trigger"), b.index("nft -f"); trigger < 0 || load < trigger {
		t.Fatalf("filter loaded before the cards were named: %q", b.net.argv)
	}
}

// A host booted with ipv6.disable=1 has no IPv6 sysctls; the filter still loads and is still removed.
func TestFilterWithoutIPv6(t *testing.T) {
	b := newBench(t)
	if err := os.RemoveAll(filepath.Join(b.net.root, "proc")); err != nil {
		t.Fatal(err)
	}
	id := b.gen(false, filtered(one, "aa"))
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Rollback(t.Context(), id, "operator"); err != nil || b.net.table != "" {
		t.Fatalf("rollback %v, table %q left", err, b.net.table)
	}
}

// A failed listing is not an absent table: the filter may still be loaded, so the rollback must not report success.
func TestRollbackKeepsPendingWhenTableListFails(t *testing.T) {
	b := newBench(t)
	id := b.gen(false, filtered(one, "aa"))
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.net.fail = "nft -j list"
	if err := b.r.Rollback(t.Context(), id, "operator"); err == nil || b.net.table != "sha256:aa" || b.pointers().Pending == nil {
		t.Fatalf("rollback %v, table %q, pointers %+v", err, b.net.table, b.pointers())
	}
}

// The card rendered as eth0 is gone and the one rendered as eth7 is now eth0: eth0's rules must not reach it.
func TestFilterFailsClosedForAbsentCard(t *testing.T) {
	b := newBench(t)
	s := "# interface eth0 02:00:00:00:00:99\n# interface eth7 02:00:00:00:00:01\n" +
		strings.Replace(script("aa"), "\tcomment", "\tset uplinks { type ifname; elements = { \"eth0\", \"eth7\" }; }\n\tcomment", 1)
	id := b.gen(false, map[string]string{"50-andaru-eth0.network": uplinkDHCP, filterEntry: s})
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.net.script, `{ "gone:0", "eth0" }`) {
		t.Fatalf("loaded %q", b.net.script)
	}
}

func TestBootReportsWhyTheFilterFailed(t *testing.T) {
	b := newBench(t)
	id := b.gen(false, filtered(one, "aa"))
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.reboot()
	b.net.table = ""
	b.net.fail = "nft -f"
	if err := b.r.Boot(t.Context()); err == nil || !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("boot error %v", err)
	}
}

// nft took the filter but its readback failed: booting the baseline must not leave that table behind.
func TestBootOnBaselineDropsHalfLoadedFilter(t *testing.T) {
	b := cloud(t)
	id := b.gen(true, filtered(one, "aa"))
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.reboot()
	b.net.table = ""
	b.net.tableComment = "sha256:other"
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.net.table != "" || b.pointers().Live != nil {
		t.Fatalf("table %q, live %v on the baseline", b.net.table, b.pointers().Live)
	}
	b.wantDisk(netplan)
}

// A previous generation whose filter does not load gives way to last-known-good, files and filter both.
func TestRollbackSkipsGenerationWhoseFilterFails(t *testing.T) {
	b := newBench(t)
	first, second := b.gen(false, filtered(one, "11")), b.gen(false, filtered(two, "22"))
	for _, id := range []int{first, second} {
		if err := b.r.Activate(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		if err := b.r.Confirm(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	third := b.gen(false, filtered(map[string]string{"50-andaru-eth0.network": uplinkStatic}, "33"))
	if err := b.r.Activate(t.Context(), third); err != nil {
		t.Fatal(err)
	}
	b.net.fail = "nft -f " + filepath.Join(b.dir, itoa(second))
	if err := b.timerFires(third); err != nil {
		t.Fatal(err)
	}
	b.wantDisk(one)
	if b.net.table != "sha256:11" || deref(b.pointers().Live) != first {
		t.Fatalf("table %q, pointers %+v", b.net.table, b.pointers())
	}
}

// Without nft installed Andaru never loaded a table, so a host without a filter applies as before.
func TestNoNftMeansNoTable(t *testing.T) {
	b := newBench(t)
	b.net.noNft = true
	if err := b.r.Activate(t.Context(), b.gen(false, one)); err != nil {
		t.Fatal(err)
	}
}

// Removing the filter checks nft works before any file moves: files must never run under a filter that stays.
func TestRemovingTheFilterChecksNftFirst(t *testing.T) {
	b := newBench(t)
	first := b.gen(false, filtered(one, "aa"))
	if err := b.r.Activate(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	b.net.argv, b.net.fail = nil, "nft -j list"
	if err := b.r.Activate(t.Context(), b.gen(false, one)); err == nil || b.index("networkctl reload") >= 0 {
		t.Fatalf("files moved while the filter could not be removed: %v %q", err, b.net.argv)
	}
}

// A confirmed filter that half-loaded at boot must not stay over last-known-good's unfiltered files.
func TestBootDropsHalfLoadedFilterForUnfilteredFallback(t *testing.T) {
	b := cloud(t)
	first := b.gen(true, one)
	if err := b.r.Activate(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	second := b.gen(false, filtered(one, "aa"))
	if err := b.r.Activate(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	b.reboot()
	b.net.table, b.net.tableComment = "", "sha256:other"
	if err := b.r.Boot(t.Context()); err != nil {
		t.Fatal(err)
	}
	if live := b.pointers().Live; live == nil || *live != first || b.net.table != "" {
		t.Fatalf("live %v, table %q", live, b.net.table)
	}
}
