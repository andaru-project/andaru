package engine

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/andaru-project/andaru/internal/aliasfeed"
	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/bus"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
	"github.com/andaru-project/andaru/internal/notify"
)

const (
	// aliasSource raises the notification of an alias that keeps failing, after aliasFailures failures in a row.
	aliasSource   = "alias"
	aliasFailures = 3
	aliasesPage   = "/firewall/aliases"
	// maxPending bounds the requests a source keeps waiting for, while andaru-outbound does not answer.
	maxPending = 16
	// maxRefreshSeconds is the longest interval an alias may have.
	maxRefreshSeconds = 86400
	// keepUnwanted is how long the state of a source no wanted generation names is kept after its last attempt: a
	// rollback, or a boot of a kept generation, finds its list for as long.
	keepUnwanted = 30 * 24 * time.Hour
	// refreshEvery is how often "Refresh now" may ask for one alias.
	refreshEvery = 10 * time.Second
)

// source is one alias's URL or set of names, by its hash: the members belong to it, in its own state file.
type source struct{ alias, hash string }

func sourceOf(a config.DynamicAlias) source {
	return source{a.ID, aliasfeed.Hash(a.Kind, a.URL, a.Names)}
}

// wanted is a source the saved head or the live generation names, with the alias as one of them saves it.
type wanted struct {
	alias config.DynamicAlias
	// saved and live say which of the saved head and the live generation name it: the live filter holds its sets.
	saved, live bool
}

// aliasFeeds keeps the address aliases fed from a URL or from host names: it asks andaru-outbound for the list of
// every source the saved head or the live generation names, on its interval, keeps each result in the source's state
// file, and loads a changed list into the filter. A saved change never touches the live filter: its sets are filled
// from the sources the live filter was rendered for, and an apply finds its new sources already fetched.
type aliasFeeds struct {
	mu       sync.Mutex
	stateDir string
	// life is the engine's: a load stops with it.
	life context.Context
	// aliases returns those of the saved head, and those of the live generation's saved version.
	aliases func(context.Context) (saved, live []config.DynamicAlias, err error)
	publish func(topic string, payload []byte, durable bool) error
	// realize runs andaru realize aliases.
	realize func(context.Context) error
	now     func() time.Time
	log     *slog.Logger
	poke    chan struct{}
	// applied asks the run loop for a load: a generation went live.
	applied chan struct{}
	// owed says the sets may not hold what the state files do: a list changed, a load failed, a generation went live,
	// or none ran since this start, which may follow a crash between a state write and its load. filling serializes
	// the loads.
	owed    atomic.Bool
	filling sync.Mutex
	// lastLive is the live generation the last apply event found: 0 before the first since this start or when the
	// pointers did not read (so every event loads), -1 for none.
	lastLive atomic.Int64
	// keptWarned is when forget last said the live filter's sources did not read.
	keptWarned time.Time
	// refreshed is each alias's last "Refresh now", and the request it published; cleared, the aliases whose
	// notification was cleared since this start and not raised since.
	refreshed map[string]refresh
	cleared   map[string]bool
}

type refresh struct {
	at      time.Time
	request string
}

func newAliasFeeds(life context.Context, stateDir string, events *bus.Bus, aliases func(context.Context) ([]config.DynamicAlias, []config.DynamicAlias, error), realize func(context.Context) error) *aliasFeeds {
	f := &aliasFeeds{life: life, stateDir: stateDir, aliases: aliases, publish: events.Publish, realize: realize, now: time.Now,
		log: slog.Default().With("component", "aliases"), poke: make(chan struct{}, 1), applied: make(chan struct{}, 1),
		refreshed: map[string]refresh{}, cleared: map[string]bool{}}
	f.owed.Store(true)
	return f
}

// savedAndLive returns the dynamic aliases of the saved head and of the generation that is live, by the saved version
// it was applied from: none before the first apply. A saved version that does not read (gone after an update's
// rollback, or a restore) gives none, so every schedule and result does not fail on it: the sources the live filter
// names are still kept, by forget, but cannot be fetched without their URL or names.
func savedAndLive(stateDir string, store *config.Store) func(context.Context) ([]config.DynamicAlias, []config.DynamicAlias, error) {
	return func(ctx context.Context) ([]config.DynamicAlias, []config.DynamicAlias, error) {
		saved, err := store.DynamicAliases(ctx)
		if err != nil {
			return nil, nil, err
		}
		dir := generation.Dir(stateDir)
		p, err := generation.ReadPointers(dir)
		if err != nil || p.Live == nil {
			return saved, nil, err
		}
		g, err := generation.Read(dir, *p.Live)
		if err != nil || g.SavedVersion == "" {
			return saved, nil, err
		}
		live, err := store.DynamicAliasesAt(ctx, config.Version(g.SavedVersion))
		if fault := (*config.Fault)(nil); errors.As(err, &fault) && fault.Code == "NOT_FOUND" {
			slog.Warn("the live generation's saved version does not read; its alias lists are kept but not fetched",
				"component", "aliases", "generation", g.ID, "version", g.SavedVersion, "error", err)
			return saved, nil, nil
		}
		return saved, live, err
	}
}

// nudge asks for a schedule soon: the saved aliases or the live generation may have changed.
func (f *aliasFeeds) nudge() {
	select {
	case f.poke <- struct{}{}:
	default:
	}
}

// apply follows an apply event: the live generation may have changed, and with it the sources its filter's sets are
// filled from. A result recorded while it activated, after its filter was filled and before it went live, was saved
// for a source the live generation did not name yet: the load fills the sets again from the state files. Every status
// change is an event, so only one that finds another live generation than the last, or the first since this start,
// loads; one whose pointers do not read loads too.
func (f *aliasFeeds) apply() {
	live := int64(-1)
	if p, err := generation.ReadPointers(generation.Dir(f.stateDir)); err != nil {
		live = 0
	} else if p.Live != nil {
		live = int64(*p.Live)
	}
	// A load still owed, as one a failed activation's lock refused, is tried again whatever went live.
	if f.lastLive.Swap(live) != live || live == 0 || f.owed.Load() {
		f.owed.Store(true)
		select {
		case f.applied <- struct{}{}:
		default:
		}
	}
	f.nudge()
}

// run schedules at start, at each nudge, and when the next source is due, and loads the lists after each apply, off
// the loop so a long load does not hold up the schedule; it returns once the loads it started are done.
func (f *aliasFeeds) run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	var loads sync.WaitGroup
	defer loads.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case <-f.applied:
			loads.Go(f.fill)
			continue
		case <-f.poke:
		case <-timer.C:
		}
		timer.Reset(f.schedule(ctx))
	}
}

// want returns every source the saved head or the live generation names. A source both name is asked for on the
// shorter interval.
func (f *aliasFeeds) want(ctx context.Context) (map[source]wanted, error) {
	saved, live, err := f.aliases(ctx)
	if err != nil {
		return nil, err
	}
	out := map[source]wanted{}
	for i, list := range [][]config.DynamicAlias{saved, live} {
		for _, a := range list {
			w, ok := out[sourceOf(a)]
			if !ok || a.RefreshSeconds < w.alias.RefreshSeconds {
				w.alias = a
			}
			w.saved, w.live = w.saved || i == 0, w.live || i == 1
			out[sourceOf(a)] = w
		}
	}
	// What the live filter fills its sets from is live, even when its saved version no longer reads.
	if kept, err := generation.LiveFeeds(f.stateDir); err == nil {
		for src, w := range out {
			if slices.Contains(kept[src.alias], src.hash) {
				w.live = true
				out[src] = w
			}
		}
	}
	return out, nil
}

// due reports whether a source is to be asked for now, and if not, how long until it is. A request not yet answered
// counts as an attempt: no other is sent for one interval, so requests do not pile up while nobody answers.
func due(a config.DynamicAlias, s aliasfeed.State, now time.Time) (bool, time.Duration) {
	if s.LastAttempt.IsZero() {
		return true, 0
	}
	wait := s.LastAttempt.Add(time.Duration(a.RefreshSeconds) * time.Second).Sub(now)
	return wait <= 0, wait
}

// schedule asks for every wanted source that is due, removes the state of sources long unwanted, and returns how
// long until the next is due.
func (f *aliasFeeds) schedule(ctx context.Context) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	want, err := f.want(ctx)
	if err != nil {
		f.log.Warn("read the saved and the live aliases", "error", err)
		return time.Minute
	}
	f.forget(want)
	next := time.Hour
	// In a stable order, so the requests are too.
	keys := slices.SortedFunc(maps.Keys(want), func(a, b source) int { return cmp.Or(cmp.Compare(a.alias, b.alias), cmp.Compare(a.hash, b.hash)) })
	for _, src := range keys {
		a := want[src].alias
		s := f.load(src)
		fetch, wait := due(a, s, f.now())
		if fetch {
			if _, err := f.request(src, a, s); err != nil {
				f.log.Warn("ask for an alias's list", "alias", a.ID, "name", a.Name, "error", err)
				wait = time.Minute
			} else {
				wait = time.Duration(a.RefreshSeconds) * time.Second
			}
		}
		next = min(next, wait)
	}
	return next
}

// request records a request for a source's list in its state, then publishes it, durable. A request that cannot be
// recorded is not sent: its answer would be dropped.
func (f *aliasFeeds) request(src source, a config.DynamicAlias, s aliasfeed.State) (string, error) {
	req := aliasfeed.Request{ID: uuid.NewString(), Alias: a.ID, Hash: src.hash, Kind: a.Kind, URL: a.URL, Names: a.Names}
	payload, err := bus.NewEvent(aliasfeed.RequestType, "engine", req)
	if err != nil {
		return "", err
	}
	asked := s
	asked.LastAttempt = f.now().UTC()
	asked.Pending = append(slices.Clone(s.Pending), req.ID)
	if len(asked.Pending) > maxPending {
		asked.Pending = asked.Pending[len(asked.Pending)-maxPending:]
	}
	if err := f.save(src, asked); err != nil {
		return "", err
	}
	if err := f.publish(aliasfeed.Topic(a.ID), payload, true); err != nil {
		// Unsent, it is no attempt: the source is asked for again at the next schedule.
		if undo := f.save(src, s); undo != nil {
			f.log.Warn("undo an unsent alias request", "alias", a.ID, "error", undo)
		}
		return "", err
	}
	return req.ID, nil
}

// refresh asks for an alias's list now, for its saved source and, when it differs, its live one; ok is false when id
// is not an alias of the saved head fed from a URL or from host names. One alias is asked for at most once in
// refreshEvery: a refresh sooner returns the request the last one published.
func (f *aliasFeeds) refresh(ctx context.Context, id string) (request string, ok bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	want, err := f.want(ctx)
	if err != nil {
		return "", false, err
	}
	var saved, live []source
	for src, w := range want {
		switch {
		case src.alias != id:
		case w.saved:
			saved = append(saved, src)
		default:
			live = append(live, src)
		}
	}
	if len(saved) == 0 {
		return "", false, nil
	}
	if last, ok := f.refreshed[id]; ok && f.now().Sub(last.at) < refreshEvery {
		return last.request, true, nil
	}
	for i, src := range append(saved, live...) {
		r, err := f.request(src, want[src].alias, f.load(src))
		switch {
		case err != nil && i == 0:
			return "", true, err
		case err != nil:
			// The saved source's request is out: the refresh stands, and the live one is asked for on its interval.
			f.log.Warn("ask for an alias's live list", "alias", id, "error", err)
		case i == 0:
			request = r
		}
	}
	f.refreshed[id] = refresh{f.now(), request}
	return request, true, nil
}

// handle takes one result from andaru-outbound, then loads the lists into the filter if they may have changed.
func (f *aliasFeeds) handle(m bus.Message) error {
	if err := f.record(m); err != nil {
		return err
	}
	f.fill()
	return nil
}

// fill loads the lists into the filter when owed. The load runs without the lock, so the API is not held up; it is
// idempotent, filling the sets the live filter names from the state files.
func (f *aliasFeeds) fill() {
	f.filling.Lock()
	defer f.filling.Unlock()
	if !f.owed.Swap(false) {
		return
	}
	ctx, cancel := context.WithTimeout(f.life, 200*time.Second) // longer than realize's RuntimeMaxSec
	defer cancel()
	if err := f.realize(ctx); err != nil {
		// The next result or apply tries again, and every load of the filter fills the sets.
		f.owed.Store(true)
		if f.life.Err() == nil { // stopping, the engine owes the load to its next start
			f.log.Error("alias lists not loaded into the filter", "error", err)
		}
	}
}

// record keeps one result in its source's state. A result for a source no wanted generation names, or for a request
// its source no longer waits for (answered already, or pushed out), is dropped; one that cannot be decoded too, since
// trying it again would not change it, and one whose source cannot be told. Only a state that cannot be saved is
// tried again.
func (f *aliasFeeds) record(m bus.Message) error {
	if m.Gap {
		f.log.Warn("alias results may have been missed; the aliases are fetched again on their interval")
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id := strings.TrimPrefix(m.Topic, "alias-result/")
	var r aliasfeed.Result
	if e := (bus.Event{Data: &r}); json.Unmarshal(m.Payload, &e) != nil || e.Type != aliasfeed.ResultType {
		f.log.Warn("alias result dropped: not a result", "alias", id)
		return nil
	}
	want, err := f.want(ctx)
	if err != nil {
		// Dropped, not tried again, so it holds up no later result: the source is due again, and the next schedule
		// asks for it rather than waiting a full interval.
		f.log.Warn("alias result dropped: the saved and the live aliases do not read", "alias", id, "error", err)
		f.retry(source{id, r.Hash}, r.Request)
		return nil
	}
	src := source{id, r.Hash}
	w, ok := want[src]
	if !ok {
		return nil
	}
	a := w.alias
	s := f.load(src)
	i := slices.Index(s.Pending, r.Request)
	if i < 0 {
		return nil
	}
	s.Pending = s.Pending[i+1:]
	before, failures := s.Members, s.Failures
	reason := apply(a, &s, r, f.now().UTC())
	changed := !slices.Equal(before, s.Members)
	if reason == "" {
		s.LastError, s.Failures = "", 0
		f.log.Info("alias list fetched", "alias", id, "name", a.Name, "entries", len(s.Members), "changed", changed)
	} else {
		s.LastError, s.Failures = reason, s.Failures+1
		f.log.Warn("alias list not fetched; the last good list stays", "alias", id, "name", a.Name, "reason", reason, "failures", s.Failures)
	}
	if err := f.save(src, s); err != nil {
		return err // unsaved, the result is tried again
	}
	// The notification is the alias's, raised while any wanted source of it keeps failing.
	if s.Failures >= aliasFailures {
		f.tell(a, s)
	} else if reason == "" {
		if inUse, stale := f.failing(id, want); !inUse && (failures >= aliasFailures || stale && !f.cleared[id]) {
			f.tell(a, aliasfeed.State{})
		}
	}
	// Any change loads: a source only the saved head names may be one the live filter was rendered for, by an apply
	// that went live after want was read.
	if changed {
		f.owed.Store(true)
	}
	if changed && w.live {
		payload, err := bus.NewEvent("alias.members.v1", "engine", map[string]any{"id": id, "members": s.Members})
		if err == nil {
			err = f.publish("alias/"+id, payload, false)
		}
		if err != nil {
			f.log.Warn("publish an alias's members", "alias", id, "error", err)
		}
	}
	return nil
}

// retry clears the last attempt of a source with a state file that waits for request, so the source is due now.
func (f *aliasFeeds) retry(src source, request string) {
	if !slices.Contains(aliasfeed.Sources(f.stateDir)[src.alias], src.hash) {
		return
	}
	s := f.load(src)
	if !slices.Contains(s.Pending, request) {
		return
	}
	// Due at once, yet recent: a zero time would read as 30 days unwanted and let forget remove a kept list.
	s.LastAttempt = f.now().UTC().Add(-time.Duration(maxRefreshSeconds) * time.Second)
	if err := f.save(src, s); err != nil {
		f.log.Warn("ask again for an alias's dropped result", "alias", src.alias, "error", err)
		return
	}
	f.nudge()
}

// apply records r in s and returns why it failed, or "" for a success. A failure never replaces the last good list.
func apply(a config.DynamicAlias, s *aliasfeed.State, r aliasfeed.Result, now time.Time) string {
	at := r.FetchedAt.UTC()
	if at.IsZero() || at.After(now) {
		at = now
	}
	if a.Kind == "url" {
		if !r.OK {
			return cmp.Or(r.Error, "fetch failed")
		}
		members, err := aliasfeed.Members(r.Members)
		if err != nil {
			return "result not valid: " + err.Error()
		}
		s.Members, s.LastSuccess = members, at
		return ""
	}
	if !r.OK && len(r.Names) == 0 {
		return cmp.Or(r.Error, "lookup failed")
	}
	// Every name's answer is checked before any is kept. The alias fails only when every name does: a name that
	// failed keeps its last good addresses, and its error is shown.
	names := make([]aliasfeed.Name, len(a.Names))
	var failed []string
	var all []string
	for i, name := range a.Names {
		// Names match whatever their case, as the source's hash does; the alias's spelling is kept.
		names[i] = aliasfeed.Name{Addresses: []string{}}
		if j := slices.IndexFunc(s.Names, func(n aliasfeed.Name) bool { return strings.EqualFold(n.Name, name) }); j >= 0 {
			names[i] = s.Names[j]
		}
		names[i].Name = name
		got, ok := nameResult(r.Names, name)
		switch {
		case !ok:
			got.Error = "no answer"
		case got.Error == "" && len(got.Addresses) == 0:
			got.Error = "no addresses"
		case got.Error == "":
			addresses, err := aliasfeed.Members(got.Addresses)
			if err == nil && slices.ContainsFunc(addresses, func(m string) bool { return strings.Contains(m, "/") }) {
				err = errors.New("a network is not an address")
			}
			if err != nil {
				return "result not valid: " + name + ": " + err.Error()
			}
			names[i].Addresses, names[i].Error, names[i].ResolvedAt = addresses, "", at
		}
		if got.Error != "" {
			names[i].Error = got.Error
			failed = append(failed, name+": "+got.Error)
		}
		all = append(all, names[i].Addresses...)
	}
	members, err := aliasfeed.Members(all)
	if err != nil {
		return "result not valid: " + err.Error()
	}
	s.Names, s.Members = names, members
	if len(failed) == len(a.Names) {
		return strings.Join(failed, "; ")
	}
	s.LastSuccess = at
	return ""
}

// nameResult returns a result's outcome for a name, whatever its case.
func nameResult(results map[string]aliasfeed.NameResult, name string) (aliasfeed.NameResult, bool) {
	if got, ok := results[name]; ok {
		return got, true
	}
	for n, got := range results {
		if strings.EqualFold(n, name) {
			return got, true
		}
	}
	return aliasfeed.NameResult{}, false
}

// tell raises the alias's notification for a source that keeps failing, or clears it for an empty state.
func (f *aliasFeeds) tell(a config.DynamicAlias, s aliasfeed.State) {
	r := notify.Raise{Key: "alias/" + a.ID}
	if s.Failures >= aliasFailures {
		r.Severity, r.Link, r.Requires = notify.Warning, aliasesPage, string(authz.ConfigurationRead)
		r.Subject = notify.Subject(fmt.Sprintf("The alias %s failed to refresh %d times in a row", a.Name, s.Failures))
		r.Detail = notify.Detail(s.LastError + "\nThe last good list stays in use.")
	}
	topic, payload, err := notify.Event(aliasSource, r)
	if err == nil {
		err = f.publish(topic, payload, true)
	}
	if err != nil {
		f.log.Warn("alias notification not published", "alias", a.ID, "error", err)
		return
	}
	if r.Severity == "" {
		f.cleared[a.ID] = true
	} else {
		delete(f.cleared, a.ID)
	}
}

// failing reports whether a source of alias id that keeps failing is wanted (inUse), and whether one is not (stale).
func (f *aliasFeeds) failing(id string, want map[source]wanted) (inUse, stale bool) {
	for _, hash := range aliasfeed.Sources(f.stateDir)[id] {
		if src := (source{id, hash}); f.load(src).Failures >= aliasFailures {
			_, ok := want[src]
			inUse, stale = inUse || ok, stale || !ok
		}
	}
	return inUse, stale
}

// forget removes the state of each source no wanted generation and not the live filter names whose last attempt is
// older than keepUnwanted, and each state moved aside longer ago, and clears the notification of each alias whose failing sources are no
// longer wanted, once since this start or its last raise.
func (f *aliasFeeds) forget(want map[source]wanted) {
	sources := aliasfeed.Sources(f.stateDir)
	// The live filter's sets are filled from these, whatever saved version it was applied from.
	kept, keptErr := generation.LiveFeeds(f.stateDir)
	if keptErr != nil && f.now().Sub(f.keptWarned) >= time.Hour { // at most hourly: schedules run every minute or so
		f.keptWarned = f.now()
		f.log.Warn("read the live filter's alias sources; no source's state is removed", "error", keptErr)
	}
	dirs, _ := os.ReadDir(aliasfeed.Dir(f.stateDir))
	for _, d := range dirs {
		id := d.Name()
		if !d.IsDir() {
			continue
		}
		inUse, stale := f.failing(id, want)
		for _, hash := range sources[id] {
			src := source{id, hash}
			if _, ok := want[src]; ok || keptErr != nil || slices.Contains(kept[id], hash) {
				continue
			}
			if s := f.load(src); f.now().Sub(s.LastAttempt) < keepUnwanted {
				continue
			}
			if err := os.Remove(aliasfeed.Path(f.stateDir, id, hash)); err != nil {
				f.log.Warn("remove an unwanted alias source's state", "alias", id, "error", err)
			}
		}
		f.forgetCorrupt(id)
		// Empty, the alias's directory goes too; with any file left, it stays.
		_ = os.Remove(filepath.Join(aliasfeed.Dir(f.stateDir), id))
		if !inUse && stale && !f.cleared[id] {
			f.tell(config.DynamicAlias{ID: id}, aliasfeed.State{})
		}
	}
}

// forgetCorrupt removes the states of an alias that were moved aside more than keepUnwanted ago.
func (f *aliasFeeds) forgetCorrupt(id string) {
	dir := filepath.Join(aliasfeed.Dir(f.stateDir), id)
	files, _ := os.ReadDir(dir)
	for _, file := range files {
		_, at, ok := strings.Cut(file.Name(), aliasfeed.Corrupt)
		sec, err := strconv.ParseInt(at, 10, 64)
		if !ok || err != nil || f.now().Sub(time.Unix(sec, 0)) < keepUnwanted {
			continue
		}
		if err := os.Remove(filepath.Join(dir, file.Name())); err != nil {
			f.log.Warn("remove an alias source's state moved aside", "alias", id, "error", err)
		}
	}
}

// load reads a source's state: one that does not read is logged and starts empty.
func (f *aliasFeeds) load(src source) aliasfeed.State {
	s, err := aliasfeed.Load(f.stateDir, src.alias, src.hash)
	if err != nil {
		f.log.Warn("read an alias source's state; it starts empty", "alias", src.alias, "error", err)
	}
	return s
}

func (f *aliasFeeds) save(src source, s aliasfeed.State) error {
	if s.Members == nil {
		s.Members = []string{}
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	path := aliasfeed.Path(f.stateDir, src.alias, src.hash)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'), 0o600)
}
