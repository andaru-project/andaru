package generation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseWireGuardDump(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	dump := "wg0\tPRIVATE0=\tPUB0=\t51820\toff\n" +
		"wg0\tPEER1=\tPSK1=\t[2001:db8::7]:51820\t10.200.0.2/32,fd00:200::2/128\t1799999900\t1024\t2048\t25\n" +
		"wg0\tPEER2=\t(none)\t(none)\t(none)\t0\t0\t0\toff\n" +
		"wg1\tPRIVATE1=\tPUB1=\t51821\t0x1\n" +
		"wg1\tPEER3=\t(none)\t192.0.2.9:4500\t10.50.0.0/16\t1799999000\t5\t6\toff\n"
	got, err := ParseWireGuardDump([]byte(dump), now)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "PSK") {
		t.Fatalf("a secret key left the parser: %s", raw)
	}
	want := `{"interfaces":[{"ifname":"wg0","publicKey":"PUB0=","listenPort":51820,"peers":[` +
		`{"publicKey":"PEER1=","endpoint":"[2001:db8::7]:51820","allowedIps":["10.200.0.2/32","fd00:200::2/128"],"latestHandshake":1799999900,"rxBytes":1024,"txBytes":2048,"online":true},` +
		`{"publicKey":"PEER2=","allowedIps":[],"latestHandshake":0,"rxBytes":0,"txBytes":0,"online":false}]},` +
		`{"ifname":"wg1","publicKey":"PUB1=","listenPort":51821,"peers":[{"publicKey":"PEER3=","endpoint":"192.0.2.9:4500","allowedIps":["10.50.0.0/16"],"latestHandshake":1799999000,"rxBytes":5,"txBytes":6,"online":false}]}]}`
	if string(raw) != want {
		t.Fatalf("got  %s\nwant %s", raw, want)
	}
	if got, err := ParseWireGuardDump(nil, now); err != nil || len(got.Interfaces) != 0 {
		t.Fatalf("no interfaces: %+v %v", got, err)
	}
	for _, bad := range []string{"wg0\tx\n", "wg0\tA\tB\tport\toff\n", "wg0\tPEER\t(none)\t(none)\t(none)\t0\t0\t0\toff\n", "wg0\tA\tB\t1\toff\nwg0\tP\t(none)\t(none)\t(none)\tx\t0\t0\toff\n"} {
		if _, err := ParseWireGuardDump([]byte(bad), now); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "/usr/bin/wg" || strings.Join(args, " ") != "show all dump" {
			return nil, errors.New("unexpected " + name)
		}
		return []byte(dump), nil
	}
	if s, err := ReadWireGuardStatus(t.Context(), run, now); err != nil || len(s.Interfaces) != 2 {
		t.Fatalf("%+v %v", s, err)
	}
}
