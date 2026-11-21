package host

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// systemTree writes the kernel files a test names under a temporary root.
func systemTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(root+path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(root+path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// processors repeats one /proc/cpuinfo block per thread, two threads to a core.
func processors(threads int, fields string) string {
	var out strings.Builder
	for i := range threads {
		out.WriteString("processor\t: " + string(rune('0'+i)) + "\nphysical id\t: 0\ncore id\t\t: " + string(rune('0'+i/2)) + "\n" + fields + "\n\n")
	}
	return out.String()
}

const v3Flags = "fpu cx8 cmov mmx fxsr sse sse2 syscall nx lm pni pclmulqdq ssse3 fma cx16 sse4_1 sse4_2 movbe popcnt aes xsave avx f16c rdrand lahf_lm abm bmi1 avx2 smep bmi2 rdseed adx smap sha_ni umip vaes vpclmulqdq"

func TestReadSystemInfoOnHardware(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	root := systemTree(t, map[string]string{
		"/proc/cpuinfo":             processors(4, "model name\t: Intel(R) Core(TM) i5-1235U\nmicrocode\t: 0x437\nflags\t\t: "+v3Flags+" vmx ibt user_shstk"),
		"/proc/sys/kernel/hostname": "edge\n", "/proc/sys/kernel/osrelease": "6.17.8-300.fc43.x86_64\n", "/proc/sys/kernel/arch": "x86_64\n",
		"/proc/uptime": "3600.25 14000.00\n", "/proc/meminfo": "MemTotal:       16268108 kB\nMemFree:         1000 kB\n",
		"/sys/class/dmi/id/sys_vendor": "Protectli\n", "/sys/class/dmi/id/product_name": "VP2420\n", "/sys/class/dmi/id/product_serial": "ABC123\n",
		"/sys/class/dmi/id/board_vendor": "Protectli\n", "/sys/class/dmi/id/board_name": "VP2420\n", "/sys/class/dmi/id/board_serial": "BOARD9\n",
		"/sys/class/dmi/id/bios_vendor": "coreboot\n", "/sys/class/dmi/id/bios_version": "v1.2.1\n", "/sys/class/dmi/id/bios_date": "03/14/2026\n",
		"/sys/devices/system/cpu/vulnerabilities/meltdown":   "Not affected\n",
		"/sys/devices/system/cpu/vulnerabilities/spectre_v2": "Mitigation: Enhanced / Automatic IBRS\n",
		"/sys/devices/system/cpu/vulnerabilities/tsa":        "Vulnerable: No microcode\n",
		"/sys/kernel/iommu_groups/0/type":                    "DMA\n",
	})
	got, err := ReadSystemInfo(root, now)
	if err != nil {
		t.Fatal(err)
	}
	want := SystemInfo{
		Hostname: "edge", Kernel: "6.17.8-300.fc43.x86_64", Architecture: "x86_64", Time: now, BootedAt: now.Add(-time.Hour), MemoryBytes: 16268108 << 10,
		CPU: CPU{
			Model: "Intel(R) Core(TM) i5-1235U", Cores: 2, Threads: 4, Microcode: "0x437", Level: "x86-64-v3",
			Features: Features{
				Cryptography:   []string{"AES-NI", "PCLMULQDQ", "VAES", "VPCLMULQDQ", "SHA-NI", "AVX2", "ADX", "BMI2", "RDRAND", "RDSEED"},
				Security:       []string{"NX", "SMEP", "SMAP", "UMIP", "CET shadow stack", "CET IBT"},
				Virtualization: []string{"VT-x"},
			},
			Vulnerabilities: []Vulnerability{
				{Name: "meltdown", Status: "Not affected", Mitigated: true},
				{Name: "spectre_v2", Status: "Mitigation: Enhanced / Automatic IBRS", Mitigated: true},
				{Name: "tsa", Status: "Vulnerable: No microcode", Mitigated: false},
			},
		},
		Hardware: Hardware{Vendor: "Protectli", Product: "VP2420", Serial: "ABC123", BoardVendor: "Protectli", Board: "VP2420", BIOSVendor: "coreboot", BIOSVersion: "v1.2.1", BIOSDate: "03/14/2026"},
		IOMMU:    true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("system info\n got %+v\nwant %+v", got, want)
	}
}

func TestReadSystemInfoLevels(t *testing.T) {
	for level, flags := range map[string]string{
		"":          "fpu sse sse2",
		"x86-64-v1": "lm cmov cx8 fpu fxsr mmx syscall sse sse2",
		// Every flag of v3 but one is still v2.
		"x86-64-v2": strings.Replace(v3Flags, " movbe", "", 1),
		"x86-64-v3": v3Flags + " avx512f avx512bw",
		"x86-64-v4": v3Flags + " avx512f avx512bw avx512cd avx512dq avx512vl",
	} {
		info, err := ReadSystemInfo(systemTree(t, map[string]string{"/proc/cpuinfo": "processor\t: 0\nflags\t\t: " + flags + "\n"}), time.Now())
		if err != nil || info.CPU.Level != level {
			t.Errorf("flags %q: level %q, want %q (%v)", flags, info.CPU.Level, level, err)
		}
	}
}

func TestReadSystemInfoAsAGuestWithLittleToRead(t *testing.T) {
	// A virtual machine: the hypervisor flag, a vendor, no core IDs, no serial of its own, no IOMMU, no vulnerabilities directory.
	root := systemTree(t, map[string]string{
		"/proc/cpuinfo":                    "processor\t: 0\nmodel name\t: QEMU Virtual CPU\nflags\t\t: fpu lm hypervisor svm\n\nprocessor\t: 1\nmodel name\t: QEMU Virtual CPU\nflags\t\t: fpu lm hypervisor svm\n",
		"/sys/class/dmi/id/sys_vendor":     "QEMU\n",
		"/sys/class/dmi/id/board_serial":   "FALLBACK\n",
		"/sys/class/dmi/id/product_serial": "\n",
	})
	info, err := ReadSystemInfo(root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if info.Guest != "QEMU" || info.IOMMU || info.CPU.Cores != 2 || info.CPU.Threads != 2 || info.Hardware.Serial != "FALLBACK" || info.Architecture == "" || !info.BootedAt.IsZero() {
		t.Fatalf("guest read as %+v", info)
	}
	if !reflect.DeepEqual(info.CPU.Features, Features{Cryptography: []string{}, Security: []string{}, Virtualization: []string{"AMD-V"}}) || info.CPU.Vulnerabilities == nil || len(info.CPU.Vulnerabilities) != 0 {
		t.Fatalf("empty lists must be lists, not null: %+v", info.CPU)
	}

	// Xen says what it is under /sys/hypervisor, flag or no flag; a guest that names no vendor is still a guest.
	xen, _ := ReadSystemInfo(systemTree(t, map[string]string{"/proc/cpuinfo": "processor\t: 0\nflags\t\t: fpu\n", "/sys/hypervisor/type": "xen\n"}), time.Now())
	unnamed, _ := ReadSystemInfo(systemTree(t, map[string]string{"/proc/cpuinfo": "processor\t: 0\nflags\t\t: fpu hypervisor\n"}), time.Now())
	if xen.Guest != "xen" || unnamed.Guest != "unknown" {
		t.Fatalf("guests read as %q and %q", xen.Guest, unnamed.Guest)
	}
}

func TestReadSystemInfoOnArm64(t *testing.T) {
	root := systemTree(t, map[string]string{
		"/proc/cpuinfo":         "processor\t: 0\nFeatures\t: fp asimd aes pmull sha1 sha2 crc32 atomics sha3 sha512 paca pacg bti rng\nCPU implementer\t: 0x41\n\nprocessor\t: 1\nFeatures\t: fp asimd aes pmull sha1 sha2 crc32 atomics sha3 sha512 paca pacg bti rng\n",
		"/proc/sys/kernel/arch": "aarch64\n",
	})
	info, err := ReadSystemInfo(root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := Features{Cryptography: []string{"AES", "PMULL", "SHA1", "SHA2", "SHA512", "SHA3", "ASIMD", "RNG"}, Security: []string{"PAuth", "BTI"}, Virtualization: []string{}}
	// "aes" on arm64 is AES, not AES-NI, and an arm64 CPU has no x86-64 level.
	if !reflect.DeepEqual(info.CPU.Features, want) || info.CPU.Level != "" || info.CPU.Model != "" || info.CPU.Threads != 2 || info.CPU.Cores != 2 || info.Architecture != "aarch64" {
		t.Fatalf("arm64 read as %+v", info)
	}
}

func TestReadSystemInfoNeedsTheProcessorList(t *testing.T) {
	if _, err := ReadSystemInfo(t.TempDir(), time.Now()); err == nil {
		t.Fatal("a tree without /proc/cpuinfo was read")
	}
}
