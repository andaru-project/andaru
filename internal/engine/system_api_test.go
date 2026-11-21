package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	writeUnder(t, root, "/proc/uptime", "60.00 1.00\n")
	writeUnder(t, root, "/proc/meminfo", "MemTotal: 2048 kB\n")
	writeUnder(t, root, "/sys/class/dmi/id/sys_vendor", "QEMU\n")
	writeUnder(t, root, "/sys/class/dmi/id/product_serial", "SER-1\n")
	writeUnder(t, root, "/sys/devices/system/cpu/vulnerabilities/tsa", "Vulnerable\n")
	rec = get(http.MethodGet)
	validResponse(t, "SystemResponse", rec.Body.Bytes())
	var body struct {
		Version, Hostname, Guest string
		MemoryBytes              uint64
		CPU                      struct {
			Model, Level    string
			Features        struct{ Cryptography, Security, Virtualization []string }
			Vulnerabilities []struct {
				Name      string
				Mitigated bool
			}
		}
		Hardware struct{ Serial string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Version != buildVersion || body.Hostname != "edge" || body.Guest != "QEMU" || body.MemoryBytes != 2048<<10 || body.CPU.Model != "Test CPU" || body.CPU.Level != "x86-64-v1" ||
		strings.Join(body.CPU.Features.Cryptography, ",") != "AES-NI" || strings.Join(body.CPU.Features.Security, ",") != "NX" || strings.Join(body.CPU.Features.Virtualization, ",") != "VT-x" ||
		len(body.CPU.Vulnerabilities) != 1 || body.CPU.Vulnerabilities[0].Name != "tsa" || body.CPU.Vulnerabilities[0].Mitigated || body.Hardware.Serial != "SER-1" {
		t.Fatalf("%s", rec.Body)
	}

	if rec := get(http.MethodHead); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %q", rec.Code, rec.Body)
	}
	if rec := get(http.MethodPost); rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusForbidden {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body)
	}
}
