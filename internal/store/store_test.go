package store

import (
	"testing"
	"time"
)

func TestStoreRuntimeStatusRoundTrip(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	started := time.Now().UTC().Add(-time.Minute)
	disc := time.Now().UTC().Add(-10 * time.Second)
	in := RuntimeStatus{
		Mode:              "testnet",
		Symbol:            "BTCUSDT",
		InstanceID:        "bot1",
		PID:               1234,
		State:             "degraded",
		StartedAt:         started,
		LastError:         "dial timeout",
		ReconnectAttempts: 2,
		DisconnectedAt:    &disc,
	}
	if err := s.SaveRuntimeStatus(in); err != nil {
		t.Fatalf("SaveRuntimeStatus() error = %v", err)
	}

	out, ok, err := s.LoadRuntimeStatus()
	if err != nil {
		t.Fatalf("LoadRuntimeStatus() error = %v", err)
	}
	if !ok {
		t.Fatalf("LoadRuntimeStatus() ok = false, want true")
	}
	if out.Mode != in.Mode || out.Symbol != in.Symbol || out.InstanceID != in.InstanceID {
		t.Fatalf("LoadRuntimeStatus() mismatch basic fields: got %+v want %+v", out, in)
	}
	if out.State != in.State || out.PID != in.PID || out.LastError != in.LastError || out.ReconnectAttempts != in.ReconnectAttempts {
		t.Fatalf("LoadRuntimeStatus() mismatch status fields: got %+v want %+v", out, in)
	}
	if out.StartedAt.IsZero() {
		t.Fatalf("started_at should be set")
	}
	if out.UpdatedAt.IsZero() {
		t.Fatalf("updated_at should be set")
	}
	if out.DisconnectedAt == nil {
		t.Fatalf("disconnected_at should be set")
	}
}

func TestStoreLoadRuntimeStatusNotExist(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, ok, err := s.LoadRuntimeStatus()
	if err != nil {
		t.Fatalf("LoadRuntimeStatus() error = %v", err)
	}
	if ok {
		t.Fatalf("LoadRuntimeStatus() ok = true, want false")
	}
}
