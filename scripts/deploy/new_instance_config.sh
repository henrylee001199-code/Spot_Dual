#!/usr/bin/env bash
set -euo pipefail

# 生成单实例配置与可选环境文件。
# 用法：
#   sudo bash scripts/deploy/new_instance_config.sh btcusdt_r1012 BTCUSDT live

if [[ $# -lt 2 ]]; then
  echo "用法: $0 <instance_id> <symbol> [mode]"
  exit 1
fi

INSTANCE_ID="$1"
SYMBOL="$(echo "$2" | tr '[:lower:]' '[:upper:]')"
MODE="${3:-live}"

case "${MODE}" in
  backtest|testnet|live) ;;
  *)
    echo "mode 必须是 backtest/testnet/live"
    exit 1
    ;;
esac

ETC_ROOT="${ETC_ROOT:-/etc/spot-dual}"
STATE_ROOT="${STATE_ROOT:-/var/lib/spot-dual/state}"
CONFIG_PATH="${ETC_ROOT}/${INSTANCE_ID}.yaml"
ENV_PATH="${ETC_ROOT}/${INSTANCE_ID}.env"

mkdir -p "${ETC_ROOT}/instances"

if [[ -f "${CONFIG_PATH}" ]]; then
  echo "配置已存在，拒绝覆盖: ${CONFIG_PATH}"
  exit 1
fi

cat > "${CONFIG_PATH}" <<EOF
mode: ${MODE}
symbol: ${SYMBOL}
instance_id: ${INSTANCE_ID}

grid:
  stop_price: "0"
  ratio: "1.012"
  ratio_step: "0.002"
  ratio_qty_multiple: "1.2"
  sell_ratio: "1.012"
  levels: 20
  shift_levels: 10
  mode: geometric
  qty: "0.001"
  min_qty_multiple: 1

capital:
  base_budget: "0"
  quote_budget: "0"

state:
  dir: "${STATE_ROOT}"
  lock_takeover: true
  lock_stale_sec: 600

circuit_breaker:
  enabled: true
  max_place_failures: 5
  max_cancel_failures: 5
  max_reconnect_failures: 10
  reconnect_cooldown_sec: 30
  reconnect_probe_passes: 1

observability:
  telegram:
    enabled: false
    bot_token: "YOUR_TELEGRAM_BOT_TOKEN"
    chat_id: "YOUR_TELEGRAM_CHAT_ID"
    api_base_url: "https://api.telegram.org"
    timeout_sec: 10
  runtime:
    heartbeat_sec: 60
    reconcile_interval_sec: 60
    alert_drop_report_sec: 60

backtest:
  data_path: /path/to/data_or_dir
  initial_base: "0"
  initial_quote: "1000"
  fees:
    maker_rate: "0.001"
    taker_rate: "0.001"
  rules:
    min_qty: "0"
    min_notional: "0"
    price_tick: "0"
    qty_step: "0"

exchange:
  api_key: "REPLACE_WITH_API_KEY"
  api_secret: "REPLACE_WITH_API_SECRET"
  rest_base_url: "https://api.binance.com"
  ws_base_url: "wss://ws-api.binance.com/ws-api/v3"
  user_stream_auth: signature
  ws_ed25519_private_key_path: ""
  recv_window_ms: 5000
  http_timeout_sec: 15
  user_stream_keepalive_sec: 30
  order_ws_keepalive_sec: 30
EOF

if [[ ! -f "${ENV_PATH}" ]]; then
  cat > "${ENV_PATH}" <<'EOF'
# 示例：按实例设置进程参数
# GOMAXPROCS=1
EOF
fi

echo "实例配置已生成："
echo "  ${CONFIG_PATH}"
echo "  ${ENV_PATH}"
echo "下一步：编辑 API Key 并启动"
echo "  systemctl enable --now spot-dual@${INSTANCE_ID}"
