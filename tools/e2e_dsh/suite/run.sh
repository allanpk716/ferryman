#!/usr/bin/env bash
# run.sh — E2E 全链剧本一键入口（tools/e2e_dsh/suite；票08 压缩链＋票03 拓扑矩阵）。
#
#   bash tools/e2e_dsh/suite/run.sh
#
# = setup.sh（栈整备：起栈/插件换装/明文转录/web2 profile/秒级参数/重启 daemon+web×2）
#   → driver.mjs（Playwright 三相剧本：A 拓扑矩阵 T1-T8@web1 → B 跨 profile T6@web2
#   → C 票08 压缩链 15 项@web1）→ 汇总退出码。全程零人工介入；幂等可重复跑
#   （每次剧本新建会话，不依赖上次残留）。断言失败时诊断落
#   <沙箱>/logs/e2e-driver-<runstamp>/（账本行+gate.log+DOM aria 快照+截图+
#   matrix-report.md 矩阵红绿+t5-subagent-probe.md 探针结论）。
#
# 旋钮（与 setup.sh 共享）：E2E_DSH_ROOT／E2E_DSH_DAEMON_PORT／E2E_DSH_PANEL_PORT／
# E2E_DSH_WEB_PORT／E2E_DSH_WEB2_PORT（四口铁闸 25xxx 段，生产口零接触）；
# E2E_SUITE_MIN_PEAK／E2E_SUITE_TTL_S 等（见 setup.sh）。PLAYWRIGHT_MODULE 可指
# playwright 包目录。
#
# 整跑约 30~50 分钟（真模型多轮＋多段闲置/压缩窗等待）；单场景失败=记录后继续。
# 结束态：daemon 已被剧本停掉（降级乙断言所需），web×2 实例与浏览器上下文留存；
# 重跑本脚本即恢复（setup.sh 幂等重启 daemon）；彻底拆栈走 ../stop.sh。
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STACK_DIR="$(cd "$HERE/.." && pwd)"
# shellcheck disable=SC1091
. "$STACK_DIR/lib.sh"   # 端口/进程小件（拆 web2 用）

# web2 实例（票03 跨 profile 相）随剧本收尾拆除：stop.sh 的拆栈契约只认三口，
# 第二实例不外泄——EXIT 时按口清（含失败/中断路径）。
E2E_DSH_WEB2_PORT="${E2E_DSH_WEB2_PORT:-25903}"
cleanup_web2() {
  local pid
  pid="$(port_pid "$E2E_DSH_WEB2_PORT")"
  if [ -n "$pid" ]; then
    kill_pid_tree "$pid"
    echo "[suite] web2 实例（:$E2E_DSH_WEB2_PORT）已随剧本收尾拆除"
  fi
  rm -f "$(sandbox_run)/web2.msys.pid" "$(sandbox_run)/web2.win.pid" 2>/dev/null
}
trap cleanup_web2 EXIT

bash "$HERE/setup.sh" || exit 1

ENVFILE="$(. "$HERE/../lib.sh" >/dev/null 2>&1; sandbox_run)/env.driver"
[ -f "$ENVFILE" ] || { echo "[suite] 缺 $ENVFILE（setup.sh 未跑成?）" >&2; exit 1; }
set -a
# shellcheck disable=SC1090
. "$ENVFILE"
set +a

node "$HERE/driver.mjs"
rc=$?
if [ "$rc" -ne 0 ]; then
  echo "[suite] 剧本未跑绿（rc=$rc）——诊断目录见上方日志；排查后重跑本脚本即可" >&2
fi
exit "$rc"
