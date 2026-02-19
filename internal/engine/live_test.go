package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"

	"grid-trading/internal/core"
	"grid-trading/internal/exchange/binance"
	"grid-trading/internal/safety"
	"grid-trading/internal/store"
	"grid-trading/internal/strategy"
)

type liveStrategySpy struct {
	mu             sync.Mutex
	initCalls      int
	reconcileCalls int
	fills          []core.Trade
	stopAfterFill  int
	initErr        error
	onFillErr      error
	reconcileErr   error
	reconcileErrs  []error
}

func (s *liveStrategySpy) Init(_ context.Context, _ decimal.Decimal) error {
	s.mu.Lock()
	s.initCalls++
	initErr := s.initErr
	s.mu.Unlock()
	if initErr != nil {
		return initErr
	}
	return nil
}

func (s *liveStrategySpy) OnFill(_ context.Context, trade core.Trade) error {
	s.mu.Lock()
	s.fills = append(s.fills, trade)
	count := len(s.fills)
	stopAfter := s.stopAfterFill
	onFillErr := s.onFillErr
	s.mu.Unlock()
	if stopAfter > 0 && count >= stopAfter {
		return strategy.ErrStopped
	}
	if onFillErr != nil {
		return onFillErr
	}
	return nil
}

func (s *liveStrategySpy) Reconcile(_ context.Context, _ decimal.Decimal, _ []core.Order) error {
	s.mu.Lock()
	s.reconcileCalls++
	reconcileErr := s.reconcileErr
	if len(s.reconcileErrs) > 0 {
		reconcileErr = s.reconcileErrs[0]
		s.reconcileErrs = s.reconcileErrs[1:]
	}
	s.mu.Unlock()
	if reconcileErr != nil {
		return reconcileErr
	}
	return nil
}

func (s *liveStrategySpy) stats() (initCalls int, reconcileCalls int, fills []core.Trade) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]core.Trade, len(s.fills))
	copy(out, s.fills)
	return s.initCalls, s.reconcileCalls, out
}

func TestLiveRunOnceReconnectAndResync(t *testing.T) {
	asyncErrs := make(chan error, 16)

	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/ticker/price":
			_ = writeJSON(w, http.StatusOK, map[string]string{
				"symbol": "BTCUSDT",
				"price":  "100",
			})
		case "/api/v3/openOrders":
			_ = writeJSON(w, http.StatusOK, []any{})
		default:
			recordAsyncErr(asyncErrs, fmt.Errorf("unexpected REST path: %s", r.URL.Path))
			_ = writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		}
	}))
	defer rest.Close()

	var wsConnCount int32
	ws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true },
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			recordAsyncErr(asyncErrs, err)
			return
		}
		defer conn.Close()

		idx := atomic.AddInt32(&wsConnCount, 1)
		reqID, err := readWSReqID(conn)
		if err != nil {
			recordAsyncErr(asyncErrs, err)
			return
		}
		if err := writeWSResponse(conn, reqID); err != nil {
			recordAsyncErr(asyncErrs, err)
			return
		}

		if idx == 1 {
			// First stream closes immediately to simulate disconnect.
			return
		}

		if err := writeExecutionReport(conn, executionReportPayload{
			OrderID:   12001,
			TradeID:   22001,
			Side:      "BUY",
			Status:    "FILLED",
			OrderQty:  "1",
			LastQty:   "1",
			LastPrice: "100",
			CumQty:    "1",
		}); err != nil {
			recordAsyncErr(asyncErrs, err)
		}
	}))
	defer ws.Close()

	client := binance.NewClientWithOptions(binance.Options{
		APIKey:            "k",
		APISecret:         "s",
		RestBaseURL:       rest.URL,
		WSBaseURL:         httpToWS(ws.URL),
		Symbol:            "BTCUSDT",
		ClientOrderPrefix: "test",
		UserStreamAuth:    "signature",
		HTTPTimeoutSec:    3,
	})
	defer client.Close()

	strat := &liveStrategySpy{stopAfterFill: 1}
	runner := LiveRunner{
		Exchange: client,
		Strategy: strat,
		Symbol:   "BTCUSDT",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	seen := newSeenTracker(128, time.Hour)
	reconnectAttempts := 1
	disconnectStartedAt := time.Now().UTC().Add(-2 * time.Second)
	backoff := time.Second

	err := runner.runOnce(ctx, false, seen, &reconnectAttempts, &disconnectStartedAt, &backoff, time.Now().UTC())
	if err == nil {
		t.Fatalf("first runOnce error = nil, want stream disconnect error")
	}

	err = runner.runOnce(ctx, true, seen, &reconnectAttempts, &disconnectStartedAt, &backoff, time.Now().UTC())
	if err != nil {
		t.Fatalf("second runOnce error = %v, want nil", err)
	}

	initCalls, reconcileCalls, fills := strat.stats()
	if initCalls != 0 {
		t.Fatalf("init calls = %d, want 0", initCalls)
	}
	if reconcileCalls != 2 {
		t.Fatalf("reconcile calls = %d, want 2", reconcileCalls)
	}
	if len(fills) != 1 {
		t.Fatalf("fill calls = %d, want 1", len(fills))
	}
	if atomic.LoadInt32(&wsConnCount) < 2 {
		t.Fatalf("ws connections = %d, want >= 2", atomic.LoadInt32(&wsConnCount))
	}

	assertNoAsyncErr(t, asyncErrs)
}

func TestLiveRunnerProcessesPartialAndFinalTrades(t *testing.T) {
	asyncErrs := make(chan error, 16)

	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/ticker/price":
			_ = writeJSON(w, http.StatusOK, map[string]string{
				"symbol": "BTCUSDT",
				"price":  "100",
			})
		case "/api/v3/openOrders":
			_ = writeJSON(w, http.StatusOK, []any{})
		default:
			recordAsyncErr(asyncErrs, fmt.Errorf("unexpected REST path: %s", r.URL.Path))
			_ = writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		}
	}))
	defer rest.Close()

	ws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true },
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			recordAsyncErr(asyncErrs, err)
			return
		}
		defer conn.Close()

		reqID, err := readWSReqID(conn)
		if err != nil {
			recordAsyncErr(asyncErrs, err)
			return
		}
		if err := writeWSResponse(conn, reqID); err != nil {
			recordAsyncErr(asyncErrs, err)
			return
		}

		if err := writeExecutionReport(conn, executionReportPayload{
			OrderID:   32001,
			TradeID:   42001,
			Side:      "SELL",
			Status:    "PARTIALLY_FILLED",
			OrderQty:  "1",
			LastQty:   "0.4",
			LastPrice: "101",
			CumQty:    "0.4",
		}); err != nil {
			recordAsyncErr(asyncErrs, err)
			return
		}
		if err := writeExecutionReport(conn, executionReportPayload{
			OrderID:   32001,
			TradeID:   42002,
			Side:      "SELL",
			Status:    "FILLED",
			OrderQty:  "1",
			LastQty:   "0.6",
			LastPrice: "101",
			CumQty:    "1",
		}); err != nil {
			recordAsyncErr(asyncErrs, err)
		}
	}))
	defer ws.Close()

	client := binance.NewClientWithOptions(binance.Options{
		APIKey:            "k",
		APISecret:         "s",
		RestBaseURL:       rest.URL,
		WSBaseURL:         httpToWS(ws.URL),
		Symbol:            "BTCUSDT",
		ClientOrderPrefix: "test",
		UserStreamAuth:    "signature",
		HTTPTimeoutSec:    3,
	})
	defer client.Close()

	strat := &liveStrategySpy{stopAfterFill: 2}
	runner := LiveRunner{
		Exchange: client,
		Strategy: strat,
		Symbol:   "BTCUSDT",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := runner.Run(ctx)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	_, _, fills := strat.stats()
	if len(fills) != 2 {
		t.Fatalf("fill calls = %d, want 2", len(fills))
	}
	if fills[0].Status != core.OrderPartiallyFilled {
		t.Fatalf("first fill status = %s, want %s", fills[0].Status, core.OrderPartiallyFilled)
	}
	if fills[1].Status != core.OrderFilled {
		t.Fatalf("second fill status = %s, want %s", fills[1].Status, core.OrderFilled)
	}
	if fills[0].TradeID == fills[1].TradeID {
		t.Fatalf("trade ids should differ, got %q and %q", fills[0].TradeID, fills[1].TradeID)
	}

	assertNoAsyncErr(t, asyncErrs)
}

func TestLiveRunOnceExitsCleanlyWhenInitialResyncStopsStrategy(t *testing.T) {
	asyncErrs := make(chan error, 16)

	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/ticker/price":
			_ = writeJSON(w, http.StatusOK, map[string]string{
				"symbol": "BTCUSDT",
				"price":  "100",
			})
		case "/api/v3/openOrders":
			_ = writeJSON(w, http.StatusOK, []any{})
		default:
			recordAsyncErr(asyncErrs, fmt.Errorf("unexpected REST path: %s", r.URL.Path))
			_ = writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		}
	}))
	defer rest.Close()

	var wsConnCount int32
	ws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&wsConnCount, 1)
		upgrader := websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true },
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer ws.Close()

	client := binance.NewClientWithOptions(binance.Options{
		APIKey:            "k",
		APISecret:         "s",
		RestBaseURL:       rest.URL,
		WSBaseURL:         httpToWS(ws.URL),
		Symbol:            "BTCUSDT",
		ClientOrderPrefix: "test",
		UserStreamAuth:    "signature",
		HTTPTimeoutSec:    3,
	})
	defer client.Close()

	strat := &liveStrategySpy{reconcileErr: strategy.ErrStopped}
	runner := LiveRunner{
		Exchange: client,
		Strategy: strat,
		Symbol:   "BTCUSDT",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	seen := newSeenTracker(128, time.Hour)
	reconnectAttempts := 0
	disconnectStartedAt := time.Time{}
	backoff := time.Second

	err := runner.runOnce(ctx, false, seen, &reconnectAttempts, &disconnectStartedAt, &backoff, time.Now().UTC())
	if err != nil {
		t.Fatalf("runOnce() error = %v, want nil", err)
	}

	_, reconcileCalls, _ := strat.stats()
	if reconcileCalls != 1 {
		t.Fatalf("reconcile calls = %d, want 1", reconcileCalls)
	}
	if atomic.LoadInt32(&wsConnCount) != 0 {
		t.Fatalf("ws connections = %d, want 0", atomic.LoadInt32(&wsConnCount))
	}

	assertNoAsyncErr(t, asyncErrs)
}

func TestLiveReconcileMissingAppliesFilledOnlyOnce(t *testing.T) {
	asyncErrs := make(chan error, 16)

	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/order":
			_ = writeJSON(w, http.StatusOK, map[string]any{
				"symbol":              "BTCUSDT",
				"orderId":             50001,
				"clientOrderId":       "cid-50001",
				"price":               "100",
				"origQty":             "1",
				"executedQty":         "1",
				"cummulativeQuoteQty": "100",
				"status":              "FILLED",
				"side":                "BUY",
				"type":                "LIMIT",
				"time":                time.Now().Add(-time.Second).UnixMilli(),
				"updateTime":          time.Now().UnixMilli(),
			})
		case "/api/v3/openOrders":
			_ = writeJSON(w, http.StatusOK, []any{})
		default:
			recordAsyncErr(asyncErrs, fmt.Errorf("unexpected REST path: %s", r.URL.Path))
			_ = writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		}
	}))
	defer rest.Close()

	client := binance.NewClientWithOptions(binance.Options{
		APIKey:            "k",
		APISecret:         "s",
		RestBaseURL:       rest.URL,
		WSBaseURL:         "ws://127.0.0.1:1/unused",
		Symbol:            "BTCUSDT",
		ClientOrderPrefix: "test",
		UserStreamAuth:    "signature",
		HTTPTimeoutSec:    3,
	})
	defer client.Close()

	strat := &liveStrategySpy{}
	runner := LiveRunner{
		Exchange: client,
		Strategy: strat,
		Symbol:   "BTCUSDT",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	persisted := []core.Order{
		{
			ID:       "50001",
			ClientID: "cid-50001",
			Symbol:   "BTCUSDT",
			Side:     core.Buy,
			Type:     core.Limit,
			Price:    decimal.RequireFromString("100"),
			Qty:      decimal.RequireFromString("1"),
		},
	}
	seen := newSeenTracker(128, time.Hour)

	open, err := runner.reconcileMissing(ctx, nil, persisted, seen)
	if err != nil {
		t.Fatalf("reconcileMissing() first error = %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("reconcileMissing() first open count = %d, want 0", len(open))
	}

	_, err = runner.reconcileMissing(ctx, nil, persisted, seen)
	if err != nil {
		t.Fatalf("reconcileMissing() second error = %v", err)
	}

	_, _, fills := strat.stats()
	if len(fills) != 1 {
		t.Fatalf("fill calls = %d, want 1", len(fills))
	}

	assertNoAsyncErr(t, asyncErrs)
}

func TestLiveReconcileMissingAutoHandlesClosedPartialFill(t *testing.T) {
	asyncErrs := make(chan error, 16)

	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/order":
			_ = writeJSON(w, http.StatusOK, map[string]any{
				"symbol":              "BTCUSDT",
				"orderId":             50002,
				"clientOrderId":       "cid-50002",
				"price":               "100",
				"origQty":             "1",
				"executedQty":         "0.4",
				"cummulativeQuoteQty": "40",
				"status":              "CANCELED",
				"side":                "SELL",
				"type":                "LIMIT",
				"time":                time.Now().Add(-time.Second).UnixMilli(),
				"updateTime":          time.Now().UnixMilli(),
			})
		case "/api/v3/openOrders":
			_ = writeJSON(w, http.StatusOK, []any{})
		default:
			recordAsyncErr(asyncErrs, fmt.Errorf("unexpected REST path: %s", r.URL.Path))
			_ = writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		}
	}))
	defer rest.Close()

	client := binance.NewClientWithOptions(binance.Options{
		APIKey:            "k",
		APISecret:         "s",
		RestBaseURL:       rest.URL,
		WSBaseURL:         "ws://127.0.0.1:1/unused",
		Symbol:            "BTCUSDT",
		ClientOrderPrefix: "test",
		UserStreamAuth:    "signature",
		HTTPTimeoutSec:    3,
	})
	defer client.Close()

	strat := &liveStrategySpy{}
	runner := LiveRunner{
		Exchange: client,
		Strategy: strat,
		Symbol:   "BTCUSDT",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	persisted := []core.Order{
		{
			ID:       "50002",
			ClientID: "cid-50002",
			Symbol:   "BTCUSDT",
			Side:     core.Sell,
			Type:     core.Limit,
			Price:    decimal.RequireFromString("100"),
			Qty:      decimal.RequireFromString("1"),
		},
	}
	seen := newSeenTracker(128, time.Hour)

	open, err := runner.reconcileMissing(ctx, nil, persisted, seen)
	if err != nil {
		t.Fatalf("reconcileMissing() first error = %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("reconcileMissing() first open count = %d, want 0", len(open))
	}

	_, err = runner.reconcileMissing(ctx, nil, persisted, seen)
	if err != nil {
		t.Fatalf("reconcileMissing() second error = %v", err)
	}

	_, _, fills := strat.stats()
	if len(fills) != 1 {
		t.Fatalf("fill calls = %d, want 1", len(fills))
	}
	if fills[0].Status != core.OrderCanceled {
		t.Fatalf("fill status = %s, want %s", fills[0].Status, core.OrderCanceled)
	}
	if !fills[0].Qty.Equal(decimal.RequireFromString("0.4")) {
		t.Fatalf("fill qty = %s, want 0.4", fills[0].Qty)
	}

	assertNoAsyncErr(t, asyncErrs)
}

func TestLiveRunnerReconnectCircuitBreakerDoesNotStopRunner(t *testing.T) {
	asyncErrs := make(chan error, 16)

	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/ticker/price":
			_ = writeJSON(w, http.StatusOK, map[string]string{
				"symbol": "BTCUSDT",
				"price":  "100",
			})
		case "/api/v3/openOrders":
			_ = writeJSON(w, http.StatusOK, []any{})
		default:
			recordAsyncErr(asyncErrs, fmt.Errorf("unexpected REST path: %s", r.URL.Path))
			_ = writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		}
	}))
	defer rest.Close()

	client := binance.NewClientWithOptions(binance.Options{
		APIKey:            "k",
		APISecret:         "s",
		RestBaseURL:       rest.URL,
		WSBaseURL:         "ws://127.0.0.1:1/ws-api/v3",
		Symbol:            "BTCUSDT",
		ClientOrderPrefix: "test",
		UserStreamAuth:    "signature",
		HTTPTimeoutSec:    3,
	})
	defer client.Close()

	strat := &liveStrategySpy{}
	breaker := safety.NewBreaker(true, 5, 5, 1)
	breaker.SetReconnectRecovery(30*time.Second, 1)
	runner := LiveRunner{
		Exchange: client,
		Strategy: strat,
		Symbol:   "BTCUSDT",
		Breaker:  breaker,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := runner.Run(ctx)
	if err == nil {
		t.Fatalf("Run() error = nil, want context deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want errors.Is(_, context.DeadlineExceeded)", err)
	}

	assertNoAsyncErr(t, asyncErrs)
}

func TestLiveRunnerPersistsRuntimeStatus(t *testing.T) {
	asyncErrs := make(chan error, 16)

	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/ticker/price":
			_ = writeJSON(w, http.StatusOK, map[string]string{
				"symbol": "BTCUSDT",
				"price":  "100",
			})
		case "/api/v3/openOrders":
			_ = writeJSON(w, http.StatusOK, []any{})
		default:
			recordAsyncErr(asyncErrs, fmt.Errorf("unexpected REST path: %s", r.URL.Path))
			_ = writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		}
	}))
	defer rest.Close()

	ws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true },
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			recordAsyncErr(asyncErrs, err)
			return
		}
		defer conn.Close()

		reqID, err := readWSReqID(conn)
		if err != nil {
			recordAsyncErr(asyncErrs, err)
			return
		}
		if err := writeWSResponse(conn, reqID); err != nil {
			recordAsyncErr(asyncErrs, err)
			return
		}
		if err := writeExecutionReport(conn, executionReportPayload{
			OrderID:   62001,
			TradeID:   72001,
			Side:      "BUY",
			Status:    "FILLED",
			OrderQty:  "1",
			LastQty:   "1",
			LastPrice: "100",
			CumQty:    "1",
		}); err != nil {
			recordAsyncErr(asyncErrs, err)
		}
	}))
	defer ws.Close()

	client := binance.NewClientWithOptions(binance.Options{
		APIKey:            "k",
		APISecret:         "s",
		RestBaseURL:       rest.URL,
		WSBaseURL:         httpToWS(ws.URL),
		Symbol:            "BTCUSDT",
		ClientOrderPrefix: "test",
		UserStreamAuth:    "signature",
		HTTPTimeoutSec:    3,
	})
	defer client.Close()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	strat := &liveStrategySpy{stopAfterFill: 1}
	runner := LiveRunner{
		Exchange:   client,
		Strategy:   strat,
		Symbol:     "BTCUSDT",
		Mode:       "testnet",
		InstanceID: "bot1",
		Store:      st,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	status, ok, err := st.LoadRuntimeStatus()
	if err != nil {
		t.Fatalf("LoadRuntimeStatus() error = %v", err)
	}
	if !ok {
		t.Fatalf("LoadRuntimeStatus() ok = false, want true")
	}
	if status.State != "stopped" {
		t.Fatalf("status.state = %q, want stopped", status.State)
	}
	if status.Mode != "testnet" {
		t.Fatalf("status.mode = %q, want testnet", status.Mode)
	}
	if status.Symbol != "BTCUSDT" {
		t.Fatalf("status.symbol = %q, want BTCUSDT", status.Symbol)
	}
	if status.InstanceID != "bot1" {
		t.Fatalf("status.instance_id = %q, want bot1", status.InstanceID)
	}
	if status.PID <= 0 {
		t.Fatalf("status.pid = %d, want > 0", status.PID)
	}
	if status.StartedAt.IsZero() || status.UpdatedAt.IsZero() {
		t.Fatalf("status timestamps should be set, got started_at=%v updated_at=%v", status.StartedAt, status.UpdatedAt)
	}

	assertNoAsyncErr(t, asyncErrs)
}

func TestLiveRunnerPersistsReconnectAttemptsOnCircuitTrip(t *testing.T) {
	asyncErrs := make(chan error, 16)

	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/ticker/price":
			_ = writeJSON(w, http.StatusOK, map[string]string{
				"symbol": "BTCUSDT",
				"price":  "100",
			})
		case "/api/v3/openOrders":
			_ = writeJSON(w, http.StatusOK, []any{})
		default:
			recordAsyncErr(asyncErrs, fmt.Errorf("unexpected REST path: %s", r.URL.Path))
			_ = writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		}
	}))
	defer rest.Close()

	client := binance.NewClientWithOptions(binance.Options{
		APIKey:            "k",
		APISecret:         "s",
		RestBaseURL:       rest.URL,
		WSBaseURL:         "ws://127.0.0.1:1/ws-api/v3",
		Symbol:            "BTCUSDT",
		ClientOrderPrefix: "test",
		UserStreamAuth:    "signature",
		HTTPTimeoutSec:    3,
	})
	defer client.Close()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	strat := &liveStrategySpy{}
	breaker := safety.NewBreaker(true, 5, 5, 1)
	breaker.SetReconnectRecovery(30*time.Second, 1)
	runner := LiveRunner{
		Exchange:   client,
		Strategy:   strat,
		Symbol:     "BTCUSDT",
		Mode:       "testnet",
		InstanceID: "bot1",
		Store:      st,
		Breaker:    breaker,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = runner.Run(ctx)
	if err == nil {
		t.Fatalf("Run() error = nil, want deadline exceeded")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want errors.Is(_, context.DeadlineExceeded)", err)
	}

	status, ok, loadErr := st.LoadRuntimeStatus()
	if loadErr != nil {
		t.Fatalf("LoadRuntimeStatus() error = %v", loadErr)
	}
	if !ok {
		t.Fatalf("LoadRuntimeStatus() ok = false, want true")
	}
	if status.State != "stopped" {
		t.Fatalf("status.state = %q, want stopped", status.State)
	}
	if status.ReconnectAttempts != 1 {
		t.Fatalf("status.reconnect_attempts = %d, want 1", status.ReconnectAttempts)
	}
	if status.LastError == "" {
		t.Fatalf("status.last_error = empty, want non-empty")
	}
	if status.DisconnectedAt == nil {
		t.Fatalf("status.disconnected_at = nil, want non-nil")
	}

	assertNoAsyncErr(t, asyncErrs)
}

func TestSeenTrackerBoundedAndTTL(t *testing.T) {
	base := time.Now().UTC()
	tracker := newSeenTracker(2, time.Second)

	if tracker.Seen("a", base) {
		t.Fatalf("first seen(a) should be false")
	}
	if !tracker.Seen("a", base.Add(100*time.Millisecond)) {
		t.Fatalf("second seen(a) should be true")
	}
	if tracker.Seen("b", base.Add(200*time.Millisecond)) {
		t.Fatalf("first seen(b) should be false")
	}
	if tracker.Seen("c", base.Add(300*time.Millisecond)) {
		t.Fatalf("first seen(c) should be false")
	}
	if tracker.Seen("a", base.Add(400*time.Millisecond)) {
		t.Fatalf("a should have been evicted by max size")
	}

	ttlTracker := newSeenTracker(10, time.Second)
	if ttlTracker.Seen("x", base) {
		t.Fatalf("first seen(x) should be false")
	}
	if ttlTracker.Seen("x", base.Add(2*time.Second)) {
		t.Fatalf("x should expire by ttl")
	}
}

func TestLiveRunOncePeriodicReconcile(t *testing.T) {
	asyncErrs := make(chan error, 16)
	var openOrdersCalls int32

	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/ticker/price":
			_ = writeJSON(w, http.StatusOK, map[string]string{
				"symbol": "BTCUSDT",
				"price":  "100",
			})
		case "/api/v3/openOrders":
			atomic.AddInt32(&openOrdersCalls, 1)
			_ = writeJSON(w, http.StatusOK, []any{})
		default:
			recordAsyncErr(asyncErrs, fmt.Errorf("unexpected REST path: %s", r.URL.Path))
			_ = writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		}
	}))
	defer rest.Close()

	ws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true },
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			recordAsyncErr(asyncErrs, err)
			return
		}
		defer conn.Close()

		reqID, err := readWSReqID(conn)
		if err != nil {
			recordAsyncErr(asyncErrs, err)
			return
		}
		if err := writeWSResponse(conn, reqID); err != nil {
			recordAsyncErr(asyncErrs, err)
			return
		}

		time.Sleep(300 * time.Millisecond)
	}))
	defer ws.Close()

	client := binance.NewClientWithOptions(binance.Options{
		APIKey:            "k",
		APISecret:         "s",
		RestBaseURL:       rest.URL,
		WSBaseURL:         httpToWS(ws.URL),
		Symbol:            "BTCUSDT",
		ClientOrderPrefix: "test",
		UserStreamAuth:    "signature",
		HTTPTimeoutSec:    3,
	})
	defer client.Close()

	strat := &liveStrategySpy{}
	runner := LiveRunner{
		Exchange:  client,
		Strategy:  strat,
		Symbol:    "BTCUSDT",
		Reconcile: 50 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 220*time.Millisecond)
	defer cancel()

	seen := newSeenTracker(128, time.Hour)
	reconnectAttempts := 1
	disconnectStartedAt := time.Time{}
	backoff := time.Second

	err := runner.runOnce(ctx, true, seen, &reconnectAttempts, &disconnectStartedAt, &backoff, time.Now().UTC())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runOnce() error = %v, want context deadline exceeded", err)
	}

	_, reconcileCalls, _ := strat.stats()
	if reconcileCalls < 2 {
		t.Fatalf("reconcile calls = %d, want >= 2", reconcileCalls)
	}
	if atomic.LoadInt32(&openOrdersCalls) < 2 {
		t.Fatalf("openOrders calls = %d, want >= 2", atomic.LoadInt32(&openOrdersCalls))
	}

	assertNoAsyncErr(t, asyncErrs)
}

func TestLiveRunOncePeriodicReconcileStopsCleanlyOnErrStopped(t *testing.T) {
	asyncErrs := make(chan error, 16)

	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/ticker/price":
			_ = writeJSON(w, http.StatusOK, map[string]string{
				"symbol": "BTCUSDT",
				"price":  "100",
			})
		case "/api/v3/openOrders":
			_ = writeJSON(w, http.StatusOK, []any{})
		default:
			recordAsyncErr(asyncErrs, fmt.Errorf("unexpected REST path: %s", r.URL.Path))
			_ = writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		}
	}))
	defer rest.Close()

	ws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true },
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			recordAsyncErr(asyncErrs, err)
			return
		}
		defer conn.Close()

		reqID, err := readWSReqID(conn)
		if err != nil {
			recordAsyncErr(asyncErrs, err)
			return
		}
		if err := writeWSResponse(conn, reqID); err != nil {
			recordAsyncErr(asyncErrs, err)
			return
		}
		time.Sleep(300 * time.Millisecond)
	}))
	defer ws.Close()

	client := binance.NewClientWithOptions(binance.Options{
		APIKey:            "k",
		APISecret:         "s",
		RestBaseURL:       rest.URL,
		WSBaseURL:         httpToWS(ws.URL),
		Symbol:            "BTCUSDT",
		ClientOrderPrefix: "test",
		UserStreamAuth:    "signature",
		HTTPTimeoutSec:    3,
	})
	defer client.Close()

	strat := &liveStrategySpy{
		reconcileErrs: []error{nil, strategy.ErrStopped},
	}
	runner := LiveRunner{
		Exchange:  client,
		Strategy:  strat,
		Symbol:    "BTCUSDT",
		Reconcile: 50 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	seen := newSeenTracker(128, time.Hour)
	reconnectAttempts := 0
	disconnectStartedAt := time.Time{}
	backoff := time.Second

	err := runner.runOnce(ctx, false, seen, &reconnectAttempts, &disconnectStartedAt, &backoff, time.Now().UTC())
	if err != nil {
		t.Fatalf("runOnce() error = %v, want nil", err)
	}

	_, reconcileCalls, _ := strat.stats()
	if reconcileCalls < 2 {
		t.Fatalf("reconcile calls = %d, want >= 2", reconcileCalls)
	}

	assertNoAsyncErr(t, asyncErrs)
}

func TestLiveRunnerStopsOnFatalLocalErrorWithoutReconnectTrip(t *testing.T) {
	asyncErrs := make(chan error, 16)

	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/ticker/price":
			_ = writeJSON(w, http.StatusOK, map[string]string{
				"symbol": "BTCUSDT",
				"price":  "100",
			})
		case "/api/v3/openOrders":
			_ = writeJSON(w, http.StatusOK, []any{})
		default:
			recordAsyncErr(asyncErrs, fmt.Errorf("unexpected REST path: %s", r.URL.Path))
			_ = writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		}
	}))
	defer rest.Close()

	ws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true },
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			recordAsyncErr(asyncErrs, err)
			return
		}
		defer conn.Close()

		reqID, err := readWSReqID(conn)
		if err != nil {
			recordAsyncErr(asyncErrs, err)
			return
		}
		if err := writeWSResponse(conn, reqID); err != nil {
			recordAsyncErr(asyncErrs, err)
			return
		}
		if err := writeExecutionReport(conn, executionReportPayload{
			OrderID:   82001,
			TradeID:   92001,
			Side:      "BUY",
			Status:    "FILLED",
			OrderQty:  "1",
			LastQty:   "1",
			LastPrice: "100",
			CumQty:    "1",
		}); err != nil {
			recordAsyncErr(asyncErrs, err)
		}
	}))
	defer ws.Close()

	client := binance.NewClientWithOptions(binance.Options{
		APIKey:            "k",
		APISecret:         "s",
		RestBaseURL:       rest.URL,
		WSBaseURL:         httpToWS(ws.URL),
		Symbol:            "BTCUSDT",
		ClientOrderPrefix: "test",
		UserStreamAuth:    "signature",
		HTTPTimeoutSec:    3,
	})
	defer client.Close()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	strat := &liveStrategySpy{onFillErr: errors.New("disk write failed")}
	breaker := safety.NewBreaker(true, 5, 5, 1)
	runner := LiveRunner{
		Exchange:   client,
		Strategy:   strat,
		Symbol:     "BTCUSDT",
		Mode:       "testnet",
		InstanceID: "bot1",
		Store:      st,
		Breaker:    breaker,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = runner.Run(ctx)
	if err == nil {
		t.Fatalf("Run() error = nil, want fatal local error")
	}
	if !errors.Is(err, ErrFatalLocal) {
		t.Fatalf("Run() error = %v, want errors.Is(_, ErrFatalLocal)", err)
	}
	if errors.Is(err, safety.ErrCircuitOpen) {
		t.Fatalf("Run() error should not trip reconnect breaker, got %v", err)
	}

	status, ok, loadErr := st.LoadRuntimeStatus()
	if loadErr != nil {
		t.Fatalf("LoadRuntimeStatus() error = %v", loadErr)
	}
	if !ok {
		t.Fatalf("LoadRuntimeStatus() ok = false, want true")
	}
	if status.ReconnectAttempts != 0 {
		t.Fatalf("status.reconnect_attempts = %d, want 0", status.ReconnectAttempts)
	}
	if status.LastError == "" {
		t.Fatalf("status.last_error = empty, want non-empty")
	}

	assertNoAsyncErr(t, asyncErrs)
}

type executionReportPayload struct {
	OrderID   int64
	TradeID   int64
	Side      string
	Status    string
	OrderQty  string
	LastQty   string
	LastPrice string
	CumQty    string
}

func writeExecutionReport(conn *websocket.Conn, p executionReportPayload) error {
	ts := time.Now().UTC().UnixMilli()
	msg := map[string]any{
		"e": "executionReport",
		"E": ts,
		"s": "BTCUSDT",
		"i": p.OrderID,
		"S": p.Side,
		"x": "TRADE",
		"X": p.Status,
		"p": p.LastPrice,
		"q": p.OrderQty,
		"L": p.LastPrice,
		"l": p.LastQty,
		"z": p.CumQty,
		"T": ts,
		"t": p.TradeID,
	}
	return conn.WriteJSON(msg)
}

func readWSReqID(conn *websocket.Conn) (string, error) {
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	_, data, err := conn.ReadMessage()
	if err != nil {
		return "", err
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return "", err
	}
	if req.ID == "" {
		return "", errors.New("ws request id is empty")
	}
	return req.ID, nil
}

func writeWSResponse(conn *websocket.Conn, reqID string) error {
	resp := map[string]any{
		"id":     reqID,
		"status": 200,
		"result": map[string]any{},
	}
	return conn.WriteJSON(resp)
}

func writeJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}

func httpToWS(raw string) string {
	if strings.HasPrefix(raw, "https://") {
		return "wss://" + strings.TrimPrefix(raw, "https://")
	}
	return "ws://" + strings.TrimPrefix(raw, "http://")
}

func recordAsyncErr(ch chan<- error, err error) {
	if err == nil {
		return
	}
	select {
	case ch <- err:
	default:
	}
}

func assertNoAsyncErr(t *testing.T, ch <-chan error) {
	t.Helper()
	for {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("async handler error: %v", err)
			}
		default:
			return
		}
	}
}
