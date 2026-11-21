package host

import (
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// SystemInfo is what the box is, as its kernel reports it: read from /proc and /sys, never from a helper.
type SystemInfo struct {
	Hostname     string    `json:"hostname"`
	Kernel       string    `json:"kernel"`
	Architecture string    `json:"architecture"`
	Time         time.Time `json:"time"`
	BootedAt     time.Time `json:"bootedAt"`
	MemoryBytes  uint64    `json:"memoryBytes"`
	CPU          CPU       `json:"cpu"`
	Hardware     Hardware  `json:"hardware"`
	// Guest names the hypervisor when the box is a virtual machine, and is empty on hardware.
	Guest string `json:"guest"`
	// IOMMU reports whether the kernel has devices behind an active IOMMU.
	IOMMU bool `json:"iommu"`
}

type CPU struct {
	// Model is empty where the kernel names none, as on most arm64 machines.
	Model   string `json:"model"`
	Cores   int    `json:"cores"`
	Threads int    `json:"threads"`
	// Microcode is the revision the kernel loaded, not the one on disk.
	Microcode string `json:"microcode"`
	// Level is the x86-64 microarchitecture level, "x86-64-v1" to "x86-64-v4", and empty on other architectures.
	Level           string          `json:"level"`
	Features        Features        `json:"features"`
	Vulnerabilities []Vulnerability `json:"vulnerabilities"`
}

// Features are the CPU's features a firewall gains from, each by its usual name and only when present.
type Features struct {
	Cryptography   []string `json:"cryptography"`
	Security       []string `json:"security"`
	Virtualization []string `json:"virtualization"`
}

// Vulnerability is one line of the kernel's own report on a CPU vulnerability.
type Vulnerability struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// Mitigated is false only where the kernel says "Vulnerable".
	Mitigated bool `json:"mitigated"`
}

type Hardware struct {
	Vendor      string `json:"vendor"`
	Product     string `json:"product"`
	Serial      string `json:"serial"`
	BoardVendor string `json:"boardVendor"`
	Board       string `json:"board"`
	BIOSVendor  string `json:"biosVendor"`
	BIOSVersion string `json:"biosVersion"`
	BIOSDate    string `json:"biosDate"`
}

type feature struct{ flag, name string }

// The kernel's flag names beside the names people know them by, in the order they are shown. x86 first, then
// arm64, whose flags come from another line of /proc/cpuinfo and never collide with these in meaning.
var (
	cryptographyFeatures = []feature{
		{"aes", "AES-NI"}, {"pclmulqdq", "PCLMULQDQ"}, {"vaes", "VAES"}, {"vpclmulqdq", "VPCLMULQDQ"}, {"sha_ni", "SHA-NI"},
		{"avx2", "AVX2"}, {"avx512f", "AVX-512"}, {"adx", "ADX"}, {"bmi2", "BMI2"}, {"rdrand", "RDRAND"}, {"rdseed", "RDSEED"},
	}
	securityFeatures       = []feature{{"nx", "NX"}, {"smep", "SMEP"}, {"smap", "SMAP"}, {"umip", "UMIP"}, {"user_shstk", "CET shadow stack"}, {"ibt", "CET IBT"}}
	virtualizationFeatures = []feature{{"vmx", "VT-x"}, {"svm", "AMD-V"}}

	armCryptographyFeatures = []feature{
		{"aes", "AES"}, {"pmull", "PMULL"}, {"sha1", "SHA1"}, {"sha2", "SHA2"}, {"sha512", "SHA512"}, {"sha3", "SHA3"},
		{"asimd", "ASIMD"}, {"sve", "SVE"}, {"rng", "RNG"},
	}
	armSecurityFeatures = []feature{{"paca", "PAuth"}, {"bti", "BTI"}, {"mte", "MTE"}}

	// Each x86-64 level adds these flags to the one before; a CPU is at the highest level it wholly has.
	levels = [][]string{
		{"lm", "cmov", "cx8", "fpu", "fxsr", "mmx", "syscall", "sse", "sse2"},
		{"cx16", "lahf_lm", "popcnt", "sse4_1", "sse4_2", "ssse3"},
		{"avx", "avx2", "bmi1", "bmi2", "f16c", "fma", "abm", "movbe", "xsave"},
		{"avx512f", "avx512bw", "avx512cd", "avx512dq", "avx512vl"},
	}
)

// ReadSystemInfo reads the box's description from the kernel's files under root, which is "" on a real host.
// Only a missing /proc/cpuinfo is an error: everything else a machine may lack reads as empty.
func ReadSystemInfo(root string, now time.Time) (SystemInfo, error) {
	read := func(path string) string {
		raw, _ := os.ReadFile(root + path)
		return strings.TrimSpace(string(raw))
	}
	cpuinfo, err := os.ReadFile(root + "/proc/cpuinfo")
	if err != nil {
		return SystemInfo{}, err
	}
	info := SystemInfo{
		Hostname: read("/proc/sys/kernel/hostname"), Kernel: read("/proc/sys/kernel/osrelease"), Architecture: read("/proc/sys/kernel/arch"),
		Time: now.UTC(),
		Hardware: Hardware{
			Vendor: read("/sys/class/dmi/id/sys_vendor"), Product: read("/sys/class/dmi/id/product_name"), Serial: read("/sys/class/dmi/id/product_serial"),
			BoardVendor: read("/sys/class/dmi/id/board_vendor"), Board: read("/sys/class/dmi/id/board_name"),
			BIOSVendor: read("/sys/class/dmi/id/bios_vendor"), BIOSVersion: read("/sys/class/dmi/id/bios_version"), BIOSDate: read("/sys/class/dmi/id/bios_date"),
		},
	}
	// The kernel names its architecture since 6.1; before that, this binary's own is the same one.
	if info.Architecture == "" {
		info.Architecture = runtime.GOARCH
	}
	if info.Hardware.Serial == "" {
		info.Hardware.Serial = read("/sys/class/dmi/id/board_serial")
	}
	if uptime, err := strconv.ParseFloat(firstField(read("/proc/uptime")), 64); err == nil {
		// Whole seconds: the boot time must not flicker by one between two readings.
		info.BootedAt = info.Time.Truncate(time.Second).Add(-time.Duration(uptime) * time.Second)
	}
	for _, line := range strings.Split(read("/proc/meminfo"), "\n") {
		if value, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			kibibytes, _ := strconv.ParseUint(firstField(value), 10, 64)
			info.MemoryBytes = kibibytes << 10
		}
	}

	var flags []string
	cores := map[string]struct{}{}
	var socket string
	arm := false
	for _, line := range strings.Split(string(cpuinfo), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "processor":
			info.CPU.Threads++
		case "model name":
			info.CPU.Model = value
		case "microcode":
			info.CPU.Microcode = value
		case "physical id":
			socket = value
		case "core id":
			cores[socket+"/"+value] = struct{}{}
		case "flags", "Features":
			flags, arm = strings.Fields(value), key == "Features"
		}
	}
	// A kernel that names no cores (arm64, some guests) has one per thread as far as it tells.
	if info.CPU.Cores = len(cores); info.CPU.Cores == 0 {
		info.CPU.Cores = info.CPU.Threads
	}
	present := func(features []feature) []string {
		names := []string{}
		for _, feature := range features {
			if slices.Contains(flags, feature.flag) {
				names = append(names, feature.name)
			}
		}
		return names
	}
	if arm {
		info.CPU.Features = Features{Cryptography: present(armCryptographyFeatures), Security: present(armSecurityFeatures), Virtualization: []string{}}
	} else {
		info.CPU.Features = Features{Cryptography: present(cryptographyFeatures), Security: present(securityFeatures), Virtualization: present(virtualizationFeatures)}
		for level, needed := range levels {
			if slices.ContainsFunc(needed, func(flag string) bool { return !slices.Contains(flags, flag) }) {
				break
			}
			info.CPU.Level = "x86-64-v" + strconv.Itoa(level+1)
		}
	}

	info.CPU.Vulnerabilities = []Vulnerability{}
	entries, _ := os.ReadDir(root + "/sys/devices/system/cpu/vulnerabilities")
	for _, entry := range entries {
		status := read("/sys/devices/system/cpu/vulnerabilities/" + entry.Name())
		info.CPU.Vulnerabilities = append(info.CPU.Vulnerabilities, Vulnerability{Name: entry.Name(), Status: status, Mitigated: !strings.HasPrefix(status, "Vulnerable")})
	}

	// x86 guests carry the "hypervisor" flag; Xen and others also say so under /sys/hypervisor.
	if hypervisor := read("/sys/hypervisor/type"); hypervisor != "" {
		info.Guest = hypervisor
	} else if slices.Contains(flags, "hypervisor") {
		if info.Guest = info.Hardware.Vendor; info.Guest == "" {
			info.Guest = "unknown"
		}
	}
	groups, err := os.ReadDir(root + "/sys/kernel/iommu_groups")
	info.IOMMU = err == nil && len(groups) > 0
	return info, nil
}

func firstField(value string) string {
	if fields := strings.Fields(value); len(fields) > 0 {
		return fields[0]
	}
	return ""
}
