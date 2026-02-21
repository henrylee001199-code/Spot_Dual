#!/usr/bin/env bash
set -euo pipefail

# 发布压缩包到服务器并切换 current 软链接。
# 用法：
#   sudo bash scripts/deploy/deploy_release.sh /tmp/spot-dual_v0.1.0_linux_amd64.tar.gz

if [[ "${EUID}" -ne 0 ]]; then
  echo "请用 root 执行（例如 sudo bash scripts/deploy/deploy_release.sh <archive>）"
  exit 1
fi

if [[ $# -lt 1 ]]; then
  echo "用法: $0 <archive.tar.gz> [release_name]"
  exit 1
fi

ARCHIVE_PATH="$1"
RELEASE_NAME="${2:-}"

if [[ ! -f "${ARCHIVE_PATH}" ]]; then
  echo "压缩包不存在: ${ARCHIVE_PATH}"
  exit 1
fi

APP_USER="${APP_USER:-spotdual}"
APP_GROUP="${APP_GROUP:-spotdual}"
APP_ROOT="${APP_ROOT:-/opt/spot-dual}"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

tar -xzf "${ARCHIVE_PATH}" -C "${TMP_DIR}"
EXTRACTED_DIR="$(find "${TMP_DIR}" -mindepth 1 -maxdepth 1 -type d | head -n 1)"
if [[ -z "${EXTRACTED_DIR}" ]]; then
  echo "压缩包内容异常，未找到发布目录"
  exit 1
fi

if [[ -z "${RELEASE_NAME}" ]]; then
  RELEASE_NAME="$(basename "${EXTRACTED_DIR}")"
fi

DEST_DIR="${APP_ROOT}/releases/${RELEASE_NAME}"
mkdir -p "${APP_ROOT}/releases"
rm -rf "${DEST_DIR}"
mkdir -p "${DEST_DIR}"
cp -a "${EXTRACTED_DIR}/." "${DEST_DIR}/"

ln -sfn "${DEST_DIR}" "${APP_ROOT}/current"
chown -R "${APP_USER}:${APP_GROUP}" "${DEST_DIR}"
chown -h "${APP_USER}:${APP_GROUP}" "${APP_ROOT}/current"

echo "发布完成："
echo "  release=${DEST_DIR}"
echo "  current -> ${DEST_DIR}"
