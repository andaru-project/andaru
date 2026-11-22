package engine

import (
	"testing"

	"github.com/andaru-project/andaru/internal/config"
)

// The store records saved versions as written by this build, whichever command of the binary opens it.
func TestStoreWritesAsThisBuild(t *testing.T) {
	if config.Writer != buildVersion {
		t.Fatalf("the store writes as %q, the build is %q", config.Writer, buildVersion)
	}
}
