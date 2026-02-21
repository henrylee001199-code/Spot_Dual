#!/usr/bin/env bash
set -euo pipefail

# 在服务器上初始化目录和运行用户：
# - /opt/spot-dual
# - /etc/spot-dual
# - /var/lib/spot-dual
# - /var/log/spot-dual

APP_USER="${APP_USER:-spotdual}"
APP_GROUP="${APP_GROUP:-spotdual}"
APP_ROOT="${APP_ROOT:-/opt/spot-dual}"
ETC_ROOT="${ETC_ROOT:-/etc/spot-dual}"
DATA_ROOT="${DATA_ROOT:-/var/lib/spot-dual}"
LOG_ROOT="${LOG_ROOT:-/var/log/spot-dual}"

if [[ "${EUID}" -ne 0 ]]; then
  echo "请用 root 执行（例如 sudo bash scripts/deploy/install_server_layout.sh）"
  exit 1
fi

if ! getent group "${APP_GROUP}" >/dev/null 2>&1; then
  groupadd --system "${APP_GROUP}"
fi

if ! id -u "${APP_USER}" >/dev/null 2>&1; then
  useradd \
    --system \
    --home-dir "${APP_ROOT}" \
    --shell /usr/sbin/nologin \
    --gid "${APP_GROUP}" \
    "${APP_USER}"
fi

mkdir -p \
  "${APP_ROOT}/releases" \
  "${APP_ROOT}/shared" \
  "${APP_ROOT}/current" \
  "${ETC_ROOT}/instances" \
  "${DATA_ROOT}/state" \
  "${LOG_ROOT}"

touch "${LOG_ROOT}/gridbot.log"

chown -R "${APP_USER}:${APP_GROUP}" "${APP_ROOT}" "${ETC_ROOT}" "${DATA_ROOT}" "${LOG_ROOT}"
chmod 750 "${APP_ROOT}" "${APP_ROOT}/releases" "${APP_ROOT}/shared" "${ETC_ROOT}" "${ETC_ROOT}/instances" "${DATA_ROOT}" "${DATA_ROOT}/state"
chmod 640 "${LOG_ROOT}/gridbot.log"

echo "服务器目录初始化完成："
echo "  APP_ROOT=${APP_ROOT}"
echo "  ETC_ROOT=${ETC_ROOT}"
echo "  DATA_ROOT=${DATA_ROOT}"
echo "  LOG_ROOT=${LOG_ROOT}"
