package host

import (
	"fmt"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// SystemInfo is what the box is, as its kernel reports it: read from /proc and /sys, never from a helper.
type SystemInfo struct {
	Hostname string `json:"hostname"`
	Kernel   string `json:"kernel"`
	// Architecture is the kernel's name for it, such as x86_64 or aarch64.
	Architecture string    `json:"architecture"`
	Time         time.Time `json:"time"`
	// BootedAt is nil when the kernel does not say when it started.
	BootedAt *time.Time `json:"bootedAt"`
	// MemoryBytes is the memory the kernel has to use, a little less than what is installed.
	MemoryBytes uint64   `json:"memoryBytes"`
	CPU         CPU      `json:"cpu"`
	Hardware    Hardware `json:"hardware"`
	// Guest names the hypervisor when the box is a virtual machine, and is empty on hardware. A Xen control
	// domain reads as a guest of Xen, which it is; a guest given its host's DMI strings is not recognised on arm64.
	Guest string `json:"guest"`
	// IOMMU reports whether the kernel has devices behind an active IOMMU.
	IOMMU bool `json:"iommu"`
}

type CPU struct {
	// Model is empty where the kernel names none, as on most arm64 machines.
	Model string `json:"model"`
	// Cores and Threads count what is online. A kernel that names no cores has one per thread as far as it tells.
	Cores   int `json:"cores"`
	Threads int `json:"threads"`
	// Microcode is the revision the kernel loaded, not the one on disk.
	Microcode string `json:"microcode"`
	// Level is the x86-64 microarchitecture level, "x86-64-v1" to "x86-64-v4", and empty on other architectures.
	Level           string          `json:"level"`
	Features        Features        `json:"features"`
	Vulnerabilities []Vulnerability `json:"vulnerabilities"`
}

// Features are the CPU's features a firewall gains from, each by its usual name and only when every online
// processor has it.
type Features struct {
	Cryptography   []string `json:"cryptography"`
	Security       []string `json:"security"`
	Virtualization []string `json:"virtualization"`
}

// Vulnerability is one line of the kernel's own report on a CPU vulnerability.
type Vulnerability struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// State reads the status: "not_affected", "mitigated", "vulnerable" when the kernel says so anywhere in
	// the line (a mitigation that leaves SMT exposed is still vulnerable), and "unknown" for anything else.
	State string `json:"state"`
}

// Hardware is the machine as its firmware describes it. Serial is the product's, or the board's when the
// product has none. A field the vendor left as a placeholder is empty.
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

// The kernel's flag names beside the names people know them by, in the order they are shown. The arm64 tables
// are apart because the same flag means another thing there: "aes" is AES-NI on x86 and AES on arm64.
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

	// Each x86-64 level adds these flags to the one before (the psABI's lists in the kernel's spellings: "pni" is
	// SSE3, "abm" is LZCNT, and "xsave" stands for OSXSAVE, which the kernel does not list). A CPU is at the
	// highest level it wholly has.
	levels = [][]string{
		{"lm", "cmov", "cx8", "fpu", "fxsr", "mmx", "syscall", "sse", "sse2"},
		{"cx16", "lahf_lm", "popcnt", "pni", "sse4_1", "sse4_2", "ssse3"},
		{"avx", "avx2", "bmi1", "bmi2", "f16c", "fma", "abm", "movbe", "xsave"},
		{"avx512f", "avx512bw", "avx512cd", "avx512dq", "avx512vl"},
	}

	// What vendors leave in a DMI field they never filled.
	dmiPlaceholders = []string{"to be filled by o.e.m.", "default string", "none", "not specified", "not applicable", "n/a", "unknown", "0", "system serial number", "system product name", "system manufacturer", "system version"}
	// What a virtual machine's DMI vendor or product says. Used on arm64 alone.
	guestMarks = []string{"qemu", "kvm", "vmware", "virtualbox", "virtual machine", "xen", "amazon ec2", "google compute engine", "parallels", "openstack", "bochs", "bhyve"}
)

// ReadSystemInfo reads the box's description from the kernel's files under root, which is "" on a real host.
// Only a missing /proc/cpuinfo is an error: everything else a machine may lack reads as empty.
func ReadSystemInfo(root string, now time.Time) (SystemInfo, error) {
	read := func(path string) string {
		raw, _ := os.ReadFile(root + path)
		return strings.TrimSpace(string(raw))
	}
	dmi := func(name string) string {
		value := read("/sys/class/dmi/id/" + name)
		if slices.Contains(dmiPlaceholders, strings.ToLower(value)) {
			return ""
		}
		return value
	}
	cpuinfo, err := os.ReadFile(root + "/proc/cpuinfo")
	if err != nil {
		return SystemInfo{}, err
	}
	info := SystemInfo{
		Hostname: read("/proc/sys/kernel/hostname"), Kernel: read("/proc/sys/kernel/osrelease"), Architecture: read("/proc/sys/kernel/arch"),
		Time: now.UTC(),
		Hardware: Hardware{
			Vendor: dmi("sys_vendor"), Product: dmi("product_name"), Serial: dmi("product_serial"), BoardVendor: dmi("board_vendor"), Board: dmi("board_name"),
			BIOSVendor: dmi("bios_vendor"), BIOSVersion: dmi("bios_version"), BIOSDate: dmi("bios_date"),
		},
	}
	// The kernel names its architecture since 6.1; before that, this binary's own is the same one.
	if info.Architecture == "" {
		if info.Architecture = map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]; info.Architecture == "" {
			info.Architecture = runtime.GOARCH
		}
	}
	if info.Hardware.Serial == "" {
		info.Hardware.Serial = dmi("board_serial")
	}
	for _, line := range strings.Split(read("/proc/stat"), "\n") {
		// The kernel's own whole second: worked out from the uptime, it would flicker by one between two readings.
		var seconds int64
		if n, _ := fmt.Sscanf(line, "btime %d", &seconds); n == 1 && seconds > 0 {
			booted := time.Unix(seconds, 0).UTC()
			info.BootedAt = &booted
		}
	}
	for _, line := range strings.Split(read("/proc/meminfo"), "\n") {
		var kibibytes uint64
		if n, _ := fmt.Sscanf(line, "MemTotal: %d kB", &kibibytes); n == 1 {
			info.MemoryBytes = kibibytes << 10
		}
	}

	// A flag counts when every online processor lists it: the cores of one machine may differ.
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
			arm = key == "Features"
			if listed := strings.Fields(value); flags == nil {
				flags = listed
			} else {
				flags = slices.DeleteFunc(flags, func(flag string) bool { return !slices.Contains(listed, flag) })
			}
		}
	}
	if info.CPU.Cores = len(cores); info.CPU.Cores == 0 {
		info.CPU.Cores = info.CPU.Threads
	}
	present := func(features []feature) []string {
		names := []string{}
		for _, f := range features {
			if slices.Contains(flags, f.flag) {
				names = append(names, f.name)
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
		info.CPU.Vulnerabilities = append(info.CPU.Vulnerabilities, Vulnerability{Name: entry.Name(), Status: status, State: vulnerabilityState(status)})
	}

	// Xen and s390 say so under /sys/hypervisor, and x86 guests carry the "hypervisor" flag. arm64 has no such
	// flag, so there, and only there, the firmware's strings tell: on x86 they would call a bare-metal cloud
	// machine a guest.
	firmware := strings.ToLower(info.Hardware.Vendor + " " + info.Hardware.Product)
	if hypervisor := read("/sys/hypervisor/type"); hypervisor != "" {
		info.Guest = hypervisor
	} else if slices.Contains(flags, "hypervisor") || arm && slices.ContainsFunc(guestMarks, func(mark string) bool { return strings.Contains(firmware, mark) }) {
		if info.Guest = info.Hardware.Vendor; info.Guest == "" {
			info.Guest = "unknown"
		}
	}
	groups, err := os.ReadDir(root + "/sys/kernel/iommu_groups")
	info.IOMMU = err == nil && len(groups) > 0
	return info, nil
}

// vulnerabilityState reads one line of /sys/devices/system/cpu/vulnerabilities. The kernel writes "Vulnerable",
// "KVM: Vulnerable", "Processor vulnerable", "Mitigation: ...; SMT vulnerable", "Unknown: ...", "Not affected".
func vulnerabilityState(status string) string {
	lower := strings.ToLower(status)
	switch {
	case strings.Contains(lower, "vulnerable"):
		return "vulnerable"
	case lower == "not affected":
		return "not_affected"
	case strings.HasPrefix(strings.TrimPrefix(lower, "kvm: "), "mitigation"):
		return "mitigated"
	}
	return "unknown"
}
