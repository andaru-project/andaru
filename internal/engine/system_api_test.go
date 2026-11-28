package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/authz"
)

// The system description: the kernel's files as the schema promises them, beside the Andaru version.
func TestSystemStatus(t *testing.T) {
	root := t.TempDir()
	systemRoot = root
	t.Cleanup(func() { systemRoot = "" })
	h := newHandler(nil, http.NotFoundHandler())
	get := func(method string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, rootRequest(method, "/api/v1/status/system"))
		return rec
	}

	// A box whose processor list cannot be read says so; it does not answer with an empty description.
	if rec := get(http.MethodGet); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "INVENTORY_UNAVAILABLE") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}

	// The least a machine reports: every list is a list and every text a text, as the schema requires.
	writeUnder(t, root, "/proc/cpuinfo", "processor\t: 0\nFeatures\t: fp asimd\n")
	rec := get(http.MethodGet)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	validResponse(t, "SystemResponse", rec.Body.Bytes())

	writeUnder(t, root, "/proc/cpuinfo", "processor\t: 0\nmodel name\t: Test CPU\nmicrocode\t: 0x1\nflags\t\t: lm cmov cx8 fpu fxsr mmx syscall sse sse2 aes nx vmx hypervisor\n")
	writeUnder(t, root, "/proc/sys/kernel/hostname", "edge\n")
	writeUnder(t, root, "/proc/stat", "btime 1795258800\n")
	writeUnder(t, root, "/proc/meminfo", "MemTotal: 2048 kB\n")
	writeUnder(t, root, "/sys/class/dmi/id/sys_vendor", "QEMU\n")
	writeUnder(t, root, "/sys/class/dmi/id/product_serial", "SER-1\n")
	writeUnder(t, root, "/sys/devices/system/cpu/vulnerabilities/tsa", "Vulnerable\n")
	rec = get(http.MethodGet)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	validResponse(t, "SystemResponse", rec.Body.Bytes())
	// The reading itself is the host package's to test; here, that it arrives beside the version.
	var body struct{ Version, Hostname string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Version != buildVersion || body.Hostname != "edge" {
		t.Fatalf("%v %s", err, rec.Body)
	}

	if rec := get(http.MethodHead); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %q", rec.Code, rec.Body)
	}
	if rec := get(http.MethodPost); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body)
	}
}

// What the box reports about itself is status: every status route asks for status.read and nothing else, but for
// the ACME orders' status, which is the certificates' and asks for certificates.read, and the VPNs', which ask for
// vpn.read.
func TestStatusRoutesNeedStatusRead(t *testing.T) {
	seen := 0
	for _, route := range buildEngineRoutes() {
		if route.Path == acmeStatusPath {
			if route.Capability != authz.CertificatesRead {
				t.Errorf("%s %s needs %q", route.Method, route.Path, route.Capability)
			}
			continue
		}
		if route.Path == wireguardStatusPath {
			if route.Capability != authz.VPNRead {
				t.Errorf("%s %s needs %q", route.Method, route.Path, route.Capability)
			}
			continue
		}
		if strings.HasPrefix(route.Path, "/api/v1/status/") {
			seen++
			if route.Capability != authz.StatusRead {
				t.Errorf("%s %s needs %q", route.Method, route.Path, route.Capability)
			}
		}
	}
	if seen != 16 {
		t.Fatalf("%d status routes, want links, prefixes, leases, lldp, offloads, system, usage and traffic, each for GET and HEAD", seen)
	}
}
