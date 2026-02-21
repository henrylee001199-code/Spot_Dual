package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"spot-dual/internal/core"
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

func TestStoreOpenOrdersSnapshotRoundTripUsesGridSnapshotID(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	grid := GridState{
		Symbol:     "BTCUSDT",
		SnapshotID: "snap-A",
		Anchor:     decimal.NewFromInt(100),
	}
	if err := s.SaveGridState(grid); err != nil {
		t.Fatalf("SaveGridState() error = %v", err)
	}
	orders := []core.Order{
		{
			ID:     "o-1",
			Symbol: "BTCUSDT",
			Side:   core.Buy,
			Type:   core.Limit,
			Price:  decimal.NewFromInt(99),
			Qty:    decimal.NewFromInt(1),
		},
	}
	if err := s.SaveOpenOrders(orders); err != nil {
		t.Fatalf("SaveOpenOrders() error = %v", err)
	}

	snapshot, ok, err := s.LoadOpenOrdersSnapshot()
	if err != nil {
		t.Fatalf("LoadOpenOrdersSnapshot() error = %v", err)
	}
	if !ok {
		t.Fatalf("LoadOpenOrdersSnapshot() ok = false, want true")
	}
	if snapshot.SnapshotID != "snap-A" {
		t.Fatalf("snapshot_id = %q, want %q", snapshot.SnapshotID, "snap-A")
	}
	if len(snapshot.Orders) != 1 || snapshot.Orders[0].ID != "o-1" {
		t.Fatalf("snapshot orders mismatch: %+v", snapshot.Orders)
	}
}

func TestStoreLoadOpenOrdersSnapshotRejectsLegacyArray(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	legacy := []core.Order{
		{
			ID:     "legacy-1",
			Symbol: "BTCUSDT",
			Side:   core.Sell,
			Type:   core.Limit,
			Price:  decimal.NewFromInt(101),
			Qty:    decimal.NewFromInt(2),
		},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(s.root, "open_orders.json"), data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, ok, err := s.LoadOpenOrdersSnapshot()
	if err == nil {
		t.Fatalf("LoadOpenOrdersSnapshot() error = nil, want non-nil")
	}
	if ok {
		t.Fatalf("LoadOpenOrdersSnapshot() ok = true, want false")
	}
}

func TestStoreTradeLedgerRoundTripAcrossRestart(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	key := "order:123|trade:456"
	ok, err := s.HasTradeLedgerKey(key)
	if err != nil {
		t.Fatalf("HasTradeLedgerKey() before record error = %v", err)
	}
	if ok {
		t.Fatalf("HasTradeLedgerKey() before record = true, want false")
	}

	now := time.Now().UTC()
	if err := s.RecordTradeLedgerKey(key, now); err != nil {
		t.Fatalf("RecordTradeLedgerKey() error = %v", err)
	}
	ok, err = s.HasTradeLedgerKey(key)
	if err != nil {
		t.Fatalf("HasTradeLedgerKey() after record error = %v", err)
	}
	if !ok {
		t.Fatalf("HasTradeLedgerKey() after record = false, want true")
	}

	s2, err := New(root)
	if err != nil {
		t.Fatalf("New(second) error = %v", err)
	}
	ok, err = s2.HasTradeLedgerKey(key)
	if err != nil {
		t.Fatalf("HasTradeLedgerKey() after restart error = %v", err)
	}
	if !ok {
		t.Fatalf("HasTradeLedgerKey() after restart = false, want true")
	}
}

func TestStoreTradeLedgerTrimByCount(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	path := filepath.Join(root, "trade_ledger.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(trade_ledger.jsonl) error = %v", err)
	}
	enc := json.NewEncoder(f)
	base := time.Now().UTC().Add(-2 * time.Hour)
	for i := 0; i <= tradeLedgerMaxEntries; i++ {
		entry := TradeLedgerEntry{
			Key:    fmt.Sprintf("order:%d|trade:%d", i, i),
			SeenAt: base.Add(time.Duration(i) * time.Second),
		}
		if err := enc.Encode(entry); err != nil {
			_ = f.Close()
			t.Fatalf("Encode() error = %v", err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close(trade_ledger.jsonl) error = %v", err)
	}

	newKey := "order:new|trade:new"
	if err := s.RecordTradeLedgerKey(newKey, time.Now().UTC()); err != nil {
		t.Fatalf("RecordTradeLedgerKey(new) error = %v", err)
	}
	wantEntries := tradeLedgerTrimToEntries + 1
	if len(s.tradeLedgerEntries) != wantEntries {
		t.Fatalf("tradeLedgerEntries len = %d, want %d", len(s.tradeLedgerEntries), wantEntries)
	}

	ok, err := s.HasTradeLedgerKey("order:0|trade:0")
	if err != nil {
		t.Fatalf("HasTradeLedgerKey(oldest) error = %v", err)
	}
	if ok {
		t.Fatalf("HasTradeLedgerKey(oldest) = true, want false after trim")
	}
	ok, err = s.HasTradeLedgerKey(newKey)
	if err != nil {
		t.Fatalf("HasTradeLedgerKey(new) error = %v", err)
	}
	if !ok {
		t.Fatalf("HasTradeLedgerKey(new) = false, want true")
	}

	s2, err := New(root)
	if err != nil {
		t.Fatalf("New(second) error = %v", err)
	}
	ok, err = s2.HasTradeLedgerKey("order:0|trade:0")
	if err != nil {
		t.Fatalf("HasTradeLedgerKey(oldest, restart) error = %v", err)
	}
	if ok {
		t.Fatalf("HasTradeLedgerKey(oldest, restart) = true, want false")
	}
	ok, err = s2.HasTradeLedgerKey(newKey)
	if err != nil {
		t.Fatalf("HasTradeLedgerKey(new, restart) error = %v", err)
	}
	if !ok {
		t.Fatalf("HasTradeLedgerKey(new, restart) = false, want true")
	}
	if len(s2.tradeLedgerEntries) != wantEntries {
		t.Fatalf("tradeLedgerEntries len after restart = %d, want %d", len(s2.tradeLedgerEntries), wantEntries)
	}
}
