package config

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/shopspring/decimal"
	"gopkg.in/yaml.v3"
)

type Mode string

type GridMode string
type UserStreamAuth string

const (
	ModeBacktest Mode = "backtest"
	ModeTestnet  Mode = "testnet"
	ModeLive     Mode = "live"
)

const (
	GridGeo GridMode = "geometric"
)

const (
	UserStreamAuthSignature UserStreamAuth = "signature"
	UserStreamAuthSession   UserStreamAuth = "session"
)

type Config struct {
	Mode           Mode                 `yaml:"mode"`
	Symbol         string               `yaml:"symbol"`
	InstanceID     string               `yaml:"instance_id"`
	Grid           GridConfig           `yaml:"grid"`
	Backtest       BacktestConfig       `yaml:"backtest"`
	Exchange       ExchangeConfig       `yaml:"exchange"`
	State          StateConfig          `yaml:"state"`
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker"`
	Observability  ObservabilityConfig  `yaml:"observability"`
}

type GridConfig struct {
	StopPrice      Decimal  `yaml:"stop_price"`
	Ratio          Decimal  `yaml:"ratio"`
	RatioStep      *Decimal `yaml:"ratio_step"`
	SellRatio      Decimal  `yaml:"sell_ratio"`
	Levels         int      `yaml:"levels"`
	ShiftLevels    int      `yaml:"shift_levels"`
	Mode           GridMode `yaml:"mode"`
	Qty            Decimal  `yaml:"qty"`
	MinQtyMultiple int64    `yaml:"min_qty_multiple"`
}

type BacktestConfig struct {
	DataPath     string        `yaml:"data_path"`
	InitialBase  Decimal       `yaml:"initial_base"`
	InitialQuote Decimal       `yaml:"initial_quote"`
	Fees         BacktestFees  `yaml:"fees"`
	Rules        BacktestRules `yaml:"rules"`
}

type BacktestFees struct {
	MakerRate Decimal `yaml:"maker_rate"`
	TakerRate Decimal `yaml:"taker_rate"`
}

type BacktestRules struct {
	MinQty      Decimal `yaml:"min_qty"`
	MinNotional Decimal `yaml:"min_notional"`
	PriceTick   Decimal `yaml:"price_tick"`
	QtyStep     Decimal `yaml:"qty_step"`
}

type ExchangeConfig struct {
	APIKey                 string         `yaml:"api_key"`
	APISecret              string         `yaml:"api_secret"`
	RestBaseURL            string         `yaml:"rest_base_url"`
	WSBaseURL              string         `yaml:"ws_base_url"`
	UserStreamAuth         UserStreamAuth `yaml:"user_stream_auth"`
	WSEd25519KeyPath       string         `yaml:"ws_ed25519_private_key_path"`
	RecvWindowMs           int64          `yaml:"recv_window_ms"`
	HTTPTimeoutSec         int64          `yaml:"http_timeout_sec"`
	UserStreamKeepaliveSec int64          `yaml:"user_stream_keepalive_sec"`
	OrderWSKeepaliveSec    int64          `yaml:"order_ws_keepalive_sec"`
}

type StateConfig struct {
	Dir          string `yaml:"dir"`
	LockTakeover *bool  `yaml:"lock_takeover"`
	LockStaleSec int64  `yaml:"lock_stale_sec"`
}

type CircuitBreakerConfig struct {
	Enabled              bool  `yaml:"enabled"`
	MaxPlaceFailures     int   `yaml:"max_place_failures"`
	MaxCancelFailures    int   `yaml:"max_cancel_failures"`
	MaxReconnectFailures int   `yaml:"max_reconnect_failures"`
	ReconnectCooldownSec int64 `yaml:"reconnect_cooldown_sec"`
	ReconnectProbePasses int   `yaml:"reconnect_probe_passes"`
}

type ObservabilityConfig struct {
	Telegram TelegramConfig `yaml:"telegram"`
	Runtime  RuntimeConfig  `yaml:"runtime"`
}

type TelegramConfig struct {
	Enabled    bool   `yaml:"enabled"`
	BotToken   string `yaml:"bot_token"`
	ChatID     string `yaml:"chat_id"`
	APIBaseURL string `yaml:"api_base_url"`
	TimeoutSec int64  `yaml:"timeout_sec"`
}

type RuntimeConfig struct {
	HeartbeatSec         int64 `yaml:"heartbeat_sec"`
	ReconcileIntervalSec int64 `yaml:"reconcile_interval_sec"`
	AlertDropReportSec   int64 `yaml:"alert_drop_report_sec"`
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return Config{}, fmt.Errorf("config must contain a single YAML document")
		}
		return Config{}, err
	}
	cfg.normalize()
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) normalize() {
	c.Mode = Mode(strings.ToLower(strings.TrimSpace(string(c.Mode))))
	c.Symbol = strings.ToUpper(strings.TrimSpace(c.Symbol))
	c.InstanceID = strings.ToLower(strings.TrimSpace(c.InstanceID))
	c.Grid.Mode = GridMode(strings.ToLower(strings.TrimSpace(string(c.Grid.Mode))))
	c.Exchange.APIKey = strings.TrimSpace(c.Exchange.APIKey)
	c.Exchange.APISecret = strings.TrimSpace(c.Exchange.APISecret)
	c.Exchange.RestBaseURL = strings.TrimSpace(c.Exchange.RestBaseURL)
	c.Exchange.WSBaseURL = strings.TrimSpace(c.Exchange.WSBaseURL)
	c.Exchange.WSEd25519KeyPath = strings.TrimSpace(c.Exchange.WSEd25519KeyPath)
	c.State.Dir = strings.TrimSpace(c.State.Dir)
	c.Backtest.DataPath = strings.TrimSpace(c.Backtest.DataPath)
	c.Observability.Telegram.BotToken = strings.TrimSpace(c.Observability.Telegram.BotToken)
	c.Observability.Telegram.ChatID = strings.TrimSpace(c.Observability.Telegram.ChatID)
	c.Observability.Telegram.APIBaseURL = strings.TrimSpace(c.Observability.Telegram.APIBaseURL)
	auth := strings.ToLower(strings.TrimSpace(string(c.Exchange.UserStreamAuth)))
	if auth == "apikey" {
		auth = "session"
	}
	c.Exchange.UserStreamAuth = UserStreamAuth(auth)
}

func (c *Config) applyDefaults() {
	if c.Mode == "" {
		c.Mode = ModeBacktest
	}
	if c.InstanceID == "" {
		c.InstanceID = "default"
	}
	if c.Grid.Mode == "" {
		c.Grid.Mode = GridGeo
	}
	if c.Grid.MinQtyMultiple == 0 {
		c.Grid.MinQtyMultiple = 1
	}
	if c.Grid.SellRatio.Cmp(decimal.Zero) == 0 {
		c.Grid.SellRatio = c.Grid.Ratio
	}
	if c.Grid.ShiftLevels == 0 && c.Grid.Levels > 0 {
		c.Grid.ShiftLevels = c.Grid.Levels / 2
		if c.Grid.ShiftLevels < 1 {
			c.Grid.ShiftLevels = 1
		}
	}
	if c.Exchange.UserStreamAuth == "" {
		c.Exchange.UserStreamAuth = UserStreamAuthSignature
	}
	if c.Exchange.RecvWindowMs == 0 {
		c.Exchange.RecvWindowMs = 5000
	}
	if c.Exchange.HTTPTimeoutSec == 0 {
		c.Exchange.HTTPTimeoutSec = 15
	}
	if c.Exchange.UserStreamKeepaliveSec == 0 {
		c.Exchange.UserStreamKeepaliveSec = 30
	}
	if c.Exchange.OrderWSKeepaliveSec == 0 {
		c.Exchange.OrderWSKeepaliveSec = 30
	}
	if c.CircuitBreaker.MaxPlaceFailures == 0 {
		c.CircuitBreaker.MaxPlaceFailures = 5
	}
	if c.CircuitBreaker.MaxCancelFailures == 0 {
		c.CircuitBreaker.MaxCancelFailures = 5
	}
	if c.CircuitBreaker.MaxReconnectFailures == 0 {
		c.CircuitBreaker.MaxReconnectFailures = 10
	}
	if c.CircuitBreaker.ReconnectCooldownSec == 0 {
		c.CircuitBreaker.ReconnectCooldownSec = 30
	}
	if c.CircuitBreaker.ReconnectProbePasses == 0 {
		c.CircuitBreaker.ReconnectProbePasses = 1
	}
	if c.State.Dir == "" {
		c.State.Dir = "state"
	}
	if c.State.LockTakeover == nil {
		enabled := true
		c.State.LockTakeover = &enabled
	}
	if c.State.LockStaleSec == 0 {
		c.State.LockStaleSec = 600
	}
	if c.Observability.Telegram.APIBaseURL == "" {
		c.Observability.Telegram.APIBaseURL = "https://api.telegram.org"
	}
	if c.Observability.Telegram.TimeoutSec == 0 {
		c.Observability.Telegram.TimeoutSec = 10
	}
	if c.Observability.Runtime.ReconcileIntervalSec == 0 {
		c.Observability.Runtime.ReconcileIntervalSec = 60
	}
	if c.Observability.Runtime.AlertDropReportSec == 0 {
		c.Observability.Runtime.AlertDropReportSec = 60
	}
	if c.Exchange.RestBaseURL == "" {
		switch c.Mode {
		case ModeTestnet:
			c.Exchange.RestBaseURL = "https://testnet.binance.vision"
		case ModeLive:
			c.Exchange.RestBaseURL = "https://api.binance.com"
		}
	}
	if c.Exchange.WSBaseURL == "" {
		switch c.Mode {
		case ModeTestnet:
			c.Exchange.WSBaseURL = "wss://ws-api.testnet.binance.vision/ws-api/v3"
		case ModeLive:
			c.Exchange.WSBaseURL = "wss://ws-api.binance.com/ws-api/v3"
		}
	}
}

func (c Config) Validate() error {
	switch c.Mode {
	case ModeBacktest, ModeTestnet, ModeLive:
	default:
		return fmt.Errorf("mode must be backtest, testnet, or live")
	}
	if c.Symbol == "" {
		return fmt.Errorf("symbol is required")
	}
	if !isValidSymbol(c.Symbol) {
		return fmt.Errorf("symbol must match [A-Z0-9], length 6..20")
	}
	if !isValidInstanceID(c.InstanceID) {
		return fmt.Errorf("instance_id must match [a-z0-9_-], length 1..24")
	}
	if c.Grid.Levels < 1 {
		return fmt.Errorf("levels must be >= 1")
	}
	if c.Grid.Mode != GridGeo {
		return fmt.Errorf("grid mode must be geometric")
	}
	if c.Grid.ShiftLevels < 1 || c.Grid.ShiftLevels > c.Grid.Levels {
		return fmt.Errorf("shift_levels must be between 1 and levels")
	}
	if c.Grid.StopPrice.Cmp(decimal.Zero) < 0 {
		return fmt.Errorf("grid stop_price must be >= 0")
	}
	if c.Grid.Ratio.Cmp(decimal.Zero) <= 0 {
		return fmt.Errorf("grid ratio must be > 0")
	}
	if c.Grid.Ratio.Cmp(decimal.NewFromInt(1)) <= 0 {
		return fmt.Errorf("grid ratio must be > 1")
	}
	if c.Grid.SellRatio.Cmp(decimal.Zero) <= 0 {
		return fmt.Errorf("grid sell_ratio must be > 0")
	}
	if c.Grid.SellRatio.Cmp(decimal.NewFromInt(1)) <= 0 {
		return fmt.Errorf("grid sell_ratio must be > 1")
	}
	if c.Grid.RatioStep != nil && c.Grid.RatioStep.Cmp(decimal.Zero) < 0 {
		return fmt.Errorf("grid ratio_step must be >= 0")
	}
	if c.Grid.Qty.Cmp(decimal.Zero) <= 0 {
		return fmt.Errorf("qty must be > 0")
	}
	if c.Grid.MinQtyMultiple < 1 {
		return fmt.Errorf("min_qty_multiple must be >= 1")
	}
	if c.Backtest.Fees.MakerRate.Cmp(decimal.Zero) < 0 {
		return fmt.Errorf("backtest fees.maker_rate must be >= 0")
	}
	if c.Backtest.Fees.TakerRate.Cmp(decimal.Zero) < 0 {
		return fmt.Errorf("backtest fees.taker_rate must be >= 0")
	}
	if c.Backtest.Rules.MinQty.Cmp(decimal.Zero) < 0 {
		return fmt.Errorf("backtest rules.min_qty must be >= 0")
	}
	if c.Backtest.Rules.MinNotional.Cmp(decimal.Zero) < 0 {
		return fmt.Errorf("backtest rules.min_notional must be >= 0")
	}
	if c.Backtest.Rules.PriceTick.Cmp(decimal.Zero) < 0 {
		return fmt.Errorf("backtest rules.price_tick must be >= 0")
	}
	if c.Backtest.Rules.QtyStep.Cmp(decimal.Zero) < 0 {
		return fmt.Errorf("backtest rules.qty_step must be >= 0")
	}
	if c.CircuitBreaker.Enabled {
		if c.CircuitBreaker.MaxPlaceFailures < 1 {
			return fmt.Errorf("circuit_breaker.max_place_failures must be >= 1")
		}
		if c.CircuitBreaker.MaxCancelFailures < 1 {
			return fmt.Errorf("circuit_breaker.max_cancel_failures must be >= 1")
		}
		if c.CircuitBreaker.MaxReconnectFailures < 1 {
			return fmt.Errorf("circuit_breaker.max_reconnect_failures must be >= 1")
		}
		if c.CircuitBreaker.ReconnectCooldownSec < 1 || c.CircuitBreaker.ReconnectCooldownSec > 3600 {
			return fmt.Errorf("circuit_breaker.reconnect_cooldown_sec must be between 1 and 3600")
		}
		if c.CircuitBreaker.ReconnectProbePasses < 1 || c.CircuitBreaker.ReconnectProbePasses > 20 {
			return fmt.Errorf("circuit_breaker.reconnect_probe_passes must be between 1 and 20")
		}
	}
	if c.Observability.Runtime.HeartbeatSec < 0 || c.Observability.Runtime.HeartbeatSec > 3600 {
		return fmt.Errorf("observability.runtime.heartbeat_sec must be between 0 and 3600")
	}
	if c.Observability.Runtime.ReconcileIntervalSec < 0 || c.Observability.Runtime.ReconcileIntervalSec > 3600 {
		return fmt.Errorf("observability.runtime.reconcile_interval_sec must be between 0 and 3600")
	}
	if c.Observability.Runtime.ReconcileIntervalSec > 0 && c.Observability.Runtime.ReconcileIntervalSec < 10 {
		return fmt.Errorf("observability.runtime.reconcile_interval_sec must be 0 or >= 10")
	}
	if c.Observability.Runtime.AlertDropReportSec < 0 || c.Observability.Runtime.AlertDropReportSec > 3600 {
		return fmt.Errorf("observability.runtime.alert_drop_report_sec must be between 0 and 3600")
	}
	if c.Observability.Telegram.Enabled {
		if c.Observability.Telegram.BotToken == "" {
			return fmt.Errorf("observability.telegram.bot_token is required when telegram enabled")
		}
		if c.Observability.Telegram.ChatID == "" {
			return fmt.Errorf("observability.telegram.chat_id is required when telegram enabled")
		}
		if c.Observability.Telegram.TimeoutSec < 1 || c.Observability.Telegram.TimeoutSec > 120 {
			return fmt.Errorf("observability.telegram.timeout_sec must be between 1 and 120")
		}
		if err := validateURL(c.Observability.Telegram.APIBaseURL, "http", "https"); err != nil {
			return fmt.Errorf("observability.telegram.api_base_url %v", err)
		}
	}
	if c.State.LockStaleSec < 0 || c.State.LockStaleSec > 86400 {
		return fmt.Errorf("state.lock_stale_sec must be between 0 and 86400")
	}
	if c.Mode == ModeBacktest && c.Backtest.DataPath == "" {
		return fmt.Errorf("backtest data_path is required")
	}
	if c.Mode != ModeBacktest {
		if c.Exchange.APIKey == "" || c.Exchange.APISecret == "" {
			return fmt.Errorf("exchange api_key/api_secret are required for %s mode", c.Mode)
		}
		if c.Exchange.RestBaseURL == "" || c.Exchange.WSBaseURL == "" {
			return fmt.Errorf("exchange rest_base_url/ws_base_url are required for %s mode", c.Mode)
		}
		if c.Exchange.RecvWindowMs < 1 || c.Exchange.RecvWindowMs > 60000 {
			return fmt.Errorf("exchange recv_window_ms must be between 1 and 60000")
		}
		if c.Exchange.HTTPTimeoutSec < 1 || c.Exchange.HTTPTimeoutSec > 120 {
			return fmt.Errorf("exchange http_timeout_sec must be between 1 and 120")
		}
		if c.Exchange.UserStreamKeepaliveSec < 1 || c.Exchange.UserStreamKeepaliveSec > 3600 {
			return fmt.Errorf("exchange user_stream_keepalive_sec must be between 1 and 3600")
		}
		if c.Exchange.OrderWSKeepaliveSec < 1 || c.Exchange.OrderWSKeepaliveSec > 300 {
			return fmt.Errorf("exchange order_ws_keepalive_sec must be between 1 and 300")
		}
		if err := validateURL(c.Exchange.RestBaseURL, "http", "https"); err != nil {
			return fmt.Errorf("exchange rest_base_url %v", err)
		}
		if err := validateURL(c.Exchange.WSBaseURL, "ws", "wss"); err != nil {
			return fmt.Errorf("exchange ws_base_url %v", err)
		}
		if c.Exchange.UserStreamAuth != UserStreamAuthSignature && c.Exchange.UserStreamAuth != UserStreamAuthSession {
			return fmt.Errorf("exchange user_stream_auth must be signature or session")
		}
		if c.Exchange.UserStreamAuth == UserStreamAuthSession && c.Exchange.WSEd25519KeyPath == "" {
			return fmt.Errorf("exchange ws_ed25519_private_key_path is required for session auth")
		}
	}
	return nil
}

func isValidInstanceID(v string) bool {
	if len(v) < 1 || len(v) > 24 {
		return false
	}
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func isValidSymbol(v string) bool {
	if len(v) < 6 || len(v) > 20 {
		return false
	}
	for _, r := range v {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func validateURL(raw string, schemes ...string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("must be a valid URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("must include scheme and host")
	}
	for _, s := range schemes {
		if parsed.Scheme == s {
			return nil
		}
	}
	return fmt.Errorf("scheme must be %s", strings.Join(schemes, " or "))
}
