#!/usr/bin/env bash
# setup.sh — 票08 E2E 剧本的栈整备（tools/e2e_dsh/suite）。
#
# 在票07 沙箱栈（tools/e2e_dsh/{start,stop,health}.sh + lib.sh）之上做四件套08
# 专属整备，产出「夜链工作树插件 + 秒级压缩参数」的确定性起跑线：
#   1. 起栈：bash ../start.sh（幂等；失败则拆栈重起一次兜底）。
#   2. 插件换装：沙箱 profile 副本里的 ferryman-dsh 是生产拷贝（旧版），rm+cp
#      换成夜链工作树 plugin/ferryman-dsh——两处落点（profiles/web/ferryman-dsh
#      与 profiles/web/node_modules/ferryman-dsh，web 实例从前者装载）。
#   3. config 调参：start.sh 每次重写沙箱 config.toml，本脚本在其后追加
#      [heartbeat].ttl_s（TTL 基准——不可得则整条压缩链静默不触发）＋
#      [dsh_compact] 秒级参数（触发线 0.5×TTL／min_peak=12000——须卡在 glm-5.3
#      提示地板 ~9.4K 与可达峰值 ~13.4K 之间,4000 会把 gate 放行比较做成结构性
#      不可达／poll_hint_s=2——插件侧下限 10s 托底＝指令送达周期 10s／compressed
#      标记窗 3×TTL——盖过 block_s=90 留出放行断言窗口）。
#   4. 重启 daemon（吃新 config；不清数据目录——token/台账延续）＋重启 web
#      实例（吃新插件；重写 web.log 便于取登录 token）＋健康检查。
#
# 票03（dsh-cross-inject）拓扑矩阵追加：
#   5. 会话转录落明文：profiles/web/cordis.patch.yml 追加 session-persistence-jsonl
#      的 compression=none 覆写——矩阵硬门禁要 driver 直读新会话转录验「无交接
#      文本」；明文与 zstd 都是 dsh 原生实录形态（dshtrans 两形态同解析），仅编码
#      差异。none 模式后端对根内既有 .jsonl.zstd 工件拒绝启动（encodingMismatch，
#      实测）——顺带清空沙箱 sessions 目录（每次 run 全新会话面；账本旧行无害：
#      断言全按 session_id 过滤）。
#   6. 第二 profile（web2，跨 profile 拓扑 T6 用）：profiles/web 整目录拷贝（插件
#      换装+明文覆写已就位后再拷）＋独立端口起第二个 web 实例——与 web1 共享
#      DSH_HOME（sessions/storages 同根；多 profile 并行共用一个 home 是既有生产
#      形态，实测可并行）。env 追加 web2 口与 token。
#
# 全程零人工介入；幂等可重复跑。输出：driver.mjs 所需 env（run.sh 统一导出）。
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STACK_DIR="$(cd "$HERE/.." && pwd)"
# shellcheck disable=SC1091
. "$STACK_DIR/lib.sh"

# 票03：web2 实例口（跨 profile 拓扑；铁闸同 25xxx 段）
E2E_DSH_WEB2_PORT="${E2E_DSH_WEB2_PORT:-25903}"
require_port_25xxx "web2" "$E2E_DSH_WEB2_PORT" || exit 1

echo "== [suite/setup 1/7] 起沙箱栈（幂等） =="
if ! bash "$STACK_DIR/start.sh"; then
  echo "[suite] start.sh 首跑失败，拆栈后重起一次兜底" >&2
  bash "$STACK_DIR/stop.sh" || true
  bash "$STACK_DIR/start.sh"
fi

HOMEDIR="$(sandbox_home)"
LOGS="$(sandbox_logs)"
ROOT_MIX="$(mix_path "$(sandbox_root)")"
RUNSTAMP="$(date -u +%Y%m%dT%H%M%SZ)"

echo "== [suite/setup 2/7] 插件换装 + 模型路由改指沙箱可用供应商 =="
PLUGIN_SRC="$E2E_DSH_REPO_ROOT/plugin/ferryman-dsh"
[ -f "$PLUGIN_SRC/src/compact.ts" ] || {
  echo "[suite] 工作树插件缺 src/compact.ts（夜链票05 产物）——源不完整" >&2; exit 1; }
for dst in "$HOMEDIR/profiles/web/ferryman-dsh" \
           "$HOMEDIR/profiles/web/node_modules/ferryman-dsh"; do
  rm -rf "$dst"
  cp -r "$PLUGIN_SRC" "$dst"
  rm -rf "$dst/.playwright-cli"   # 调研残留，不带进沙箱
done
# 换装核验：夜链版独有文件（票05 轮询臂/票06 横幅）在两处落点都在位
for marker in "src/compact.ts" "src/banner.ts" "client.js"; do
  for dst in "$HOMEDIR/profiles/web/ferryman-dsh" \
             "$HOMEDIR/profiles/web/node_modules/ferryman-dsh"; do
    [ -f "$dst/$marker" ] || { echo "[suite] 插件换装缺 $dst/$marker" >&2; exit 1; }
  done
done
echo "  两处落点已换装（compact.ts/banner.ts/client.js 在位）"

# 模型路由：home 层 patch 的 agent-default-model 在生产靠 llm-deepseek 渡口覆写
# （已按隔离契约剥离）才可用——裸 deepseek-official 无密钥，真会话起不来。改指
# 沙箱凭据自含的供应商 zai-coding-cn/glm-5.3（settings.yaml.imported 的原生缺省，
# 密钥 ZAI_CODING_CN_API_KEY 随 ~/.dsh 副本携带，直连 z.ai，零生产接触）。幂等。
HOME_PATCH="$HOMEDIR/cordis.patch.yml"
sed -i 's/provider: deepseek-official/provider: zai-coding-cn/g; s/model: claude-opus-5/model: glm-5.3/g' "$HOME_PATCH"
if grep -q 'provider: deepseek-official' "$HOME_PATCH" 2>/dev/null; then
  echo "[suite] home patch 仍含 deepseek-official——改写未生效" >&2; exit 1
fi
# 挂 pi-ai 多供应商适配器（app.asar 自带；zai 是 pi-ai 目录供应商，端点/协议/模型
# 目录取缺省，只补密钥引用——解析走凭据缝 .credentials.yaml refs）。不挂则
# zai-coding-cn 路由 NO_ADAPTER（实测）。幂等：已在则不重复追加。
if ! grep -q 'dsh-llm-pi-ai' "$HOME_PATCH"; then
  cat >> "$HOME_PATCH" <<'EOF'
- id: llm-pi-ai
  name: '@deepseek-ai/dsh-llm-pi-ai'
  config:
    providers:
      zai-coding-cn:
        apiKeyEnv: ZAI_CODING_CN_API_KEY
EOF
fi
echo "  模型路由已改指 zai-coding-cn/glm-5.3＋挂 pi-ai 适配器（沙箱凭据自含）"

echo "== [suite/setup 3/7] 转录落明文 + sessions 清空 + web2 profile 备料（票03） =="
PROFILE_PATCH="$HOMEDIR/profiles/web/cordis.patch.yml"
if ! grep -q 'compression: none' "$PROFILE_PATCH"; then
  cat >> "$PROFILE_PATCH" <<'EOF'
# ---- 票03 E2E suite 追加（tools/e2e_dsh/suite/setup.sh）：会话转录落明文 ----
# 多会话拓扑矩阵的硬门禁要求 driver 直读会话转录（「新会话转录无交接文本」断言）；
# 明文与 zstd 都是 dsh 原生实录形态（dshtrans 两形态同解析），仅编码差异。
# id 定向 patch 是整段 config 替换——root 用与 base bundle 相同的 dshHomePath
# 表达式原样重述（用户层 patch 与 bundle patch 同一 include 方言/求值上下文）。
- id: session-persistence-jsonl
  name: '@deepseek-ai/dsh-session-persistence-jsonl'
  config:
    root: !!js dshHomePath('sessions')
    compression: none
EOF
fi
grep -q 'compression: none' "$PROFILE_PATCH" \
  || { echo "[suite] 明文覆写未写入 $PROFILE_PATCH" >&2; exit 1; }
# none 模式后端对根内既有 .jsonl.zstd 工件拒绝启动（encodingMismatch，实测）——
# 清空沙箱 sessions（旧 run 会话/陈旧草稿不带入，跨 run 确定性）
rm -rf "$HOMEDIR/sessions"
mkdir -p "$HOMEDIR/sessions"
# web2 profile：web 整目录拷贝（换装插件+明文覆写已就位后再拷；bak 残留不带）。
# 先拆在跑的 web2 实例（上一 run 留守或本机手试进程）——进程占用目录会使 rm 失败。
# （port_pid 在端口空闲时 grep 无命中，pipefail 下回非零——`|| true` 防 set -e 静默死）
WEB2_DIR="$HOMEDIR/profiles/web2"
pid="$(port_pid "$E2E_DSH_WEB2_PORT" || true)"
if [ -n "$pid" ]; then
  kill_pid_tree "$pid"
  wait_port_free "$E2E_DSH_WEB2_PORT" 15 || true
fi
rm -rf "$WEB2_DIR"
cp -r "$HOMEDIR/profiles/web" "$WEB2_DIR"
find "$WEB2_DIR" -maxdepth 1 -name '*.bak-*' -exec rm -f {} + 2>/dev/null || true
{ [ -f "$WEB2_DIR/ferryman-dsh/index.mjs" ] && [ -f "$WEB2_DIR/package.json" ]; } \
  || { echo "[suite] web2 profile 备料缺件" >&2; exit 1; }
isolation_scan "$PROFILE_PATCH" "$WEB2_DIR/cordis.patch.yml" "$WEB2_DIR/package.json" || exit 1
echo "  转录=明文（compression: none）· sessions 已清空 · web2 profile 已备料"

echo "== [suite/setup 4/7] config 调参（追加 [heartbeat]+[dsh_compact]） =="
# start.sh 每次运行都重写 config.toml（write_sandbox_config），追加节天然幂等
# （不会叠出重复节）。阈值面（summarize 45/block 90）沿用栈缺省不动。
E2E_SUITE_TTL_S="${E2E_SUITE_TTL_S:-60}"
E2E_SUITE_TRIGGER_RATIO="${E2E_SUITE_TRIGGER_RATIO:-0.5}"
# min_peak 必须卡在 glm-5.3 的提示地板（system+tools ≈ 9.4K,首请求 cache_read
# 实测 9344）与两轮灌文可达峰值（≈13.4K）之间:4000 时 gate 的 compressed 放行比
# 较（prefix < min_peak）永远差着模型地板——压缩到极致也压不进 4000,断言④结构
# 性不可达（2026-10-07 实锚:真压缩 4534 tokens 后 projected 仍 ≈8891）。12000:
# 触发侧灌 1-2 轮即过线,放行侧压缩后 ≈8.9-9.6K 余 2.4K+。
E2E_SUITE_MIN_PEAK="${E2E_SUITE_MIN_PEAK:-12000}"
E2E_SUITE_COMMAND_TTL_RATIO="${E2E_SUITE_COMMAND_TTL_RATIO:-0.5}"
E2E_SUITE_POLL_HINT_S="${E2E_SUITE_POLL_HINT_S:-2}"
E2E_SUITE_MARK_RATIO="${E2E_SUITE_MARK_RATIO:-3.0}"
cat >> "$(sandbox_config)" <<EOF

# ---- 票08 E2E suite 追加（tools/e2e_dsh/suite/setup.sh；每次 start.sh 重写后重追）----
# TTL 基准＝[heartbeat].ttl_s：缺省 0（未实测）＝压缩链整体静默——E2E 必须给值。
[heartbeat]
ttl_s = $E2E_SUITE_TTL_S

# 秒级压缩参数：触发线 0.5×TTL＝${E2E_SUITE_TRIGGER_RATIO}×${E2E_SUITE_TTL_S}s；min_peak＝
# ${E2E_SUITE_MIN_PEAK}（卡 glm-5.3 提示地板 ~9.4K 与可达峰值 ~13.4K 之间——
# gate 放行比较 prefix<min_peak 要压缩后能真过线）;指令有效期 0.5×TTL（盖过 10s 轮询周期）；
# poll_hint_s=2（插件下限 10s 托底）；compressed 标记窗 ${E2E_SUITE_MARK_RATIO}×TTL＝$(awk "BEGIN{print $E2E_SUITE_MARK_RATIO*$E2E_SUITE_TTL_S}")s
# （须 > block_s=90，给 gate 放行断言留窗口）。
[dsh_compact]
enabled = true
trigger_ratio = $E2E_SUITE_TRIGGER_RATIO
min_peak_tokens = $E2E_SUITE_MIN_PEAK
command_ttl_ratio = $E2E_SUITE_COMMAND_TTL_RATIO
poll_hint_s = $E2E_SUITE_POLL_HINT_S
compressed_flag_ttl_ratio = $E2E_SUITE_MARK_RATIO
EOF
echo "  ttl_s=$E2E_SUITE_TTL_S trigger@${E2E_SUITE_TRIGGER_RATIO}x min_peak=$E2E_SUITE_MIN_PEAK mark=${E2E_SUITE_MARK_RATIO}xTTL"

echo "== [suite/setup 5/7] 重启沙箱 daemon（吃新 config；数据目录不清） =="
TOKEN_FILE="$(sandbox_data)/daemon.token"
if daemon_stats_ok "$E2E_DSH_DAEMON_PORT" "$TOKEN_FILE"; then
  shutdown_daemon "$E2E_DSH_DAEMON_PORT" "$TOKEN_FILE" && echo "  /shutdown 已发"
fi
wait_port_free "$E2E_DSH_DAEMON_PORT" 15 || {
  pid="$(port_pid "$E2E_DSH_DAEMON_PORT")"
  echo "  15s 未退（PID $pid），强杀进程树"
  [ -n "$pid" ] && kill_pid_tree "$pid"
  wait_port_free "$E2E_DSH_DAEMON_PORT" 5 || { echo "[suite] daemon 端口不净" >&2; exit 1; }
}
# 与 start.sh 完全同款启动形态（仅日志分开落，便于本套件排障）
env -u CODEX_HOME -u DSH_HOME \
    USERPROFILE="$(win_path "$(sandbox_root)")" \
    FERRYMAN_CONFIG="$ROOT_MIX/config.toml" \
    FERRYMAN_DATA="$(mix_path "$(sandbox_data)")" \
    FERRYMAN_PORT="$E2E_DSH_DAEMON_PORT" \
    "$(mix_path "$(sandbox_bin)")" serve --port "$E2E_DSH_PANEL_PORT" --no-tray --no-browser --smoke \
    >>"$LOGS/daemon-suite-$RUNSTAMP.log" 2>&1 &
echo $! > "$(sandbox_run)/daemon.msys.pid"
ok=0
for _ in $(seq 1 45); do
  if daemon_stats_ok "$E2E_DSH_DAEMON_PORT" "$TOKEN_FILE"; then ok=1; break; fi
  sleep 1
done
[ "$ok" = "1" ] || { echo "[suite] 重启后 daemon 45s 未就绪，日志尾部：" >&2
  tail -30 "$LOGS"/daemon-suite-$RUNSTAMP.log >&2 || true; exit 1; }
echo "  daemon 就绪（新参数已生效；日志 daemon-suite-$RUNSTAMP.log）"

echo "== [suite/setup 6/7] 重启 web 实例（吃新插件；重取登录 token） =="
pid="$(port_pid "$E2E_DSH_WEB_PORT")"
[ -z "$pid" ] || { kill_pid_tree "$pid"; wait_port_free "$E2E_DSH_WEB_PORT" 15 || true; }
: > "$LOGS/web.log"
env -u FERRYMAN_DISABLE \
    DSH_HOME="$(mix_path "$HOMEDIR")" \
    FERRYMAN_PORT="$E2E_DSH_DAEMON_PORT" \
    FERRYMAN_TOKEN_FILE="$(mix_path "$(sandbox_data)")/daemon.token" \
    "$DSH_CLI" web --no-open --port "$E2E_DSH_WEB_PORT" >>"$LOGS/web.log" 2>&1 &
echo $! > "$(sandbox_run)/web.msys.pid"
ok=0
for _ in $(seq 1 120); do
  if web_http_ok "$E2E_DSH_WEB_PORT"; then ok=1; break; fi
  sleep 1
done
[ "$ok" = "1" ] || { echo "[suite] web 实例 120s 未就绪，日志尾部：" >&2
  tail -30 "$LOGS/web.log" >&2 || true; exit 1; }
port_pid "$E2E_DSH_WEB_PORT" > "$(sandbox_run)/web.win.pid"
WEB_TOKEN=""
for _ in $(seq 1 30); do
  WEB_TOKEN="$(grep -ao 'token=[^[:space:]]*' "$LOGS/web.log" | tail -1 | cut -d= -f2 || true)"
  [ -n "$WEB_TOKEN" ] && break
  sleep 1
done
[ -n "$WEB_TOKEN" ] || { echo "[suite] 30s 内未在 web.log 捕到登录 token" >&2; exit 1; }
echo "  web 实例就绪（win PID $(cat "$(sandbox_run)/web.win.pid")，token 已捕获）"

echo "== [suite/setup 7/7] web2 实例（票03 跨 profile 拓扑）＋健康检查 =="
# 与 web1 并行共用 DSH_HOME（生产多 profile 形态）；口独立、日志独立、token 独立。
pid="$(port_pid "$E2E_DSH_WEB2_PORT" || true)"
[ -z "$pid" ] || { kill_pid_tree "$pid"; wait_port_free "$E2E_DSH_WEB2_PORT" 15 || true; }
: > "$LOGS/web2.log"
env -u FERRYMAN_DISABLE \
    DSH_HOME="$(mix_path "$HOMEDIR")" \
    FERRYMAN_PORT="$E2E_DSH_DAEMON_PORT" \
    FERRYMAN_TOKEN_FILE="$(mix_path "$(sandbox_data)")/daemon.token" \
    "$DSH_CLI" web2 --no-open --port "$E2E_DSH_WEB2_PORT" >>"$LOGS/web2.log" 2>&1 &
echo $! > "$(sandbox_run)/web2.msys.pid"
ok=0
for _ in $(seq 1 120); do
  if web_http_ok "$E2E_DSH_WEB2_PORT"; then ok=1; break; fi
  sleep 1
done
[ "$ok" = "1" ] || { echo "[suite] web2 实例 120s 未就绪，日志尾部：" >&2
  tail -30 "$LOGS/web2.log" >&2 || true; exit 1; }
port_pid "$E2E_DSH_WEB2_PORT" > "$(sandbox_run)/web2.win.pid"
WEB2_TOKEN=""
for _ in $(seq 1 30); do
  WEB2_TOKEN="$(grep -ao 'token=[^[:space:]]*' "$LOGS/web2.log" | tail -1 | cut -d= -f2 || true)"
  [ -n "$WEB2_TOKEN" ] && break
  sleep 1
done
[ -n "$WEB2_TOKEN" ] || { echo "[suite] 30s 内未在 web2.log 捕到登录 token" >&2; exit 1; }
echo "  web2 实例就绪（win PID $(cat "$(sandbox_run)/web2.win.pid")，token 已捕获）"
bash "$STACK_DIR/health.sh"

# ---- 供 run.sh/driver.mjs 消费的 env（mixed 路径=node 可直接用；落沙箱不落仓库） ----
cat > "$(sandbox_run)/env.driver" <<EOF
E2E_SUITE_ROOT=$ROOT_MIX
E2E_SUITE_DATA=$(mix_path "$(sandbox_data)")
E2E_SUITE_HOME=$(mix_path "$HOMEDIR")
E2E_SUITE_SESSIONS=$(mix_path "$HOMEDIR/sessions")
E2E_SUITE_LOGS=$(mix_path "$LOGS")
E2E_SUITE_DAEMON_LOG=$(mix_path "$LOGS/daemon-suite-$RUNSTAMP.log")
E2E_SUITE_DAEMON_PORT=$E2E_DSH_DAEMON_PORT
E2E_SUITE_WEB_PORT=$E2E_DSH_WEB_PORT
E2E_SUITE_WEB_TOKEN=$WEB_TOKEN
E2E_SUITE_WEB2_PORT=$E2E_DSH_WEB2_PORT
E2E_SUITE_WEB2_TOKEN=$WEB2_TOKEN
E2E_SUITE_TTL_S=$E2E_SUITE_TTL_S
E2E_SUITE_TRIGGER_RATIO=$E2E_SUITE_TRIGGER_RATIO
E2E_SUITE_MIN_PEAK=$E2E_SUITE_MIN_PEAK
E2E_SUITE_COMMAND_TTL_RATIO=$E2E_SUITE_COMMAND_TTL_RATIO
E2E_SUITE_MARK_RATIO=$E2E_SUITE_MARK_RATIO
E2E_SUITE_BLOCK_S=90
E2E_SUITE_RUNSTAMP=$RUNSTAMP
EOF
echo "[suite] 整备完成（env.driver 已写至$(sandbox_run)/env.driver；runStamp $RUNSTAMP）"
