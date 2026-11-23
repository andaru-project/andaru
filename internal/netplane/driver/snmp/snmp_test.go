// SPDX-License-Identifier: AGPL-3.0-or-later
package snmp

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/gosnmp/gosnmp"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

type fakeWalker struct {
	trees map[string][]gosnmp.SnmpPDU
	fail  map[string]bool // root OIDs whose walk fails
}

func (f *fakeWalker) Walk(_ context.Context, rootOID string) ([]gosnmp.SnmpPDU, error) {
	if f.fail[rootOID] {
		return nil, errors.New("request timeout")
	}
	return f.trees[rootOID], nil
}
func (f *fakeWalker) Close() error { return nil }

func TestSNMPValidationV2cAndV3(t *testing.T) {
	drv := New()
	if err := drv.ValidateConfig(driver.TargetConfig{ID: "s1"}); err == nil {
		t.Fatal("expected error for missing endpoint")
	}
	if err := drv.ValidateConfig(driver.TargetConfig{ID: "s1", Endpoint: "192.0.2.30"}); err == nil {
		t.Fatal("expected error for missing v2c community")
	}
	if err := drv.ValidateConfig(driver.TargetConfig{
		ID: "s1", Endpoint: "192.0.2.30", Settings: map[string]any{"version": "2c"},
		ResolvedSecrets: map[string]string{"community": "public"},
	}); err != nil {
		t.Fatalf("valid v2c rejected: %v", err)
	}
	if err := drv.ValidateConfig(driver.TargetConfig{
		ID: "s3", Endpoint: "192.0.2.30:161",
		Settings:        map[string]any{"version": "3", "username": "netplane", "auth_protocol": "SHA256", "priv_protocol": "AES"},
		ResolvedSecrets: map[string]string{"auth_passphrase": "auth", "priv_passphrase": "priv"},
	}); err != nil {
		t.Fatalf("valid v3 rejected: %v", err)
	}
	if err := drv.ValidateConfig(driver.TargetConfig{
		ID: "s3-bad", Endpoint: "192.0.2.30:161",
		Settings: map[string]any{"version": "3", "username": "netplane", "auth_protocol": "SHA256"},
	}); err == nil {
		t.Fatal("expected error for missing v3 auth_passphrase")
	}
	if parseAuthProto("SHA256") != gosnmp.SHA256 || parsePrivProto("AES256") != gosnmp.AES256 {
		t.Fatal("unexpected v3 protocol mapping")
	}
}

func TestSNMPDriverPollWithFixtures(t *testing.T) {
	fw := &fakeWalker{
		trees: map[string][]gosnmp.SnmpPDU{
			".1.3.6.1.2.1.1": {
				{Name: oidSysDescr, Type: gosnmp.OctetString, Value: []byte("Managed 24P Switch")},
				{Name: oidSysName, Type: gosnmp.OctetString, Value: []byte("core-sw")},
				{Name: oidSysUpTime, Type: gosnmp.TimeTicks, Value: uint32(360000)},
			},
			oidIfTable: {
				{Name: oidIfTable + ".2.1", Type: gosnmp.OctetString, Value: []byte("gi1")},
				{Name: oidIfTable + ".4.1", Type: gosnmp.Integer, Value: 1500},
				{Name: oidIfTable + ".6.1", Type: gosnmp.OctetString, Value: []byte{0xaa, 0xbb, 0xcc, 0x00, 0x00, 0x01}},
				{Name: oidIfTable + ".7.1", Type: gosnmp.Integer, Value: 1},
				{Name: oidIfTable + ".8.1", Type: gosnmp.Integer, Value: 1},
				{Name: oidIfTable + ".10.1", Type: gosnmp.Counter32, Value: uint(111)},
				{Name: oidIfTable + ".13.1", Type: gosnmp.Counter32, Value: uint(2)},
				{Name: oidIfTable + ".14.1", Type: gosnmp.Counter32, Value: uint(3)},
				{Name: oidIfTable + ".16.1", Type: gosnmp.Counter32, Value: uint(222)},
				{Name: oidIfTable + ".19.1", Type: gosnmp.Counter32, Value: uint(4)},
				{Name: oidIfTable + ".20.1", Type: gosnmp.Counter32, Value: uint(5)},
				{Name: oidIfTable + ".2.25", Type: gosnmp.OctetString, Value: []byte("sfp25")},
				{Name: oidIfTable + ".4.25", Type: gosnmp.Integer, Value: 9000},
				{Name: oidIfTable + ".7.25", Type: gosnmp.Integer, Value: 1},
				{Name: oidIfTable + ".8.25", Type: gosnmp.Integer, Value: 1},
			},
			oidIfXTable: {
				{Name: oidIfXTable + ".1.1", Type: gosnmp.OctetString, Value: []byte("gi1")},
				{Name: oidIfXTable + ".6.1", Type: gosnmp.Counter64, Value: uint64(123456)},
				{Name: oidIfXTable + ".7.1", Type: gosnmp.Counter64, Value: uint64(70)},
				{Name: oidIfXTable + ".10.1", Type: gosnmp.Counter64, Value: uint64(654321)},
				{Name: oidIfXTable + ".11.1", Type: gosnmp.Counter64, Value: uint64(110)},
				{Name: oidIfXTable + ".15.1", Type: gosnmp.Gauge32, Value: uint32(1000)},
				{Name: oidIfXTable + ".18.1", Type: gosnmp.OctetString, Value: []byte("AP-Office")},
				{Name: oidIfXTable + ".1.25", Type: gosnmp.OctetString, Value: []byte("sfp25")},
				{Name: oidIfXTable + ".15.25", Type: gosnmp.Gauge32, Value: uint32(10000)},
			},
			oidBasePortIf: {
				{Name: oidBasePortIf + ".1", Type: gosnmp.Integer, Value: 1},
			},
			oidDot1qFdb: {
				// FDB 7, MAC de:ad:be:ef:00:01 -> bridge port 1
				{Name: oidDot1qFdb + ".7.222.173.190.239.0.1", Type: gosnmp.Integer, Value: 1},
			},
			oidVlanFdbID: {
				// VLAN 10 uses FDB 7
				{Name: oidVlanFdbID + ".0.10", Type: gosnmp.Gauge32, Value: uint32(7)},
			},
			oidLldpRem: {
				{Name: oidLldpRem + ".5.100.25.1", Type: gosnmp.OctetString, Value: []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x01}},
				{Name: oidLldpRem + ".7.100.25.1", Type: gosnmp.OctetString, Value: []byte("sfp0")},
				{Name: oidLldpRem + ".9.100.25.1", Type: gosnmp.OctetString, Value: []byte("core-fw")},
			},
			oidPethPort: {
				{Name: oidPethPort + ".3.1.1", Type: gosnmp.Integer, Value: 1},
				{Name: oidPethPort + ".6.1.1", Type: gosnmp.Integer, Value: 3},
			},
		},
	}

	drv := NewWithFactory(func(context.Context, driver.TargetConfig) (Walker, error) { return fw, nil })
	snap, err := drv.Poll(context.Background(), driver.TargetConfig{
		ID:              "sw-snmp",
		Driver:          "snmp",
		Endpoint:        "192.0.2.30:161",
		Settings:        map[string]any{"version": "2c"},
		ResolvedSecrets: map[string]string{"community": "public"},
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	snap.Normalize()
	if len(snap.Devices) != 1 || snap.Devices[0].ID != "snmp:sw-snmp" || snap.Devices[0].UptimeSec != 3600 || snap.Devices[0].PrimaryMAC != "aa:bb:cc:00:00:01" {
		t.Fatalf("unexpected device: %+v", snap.Devices)
	}
	if len(snap.Links) != 2 || snap.Links[0].SpeedMbps != 1000 || snap.Links[0].PoE == nil || snap.Links[0].PoE.State != "delivering" {
		t.Fatalf("unexpected links[0]: %+v", snap.Links)
	}
	// Both HC octet counters exist, so they replace the 32-bit ones; the rest come from ifTable.
	if c := snap.Links[0].Counters; c == nil || *c != (model.PortCounters{RxBytes: 123456, TxBytes: 654321, RxPackets: 70, TxPackets: 110, RxErrors: 3, TxErrors: 5, RxDropped: 2, TxDropped: 4}) {
		t.Fatalf("gi1 counters = %+v", c)
	}
	if !snap.Links[1].IsFiber || snap.Links[1].Media != model.MediaSFPPl {
		t.Fatalf("unexpected sfp link: %+v", snap.Links[1])
	}
	if len(snap.FDB) != 1 || snap.FDB[0].MAC != "de:ad:be:ef:00:01" || snap.FDB[0].VLAN != 10 || snap.FDB[0].LinkName != "gi1" {
		t.Fatalf("unexpected FDB: %+v", snap.FDB)
	}
	if len(snap.LLDP) != 1 || snap.LLDP[0].ChassisID != "00:11:22:33:44:01" || snap.LLDP[0].LocalLink != "sfp25" {
		t.Fatalf("unexpected LLDP: %+v", snap.LLDP)
	}
}

func TestSNMPV3RejectsUnknownProtocols(t *testing.T) {
	drv := New()
	base := func(s map[string]any) driver.TargetConfig {
		s["version"], s["username"] = "3", "netplane"
		return driver.TargetConfig{ID: "s3", Endpoint: "192.0.2.30", Settings: s,
			ResolvedSecrets: map[string]string{"auth_passphrase": "a", "priv_passphrase": "p"}}
	}
	if err := drv.ValidateConfig(base(map[string]any{"auth_protocol": "SHA-999"})); err == nil {
		t.Fatal("expected error for unknown auth_protocol")
	}
	if err := drv.ValidateConfig(base(map[string]any{"auth_protocol": "SHA", "priv_protocol": "3DES"})); err == nil {
		t.Fatal("expected error for unknown priv_protocol")
	}
	if err := drv.ValidateConfig(base(map[string]any{})); err != nil {
		t.Fatalf("noAuthNoPriv rejected: %v", err)
	}
}

func pollFixture(t *testing.T, trees map[string][]gosnmp.SnmpPDU) *driver.Snapshot {
	t.Helper()
	trees[".1.3.6.1.2.1.1"] = []gosnmp.SnmpPDU{{Name: oidSysName, Type: gosnmp.OctetString, Value: []byte("sw")}}
	drv := NewWithFactory(func(context.Context, driver.TargetConfig) (Walker, error) { return &fakeWalker{trees: trees}, nil })
	snap, err := drv.Poll(context.Background(), driver.TargetConfig{
		ID: "sw", Endpoint: "192.0.2.30", ResolvedSecrets: map[string]string{"community": "public"},
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	return snap
}

func TestSNMPCounter32FromIfTable(t *testing.T) {
	snap := pollFixture(t, map[string][]gosnmp.SnmpPDU{
		oidIfTable: {
			{Name: oidIfTable + ".2.1", Type: gosnmp.OctetString, Value: []byte("old1")},
			{Name: oidIfTable + ".10.1", Type: gosnmp.Counter32, Value: uint32(100)},
			{Name: oidIfTable + ".16.1", Type: gosnmp.Counter32, Value: uint32(200)},
			{Name: oidIfTable + ".2.2", Type: gosnmp.OctetString, Value: []byte("new2")},
			{Name: oidIfTable + ".10.2", Type: gosnmp.Counter32, Value: uint32(100)},
			{Name: oidIfTable + ".16.2", Type: gosnmp.Counter32, Value: uint32(200)},
		},
		oidIfXTable: {
			{Name: oidIfXTable + ".6.2", Type: gosnmp.Counter64, Value: uint64(1 << 40)},
			{Name: oidIfXTable + ".10.2", Type: gosnmp.Counter64, Value: uint64(1 << 41)},
		},
	})
	if len(snap.Links) != 2 {
		t.Fatalf("links: %+v", snap.Links)
	}
	if c := snap.Links[0].Counters; !c.Counter32 || c.RxBytes != 100 {
		t.Fatalf("ifTable-only counters: %+v", c)
	}
	if c := snap.Links[1].Counters; c.Counter32 || c.RxBytes != 1<<40 {
		t.Fatalf("HC counters: %+v", c)
	}
}

func TestSNMPLLDPLocalPortNumMapsViaLocPortTable(t *testing.T) {
	snap := pollFixture(t, map[string][]gosnmp.SnmpPDU{
		oidIfTable: {
			{Name: oidIfTable + ".2.10101", Type: gosnmp.OctetString, Value: []byte("GigabitEthernet1/0/1")},
			{Name: oidIfTable + ".2.1", Type: gosnmp.OctetString, Value: []byte("Vlan1")},
		},
		oidIfXTable: {
			{Name: oidIfXTable + ".1.10101", Type: gosnmp.OctetString, Value: []byte("Gi1/0/1")},
		},
		oidLldpLoc: {
			{Name: oidLldpLoc + ".3.1", Type: gosnmp.OctetString, Value: []byte("Gi1/0/1")},
			{Name: oidLldpLoc + ".4.1", Type: gosnmp.OctetString, Value: []byte("GigabitEthernet1/0/1")},
		},
		oidLldpRem: {
			{Name: oidLldpRem + ".9.0.1.1", Type: gosnmp.OctetString, Value: []byte("ap1")},
		},
	})
	if len(snap.LLDP) != 1 || snap.LLDP[0].LocalLink != "Gi1/0/1" {
		t.Fatalf("unexpected LLDP: %+v", snap.LLDP)
	}
}

func TestSNMPV3RejectsPrivWithoutAuth(t *testing.T) {
	err := New().ValidateConfig(driver.TargetConfig{
		ID: "s3", Endpoint: "192.0.2.30",
		Settings:        map[string]any{"version": "3", "username": "netplane", "priv_protocol": "AES"},
		ResolvedSecrets: map[string]string{"priv_passphrase": "p"},
	})
	if err == nil {
		t.Fatal("expected error for priv_protocol without auth_protocol")
	}
}

func TestSNMPRejectsInvalidExplicitPort(t *testing.T) {
	drv := New()
	for _, ep := range []string{"switch.example:65536", "switch.example:0", "switch.example:snmp"} {
		if err := drv.ValidateConfig(driver.TargetConfig{ID: "s", Endpoint: ep,
			ResolvedSecrets: map[string]string{"community": "public"}}); err == nil {
			t.Errorf("%s: expected invalid port error", ep)
		}
	}
	for _, ep := range []string{"switch.example", "switch.example:1161"} {
		if err := drv.ValidateConfig(driver.TargetConfig{ID: "s", Endpoint: ep,
			ResolvedSecrets: map[string]string{"community": "public"}}); err != nil {
			t.Errorf("%s: %v", ep, err)
		}
	}
}

func TestSNMPWalkerFactoryHonoursPollContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := driver.TargetConfig{ID: "s", Endpoint: "192.0.2.30", ResolvedSecrets: map[string]string{"community": "public"}}
	if w, err := defaultWalkerFactory(ctx, cfg); err == nil {
		_ = w.Close()
		t.Fatal("expected connect to fail with a cancelled context")
	}
}

func TestSNMPMixedCounterWidthsUse32BitBoth(t *testing.T) {
	snap := pollFixture(t, map[string][]gosnmp.SnmpPDU{
		oidIfTable: {
			{Name: oidIfTable + ".2.1", Type: gosnmp.OctetString, Value: []byte("gi1")},
			{Name: oidIfTable + ".10.1", Type: gosnmp.Counter32, Value: uint32(100)},
			{Name: oidIfTable + ".16.1", Type: gosnmp.Counter32, Value: uint32(200)},
		},
		oidIfXTable: {
			{Name: oidIfXTable + ".6.1", Type: gosnmp.Counter64, Value: uint64(1 << 40)},
		},
	})
	if c := snap.Links[0].Counters; !c.Counter32 || c.RxBytes != 100 || c.TxBytes != 200 {
		t.Fatalf("mixed-width counters: %+v", c)
	}
}

func TestSNMPFdbSkipsPortZeroAndSharedDatabases(t *testing.T) {
	snap := pollFixture(t, map[string][]gosnmp.SnmpPDU{
		oidIfTable: {{Name: oidIfTable + ".2.1", Type: gosnmp.OctetString, Value: []byte("gi1")}},
		oidDot1qFdb: {
			{Name: oidDot1qFdb + ".1.222.173.190.239.0.1", Type: gosnmp.Integer, Value: 1},
			{Name: oidDot1qFdb + ".1.222.173.190.239.0.2", Type: gosnmp.Integer, Value: 0},
		},
		oidVlanFdbID: {
			{Name: oidVlanFdbID + ".0.10", Type: gosnmp.Gauge32, Value: uint32(1)},
			{Name: oidVlanFdbID + ".0.20", Type: gosnmp.Gauge32, Value: uint32(1)},
		},
	})
	if len(snap.FDB) != 1 || snap.FDB[0].LinkName != "gi1" || snap.FDB[0].VLAN != 0 {
		t.Fatalf("unexpected FDB: %+v", snap.FDB)
	}
}

func TestSNMPPoEAmbiguousAcrossGroupsNotAttached(t *testing.T) {
	snap := pollFixture(t, map[string][]gosnmp.SnmpPDU{
		oidIfTable: {{Name: oidIfTable + ".2.1", Type: gosnmp.OctetString, Value: []byte("gi1")}},
		oidPethPort: {
			{Name: oidPethPort + ".3.1.1", Type: gosnmp.Integer, Value: 1},
			{Name: oidPethPort + ".3.2.1", Type: gosnmp.Integer, Value: 2},
		},
	})
	if snap.Links[0].PoE != nil {
		t.Fatalf("PoE from two PSE groups merged onto ifIndex 1: %+v", snap.Links[0].PoE)
	}
}

func TestSNMPFailedWalksMarkSections(t *testing.T) {
	poll := func(failOID string) (*driver.Snapshot, error) {
		fw := &fakeWalker{trees: map[string][]gosnmp.SnmpPDU{
			".1.3.6.1.2.1.1": {{Name: oidSysName, Type: gosnmp.OctetString, Value: []byte("sw")}},
		}, fail: map[string]bool{failOID: true}}
		drv := NewWithFactory(func(context.Context, driver.TargetConfig) (Walker, error) { return fw, nil })
		return drv.Poll(context.Background(), driver.TargetConfig{ID: "s1", Endpoint: "192.0.2.1", Settings: map[string]any{"version": "2c"}, ResolvedSecrets: map[string]string{"community": "public"}})
	}
	for oid, want := range map[string][]driver.Section{
		oidDot1qFdb: {driver.SectionFDB}, oidBasePortIf: {driver.SectionFDB}, oidVlanFdbID: {driver.SectionFDB},
		oidLldpRem: {driver.SectionLLDP}, oidLldpLoc: {driver.SectionLLDP},
	} {
		snap, err := poll(oid)
		if err != nil {
			t.Fatalf("%s: %v", oid, err)
		}
		if !slices.Equal(snap.Unavailable, want) {
			t.Errorf("failed walk %s: unavailable=%v, want %v", oid, snap.Unavailable, want)
		}
	}
	if _, err := poll(oidIfXTable); err == nil {
		t.Error("a failed ifXTable walk must fail the poll (it renames every port)")
	}
}

func TestGuessSnmpMedia(t *testing.T) {
	for _, tc := range []struct {
		name  string
		speed int
		want  model.MediaType
	}{
		{"tun0", 10, model.MediaOther}, {"eth0", 100, model.MediaFE}, {"Te1/0/5", 2500, model.Media25GE},
		{"TenGigabitEthernet1/0/2", 10000, model.MediaSFPPl}, {"tenant0", 10000, model.Media10GE}, {"Intent0", 2500, model.Media25GE},
		{"sfp1", 1000, model.MediaSFP},
		{"Hu1/0/49", 100000, model.MediaQSFP28}, {"FortyGigabitEthernet1/1/1", 40000, model.MediaQSFPPl},
		{"Twe1/0/1", 25000, model.MediaSFP28}, {"HundredGigE0/0/0/0", 100000, model.MediaQSFP28}, {"house0", 1000, model.MediaGE},
		{"TwentyFiveGigE1/0/1", 10000, model.MediaSFP28}, {"Hu1/0/50", 0, model.MediaQSFP28}, {"FourHundredGigE0/0/0/1", 400000, model.MediaQSFP28},
		{"hub0", 1000, model.MediaGE}, {"fortnight1", 1000, model.MediaGE}, {"Ethernet49/1", 100000, model.MediaQSFP28}, {"Po1", 30000, model.MediaOther}, {"Port-channel2", 40000, model.MediaOther}, {"ae0", 100000, model.MediaOther},
		{"Bundle-Ether1", 100000, model.MediaOther}, {"BE2", 40000, model.MediaOther}, {"bond0", 40000, model.MediaOther}, {"Trk1", 40000, model.MediaOther},
		{"Eth-Trunk1", 40000, model.MediaOther}, {"Bridge-Aggregation1", 40000, model.MediaOther}, {"team0", 40000, model.MediaOther}, {"lag 1", 40000, model.MediaOther},
		{"power0", 1000, model.MediaGE},
	} {
		if got := guessSnmpMedia(tc.name, 6, tc.speed); got != tc.want {
			t.Errorf("guessSnmpMedia(%q, %d) = %s, want %s", tc.name, tc.speed, got, tc.want)
		}
	}
	if got := guessSnmpMedia("uplink", ifTypeLag, 40000); got != model.MediaOther {
		t.Errorf("an ieee8023adLag interface = %s, want other", got)
	}
}
