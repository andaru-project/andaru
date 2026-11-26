package generation

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSyncLeasesWritesLeaseViewAndEvaluatesExpiry(t *testing.T) {
	b := newBench(t)
	kea4Cfg := `{"Dhcp4":{"subnet4":[
		{"id":1,"subnet":"192.168.40.0/24","user-context":{"andaru":{"segment":"seg_lan","domain":"home.arpa","register":true}}}]}}`
	writeFile(t, filepath.Join(b.net.root, keaFile), kea4Cfg)
	kea6Cfg := `{"Dhcp6":{"subnet6":[
		{"id":7,"subnet":"fd00:1::/64","interface":"ens19","user-context":{"andaru":{"segment":"seg_lan","domain":"home.arpa","register":true}}}]}}`
	writeFile(t, filepath.Join(b.net.root, kea6File), kea6Cfg)

	serveSocket := func(sock string, reply map[string]any) {
		path := filepath.Join(b.net.root, sock)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		l, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				_, _ = bufio.NewReader(c).ReadString('}')
				raw, _ := json.Marshal(reply)
				_, _ = c.Write(raw)
				_ = c.Close()
			}
		}()
	}

	baseTime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC).Unix()
	serveSocket(keaSocket, map[string]any{
		"result": 0,
		"arguments": map[string]any{
			"leases": []map[string]any{
				// Unsorted order to verify deterministic address sorting.
				{"ip-address": "192.168.40.200", "hw-address": "AA:BB:CC:DD:EE:02", "hostname": "", "state": 0, "subnet-id": 1, "valid-lft": 3600, "cltt": baseTime},
				{"ip-address": "192.168.40.50", "hw-address": "AA:BB:CC:DD:EE:01", "client-id": "01:aa:bb", "hostname": "laptop", "state": 0, "subnet-id": 1, "valid-lft": 7200, "cltt": baseTime},
				{"ip-address": "192.168.40.99", "hw-address": "AA:BB:CC:DD:EE:03", "hostname": "old", "state": 3, "subnet-id": 1, "valid-lft": 7200, "cltt": baseTime},
			},
		},
	})
	serveSocket(kea6Socket, map[string]any{
		"result": 0,
		"arguments": map[string]any{
			"leases": []map[string]any{
				{"ip-address": "fd00:1::1000", "type": "IA_NA", "duid": "00:01:AA:BB", "hw-address": "AA:BB:CC:DD:EE:01", "hostname": "laptop", "state": 0, "subnet-id": 7, "valid-lft": 7200, "cltt": baseTime},
				{"ip-address": "fd00:beef:1:100::", "prefix-len": 56, "type": "IA_PD", "duid": "00:01:CC:DD", "hostname": "subrouter", "state": 0, "subnet-id": 7, "valid-lft": 3600, "cltt": baseTime},
			},
		},
	})

	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(b.net.root, leasesViewFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("leases.json mode = %v (%v), want 0600", info, err)
	}

	// Read 90 minutes after baseTime: 3600s leases are expired, 7200s leases stay active.
	now := time.Unix(baseTime+5400, 0).UTC()
	view, err := ReadLeaseView(b.net.root, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.V4) != 3 {
		t.Fatalf("got %d v4 leases, want 3: %+v", len(view.V4), view.V4)
	}
	if view.V4[0].Address != "192.168.40.50" || view.V4[0].MAC != "aa:bb:cc:dd:ee:01" || view.V4[0].Segment != "seg_lan" || view.V4[0].State != "active" {
		t.Fatalf("v4[0]: %+v", view.V4[0])
	}
	if view.V4[1].Address != "192.168.40.99" || view.V4[1].State != "released" {
		t.Fatalf("v4[1]: %+v", view.V4[1])
	}
	if view.V4[2].Address != "192.168.40.200" || view.V4[2].State != "expired" {
		t.Fatalf("v4[2]: %+v", view.V4[2])
	}
	if len(view.V6) != 2 {
		t.Fatalf("got %d v6 leases, want 2: %+v", len(view.V6), view.V6)
	}
	if view.V6[0].Type != "address" || view.V6[0].Address != "fd00:1::1000" || view.V6[0].DUID != "00:01:aa:bb" || view.V6[0].State != "active" {
		t.Fatalf("v6[0]: %+v", view.V6[0])
	}
	if view.V6[1].Type != "prefix" || view.V6[1].Address != "fd00:beef:1:100::/56" || view.V6[1].State != "expired" {
		t.Fatalf("v6[1]: %+v", view.V6[1])
	}

	// Removing DHCPv4 config clears v4 leases while keeping v6.
	if err := os.Remove(filepath.Join(b.net.root, keaFile)); err != nil {
		t.Fatal(err)
	}
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(b.net.root, leasesViewFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("leases.json mode = %v (%v), want 0600", info, err)
	}
	view, err = ReadLeaseView(b.net.root, time.Unix(baseTime+60, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(view.V4) != 0 || len(view.V6) != 2 {
		t.Fatalf("after removing kea4: v4=%d v6=%d", len(view.V4), len(view.V6))
	}
}

func TestReadLeaseViewAbsentReturnsEmpty(t *testing.T) {
	view, err := ReadLeaseView(t.TempDir(), time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if view.V4 == nil || view.V6 == nil || len(view.V4) != 0 || len(view.V6) != 0 {
		t.Fatalf("want empty non-nil slices, got %+v", view)
	}
}

func TestSyncLeasesUpdatesSnapshotWhenDNSUnchanged(t *testing.T) {
	b := newBench(t)
	// First sync: no hostname, so leases.conf stays "server:\n".
	b.kea(lease{"192.168.40.130", "", 0, 1, 7200, 1780000000})
	if err := b.r.SyncLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(b.net.root, leasesViewFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("leases.json mode = %v (%v), want 0600", info, err)
	}
	raw, err := os.ReadFile(filepath.Join(b.net.root, leasesViewFile))
	if err != nil || !strings.Contains(string(raw), "192.168.40.130") {
		t.Fatalf("leases.json missing unnamed lease: %v %s", err, raw)
	}
}
