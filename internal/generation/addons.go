package generation

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/andaru-project/andaru/internal/addon"
)

const (
	addonQuadletPrefix     = "/run/containers/systemd/andaru-addon-"
	addonRuntimeDir        = "/run/andaru-addons/"
	addonBusSocketHostDir  = "/run/andaru-addons/bus-socket"
	addonStoragePrefix     = "/var/lib/andaru/addons/"
	defaultAddonVerifyWait = 15 * time.Second
)

var (
	// ErrSafeModeActive is returned when manual add-on lifecycle control is attempted during safe mode or settings hold.
	ErrSafeModeActive = errors.New("SAFE_MODE_ACTIVE: add-ons are held off by safe mode")
	// ErrAddonNotApplied is returned when manual lifecycle control names an add-on not present in the live generation.
	ErrAddonNotApplied = errors.New("add-on is not enabled in the applied generation")

	addonStorageNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

	// addonHealthClient is strictly restricted to verified 127.0.0.1 loopback URLs in probeHealthURL.
	// InsecureSkipVerify applies only to internal loopback HTTPS health endpoints (often self-signed).
	addonHealthClient = &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // loopback-only verified in probeHealthURL
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
)

func addonUnitName(slug string) string { return "andaru-addon-" + slug + ".service" }

func addonContainerName(slug string) string { return "systemd-andaru-addon-" + slug }

func addonStampPath(slug string) string { return addonRuntimeDir + slug + "/started" }

func addonSlugFromPath(path string) (string, bool) {
	if m := addonContainer.FindStringSubmatch(path); len(m) == 2 {
		return m[1], true
	}
	if m := addonEnv.FindStringSubmatch(path); len(m) == 3 && m[1] == m[2] {
		return m[1], true
	}
	if m := addonSecret.FindStringSubmatch(path); len(m) == 3 && m[1] == m[2] {
		return m[1], true
	}
	return "", false
}

func addonSlugs(set []placed) []string {
	seen := map[string]bool{}
	for _, f := range set {
		if m := addonContainer.FindStringSubmatch(f.path); len(m) == 2 {
			seen[m[1]] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	return slices.Sorted(maps.Keys(seen))
}

func addonSum(slug string, set []placed) string {
	var parts []placed
	for _, f := range set {
		if s, ok := addonSlugFromPath(f.path); ok && s == slug {
			parts = append(parts, f)
		}
	}
	slices.SortFunc(parts, func(a, b placed) int { return strings.Compare(a.path, b.path) })
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p.path))
		h.Write([]byte{0})
		h.Write(p.data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (r Realizer) addonStoragePrefix() string {
	if r.StateDir != "" && r.Root == "" {
		return filepath.Join(r.StateDir, "addons") + "/"
	}
	return addonStoragePrefix
}

func (r Realizer) addonStorageRoot() string {
	if r.Root != "" {
		return filepath.Join(r.Root, "var", "lib", "andaru", "addons")
	}
	if r.StateDir != "" {
		return filepath.Join(r.StateDir, "addons")
	}
	return "/var/lib/andaru/addons"
}

// ensureAddonDirs creates host storage and bus-mount directories referenced by Volume= lines in .container files.
func (r Realizer) ensureAddonDirs(set []placed) error {
	base := r.addonStorageRoot()
	volPrefix := r.addonStoragePrefix()
	for _, f := range set {
		m := addonContainer.FindStringSubmatch(f.path)
		if len(m) != 2 {
			continue
		}
		slug := m[1]
		storagePrefix := "Volume=" + volPrefix + slug + "/"
		busTokenPrefix := "Volume=" + addonRuntimeDir + slug + "/bus:"
		busSocketPrefix := "Volume=" + addonBusSocketHostDir + ":"
		for line := range strings.SplitSeq(string(f.data), "\n") {
			trimmed := strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(trimmed, storagePrefix):
				rest := strings.TrimPrefix(trimmed, storagePrefix)
				name, _, hasColon := strings.Cut(rest, ":")
				if !hasColon || !addonStorageNamePattern.MatchString(name) {
					continue
				}
				if err := os.MkdirAll(filepath.Join(base, slug, name), 0o700); err != nil {
					return fmt.Errorf("create add-on storage directory %s/%s: %w", slug, name, err)
				}
			case strings.HasPrefix(trimmed, busTokenPrefix):
				if err := os.MkdirAll(r.Root+strings.TrimSuffix(addonRuntimeDir, "/"), 0o700); err != nil {
					return err
				}
				if err := os.MkdirAll(r.Root+addonRuntimeDir+slug+"/bus", 0o700); err != nil {
					return fmt.Errorf("create add-on bus token directory %s: %w", slug, err)
				}
			case strings.HasPrefix(trimmed, busSocketPrefix):
				if err := os.MkdirAll(r.Root+strings.TrimSuffix(addonRuntimeDir, "/"), 0o700); err != nil {
					return err
				}
				if err := os.MkdirAll(r.Root+addonBusSocketHostDir, 0o755); err != nil {
					return fmt.Errorf("create add-on bus socket directory: %w", err)
				}
			}
		}
	}
	return nil
}

func (r Realizer) stopAddon(ctx context.Context, slug string) error {
	unit := addonUnitName(slug)
	if err := r.stopUnit(ctx, unit); err != nil {
		return fmt.Errorf("stop add-on %s: %w", unit, err)
	}
	return nil
}

// addons stops removed/disabled add-on units, reloads systemd so podman-system-generator updates
// generated units, and—unless safe mode or settings hold is active—starts or restarts changed add-ons concurrently.
func (r Realizer) addons(ctx context.Context, from, to []placed) error {
	had, has := addonSlugs(from), addonSlugs(to)
	if len(had) == 0 && len(has) == 0 {
		return nil
	}

	// Stop removed/disabled add-ons concurrently before daemon-reload drops their generated units.
	var removed []string
	for _, slug := range had {
		if !slices.Contains(has, slug) {
			removed = append(removed, slug)
		}
	}
	if len(removed) > 0 {
		errs := make([]error, len(removed))
		var wg sync.WaitGroup
		for i, slug := range removed {
			wg.Go(func() {
				if err := r.stopAddon(ctx, slug); err != nil {
					errs[i] = err
					return
				}
				_ = os.RemoveAll(r.Root + addonRuntimeDir + slug)
			})
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			return err
		}
	}

	if err := r.ensureAddonDirs(to); err != nil {
		return err
	}

	if _, err := r.Run(ctx, "/usr/bin/systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("reload systemd for add-on units: %w", err)
	}

	// Safe mode or settings hold keeps add-ons off; stop any previously live add-on whose files changed so it
	// never continues running stale config/secrets, and drop stale stamps so ReleaseAddonsHold starts it once cleared.
	if ReadSafeMode(r.Root, r.StateDir).Active {
		var stopErrs []error
		for _, slug := range has {
			sum := addonSum(slug, to)
			stamp := r.Root + addonStampPath(slug)
			started, err := os.ReadFile(stamp)
			changed := err != nil || string(started) != sum
			if changed && slices.Contains(had, slug) {
				if serr := r.stopAddon(ctx, slug); serr != nil {
					stopErrs = append(stopErrs, serr)
				}
			}
			if changed {
				_ = os.Remove(stamp)
			}
		}
		return errors.Join(stopErrs...)
	}

	if len(has) == 0 {
		return nil
	}

	errs := make([]error, len(has))
	var wg sync.WaitGroup
	for i, slug := range has {
		wg.Go(func() {
			sum := addonSum(slug, to)
			stamp := r.Root + addonStampPath(slug)
			if started, err := os.ReadFile(stamp); err != nil || string(started) != sum {
				// Clear stamp before stop/start so an interruption guarantees rollback recreates the container.
				_ = os.Remove(stamp)
				if err := r.stopAddon(ctx, slug); err != nil {
					errs[i] = err
					return
				}
			}
			if _, err := r.Run(ctx, "/usr/bin/systemctl", "start", "--no-block", addonUnitName(slug)); err != nil {
				errs[i] = fmt.Errorf("start add-on %s: %w", addonUnitName(slug), err)
				return
			}
			if err := os.MkdirAll(filepath.Dir(stamp), 0o700); err != nil {
				errs[i] = err
				return
			}
			if err := writeAtomic(stamp, []byte(sum), 0o600); err != nil {
				errs[i] = err
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

type addonHealthSpec struct {
	slug           string
	url            string
	expectedStatus int
	timeout        time.Duration
	startPeriod    time.Duration
	hasHealthCmd   bool
}

func parseAddonHealth(slug string, data []byte) addonHealthSpec {
	spec := addonHealthSpec{slug: slug, expectedStatus: 200, timeout: 2 * time.Second}
	for line := range strings.SplitSeq(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "# andaru-health-url:"):
			fields := strings.Fields(strings.TrimPrefix(trimmed, "# andaru-health-url:"))
			if len(fields) >= 1 {
				spec.url = fields[0]
			}
			if len(fields) >= 2 {
				if code, err := strconv.Atoi(fields[1]); err == nil && code >= 100 && code <= 599 {
					spec.expectedStatus = code
				}
			}
		case strings.HasPrefix(trimmed, "# andaru-health-timeout:"):
			if sec, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(trimmed, "# andaru-health-timeout:"))); err == nil && sec > 0 && sec <= 10 {
				spec.timeout = time.Duration(sec) * time.Second
			}
		case strings.HasPrefix(trimmed, "# andaru-health-start-period:"):
			if sec, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(trimmed, "# andaru-health-start-period:"))); err == nil && sec > 0 && sec <= 30 {
				spec.startPeriod = time.Duration(sec) * time.Second
			}
		case strings.HasPrefix(trimmed, "HealthCmd="):
			spec.hasHealthCmd = true
		case strings.HasPrefix(trimmed, "HealthStartPeriod="):
			raw := strings.TrimSuffix(strings.TrimPrefix(trimmed, "HealthStartPeriod="), "s")
			if sec, err := strconv.Atoi(raw); err == nil && sec > 0 && sec <= 30 {
				spec.startPeriod = time.Duration(sec) * time.Second
			}
		}
	}
	return spec
}

func (r Realizer) probeHealthURL(ctx context.Context, rawURL string, timeout time.Duration) (int, error) {
	if r.HealthGet != nil {
		return r.HealthGet(ctx, rawURL)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() != "127.0.0.1" {
		return 0, fmt.Errorf("invalid loopback health URL %q", rawURL)
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := addonHealthClient.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// verifyAddons checks that every enabled add-on in next reaches active/healthy state within a bounded budget.
// Skipped when safe mode or settings hold is active so recovery applies succeed while add-ons are held off.
func (r Realizer) verifyAddons(ctx context.Context, next []placed) string {
	if ReadSafeMode(r.Root, r.StateDir).Active {
		return ""
	}
	bySlug := map[string]addonHealthSpec{}
	for _, f := range next {
		if m := addonContainer.FindStringSubmatch(f.path); len(m) == 2 {
			bySlug[m[1]] = parseAddonHealth(m[1], f.data)
		}
	}
	if len(bySlug) == 0 {
		return ""
	}
	needed := defaultAddonVerifyWait
	for _, spec := range bySlug {
		if want := spec.startPeriod + spec.timeout + 5*time.Second; want > needed {
			needed = want
		}
	}
	if needed > 30*time.Second {
		needed = 30 * time.Second
	}
	budget := needed
	if r.VerifyWait > 0 && r.VerifyWait < budget {
		budget = r.VerifyWait
	}
	verifyCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	poll := r.Poll
	if poll <= 0 {
		poll = 200 * time.Millisecond
	}
	slugs := slices.Sorted(maps.Keys(bySlug))
	reasons := make([]string, len(slugs))
	var wg sync.WaitGroup
	for i, slug := range slugs {
		spec := bySlug[slug]
		wg.Go(func() {
			reasons[i] = r.waitAddonReady(verifyCtx, spec, poll)
		})
	}
	wg.Wait()
	for _, reason := range reasons {
		if reason != "" {
			return reason
		}
	}
	return ""
}

func (r Realizer) waitAddonReady(ctx context.Context, spec addonHealthSpec, poll time.Duration) string {
	unit := addonUnitName(spec.slug)
	startedAt := time.Now()
	lastReason := "add-on " + spec.slug + " did not become ready"
	for {
		out, err := r.Run(ctx, "/usr/bin/systemctl", "show", "-p", "ActiveState", "--value", unit)
		if err != nil {
			lastReason = fmt.Sprintf("add-on %s unit status failed: %v", spec.slug, err)
		} else {
			state := strings.TrimSpace(string(out))
			switch state {
			case "failed":
				return fmt.Sprintf("add-on %s failed to start", spec.slug)
			case "active":
				switch {
				case spec.url != "":
					code, herr := r.probeHealthURL(ctx, spec.url, spec.timeout)
					if herr == nil && code == spec.expectedStatus {
						return ""
					}
					if herr != nil {
						lastReason = fmt.Sprintf("add-on %s health check failed: %v", spec.slug, herr)
					} else {
						lastReason = fmt.Sprintf("add-on %s health check returned HTTP %d (want %d)", spec.slug, code, spec.expectedStatus)
					}
				case spec.hasHealthCmd:
					hout, herr := r.Run(ctx, "/usr/bin/podman", "inspect", "--format", "{{if .State.Health}}{{.State.Health.Status}}{{end}}", addonContainerName(spec.slug))
					if herr != nil {
						lastReason = fmt.Sprintf("add-on %s health inspection failed: %v", spec.slug, herr)
					} else {
						switch status := strings.TrimSpace(string(hout)); status {
						case "healthy":
							return ""
						case "unhealthy":
							if spec.startPeriod == 0 || time.Since(startedAt) >= spec.startPeriod {
								return fmt.Sprintf("add-on %s reported unhealthy", spec.slug)
							}
							lastReason = fmt.Sprintf("add-on %s health status is unhealthy (within start period)", spec.slug)
						default:
							lastReason = fmt.Sprintf("add-on %s health status is %q", spec.slug, status)
						}
					}
				default:
					return ""
				}
			default:
				lastReason = fmt.Sprintf("add-on %s is %s", spec.slug, state)
			}
		}
		select {
		case <-ctx.Done():
			return "not verified in time: " + lastReason
		case <-time.After(poll):
		}
	}
}

// startLiveAddonsLocked starts enabled add-on units of the live generation after boot hold clears.
// Caller holds r.lock() when r.dir() exists.
func (r Realizer) startLiveAddonsLocked(ctx context.Context) error {
	if r.StateDir == "" {
		return nil
	}
	if _, err := os.Stat(r.dir()); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	p, err := readPointers(r.dir())
	if err != nil {
		return err
	}
	if p.Live == nil {
		return nil
	}
	g, err := read(r.dir(), *p.Live, p)
	if err != nil {
		return err
	}
	set, err := r.files(g)
	if err != nil {
		return err
	}
	if err := r.addons(ctx, nil, set); err != nil {
		// Use a detached bounded context and stop units concurrently so cleanup stops queued add-ons
		// even if ctx was canceled/timed out, and report any cleanup stop failures alongside err.
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreLimit)
		defer cancel()
		slugs := addonSlugs(set)
		stopErrs := make([]error, len(slugs))
		var wg sync.WaitGroup
		for i, slug := range slugs {
			wg.Go(func() {
				stopErrs[i] = r.stopAddon(stopCtx, slug)
				_ = os.Remove(r.Root + addonStampPath(slug))
			})
		}
		wg.Wait()
		return errors.Join(append([]error{err}, stopErrs...)...)
	}
	return nil
}

// AddonControl runs start, stop, or restart on an add-on unit present in the applied (live) generation.
// Refuses while safe mode or settings hold is active.
func (r Realizer) AddonControl(ctx context.Context, action, slug string) error {
	switch action {
	case "start", "stop", "restart":
	default:
		return fmt.Errorf("invalid add-on action %q", action)
	}
	if !addon.ValidSlug(slug) {
		return fmt.Errorf("invalid add-on slug %q", slug)
	}
	if ReadSafeMode(r.Root, r.StateDir).Active {
		return ErrSafeModeActive
	}
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	// Re-check under lock in case ReleaseAddonsHold restored the hold while waiting for the lock.
	if ReadSafeMode(r.Root, r.StateDir).Active {
		return ErrSafeModeActive
	}
	p, err := readPointers(r.dir())
	if err != nil {
		return err
	}
	if p.Live == nil {
		return ErrAddonNotApplied
	}
	g, err := read(r.dir(), *p.Live, p)
	if err != nil {
		return err
	}
	wantPath := addonQuadletPrefix + slug + ".container"
	if !slices.ContainsFunc(g.Files, func(f FileEntry) bool { return f.Path == wantPath }) {
		return ErrAddonNotApplied
	}
	if _, err := r.Run(ctx, "/usr/bin/systemctl", action, addonUnitName(slug)); err != nil {
		return fmt.Errorf("%s add-on %s: %w", action, slug, err)
	}
	return nil
}

func sameRegularFile(path string, want []byte, mode fs.FileMode) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != mode || info.Size() != int64(len(want)) {
		return false
	}
	got, err := os.ReadFile(path)
	return err == nil && string(got) == string(want)
}

// ReferencedAddonImages returns all immutable Image= digests referenced by Confirmed, LastKnownGood, Pending, Live,
// or newly created un-activated generations.
func ReferencedAddonImages(stateDir string) (map[string]bool, error) {
	var out map[string]bool
	err := WithAddonImageLock(context.Background(), stateDir, func(refs map[string]bool) error {
		out = refs
		return nil
	})
	return out, err
}

// WithAddonImageLock serializes add-on image unstaging against generation publication and activation by
// acquiring generations/lock first, collecting all referenced add-on Image= digests (Confirmed, LastKnownGood,
// Pending, Live, plus any newly created un-activated generation awaiting Activate), and invoking fn while the lock is held.
func WithAddonImageLock(ctx context.Context, stateDir string, fn func(refs map[string]bool) error) error {
	dir := Dir(stateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lockFile, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lockFile.Close()
	for deadline := time.Now().Add(10 * time.Second); ; {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("lock generations: %w", err)
		}
		err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if (!errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR)) || time.Now().After(deadline) {
			return fmt.Errorf("lock generations: %w", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("lock generations: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	refs, err := referencedAddonImagesLocked(dir)
	if err != nil {
		return err
	}
	return fn(refs)
}

func referencedAddonImagesLocked(dir string) (map[string]bool, error) {
	refs := map[string]bool{}
	p, err := readPointers(dir)
	if err != nil {
		return nil, err
	}
	seenID := map[int]bool{}
	collect := func(id int) error {
		if seenID[id] {
			return nil
		}
		seenID[id] = true
		g, err := read(dir, id, p)
		if err != nil {
			return err
		}
		for _, f := range g.Files {
			if !addonContainer.MatchString(f.Path) {
				continue
			}
			data, err := g.content(dir, f)
			if err != nil {
				return err
			}
			for line := range strings.SplitSeq(string(data), "\n") {
				if img, ok := strings.CutPrefix(strings.TrimSpace(line), "Image="); ok && img != "" {
					refs[img] = true
				}
			}
		}
		return nil
	}
	for _, id := range []*int{p.Confirmed, p.LastKnownGood, p.Pending, p.Live} {
		if id != nil {
			if err := collect(*id); err != nil {
				return nil, err
			}
		}
	}
	// Also protect generations just published by generation.Create that have not yet been picked up by Activate.
	allIDs, err := ids(dir)
	if err != nil {
		return nil, err
	}
	for _, id := range allIDs {
		if seenID[id] {
			continue
		}
		var st State
		present, rerr := readJSON(filepath.Join(dir, strconv.Itoa(id), "state.json"), &st)
		if rerr != nil {
			continue
		}
		if !present || st.State == "" || st.State == Activating || st.State == Awaiting {
			if err := collect(id); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, err
			}
		}
	}
	return refs, nil
}

// cleanupAddonsOnTeardownIfPresent runs cleanup only when orphaned add-on files or staged images exist
// (preserving Teardown's no-op behavior when p.Live == nil on a clean host).
func (r Realizer) cleanupAddonsOnTeardownIfPresent(ctx context.Context) error {
	matches, _ := filepath.Glob(r.Root + addonQuadletPrefix + "*.container")
	hasRuntime := false
	if entries, err := os.ReadDir(r.Root + strings.TrimSuffix(addonRuntimeDir, "/")); err == nil {
		for _, e := range entries {
			if e.Name() != "bus-socket" {
				hasRuntime = true
				break
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("scan add-on runtime directory: %w", err)
	}
	hasStaged := false
	if r.StateDir != "" {
		_, err := os.Stat(filepath.Join(r.StateDir, addon.StagedStateFile))
		if err == nil {
			hasStaged = true
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("stat staged add-on index: %w", err)
		}
	}
	if len(matches) == 0 && !hasRuntime && !hasStaged {
		return nil
	}
	return r.cleanupAddonsOnTeardown(ctx)
}

// cleanupAddonsOnTeardown removes any stray add-on Quadlets, per-add-on runtime directories under /run/andaru-addons
// (preserving /run/andaru-addons/bus-socket for andaru-engine), and tracked Podman images during factory reset.
func (r Realizer) cleanupAddonsOnTeardown(ctx context.Context) error {
	var errs []error
	matches, _ := filepath.Glob(r.Root + addonQuadletPrefix + "*.container")
	removedQuadlet := false
	for _, match := range matches {
		rel := strings.TrimPrefix(match, r.Root)
		if slug, ok := addonSlugFromPath(rel); ok {
			if err := r.stopAddon(ctx, slug); err != nil {
				// Keep the Quadlet on disk so a teardown retry can rediscover and stop the unit.
				errs = append(errs, err)
				continue
			}
		}
		if err := os.Remove(match); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		removedQuadlet = true
	}
	if removedQuadlet && r.Run != nil {
		if _, err := r.Run(ctx, "/usr/bin/systemctl", "daemon-reload"); err != nil {
			errs = append(errs, err)
		}
	}
	runBase := r.Root + strings.TrimSuffix(addonRuntimeDir, "/")
	if entries, err := os.ReadDir(runBase); err == nil {
		for _, e := range entries {
			if e.Name() == "bus-socket" {
				continue
			}
			if rerr := os.RemoveAll(filepath.Join(runBase, e.Name())); rerr != nil {
				errs = append(errs, rerr)
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, fmt.Errorf("scan add-on runtime directory: %w", err))
	}
	if r.StateDir != "" {
		if err := addon.RemoveAllManagedImages(ctx, r.StateDir, r.Run); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
