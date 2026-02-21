# Spot_Dual (grid-trading)

一个基于 Go 的 Binance 现货网格策略项目，支持：

- **回测**（`mode: backtest`）
- **测试网实盘**（`mode: testnet`）
- **生产实盘**（`mode: live`）

核心策略为 `SpotDual`：买卖双边几何网格，带状态持久化、重连对账、断路器和告警能力。

---

## 1. 项目结构

```text
cmd/
  gridbot/        # 主程序入口（回测/实盘）
  marketdata/     # 拉取K线并存储为jsonl
  testnetcheck/   # 交易链路与策略自检
internal/
  strategy/       # SpotDual策略
  engine/         # live/backtest 执行引擎
  exchange/       # 交易所实现（Binance）
  safety/         # 断路器、保护执行器
  store/          # 状态与运行状态持久化
  config/         # 配置加载与校验
config/
  config.example.yaml
state/            # 运行时状态（默认）
```

---

## 2. 环境要求

- Go 1.21+
- Binance API Key（testnet 或 live）

> 本机若 `go` 不在 PATH，可用绝对路径：`/usr/local/go/bin/go`

---

## 3. 快速开始

### 3.1 复制配置

```bash
cp config/config.example.yaml config/config.yaml
```

按你的模式填写：

- `mode`: `backtest` / `testnet` / `live`
- `exchange.api_key` / `exchange.api_secret`
- `symbol`、`grid.*` 参数

### 3.2 运行测试

```bash
/usr/local/go/bin/go test ./...
```

### 3.3 启动主程序

```bash
/usr/local/go/bin/go run ./cmd/gridbot -config config/config.yaml
```

---

## 4. 典型用法

### 4.1 回测

1) 准备数据（jsonl 文件或按天分片目录）

可用 `marketdata` 拉取：

```bash
/usr/local/go/bin/go run ./cmd/marketdata \
  -symbol BTCUSDT \
  -interval 1m \
  -months 6 \
  -out-dir data/binance
```

2) 配置 `mode: backtest`，并设置：

- `backtest.data_path`
- `backtest.initial_base`
- `backtest.initial_quote`
- `backtest.fees.*`
- `backtest.rules.*`

3) 运行：

```bash
/usr/local/go/bin/go run ./cmd/gridbot -config config/config.yaml
```

程序会输出回测 summary（收益、回撤、资金占用、手续费等）。

---

### 4.2 Testnet 自检（强烈建议先跑）

```bash
/usr/local/go/bin/go run ./cmd/testnetcheck -config config/config.yaml -check all
```

可选项：

- `-check default|all|bootstrap|preflight,lifecycle,...`
- `-timeout-sec 180`
- `-out-json report.json`

---

### 4.3 实盘/测试网运行

```bash
/usr/local/go/bin/go run ./cmd/gridbot -config config/config.yaml
```

策略会在 `state/{mode}/{symbol}/{instance_id}` 下维护状态（网格状态、开单快照、运行状态、锁文件）。

---

## 5. 关键配置说明（节选）

- `grid.ratio`：买网格几何比率（>1）
- `grid.sell_ratio`：卖网格几何比率（>1）
- `grid.levels`：买侧层数
- `grid.shift_levels`：卖侧层数/上移窗口
- `grid.qty`：基础下单数量（后续会经过规则归一化）
- `grid.min_qty_multiple`：最小数量倍数保护
- `grid.stop_price`：大于该价格时策略停止（0=禁用）
- `capital.base_budget`：单实例可用的 base 资产预算（0=不限制）
- `capital.quote_budget`：单实例可用的 quote 资产预算（0=不限制）

风控/运行：

- `circuit_breaker.*`：下单/撤单/重连断路器
- `observability.runtime.reconcile_interval_sec`：周期对账间隔
- `state.lock_takeover`：是否接管陈旧锁

---

## 6. 重要行为说明

- 下单前会做规则归一化：
  - `qty_step` 向下/向上处理（按场景）
  - `min_qty` 保护
  - `min_notional` 保护（按 `qty >= minNotional/price` 计算）
- Live 引擎支持：
  - 用户流中断重连
  - reconnect 断路器（open/half-open/closed）
  - 重连后对账与缺失订单修复

---

## 7. 风险提示

- 本项目是交易系统，配置错误会造成真实资金风险。
- 上线前建议顺序：
  1) 回测
  2) testnet 全量自检
  3) 小资金 live 灰度
- 建议开启 Telegram 告警并监控运行状态。

---

## 8. 常见问题

### Q1: `go: command not found`
使用绝对路径：

```bash
/usr/local/go/bin/go version
```

### Q2: 配置报错（字段未知）
配置解析开启了 `KnownFields(true)`，请检查 YAML 字段名是否与 `config.example.yaml` 一致。

### Q3: 实盘重启后是否能恢复
会从 `state` 目录加载网格状态与开单快照，并执行 reconcile。

---

## 9. License

当前仓库未声明许可证（可按你的需要补充 `LICENSE` 文件）。
