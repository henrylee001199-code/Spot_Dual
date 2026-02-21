#!/usr/bin/env bash
set -euo pipefail

# 安装 systemd 模板：
# - spot-dual@.service（多实例）
# - spot-dual.target（实例编组）

if [[ "${EUID}" -ne 0 ]]; then
  echo "请用 root 执行（例如 sudo bash scripts/deploy/install_systemd_units.sh）"
  exit 1
fi

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SYSTEMD_DIR="${SYSTEMD_DIR:-/etc/systemd/system}"

install -m 644 "${ROOT_DIR}/deploy/systemd/spot-dual@.service" "${SYSTEMD_DIR}/spot-dual@.service"
install -m 644 "${ROOT_DIR}/deploy/systemd/spot-dual.target" "${SYSTEMD_DIR}/spot-dual.target"

systemctl daemon-reload

echo "systemd 模板安装完成："
echo "  ${SYSTEMD_DIR}/spot-dual@.service"
echo "  ${SYSTEMD_DIR}/spot-dual.target"
echo "后续可用示例："
echo "  systemctl enable --now spot-dual@btcusdt_r1012"
echo "  systemctl status spot-dual@btcusdt_r1012"
