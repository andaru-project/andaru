package engine

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/andaru-project/andaru/internal/aliasfeed"
	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/bus"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/notify"
)

const (
	// aliasSource raises the notification of an alias that keeps failing, after aliasFailures failures in a row.
	aliasSource   = "alias"
	aliasFailures = 3
	aliasesPage   = "/firewall/aliases"
	// maxPending bounds the requests an alias keeps waiting for, while andaru-outbound does not answer.
	maxPending = 16
)

// aliasFeeds keeps the address aliases fed from a URL or from host names: it asks andaru-outbound for each one's
// list on its interval, keeps each result in the alias's state file, and loads a changed list into the filter.
type aliasFeeds struct {
	mu       sync.Mutex
	stateDir string
	aliases  func(context.Context) ([]config.DynamicAlias, error)
	publish  func(topic string, payload []byte, durable bool) error
	// realize runs andaru realize aliases.
	realize func(context.Context) error
	now     func() time.Time
	log     *slog.Logger
	poke    chan struct{}
	// owed says the sets may not hold what the state files do: a load failed, or none ran since this start, which
	// may follow a crash between a state write and its load.
	owed bool
}

func newAliasFeeds(stateDir string, events *bus.Bus, aliases func(context.Context) ([]config.DynamicAlias, error), realize func(context.Context) error) *aliasFeeds {
	return &aliasFeeds{stateDir: stateDir, aliases: aliases, publish: events.Publish, realize: realize, now: time.Now,
		log: slog.Default().With("component", "aliases"), poke: make(chan struct{}, 1), owed: true}
}

// nudge asks for a schedule soon: the saved aliases may have changed.
func (f *aliasFeeds) nudge() {
	select {
	case f.poke <- struct{}{}:
	default:
	}
}

// run schedules at start, at each nudge, and when the next alias is due.
func (f *aliasFeeds) run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-f.poke:
		case <-timer.C:
		}
		timer.Reset(f.schedule(ctx))
	}
}

// aliasHash is of what a fetch asks for: when it changes, the alias is fetched at once.
func aliasHash(a config.DynamicAlias) string {
	sum := sha256.Sum256([]byte(a.Kind + "\x00" + a.URL + "\x00" + strings.Join(a.Names, "\x00")))
	return hex.EncodeToString(sum[:])
}

// due reports whether an alias is to be fetched now, and if not, how long until it is.
func due(a config.DynamicAlias, s aliasfeed.State, now time.Time) (bool, time.Duration) {
	if s.LastAttempt.IsZero() || s.Hash != aliasHash(a) {
		return true, 0
	}
	wait := s.LastAttempt.Add(time.Duration(a.RefreshSeconds) * time.Second).Sub(now)
	return wait <= 0, wait
}

// schedule asks for every alias that is due, removes the state of aliases no longer saved, and returns how long
// until the next is due.
func (f *aliasFeeds) schedule(ctx context.Context) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	list, err := f.aliases(ctx)
	if err != nil {
		f.log.Warn("read the saved aliases", "error", err)
		return time.Minute
	}
	f.forget(list)
	next := time.Hour
	for _, a := range list {
		s, err := f.load(a.ID)
		if err != nil {
			f.log.Warn("read an alias's state; it is fetched again", "alias", a.ID, "error", err)
		}
		now := f.now()
		fetch, wait := due(a, s, now)
		if fetch {
			if _, err := f.request(a, s); err != nil {
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

// request publishes a durable request for a's list and records it in its state.
func (f *aliasFeeds) request(a config.DynamicAlias, s aliasfeed.State) (string, error) {
	if h := aliasHash(a); s.Hash != h {
		// Answers to what was asked before are not this alias's list any more.
		s.Hash, s.Pending = h, nil
	}
	req := aliasfeed.Request{ID: uuid.NewString(), Alias: a.ID, Kind: a.Kind, URL: a.URL, Names: a.Names}
	payload, err := bus.NewEvent(aliasfeed.RequestType, "engine", req)
	if err == nil {
		err = f.publish(aliasfeed.Topic(a.ID), payload, true)
	}
	if err != nil {
		return "", err
	}
	s.LastAttempt = f.now().UTC()
	s.Pending = append(s.Pending, req.ID)
	if len(s.Pending) > maxPending {
		s.Pending = s.Pending[len(s.Pending)-maxPending:]
	}
	return req.ID, f.save(a.ID, s)
}

// refresh asks for an alias's list now; ok is false when id is not an alias fed from a URL or from host names.
func (f *aliasFeeds) refresh(ctx context.Context, id string) (request string, ok bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok, err := f.find(ctx, id)
	if err != nil || !ok {
		return "", ok, err
	}
	s, err := f.load(id)
	if err != nil {
		f.log.Warn("read an alias's state; it is fetched again", "alias", id, "error", err)
	}
	request, err = f.request(a, s)
	return request, true, err
}

func (f *aliasFeeds) find(ctx context.Context, id string) (config.DynamicAlias, bool, error) {
	list, err := f.aliases(ctx)
	if err != nil {
		return config.DynamicAlias{}, false, err
	}
	i := slices.IndexFunc(list, func(a config.DynamicAlias) bool { return a.ID == id })
	if i < 0 {
		return config.DynamicAlias{}, false, nil
	}
	return list[i], true, nil
}

// handle takes one result from andaru-outbound, then loads the lists into the filter if they may have changed. The
// bus calls it from one goroutine, which alone touches owed; the load runs without the lock, so the API is not held up.
func (f *aliasFeeds) handle(m bus.Message) error {
	if err := f.record(m); err != nil {
		return err
	}
	if !f.owed {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Second) // longer than realize's RuntimeMaxSec
	defer cancel()
	if err := f.realize(ctx); err != nil {
		// The next result tries again, and every load of the filter fills the sets.
		f.log.Error("alias lists not loaded into the filter", "error", err)
		return nil
	}
	f.owed = false
	return nil
}

// record keeps one result in its alias's state. A result for an alias no longer saved, or for a request it no longer
// waits for, is dropped; one that cannot be decoded too, since trying it again would not change it.
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
	a, ok, err := f.find(ctx, id)
	if err != nil {
		return err // the store comes back; the result is tried again
	}
	if !ok {
		return nil
	}
	s, err := f.load(id)
	if err != nil {
		f.log.Warn("read an alias's state", "alias", id, "error", err)
	}
	i := slices.Index(s.Pending, r.Request)
	if i < 0 || s.Hash != aliasHash(a) {
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
	if err := f.save(id, s); err != nil {
		return err // unsaved, the result is tried again
	}
	f.tell(a, s, failures)
	if changed {
		f.owed = true
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
	// Every name's answer is checked before any is kept.
	names := make([]aliasfeed.Name, len(a.Names))
	var failed []string
	var all []string
	for i, name := range a.Names {
		names[i] = aliasfeed.Name{Name: name, Addresses: []string{}}
		if j := slices.IndexFunc(s.Names, func(n aliasfeed.Name) bool { return n.Name == name }); j >= 0 {
			names[i] = s.Names[j]
		}
		got, ok := r.Names[name]
		switch {
		case !ok:
			got.Error = "no answer"
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
	if len(failed) > 0 {
		return strings.Join(failed, "; ")
	}
	if !r.OK {
		return cmp.Or(r.Error, "lookup failed")
	}
	s.LastSuccess = at
	return ""
}

// tell raises the alias's notification while it keeps failing, and clears it at its first success.
func (f *aliasFeeds) tell(a config.DynamicAlias, s aliasfeed.State, failuresBefore int) {
	r := notify.Raise{Key: "alias/" + a.ID}
	switch {
	case s.Failures >= aliasFailures:
		r.Severity, r.Link, r.Requires = notify.Warning, aliasesPage, string(authz.ConfigurationRead)
		r.Subject = notify.Subject(fmt.Sprintf("The alias %s failed to refresh %d times in a row", a.Name, s.Failures))
		r.Detail = notify.Detail(s.LastError + "\nThe last good list stays in use.")
	case s.Failures == 0 && failuresBefore >= aliasFailures:
	default:
		return
	}
	topic, payload, err := notify.Event(aliasSource, r)
	if err == nil {
		err = f.publish(topic, payload, true)
	}
	if err != nil {
		f.log.Warn("alias notification not published", "alias", a.ID, "error", err)
	}
}

// forget removes the state of aliases no longer saved as fed from a URL or from host names, and clears their
// notification.
func (f *aliasFeeds) forget(list []config.DynamicAlias) {
	entries, err := os.ReadDir(aliasfeed.Dir(f.stateDir))
	if err != nil {
		return
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || slices.ContainsFunc(list, func(a config.DynamicAlias) bool { return a.ID == id }) {
			continue
		}
		s, _ := f.load(id)
		if err := os.Remove(aliasfeed.Path(f.stateDir, id)); err != nil {
			f.log.Warn("remove a removed alias's state", "alias", id, "error", err)
			continue
		}
		f.tell(config.DynamicAlias{ID: id}, aliasfeed.State{}, s.Failures)
	}
}

func (f *aliasFeeds) load(id string) (aliasfeed.State, error) {
	var s aliasfeed.State
	data, err := os.ReadFile(aliasfeed.Path(f.stateDir, id))
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &s)
	}
	if err != nil {
		return aliasfeed.State{}, err
	}
	return s, nil
}

func (f *aliasFeeds) save(id string, s aliasfeed.State) error {
	if s.Members == nil {
		s.Members = []string{}
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(aliasfeed.Dir(f.stateDir), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(aliasfeed.Path(f.stateDir, id), append(data, '\n'), 0o600)
}
