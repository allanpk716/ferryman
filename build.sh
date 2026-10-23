# build.sh — Ferryman 合并 exe 构建（macOS/Linux 版；与 build.ps1 同构）。
#
# 用法:
#   ./build.sh              # 开发构建（版本号 dev）
#   ./build.sh --release    # 发布构建：git describe --tags --always 注入 main.version，
#                           # 产物 ferryman（darwin arm64 原生；cgo/clang 由 Xcode CLT 提供）
#   ./build.sh --out 路径    # 指定产物路径
#
# macOS 部署形态提醒：daemon 用 launchd 常驻（com.allan.ferryman.plist，serve --no-tray），
# 不需要 Windows 的 Run 键/看门/点火脚本（doctor 相关 4 项 FAIL 属预期平台差异）。
set -euo pipefail
cd "$(dirname "$0")"

out="ferryman"
ver="dev"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --release) ver="$(git describe --tags --always 2>/dev/null | head -1 || true)"; [[ -z "$ver" ]] && ver="dev" ;;
    --out) out="$2"; shift ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
  shift
done

go build -trimpath -ldflags "-s -w -X main.version=${ver}" -o "$out" ./cmd/ferryman
echo "built: $out (version=${ver}, $(go env GOOS)/$(go env GOARCH))"
