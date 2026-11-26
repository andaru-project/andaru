package engine

import (
	"os"
	"testing"
)

// Only Andaru's own services are named on the services socket: root, or any other account, is refused there.
func TestServiceUserRefusesOtherAccounts(t *testing.T) {
	if name, err := serviceUser(uint32(os.Getuid())); err == nil {
		t.Fatalf("serviceUser(%d) = %q, want a refusal", os.Getuid(), name)
	}
}
