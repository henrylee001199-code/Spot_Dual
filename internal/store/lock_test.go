package store

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAcquireInstanceLockExclusive(t *testing.T) {
	root := t.TempDir()
	lock, err := AcquireInstanceLockWithOptions(root, LockOptions{})
	if err != nil {
		t.Fatalf("AcquireInstanceLockWithOptions() error = %v", err)
	}
	defer lock.Release()

	_, err = AcquireInstanceLockWithOptions(root, LockOptions{})
	if err == nil {
		t.Fatalf("second AcquireInstanceLockWithOptions() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "instance lock exists") {
		t.Fatalf("second AcquireInstanceLockWithOptions() error = %q, want lock exists", err.Error())
	}
}

func TestAcquireInstanceLockTakeoverDeadPID(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".instance.lock")
	if err := os.WriteFile(path, []byte("pid=999999\nstarted_at="+time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		t.Fatalf("write stale lock failed: %v", err)
	}

	lock, err := AcquireInstanceLockWithOptions(root, LockOptions{
		TakeoverEnabled: true,
		StaleAfter:      10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("AcquireInstanceLockWithOptions() error = %v, want nil", err)
	}
	defer lock.Release()
}

func TestAcquireInstanceLockDoesNotTakeoverRunningPID(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".instance.lock")
	if err := os.WriteFile(path, []byte("pid="+strconvI(os.Getpid())+"\nstarted_at="+time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)+"\n"), 0o644); err != nil {
		t.Fatalf("write active lock failed: %v", err)
	}

	_, err := AcquireInstanceLockWithOptions(root, LockOptions{
		TakeoverEnabled: true,
		StaleAfter:      time.Second,
	})
	if err == nil {
		t.Fatalf("AcquireInstanceLockWithOptions() error = nil, want active lock error")
	}
	if !strings.Contains(err.Error(), "owner_process_running") {
		t.Fatalf("AcquireInstanceLockWithOptions() error = %q, want owner_process_running", err.Error())
	}
}

func TestAcquireInstanceLockTakeoverByAgeWithoutPID(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".instance.lock")
	started := time.Now().UTC().Add(-2 * time.Minute)
	if err := os.WriteFile(path, []byte("started_at="+started.Format(time.RFC3339)+"\n"), 0o644); err != nil {
		t.Fatalf("write stale lock failed: %v", err)
	}

	lock, err := AcquireInstanceLockWithOptions(root, LockOptions{
		TakeoverEnabled: true,
		StaleAfter:      time.Minute,
		Now: func() time.Time {
			return started.Add(2 * time.Minute)
		},
	})
	if err != nil {
		t.Fatalf("AcquireInstanceLockWithOptions() error = %v, want nil", err)
	}
	defer lock.Release()
}

func TestAcquireInstanceLockKeepsRecentUnknownLock(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".instance.lock")
	started := time.Now().UTC()
	if err := os.WriteFile(path, []byte("started_at="+started.Format(time.RFC3339)+"\n"), 0o644); err != nil {
		t.Fatalf("write lock failed: %v", err)
	}

	_, err := AcquireInstanceLockWithOptions(root, LockOptions{
		TakeoverEnabled: true,
		StaleAfter:      10 * time.Minute,
		Now: func() time.Time {
			return started.Add(30 * time.Second)
		},
	})
	if err == nil {
		t.Fatalf("AcquireInstanceLockWithOptions() error = nil, want lock active error")
	}
	if !strings.Contains(err.Error(), "lock_not_stale") {
		t.Fatalf("AcquireInstanceLockWithOptions() error = %q, want lock_not_stale", err.Error())
	}
}

func strconvI(v int) string {
	return strconv.Itoa(v)
}
