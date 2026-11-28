package generation

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/andaru-project/andaru/internal/addon"
)

const (
	// WebCertificate and WebKey are the managed certificate andaru-web serves instead of its bootstrap one.
	WebCertificate     = "/run/andaru-web/tls.crt"
	WebKey             = "/run/andaru-web/tls.key"
	webReloadPending   = "/run/andaru-web/.reload-pending"
	webUnit            = "andaru-web.service"
	// MaxCertSyncBytes is the shared size bound for staged cert-sync payloads (covers up to 64 add-ons with max PEM chains).
	MaxCertSyncBytes = 16 << 20
)

var certFileName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}\.(crt|key|pem)$`)

// CertSync is what andaru realize cert-sync places: the certificates the applied generation's services name, as the
// saved head holds them now. Each consumer is one field, so the VPN services add theirs beside these.
type CertSync struct {
	// ExpectedLive, when set, checks under the generations lock that Pointers.Live still matches before writing,
	// preventing a stale payload from overwriting a concurrent activation or rollback.
	ExpectedLive *int `json:"expectedLive,omitempty"`
	// KeepWeb leaves any existing /run/andaru-web managed certificate untouched (e.g., when the live generation
	// bound a certificate that was subsequently deleted in saved head before an Apply).
	KeepWeb bool `json:"keepWeb,omitempty"`
	// Web is the certificate andaru-web serves; nil removes the managed one unless KeepWeb is true.
	Web *CertPair `json:"web"`
	// Addons are, by slug, the files of each add-on's certs directory, by file name.
	Addons map[string]map[string]string `json:"addons"`
	// IPsec are the IPsec connections' certificates and keys, by path under /run/andaru-ipsec
	// (x509/<id>.pem, private/<id>.key).
	IPsec map[string]string `json:"ipsec,omitempty"`
}

// CertPair is a PEM certificate, its chain appended, and its private key.
type CertPair struct {
	Cert string `json:"cert"`
	Key  string `json:"key"`
}

// lookupWebUser is the andaru-web account the managed certificate belongs to; tests replace it.
var lookupWebUser = func() (uid, gid int, err error) {
	u, err := user.Lookup("andaru-web")
	if err != nil {
		return 0, 0, err
	}
	uid, err = strconv.Atoi(u.Uid)
	if err == nil {
		gid, err = strconv.Atoi(u.Gid)
	}
	return uid, gid, err
}

// Validate refuses a payload naming a file or add-on outside what cert-sync may write.
func (s CertSync) Validate() error {
	if s.Web != nil && (s.Web.Cert == "" || s.Web.Key == "") {
		return errors.New("cert-sync: the web certificate needs a certificate and a key")
	}
	for slug, files := range s.Addons {
		if !addon.ValidSlug(slug) {
			return fmt.Errorf("cert-sync: invalid add-on slug %q", slug)
		}
		for name := range files {
			if !certFileName.MatchString(name) {
				return fmt.Errorf("cert-sync: invalid certificate file %q for add-on %q", name, slug)
			}
		}
	}
	for name := range s.IPsec {
		if !ipsecCredential.MatchString(ipsecDir+"/"+name) || strings.HasPrefix(name, "x509ca/") {
			return fmt.Errorf("cert-sync: invalid IPsec credential %q", name)
		}
	}
	return nil
}

// SyncCertificates places the certificates of s, rewriting only files whose content, owner, or SELinux label changed,
// and reloads what serves them. It runs under the generations lock, so it never interleaves with an activation placing files.
func (r Realizer) SyncCertificates(ctx context.Context, s CertSync) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if r.StateDir != "" {
		if _, err := os.Stat(r.dir()); err == nil {
			unlock, err := r.lock()
			if err != nil {
				return err
			}
			defer unlock()
			if s.ExpectedLive != nil {
				p, err := ReadPointers(r.dir())
				if err != nil {
					return err
				}
				if *s.ExpectedLive == 0 {
					if p.Live != nil {
						return nil
					}
				} else if p.Live == nil || *p.Live != *s.ExpectedLive {
					return nil
				}
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return errors.Join(r.syncWebCertificate(ctx, s.Web, s.KeepWeb), r.syncAddonCertificates(s.Addons), r.syncIPsecCertificates(ctx, s.IPsec))
}

func (r Realizer) syncWebCertificate(ctx context.Context, pair *CertPair, keepWeb bool) error {
	if keepWeb {
		return nil
	}
	webDir := filepath.Dir(r.Root + WebCertificate)
	if info, err := os.Lstat(webDir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed web certificate directory %s is not a real directory", webDir)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat managed web certificate directory: %w", err)
	}
	changed := false
	if pair == nil {
		for _, path := range []string{WebCertificate, WebKey} {
			if err := os.Remove(r.Root + path); err == nil {
				changed = true
			} else if !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("remove the managed web certificate: %w", err)
			}
		}
	} else {
		uid, gid, err := lookupWebUser()
		if err != nil {
			return fmt.Errorf("look up andaru-web: %w", err)
		}
		// The key first: a reload between the two writes then finds a pair that does not match and keeps the old one.
		for _, f := range []struct{ path, data string }{{WebKey, pair.Key}, {WebCertificate, pair.Cert}} {
			wrote, err := writeOwned(r.Root+f.path, []byte(f.data), uid, gid, "")
			if err != nil {
				return fmt.Errorf("place the managed web certificate: %w", err)
			}
			changed = changed || wrote
		}
	}
	pendingMarker := r.Root + webReloadPending
	_, markerErr := os.Lstat(pendingMarker)
	reloadPending := markerErr == nil
	if !changed && !reloadPending {
		return nil
	}
	if changed {
		// Record a reload marker so if systemctl fails, a subsequent retry still reloads andaru-web even though the files match.
		_ = os.WriteFile(pendingMarker, []byte("1\n"), 0o600)
	}
	// Only a running andaru-web is told; one that starts later reads the files itself.
	if _, err := r.Run(ctx, "/usr/bin/systemctl", "try-reload-or-restart", webUnit); err != nil {
		return fmt.Errorf("reload %s: %w", webUnit, err)
	}
	_ = os.Remove(pendingMarker)
	return nil
}

// syncAddonCertificates rewrites certificate files in the certs directories of add-ons the applied generation
// placed; an add-on without one is not realized and is skipped. Each file keeps the owner and SELinux label Podman's
// :U and :Z gave the directory, so the running container still reads it.
func (r Realizer) syncAddonCertificates(addons map[string]map[string]string) error {
	var errs []error
	for _, slug := range slices.Sorted(maps.Keys(addons)) {
		parentDir := r.Root + addonRuntimeDir + slug
		if pinfo, err := os.Lstat(parentDir); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil || !pinfo.IsDir() || pinfo.Mode()&os.ModeSymlink != 0 {
			errs = append(errs, fmt.Errorf("add-on %s runtime directory is not a real directory", slug))
			continue
		}
		dir := parentDir + "/certs"
		info, err := os.Lstat(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			errs = append(errs, fmt.Errorf("add-on %s certificate directory is not a directory", slug))
			continue
		}
		uid, gid := 0, 0
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			uid, gid = int(st.Uid), int(st.Gid)
		}
		files := addons[slug]
		for _, name := range slices.Sorted(maps.Keys(files)) {
			if _, err := writeOwned(filepath.Join(dir, name), []byte(files[name]), uid, gid, dir); err != nil {
				errs = append(errs, fmt.Errorf("place add-on %s certificate %s: %w", slug, name, err))
			}
		}
	}
	return errors.Join(errs...)
}

func sameOwnedRegularFile(path string, want []byte, mode fs.FileMode, uid, gid int, labelFrom string) bool {
	if !sameRegularFile(path, want, mode) {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		if int(st.Uid) != uid || int(st.Gid) != gid {
			return false
		}
	}
	if labelFrom != "" {
		wantBuf := make([]byte, 256)
		if wn, err := syscall.Getxattr(labelFrom, "security.selinux", wantBuf); err == nil && wn > 0 {
			gotBuf := make([]byte, 256)
			gn, err := syscall.Getxattr(path, "security.selinux", gotBuf)
			if err != nil || gn != wn || string(gotBuf[:gn]) != string(wantBuf[:wn]) {
				return false
			}
		}
	}
	return true
}

// writeOwned atomically replaces path with data, mode 0400, owned by uid:gid and labelled as labelFrom when set. A
// file that already holds matching data, mode, owner, and SELinux label is left alone.
func writeOwned(path string, data []byte, uid, gid int, labelFrom string) (bool, error) {
	if sameOwnedRegularFile(path, data, 0o400, uid, gid, labelFrom) {
		return false, nil
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".cert-*")
	if err != nil {
		return false, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(data); err == nil {
		err = f.Chmod(0o400)
	}
	if err == nil {
		err = f.Chown(uid, gid)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, err
	}
	if labelFrom != "" {
		copySELinuxLabel(labelFrom, tmp)
	}
	if err = os.Rename(tmp, path); err != nil {
		return false, err
	}
	return true, syncDir(filepath.Dir(path))
}

// syncIPsecCertificates rewrites the IPsec certificates and keys that changed, while a generation runs strongSwan, and
// has a running charon load them again.
func (r Realizer) syncIPsecCertificates(ctx context.Context, files map[string]string) error {
	if len(files) == 0 {
		return nil
	}
	if _, err := os.Lstat(r.Root + ipsecConf); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	changed := false
	for _, name := range slices.Sorted(maps.Keys(files)) {
		path, mode := r.Root+ipsecDir+"/"+name, fs.FileMode(0o644)
		if strings.HasPrefix(name, "private/") {
			mode = 0o400
		}
		if sameRegularFile(path, []byte(files[name]), mode) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := writeAtomic(path, []byte(files[name]), mode); err != nil {
			return fmt.Errorf("place IPsec credential %s: %w", name, err)
		}
		changed = true
	}
	if !changed {
		return nil
	}
	if err := r.ipsecCredentialDirs(ctx); err != nil {
		return err
	}
	if _, err := r.Run(ctx, "/usr/bin/systemctl", "is-active", "--quiet", strongswanUnit); err != nil {
		return nil // a charon that starts later loads them itself
	}
	if _, err := r.Run(ctx, swanctl, "--load-creds", "--file", ipsecConf, "--noprompt"); err != nil {
		return fmt.Errorf("reload the IPsec credentials: %w", err)
	}
	return nil
}
