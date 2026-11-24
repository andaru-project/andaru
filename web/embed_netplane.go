// SPDX-License-Identifier: AGPL-3.0-or-later
//go:build embedui

package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist-netplane
var netplaneDistFS embed.FS

// NetplaneDistFS returns the built standalone Netplane SPA rooted at index.html.
func NetplaneDistFS() fs.FS {
	sub, err := fs.Sub(netplaneDistFS, "dist-netplane")
	if err != nil {
		panic(err)
	}
	return sub
}
