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
  config.yaml
state/            # 运行时状态（默认）
```

---

## 2. 环境要求

- Go 1.21+
- Binance API Key（testnet 或 live）

> 本机若 `go` 不在 PATH，可用绝对路径：`/usr/local/go/bin/go`

---

## 3. 快速开始

### 3.1 编辑配置

按你的模式填写：

- `mode`: `backtest` / `testnet` / `live`
- `exchange.api_key` / `exchange.api_secret`
- `symbol`、`grid.*` 参数
- `instance_id`：建议使用 `coin_ratio` 命名（如 `btcusdt_r1012`）

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
配置解析开启了 `KnownFields(true)`，请检查 YAML 字段名是否与 `config/config.yaml` 一致。

### Q3: 实盘重启后是否能恢复
会从 `state` 目录加载网格状态与开单快照，并执行 reconcile。

---

## 9. License

当前仓库未声明许可证（可按你的需要补充 `LICENSE` 文件）。

---

## 10. 本地发布准备

可用一键脚本在本地完成“测试 + Linux 二进制构建 + 打包”：

```bash
bash scripts/release_local.sh
```

也可以传入版本号：

```bash
bash scripts/release_local.sh v0.1.0
```

产物位于 `dist/` 目录，包含：

- `spot-dual_<version>_linux_amd64/`
- `spot-dual_<version>_linux_amd64.tar.gz`

发布目录中会包含：

- `bin/gridbot`
- `bin/testnetcheck`
- `config/config.yaml`
- `scripts/run_gridbot.sh`
- `scripts/run_testnet_check.sh`
- `scripts/deploy/*.sh`
- `deploy/systemd/*`
- `SHA256SUMS`

---

## 11. 服务器部署（多实例）

以下流程适用于 GCP/AWS Lightsail 的 Ubuntu 主机。

### 11.1 初始化服务器目录与用户（只需一次）

```bash
sudo bash scripts/deploy/install_server_layout.sh
sudo bash scripts/deploy/install_systemd_units.sh
```

默认会创建：

- 程序目录：`/opt/spot-dual`
- 配置目录：`/etc/spot-dual`
- 状态目录：`/var/lib/spot-dual/state`
- 运行用户：`spotdual`

### 11.2 发布新版本

先在本地构建发布包：

```bash
bash scripts/release_local.sh v0.1.0
```

把 `dist/spot-dual_v0.1.0_linux_amd64.tar.gz` 上传到服务器后执行：

```bash
sudo bash scripts/deploy/deploy_release.sh /tmp/spot-dual_v0.1.0_linux_amd64.tar.gz
```

### 11.3 创建实例配置

示例：创建 10 个实例（同账号多实例，不同 `instance_id`，采用 `coin_ratio` 命名）：

```bash
for r in 1008 1010 1012 1014 1016 1018 1020 1022 1024 1026; do
  sudo bash scripts/deploy/new_instance_config.sh "btcusdt_r${r}" BTCUSDT live
done
```

然后逐个编辑 `/etc/spot-dual/btcusdt_r1012.yaml` 等配置，重点确认：

- `exchange.api_key` / `exchange.api_secret`
- `capital.base_budget` / `capital.quote_budget`
- `symbol` 与 `grid.*`

### 11.4 启动与运维

```bash
sudo systemctl enable --now spot-dual@btcusdt_r1012
sudo systemctl status spot-dual@btcusdt_r1012
sudo journalctl -u spot-dual@btcusdt_r1012 -f
```

批量启动：

```bash
sudo systemctl enable --now spot-dual@btcusdt_r1012 spot-dual@btcusdt_r1014 spot-dual@btcusdt_r1016
```

滚动重启（升级后）：

```bash
sudo systemctl restart spot-dual@btcusdt_r1012
```
