package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
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
	if cfg.Grid.RatioStep != nil {
		t.Fatalf("grid.ratio_step = %v, want nil when omitted", cfg.Grid.RatioStep)
	}
	if !cfg.Grid.RatioQtyMultiple.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("grid.ratio_qty_multiple = %s, want 1", cfg.Grid.RatioQtyMultiple.String())
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

func TestLoadRejectsInvalidRatioStep(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: backtest
symbol: BTCUSDT

grid:
  ratio: "1.01"
  ratio_step: "-0.001"
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
	if !strings.Contains(err.Error(), "grid ratio_step must be >= 0") {
		t.Fatalf("Load() error = %q, want ratio_step validation", err.Error())
	}
}

func TestLoadAllowsZeroRatioStep(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: backtest
symbol: BTCUSDT

grid:
  ratio: "1.01"
  ratio_step: "0"
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
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Grid.RatioStep == nil {
		t.Fatalf("grid.ratio_step = nil, want explicit zero")
	}
	if !cfg.Grid.RatioStep.Equal(decimal.Zero) {
		t.Fatalf("grid.ratio_step = %s, want 0", cfg.Grid.RatioStep.String())
	}
}

func TestLoadParsesPositiveRatioStep(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: backtest
symbol: BTCUSDT

grid:
  ratio: "1.01"
  ratio_step: "0.0025"
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
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Grid.RatioStep == nil {
		t.Fatalf("grid.ratio_step = nil, want explicit 0.0025")
	}
	want := decimal.RequireFromString("0.0025")
	if !cfg.Grid.RatioStep.Equal(want) {
		t.Fatalf("grid.ratio_step = %s, want %s", cfg.Grid.RatioStep.String(), want.String())
	}
}

func TestLoadParsesRatioQtyMultiple(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: backtest
symbol: BTCUSDT

grid:
  ratio: "1.01"
  ratio_qty_multiple: "1.2"
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
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if !cfg.Grid.RatioQtyMultiple.Equal(decimal.RequireFromString("1.2")) {
		t.Fatalf("grid.ratio_qty_multiple = %s, want 1.2", cfg.Grid.RatioQtyMultiple.String())
	}
}

func TestLoadParsesCapitalBudgets(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: backtest
symbol: BTCUSDT

grid:
  ratio: "1.01"
  levels: 20
  qty: "0.001"

capital:
  base_budget: "1.5"
  quote_budget: "200"

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
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if !cfg.Capital.BaseBudget.Equal(decimal.RequireFromString("1.5")) {
		t.Fatalf("capital.base_budget = %s, want 1.5", cfg.Capital.BaseBudget.String())
	}
	if !cfg.Capital.QuoteBudget.Equal(decimal.RequireFromString("200")) {
		t.Fatalf("capital.quote_budget = %s, want 200", cfg.Capital.QuoteBudget.String())
	}
}

func TestLoadRejectsNegativeCapitalBudget(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: backtest
symbol: BTCUSDT

grid:
  ratio: "1.01"
  levels: 20
  qty: "0.001"

capital:
  quote_budget: "-1"

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
	if !strings.Contains(err.Error(), "capital quote_budget must be >= 0") {
		t.Fatalf("Load() error = %q, want capital quote_budget validation", err.Error())
	}
}

func TestLoadRejectsInvalidRatioQtyMultiple(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: backtest
symbol: BTCUSDT

grid:
  ratio: "1.01"
  ratio_qty_multiple: "-1"
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
	if !strings.Contains(err.Error(), "grid ratio_qty_multiple must be > 0") {
		t.Fatalf("Load() error = %q, want ratio_qty_multiple validation", err.Error())
	}
}

func TestLoadRejectsRemovedRegimeField(t *testing.T) {
	cfgPath := writeTempConfig(t, `
mode: backtest
symbol: BTCUSDT

grid:
  ratio: "1.01"
  levels: 20
  qty: "0.001"
  regime:
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
	if !strings.Contains(err.Error(), "field regime not found") {
		t.Fatalf("Load() error = %q, want unknown removed regime field message", err.Error())
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
