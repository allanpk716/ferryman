#!/usr/bin/env bash
# stop.sh — E2E 沙箱栈一键拆栈（票07 · tools/e2e_dsh）。
#
# 优雅优先：daemon 走 POST /shutdown（守护内部收口序：watcher/worker 停、pid
# 文件删、面板同收）；web 实例按端口找 win PID 杀进程树。收尾逐口验证无残留，
# 有残留即非零退出（验收标准：拆栈无残留）。幂等：栈未起时跑一遍 = 空操作。
set -u

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
. "$HERE/lib.sh"

RC=0
validate_ports || exit 1

echo "== 拆栈：daemon（优雅 /shutdown 优先） =="
shutdown_daemon "$E2E_DSH_DAEMON_PORT" "$(sandbox_data)/daemon.token" \
  && echo "  /shutdown 已发" || echo "  无在跑 daemon 或 token 缺失，跳过优雅停"
if ! wait_port_free "$E2E_DSH_DAEMON_PORT" 15; then
  pid="$(port_pid "$E2E_DSH_DAEMON_PORT")"
  echo "  15s 未退（PID $pid），强杀进程树"
  [ -n "$pid" ] && kill_pid_tree "$pid"
  wait_port_free "$E2E_DSH_DAEMON_PORT" 5 || RC=1
fi
wait_port_free "$E2E_DSH_PANEL_PORT" 10 || { # 面板与守护同进程，正常随收；兜底强杀
  pid="$(port_pid "$E2E_DSH_PANEL_PORT")"
  [ -n "$pid" ] && kill_pid_tree "$pid"
  wait_port_free "$E2E_DSH_PANEL_PORT" 5 || RC=1
}

echo "== 拆栈：dsh web 实例 =="
pid="$(port_pid "$E2E_DSH_WEB_PORT")"
if [ -n "$pid" ]; then
  echo "  占口 PID $pid，杀进程树"
  kill_pid_tree "$pid"
  wait_port_free "$E2E_DSH_WEB_PORT" 15 || RC=1
else
  echo "  端口 $E2E_DSH_WEB_PORT 已无监听"
fi

rm -f "$(sandbox_run)"/daemon.msys.pid "$(sandbox_run)"/web.msys.pid "$(sandbox_run)"/web.win.pid 2>/dev/null

echo "== 残留核验 =="
for p in "$E2E_DSH_DAEMON_PORT" "$E2E_DSH_PANEL_PORT" "$E2E_DSH_WEB_PORT"; do
  if port_listening "$p"; then
    echo "  残留: 端口 $p 仍被 PID $(port_pid "$p") 监听"
    RC=1
  else
    echo "  干净: 端口 $p"
  fi
done

if [ "$RC" -eq 0 ]; then
  echo "[e2e_dsh] 拆栈完成，进程与端口无残留"
else
  echo "[e2e_dsh] 拆栈完成但有残留（见上），请人工核查" >&2
fi
exit "$RC"
