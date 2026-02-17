package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaultsGridModeToGeometric(t *testing.T) {
	cfgPath := writeTempConfig(t, `
symbol: BTCUSDT

grid:
  ratio: "1.01"
  levels: 20
  qty: "0.001"

backtest:
  data_path: data/binance/BTCUSDT/1m
  initial_base: "0"
  initial_quote: "1000"
  fees:
    maker_rate: "0"
    taker_rate: "0"
  rules:
    min_qty: "0"
    min_notional: "0"
    price_tick: "0"
    qty_step: "0"
`)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Grid.Mode != GridGeo {
		t.Fatalf("grid.mode = %q, want %q", cfg.Grid.Mode, GridGeo)
	}
	if !cfg.Grid.SellRatio.Equal(cfg.Grid.Ratio.Decimal) {
		t.Fatalf("grid.sell_ratio = %s, want %s", cfg.Grid.SellRatio.String(), cfg.Grid.Ratio.String())
	}
	if cfg.Grid.Regime.Window != 30 {
		t.Fatalf("grid.regime.window = %d, want 30", cfg.Grid.Regime.Window)
	}
	if cfg.Grid.Regime.EnterConfirm != 3 {
		t.Fatalf("grid.regime.enter_confirm = %d, want 3", cfg.Grid.Regime.EnterConfirm)
	}
	if cfg.Grid.Regime.ExitConfirm != 5 {
		t.Fatalf("grid.regime.exit_confirm = %d, want 5", cfg.Grid.Regime.ExitConfirm)
	}
	if cfg.Exchange.UserStreamKeepaliveSec != 30 {
		t.Fatalf("exchange.user_stream_keepalive_sec = %d, want 30", cfg.Exchange.UserStreamKeepaliveSec)
	}
	if cfg.Observability.Runtime.ReconcileIntervalSec != 60 {
		t.Fatalf("observability.runtime.reconcile_interval_sec = %d, want 60", cfg.Observability.Runtime.ReconcileIntervalSec)
	}
	if cfg.Observability.Runtime.AlertDropReportSec != 60 {
		t.Fatalf("observability.runtime.alert_drop_report_sec = %d, want 60", cfg.Observability.Runtime.AlertDropReportSec)
	}
	if cfg.State.LockStaleSec != 600 {
		t.Fatalf("state.lock_stale_sec = %d, want 600", cfg.State.LockStaleSec)
	}
	if cfg.State.LockTakeover == nil || !*cfg.State.LockTakeover {
		t.Fatalf("state.lock_takeover = %v, want true", cfg.State.LockTakeover)
	}
}

func TestLoadRejectsLinearGridMode(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: backtest
symbol: BTCUSDT

grid:
  ratio: "1.01"
  levels: 20
  mode: linear
  qty: "0.001"

backtest:
  data_path: data/binance/BTCUSDT/1m
  initial_base: "0"
  initial_quote: "1000"
  fees:
    maker_rate: "0"
    taker_rate: "0"
  rules:
    min_qty: "0"
    min_notional: "0"
    price_tick: "0"
    qty_step: "0"
`)

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatalf("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "grid mode must be geometric") {
		t.Fatalf("Load() error = %q, want contains %q", err.Error(), "grid mode must be geometric")
	}
}

func TestLoadRejectsLegacyGridInventoryField(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: backtest
symbol: BTCUSDT

grid:
  ratio: "1.01"
  levels: 20
  qty: "0.001"
  inventory:
    enabled: true

backtest:
  data_path: data/binance/BTCUSDT/1m
  initial_base: "0"
  initial_quote: "1000"
  fees:
    maker_rate: "0"
    taker_rate: "0"
  rules:
    min_qty: "0"
    min_notional: "0"
    price_tick: "0"
    qty_step: "0"
`)

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatalf("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "field inventory not found") {
		t.Fatalf("Load() error = %q, want unknown field inventory message", err.Error())
	}
}

func TestLoadRejectsLegacyGridHighField(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: backtest
symbol: BTCUSDT

grid:
  high: "100000"
  ratio: "1.01"
  levels: 20
  qty: "0.001"

backtest:
  data_path: data/binance/BTCUSDT/1m
  initial_base: "0"
  initial_quote: "1000"
  fees:
    maker_rate: "0"
    taker_rate: "0"
  rules:
    min_qty: "0"
    min_notional: "0"
    price_tick: "0"
    qty_step: "0"
`)

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatalf("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "field high not found") {
		t.Fatalf("Load() error = %q, want unknown field high message", err.Error())
	}
}

func TestLoadRejectsInvalidSellRatio(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: backtest
symbol: BTCUSDT

grid:
  ratio: "1.01"
  sell_ratio: "1"
  levels: 20
  qty: "0.001"

backtest:
  data_path: data/binance/BTCUSDT/1m
  initial_base: "0"
  initial_quote: "1000"
  fees:
    maker_rate: "0"
    taker_rate: "0"
  rules:
    min_qty: "0"
    min_notional: "0"
    price_tick: "0"
    qty_step: "0"
`)

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatalf("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "grid sell_ratio must be > 1") {
		t.Fatalf("Load() error = %q, want sell_ratio validation", err.Error())
	}
}

func TestLoadRejectsInvalidRegimeMultiplier(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: backtest
symbol: BTCUSDT

grid:
  ratio: "1.01"
  levels: 20
  qty: "0.001"
  regime:
    trend_up_buy_spacing_mult: "1.1"

backtest:
  data_path: data/binance/BTCUSDT/1m
  initial_base: "0"
  initial_quote: "1000"
  fees:
    maker_rate: "0"
    taker_rate: "0"
  rules:
    min_qty: "0"
    min_notional: "0"
    price_tick: "0"
    qty_step: "0"
`)

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatalf("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "grid regime.trend_up_buy_spacing_mult must be between 0 and 1") {
		t.Fatalf("Load() error = %q, want regime multiplier validation", err.Error())
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: backtest
symbol: BTCUSDT

grid:
  ratio: "1.01"
  levels: 20
  qty: "0.001"
  unknown_field: 1

backtest:
  data_path: data/binance/BTCUSDT/1m
  initial_base: "0"
  initial_quote: "1000"
  fees:
    maker_rate: "0"
    taker_rate: "0"
  rules:
    min_qty: "0"
    min_notional: "0"
    price_tick: "0"
    qty_step: "0"
`)

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatalf("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "field unknown_field not found") {
		t.Fatalf("Load() error = %q, want unknown field message", err.Error())
	}
}

func TestLoadNormalizesSymbolAndInstanceID(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: backtest
symbol:  btcusdt
instance_id:  BOT_A1

grid:
  ratio: "1.01"
  levels: 20
  qty: "0.001"

backtest:
  data_path: data/binance/BTCUSDT/1m
  initial_base: "0"
  initial_quote: "1000"
  fees:
    maker_rate: "0"
    taker_rate: "0"
  rules:
    min_qty: "0"
    min_notional: "0"
    price_tick: "0"
    qty_step: "0"
`)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Symbol != "BTCUSDT" {
		t.Fatalf("symbol = %q, want BTCUSDT", cfg.Symbol)
	}
	if cfg.InstanceID != "bot_a1" {
		t.Fatalf("instance_id = %q, want bot_a1", cfg.InstanceID)
	}
}

func TestLoadRejectsInvalidMode(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: paper
symbol: BTCUSDT

grid:
  ratio: "1.01"
  levels: 20
  qty: "0.001"

backtest:
  data_path: data/binance/BTCUSDT/1m
  initial_base: "0"
  initial_quote: "1000"
  fees:
    maker_rate: "0"
    taker_rate: "0"
  rules:
    min_qty: "0"
    min_notional: "0"
    price_tick: "0"
    qty_step: "0"
`)

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatalf("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "mode must be backtest, testnet, or live") {
		t.Fatalf("Load() error = %q, want mode validation", err.Error())
	}
}

func TestLoadRejectsInvalidRuntimeHeartbeat(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: backtest
symbol: BTCUSDT

grid:
  ratio: "1.01"
  levels: 20
  qty: "0.001"

observability:
  runtime:
    heartbeat_sec: -1

backtest:
  data_path: data/binance/BTCUSDT/1m
  initial_base: "0"
  initial_quote: "1000"
  fees:
    maker_rate: "0"
    taker_rate: "0"
  rules:
    min_qty: "0"
    min_notional: "0"
    price_tick: "0"
    qty_step: "0"
`)

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatalf("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "observability.runtime.heartbeat_sec must be between 0 and 3600") {
		t.Fatalf("Load() error = %q, want heartbeat validation", err.Error())
	}
}

func TestLoadRejectsInvalidRuntimeReconcileInterval(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: backtest
symbol: BTCUSDT

grid:
  ratio: "1.01"
  levels: 20
  qty: "0.001"

observability:
  runtime:
    reconcile_interval_sec: 5

backtest:
  data_path: data/binance/BTCUSDT/1m
  initial_base: "0"
  initial_quote: "1000"
  fees:
    maker_rate: "0"
    taker_rate: "0"
  rules:
    min_qty: "0"
    min_notional: "0"
    price_tick: "0"
    qty_step: "0"
`)

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatalf("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "observability.runtime.reconcile_interval_sec must be 0 or >= 10") {
		t.Fatalf("Load() error = %q, want reconcile interval validation", err.Error())
	}
}

func TestLoadRejectsInvalidRuntimeAlertDropReportInterval(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: backtest
symbol: BTCUSDT

grid:
  ratio: "1.01"
  levels: 20
  qty: "0.001"

observability:
  runtime:
    alert_drop_report_sec: -1

backtest:
  data_path: data/binance/BTCUSDT/1m
  initial_base: "0"
  initial_quote: "1000"
  fees:
    maker_rate: "0"
    taker_rate: "0"
  rules:
    min_qty: "0"
    min_notional: "0"
    price_tick: "0"
    qty_step: "0"
`)

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatalf("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "observability.runtime.alert_drop_report_sec must be between 0 and 3600") {
		t.Fatalf("Load() error = %q, want alert_drop_report_sec validation", err.Error())
	}
}

func TestLoadRejectsInvalidExchangeWSBaseURLScheme(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: testnet
symbol: BTCUSDT

grid:
  ratio: "1.01"
  levels: 20
  qty: "0.001"

exchange:
  api_key: "k"
  api_secret: "s"
  ws_base_url: "http://localhost:8080/ws-api/v3"

backtest:
  data_path: data/binance/BTCUSDT/1m
  initial_base: "0"
  initial_quote: "1000"
  fees:
    maker_rate: "0"
    taker_rate: "0"
  rules:
    min_qty: "0"
    min_notional: "0"
    price_tick: "0"
    qty_step: "0"
`)

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatalf("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "exchange ws_base_url scheme must be ws or wss") {
		t.Fatalf("Load() error = %q, want ws url scheme validation", err.Error())
	}
}

func TestLoadBacktestIgnoresExchangeRangeValidation(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: backtest
symbol: BTCUSDT

grid:
  ratio: "1.01"
  levels: 20
  qty: "0.001"

exchange:
  recv_window_ms: -1
  http_timeout_sec: 999
  user_stream_keepalive_sec: 9999
  order_ws_keepalive_sec: 9999
  rest_base_url: "invalid"
  ws_base_url: "invalid"

backtest:
  data_path: data/binance/BTCUSDT/1m
  initial_base: "0"
  initial_quote: "1000"
  fees:
    maker_rate: "0"
    taker_rate: "0"
  rules:
    min_qty: "0"
    min_notional: "0"
    price_tick: "0"
    qty_step: "0"
`)

	_, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil for backtest mode", err)
	}
}

func TestLoadTelegramDisabledIgnoresInvalidAPIBaseURL(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: backtest
symbol: BTCUSDT

grid:
  ratio: "1.01"
  levels: 20
  qty: "0.001"

observability:
  telegram:
    enabled: false
    api_base_url: "://bad-url"

backtest:
  data_path: data/binance/BTCUSDT/1m
  initial_base: "0"
  initial_quote: "1000"
  fees:
    maker_rate: "0"
    taker_rate: "0"
  rules:
    min_qty: "0"
    min_notional: "0"
    price_tick: "0"
    qty_step: "0"
`)

	_, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil when telegram disabled", err)
	}
}

func TestLoadRejectsInvalidStateLockStaleSec(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: backtest
symbol: BTCUSDT

grid:
  ratio: "1.01"
  levels: 20
  qty: "0.001"

state:
  lock_stale_sec: -1

backtest:
  data_path: data/binance/BTCUSDT/1m
  initial_base: "0"
  initial_quote: "1000"
  fees:
    maker_rate: "0"
    taker_rate: "0"
  rules:
    min_qty: "0"
    min_notional: "0"
    price_tick: "0"
    qty_step: "0"
`)

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatalf("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "state.lock_stale_sec must be between 0 and 86400") {
		t.Fatalf("Load() error = %q, want state lock stale validation", err.Error())
	}
}

func TestLoadStateLockTakeoverCanDisableExplicitly(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: backtest
symbol: BTCUSDT

grid:
  ratio: "1.01"
  levels: 20
  qty: "0.001"

state:
  lock_takeover: false

backtest:
  data_path: data/binance/BTCUSDT/1m
  initial_base: "0"
  initial_quote: "1000"
  fees:
    maker_rate: "0"
    taker_rate: "0"
  rules:
    min_qty: "0"
    min_notional: "0"
    price_tick: "0"
    qty_step: "0"
`)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.State.LockTakeover == nil {
		t.Fatalf("state.lock_takeover = nil, want false")
	}
	if *cfg.State.LockTakeover {
		t.Fatalf("state.lock_takeover = true, want false")
	}
}

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o644); err != nil {
		t.Fatalf("write temp config failed: %v", err)
	}
	return path
}
