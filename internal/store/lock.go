package store

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type InstanceLock struct {
	path string
	file *os.File
}

type LockOptions struct {
	TakeoverEnabled bool
	StaleAfter      time.Duration
	Now             func() time.Time
}

func AcquireInstanceLock(root string) (*InstanceLock, error) {
	return AcquireInstanceLockWithOptions(root, LockOptions{})
}

func AcquireInstanceLockWithOptions(root string, opts LockOptions) (*InstanceLock, error) {
	if root == "" {
		return nil, fmt.Errorf("state dir required")
	}
	path := filepath.Join(root, ".instance.lock")
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}

	for attempts := 0; attempts < 3; attempts++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			if writeErr := writeLockFile(f, nowFn().UTC()); writeErr != nil {
				_ = f.Close()
				_ = os.Remove(path)
				return nil, writeErr
			}
			return &InstanceLock{path: path, file: f}, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if !opts.TakeoverEnabled {
			return nil, fmt.Errorf("instance lock exists: %s", path)
		}
		stale, reason, staleErr := shouldTakeoverLock(path, nowFn().UTC(), opts.StaleAfter)
		if staleErr != nil {
			return nil, fmt.Errorf("instance lock exists: %s (stale check failed: %v)", path, staleErr)
		}
		if !stale {
			return nil, fmt.Errorf("instance lock exists: %s (%s)", path, reason)
		}
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return nil, removeErr
		}
	}
	return nil, fmt.Errorf("instance lock exists: %s", path)
}

func writeLockFile(f *os.File, now time.Time) error {
	if f == nil {
		return errors.New("lock file is nil")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	payload := "pid=" + strconv.Itoa(os.Getpid()) + "\nstarted_at=" + now.UTC().Format(time.RFC3339) + "\n"
	if _, err := f.WriteString(payload); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return nil
}

type lockMeta struct {
	pid       int
	startedAt time.Time
}

func shouldTakeoverLock(path string, now time.Time, staleAfter time.Duration) (bool, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return true, "lock_disappeared", nil
		}
		return false, "", err
	}
	meta, err := parseLockMeta(data)
	if err != nil {
		return false, "", err
	}

	if meta.pid > 0 {
		alive, err := isProcessAlive(meta.pid)
		if err != nil {
			return false, "", err
		}
		if alive {
			return false, "owner_process_running", nil
		}
		return true, "owner_process_not_running", nil
	}

	if staleAfter > 0 && !meta.startedAt.IsZero() && now.UTC().Sub(meta.startedAt.UTC()) >= staleAfter {
		return true, "lock_age_exceeded", nil
	}
	if meta.startedAt.IsZero() {
		return false, "missing_lock_owner_info", nil
	}
	return false, "lock_not_stale", nil
}

func parseLockMeta(data []byte) (lockMeta, error) {
	meta := lockMeta{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		switch key {
		case "pid":
			if pid, err := strconv.Atoi(value); err == nil && pid > 0 {
				meta.pid = pid
			}
		case "started_at":
			if ts, err := time.Parse(time.RFC3339, value); err == nil {
				meta.startedAt = ts.UTC()
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return lockMeta{}, err
	}
	return meta, nil
}

func isProcessAlive(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false, err
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrProcessDone) {
		return false, nil
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "no such process"),
		strings.Contains(msg, "process already finished"),
		strings.Contains(msg, "not found"):
		return false, nil
	case strings.Contains(msg, "operation not permitted"),
		strings.Contains(msg, "permission denied"),
		strings.Contains(msg, "access is denied"):
		return true, nil
	default:
		return false, nil
	}
}

func (l *InstanceLock) Release() error {
	if l == nil {
		return nil
	}
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
	if l.path == "" {
		return nil
	}
	err := os.Remove(l.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	l.path = ""
	return nil
}
