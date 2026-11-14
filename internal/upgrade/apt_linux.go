package upgrade

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

const outputLimit = 1024 * 1024
const commandTimeout = 5 * time.Minute

var versionPattern = regexp.MustCompile(`^[0-9][A-Za-z0-9.+:~\-]*$`)
var architecturePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

type Availability struct {
	Installed    string    `json:"installed"`
	Candidate    string    `json:"candidate"`
	Architecture string    `json:"architecture"`
	Repository   string    `json:"repository"`
	Suite        string    `json:"suite"`
	ReleaseNotes string    `json:"releaseNotes"`
	RefreshedAt  time.Time `json:"refreshedAt"`
}

type packageInfo struct{ Version, Architecture string }
type commandRunner func(context.Context, string, ...string) ([]byte, error)

// Check refreshes only package indexes. Installation and package downloads remain explicit APT operations.
func Check(ctx context.Context) (Availability, error) {
	if os.Geteuid() != 0 {
		return Availability{}, errors.New("update checks require root")
	}
	return check(ctx, runnerAt(""), time.Now)
}

func check(ctx context.Context, run commandRunner, now func() time.Time) (Availability, error) {
	var result Availability
	if _, err := run(ctx, "apt-get", "-o", "APT::Update::Error-Mode=any", "update"); err != nil {
		return result, fmt.Errorf("refresh APT metadata: %w", err)
	}
	refreshed := now().UTC()
	info, err := installedPackage(ctx, run)
	if err != nil {
		return result, err
	}
	raw, err := run(ctx, "apt-cache", "policy", "andaru:"+info.Architecture)
	if err != nil {
		return result, fmt.Errorf("read APT policy: %w", err)
	}
	if err = textOutput(raw); err != nil {
		return result, err
	}
	candidate := ""
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "Candidate:"); ok {
			if found {
				return result, errors.New("duplicate candidate in APT policy")
			}
			found = true
			candidate = strings.TrimSpace(value)
			if candidate == "(none)" {
				candidate = ""
			} else if !versionPattern.MatchString(candidate) {
				return result, errors.New("invalid candidate in APT policy")
			}
		}
	}
	if !found {
		return result, errors.New("APT policy has no candidate field")
	}
	result = Availability{Installed: info.Version, Candidate: candidate, Architecture: info.Architecture,
		ReleaseNotes: "https://github.com/andaru-project/andaru/releases", RefreshedAt: refreshed}
	inCandidate := false
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "***" {
			fields = fields[1:]
		}
		if len(fields) == 2 && versionPattern.MatchString(fields[0]) {
			if _, err := strconv.Atoi(fields[1]); err == nil {
				inCandidate = fields[0] == candidate
				continue
			}
		}
		if inCandidate && len(fields) == 5 && fields[4] == "Packages" {
			if _, err := strconv.Atoi(fields[0]); err != nil {
				return Availability{}, errors.New("invalid source priority in APT policy")
			}
			u, err := url.Parse(fields[1])
			if err != nil || u.Scheme == "" {
				return Availability{}, errors.New("invalid repository in APT policy")
			}
			u.User = nil
			result.Repository = u.String()
			result.Suite = strings.SplitN(fields[2], "/", 2)[0]
			break
		}
	}
	return result, nil
}

func installedPackage(ctx context.Context, run commandRunner) (packageInfo, error) {
	raw, err := run(ctx, "dpkg-query", "-W", "-f=${Status}\t${Version}\t${Architecture}\n", "andaru")
	if err != nil {
		return packageInfo{}, fmt.Errorf("read installed Andaru package: %w", err)
	}
	if err = textOutput(raw); err != nil {
		return packageInfo{}, err
	}
	fields := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\t")
	if len(fields) != 3 || fields[0] != "install ok installed" || !versionPattern.MatchString(fields[1]) || !architecturePattern.MatchString(fields[2]) {
		return packageInfo{}, errors.New("Andaru must be an installed, configured Debian package; inspect dpkg --audit")
	}
	return packageInfo{Version: fields[1], Architecture: fields[2]}, nil
}

func textOutput(raw []byte) error {
	if len(raw) > outputLimit || !utf8.Valid(raw) {
		return errors.New("invalid or oversized package metadata")
	}
	for _, c := range string(raw) {
		if unicode.IsControl(c) && c != '\n' && c != '\t' {
			return errors.New("control character in package metadata")
		}
	}
	return nil
}

type commandError struct {
	name string
	code int
}

func (e *commandError) Error() string { return fmt.Sprintf("%s exited with status %d", e.name, e.code) }

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > outputLimit-b.Len() {
		return 0, errors.New("command output exceeds 1 MiB")
	}
	return b.Buffer.Write(p)
}

func runnerAt(dir string) commandRunner {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, commandTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if err == syscall.ESRCH {
				return os.ErrProcessDone
			}
			return err
		}
		cmd.WaitDelay = 2 * time.Second
		var out, diagnostics boundedOutput
		cmd.Stdout = &out
		cmd.Stderr = &diagnostics
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return nil, &commandError{name: name, code: exit.ExitCode()}
			}
			return nil, fmt.Errorf("run %s: %w", name, err)
		}
		return out.Bytes(), nil
	}
}
