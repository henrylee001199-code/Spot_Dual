package safety

import (
	"errors"
	"testing"
	"time"
)

func TestBreakerReconnectHalfOpenRecovery(t *testing.T) {
	b := NewBreaker(true, 5, 5, 2)
	b.SetReconnectRecovery(120*time.Millisecond, 1)

	if err := b.RecordReconnect(errors.New("dial failed 1")); err != nil {
		t.Fatalf("RecordReconnect(first) error = %v, want nil", err)
	}
	tripErr := b.RecordReconnect(errors.New("dial failed 2"))
	if !errors.Is(tripErr, ErrCircuitOpen) {
		t.Fatalf("RecordReconnect(second) error = %v, want ErrCircuitOpen", tripErr)
	}

	if err := b.AllowReconnect(); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("AllowReconnect() error = %v, want ErrCircuitOpen while cooling down", err)
	}
	if rem := b.ReconnectCooldownRemaining(); rem <= 0 {
		t.Fatalf("ReconnectCooldownRemaining() = %s, want > 0", rem)
	}

	time.Sleep(150 * time.Millisecond)
	if err := b.AllowReconnect(); err != nil {
		t.Fatalf("AllowReconnect(after cooldown) error = %v, want nil", err)
	}
	if err := b.RecordReconnect(nil); err != nil {
		t.Fatalf("RecordReconnect(success probe) error = %v, want nil", err)
	}
	if rem := b.ReconnectCooldownRemaining(); rem != 0 {
		t.Fatalf("ReconnectCooldownRemaining() = %s, want 0 after recovery", rem)
	}
}

func TestBreakerReconnectHalfOpenFailureReopens(t *testing.T) {
	b := NewBreaker(true, 5, 5, 1)
	b.SetReconnectRecovery(120*time.Millisecond, 1)

	tripErr := b.RecordReconnect(errors.New("dial failed"))
	if !errors.Is(tripErr, ErrCircuitOpen) {
		t.Fatalf("RecordReconnect(trip) error = %v, want ErrCircuitOpen", tripErr)
	}

	time.Sleep(150 * time.Millisecond)
	if err := b.AllowReconnect(); err != nil {
		t.Fatalf("AllowReconnect(after cooldown) error = %v, want nil", err)
	}
	tripErr = b.RecordReconnect(errors.New("probe failed"))
	if !errors.Is(tripErr, ErrCircuitOpen) {
		t.Fatalf("RecordReconnect(half-open failure) error = %v, want ErrCircuitOpen", tripErr)
	}

	if err := b.AllowReconnect(); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("AllowReconnect() error = %v, want ErrCircuitOpen after re-open", err)
	}
}
