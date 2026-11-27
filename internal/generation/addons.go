package generation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andaru-project/andaru/internal/addon"
)

const (
	addonQuadletPrefix = "/run/containers/systemd/andaru-addon-"
	addonRuntimeDir    = "/run/andaru-addons/"
	addonStoragePrefix = "/var/lib/andaru/addons/"
)

var (
	// ErrSafeModeActive is returned when manual add-on lifecycle control is attempted during safe mode or settings hold.
	ErrSafeModeActive = errors.New("SAFE_MODE_ACTIVE: add-ons are held off by safe mode")
	// ErrAddonNotApplied is returned when manual lifecycle control names an add-on not present in the live generation.
	ErrAddonNotApplied = errors.New("add-on is not enabled in the applied generation")

	addonStorageNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
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
	var slugs []string
	for _, f := range set {
		if m := addonContainer.FindStringSubmatch(f.path); len(m) == 2 && !seen[m[1]] {
			seen[m[1]] = true
			slugs = append(slugs, m[1])
		}
	}
	slices.Sort(slugs)
	return slugs
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

func (r Realizer) addonStorageRoot() string {
	if r.Root != "" {
		return filepath.Join(r.Root, "var", "lib", "andaru", "addons")
	}
	if r.StateDir != "" {
		return filepath.Join(r.StateDir, "addons")
	}
	return "/var/lib/andaru/addons"
}

// ensureAddonStorageDirs creates host storage directories referenced by Volume=/var/lib/andaru/addons/<slug>/<name>:...
func (r Realizer) ensureAddonStorageDirs(set []placed) error {
	base := r.addonStorageRoot()
	for _, f := range set {
		m := addonContainer.FindStringSubmatch(f.path)
		if len(m) != 2 {
			continue
		}
		slug := m[1]
		prefix := "Volume=" + addonStoragePrefix + slug + "/"
		for line := range strings.SplitSeq(string(f.data), "\n") {
			rest, ok := strings.CutPrefix(strings.TrimSpace(line), prefix)
			if !ok {
				continue
			}
			name, _, hasColon := strings.Cut(rest, ":")
			if !hasColon || !addonStorageNamePattern.MatchString(name) {
				continue
			}
			if err := os.MkdirAll(filepath.Join(base, slug, name), 0o700); err != nil {
				return fmt.Errorf("create add-on storage directory %s/%s: %w", slug, name, err)
			}
		}
	}
	return nil
}

func (r Realizer) stopAddon(ctx context.Context, slug string) error {
	unit := addonUnitName(slug)
	_, err := r.Run(ctx, "/usr/bin/systemctl", "stop", unit)
	if err == nil {
		return nil
	}
	if state, showErr := r.Run(ctx, "/usr/bin/systemctl", "show", "-p", "LoadState", "--value", unit); showErr == nil && strings.TrimSpace(string(state)) == "not-found" {
		return nil
	}
	return fmt.Errorf("stop add-on %s: %w", unit, err)
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
			wg.Add(1)
			go func(idx int, sl string) {
				defer wg.Done()
				if err := r.stopAddon(ctx, sl); err != nil {
					errs[idx] = err
					return
				}
				_ = os.Remove(r.Root + addonStampPath(sl))
				_ = os.Remove(r.Root + addonRuntimeDir + sl + "/secrets")
				_ = os.Remove(r.Root + addonRuntimeDir + sl)
			}(i, slug)
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			return err
		}
	}

	if err := r.ensureAddonStorageDirs(to); err != nil {
		return err
	}

	if _, err := r.Run(ctx, "/usr/bin/systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("reload systemd for add-on units: %w", err)
	}

	// Safe mode or settings hold keeps add-ons off; drop stale stamps so ReleaseAddonsHold starts them once cleared.
	if ReadSafeMode(r.Root, r.StateDir).Active {
		for _, slug := range has {
			sum := addonSum(slug, to)
			if started, err := os.ReadFile(r.Root + addonStampPath(slug)); err == nil && string(started) != sum {
				_ = os.Remove(r.Root + addonStampPath(slug))
			}
		}
		return nil
	}

	if len(has) == 0 {
		return nil
	}

	errs := make([]error, len(has))
	var wg sync.WaitGroup
	for i, slug := range has {
		wg.Add(1)
		go func(idx int, sl string) {
			defer wg.Done()
			sum := addonSum(sl, to)
			stamp := r.Root + addonStampPath(sl)
			if started, err := os.ReadFile(stamp); err != nil || string(started) != sum {
				if err := r.stopAddon(ctx, sl); err != nil {
					errs[idx] = err
					return
				}
			}
			if _, err := r.Run(ctx, "/usr/bin/systemctl", "start", "--no-block", addonUnitName(sl)); err != nil {
				errs[idx] = fmt.Errorf("start add-on %s: %w", addonUnitName(sl), err)
				return
			}
			if err := os.MkdirAll(filepath.Dir(stamp), 0o700); err != nil {
				errs[idx] = err
				return
			}
			if err := writeAtomic(stamp, []byte(sum), 0o600); err != nil {
				errs[idx] = err
			}
		}(i, slug)
	}
	wg.Wait()
	return errors.Join(errs...)
}

type addonHealthSpec struct {
	slug           string
	url            string
	expectedStatus int
	hasHealthCmd   bool
}

func parseAddonHealth(slug string, data []byte) addonHealthSpec {
	spec := addonHealthSpec{slug: slug, expectedStatus: 200}
	for line := range strings.SplitSeq(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(trimmed, "# andaru-health-url:"); ok {
			fields := strings.Fields(rest)
			if len(fields) >= 1 {
				spec.url = fields[0]
			}
			if len(fields) >= 2 {
				if code, err := strconv.Atoi(fields[1]); err == nil && code >= 100 && code <= 599 {
					spec.expectedStatus = code
				}
			}
		} else if strings.HasPrefix(trimmed, "HealthCmd=") {
			spec.hasHealthCmd = true
		}
	}
	return spec
}

func (r Realizer) probeHealthURL(ctx context.Context, rawURL string) (int, error) {
	if r.HealthGet != nil {
		return r.HealthGet(ctx, rawURL)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() != "127.0.0.1" {
		return 0, fmt.Errorf("invalid loopback health URL %q", rawURL)
	}
	client := &http.Client{
		Timeout: 2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// verifyAddons checks that every enabled add-on in next reaches active/healthy state within the verify context.
// Skipped when safe mode or settings hold is active so recovery applies succeed while add-ons are held off.
func (r Realizer) verifyAddons(ctx context.Context, next []placed) string {
	slugs := addonSlugs(next)
	if len(slugs) == 0 || ReadSafeMode(r.Root, r.StateDir).Active {
		return ""
	}
	bySlug := map[string]addonHealthSpec{}
	for _, f := range next {
		if m := addonContainer.FindStringSubmatch(f.path); len(m) == 2 {
			bySlug[m[1]] = parseAddonHealth(m[1], f.data)
		}
	}

	poll := r.Poll
	if poll <= 0 {
		poll = 200 * time.Millisecond
	}
	reasons := make([]string, len(slugs))
	var wg sync.WaitGroup
	for i, slug := range slugs {
		wg.Add(1)
		go func(idx int, spec addonHealthSpec) {
			defer wg.Done()
			reasons[idx] = r.waitAddonReady(ctx, spec, poll)
		}(i, bySlug[slug])
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
					code, herr := r.probeHealthURL(ctx, spec.url)
					if herr == nil && code == spec.expectedStatus {
						return ""
					}
					if herr != nil {
						lastReason = fmt.Sprintf("add-on %s health check failed: %v", spec.slug, herr)
					} else {
						lastReason = fmt.Sprintf("add-on %s health check returned HTTP %d (want %d)", spec.slug, code, spec.expectedStatus)
					}
				case spec.hasHealthCmd:
					if _, herr := r.Run(ctx, "/usr/bin/podman", "healthcheck", "run", addonContainerName(spec.slug)); herr == nil {
						return ""
					} else {
						lastReason = fmt.Sprintf("add-on %s health command failed: %v", spec.slug, herr)
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

// startLiveAddons starts enabled add-on units of the live generation after boot hold clears.
func (r Realizer) startLiveAddons(ctx context.Context) error {
	if r.StateDir == "" {
		return nil
	}
	if _, err := os.Stat(r.dir()); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	p, err := readPointers(r.dir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
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
	if len(addonSlugs(set)) == 0 {
		return nil
	}
	return r.addons(ctx, nil, set)
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

// ReferencedAddonImages returns all immutable Image= digests referenced by Confirmed, LastKnownGood, Pending, or Live generations.
func ReferencedAddonImages(stateDir string) (map[string]bool, error) {
	refs := map[string]bool{}
	dir := Dir(stateDir)
	p, err := readPointers(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return refs, nil
		}
		return nil, err
	}
	seenID := map[int]bool{}
	for _, id := range []*int{p.Confirmed, p.LastKnownGood, p.Pending, p.Live} {
		if id == nil || seenID[*id] {
			continue
		}
		seenID[*id] = true
		g, err := read(dir, *id, p)
		if err != nil {
			return nil, err
		}
		for _, f := range g.Files {
			if !addonContainer.MatchString(f.Path) {
				continue
			}
			data, err := g.content(dir, f)
			if err != nil {
				return nil, err
			}
			for line := range strings.SplitSeq(string(data), "\n") {
				if img, ok := strings.CutPrefix(strings.TrimSpace(line), "Image="); ok && img != "" {
					refs[img] = true
				}
			}
		}
	}
	return refs, nil
}

// cleanupAddonsOnTeardown removes any stray add-on Quadlets, /run/andaru-addons, and tracked Podman images during factory reset.
func (r Realizer) cleanupAddonsOnTeardown(ctx context.Context) error {
	var errs []error
	matches, _ := filepath.Glob(r.Root + addonQuadletPrefix + "*.container")
	for _, match := range matches {
		rel := strings.TrimPrefix(match, r.Root)
		if slug, ok := addonSlugFromPath(rel); ok {
			if err := r.stopAddon(ctx, slug); err != nil {
				errs = append(errs, err)
			}
		}
		if err := os.Remove(match); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if len(matches) > 0 && r.Run != nil {
		if _, err := r.Run(ctx, "/usr/bin/systemctl", "daemon-reload"); err != nil {
			errs = append(errs, err)
		}
	}
	if err := os.RemoveAll(r.Root + strings.TrimSuffix(addonRuntimeDir, "/")); err != nil {
		errs = append(errs, err)
	}
	if r.StateDir != "" {
		if err := addon.RemoveAllManagedImages(ctx, r.StateDir, r.Run); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
