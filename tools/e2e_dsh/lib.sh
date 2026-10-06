#!/usr/bin/env bash
# lib.sh — E2E 沙箱栈共享库（票07 · tools/e2e_dsh）。
#
# 沙箱栈三件：沙箱 daemon（本仓 go build 测试构建）＋隔离 DSH web 实例（DSH_HOME
# 整目录副本）＋一键起停/健康检查。隔离契约（硬约束，越线拒跑）：
#   - 全部端口落在 25xxx 段（require_port_25xxx 铁闸；生产 3080/15700/15722/
#     3081/15900 想都别想）；
#   - 全部落盘路径钉在沙箱根（默认 %TEMP%/ferryman-e2e-dsh，env 可覆写），
#     生产 ~/ferryman 与生产 ~/.dsh 只作复制源只读引用；
#   - 沙箱 dsh home 的模型路由层剥离 llm-deepseek 渡口覆写（生产 15722 数据面
#     来源），起栈前对关键配置文件做生产端口残迹扫描（isolation_scan）。
#
# Windows 零闪窗铁律口径：本库脚本一律由 bash 会话（CC Bash 工具/CI shell）
# 执行——bash 自带隐藏控制台，子进程（go build、ferryman-e2e.exe、cmd 包装的
# dsh.cmd）继承隐藏控制台，不新建可见窗口；dsh web 宿主是 GUI 子系统的
# DeepSeek Harness.exe（ELECTRON_RUN_AS_NODE），天生无控制台。沙箱 profile 已
# 剔除 MCP 注入（npx 子进程＝闪窗+网络双风险）。禁用 PowerShell Start-Process
# 形态——不要改。

# ---- 可覆写旋钮（env 优先） ------------------------------------------------

export E2E_DSH_ROOT="${E2E_DSH_ROOT:-${TMPDIR:-${TMP:-/tmp}}/ferryman-e2e-dsh}"
export E2E_DSH_DAEMON_PORT="${E2E_DSH_DAEMON_PORT:-25900}"   # daemon 控制口（[server].port）
export E2E_DSH_PANEL_PORT="${E2E_DSH_PANEL_PORT:-25901}"     # serve 面板口（--port）
export E2E_DSH_WEB_PORT="${E2E_DSH_WEB_PORT:-25902}"         # dsh web 实例口
# dsh CLI 与生产 dsh home 复制源（只读引用；本机安装位，可 env 覆写以便他机复用）
export DSH_CLI="${DSH_CLI:-C:/Users/allan716/AppData/Local/Programs/DeepSeek Harness/resources/runtime/cli/bin/dsh.cmd}"
export DSH_HOME_SRC="${DSH_HOME_SRC:-$HOME/.dsh}"
# 仓库根（lib.sh 位于 <repo>/tools/e2e_dsh/）
E2E_DSH_REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# ---- 路径 ------------------------------------------------------------------

# mix_path 转 Windows 混合斜杠绝对路径（C:/...；TOML/node/Go/env 通用，反斜杠
# 在 TOML 里是转义符，故全链一律正斜杠）。cygpath 不可用时原样返回。
mix_path() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s\n' "$1"; fi
}

# win_path 转纯反斜杠 Windows 路径（robocopy/taskkill 等原生工具用）。
win_path() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -w "$1"; else printf '%s\n' "$1"; fi
}

sandbox_root()   { printf '%s\n' "$E2E_DSH_ROOT"; }
sandbox_home()   { printf '%s/dsh-home\n' "$E2E_DSH_ROOT"; }
sandbox_data()   { printf '%s/ferryman-data\n' "$E2E_DSH_ROOT"; }
sandbox_bin()    { printf '%s/bin/ferryman-e2e.exe\n' "$E2E_DSH_ROOT"; }
sandbox_logs()   { printf '%s/logs\n' "$E2E_DSH_ROOT"; }
sandbox_run()    { printf '%s/run\n' "$E2E_DSH_ROOT"; }
sandbox_config() { printf '%s/config.toml\n' "$E2E_DSH_ROOT"; }

# ---- 端口硬校验（验收标准②：越界即拒跑） ----------------------------------

# require_port_25xxx <名字> <端口>：非 25xxx 段（25000-25999）一票拒绝。
# 生产口 3080/15700/15722/3081 天然越界，无需逐个枚举。
require_port_25xxx() {
  local name="$1" port="${2:-}"
  case "$port" in
    ''|*[!0-9]*)
      echo "[e2e_dsh] 拒跑：$name 端口非法（got: '$port'，须为 25000-25999）" >&2
      return 1 ;;
  esac
  if [ "$port" -lt 25000 ] || [ "$port" -gt 25999 ]; then
    echo "[e2e_dsh] 拒跑：$name 端口 $port 越出 25xxx 段（生产口隔离铁闸）" >&2
    return 1
  fi
  return 0
}

# validate_ports 对三口整组校验（读 E2E_DSH_* 环境变量）。
validate_ports() {
  require_port_25xxx "daemon"  "$E2E_DSH_DAEMON_PORT" || return 1
  require_port_25xxx "panel"   "$E2E_DSH_PANEL_PORT"  || return 1
  require_port_25xxx "web"     "$E2E_DSH_WEB_PORT"    || return 1
}

# ---- 端口/进程探测（netstat 单源） -----------------------------------------

# port_listening <port>：本机是否有人 LISTENING。
port_listening() {
  local out
  out="$(netstat -ano -p tcp 2>/dev/null | grep -E ":$1[[:space:]].*LISTENING" | head -1)"
  [ -n "$out" ]
}

# port_pid <port>：回显占口 Windows PID（无占口回显空串）。
port_pid() {
  netstat -ano -p tcp 2>/dev/null | grep -E ":$1[[:space:]].*LISTENING" | head -1 | awk '{print $NF}'
}

# kill_pid_tree <winpid>：强杀进程树（teardown 兜底路径；优雅路径走 /shutdown）。
kill_pid_tree() {
  taskkill //F //T //PID "$1" >/dev/null 2>&1
}

# wait_port_free <port> <timeout_s>：等到端口无人监听或超时。
wait_port_free() {
  local port="$1" timeout="$2" waited=0
  while port_listening "$port"; do
    [ "$waited" -ge "$timeout" ] && return 1
    sleep 1
    waited=$((waited + 1))
  done
  return 0
}

# ---- 健康探针 --------------------------------------------------------------

# daemon_stats_ok <port> <token_file>：GET /stats 带 Bearer 须 200。
daemon_stats_ok() {
  local port="$1" token_file="$2" token code
  [ -f "$token_file" ] || return 1
  token="$(tr -d ' \t\r\n' < "$token_file")"
  [ -n "$token" ] || return 1
  code="$(curl -s -o /dev/null -w '%{http_code}' -m 5 \
    -H "Authorization: Bearer $token" "http://127.0.0.1:$port/stats" 2>/dev/null)"
  [ "$code" = "200" ]
}

# web_http_ok <port>：web 实例口对 HTTP 有应答（任意状态码＝监听且在服务）。
web_http_ok() {
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' -m 5 "http://127.0.0.1:$1/" 2>/dev/null)"
  [ -n "$code" ] && [ "$code" != "000" ]
}

# shutdown_daemon <port> <token_file>：POST /shutdown 优雅停（守护内部收口序：
# watcher/worker 停、pid 文件删、面板同收）。应答不挑——发了就算。
shutdown_daemon() {
  local port="$1" token_file="$2" token
  [ -f "$token_file" ] || return 1
  token="$(tr -d ' \t\r\n' < "$token_file")"
  curl -s -o /dev/null -m 5 -X POST \
    -H "Authorization: Bearer $token" "http://127.0.0.1:$port/shutdown" 2>/dev/null
}

# ---- 生成面（selftest 钉死内容契约） ---------------------------------------

# write_sandbox_config <out> <root_mix> <daemon_port>：沙箱 daemon 测试 config。
# 秒级阈值（summarize 45 / block 90）须配 `serve --smoke`（阈值差≥120s 校验放宽，
# cmd/ferryman serveOpts.smoke 钉点）；无 [dock] 节＝渡口完全不启动（F11 opt-in），
# 沙箱永不绑 15722；dsh 闸门 enforce＝E2E 真拦；watch 三目录全指沙箱内空目录，
# 生产会话转录零进入。
write_sandbox_config() {
  local out="$1" root="$2" daemon_port="$3"
  cat > "$out" <<EOF
# Ferryman E2E sandbox daemon config - generated by tools/e2e_dsh (do not hand-edit).
# Isolation contract: every path stays under the sandbox root; ports stay in 25xxx.
# Second-level thresholds require: ferryman serve --smoke (gap >= 120s check relaxed).

[server]
port = $daemon_port
data_dir = "$root/ferryman-data"

[gate]
cc_mode = "off"
codex_mode = "off"
dsh_mode = "enforce"

[thresholds]
summarize_s = 45
block_s = 90
min_ctx_tokens = 500
cache_warn_s = 0

[watch]
poll_interval_s = 1
cc_projects_dir = "$root/watch/cc-projects"
codex_sessions_dir = "$root/watch/codex-sessions"
dsh_sessions_dir = "$root/dsh-home/sessions"
EOF
}

# write_sandbox_patch <out>：沙箱 web profile 的 cordis.patch.yml。
# 对生产 profiles/web/cordis.patch.yml 的差异：ios-control 整体剔除（其 config
# 钉生产口 3081，且 bundle 从 package.json 一并移除）、四条 MCP 注入剔除
# （npx 子进程＝闪窗+网络双风险）、密钥零携带；只留 ferryman-dsh 插件挂载
# （./ferryman-dsh 相对路径锚定在 patch 旁，副本内是 deref 后的实体目录）。
write_sandbox_patch() {
  local out="$1"
  cat > "$out" <<'EOF'
# Ferryman E2E sandbox profile patch - generated by tools/e2e_dsh (do not hand-edit).
# Diffs vs production profiles/web/cordis.patch.yml:
#   - phone-remote bundle entry removed (its config pins a production port)
#   - MCP client inserts removed (npx spawns: console-flash + network)
#   - no production secrets carried
- insert:
    - id: ferryman-dsh
      name: ./ferryman-dsh/index.mjs
EOF
}

# write_sandbox_package_json <out>：沙箱 profile 的 package.json（bundles 表镜像
# 生产 profiles/web/package.json，仅摘除 dsh-ios-control）。运行时决议只看
# bundles 表；dependencies 仅为 pnpm 元数据留档（沙箱从不起 pnpm install）。
write_sandbox_package_json() {
  local out="$1"
  cat > "$out" <<'EOF'
{
  "name": "dsh-profile-web-e2e-sandbox",
  "private": true,
  "dependencies": {
    "@deepseek-ai/dsh-hooks-claude-code": "0.2.0-rc.2",
    "@deepseek-ai/dsh-mcp-client": "0.2.0-rc.2",
    "dsh-hypatia": "^0.2.3",
    "ferryman-dsh": "file:C:/WorkSpace/agent/Ferryman/plugin/ferryman-dsh"
  },
  "dsh": {
    "profile": {
      "bundles": [
        "@deepseek-ai/dsh-base",
        "@deepseek-ai/dsh-web-app",
        "@deepseek-ai/dsh-experimental-agent-team-profile",
        "@deepseek-ai/dsh-experimental-auto-review",
        "@deepseek-ai/dsh-experimental-schedule-bundle",
        "ferryman-dsh",
        "dsh-hypatia"
      ]
    }
  }
}
EOF
}

# strip_llm_deepseek <patch_file>：从 home 层 cordis.patch.yml 摘除 llm-deepseek
# 覆写条目（其 baseURL 钉生产渡口 127.0.0.1:15722——沙箱模型路由不得经生产口；
# 其余条目原样保留，含两个 disabled 遥测/会话日志条目的生产姿态）。条目以
# "- id:" 行分块，逐块保留/丢弃，与条目顺序无关。
strip_llm_deepseek() {
  local file="$1"
  [ -f "$file" ] || return 0
  awk '
    /^- id:/ {
      if (!skip && blk != "") printf "%s", blk
      blk = ""; skip = ($0 ~ /id: llm-deepseek/)
    }
    { blk = blk $0 "\n" }
    END { if (!skip && blk != "") printf "%s", blk }
  ' "$file" > "$file.tmp" && mv -f "$file.tmp" "$file"
}

# isolation_scan <file>...：生产端口残迹扫描（15700/15722/3080/3081/15900），
# 任一命中即失败——起栈前最后一道隔离铁闸。
isolation_scan() {
  local f hit=0
  for f in "$@"; do
    [ -f "$f" ] || continue
    if grep -nE '(^|[^0-9])(15700|15722|3080|3081|15900)([^0-9]|$)' "$f" >/dev/null 2>&1; then
      echo "[e2e_dsh] 隔离扫描失败：$f 含生产端口残迹：" >&2
      grep -nE '(^|[^0-9])(15700|15722|3080|3081|15900)([^0-9]|$)' "$f" >&2 || true
      hit=1
    fi
  done
  return "$hit"
}

# ---- 沙箱 home 备料（复制源只读） ------------------------------------------

# prepare_sandbox_home <src_home> <dst_home>：robocopy 整备沙箱 dsh home。
# 默认跟随 junction（deref）——profiles/web 内 ferryman-dsh junction 落为实体
# 目录，副本自含（spec Further Notes 钉点）。
# 退出码契约：bit 8（个别条目复制失败）容忍——生产源 ~/.dsh/profiles/node_modules
# 的 runtime-resolution junction 群本就有大量悬垂目标（cp -L 同样报 607 处，
# 生产照常运行），属源侧既有状态而非复制缺陷；真正的完整性由调用方结构断言
# 兜底（插件实体/index.mjs、bundle 包目录在位）。rc≥16（fatal/用法错）才必败。
# 收尾卫生：sessions 清空（生产会话转录不进沙箱）、remote-link/start-web 脚本/
# runkeys 剔除（生产专属工件）。
prepare_sandbox_home() {
  local src="$1" dst="$2" rc=0
  [ -d "$src" ] || { echo "[e2e_dsh] dsh 复制源不存在：$src" >&2; return 1; }
  mkdir -p "$dst"
  robocopy "$(win_path "$src")" "$(win_path "$dst")" \
    //E //NFL //NDL //NJH //NJS //NP >/dev/null || rc=$?
  [ "$rc" -lt 16 ] || { echo "[e2e_dsh] robocopy fatal rc=$rc" >&2; return 1; }
  rm -rf "$dst/sessions"
  mkdir -p "$dst/sessions"
  rm -rf "$dst/remote-link" "$dst/start-web.cmd" "$dst/start-web-hidden.vbs" \
    "$dst/autostart-runkeys.reg"
  find "$dst" -maxdepth 1 -name '*.bak-*' -exec rm -f {} + 2>/dev/null
  return 0
}
