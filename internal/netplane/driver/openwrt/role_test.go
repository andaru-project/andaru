// SPDX-License-Identifier: AGPL-3.0-or-later
package openwrt

import (
	"testing"

	"github.com/andaru-project/andaru/internal/netplane/model"
)

func TestGuessRole(t *testing.T) {
	for _, c := range []struct {
		radios, ifaces, devs int
		wan                  bool
		want                 model.DeviceRole
	}{
		{radios: 2, ifaces: 1, devs: 3, want: model.RoleAP},
		{ifaces: 3, devs: 8, want: model.RoleSwitch},
		{ifaces: 4, devs: 8, wan: true, want: model.RoleRouter}, // a wired router with a WAN port
		{radios: 2, ifaces: 4, devs: 8, wan: true, want: model.RoleRouter},
		{radios: 2, ifaces: 2, devs: 3, wan: true, want: model.RoleRouter}, // a travel router: wan + lan

	} {
		if got := guessRole(c.radios, c.ifaces, c.devs, c.wan); got != c.want {
			t.Errorf("guessRole(%+v) = %s, want %s", c, got, c.want)
		}
	}
}
