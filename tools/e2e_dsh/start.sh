#!/usr/bin/env bash
# start.sh — E2E 沙箱栈一键起栈（票07 · tools/e2e_dsh）。
#
# 起 ①沙箱 daemon（本仓 go build 测试构建，端口/数据目录全在沙箱内）＋
# ②隔离 DSH web 实例（DSH_HOME=整目录副本，插件 daemonURL 经 FERRYMAN_PORT/
# FERRYMAN_TOKEN_FILE 环境注入指沙箱）。幂等：栈已在则复用，不叠进程。
#
# 用法: bash tools/e2e_dsh/start.sh
# 旋钮: E2E_DSH_ROOT / E2E_DSH_DAEMON_PORT / E2E_DSH_PANEL_PORT / E2E_DSH_WEB_PORT
#       DSH_CLI / DSH_HOME_SRC（复制源，只读）/ E2E_DSH_REBUILD_HOME=1（重建副本）
#       E2E_DSH_KEEP_ON_FAIL=1（失败不自动拆栈，留现场排障）
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
. "$HERE/lib.sh"

fail_teardown() {
  echo "[e2e_dsh] 起栈失败。" >&2
  if [ "${E2E_DSH_KEEP_ON_FAIL:-0}" != "1" ]; then
    echo "[e2e_dsh] 自动拆栈（E2E_DSH_KEEP_ON_FAIL=1 可保留现场）…" >&2
    bash "$HERE/stop.sh" >&2 || true
  fi
  exit 1
}

echo "== [0/5] 端口铁闸 =="
validate_ports || exit 1
echo "  daemon=$E2E_DSH_DAEMON_PORT panel=$E2E_DSH_PANEL_PORT web=$E2E_DSH_WEB_PORT（25xxx 段校验通过）"

ROOT_MIX="$(mix_path "$(sandbox_root)")"
LOGS="$(sandbox_logs)"
RUN="$(sandbox_run)"
mkdir -p "$ROOT_MIX/bin" "$LOGS" "$RUN" \
  "$ROOT_MIX/watch/cc-projects" "$ROOT_MIX/watch/codex-sessions"

echo "== [1/5] 沙箱 dsh home 备料（复制源只读：$DSH_HOME_SRC） =="
HOMEDIR="$(sandbox_home)"
HOME_MIX="$(mix_path "$HOMEDIR")"
if [ ! -f "$HOMEDIR/.e2e-prepared" ] || [ "${E2E_DSH_REBUILD_HOME:-0}" = "1" ]; then
  rm -rf "$HOMEDIR"
  prepare_sandbox_home "$DSH_HOME_SRC" "$HOMEDIR" || fail_teardown
  date -u +"%Y-%m-%dT%H:%M:%SZ" > "$HOMEDIR/.e2e-prepared"
  echo "  副本已整备：$HOMEDIR"
else
  echo "  副本已在，复用（E2E_DSH_REBUILD_HOME=1 可重建）：$HOMEDIR"
fi
# 生成面每次都重写：与 lib.sh 内容契约保持同步（幂等再入安全）
strip_llm_deepseek "$HOMEDIR/cordis.patch.yml"
write_sandbox_patch "$HOMEDIR/profiles/web/cordis.patch.yml"
write_sandbox_package_json "$HOMEDIR/profiles/web/package.json"
# 备料完整性断言：副本自含面（junction deref 真实落地的部分）在位。dsh 自家
# bundle（@deepseek-ai/dsh-web-app 等）不经副本解析——生产源 $DSH_HOME/profiles/
# node_modules 是指向宿主 app 资源的 junction 群（生产侧本就大量悬垂，dsh 回落
# app 自带副本解析，本机同宿主启动等价），不作为副本断言项。
for need in \
  "$HOMEDIR/profiles/web/ferryman-dsh/index.mjs" \
  "$HOMEDIR/profiles/web/node_modules/ferryman-dsh/src/index.ts" \
  "$HOMEDIR/profiles/web/node_modules/dsh-hypatia/package.json" \
  "$HOMEDIR/profiles/web/node_modules/eventsource/package.json"; do
  [ -e "$need" ] || { echo "[e2e_dsh] 副本缺 $need（junction 未跟随或包缺失？）" >&2; fail_teardown; }
done
[ -d "$HOMEDIR/profiles/web/node_modules" ] \
  || { echo "[e2e_dsh] 副本缺 profiles/web/node_modules" >&2; fail_teardown; }

echo "== [2/5] 沙箱 config + 隔离扫描 =="
write_sandbox_config "$(sandbox_config)" "$ROOT_MIX" "$E2E_DSH_DAEMON_PORT"
isolation_scan \
  "$(sandbox_config)" \
  "$HOMEDIR/cordis.patch.yml" \
  "$HOMEDIR/profiles/web/cordis.patch.yml" \
  "$HOMEDIR/profiles/web/package.json" || fail_teardown
echo "  隔离扫描通过：关键配置无生产端口残迹（15700/15722/3080/3081/15900）"

echo "== [3/5] 沙箱 daemon（本仓测试构建） =="
TOKEN_FILE="$(sandbox_data)/daemon.token"
if daemon_stats_ok "$E2E_DSH_DAEMON_PORT" "$TOKEN_FILE"; then
  echo "  已在运行且健康，复用（不叠进程）"
else
  if port_listening "$E2E_DSH_DAEMON_PORT"; then
    echo "[e2e_dsh] 端口 $E2E_DSH_DAEMON_PORT 被非本栈进程占用（PID $(port_pid "$E2E_DSH_DAEMON_PORT")）。" \
         "先 bash tools/e2e_dsh/stop.sh 清场" >&2
    fail_teardown
  fi
  echo "  go build ./cmd/ferryman → $(sandbox_bin)"
  if ! (cd "$E2E_DSH_REPO_ROOT" && go build -o "$(win_path "$(sandbox_bin)")" ./cmd/ferryman) \
      >"$LOGS/build.log" 2>&1; then
    echo "[e2e_dsh] go build 失败，日志尾部：" >&2
    tail -20 "$LOGS/build.log" >&2 || true
    fail_teardown
  fi
  # 全新台账：清空沙箱数据目录与 daemon 日志（上次运行的 skeleton handoffs/
  # 账本/日志不带入，日志逐次可读）
  rm -rf "$(sandbox_data)"
  : > "$LOGS/daemon.log"
  echo "  启动守护（FERRYMAN_CONFIG/FERRYMAN_DATA 全指沙箱；USERPROFILE 钉进沙箱" \
       "——watcher 的 Orca codex 目录自动追加按 home 推导，不钉则读生产会话转录；" \
       "--no-tray --no-browser --smoke）"
  env -u CODEX_HOME -u DSH_HOME \
      USERPROFILE="$(win_path "$(sandbox_root)")" \
      FERRYMAN_CONFIG="$ROOT_MIX/config.toml" \
      FERRYMAN_DATA="$(mix_path "$(sandbox_data)")" \
      FERRYMAN_PORT="$E2E_DSH_DAEMON_PORT" \
      "$(mix_path "$(sandbox_bin)")" serve --port "$E2E_DSH_PANEL_PORT" --no-tray --no-browser --smoke \
      >>"$LOGS/daemon.log" 2>&1 &
  echo $! > "$RUN/daemon.msys.pid"
  ok=0
  for _ in $(seq 1 45); do
    if daemon_stats_ok "$E2E_DSH_DAEMON_PORT" "$TOKEN_FILE"; then ok=1; break; fi
    sleep 1
  done
  if [ "$ok" != "1" ]; then
    echo "[e2e_dsh] daemon /stats 45s 未就绪，日志尾部：" >&2
    tail -30 "$LOGS/daemon.log" >&2 || true
    fail_teardown
  fi
  echo "  daemon /stats 就绪（token: $TOKEN_FILE）"
fi

if ! port_listening "$E2E_DSH_PANEL_PORT"; then
  echo "[e2e_dsh] 面板口 $E2E_DSH_PANEL_PORT 未监听（被占或缺位），日志尾部：" >&2
  tail -10 "$LOGS/daemon.log" >&2 || true
  fail_teardown
fi
echo "  面板就绪：http://127.0.0.1:$E2E_DSH_PANEL_PORT"

echo "== [4/5] 隔离 DSH web 实例（端口 $E2E_DSH_WEB_PORT） =="
if port_listening "$E2E_DSH_WEB_PORT"; then
  if web_http_ok "$E2E_DSH_WEB_PORT"; then
    echo "  已在监听且应答，复用（不叠进程）"
  else
    echo "[e2e_dsh] 端口 $E2E_DSH_WEB_PORT 被占且无 HTTP 应答（PID $(port_pid "$E2E_DSH_WEB_PORT")）。" \
         "先 bash tools/e2e_dsh/stop.sh 清场" >&2
    fail_teardown
  fi
else
  echo "  DSH_HOME=$HOME_MIX · FERRYMAN_PORT=$E2E_DSH_DAEMON_PORT · 插件 token 指沙箱"
  : > "$LOGS/web.log"
  env -u FERRYMAN_DISABLE \
      DSH_HOME="$HOME_MIX" \
      FERRYMAN_PORT="$E2E_DSH_DAEMON_PORT" \
      FERRYMAN_TOKEN_FILE="$ROOT_MIX/ferryman-data/daemon.token" \
      "$DSH_CLI" web --no-open --port "$E2E_DSH_WEB_PORT" >>"$LOGS/web.log" 2>&1 &
  echo $! > "$RUN/web.msys.pid"
  ok=0
  for _ in $(seq 1 120); do
    if web_http_ok "$E2E_DSH_WEB_PORT"; then ok=1; break; fi
    sleep 1
  done
  if [ "$ok" != "1" ]; then
    echo "[e2e_dsh] web 实例 120s 未就绪，日志尾部：" >&2
    tail -30 "$LOGS/web.log" >&2 || true
    fail_teardown
  fi
  port_pid "$E2E_DSH_WEB_PORT" > "$RUN/web.win.pid"
  echo "  web 实例就绪：http://127.0.0.1:$E2E_DSH_WEB_PORT（win PID $(cat "$RUN/web.win.pid")）"
fi

echo "== [5/5] 健康检查 =="
bash "$HERE/health.sh"

cat <<EOF

[e2e_dsh] 沙箱栈已就绪
  daemon 控制口 : http://127.0.0.1:$E2E_DSH_DAEMON_PORT   （/stats，token 在 $(sandbox_data)/daemon.token）
  面板          : http://127.0.0.1:$E2E_DSH_PANEL_PORT
  dsh web       : http://127.0.0.1:$E2E_DSH_WEB_PORT
  沙箱根        : $ROOT_MIX
  日志          : $LOGS/{daemon,web,build}.log
  拆栈          : bash tools/e2e_dsh/stop.sh
EOF
