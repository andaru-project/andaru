package host

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Usage is how hard the box is working at one moment, read from /proc and /sys. Counters are cumulative: a
// client works out a rate from two readings and their times.
type Usage struct {
	Time time.Time `json:"time"`
	CPU  CPUTimes  `json:"cpu"`
	// Load is the kernel's load averages over 1, 5 and 15 minutes; nil when it gives none.
	Load        []float64    `json:"load"`
	Memory      Memory       `json:"memory"`
	FileSystems []FileSystem `json:"fileSystems"`
	// Connections is the connection-tracking table; nil while the kernel tracks no connections.
	Connections  *Connections  `json:"connections"`
	Temperatures []Temperature `json:"temperatures"`
}

// CPUTimes are the kernel's tick counters summed over all processors since boot. Busy over a period is one
// minus the growth of Idle over the growth of Total.
type CPUTimes struct {
	Total uint64 `json:"total"`
	// Idle includes time waiting for I/O: the processor was free then.
	Idle uint64 `json:"idle"`
}

type Memory struct {
	TotalBytes uint64 `json:"totalBytes"`
	// AvailableBytes is what programs can still get without swapping, by the kernel's own estimate.
	AvailableBytes uint64 `json:"availableBytes"`
	SwapTotalBytes uint64 `json:"swapTotalBytes"`
	SwapFreeBytes  uint64 `json:"swapFreeBytes"`
}

type FileSystem struct {
	// MountPoint is where the device shows: "/" or "/var" when it is mounted there, else its shortest mount point.
	MountPoint string `json:"mountPoint"`
	Type       string `json:"type"`
	TotalBytes uint64 `json:"totalBytes"`
	// AvailableBytes is what an unprivileged writer can still use, which is what "full" means in practice.
	AvailableBytes uint64 `json:"availableBytes"`
}

type Connections struct {
	Count uint64 `json:"count"`
	Limit uint64 `json:"limit"`
}

type Temperature struct {
	// Sensor is the chip's name and, where the chip names its inputs, the input's label.
	Sensor  string  `json:"sensor"`
	Celsius float64 `json:"celsius"`
}

// Counters are one network interface's cumulative counters, as the kernel keeps them.
type Counters struct {
	RxBytes   uint64 `json:"rxBytes"`
	TxBytes   uint64 `json:"txBytes"`
	RxPackets uint64 `json:"rxPackets"`
	TxPackets uint64 `json:"txPackets"`
	RxErrors  uint64 `json:"rxErrors"`
	TxErrors  uint64 `json:"txErrors"`
	RxDropped uint64 `json:"rxDropped"`
	TxDropped uint64 `json:"txDropped"`
}

// statfs is the file system call ReadUsage sizes mounts with; tests replace it.
var statfs = syscall.Statfs

// ReadUsage reads the box's present load from the kernel's files under root, which is "" on a real host. Only
// a missing /proc/stat is an error: what a machine lacks reads as zero, nil or an empty list.
func ReadUsage(root string, now time.Time) (Usage, error) {
	read := func(path string) string { return readFile(root + path) }
	number := func(path string) (uint64, bool) {
		value, err := strconv.ParseUint(read(path), 10, 64)
		return value, err == nil
	}
	stat, err := os.ReadFile(root + "/proc/stat")
	if err != nil {
		return Usage{}, err
	}
	usage := Usage{Time: now.UTC(), FileSystems: []FileSystem{}, Temperatures: []Temperature{}}

	// The first line: "cpu user nice system idle iowait irq softirq steal guest guest_nice". Guest time is
	// already inside user and nice, so the first eight make the total.
	if fields := strings.Fields(strings.SplitN(string(stat), "\n", 2)[0]); len(fields) > 0 && fields[0] == "cpu" {
		for index, field := range fields[1:min(len(fields), 9)] {
			ticks, _ := strconv.ParseUint(field, 10, 64)
			usage.CPU.Total += ticks
			if index == 3 || index == 4 {
				usage.CPU.Idle += ticks
			}
		}
	}
	// All three or none: two numbers would not say which periods they are for.
	var load [3]float64
	if n, _ := fmt.Sscan(read("/proc/loadavg"), &load[0], &load[1], &load[2]); n == 3 && !slices.ContainsFunc(load[:], func(value float64) bool { return math.IsNaN(value) || math.IsInf(value, 0) }) {
		usage.Load = load[:]
	}
	for _, line := range strings.Split(read("/proc/meminfo"), "\n") {
		var name string
		var kibibytes uint64
		if n, _ := fmt.Sscanf(line, "%s %d kB", &name, &kibibytes); n == 2 {
			switch name {
			case "MemTotal:":
				usage.Memory.TotalBytes = kibibytes << 10
			case "MemAvailable:":
				usage.Memory.AvailableBytes = kibibytes << 10
			case "SwapTotal:":
				usage.Memory.SwapTotalBytes = kibibytes << 10
			case "SwapFree:":
				usage.Memory.SwapFreeBytes = kibibytes << 10
			}
		}
	}

	// File systems that hold data on a device or a pool. One device mounted in several places, as on an image
	// based system, counts once: as "/" or "/var" when it is mounted there, where the box's data grows, and
	// else at its shortest mount point. A ZFS pool counts once the same way: its datasets share its room.
	// FUSE mounts are left out, since a dead daemon would hold the reading for ever.
	// shortcut: a ZFS pool is sized by the one dataset shown, whose total is its own data plus the pool's free
	// room: a pool whose other datasets hold the data reads emptier than it is, and a dataset at its quota is not
	// seen. Read the pool's own size and allocation when ZFS ships in the image.
	// shortcut: statfs has no time limit, so a block device that stops answering holds this request; size the
	// mounts from a background reader when such devices are supported.
	seen := map[string]int{}
	for _, line := range strings.Split(read("/proc/self/mounts"), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || strings.HasPrefix(fields[2], "fuse") || !strings.HasPrefix(fields[0], "/dev/") && fields[2] != "zfs" {
			continue
		}
		device, mountPoint := fields[0], unescapeMount(fields[1])
		if fields[2] == "zfs" {
			device, _, _ = strings.Cut(device, "/")
		}
		var size syscall.Statfs_t
		if statfs(root+mountPoint, &size) != nil || size.Blocks == 0 {
			continue
		}
		// Block counts are in fragments, which is the block size on most file systems and not on all.
		unit := uint64(size.Frsize)
		if unit == 0 {
			unit = uint64(size.Bsize)
		}
		entry := FileSystem{MountPoint: mountPoint, Type: fields[2], TotalBytes: size.Blocks * unit, AvailableBytes: size.Bavail * unit}
		if index, ok := seen[device]; !ok {
			seen[device] = len(usage.FileSystems)
			usage.FileSystems = append(usage.FileSystems, entry)
		} else if mountRank(mountPoint) < mountRank(usage.FileSystems[index].MountPoint) {
			usage.FileSystems[index] = entry
		}
	}
	slices.SortFunc(usage.FileSystems, func(a, b FileSystem) int { return strings.Compare(a.MountPoint, b.MountPoint) })

	if count, ok := number("/proc/sys/net/netfilter/nf_conntrack_count"); ok {
		// A count without its limit says nothing about how full the table is.
		if limit, ok := number("/proc/sys/net/netfilter/nf_conntrack_max"); ok && limit > 0 {
			usage.Connections = &Connections{Count: count, Limit: limit}
		}
	}

	inputs, _ := filepath.Glob(root + "/sys/class/hwmon/hwmon*/temp*_input")
	slices.Sort(inputs)
	for _, input := range inputs {
		milli, err := strconv.ParseInt(readFile(input), 10, 64)
		if err != nil {
			continue
		}
		sensor := readFile(filepath.Join(filepath.Dir(input), "name"))
		if label := readFile(strings.TrimSuffix(input, "_input") + "_label"); label != "" {
			sensor = strings.TrimSpace(sensor + " " + label)
		}
		usage.Temperatures = append(usage.Temperatures, Temperature{Sensor: sensor, Celsius: float64(milli) / 1000})
	}
	return usage, nil
}

// ReadCounters reads every network interface's counters but the loopback's, by interface name. An interface
// whose counters cannot all be read, because it is going away, is left out: a zero would read as a reset.
func ReadCounters(root string) map[string]Counters {
	counters := map[string]Counters{}
	// /sys/class/net also holds files such as bonding_masters: only an entry with statistics is an interface.
	paths, _ := filepath.Glob(root + "/sys/class/net/*/statistics")
	for _, path := range paths {
		name := filepath.Base(filepath.Dir(path))
		if name == "lo" {
			continue
		}
		var reading Counters
		whole := true
		for file, into := range map[string]*uint64{
			"rx_bytes": &reading.RxBytes, "tx_bytes": &reading.TxBytes, "rx_packets": &reading.RxPackets, "tx_packets": &reading.TxPackets,
			"rx_errors": &reading.RxErrors, "tx_errors": &reading.TxErrors, "rx_dropped": &reading.RxDropped, "tx_dropped": &reading.TxDropped,
		} {
			value, err := strconv.ParseUint(readFile(filepath.Join(path, file)), 10, 64)
			*into, whole = value, whole && err == nil
		}
		if whole {
			counters[name] = reading
		}
	}
	return counters
}

func mountRank(mountPoint string) int {
	switch mountPoint {
	case "/":
		return 0
	case "/var":
		return 1
	}
	return 1 + len(mountPoint)
}

// readFile is a kernel file's text without its line end, and empty when it cannot be read.
func readFile(path string) string {
	raw, _ := os.ReadFile(path)
	return strings.TrimSpace(string(raw))
}

// unescapeMount undoes the kernel's octal escapes in a mount point: a space is written "\040".
func unescapeMount(value string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(value)
}
