//go:build integration

package upgrade

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type prepareBench struct {
	env                  environment
	active               map[string]bool
	enabled              map[string]string
	calls                []string
	stop                 func(string) error
	show                 func(string)
	audit                string
	jobs, transientUnits string
}

func preparationBench(t *testing.T) *prepareBench {
	t.Helper()
	state := snapshotSource(t)
	root := recoveryRoot(t)
	b := &prepareBench{active: map[string]bool{}, enabled: map[string]string{}}
	for _, name := range []string{"andaru-engine.service", "andaru-leases.path", "andaru-prefixes.path", "andaru-prefixes.timer"} {
		b.active[name] = true
		b.enabled[name] = "enabled"
	}
	b.env = environment{stateDir: state, recoveryDir: root, now: func() time.Time { return time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC) }, bootID: "fixture-boot", pauseTimeout: 300 * time.Millisecond}
	b.env.commands = func(dir string) commandRunner { return b.run }
	if err := os.WriteFile(filepath.Join(state, "generations", "pointers.json"), []byte(`{"confirmed":null,"lastKnownGood":null,"pending":null,"live":null,"takeover":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	return b
}

func (b *prepareBench) run(_ context.Context, name string, args ...string) ([]byte, error) {
	b.calls = append(b.calls, name+" "+strings.Join(args, " "))
	switch name {
	case "dpkg":
		if len(args) == 1 && args[0] == "--audit" {
			return []byte(b.audit), nil
		}
	case "dpkg-query":
		if len(args) > 0 && args[len(args)-1] == "andaru" {
			return []byte(installedFixture), nil
		}
		return []byte("andaru\t0.4.0~rc2-1\tamd64\n"), nil
	case "apt-cache":
		return nil, nil
	case "systemctl":
		if len(args) < 2 {
			return nil, errors.New("missing unit")
		}
		unit := args[len(args)-1]
		switch args[0] {
		case "list-jobs":
			return []byte(b.jobs), nil
		case "list-units":
			return []byte(b.transientUnits), nil
		case "show":
			if b.show != nil {
				b.show(unit)
			}
			state := "inactive"
			if b.active[unit] {
				state = "active"
			}
			enabled := b.enabled[unit]
			if enabled == "" {
				enabled = "static"
			}
			return []byte("ActiveState=" + state + "\nUnitFileState=" + enabled + "\n"), nil
		case "stop":
			if !strings.HasPrefix(unit, "andaru-") {
				return nil, errors.New("stopped a network service")
			}
			b.active[unit] = false
			if b.stop != nil {
				return nil, b.stop(unit)
			}
			return nil, nil
		case "start":
			b.active[unit] = true
			return nil, nil
		case "reset-failed":
			return nil, nil
		}
	}
	return nil, fmt.Errorf("unexpected command: %s %q", name, args)
}

func TestPrepareMissingPackageReportsStateOnly(t *testing.T) {
	b := preparationBench(t)
	got, err := prepare(context.Background(), b.env)
	if err != nil {
		t.Fatal(err)
	}
	if got.PackageRetained || len(got.Warnings) == 0 || got.Version != "0.4.0~rc2-1" || !got.PreparedAt.Equal(b.env.now()) {
		t.Fatalf("misrepresented state-only result: %+v", got)
	}
	if _, err = os.Stat(filepath.Join(got.Directory, "state", "configuration", "secrets.db")); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareRefusesBrokenDpkgState(t *testing.T) {
	b := preparationBench(t)
	b.audit = "andaru is only unpacked\n"
	if _, err := prepare(context.Background(), b.env); err == nil {
		t.Fatal("accepted broken dpkg state")
	}
	if !b.active["andaru-engine.service"] {
		t.Fatal("stopped engine before refusing")
	}
}

func TestPrepareRejectsPendingApply(t *testing.T) {
	b := preparationBench(t)
	if err := os.WriteFile(filepath.Join(b.env.stateDir, "generations", "pointers.json"), []byte(`{"confirmed":null,"lastKnownGood":null,"pending":1,"live":null,"takeover":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepare(context.Background(), b.env); err == nil {
		t.Fatal("prepared during pending network apply")
	}
	if !b.active["andaru-engine.service"] {
		t.Fatal("stopped engine before refusing")
	}
}

func TestPrepareRechecksPendingUnderLocks(t *testing.T) {
	b := preparationBench(t)
	b.stop = func(unit string) error {
		if unit == "andaru-engine.service" {
			return os.WriteFile(filepath.Join(b.env.stateDir, "generations", "pointers.json"), []byte(`{"confirmed":null,"lastKnownGood":null,"pending":1,"live":null,"takeover":false}`), 0600)
		}
		return nil
	}
	if _, err := prepare(context.Background(), b.env); err == nil {
		t.Fatal("missed apply racing with shutdown")
	}
	for _, unit := range []string{"andaru-engine.service", "andaru-leases.path", "andaru-prefixes.path", "andaru-prefixes.timer"} {
		if !b.active[unit] {
			t.Fatalf("did not restore %s after refusal", unit)
		}
	}
	if _, err := os.Stat(filepath.Join(b.env.recoveryDir, "state-only.json")); !os.IsNotExist(err) {
		t.Fatal("published while apply was pending")
	}
}

func TestPrepareRestoresOnlyPausedUnits(t *testing.T) {
	b := preparationBench(t)
	b.active["andaru-prefixes.timer"] = false
	b.enabled["andaru-prefixes.timer"] = "disabled"
	if _, err := prepare(context.Background(), b.env); err != nil {
		t.Fatal(err)
	}
	if b.active["andaru-prefixes.timer"] {
		t.Fatal("started disabled timer")
	}
	if !b.active["andaru-engine.service"] || !b.active["andaru-leases.path"] {
		t.Fatal("left previous running units stopped")
	}
}

func TestPrepareLeavesNetworkServicesUntouched(t *testing.T) {
	b := preparationBench(t)
	if _, err := prepare(context.Background(), b.env); err != nil {
		t.Fatal(err)
	}
	for _, call := range b.calls {
		if strings.Contains(call, "networkctl") || strings.HasPrefix(call, "nft ") || strings.Contains(call, "rollback") || strings.Contains(call, "enable ") || strings.Contains(call, "mask ") {
			t.Fatalf("changed live network/service ownership: %s", call)
		}
		if strings.HasPrefix(call, "systemctl stop ") || strings.HasPrefix(call, "systemctl start ") {
			unit := strings.Fields(call)[2]
			if !strings.HasPrefix(unit, "andaru-") || unit == "andaru-network-boot.service" {
				t.Fatalf("changed network service: %s", call)
			}
		}
	}
}

func TestPrepareDrainsRealizer(t *testing.T) {
	b := preparationBench(t)
	b.active["andaru-prefixes.service"] = true
	probes := 0
	b.show = func(unit string) {
		if unit == "andaru-prefixes.service" {
			probes++
			if probes >= 3 {
				b.active[unit] = false
			}
		}
	}
	if _, err := prepare(context.Background(), b.env); err != nil {
		t.Fatal(err)
	}
	if probes < 3 {
		t.Fatal("copied state before existing writer drained")
	}
	for _, call := range b.calls {
		if call == "systemctl stop andaru-prefixes.service" {
			t.Fatal("killed realizer instead of draining")
		}
	}
}

func TestPrepareInterruptedMarker(t *testing.T) {
	b := preparationBench(t)
	if err := os.WriteFile(filepath.Join(b.env.recoveryDir, "preparing.json"), []byte(`{"formatVersion":1,"bootId":"old-boot","units":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepare(context.Background(), b.env); err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("forgot interrupted operation: %v", err)
	}
	for _, call := range b.calls {
		if strings.HasPrefix(call, "systemctl stop") || strings.HasPrefix(call, "systemctl start") {
			t.Fatal("silently resumed interrupted operation")
		}
	}
}

func TestPrepareLockHolder(t *testing.T) {
	path := os.Getenv("ANDARU_PREPARE_LOCK")
	if path == "" {
		t.Skip("subprocess lock fixture")
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	lock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	if err = syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lock); err != nil {
		t.Fatal(err)
	}
	fmt.Println("locked")
	var one [1]byte
	_, _ = os.Stdin.Read(one[:])
	f.Close()
}

func TestPrepareRefusesPackageActivity(t *testing.T) {
	b := preparationBench(t)
	path := filepath.Join(b.env.recoveryDir, "dpkg-lock")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestPrepareLockHolder$")
	child.Env = append(os.Environ(), "ANDARU_PREPARE_LOCK="+path)
	in, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close(); child.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("lock fixture: %q %v", line, err)
	}
	b.env.packageLocks = []string{path}
	if _, err = prepare(context.Background(), b.env); err == nil {
		t.Fatal("prepared while package operation held POSIX lock")
	}
}

func TestPrepareFailureRestoresUnitsAndRecovery(t *testing.T) {
	for _, kind := range []string{"stop", "copy", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			b := preparationBench(t)
			old := publishedSnapshot(t, b.env.recoveryDir, b.env.stateDir)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			b.stop = func(unit string) error {
				if unit != "andaru-engine.service" {
					return nil
				}
				switch kind {
				case "stop":
					return errors.New("stop interrupted after taking effect")
				case "copy":
					return os.WriteFile(filepath.Join(b.env.stateDir, "configuration", "secrets.db"), []byte("corrupt"), 0600)
				case "cancel":
					cancel()
				}
				return nil
			}
			if _, err := prepare(ctx, b.env); err == nil {
				t.Fatal("failed preparation became success")
			}
			for _, name := range preparationUnits {
				if !b.active[name] {
					t.Fatalf("left %s stopped", name)
				}
			}
			if _, err := os.Stat(old.Directory); err != nil {
				t.Fatal("failed preparation removed previous recovery")
			}
			if _, err := os.Stat(filepath.Join(b.env.recoveryDir, "preparing.json")); !os.IsNotExist(err) {
				t.Fatal("successfully restored units but left an interruption marker")
			}
		})
	}
}

func TestPrepareWaitsForWriterLocks(t *testing.T) {
	for _, relative := range []string{"engine.lock", "generations/lock"} {
		t.Run(relative, func(t *testing.T) {
			b := preparationBench(t)
			file, err := os.OpenFile(filepath.Join(b.env.stateDir, relative), os.O_RDWR|os.O_CREATE, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			b.env.pauseTimeout = 40 * time.Millisecond
			if _, err = prepare(context.Background(), b.env); err == nil {
				t.Fatal("copied state while writer held its lock")
			}
			for _, unit := range preparationUnits {
				if !b.active[unit] {
					t.Fatalf("lock timeout left %s stopped", unit)
				}
			}
			if _, err = os.Stat(filepath.Join(b.env.recoveryDir, "state-only.json")); !os.IsNotExist(err) {
				t.Fatal("published past a busy writer")
			}
		})
	}
}

func TestPrepareMarkerPrecedesFirstStop(t *testing.T) {
	b := preparationBench(t)
	stops := 0
	b.stop = func(string) error {
		stops++
		var record preparationRecord
		if err := privateJSON(filepath.Join(b.env.recoveryDir, "preparing.json"), &record); err != nil {
			return err
		}
		if record.BootID != "fixture-boot" || len(record.Units) != 4 || record.Version != "0.4.0~rc2-1" {
			return errors.New("incomplete durable stop intent")
		}
		return nil
	}
	if _, err := prepare(context.Background(), b.env); err != nil {
		t.Fatal(err)
	}
	if stops != 4 {
		t.Fatalf("stop sequence did not run: %d", stops)
	}
}

func TestPrepareReusesVerifiedLocalPackage(t *testing.T) {
	b := preparationBench(t)
	publishedSnapshot(t, b.env.recoveryDir, b.env.stateDir)
	result, err := prepare(context.Background(), b.env)
	if err != nil {
		t.Fatal(err)
	}
	if !result.PackageRetained {
		t.Fatal("lost a verified local recovery package when no longer indexed")
	}
}

func TestPrepareRefusesTamperedLocalPackage(t *testing.T) {
	b := preparationBench(t)
	old := publishedSnapshot(t, b.env.recoveryDir, b.env.stateDir)
	if err := os.WriteFile(filepath.Join(old.Directory, "package.deb"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepare(context.Background(), b.env); err == nil {
		t.Fatal("ignored a corrupt retained recovery package")
	}
}

func TestPrepareRestoresUnitsDespiteOwnStartBudget(t *testing.T) {
	b := preparationBench(t)
	b.active["andaru-prefixes.timer"] = false
	b.enabled["andaru-prefixes.timer"] = "disabled"
	budget := map[string]bool{}
	b.env.commands = func(string) commandRunner {
		return func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "systemctl" && len(args) == 2 {
				if args[0] == "reset-failed" {
					budget[args[1]] = true
				}
				if args[0] == "start" && !budget[args[1]] {
					return nil, errors.New("start-limit-hit")
				}
			}
			return b.run(ctx, name, args...)
		}
	}
	if _, err := prepare(context.Background(), b.env); err != nil {
		t.Fatalf("own pauses exhausted the systemd start budget: %v", err)
	}
	if !b.active["andaru-engine.service"] || !b.active["andaru-prefixes.path"] || !b.active["andaru-leases.path"] {
		t.Fatal("left control plane stopped")
	}
	if budget["andaru-prefixes.timer"] {
		t.Fatal("reset a unit this preparation never paused")
	}
}

func TestPrepareRefusesIndependentActivation(t *testing.T) {
	for _, kind := range []string{"queued", "running"} {
		t.Run(kind, func(t *testing.T) {
			b := preparationBench(t)
			b.stop = func(unit string) error {
				if unit == "andaru-engine.service" {
					if kind == "queued" {
						b.jobs = "42 andaru-realize-activate-2.service start waiting\n"
					} else {
						b.transientUnits = "andaru-realize-activate-2.service loaded activating start Andaru activation\n"
					}
				}
				return nil
			}
			if _, err := prepare(context.Background(), b.env); err == nil {
				t.Fatal("published recovery with independent activation still scheduled")
			}
			if _, err := os.Stat(filepath.Join(b.env.recoveryDir, "state-only.json")); !os.IsNotExist(err) {
				t.Fatal("published unsafe recovery")
			}
			if !b.active["andaru-engine.service"] {
				t.Fatal("did not restore engine after refusal")
			}
		})
	}
}

func TestPrepareRefusesOrphanApplyState(t *testing.T) {
	for _, state := range []string{"activating", "awaiting_confirmation", "rolling_back"} {
		t.Run(state, func(t *testing.T) {
			b := preparationBench(t)
			dir := filepath.Join(b.env.stateDir, "generations", "2")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"state":"`+state+`","previous":null}`), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := prepare(context.Background(), b.env); err == nil {
				t.Fatal("accepted orphan apply state")
			}
		})
	}
}
