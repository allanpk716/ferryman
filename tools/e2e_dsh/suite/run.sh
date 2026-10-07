#!/usr/bin/env bash
# run.sh — 票08 E2E 全链剧本一键入口（tools/e2e_dsh/suite）。
#
#   bash tools/e2e_dsh/suite/run.sh
#
# = setup.sh（栈整备：起栈/插件换装/秒级参数/重启 daemon+web）→ driver.mjs
#   （Playwright 全链断言）→ 汇总退出码。全程零人工介入；幂等可重复跑
#   （每次剧本新建会话，不依赖上次残留）。断言失败时诊断落
#   <沙箱>/logs/e2e-driver-<runstamp>/（账本行+gate.log+DOM aria 快照+截图）。
#
# 旋钮（与 setup.sh 共享）：E2E_DSH_ROOT／E2E_DSH_DAEMON_PORT／E2E_DSH_PANEL_PORT／
# E2E_DSH_WEB_PORT（三口铁闸 25xxx 段，生产口零接触）；E2E_SUITE_MIN_PEAK／
# E2E_SUITE_TTL_S 等（见 setup.sh）。PLAYWRIGHT_MODULE 可指 playwright 包目录。
#
# 结束态：daemon 已被剧本停掉（降级乙断言所需），web 实例与浏览器上下文留存；
# 重跑本脚本即恢复（setup.sh 幂等重启 daemon）；彻底拆栈走 ../stop.sh。
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

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
