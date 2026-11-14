package upgrade

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

const installedFixture = "install ok installed\t0.4.0~rc2-1\tamd64\n"
const policyFixture = `andaru:
  Installed: 0.4.0~rc2-1
  Candidate: 0.4.0-1
  Version table:
     0.4.0-1 500
        500 https://apt.andaru.io trixie/main amd64 Packages
 *** 0.4.0~rc2-1 100
        100 /var/lib/dpkg/status
`

// An accidental install/download in check, or parsing the installed version as the candidate, must fail here.
func TestCheckRefreshesMetadataOnly(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name + " " + strings.Join(args, " ") {
		case "apt-get -o APT::Update::Error-Mode=any update":
			return nil, nil
		case "dpkg-query -W -f=${Status}\t${Version}\t${Architecture}\n andaru":
			return []byte(installedFixture), nil
		case "apt-cache policy andaru:amd64":
			return []byte(policyFixture), nil
		default:
			return nil, fmt.Errorf("unexpected command %s %q", name, args)
		}
	}
	got, err := check(context.Background(), run, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if got.Installed != "0.4.0~rc2-1" || got.Candidate != "0.4.0-1" || got.Architecture != "amd64" || got.Repository != "https://apt.andaru.io" || got.Suite != "trixie" || !got.RefreshedAt.Equal(now) {
		t.Fatalf("wrong availability: %+v", got)
	}
}

func TestCheckRefreshFailure(t *testing.T) {
	calls := 0
	_, err := check(context.Background(), func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return nil, errors.New("refresh failed")
	}, time.Now)
	if err == nil || calls != 1 {
		t.Fatalf("stale result after refresh failure: calls=%d error=%v", calls, err)
	}
}

func TestInstalledVersionMissing(t *testing.T) {
	for _, body := range []string{"", "unknown ok not-installed\t\t\n", "install ok unpacked\t0.4.0-1\tamd64\n"} {
		_, err := installedPackage(context.Background(), func(context.Context, string, ...string) ([]byte, error) { return []byte(body), nil })
		if err == nil {
			t.Fatalf("accepted absent/unconfigured package: %q", body)
		}
	}
}

func TestAptMetadataMalformed(t *testing.T) {
	for _, body := range []string{
		"andaru:\n Candidate: -oAnything\n",
		"andaru:\n Candidate: 0.4.0-1\n Candidate: 0.5.0-1\n",
		"andaru:\n Candidate: 0.4.0-1\x1b[0m\n",
		strings.Repeat("x", 1024*1024+1),
	} {
		_, err := check(context.Background(), metadataRunner(body), time.Now)
		if err == nil {
			t.Fatalf("accepted malformed policy (%d bytes)", len(body))
		}
	}
}

func TestCheckNoCandidate(t *testing.T) {
	got, err := check(context.Background(), metadataRunner("andaru:\n Installed: 0.4.0~rc2-1\n Candidate: (none)\n Version table:\n"), time.Now)
	if err != nil || got.Candidate != "" {
		t.Fatalf("no candidate: %+v %v", got, err)
	}
}

func metadataRunner(policy string) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "apt-get":
			return nil, nil
		case "dpkg-query":
			return []byte(installedFixture), nil
		case "apt-cache":
			return []byte(policy), nil
		default:
			return nil, fmt.Errorf("unexpected %s", name)
		}
	}
}
