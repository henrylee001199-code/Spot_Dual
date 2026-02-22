#!/usr/bin/env bash
set -euo pipefail

# 本地发布准备脚本：
# 1) 运行测试
# 2) 构建 Linux amd64 二进制
# 3) 组装发布目录并生成 SHA256
# 4) 打包为 tar.gz

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_CMD="${GO_CMD:-go}"
VERSION="${1:-$(git -C "${ROOT_DIR}" rev-parse --short HEAD)}"
PACKAGE_NAME="spot-dual_${VERSION}_linux_amd64"
DIST_DIR="${ROOT_DIR}/dist"
PKG_DIR="${DIST_DIR}/${PACKAGE_NAME}"
ARCHIVE_PATH="${DIST_DIR}/${PACKAGE_NAME}.tar.gz"

# 避免 macOS 扩展属性进入 tar，减少 Linux 解压告警与 ._ 文件噪音。
export COPYFILE_DISABLE=1
export COPY_EXTENDED_ATTRIBUTES_DISABLE=1

echo "[1/4] 运行测试"
cd "${ROOT_DIR}"
"${GO_CMD}" test ./...

echo "[2/4] 构建二进制"
rm -rf "${PKG_DIR}" "${ARCHIVE_PATH}"
mkdir -p "${PKG_DIR}/bin" "${PKG_DIR}/config" "${PKG_DIR}/scripts" "${PKG_DIR}/deploy"

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "${GO_CMD}" build -trimpath -ldflags="-s -w" -o "${PKG_DIR}/bin/gridbot" ./cmd/gridbot
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "${GO_CMD}" build -trimpath -ldflags="-s -w" -o "${PKG_DIR}/bin/testnetcheck" ./cmd/testnetcheck

echo "[3/4] 组装发布文件"
cp "${ROOT_DIR}/config/config.yaml" "${PKG_DIR}/config/config.yaml"

if [[ -f "${ROOT_DIR}/config/backtest.btcusdt.6m.yaml" ]]; then
  cp "${ROOT_DIR}/config/backtest.btcusdt.6m.yaml" "${PKG_DIR}/config/backtest.btcusdt.6m.yaml"
fi

cat > "${PKG_DIR}/scripts/run_testnet_check.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
"${ROOT_DIR}/bin/testnetcheck" -config "${ROOT_DIR}/config/config.yaml" -check all
EOF
chmod +x "${PKG_DIR}/scripts/run_testnet_check.sh"

cat > "${PKG_DIR}/scripts/run_gridbot.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
"${ROOT_DIR}/bin/gridbot" -config "${ROOT_DIR}/config/config.yaml"
EOF
chmod +x "${PKG_DIR}/scripts/run_gridbot.sh"

if [[ -d "${ROOT_DIR}/deploy" ]]; then
  cp -a "${ROOT_DIR}/deploy/." "${PKG_DIR}/deploy/"
fi

if [[ -d "${ROOT_DIR}/scripts/deploy" ]]; then
  mkdir -p "${PKG_DIR}/scripts/deploy"
  cp -a "${ROOT_DIR}/scripts/deploy/." "${PKG_DIR}/scripts/deploy/"
  find "${PKG_DIR}/scripts/deploy" -type f -name "*.sh" -exec chmod +x {} \;
fi

find "${PKG_DIR}" -type f -name '._*' -delete

(cd "${PKG_DIR}" && shasum -a 256 bin/gridbot bin/testnetcheck config/config.yaml > SHA256SUMS)

echo "[4/4] 生成压缩包"
mkdir -p "${DIST_DIR}"
tar -C "${DIST_DIR}" -czf "${ARCHIVE_PATH}" "${PACKAGE_NAME}"

echo "发布准备完成:"
echo "  目录: ${PKG_DIR}"
echo "  压缩包: ${ARCHIVE_PATH}"
echo "  版本: ${VERSION}"
