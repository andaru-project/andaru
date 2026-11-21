package host

import (
	"errors"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReadUsage(t *testing.T) {
	root := systemTree(t, map[string]string{
		// user nice system idle iowait irq softirq steal guest guest_nice: guest time is inside user already.
		"/proc/stat":    "cpu  100 5 50 800 20 3 2 20 40 0\ncpu0 50 2 25 400 10 1 1 10 20 0\nbtime 1795258800\n",
		"/proc/loadavg": "0.52 0.31 0.10 2/345 6789\n",
		"/proc/meminfo": "MemTotal:        8000000 kB\nMemFree:          100000 kB\nMemAvailable:    6000000 kB\nSwapTotal:       2000000 kB\nSwapFree:        1500000 kB\n",
		"/proc/self/mounts": strings.Join([]string{
			"composefs / overlay ro,relatime 0 0",
			"/dev/vda4 /sysroot xfs ro,relatime 0 0",
			"/dev/vda4 /etc xfs rw,relatime 0 0",
			"/dev/vda4 /var xfs rw,relatime 0 0",
			"/dev/vda3 /boot ext4 ro,relatime 0 0",
			"tank/data /srv/my\\040data zfs rw 0 0",
			"tmpfs /run tmpfs rw 0 0",
			"/dev/sr0 /media/cd iso9660 ro 0 0",
			"/dev/vdb1 /gone ext4 rw 0 0",
		}, "\n") + "\n",
		"/proc/sys/net/netfilter/nf_conntrack_count": "1234\n",
		"/proc/sys/net/netfilter/nf_conntrack_max":   "262144\n",
		"/sys/class/hwmon/hwmon0/name":               "coretemp\n",
		"/sys/class/hwmon/hwmon0/temp1_input":        "45500\n",
		"/sys/class/hwmon/hwmon0/temp1_label":        "Package id 0\n",
		"/sys/class/hwmon/hwmon1/name":               "nvme\n",
		"/sys/class/hwmon/hwmon1/temp1_input":        "38000\n",
		"/sys/class/hwmon/hwmon1/temp2_input":        "not a number\n",
	})
	sizes := map[string]syscall.Statfs_t{
		root + "/sysroot":     {Bsize: 4096, Blocks: 1000, Bavail: 400, Bfree: 450},
		root + "/var":         {Bsize: 4096, Blocks: 1000, Bavail: 400, Bfree: 450},
		root + "/etc":         {Bsize: 4096, Blocks: 1000, Bavail: 400, Bfree: 450},
		root + "/boot":        {Bsize: 1024, Blocks: 500, Bavail: 100},
		root + "/srv/my data": {Bsize: 4096, Blocks: 2000, Bavail: 1500},
		root + "/media/cd":    {Bsize: 2048, Blocks: 0},
	}
	statfs = func(path string, out *syscall.Statfs_t) error {
		size, ok := sizes[path]
		if !ok {
			return errors.New("no such mount")
		}
		*out = size
		return nil
	}
	t.Cleanup(func() { statfs = syscall.Statfs })

	now := time.Date(2026, 11, 21, 13, 0, 0, 0, time.FixedZone("CET", 3600))
	got, err := ReadUsage(root, now)
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{
		Time: now.UTC(),
		// 100+5+50+800+20+3+2+20; idle is 800 idle plus 20 waiting for I/O.
		CPU:    CPUTimes{Total: 1000, Idle: 820},
		Load:   []float64{0.52, 0.31, 0.10},
		Memory: Memory{TotalBytes: 8000000 << 10, AvailableBytes: 6000000 << 10, SwapTotalBytes: 2000000 << 10, SwapFreeBytes: 1500000 << 10},
		// One device counts once, as /var where it is mounted there; a mount that cannot be sized, or is empty, is left out.
		FileSystems: []FileSystem{
			{MountPoint: "/boot", Type: "ext4", TotalBytes: 500 * 1024, AvailableBytes: 100 * 1024},
			{MountPoint: "/srv/my data", Type: "zfs", TotalBytes: 2000 * 4096, AvailableBytes: 1500 * 4096},
			{MountPoint: "/var", Type: "xfs", TotalBytes: 1000 * 4096, AvailableBytes: 400 * 4096},
		},
		Connections:  &Connections{Count: 1234, Limit: 262144},
		Temperatures: []Temperature{{Sensor: "coretemp Package id 0", Celsius: 45.5}, {Sensor: "nvme", Celsius: 38}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("usage\n got %+v (connections %+v)\nwant %+v (connections %+v)", got, got.Connections, want, want.Connections)
	}
}

func TestReadUsageOnABoxWithLittleToRead(t *testing.T) {
	got, err := ReadUsage(systemTree(t, map[string]string{"/proc/stat": "cpu  1 0 1 8 0 0 0 0\n"}), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Lists are lists, and what is not there is nil: no connection tracking, no load.
	if got.CPU != (CPUTimes{Total: 10, Idle: 8}) || got.FileSystems == nil || len(got.FileSystems) != 0 || got.Temperatures == nil || len(got.Temperatures) != 0 || got.Connections != nil || got.Load != nil {
		t.Fatalf("%+v", got)
	}
	if _, err = ReadUsage(t.TempDir(), time.Now()); err == nil {
		t.Fatal("a tree without /proc/stat was read")
	}
}

func TestReadCounters(t *testing.T) {
	root := systemTree(t, map[string]string{
		"/sys/class/net/lo/statistics/rx_bytes":      "999\n",
		"/sys/class/net/ens18/statistics/rx_bytes":   "1000\n",
		"/sys/class/net/ens18/statistics/tx_bytes":   "2000\n",
		"/sys/class/net/ens18/statistics/rx_packets": "10\n",
		"/sys/class/net/ens18/statistics/tx_packets": "20\n",
		"/sys/class/net/ens18/statistics/rx_errors":  "1\n",
		"/sys/class/net/ens18/statistics/tx_errors":  "2\n",
		"/sys/class/net/ens18/statistics/rx_dropped": "3\n",
		"/sys/class/net/ens18/statistics/tx_dropped": "4\n",
		"/sys/class/net/ens19/statistics/rx_bytes":   "5\n",
		"/sys/class/net/bonding_masters":             "bond0\n",
	})
	want := map[string]Counters{
		"ens18": {RxBytes: 1000, TxBytes: 2000, RxPackets: 10, TxPackets: 20, RxErrors: 1, TxErrors: 2, RxDropped: 3, TxDropped: 4},
		"ens19": {RxBytes: 5},
	}
	if got := ReadCounters(root); !reflect.DeepEqual(got, want) {
		t.Fatalf("counters\n got %+v\nwant %+v", got, want)
	}
	if got := ReadCounters(t.TempDir()); len(got) != 0 {
		t.Fatalf("a box without interfaces read as %+v", got)
	}
}
