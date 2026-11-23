// SPDX-License-Identifier: AGPL-3.0-or-later
package driver

import (
	"slices"
	"testing"
)

func TestCollectMACs(t *testing.T) {
	macs, primary := CollectMACs([]string{"", "00:00:00:00:00:00", "AA-BB-CC-DD-EE-FF", "bogus", "aabbccddeeff", "11:22:33:44:55:66"})
	if want := []string{"aa:bb:cc:dd:ee:ff", "11:22:33:44:55:66"}; !slices.Equal(macs, want) || primary != want[0] {
		t.Fatalf("got %v %q, want %v %q", macs, primary, want, want[0])
	}
	if macs, primary := CollectMACs(nil); macs == nil || len(macs) != 0 || primary != "" {
		t.Fatalf("empty: got %#v %q", macs, primary)
	}
}
