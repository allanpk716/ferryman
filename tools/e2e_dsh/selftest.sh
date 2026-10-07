#!/usr/bin/env bash
# tools/e2e_dsh/selftest.sh — lib.sh 纯函数自测（不起栈、不占端口、不碰网络）。
# 用法: bash tools/e2e_dsh/selftest.sh   （全绿退出 0，任何红退出 1）
# TDD 钉子：本文件先于 lib.sh 写下——lib.sh 缺位/行为漂移时此套件必须红。

set -u

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIB="$HERE/lib.sh"

PASS=0
FAIL=0

ok()   { PASS=$((PASS + 1)); echo "  ok  - $1"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL - $1"; }

# assert_rc <期望rc> <说明> <命令...>
assert_rc() {
  local want="$1" desc="$2"; shift 2
  local rc=0
  "$@" >/dev/null 2>&1 || rc=$?
  if [ "$rc" -eq "$want" ]; then ok "$desc"; else bad "$desc (want rc=$want got rc=$rc)"; fi
}

# assert_grep <说明> <pattern> <file>
assert_grep() {
  local desc="$1" pat="$2" file="$3"
  if grep -q "$pat" "$file" 2>/dev/null; then ok "$desc"; else bad "$desc (pattern not found: $pat)"; fi
}

# assert_ngrep <说明> <pattern> <file>  （文件里不得出现 pattern）
assert_ngrep() {
  local desc="$1" pat="$2" file="$3"
  if ! grep -q "$pat" "$file" 2>/dev/null; then ok "$desc"; else bad "$desc (forbidden pattern found: $pat)"; fi
}

echo "# 1/5 lib.sh 可加载"
if [ -f "$LIB" ]; then
  # shellcheck disable=SC1091
  . "$LIB" && ok "lib.sh source 成功" || bad "lib.sh source 失败"
else
  bad "lib.sh 不存在（$LIB）——先实现再谈绿"
  echo "passed=$PASS failed=$FAIL"
  exit 1
fi

# 测试专用沙箱根：不与真实栈默认根打架
TROOT="$(mktemp -d)"
E2E_DSH_ROOT="$TROOT/root"
E2E_DSH_DAEMON_PORT=25900
E2E_DSH_PANEL_PORT=25901
E2E_DSH_WEB_PORT=25902

echo "# 2/5 端口硬校验（25xxx 段铁闸）"
assert_rc 0 "25000 段内放行"   require_port_25xxx t 25000
assert_rc 0 "25999 段内放行"   require_port_25xxx t 25999
assert_rc 1 "24999 越界拒绝"   require_port_25xxx t 24999
assert_rc 1 "26000 越界拒绝"   require_port_25xxx t 26000
assert_rc 1 "15700 生产口拒绝" require_port_25xxx t 15700
assert_rc 1 "3080 生产口拒绝"  require_port_25xxx t 3080
assert_rc 1 "3081 生产口拒绝"  require_port_25xxx t 3081
assert_rc 1 "15722 生产口拒绝" require_port_25xxx t 15722
assert_rc 1 "非数字拒绝"       require_port_25xxx t abc
assert_rc 1 "空串拒绝"         require_port_25xxx t ""
assert_rc 0 "validate_ports 三口全段内放行" validate_ports
E2E_DSH_DAEMON_PORT=15700
assert_rc 1 "validate_ports 守护口越界整组拒绝" validate_ports
E2E_DSH_DAEMON_PORT=25900

echo "# 3/5 沙箱 config.toml 生成"
mkdir -p "$E2E_DSH_ROOT"
CFG="$E2E_DSH_ROOT/config.toml"
assert_rc 0 "write_sandbox_config 成功" write_sandbox_config "$CFG" "$E2E_DSH_ROOT" 25900
assert_grep "守护口写进配置"     'port = 25900'            "$CFG"
assert_grep "数据目录钉在沙箱根" "data_dir = \"$E2E_DSH_ROOT/ferryman-data\"" "$CFG"
assert_grep "dsh 会话目录指向沙箱 home" 'dsh-home/sessions'  "$CFG"
assert_grep "阈值秒级化 summarize" 'summarize_s = 45'       "$CFG"
assert_grep "阈值秒级化 block"     'block_s = 90'           "$CFG"
assert_grep "dsh 闸门 enforce"     'dsh_mode = "enforce"'   "$CFG"
assert_ngrep "不启用渡口（无 [dock] 节）" '\[dock\]'          "$CFG"
assert_ngrep "无生产端口残迹 15722" '15722'                 "$CFG"
assert_ngrep "无生产端口残迹 15700" '15700'                 "$CFG"
assert_ngrep "无生产端口残迹 3080"  '3080'                  "$CFG"

echo "# 4/5 沙箱 profile 面（patch / package.json）"
PATCH="$E2E_DSH_ROOT/cordis.patch.yml"
assert_rc 0 "write_sandbox_patch 成功" write_sandbox_patch "$PATCH"
assert_grep "保留 ferryman-dsh 插件挂载" 'ferryman-dsh'        "$PATCH"
assert_ngrep "剔除 ios-control（生产 3081 来源）" 'ios-control' "$PATCH"
assert_ngrep "剔除 MCP 注入（npx 闪窗+网络）"     'mcp-client'  "$PATCH"
assert_ngrep "无生产密钥 tvly-"                    'tvly-'       "$PATCH"
assert_ngrep "无生产密钥 SERPAPI"                  'SERPAPI'     "$PATCH"
assert_ngrep "无生产 publicUrl"                    'publicUrl'   "$PATCH"

PKG="$E2E_DSH_ROOT/package.json"
assert_rc 0 "write_sandbox_package_json 成功" write_sandbox_package_json "$PKG"
assert_ngrep "bundle 表无 dsh-ios-control" 'dsh-ios-control'   "$PKG"
assert_grep "bundle 表保留 ferryman-dsh"    'ferryman-dsh'      "$PKG"
assert_grep "bundle 表保留 web-app"         '@deepseek-ai/dsh-web-app' "$PKG"
if command -v node >/dev/null 2>&1; then
  assert_rc 0 "package.json 是合法 JSON" node -e "JSON.parse(require('fs').readFileSync(process.argv[1],'utf8'))" "$PKG"
else
  echo "  skip - node 不可用，JSON 合法性跳过"
fi

echo "# 5/5 路径与进程面工具"
assert_grep "sandbox_data 在沙箱根下" "ferryman-data" <(sandbox_data)
MIX="$(mix_path "/tmp/some dir/x")"
case "$MIX" in
  *\\*) bad "mix_path 应产出正斜杠路径（got: $MIX）" ;;
  */*)  ok "mix_path 产出正斜杠路径" ;;
  *)    bad "mix_path 产出异常（got: $MIX）" ;;
esac
assert_rc 1 "port_listening 对空闲口返回未监听" port_listening 1
[ -z "$(port_pid 1)" ] && ok "port_pid 对空闲口返回空" || bad "port_pid 对空闲口应返回空"

rm -rf "$TROOT"

echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
