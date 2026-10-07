#!/usr/bin/env bash
# health.sh — E2E 沙箱栈健康检查（票07 · tools/e2e_dsh）。
#
# 三项：①daemon /stats 带 token 200；②面板口监听；③web 实例口监听且 HTTP
# 有应答。全绿退出 0，任一红退出 1（供 start.sh 收尾与 CI 复用）。
set -u

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
. "$HERE/lib.sh"

FAIL=0
validate_ports || exit 1

# ① daemon /stats
TOKEN_FILE="$(sandbox_data)/daemon.token"
if daemon_stats_ok "$E2E_DSH_DAEMON_PORT" "$TOKEN_FILE"; then
  token="$(tr -d ' \t\r\n' < "$TOKEN_FILE")"
  ver="$(curl -s -m 5 -H "Authorization: Bearer $token" \
    "http://127.0.0.1:$E2E_DSH_DAEMON_PORT/stats" 2>/dev/null \
    | grep -o '"version":"[^"]*"' | head -1)"
  echo "  ok: daemon /stats 200（${ver:-version 字段未取到}）"
else
  echo "  FAIL: daemon /stats 未就绪（端口 $E2E_DSH_DAEMON_PORT，token $TOKEN_FILE）"
  FAIL=1
fi

# ② 面板口
if port_listening "$E2E_DSH_PANEL_PORT"; then
  echo "  ok: 面板口 $E2E_DSH_PANEL_PORT 监听中"
else
  echo "  FAIL: 面板口 $E2E_DSH_PANEL_PORT 无监听"
  FAIL=1
fi

# ③ web 实例口
if port_listening "$E2E_DSH_WEB_PORT"; then
  if web_http_ok "$E2E_DSH_WEB_PORT"; then
    echo "  ok: web 实例口 $E2E_DSH_WEB_PORT 监听且 HTTP 应答"
  else
    echo "  FAIL: web 实例口 $E2E_DSH_WEB_PORT 监听但 HTTP 无应答"
    FAIL=1
  fi
else
  echo "  FAIL: web 实例口 $E2E_DSH_WEB_PORT 无监听"
  FAIL=1
fi

[ "$FAIL" -eq 0 ] && echo "[e2e_dsh] 健康检查全绿" || echo "[e2e_dsh] 健康检查有红项" >&2
exit "$FAIL"
