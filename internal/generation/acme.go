package generation

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// ACMESet is the packet filter's set of interfaces whose port 80 goes to the HTTP-01 responder.
const ACMESet = "acme_http01_active"

var ifname = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,14}$`)

// ValidIfname reports whether name is a kernel interface name nft may be given.
func ValidIfname(name string) bool { return ifname.MatchString(name) }

// ACMEHTTP01 opens the HTTP-01 redirect on ifaces, in one nft transaction, or closes it on all interfaces when
// ifaces is empty. Closing a filter without the set, rendered only while an enabled HTTP-01 order exists, succeeds.
// It runs under the generations lock when StateDir is initialized so it never interleaves with Activate/Rollback.
func (r Realizer) ACMEHTTP01(ctx context.Context, ifaces []string) error {
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
	if len(ifaces) == 0 {
		if _, err := r.Run(ctx, "/usr/sbin/nft", "flush", "set", "inet", "andaru", ACMESet); err != nil {
			if _, listErr := r.Run(ctx, "/usr/sbin/nft", "list", "set", "inet", "andaru", ACMESet); listErr != nil {
				return nil // no set, so nothing redirected
			}
			return err
		}
		return nil
	}
	quoted := make([]string, len(ifaces))
	for i, name := range ifaces {
		if !ValidIfname(name) {
			return fmt.Errorf("invalid interface name %q", name)
		}
		quoted[i] = strconv.Quote(name)
	}
	_, err := r.Run(ctx, "/usr/sbin/nft", "add", "element", "inet", "andaru", ACMESet, "{ "+strings.Join(quoted, ", ")+" }")
	return err
}
