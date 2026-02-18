package store

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"grid-trading/internal/core"
)

type GridState struct {
	Strategy       string          `json:"strategy"`
	Symbol         string          `json:"symbol"`
	Anchor         decimal.Decimal `json:"anchor"`
	Low            decimal.Decimal `json:"low"`
	StopPrice      decimal.Decimal `json:"stop_price"`
	Ratio          decimal.Decimal `json:"ratio"`
	SellRatio      decimal.Decimal `json:"sell_ratio,omitempty"`
	Levels         int             `json:"levels"`
	MinLevel       int             `json:"min_level"`
	MaxLevel       int             `json:"max_level"`
	Qty            decimal.Decimal `json:"qty"`
	MinQtyMultiple int64           `json:"min_qty_multiple"`
	Rules          core.Rules      `json:"rules"`
	Initialized    bool            `json:"initialized"`
	Stopped        bool            `json:"stopped"`
	UpdatedAt      time.Time       `json:"updated_at"`
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
	root string
	mu   sync.Mutex
}

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
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONAtomic(s.statePath(), state)
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
	return writeJSONAtomic(s.ordersPath(), orders)
}

func (s *Store) LoadOpenOrders() ([]core.Order, bool, error) {
	data, err := os.ReadFile(s.ordersPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var orders []core.Order
	if err := json.Unmarshal(data, &orders); err != nil {
		return nil, false, err
	}
	return orders, true, nil
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

func (s *Store) statePath() string {
	return filepath.Join(s.root, "state.json")
}

func (s *Store) ordersPath() string {
	return filepath.Join(s.root, "open_orders.json")
}

func (s *Store) runtimeStatusPath() string {
	return filepath.Join(s.root, "runtime_status.json")
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
