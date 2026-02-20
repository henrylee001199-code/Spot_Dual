package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"grid-trading/internal/core"
)

type GridState struct {
	Strategy           string          `json:"strategy"`
	Symbol             string          `json:"symbol"`
	SnapshotID         string          `json:"snapshot_id,omitempty"`
	Anchor             decimal.Decimal `json:"anchor"`
	Low                decimal.Decimal `json:"low"`
	StopPrice          decimal.Decimal `json:"stop_price"`
	Ratio              decimal.Decimal `json:"ratio"`
	BaseRatio          decimal.Decimal `json:"base_ratio,omitempty"`
	SellRatio          decimal.Decimal `json:"sell_ratio,omitempty"`
	Levels             int             `json:"levels"`
	MinLevel           int             `json:"min_level"`
	MaxLevel           int             `json:"max_level"`
	Qty                decimal.Decimal `json:"qty"`
	MinQtyMultiple     int64           `json:"min_qty_multiple"`
	Rules              core.Rules      `json:"rules"`
	Initialized        bool            `json:"initialized"`
	Stopped            bool            `json:"stopped"`
	LastDownShiftPrice decimal.Decimal `json:"last_down_shift_price,omitempty"`
	LastDownShiftAt    time.Time       `json:"last_down_shift_at,omitempty"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

type OpenOrdersSnapshot struct {
	SnapshotID string       `json:"snapshot_id,omitempty"`
	Orders     []core.Order `json:"orders"`
	UpdatedAt  time.Time    `json:"updated_at,omitempty"`
}

type TradeLedgerEntry struct {
	Key    string    `json:"key"`
	SeenAt time.Time `json:"seen_at"`
}

type RuntimeStatus struct {
	Mode              string     `json:"mode"`
	Symbol            string     `json:"symbol"`
	InstanceID        string     `json:"instance_id"`
	PID               int        `json:"pid"`
	State             string     `json:"state"`
	StartedAt         time.Time  `json:"started_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	LastError         string     `json:"last_error,omitempty"`
	ReconnectAttempts int        `json:"reconnect_attempts,omitempty"`
	DisconnectedAt    *time.Time `json:"disconnected_at,omitempty"`
}

type Persister interface {
	SaveGridState(state GridState) error
	SaveOpenOrders(orders []core.Order) error
	AppendTrade(trade core.Trade) error
}

type Store struct {
	root               string
	mu                 sync.Mutex
	pendingSnapshotID  string
	tradeLedgerLoaded  bool
	tradeLedger        map[string]struct{}
	tradeLedgerEntries []TradeLedgerEntry
}

const (
	tradeLedgerMaxEntries    = 10000
	tradeLedgerTrimToEntries = 8000
)

func New(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("state dir required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

func (s *Store) SaveGridState(state GridState) error {
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	}
	state.SnapshotID = strings.TrimSpace(state.SnapshotID)
	if state.SnapshotID == "" {
		state.SnapshotID = newSnapshotID(state.UpdatedAt)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeJSONAtomic(s.statePath(), state); err != nil {
		return err
	}
	s.pendingSnapshotID = state.SnapshotID
	return nil
}

func (s *Store) LoadGridState() (GridState, bool, error) {
	data, err := os.ReadFile(s.statePath())
	if err != nil {
		if os.IsNotExist(err) {
			return GridState{}, false, nil
		}
		return GridState{}, false, err
	}
	var state GridState
	if err := json.Unmarshal(data, &state); err != nil {
		return GridState{}, false, err
	}
	return state, true, nil
}

func (s *Store) SaveOpenOrders(orders []core.Order) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	snapshotID := strings.TrimSpace(s.pendingSnapshotID)
	payload := OpenOrdersSnapshot{
		SnapshotID: snapshotID,
		Orders:     orders,
		UpdatedAt:  now,
	}
	if payload.Orders == nil {
		payload.Orders = make([]core.Order, 0)
	}
	if err := writeJSONAtomic(s.ordersPath(), payload); err != nil {
		return err
	}
	s.pendingSnapshotID = ""
	return nil
}

func (s *Store) LoadOpenOrders() ([]core.Order, bool, error) {
	snapshot, ok, err := s.LoadOpenOrdersSnapshot()
	if err != nil || !ok {
		return nil, ok, err
	}
	return snapshot.Orders, true, nil
}

func (s *Store) LoadOpenOrdersSnapshot() (OpenOrdersSnapshot, bool, error) {
	data, err := os.ReadFile(s.ordersPath())
	if err != nil {
		if os.IsNotExist(err) {
			return OpenOrdersSnapshot{}, false, nil
		}
		return OpenOrdersSnapshot{}, false, err
	}
	snapshot, err := decodeOpenOrdersSnapshot(data)
	if err != nil {
		return OpenOrdersSnapshot{}, false, err
	}
	return snapshot, true, nil
}

func (s *Store) SaveRuntimeStatus(status RuntimeStatus) error {
	if status.UpdatedAt.IsZero() {
		status.UpdatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONAtomic(s.runtimeStatusPath(), status)
}

func (s *Store) LoadRuntimeStatus() (RuntimeStatus, bool, error) {
	data, err := os.ReadFile(s.runtimeStatusPath())
	if err != nil {
		if os.IsNotExist(err) {
			return RuntimeStatus{}, false, nil
		}
		return RuntimeStatus{}, false, err
	}
	var status RuntimeStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return RuntimeStatus{}, false, err
	}
	return status, true, nil
}

func (s *Store) AppendTrade(trade core.Trade) error {
	if trade.Time.IsZero() {
		trade.Time = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Join(s.root, "trades")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	date := trade.Time.UTC().Format("2006-01-02")
	path := filepath.Join(dir, date+".jsonl")
	data, err := json.Marshal(trade)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

func (s *Store) HasTradeLedgerKey(key string) (bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadTradeLedgerLocked(); err != nil {
		return false, err
	}
	_, ok := s.tradeLedger[key]
	return ok, nil
}

func (s *Store) RecordTradeLedgerKey(key string, seenAt time.Time) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	if seenAt.IsZero() {
		seenAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadTradeLedgerLocked(); err != nil {
		return err
	}
	if _, ok := s.tradeLedger[key]; ok {
		return nil
	}

	entry := TradeLedgerEntry{
		Key:    key,
		SeenAt: seenAt.UTC(),
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	path := s.tradeLedgerPath()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	s.tradeLedger[key] = struct{}{}
	s.tradeLedgerEntries = append(s.tradeLedgerEntries, entry)
	if len(s.tradeLedgerEntries) > tradeLedgerMaxEntries {
		if err := s.trimTradeLedgerLocked(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) trimTradeLedgerLocked() error {
	if len(s.tradeLedgerEntries) <= tradeLedgerMaxEntries {
		return nil
	}
	keep := tradeLedgerTrimToEntries
	if keep < 1 || keep > tradeLedgerMaxEntries {
		keep = tradeLedgerMaxEntries
	}
	if keep > len(s.tradeLedgerEntries) {
		keep = len(s.tradeLedgerEntries)
	}
	start := len(s.tradeLedgerEntries) - keep
	kept := append([]TradeLedgerEntry(nil), s.tradeLedgerEntries[start:]...)
	if err := writeJSONLinesAtomic(s.tradeLedgerPath(), kept); err != nil {
		return err
	}
	s.tradeLedgerEntries = kept
	s.tradeLedger = make(map[string]struct{}, len(kept))
	for _, entry := range kept {
		key := strings.TrimSpace(entry.Key)
		if key == "" {
			continue
		}
		s.tradeLedger[key] = struct{}{}
	}
	return nil
}

func (s *Store) statePath() string {
	return filepath.Join(s.root, "state.json")
}

func (s *Store) ordersPath() string {
	return filepath.Join(s.root, "open_orders.json")
}

func (s *Store) runtimeStatusPath() string {
	return filepath.Join(s.root, "runtime_status.json")
}

func (s *Store) tradeLedgerPath() string {
	return filepath.Join(s.root, "trade_ledger.jsonl")
}

func writeJSONAtomic(path string, v any) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "tmp-*")
	if err != nil {
		return err
	}
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return fsyncDirBestEffort(dir, path)
}

func writeJSONLinesAtomic(path string, entries []TradeLedgerEntry) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "tmp-*")
	if err != nil {
		return err
	}
	enc := json.NewEncoder(tmp)
	for _, entry := range entries {
		if err := enc.Encode(entry); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
			return err
		}
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return fsyncDirBestEffort(dir, path)
}

func fsyncDirBestEffort(dir, path string) error {
	// Best-effort directory fsync to improve rename durability across crashes.
	d, err := os.Open(dir)
	if err != nil {
		log.Printf(
			"level=WARN event=store_dir_fsync_skipped reason=%q dir=%q target=%q",
			err.Error(),
			dir,
			path,
		)
		return nil
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		log.Printf(
			"level=WARN event=store_dir_fsync_failed reason=%q dir=%q target=%q",
			err.Error(),
			dir,
			path,
		)
		return nil
	}
	return nil
}

func decodeOpenOrdersSnapshot(data []byte) (OpenOrdersSnapshot, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return OpenOrdersSnapshot{}, errors.New("open orders snapshot is empty")
	}
	var snapshot OpenOrdersSnapshot
	if err := json.Unmarshal(trimmed, &snapshot); err != nil {
		return OpenOrdersSnapshot{}, err
	}
	if snapshot.Orders == nil {
		snapshot.Orders = make([]core.Order, 0)
	}
	return snapshot, nil
}

func newSnapshotID(now time.Time) string {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return strconv.FormatInt(now.UnixNano(), 36)
}

func (s *Store) loadTradeLedgerLocked() error {
	if s.tradeLedgerLoaded {
		return nil
	}
	s.tradeLedger = make(map[string]struct{})
	s.tradeLedgerEntries = make([]TradeLedgerEntry, 0)
	path := s.tradeLedgerPath()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.tradeLedgerLoaded = true
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 2*1024*1024)
	loadedAt := time.Now().UTC()
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var entry TradeLedgerEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		key := strings.TrimSpace(entry.Key)
		if key == "" {
			continue
		}
		if _, ok := s.tradeLedger[key]; ok {
			continue
		}
		entry.Key = key
		if entry.SeenAt.IsZero() {
			entry.SeenAt = loadedAt
		}
		s.tradeLedger[key] = struct{}{}
		s.tradeLedgerEntries = append(s.tradeLedgerEntries, entry)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(s.tradeLedgerEntries) > tradeLedgerMaxEntries {
		if err := s.trimTradeLedgerLocked(); err != nil {
			return err
		}
	}
	s.tradeLedgerLoaded = true
	return nil
}
