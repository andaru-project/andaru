// SPDX-License-Identifier: AGPL-3.0-or-later
package main

import "testing"

func TestIsLoopbackListen(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8099", "[::1]:8099", "localhost:8099"} {
		if !isLoopbackListen(addr) {
			t.Errorf("isLoopbackListen(%q) = false, want true", addr)
		}
	}
	for _, addr := range []string{":8099", "0.0.0.0:8099", "[::]:8099", "192.168.1.10:8099", "invalid"} {
		if isLoopbackListen(addr) {
			t.Errorf("isLoopbackListen(%q) = true, want false", addr)
		}
	}
}
