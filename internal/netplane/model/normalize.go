// SPDX-License-Identifier: AGPL-3.0-or-later
package model

import (
	"fmt"
	"math"
	"net"
	"strings"
	"time"
)

// Default SFP optical warning thresholds when a transceiver does not expose hardware flags.
const (
	DefaultSfpRxMinDbm = -20.0
	DefaultSfpRxMaxDbm = 0.0
	DefaultSfpTempMaxC = 70.0
)

// NormalizeMAC parses colon, hyphen, Cisco-dot, or bare 12-hex MAC strings
// and returns lowercase colon-separated "aa:bb:cc:dd:ee:ff".
func NormalizeMAC(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if len(s) == 12 && !strings.ContainsAny(s, ":-.") {
		s = fmt.Sprintf("%s:%s:%s:%s:%s:%s", s[0:2], s[2:4], s[4:6], s[6:8], s[8:10], s[10:12])
	}
	hw, err := net.ParseMAC(s)
	if err != nil || len(hw) != 6 {
		return "", fmt.Errorf("invalid MAC %q", raw)
	}
	return strings.ToLower(hw.String()), nil
}

// IsFiberMedia returns true for optical/SFP cage media types where copper-only
// features (such as EEE or copper auto-pause assumptions) must be guarded.
func IsFiberMedia(m MediaType) bool {
	switch MediaType(strings.ToUpper(strings.TrimSpace(string(m)))) {
	case MediaSFP, MediaSFPPl, MediaSFP28, MediaQSFPPl, MediaQSFP28:
		return true
	default:
		return false
	}
}

// SfpThresholds controls fallback optical health alerting.
type SfpThresholds struct {
	RxMinDbm float64
	RxMaxDbm float64
	TempMaxC float64
}

// DefaultSfpThresholds returns standard optical warning limits.
func DefaultSfpThresholds() SfpThresholds {
	return SfpThresholds{
		RxMinDbm: DefaultSfpRxMinDbm,
		RxMaxDbm: DefaultSfpRxMaxDbm,
		TempMaxC: DefaultSfpTempMaxC,
	}
}

// EvaluateSfpAlert computes AlertState from DOM measurements when hardware alert
// flags are absent or AlertUnknown.
func EvaluateSfpAlert(dom *SfpDom, th SfpThresholds) AlertState {
	if dom == nil || !dom.Present {
		return AlertOK
	}
	if dom.AlertState != "" && dom.AlertState != AlertUnknown {
		return dom.AlertState
	}
	state := AlertOK
	raise := func(a AlertState) {
		if a == AlertCrit || state == AlertOK {
			state = a
		}
	}
	if dom.RxPowerDbm != nil {
		rx := *dom.RxPowerDbm
		if rx < th.RxMinDbm-5.0 || rx > th.RxMaxDbm+3.0 {
			raise(AlertCrit)
		} else if rx < th.RxMinDbm || rx > th.RxMaxDbm {
			raise(AlertWarn)
		}
	}
	if dom.TempC != nil {
		t := *dom.TempC
		if t > th.TempMaxC+10.0 {
			raise(AlertCrit)
		} else if t > th.TempMaxC {
			raise(AlertWarn)
		}
	}
	return state
}

// ComputeBitrate computes bits-per-second from two cumulative byte counters separated
// by elapsed duration. Handles 32-bit counter wraparound (when counter32 is true)
// and device reboot resets cleanly, never returning negative or NaN values.
func ComputeBitrate(prevBytes, currBytes uint64, elapsed time.Duration, counter32 bool) float64 {
	secs := elapsed.Seconds()
	if secs <= 0 || math.IsNaN(secs) || math.IsInf(secs, 0) {
		return 0
	}
	var delta uint64
	if currBytes >= prevBytes {
		delta = currBytes - prevBytes
	} else if counter32 && prevBytes <= math.MaxUint32 && currBytes <= math.MaxUint32 {
		// 32-bit unsigned counter wraparound: (2^32 - prev) + curr
		delta = (uint64(math.MaxUint32) - prevBytes + 1) + currBytes
	} else {
		// Device reboot / counter reset: treat current counter as bytes since reset.
		delta = currBytes
	}
	return float64(delta) * 8 / secs
}

// NormalizeSlices ensures non-nil empty slices so wire JSON emits [] instead of null.
func (d *Device) NormalizeSlices() {
	if d.MACs == nil {
		d.MACs = []string{}
	}
	if d.Capabilities == nil {
		d.Capabilities = []Capability{}
	}
	if d.Sensors == nil {
		d.Sensors = []SensorReading{}
	}
}

// NormalizeSlices ensures non-nil slices on Link and sets IsFiber from Media.
func (l *Link) NormalizeSlices() {
	if l.TaggedVLANs == nil {
		l.TaggedVLANs = []int{}
	}
	if IsFiberMedia(l.Media) {
		l.IsFiber = true
	}
}

// NormalizeSlices ensures non-nil slices on LagGroup.
func (g *LagGroup) NormalizeSlices() {
	if g.MemberLinks == nil {
		g.MemberLinks = []string{}
	}
}

// NormalizeSlices ensures non-nil slices on Segment.
func (s *Segment) NormalizeSlices() {
	if s.Addresses == nil {
		s.Addresses = []IPAddress{}
	}
}

// NormalizeSlices ensures non-nil slices on Zone.
func (z *Zone) NormalizeSlices() {
	if z.SegmentIDs == nil {
		z.SegmentIDs = []string{}
	}
}

// NormalizeSlices ensures non-nil slices on FirewallState.
func (f *FirewallState) NormalizeSlices() {
	if f.Services == nil {
		f.Services = []ServiceStatus{}
	}
}

// NormalizeSlices ensures non-nil slices on WirelessRadio.
func (r *WirelessRadio) NormalizeSlices() {
	if r.SSIDs == nil {
		r.SSIDs = []Ssid{}
	}
}

// NormalizeSlices ensures non-nil slices on Client.
func (c *Client) NormalizeSlices() {
	if c.IPs == nil {
		c.IPs = []string{}
	}
}
