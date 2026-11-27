package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/andaru-project/andaru/internal/addon"
	"github.com/andaru-project/andaru/internal/bus"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/generation"
)

const (
	addonBusRetryDelay    = 5 * time.Second
	addonBusSyncTimeout   = 30 * time.Second
	addonBusTokenTempGlob = "addon-bus-tokens-*.json"
	addonQuadletDir       = "/run/containers/systemd/"
)

// addonBusSync keeps dynamic add-on event bus clients and their /run/andaru-addons/<slug>/bus/token files
// synchronized with the applied (live) generation's granted topics. Tokens are 256-bit random values kept
// only in engine memory and /run tmpfs—never in saved configuration or backups.
type addonBusSync struct {
	root       string
	stateDir   string
	runtimeDir string
	catalog    *addon.Catalog
	events     *bus.Bus
	grantsAt   func(context.Context, config.Version) ([]config.AddonBusGrant, error)
	realize    func(ctx context.Context, tokensFile string) error
	retryDelay time.Duration
	onReady    func()

	mu          sync.Mutex
	cleanedTemp bool
	preparing   bool // true while an activate/rollback realize transition holds the prepared target
	bootReady   bool // true once at least one live-generation sync has succeeded
	synced      bool
	lastLive    int64
	registered  map[string]bus.Grants // "addon.<slug>" -> active grants

	wake chan struct{}
}

func newAddonBusSync(
	root, stateDir, runtimeDir string,
	catalog *addon.Catalog,
	events *bus.Bus,
	grantsAt func(context.Context, config.Version) ([]config.AddonBusGrant, error),
	realize func(ctx context.Context, tokensFile string) error,
) *addonBusSync {
	return &addonBusSync{
		root:       root,
		stateDir:   stateDir,
		runtimeDir: runtimeDir,
		catalog:    catalog,
		events:     events,
		grantsAt:   grantsAt,
		realize:    realize,
		retryDelay: addonBusRetryDelay,
		registered: map[string]bus.Grants{},
		wake:       make(chan struct{}, 1),
	}
}

func (s *addonBusSync) nudge() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *addonBusSync) isReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bootReady
}

func (s *addonBusSync) runWithInitial(ctx context.Context, initialRetry bool) {
	var retry *time.Timer
	var retryCh <-chan time.Time
	schedule := func(needRetry bool) {
		if retry != nil {
			retry.Stop()
			retry = nil
			retryCh = nil
		}
		if needRetry {
			delay := s.retryDelay
			if delay <= 0 {
				delay = addonBusRetryDelay
			}
			retry = time.NewTimer(delay)
			retryCh = retry.C
		}
	}
	defer func() {
		if retry != nil {
			retry.Stop()
		}
	}()
	schedule(initialRetry)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
			schedule(s.sync(ctx))
		case <-retryCh:
			schedule(s.sync(ctx))
		}
	}
}

// beginPrepare pre-syncs add-on bus tokens and grants (and immediately revokes dropped/changed grants)
// for a generation about to be activated or rolled back to, while suppressing background apply/+ syncs
// until the returned finish function runs after realize completes. On failure, it clears the prepare
// window, reconciles back to the current live generation so revoked clients are restored, and returns
// the error so activation can abort before starting add-ons without credentials.
func (s *addonBusSync) beginPrepare(ctx context.Context, id int) (func(context.Context), error) {
	var target *int
	if id > 0 {
		target = &id
	}
	s.mu.Lock()
	s.preparing = true
	err, _ := s.syncTargetLocked(ctx, target, false)
	if err != nil {
		s.preparing = false
		reconcileErr, notifyReady := s.syncLiveLocked(ctx)
		s.mu.Unlock()
		if notifyReady != nil {
			notifyReady()
		}
		if reconcileErr != nil {
			s.nudge()
		}
		return nil, err
	}
	s.mu.Unlock()
	return func(doneCtx context.Context) {
		s.mu.Lock()
		s.preparing = false
		syncErr, notifyReady := s.syncLiveLocked(doneCtx)
		s.mu.Unlock()
		if notifyReady != nil {
			notifyReady()
		}
		if syncErr != nil {
			s.nudge()
		}
	}, nil
}

func (s *addonBusSync) sync(ctx context.Context) (retry bool) {
	s.mu.Lock()
	if s.preparing {
		s.mu.Unlock()
		return false
	}
	err, notifyReady := s.syncLiveLocked(ctx)
	s.mu.Unlock()
	if notifyReady != nil {
		notifyReady()
	}
	return err != nil
}

// syncLiveLocked reads pointers.json and syncs the live generation. Caller holds s.mu.
func (s *addonBusSync) syncLiveLocked(ctx context.Context) (err error, notifyReady func()) {
	dir := generation.Dir(s.stateDir)
	p, err := generation.ReadPointers(dir)
	if err != nil {
		slog.Warn("addon bus: read generation pointers", "error", err)
		return err, nil
	}
	return s.syncTargetLocked(ctx, p.Live, true)
}

func (s *addonBusSync) syncTargetLocked(ctx context.Context, target *int, isLive bool) (err error, notifyReady func()) {
	if !s.cleanedTemp {
		s.cleanedTemp = true
		if s.runtimeDir != "" {
			if matches, err := filepath.Glob(filepath.Join(s.runtimeDir, addonBusTokenTempGlob)); err == nil {
				for _, m := range matches {
					_ = os.Remove(m)
				}
			}
		}
	}

	syncCtx, cancel := context.WithTimeout(ctx, addonBusSyncTimeout)
	defer cancel()

	dir := generation.Dir(s.stateDir)
	liveID := int64(0)
	if target != nil {
		liveID = int64(*target)
	}
	markReady := func() {
		if isLive && !s.bootReady {
			s.bootReady = true
			if s.onReady != nil {
				notifyReady = s.onReady
			}
		}
	}
	if s.synced && liveID == s.lastLive {
		markReady()
		return nil, notifyReady
	}

	desired, retryErr := s.desiredGrants(syncCtx, dir, target)
	if retryErr != nil {
		slog.Warn("addon bus: compute live grants", "error", retryErr)
		return retryErr, nil
	}

	// Mark unsynced BEFORE mutating bus clients so a transient failure followed by rollback to lastLive
	// never skips re-registering the rolled-back generation's clients.
	s.synced = false

	// Step 1: immediately revoke any removed or grant-changed clients so revoked grants have zero window.
	for user, oldGrants := range s.registered {
		slug := strings.TrimPrefix(user, "addon.")
		newGrants, ok := desired[slug]
		if !ok || !sameBusGrants(oldGrants, newGrants) {
			s.events.RemoveClient(user)
			delete(s.registered, user)
		}
	}

	// Fast path when no add-on uses the event bus and no stale token files exist on disk.
	if len(desired) == 0 && !hasStaleAddonBusTokens(s.root) {
		s.synced = true
		s.lastLive = liveID
		markReady()
		return nil, notifyReady
	}

	// Step 2: generate fresh 256-bit random tokens and write them via realize before updating bus clients
	// so reconnecting add-ons immediately read the new token from /run/andaru/bus/token.
	tokens := make(map[string]string, len(desired))
	for slug := range desired {
		tok, err := randomBusToken()
		if err != nil {
			slog.Error("addon bus: generate token", "slug", slug, "error", err)
			return err, nil
		}
		tokens[slug] = tok
	}
	if err := s.deliverTokens(syncCtx, tokens); err != nil {
		slog.Warn("addon bus: sync token files", "error", err)
		return err, nil
	}

	// Step 3: register each desired client on the bus (closing old connections for rotated tokens).
	// Validation errors from SetClient are permanent for this generation: log and drop only the offending
	// client instead of retrying every 5s and disconnecting healthy add-ons in a loop.
	for slug, grants := range desired {
		user := "addon." + slug
		if err := s.events.SetClient(user, tokens[slug], grants); err != nil {
			slog.Error("addon bus: register client refused", "user", user, "error", err)
			s.events.RemoveClient(user)
			delete(s.registered, user)
			continue
		}
		s.registered[user] = grants
	}
	s.synced = true
	s.lastLive = liveID
	markReady()
	return nil, notifyReady
}

func (s *addonBusSync) desiredGrants(ctx context.Context, dir string, target *int) (map[string]bus.Grants, error) {
	desired := map[string]bus.Grants{}
	if target == nil || s.catalog == nil {
		return desired, nil
	}
	g, err := generation.Read(dir, *target)
	if err != nil {
		return nil, err
	}
	realized := map[string]bool{}
	for _, f := range g.Files {
		rest, ok := strings.CutPrefix(f.Path, addonQuadletDir+"andaru-addon-")
		if !ok {
			continue
		}
		if slug, ok := strings.CutSuffix(rest, ".container"); ok && addon.ValidSlug(slug) {
			realized[slug] = true
		}
	}
	if len(realized) == 0 || g.SavedVersion == "" {
		return desired, nil
	}
	entries, err := s.grantsAt(ctx, config.Version(g.SavedVersion))
	if fault := (*config.Fault)(nil); errors.As(err, &fault) && fault.Code == "NOT_FOUND" {
		slog.Warn("addon bus: live generation saved version unavailable; granting no bus access",
			"generation", g.ID, "version", g.SavedVersion, "error", err)
		return desired, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.Enabled {
			continue
		}
		m, ok := s.catalog.ByID(entry.ID)
		if !ok || m.Bus == nil || !realized[m.Slug] {
			continue
		}
		var gr bus.Grants
		for _, spec := range m.Bus.Publish {
			if entry.Publish[spec.Topic] == "granted" {
				gr.Publish = append(gr.Publish, spec.Topic)
			}
		}
		for _, spec := range m.Bus.Subscribe {
			if entry.Subscribe[spec.Topic] == "granted" {
				gr.Subscribe = append(gr.Subscribe, spec.Topic)
			}
		}
		if len(gr.Publish) == 0 && len(gr.Subscribe) == 0 {
			continue
		}
		slices.Sort(gr.Publish)
		slices.Sort(gr.Subscribe)
		desired[m.Slug] = gr
	}
	return desired, nil
}

func (s *addonBusSync) deliverTokens(ctx context.Context, tokens map[string]string) error {
	payload, err := json.Marshal(tokens)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.runtimeDir, addonBusTokenTempGlob)
	if err != nil {
		return fmt.Errorf("create temp tokens file: %w", err)
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath)
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("chmod temp tokens file: %w", err)
	}
	if _, err := f.Write(payload); err != nil {
		_ = f.Close()
		return fmt.Errorf("write temp tokens file: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync temp tokens file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp tokens file: %w", err)
	}
	return s.realize(ctx, tmpPath)
}

func randomBusToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func sameBusGrants(a, b bus.Grants) bool {
	return slices.Equal(a.Publish, b.Publish) && slices.Equal(a.Subscribe, b.Subscribe)
}

func hasStaleAddonBusTokens(root string) bool {
	runBase := root + "/run/andaru-addons"
	entries, err := os.ReadDir(runBase)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	for _, e := range entries {
		if e.Name() == "bus-socket" || !addon.ValidSlug(e.Name()) {
			continue
		}
		// Note: if Podman :U chowned a non-root add-on's bus/ dir, engine (without CAP_DAC_OVERRIDE)
		// receives EACCES here and fails closed to true, triggering realize cleanup once disabled.
		if _, err := os.Lstat(filepath.Join(runBase, e.Name(), "bus", "token")); err == nil || !errors.Is(err, fs.ErrNotExist) {
			return true
		}
	}
	return false
}
