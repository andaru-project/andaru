package generation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func rootGenerationDir(t *testing.T) string {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Fatal("generation observation tests require root")
	}
	root, err := os.MkdirTemp("/var/tmp", "andaru-observe-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return filepath.Join(root, "generations")
}

func TestObserveInitializesAndCapturesBaselineWithoutPointers(t *testing.T) {
	dir := rootGenerationDir(t)
	called := false
	if err := Observe(context.Background(), dir, func(observation Observation) error {
		called = true
		if observation.Live != nil || observation.Pending != nil || observation.LiveSavedVersion != "" {
			t.Fatalf("baseline observation = %+v", observation)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("capture was not invoked")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "lock" {
		t.Fatalf("observation wrote generation state: %v", entries)
	}
}

func TestObserveHoldsLockAcrossCapture(t *testing.T) {
	dir := rootGenerationDir(t)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Observe(context.Background(), dir, func(Observation) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	file, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
		file.Close()
		t.Fatalf("capture did not hold generation lock: %v", err)
	}
	file.Close()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestObserveCancellationReleasesLock(t *testing.T) {
	dir := rootGenerationDir(t)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := Observe(ctx, dir, func(Observation) error { t.Fatal("capture ran while lock held"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled observation = %v", err)
	}
	file.Close()
	if err := Observe(context.Background(), dir, func(Observation) error { return nil }); err != nil {
		t.Fatalf("lock remained held after cancellation: %v", err)
	}
}

func TestObserveRejectsMalformedPointerWithoutCapture(t *testing.T) {
	dir := rootGenerationDir(t)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pointers.json"), []byte(`{"live":7}`), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := Observe(context.Background(), dir, func(Observation) error { called = true; return nil }); err == nil || called {
		t.Fatalf("malformed existing state accepted: err=%v called=%v", err, called)
	}
}
