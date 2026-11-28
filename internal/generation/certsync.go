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
	"syscall"

	"github.com/andaru-project/andaru/internal/addon"
)

const (
	// WebCertificate and WebKey are the managed certificate andaru-web serves instead of its bootstrap one.
	WebCertificate = "/run/andaru-web/tls.crt"
	WebKey         = "/run/andaru-web/tls.key"
	webUnit        = "andaru-web.service"
)

var certFileName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}\.(crt|key|pem)$`)

// CertSync is what andaru realize cert-sync places: the certificates the applied generation's services name, as the
// saved head holds them now. Each consumer is one field, so the VPN services add theirs beside these.
type CertSync struct {
	// Web is the certificate andaru-web serves; nil removes the managed one, so it serves its bootstrap certificate.
	Web *CertPair `json:"web"`
	// Addons are, by slug, the files of each add-on's certs directory, by file name.
	Addons map[string]map[string]string `json:"addons"`
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
	return nil
}

// SyncCertificates places the certificates of s, rewriting only files whose content changed, and reloads what
// serves them. It runs under the generations lock, so it never interleaves with an activation placing files.
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
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return errors.Join(r.syncWebCertificate(ctx, s.Web), r.syncAddonCertificates(s.Addons))
}

func (r Realizer) syncWebCertificate(ctx context.Context, pair *CertPair) error {
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
			wrote, err := r.writeOwned(r.Root+f.path, []byte(f.data), uid, gid, "")
			if err != nil {
				return fmt.Errorf("place the managed web certificate: %w", err)
			}
			changed = changed || wrote
		}
	}
	if !changed {
		return nil
	}
	// Only a running andaru-web is told; one that starts later reads the files itself.
	if _, err := r.Run(ctx, "/usr/bin/systemctl", "try-reload-or-restart", webUnit); err != nil {
		return fmt.Errorf("reload %s: %w", webUnit, err)
	}
	return nil
}

// syncAddonCertificates rewrites certificate files in the certs directories of add-ons the applied generation
// placed; an add-on without one is not realized and is skipped. Each file keeps the owner and SELinux label Podman's
// :U and :Z gave the directory, so the running container still reads it.
func (r Realizer) syncAddonCertificates(addons map[string]map[string]string) error {
	var errs []error
	for _, slug := range slices.Sorted(maps.Keys(addons)) {
		dir := r.Root + addonRuntimeDir + slug + "/certs"
		info, err := os.Lstat(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() {
			errs = append(errs, fmt.Errorf("add-on %s certificate directory is not a directory", slug))
			continue
		}
		uid, gid := 0, 0
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			uid, gid = int(st.Uid), int(st.Gid)
		}
		files := addons[slug]
		for _, name := range slices.Sorted(maps.Keys(files)) {
			if _, err := r.writeOwned(filepath.Join(dir, name), []byte(files[name]), uid, gid, dir); err != nil {
				errs = append(errs, fmt.Errorf("place add-on %s certificate %s: %w", slug, name, err))
			}
		}
	}
	return errors.Join(errs...)
}

// writeOwned atomically replaces path with data, mode 0400, owned by uid:gid and labelled as labelFrom when set. A
// file that already holds data is left alone, inode, owner and label included.
func (r Realizer) writeOwned(path string, data []byte, uid, gid int, labelFrom string) (bool, error) {
	if sameRegularFile(path, data, 0o400) {
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
