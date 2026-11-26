package generation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/aliasfeed"
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
	id := b.gen(filtered(one, "aa"))
	var told []string // the state each time Activating was called
	b.r.Activating = func(got int) { told = append(told, fmt.Sprint(got, " ", b.read(got).State.State)) }
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprint(id, " ", Activating); !slices.Equal(told, []string{want}) {
		t.Fatalf("told %q, want %q", told, want)
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
	b.net.fail = "nft -f " + b.dir
	id := b.gen(filtered(one, "aa"))
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
	id := b.gen(filtered(one, "aa"))
	if err := b.r.Activate(t.Context(), id); err == nil || !strings.Contains(b.read(id).State.Reason, "filter") {
		t.Fatalf("a foreign table passed verification: %v %+v", err, b.read(id).State)
	}
}

func TestRollbackOfFirstApplyStopsForwarding(t *testing.T) {
	b := newBench(t)
	id := b.gen(filtered(one, "aa"))
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.net.argv = nil
	if err := b.r.Rollback(t.Context(), id, "operator"); err != nil {
		t.Fatal(err)
	}
	// networkd leaves IPv4Forwarding= as it was when a file omits it, so nothing would stop routing unfiltered.
	v4, _ := os.ReadFile(filepath.Join(b.net.root, ipv4Forwarding))
	if b.net.table != "andaru factory" || b.forwarding() != "0\n" || string(v4) != "0\n" {
		t.Fatalf("table %q, forwarding v6 %q v4 %q with no generation", b.net.table, b.forwarding(), v4)
	}
}

func (b *bench) forwarding() string {
	data, _ := os.ReadFile(filepath.Join(b.net.root, ipv6Forwarding))
	return string(data)
}

func TestRollbackToGenerationLoadsItsFilterFirst(t *testing.T) {
	b := newBench(t)
	first := b.gen(filtered(one, "11"))
	if err := b.r.Activate(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	second := b.gen(filtered(two, "22"))
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
	first := b.gen(filtered(one, "11"))
	for _, id := range []int{first} {
		if err := b.r.Activate(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		if err := b.r.Confirm(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	second := b.gen(filtered(two, "22"))
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
	id := b.gen(map[string]string{"50-andaru-eth0.network": uplinkDHCP, filterEntry: s})
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
	id := b.gen(files)
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
	id := b.gen(filtered(one, "aa"))
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Rollback(t.Context(), id, "operator"); err != nil || b.net.table != "andaru factory" {
		t.Fatalf("rollback %v, table %q", err, b.net.table)
	}
}

// A failed listing is not an absent table: the filter may still be loaded, so the rollback must not report success.
func TestRollbackKeepsPendingWhenTableListFails(t *testing.T) {
	b := newBench(t)
	b.live(one)
	id := b.gen(filtered(one, "aa"))
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
	id := b.gen(map[string]string{"50-andaru-eth0.network": uplinkDHCP, filterEntry: s})
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.net.script, `{ "gone:0", "eth0" }`) {
		t.Fatalf("loaded %q", b.net.script)
	}
}

func TestBootReportsWhyTheFilterFailed(t *testing.T) {
	b := newBench(t)
	id := b.gen(filtered(one, "aa"))
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

// nft took the filter but its readback failed: with no generation usable, deny-all replaces that table.
func TestBootWithoutGenerationReplacesHalfLoadedFilter(t *testing.T) {
	b := newBench(t)
	id := b.gen(filtered(one, "aa"))
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.reboot()
	b.net.table = ""
	b.net.tableComment = "sha256:other"
	if err := b.r.Boot(t.Context()); err == nil {
		t.Fatal("boot reported success with nothing usable")
	}
	if b.net.table != denyAllComment || b.pointers().Live != nil {
		t.Fatalf("table %q, live %v with no generation", b.net.table, b.pointers().Live)
	}
	b.wantDisk(withCatchAll(map[string]string{}))
}

// A previous generation whose filter does not load gives way to last-known-good, files and filter both.
func TestRollbackSkipsGenerationWhoseFilterFails(t *testing.T) {
	b := newBench(t)
	first, second := b.gen(filtered(one, "11")), b.gen(filtered(two, "22"))
	for _, id := range []int{first, second} {
		if err := b.r.Activate(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		if err := b.r.Confirm(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	third := b.gen(filtered(map[string]string{"50-andaru-eth0.network": uplinkStatic}, "33"))
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
	if err := b.r.Activate(t.Context(), b.gen(one)); err != nil {
		t.Fatal(err)
	}
}

// Removing the filter checks nft works before any file moves: files must never run under a filter that stays.
func TestRemovingTheFilterChecksNftFirst(t *testing.T) {
	b := newBench(t)
	first := b.gen(filtered(one, "aa"))
	if err := b.r.Activate(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	b.net.argv, b.net.fail = nil, "nft -j list"
	if err := b.r.Activate(t.Context(), b.gen(one)); err == nil || b.index("networkctl reload") >= 0 {
		t.Fatalf("files moved while the filter could not be removed: %v %q", err, b.net.argv)
	}
}

// A confirmed filter that half-loaded at boot must not stay over last-known-good's unfiltered files.
func TestBootDropsHalfLoadedFilterForUnfilteredFallback(t *testing.T) {
	b := newBench(t)
	first := b.gen(one)
	if err := b.r.Activate(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	second := b.gen(filtered(one, "aa"))
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

// A candidate dropped for its filter leaves none of its files behind, .link files included.
func TestBootRemovesTheLinksOfAnUnusableCandidate(t *testing.T) {
	b := newBench(t)
	link := "50-andaru-eth9.link"
	id := b.gen(filtered(map[string]string{"50-andaru-eth0.network": uplinkDHCP, link: "[Match]\nPermanentMACAddress=02:00:00:00:00:09\n\n[Link]\nName=eth9\n"}, "aa"))
	if err := b.r.Activate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.r.Confirm(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	b.reboot()
	b.net.fail = "nft -f"
	if err := b.r.Boot(t.Context()); err == nil {
		t.Fatal("boot reported success with nothing usable")
	}
	if b.exists(netDir + link) {
		t.Fatal("the unusable candidate's .link file stayed")
	}
}

// dynamicScript is a filter with the empty sets of one alias fed from a URL and another never fetched.
func dynamicScript(sum string) string {
	return strings.Replace(script(sum), "\tcomment", "\tset adr_0001_aa_4 { type ipv4_addr; flags interval; auto-merge; }\n"+
		"\tset adr_0001_aa_6 { type ipv6_addr; flags interval; auto-merge; }\n"+
		"\tset adr_0002_bb_4 { type ipv4_addr; flags interval; auto-merge; }\n"+
		"\tset adr_0003_cc_4 { type ipv4_addr; flags interval; auto-merge; elements = { 192.0.2.1 }; }\n\tcomment", 1)
}

func (b *bench) aliasState(id string, members ...string) {
	b.t.Helper()
	data, _ := json.Marshal(aliasfeed.State{Members: members})
	writeFile(b.t, aliasfeed.Path(b.r.StateDir, id), string(data))
}

func TestAliasFill(t *testing.T) {
	b := newBench(t)
	b.aliasState("adr_0001-aa", "198.51.100.0/24", "2001:db8::/32", "192.0.2.7")
	b.aliasState("adr_0003-cc", "203.0.113.1") // a static set is never filled
	want := "flush set inet andaru adr_0001_aa_4\n" +
		"add element inet andaru adr_0001_aa_4 { 192.0.2.7, 198.51.100.0/24 }\n" +
		"flush set inet andaru adr_0001_aa_6\n" +
		"add element inet andaru adr_0001_aa_6 { 2001:db8::/32 }\n" +
		"flush set inet andaru adr_0002_bb_4\n"
	if got := string(aliasFill(b.r.StateDir, []byte(dynamicScript("aa")))); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	// A state that does not read, or holds what is not a list, flushes only.
	writeFile(t, aliasfeed.Path(b.r.StateDir, "adr_0001-aa"), `{"members":["not an address"]}`)
	if got := string(aliasFill(b.r.StateDir, []byte(dynamicScript("aa")))); strings.Contains(got, "add element") {
		t.Fatalf("got\n%s", got)
	}
	if got := aliasFill(b.r.StateDir, []byte(script("aa"))); got != nil {
		t.Fatalf("a filter without such sets: %q", got)
	}
}

// Every load of the filter fills the aliases' sets in the same transaction.
func TestFilterLoadFillsAliases(t *testing.T) {
	b := newBench(t)
	b.aliasState("adr_0001-aa", "198.51.100.0/24")
	g := b.gen(map[string]string{"50-andaru-eth0.network": uplinkDHCP, filterEntry: dynamicScript("bb")})
	if err := b.r.Activate(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(b.net.script, "}\nflush set inet andaru adr_0001_aa_4\nadd element inet andaru adr_0001_aa_4 { 198.51.100.0/24 }\n"+
		"flush set inet andaru adr_0001_aa_6\nflush set inet andaru adr_0002_bb_4\n") || !strings.HasPrefix(b.net.script, "table inet andaru {}") {
		t.Fatalf("loaded %q", b.net.script)
	}
	// realize aliases fills the live filter's sets again from the state files.
	b.aliasState("adr_0001-aa", "2001:db8::1")
	b.net.script = ""
	if err := b.r.FillAliases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if want := "flush set inet andaru adr_0001_aa_4\nflush set inet andaru adr_0001_aa_6\nadd element inet andaru adr_0001_aa_6 { 2001:db8::1 }\nflush set inet andaru adr_0002_bb_4\n"; b.net.script != want {
		t.Fatalf("filled %q", b.net.script)
	}
	// Without the table there is nothing to fill.
	b.net.table, b.net.script = "", ""
	if err := b.r.FillAliases(t.Context()); err != nil || b.net.script != "" {
		t.Fatalf("filled %q: %v", b.net.script, err)
	}
}
