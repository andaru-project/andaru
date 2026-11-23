// SPDX-License-Identifier: AGPL-3.0-or-later
package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNormalizeMAC(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"AA:BB:CC:DD:EE:FF", "aa:bb:cc:dd:ee:ff", false},
		{"aa-bb-cc-dd-ee-ff", "aa:bb:cc:dd:ee:ff", false},
		{"aabb.ccdd.eeff", "aa:bb:cc:dd:ee:ff", false},
		{"02510A000001", "02:51:0a:00:00:01", false},
		{"  02:51:0a:00:00:01  ", "02:51:0a:00:00:01", false},
		{"not-a-mac", "", true},
		{"02:51:0a:00:00", "", true},
		{"02:51:0a:00:00:01:02:03", "", true}, // EUI-64 rejected for 6-byte Ethernet MAC
		{"", "", true},
	}

	for _, tc := range tests {
		got, err := NormalizeMAC(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("NormalizeMAC(%q) err=%v, wantErr=%v", tc.in, err, tc.wantErr)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeMAC(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsFiberMedia(t *testing.T) {
	fiber := []MediaType{MediaSFP, MediaSFPPl, MediaSFP28, MediaQSFPPl, MediaQSFP28, "sfp+", "qsfp+"}
	for _, m := range fiber {
		if !IsFiberMedia(m) {
			t.Errorf("IsFiberMedia(%q) = false, want true", m)
		}
	}
	copper := []MediaType{MediaFE, MediaGE, Media25GE, Media5GE, Media10GE, Media25G, Media40G, Media100G, MediaOther, ""}
	for _, m := range copper {
		if IsFiberMedia(m) {
			t.Errorf("IsFiberMedia(%q) = true, want false", m)
		}
	}
}

func TestEvaluateSfpAlert(t *testing.T) {
	ptr := func(v float64) *float64 { return &v }
	th := DefaultSfpThresholds()

	tests := []struct {
		name string
		dom  *SfpDom
		want AlertState
	}{
		{"nil", nil, AlertOK},
		{"absent", &SfpDom{Present: false}, AlertOK},
		{"healthy", &SfpDom{Present: true, RxPowerDbm: ptr(-5.2), TempC: ptr(42.0)}, AlertOK},
		{"low_rx_warn", &SfpDom{Present: true, RxPowerDbm: ptr(-21.5), TempC: ptr(40.0)}, AlertWarn},
		{"low_rx_crit", &SfpDom{Present: true, RxPowerDbm: ptr(-26.0), TempC: ptr(40.0)}, AlertCrit},
		{"high_rx_warn", &SfpDom{Present: true, RxPowerDbm: ptr(1.5), TempC: ptr(40.0)}, AlertWarn},
		{"high_rx_crit", &SfpDom{Present: true, RxPowerDbm: ptr(4.0), TempC: ptr(40.0)}, AlertCrit},
		{"high_temp_warn", &SfpDom{Present: true, RxPowerDbm: ptr(-4.0), TempC: ptr(72.0)}, AlertWarn},
		{"high_temp_crit", &SfpDom{Present: true, RxPowerDbm: ptr(-4.0), TempC: ptr(82.0)}, AlertCrit},
		{"hardware_flag_preserved", &SfpDom{Present: true, RxPowerDbm: ptr(-4.0), AlertState: AlertCrit}, AlertCrit},
		{"hardware_ok_preserved", &SfpDom{Present: true, RxPowerDbm: ptr(-22.0), AlertState: AlertOK}, AlertOK},
		{"unknown_falls_back", &SfpDom{Present: true, RxPowerDbm: ptr(-22.0), AlertState: AlertUnknown}, AlertWarn},
		{"rx_warn_temp_crit", &SfpDom{Present: true, RxPowerDbm: ptr(-22.0), TempC: ptr(85.0)}, AlertCrit},
		{"rx_warn_temp_warn", &SfpDom{Present: true, RxPowerDbm: ptr(-22.0), TempC: ptr(72.0)}, AlertWarn},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := EvaluateSfpAlert(tc.dom, th); got != tc.want {
				t.Errorf("EvaluateSfpAlert() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestComputeBitrate(t *testing.T) {
	// Normal increment: 1000 bytes over 2 seconds = 4000 bps
	if got := ComputeBitrate(1000, 2000, 2*time.Second, false); got != 4000 {
		t.Fatalf("normal delta = %v, want 4000", got)
	}
	// Zero / negative duration returns 0
	if got := ComputeBitrate(1000, 2000, 0, false); got != 0 {
		t.Fatalf("zero elapsed = %v, want 0", got)
	}
	// 32-bit wrap: 0xFFFF_FF00 -> 0x0000_0100 = 512 bytes over 1s = 4096 bps
	if got := ComputeBitrate(0xFFFFFF00, 0x00000100, time.Second, true); got != 4096 {
		t.Fatalf("32-bit wrap = %v, want 4096", got)
	}
	// Reboot/reset on 64-bit counter: prev > curr -> uses currBytes (500 bytes / 1s = 4000 bps)
	if got := ComputeBitrate(1_000_000_000_000, 500, time.Second, false); got != 4000 {
		t.Fatalf("reboot reset = %v, want 4000", got)
	}
}

func TestNormalizeSlicesAndCapabilities(t *testing.T) {
	d := Device{ID: "openwrt:02:51:0a:00:00:01", Driver: "openwrt", TargetID: "t1", Name: "LG510S", Role: RoleSwitch}
	d.NormalizeSlices()
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal device: %v", err)
	}
	s := string(raw)
	for _, field := range []string{`"macs":[]`, `"capabilities":[]`, `"sensors":[]`} {
		if !strings.Contains(s, field) {
			t.Errorf("expected %s in JSON %s", field, s)
		}
	}
	d.Capabilities = []Capability{CapLinks, CapSfpDom}
	if !d.HasCapability(CapSfpDom) || d.HasCapability(CapPoE) {
		t.Fatalf("unexpected HasCapability results")
	}

	l := Link{DeviceID: d.ID, IfName: "sfp1", Media: MediaSFPPl}
	l.NormalizeSlices()
	if !l.IsFiber {
		t.Fatalf("expected IsFiber=true for SFP+")
	}
	lraw, _ := json.Marshal(l)
	if !strings.Contains(string(lraw), `"tagged_vlans":[]`) {
		t.Errorf("expected empty tagged_vlans array, got %s", lraw)
	}

	g := LagGroup{ID: "lag1"}
	g.NormalizeSlices()
	seg := Segment{ID: "seg1"}
	seg.NormalizeSlices()
	z := Zone{ID: "z1"}
	z.NormalizeSlices()
	fw := FirewallState{DeviceID: d.ID}
	fw.NormalizeSlices()
	r := WirelessRadio{DeviceID: d.ID}
	r.NormalizeSlices()
	c := Client{MAC: "aa:bb:cc:dd:ee:ff"}
	c.NormalizeSlices()
	if g.MemberLinks == nil || seg.Addresses == nil || z.SegmentIDs == nil || fw.Services == nil || r.SSIDs == nil || c.IPs == nil {
		t.Fatalf("expected all normalized slices to be non-nil")
	}
}

func TestMediaForSpeed(t *testing.T) {
	for _, tc := range []struct {
		speed     int
		pluggable bool
		want      MediaType
	}{
		{0, false, MediaOther}, {10, false, MediaFE}, {100, false, MediaFE}, {1000, false, MediaGE},
		{2500, false, Media25GE}, {5000, false, Media5GE}, {10000, false, Media10GE},
		{0, true, MediaSFP}, {1000, true, MediaSFP}, {2500, true, MediaSFPPl}, {10000, true, MediaSFPPl},
		{25000, true, MediaSFP28}, {40000, true, MediaQSFPPl}, {50000, true, MediaQSFP28}, {100000, true, MediaQSFP28}, {400000, true, MediaQSFP28},
		{25000, false, MediaSFP28}, {40000, false, MediaQSFPPl}, {100000, false, MediaQSFP28},
		{20000, false, MediaOther}, {30000, false, MediaOther}, // sums of aggregated links
		{56000, true, MediaQSFP28}, {20000, true, MediaSFPPl}, {30000, true, MediaSFP28},
	} {
		if got := MediaForSpeed(tc.speed, tc.pluggable); got != tc.want {
			t.Errorf("MediaForSpeed(%d, %v) = %s, want %s", tc.speed, tc.pluggable, got, tc.want)
		}
	}
}
