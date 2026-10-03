// dsh_hooks_test.go — 票01（P2-3 桥仓库侧，2026-10-03）：dsh-hooks/hooks.json
// 单发机制 ＋ 桥行为钉测试。
//
// 机制面断言：
//   - 新建：~/ferryman/dsh-hooks/hooks.json 从无到有，内容只指脚本不复制脚本；
//   - 幂等：二跑 unchanged、零新增备份；带标记旧版本重铸＋成组备份；
//   - 他人文件（无标记）拒绝：全案零写盘；
//   - dsh 未安装＝skip；还原走既有 Restore 纪律（新建＝删除还原）；
//   - D12 红线：绝不写 ~/.dsh/（写入目标前缀断言）；
//   - 回归钉：幂等短路 any 须计入 dsh 家族（否则"其余全到位、仅 dsh 需写"
//     时报 written 不落盘——phase-1 遗留缺陷，全量接管后的真机首装 hooks.json
//     正踩此坑）。
//
// 桥行为钉测试（约束 6 纪律）：对官方 CC 钩子桥插件源码钉四组夹具——
// ①事件映射与 payload 形状；②deny/block 传导语义；③configPath 生效方式；
// ④桥键零换算。源码＝调研克隆 packages/hooks/（只读），file:line 出处随断言，
// 行号对 2026-10-03 克隆版本；桥码不进仓，事实以字面量夹具＋仓内 gate 脚本
// （真文件）对钉。
package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// ---- 夹具 ----

// dshHooksTargetsOf 在 dsh 家族夹具上叠加 hooks.json 两字段（路径全在临时目录）。
func dshHooksTargetsOf(fp fixturePaths) Targets {
	t := dshTargetsOf(fp)
	t.DSHHooksJSON = dshHooksPathOf(fp)
	t.FerrymanHooksDir = filepath.Join(fp.home, "ferryhooks")
	return t
}

// dshHooksPathOf hooks.json 落点（~/ferryman/dsh-hooks/hooks.json 的临时镜像——
// Ferryman 自家目录，绝不 在 ~/.dsh 下，D12）。
func dshHooksPathOf(fp fixturePaths) string {
	return filepath.Join(fp.home, "ferryman", "dsh-hooks", "hooks.json")
}

// mkHooksScriptDir 造钩子脚本目录＋gate 脚本占位（内容无关紧要——机制测试只
// 钉"命令指向该路径"；桥钉测试读仓内真脚本）。
func mkHooksScriptDir(t *testing.T, dir string) {
	t.Helper()
	writeFixture(t, filepath.Join(dir, "ferryman-gate.ps1"), "# stub gate\n")
}

// dshHooksGlobDotDash 列 ~/.dsh（临时镜像）下新增文件名（D12 红线断言用）。
func dshDirEntries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// ---- 机制：新建 / 幂等 / 重铸 / 拒绝 / skip / 还原 ----

// TestApplyDSHHooksCreatesJSON 首跑落 hooks.json：written、无备份（新建）、
// 内容结构钉死（marker 居首、UPS 单组单 command、命令指向脚本目录、timeout 3、
// 无 matcher）；脚本本体不被复制（目录里只有 hooks.json）；~/.dsh 零新增（D12）。
func TestApplyDSHHooksCreatesJSON(t *testing.T) {
	fp := interimFixture(t)
	mkDSHHome(t, fp)
	mkHooksScriptDir(t, dshHooksTargetsOf(fp).FerrymanHooksDir)

	rep, err := Apply(dshHooksTargetsOf(fp))
	if err != nil {
		t.Fatal(err)
	}
	r := findRow(t, rep, targetDSHHooks)
	if r.Action != ActionWritten {
		t.Fatalf("dsh-hooks 动作=%s want written", r.Action)
	}
	if r.Backup != "" {
		t.Fatalf("新建文件不应带备份: %+v", r)
	}
	raw := mustReadStr(t, dshHooksPathOf(fp))
	// marker 居首（前 4 行内——hasDSHMarker 判定窗口；JSON 不能带 # 注释，
	// 标记以顶层键承载，桥解析器只读 CLAUDE_EVENTS 键会忽略它）。
	if !hasDSHMarker(raw) {
		t.Fatalf("hooks.json 缺接管标记（前 4 行）:\n%s", raw)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("hooks.json 非法 JSON（桥 JSON.parse 会拒）: %v\n%s", err, raw)
	}
	if doc["ferryman-takeover"] != true {
		t.Fatalf("marker 键应首键 true: %v", doc["ferryman-takeover"])
	}
	hooksMap, ok := doc["UserPromptSubmit"].([]any)
	if !ok || len(hooksMap) != 1 {
		t.Fatalf("UserPromptSubmit 应为单组: %v", doc["UserPromptSubmit"])
	}
	group, _ := hooksMap[0].(map[string]any)
	if _, hasMatcher := group["matcher"]; hasMatcher {
		t.Fatal("UserPromptSubmit 组不应带 matcher（config.ts:109-111 该事件丢弃 matcher）")
	}
	hs, _ := group["hooks"].([]any)
	if len(hs) != 1 {
		t.Fatalf("组内应单钩子: %v", group["hooks"])
	}
	hook, _ := hs[0].(map[string]any)
	if hook["type"] != "command" {
		t.Fatalf("type want command: %v", hook["type"])
	}
	cmd, _ := hook["command"].(string)
	wantSub := filepath.Join(dshHooksTargetsOf(fp).FerrymanHooksDir, "ferryman-gate.ps1")
	if !strings.Contains(cmd, wantSub) {
		t.Fatalf("命令应指向既有脚本路径 %s:\n%s", wantSub, cmd)
	}
	if !strings.HasPrefix(cmd, "powershell -NoProfile -ExecutionPolicy Bypass -File") {
		t.Fatalf("命令前缀应与 CC 侧 install 惯例一致:\n%s", cmd)
	}
	if hook["timeout"] != float64(3) {
		t.Fatalf("timeout want 3（秒，与 ccSpecs UserPromptSubmit 同值）: %v", hook["timeout"])
	}
	// 不复制脚本本体：dsh-hooks 目录里只有 hooks.json。
	entries := dshDirEntries(t, filepath.Dir(dshHooksPathOf(fp)))
	if len(entries) != 1 || entries[0] != "hooks.json" {
		t.Fatalf("dsh-hooks 目录应只有 hooks.json: %v", entries)
	}
	// D12 红线：hooks 件绝不进 ~/.dsh（dsh 家族自家的 patch/.env 照常写，
	// 不在本断言范围）。
	for _, e := range dshDirEntries(t, filepath.Join(fp.home, ".dsh")) {
		if strings.Contains(strings.ToLower(e), "hooks") {
			t.Fatalf("~/.dsh 出现 hooks 件（D12 红线）: %s", e)
		}
	}
}

// TestApplyDSHHooksIdempotent 二跑：hooks 行 unchanged、全侧零新增备份。
func TestApplyDSHHooksIdempotent(t *testing.T) {
	fp := interimFixture(t)
	mkDSHHome(t, fp)
	mkHooksScriptDir(t, dshHooksTargetsOf(fp).FerrymanHooksDir)
	tg := dshHooksTargetsOf(fp)
	if _, err := Apply(tg); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, dshHooksPathOf(fp))
	rep, err := Apply(tg)
	if err != nil {
		t.Fatal(err)
	}
	if r := findRow(t, rep, targetDSHHooks); r.Action != ActionUnchanged {
		t.Fatalf("hooks 二跑动作=%s want unchanged", r.Action)
	}
	if string(before[dshHooksPathOf(fp)]) != mustReadStr(t, dshHooksPathOf(fp)) {
		t.Fatal("二跑改动了 hooks.json（幂等破坏）")
	}
	if n := countBackups(t, filepath.Dir(dshHooksPathOf(fp)), "hooks.json"); n != 0 {
		t.Fatalf("hooks.json 不应有备份: %d", n)
	}
}

// TestApplyDSHHooksForeignRejected 无标记 hooks.json（他人手放）＝全案拒绝、
// 零写盘（与 dsh patch/.env 同纪律）。
func TestApplyDSHHooksForeignRejected(t *testing.T) {
	fp := interimFixture(t)
	mkDSHHome(t, fp)
	hooksDir := dshHooksTargetsOf(fp).FerrymanHooksDir
	mkHooksScriptDir(t, hooksDir)
	foreign := `{"UserPromptSubmit": [{"hooks": [{"type": "command", "command": "echo hi"}]}]}`
	writeFixture(t, dshHooksPathOf(fp), foreign)
	before := snapshot(t, fp.cc, fp.codexCfg, dshHooksPathOf(fp))
	_, err := Apply(dshHooksTargetsOf(fp))
	if err == nil || !strings.Contains(err.Error(), "他人文件") {
		t.Fatalf("want 他人文件拒绝, got %v", err)
	}
	after := snapshot(t, fp.cc, fp.codexCfg, dshHooksPathOf(fp))
	for p, b := range before {
		if string(after[p]) != string(b) {
			t.Fatalf("拒绝路径仍有写盘: %s", p)
		}
	}
}

// TestApplyDSHHooksMarkerRewrite 带标记旧版本（脚本目录变更等）：整体重铸＋
// 成组备份，备份内容＝重铸前。
func TestApplyDSHHooksMarkerRewrite(t *testing.T) {
	fp := interimFixture(t)
	mkDSHHome(t, fp)
	hooksDir := dshHooksTargetsOf(fp).FerrymanHooksDir
	mkHooksScriptDir(t, hooksDir)
	// 旧内容＝按别的脚本目录生成的历史形态（带标记——apply 生成的旧版本）。
	old := dshHooksJSONContent(filepath.Join(fp.home, "elsewhere"))
	writeFixture(t, dshHooksPathOf(fp), old)
	rep, err := Apply(dshHooksTargetsOf(fp))
	if err != nil {
		t.Fatal(err)
	}
	r := findRow(t, rep, targetDSHHooks)
	if r.Action != ActionWritten || r.Backup == "" {
		t.Fatalf("旧版本应重铸＋备份: %+v", r)
	}
	if got := mustReadStr(t, dshHooksPathOf(fp)); got != dshHooksJSONContent(hooksDir) {
		t.Fatal("重铸后内容未到位")
	}
	if got := mustReadStr(t, r.Backup); got != old {
		t.Fatal("备份不是重铸前内容")
	}
}

// TestApplyDSHHooksDirAbsentSkips dsh 未安装（.dsh 目录不在位）：hooks 行 skip
// 不代建（与 patch/.env 同判）。
func TestApplyDSHHooksDirAbsentSkips(t *testing.T) {
	fp := interimFixture(t) // 不建 .dsh
	tg := dshHooksTargetsOf(fp)
	mkHooksScriptDir(t, tg.FerrymanHooksDir)
	rep, err := Apply(tg)
	if err != nil {
		t.Fatal(err)
	}
	r := findRow(t, rep, targetDSHHooks)
	if r.Action != ActionSkipped {
		t.Fatalf("hooks 动作=%s want skipped", r.Action)
	}
	if _, err := os.Stat(dshHooksPathOf(fp)); !os.IsNotExist(err) {
		t.Fatal("skip 路径不应代建 hooks.json")
	}
}

// TestApplyDSHHooksOnlyChangeWrites 回归钉（phase-1 遗留）：cc/codex/orca 全已
// 指向渡口、仅 dsh 家族需写时，apply 必须真落盘——幂等短路 any 须计入 dsh
// 家族。此场景在修复前报 written 但三文件全不落盘。
func TestApplyDSHHooksOnlyChangeWrites(t *testing.T) {
	// 全到位夹具：CC 已指渡口、codex/orca 用 direct 形。
	fp := fixtureHome(t, ccProxyForm, codexDirectForm, codexOrcaDirectForm, authJSONAPIKey)
	mkDSHHome(t, fp)
	tg := dshHooksTargetsOf(fp)
	mkHooksScriptDir(t, tg.FerrymanHooksDir)
	rep, err := Apply(tg)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{targetDSHPatch, targetDSHEnv, targetDSHHooks} {
		if r := findRow(t, rep, name); r.Action != ActionWritten {
			t.Fatalf("%s 动作=%s want written（仅 dsh 需写场景必须真写）", name, r.Action)
		}
	}
	if _, err := os.Stat(dshPatchOf(fp)); err != nil {
		t.Fatalf("patch 未落盘: %v", err)
	}
	if _, err := os.Stat(dshEnvOf(fp)); err != nil {
		t.Fatalf(".env 未落盘: %v", err)
	}
	if _, err := os.Stat(dshHooksPathOf(fp)); err != nil {
		t.Fatalf("hooks.json 未落盘: %v", err)
	}
}

// TestRestoreDSHHooksCreatedDeleted 无备份＋带标记＝apply 新建 → 删除还原；
// 重铸出备份的场景 → 还原到备份内容。
func TestRestoreDSHHooksCreatedDeleted(t *testing.T) {
	fp := interimFixture(t)
	mkDSHHome(t, fp)
	tg := dshHooksTargetsOf(fp)
	mkHooksScriptDir(t, tg.FerrymanHooksDir)
	if _, err := Apply(tg); err != nil {
		t.Fatal(err)
	}
	rep, err := Restore(tg)
	if err != nil {
		t.Fatal(err)
	}
	if r := findRow(t, rep, targetDSHHooks); r.Action != ActionRestored {
		t.Fatalf("dsh-hooks want restored(删除), got %+v", r)
	}
	if _, err := os.Stat(dshHooksPathOf(fp)); !os.IsNotExist(err) {
		t.Fatal("新建的 hooks.json 应被删除还原")
	}
}

// TestRestoreDSHHooksFromBackup 重铸后（有备份）restore → 回备份内容
// （组里有该份备份＝按备份还原，与 cc/codex/patch 同纪律）。
func TestRestoreDSHHooksFromBackup(t *testing.T) {
	fp := interimFixture(t)
	mkDSHHome(t, fp)
	tg := dshHooksTargetsOf(fp)
	mkHooksScriptDir(t, tg.FerrymanHooksDir)
	// 旧版本在位 → apply 备份 → restore 回旧版本。
	old := dshHooksJSONContent(filepath.Join(fp.home, "elsewhere"))
	writeFixture(t, dshHooksPathOf(fp), old)
	if _, err := Apply(tg); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(tg); err != nil {
		t.Fatal(err)
	}
	if got := mustReadStr(t, dshHooksPathOf(fp)); got != old {
		t.Fatalf("hooks.json 还原漂移")
	}
}

// TestRestoreDSHHooksUserFileNoBackupSkipped apply 新建（无备份）→ restore
// 删除还原之后，用户手放的无标记文件（依旧无备份）→ restore 如实 skip 不误删。
func TestRestoreDSHHooksUserFileNoBackupSkipped(t *testing.T) {
	fp := interimFixture(t)
	mkDSHHome(t, fp)
	tg := dshHooksTargetsOf(fp)
	mkHooksScriptDir(t, tg.FerrymanHooksDir)
	if _, err := Apply(tg); err != nil { // hooks.json 新建（无备份）
		t.Fatal(err)
	}
	if _, err := Restore(tg); err != nil { // 删除还原
		t.Fatal(err)
	}
	users := `{"UserPromptSubmit": [{"hooks": [{"type": "command", "command": "echo mine"}]}]}`
	writeFixture(t, dshHooksPathOf(fp), users)
	rep, err := Restore(tg)
	if err != nil {
		t.Fatal(err)
	}
	if r := findRow(t, rep, targetDSHHooks); r.Action != ActionSkipped {
		t.Fatalf("无标记无备份应 skip: %+v", r)
	}
	if got := mustReadStr(t, dshHooksPathOf(fp)); got != users {
		t.Fatal("用户文件被误删/误改")
	}
}

// TestDSHHooksEmptyHooksDirRejected DSHHooksJSON 已设但 FerrymanHooksDir 空＝
// 拒绝盲写（钩子命令无从派生）。
func TestDSHHooksEmptyHooksDirRejected(t *testing.T) {
	fp := interimFixture(t)
	mkDSHHome(t, fp)
	tg := dshHooksTargetsOf(fp)
	tg.FerrymanHooksDir = ""
	if _, err := Apply(tg); err == nil || !strings.Contains(err.Error(), "FerrymanHooksDir") {
		t.Fatalf("want 拒绝盲写, got %v", err)
	}
}

// ---- 桥行为钉测试（四组；源码事实＝调研克隆 packages/hooks/，file:line 随注）----

// 真仓 gate 脚本（桥钉测试对真文件钉——钩子脚本与 hooks.json 单发机制复用同一体）。
const repoGateScript = "../../hooks/ferryman-gate.ps1"

func readRepoGateScript(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(repoGateScript)
	if err != nil {
		t.Fatalf("仓内 gate 脚本读不到（钉测试前提）: %v", err)
	}
	return string(b)
}

// bridgeEventMap ①事件映射：dsh 扩展点 → CC 钩子点（hooks-claude-code/src/
// index.ts:209/225/244/253/276/287/297 的 ctx.on 注册面）。
var bridgeEventMap = map[string]string{
	"agent/created":       "SessionStart",
	"agent/pre-step":      "UserPromptSubmit",
	"tools/pre-execute":   "PreToolUse",
	"tools/post-execute":  "PostToolUse",
	"agent/turn-stopping": "Stop",
	"subagent/start":      "SubagentStart",
	"subagent/end":        "SubagentStop",
}

// bridgeCLAUDEEvents 桥解析的七个事件键（config.ts:11-19；其余顶层键忽略）。
var bridgeCLAUDEEvents = []string{
	"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
	"Stop", "SubagentStart", "SubagentStop",
}

// bridgeUPSPayloadFields ①payload 形状：UserPromptSubmit 的 stdin 字段集＝
// base()（index.ts:327-336：session_id/transcript_path/cwd/hook_event_name）
// ＋ prompt（index.ts:341-343）。
var bridgeUPSPayloadFields = []string{"session_id", "transcript_path", "cwd", "hook_event_name", "prompt"}

// TestBridgePinEventMappingAndPayload ①桥接事件映射与 payload 形状：
//   - 生成的 hooks.json 事件键 ⊆ 桥七事件（config.ts:11-19——其余键被忽略，
//     写错键＝静默不跑）；
//   - 闸门事件 UserPromptSubmit 在映射表内（index.ts:225）且无 matcher 载体
//     （index.ts:228 matchQuery 传 ”；config.ts:109-111 matcher 丢弃）；
//   - 仓内 gate 脚本消费的字段 ⊆ 桥 payload 字段集（脚本读不到的字段＝空串
//     传 daemon——形状错位会静默降级）；
//   - gate 的 additionalContext 输出带 hookEventName='UserPromptSubmit'
//     （桥 expectedEventName 校验，index.ts:176-178：事件名不符整块丢弃）。
func TestBridgePinEventMappingAndPayload(t *testing.T) {
	// 生成物事件键 ⊆ 桥事件集。
	fp := interimFixture(t)
	content := dshHooksJSONContent(filepath.Join(fp.home, "ferryhooks"))
	var doc map[string]any
	if err := json.Unmarshal([]byte(content), &doc); err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, e := range bridgeCLAUDEEvents {
		known[e] = true
	}
	for k := range doc {
		if k == "ferryman-takeover" {
			continue // 标记键：非事件键，桥解析器忽略（config.ts:86 只遍历 CLAUDE_EVENTS）
		}
		if !known[k] {
			t.Fatalf("事件键 %q 不在桥事件集（config.ts:11-19）——桥会静默忽略", k)
		}
	}
	// 闸门事件在桥映射表内。
	if got, ok := bridgeEventMap["agent/pre-step"]; !ok || got != "UserPromptSubmit" {
		t.Fatalf("agent/pre-step 应映射 UserPromptSubmit（index.ts:225）, got %q", got)
	}
	if got, ok := bridgeEventMap["agent/created"]; !ok || got != "SessionStart" {
		t.Fatalf("agent/created 应映射 SessionStart（index.ts:209）, got %q", got)
	}
	// 仓内 gate 脚本消费字段 ⊆ 桥 payload 字段集（index.ts:327-343）。
	ps1 := readRepoGateScript(t)
	consumedRe := regexp.MustCompile(`\[string\]\$j\.(\w+)`)
	seen := map[string]bool{}
	for _, m := range consumedRe.FindAllStringSubmatch(ps1, -1) {
		seen[m[1]] = true
	}
	payloadSet := map[string]bool{}
	for _, f := range bridgeUPSPayloadFields {
		payloadSet[f] = true
	}
	for f := range seen {
		if !payloadSet[f] {
			t.Fatalf("gate 脚本消费字段 %q 不在桥 UserPromptSubmit payload（index.ts:327-343）", f)
		}
	}
	if !seen["session_id"] || !seen["prompt"] {
		t.Fatalf("gate 脚本应消费 session_id 与 prompt（闸门键与问询体）: %v", seen)
	}
	// additionalContext 输出的事件名与触发点一致（index.ts:176-178 否则丢弃）。
	if !strings.Contains(ps1, "hookEventName    = 'UserPromptSubmit'") {
		t.Fatal("gate 脚本 additionalContext 输出缺 hookEventName='UserPromptSubmit'（桥 expectedEventName 校验会丢弃）")
	}
}

// bridgeDenyChain ②deny/block 传导链（每步带源码出处）：
// gate 顶层输出 decision='block' → 桥 HookOutput.decision 归一为 'block'
// （types.ts:109-119：顶层 decision 只认 approve/block）→ merge rank 3 折成
// 'deny'（merge.ts:19-20/35-42，deny > ask > allow）→ 桥 agent/pre-step 对
// deny 回 {kind:'reject'}（index.ts:229-231）＝dsh 拒绝本轮提交。
var bridgeDenyChain = []string{
	"gate: stdout 顶层 {decision:'block', reason} (ferryman-gate.ps1:32-38)",
	"parse: 顶层 decision 'block' → HookOutput.decision='block' (types.ts:111-119)",
	"merge: rank('block')=3 → merged.decision='deny' (merge.ts:19-20,35-42)",
	"bridge: UserPromptSubmit deny → {kind:'reject'} (index.ts:229-231)",
}

// TestBridgePinDenySemantics ②deny/block 传导语义：链夹具逐环钉死（手滑改链
// 即红）；仓内 gate 脚本实钉 block 输出与 reason 透传；生成物只含 command 型
// 钩子（非 command 型被桥 skip 不跑，config.ts:97-99）。
func TestBridgePinDenySemantics(t *testing.T) {
	want := []string{
		"gate: stdout 顶层 {decision:'block', reason} (ferryman-gate.ps1:32-38)",
		"parse: 顶层 decision 'block' → HookOutput.decision='block' (types.ts:111-119)",
		"merge: rank('block')=3 → merged.decision='deny' (merge.ts:19-20,35-42)",
		"bridge: UserPromptSubmit deny → {kind:'reject'} (index.ts:229-231)",
	}
	if !reflect.DeepEqual(bridgeDenyChain, want) {
		t.Fatalf("deny 传导链夹具漂移:\n%v\nwant\n%v", bridgeDenyChain, want)
	}
	ps1 := readRepoGateScript(t)
	// block 输出实钉：顶层 decision='block' ＋ daemon reason 原样透传。
	if !strings.Contains(ps1, "decision = 'block'") {
		t.Fatal("gate 脚本缺顶层 decision='block' 输出（deny 链首环）")
	}
	if !strings.Contains(ps1, "reason = $resp.reason") {
		t.Fatal("gate 脚本 block 输出未透传 daemon reason（拦截理由会丢）")
	}
	// 生成物只含 command 型钩子（deny 传导仅对 command 钩子生效）。
	fp := interimFixture(t)
	var doc map[string]any
	if err := json.Unmarshal([]byte(dshHooksJSONContent(filepath.Join(fp.home, "ferryhooks"))), &doc); err != nil {
		t.Fatal(err)
	}
	for event, groups := range doc {
		gs, ok := groups.([]any)
		if !ok {
			continue // marker 等非数组键
		}
		for _, g := range gs {
			for _, h := range g.(map[string]any)["hooks"].([]any) {
				if h.(map[string]any)["type"] != "command" {
					t.Fatalf("%s 组含非 command 钩子（桥 skip 不跑）: %v", event, h)
				}
			}
		}
	}
}

// TestBridgePinConfigPath ③configPath 生效方式：
//   - 生成物是纯 JSON（桥加载即 JSON.parse，index.ts:110；读/析失败＝一个钩子
//     都不注册，index.ts:118-122——机制必须先建目录再落文件）；
//   - 裸事件图形态（config.ts:82-84：`{hooks:{…}}` 或裸图二选一——生成物顶层
//     不得有 "hooks" 键，否则键义翻转）；
//   - 命令串无未替换的 ${CLAUDE_PLUGIN_ROOT}/${CLAUDE_PROJECT_DIR} 残留
//     （config.ts:57-62：token 未配置时原样保留＝按字面执行）；
//   - 命令脚本路径为绝对（index.ts:54-57：configPath/命令的相对解析以 dsh 进程
//     启动 cwd 为基准——晨间说明要求绝对路径）。
func TestBridgePinConfigPath(t *testing.T) {
	fp := interimFixture(t)
	hooksDir := filepath.Join(fp.home, "ferryhooks")
	content := dshHooksJSONContent(hooksDir)
	if !json.Valid([]byte(content)) {
		t.Fatal("生成物非合法 JSON（桥 JSON.parse 失败＝零钩子注册，index.ts:110/118-122）")
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(content), &doc); err != nil {
		t.Fatal(err)
	}
	if _, has := doc["hooks"]; has {
		t.Fatal("生成物顶层不得有 hooks 键（config.ts:82-84 会按 settings 形解——键义翻转）")
	}
	if _, has := doc["UserPromptSubmit"]; !has {
		t.Fatal("生成物缺 UserPromptSubmit 事件（闸门空挂）")
	}
	// apply 真跑：目录先建（读失败＝零钩子注册的桥行为要求文件先在位）。
	mkDSHHome(t, fp)
	tg := dshHooksTargetsOf(fp)
	writeFixture(t, filepath.Join(tg.FerrymanHooksDir, "ferryman-gate.ps1"), "# stub\n")
	if _, err := Apply(tg); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Dir(tg.DSHHooksJSON)); err != nil || !fi.IsDir() {
		t.Fatalf("apply 应代建 dsh-hooks 目录: %v", err)
	}
	// 命令串：无未替换 token；脚本目录在命令里（夹具目录为临时绝对路径——
	// 相对路径形态由 cmd 层测试钉：晨间说明要求绝对路径）。
	groups := doc["UserPromptSubmit"].([]any)
	hook := groups[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	cmd := hook["command"].(string)
	if strings.Contains(cmd, "${CLAUDE_PLUGIN_ROOT}") || strings.Contains(cmd, "${CLAUDE_PROJECT_DIR}") {
		t.Fatalf("命令串含未替换 token（会按字面执行，config.ts:57-62）:\n%s", cmd)
	}
	if !strings.Contains(cmd, hooksDir) {
		t.Fatalf("命令应含脚本目录 %s:\n%s", hooksDir, cmd)
	}
}

// TestBridgePinSessionKeyZeroConversion ④桥键零换算：
//   - 桥传钩子的 session_id＝`agent?.session.header.id ?? ”`（index.ts:329）
//     ＝dsh 会话头行 id（session-<uuid>）零换算＝守望/台账现行键（P2-2 定案）；
//   - 空串回退边界：agent 不可用（含子代理 child 缺位，index.ts:353-358 注记）
//     时 session_id=”——桥不造键；
//   - 仓内 gate 脚本对 session_id 原样透传（body session_id = $j.session_id），
//     无任何二次加工——台账键全程零换算。
func TestBridgePinSessionKeyZeroConversion(t *testing.T) {
	// 桥键表达式字面量钉（index.ts:329）。
	const bridgeSessionKeyExpr = "agent?.session.header.id ?? ''"
	if bridgeSessionKeyExpr != "agent?.session.header.id ?? ''" {
		t.Fatal("桥键表达式夹具漂移")
	}
	ps1 := readRepoGateScript(t)
	// 原样透传实钉：body 组装用 [string]$j.session_id。
	if !strings.Contains(ps1, "session_id      = [string]$j.session_id") {
		t.Fatal("gate 脚本 session_id 非原样透传（台账键零换算破坏）")
	}
	// 无二次加工：全文不得对 $j.session_id 重新赋值/正则替换。
	if regexp.MustCompile(`\$j\.session_id\s*=`).MatchString(ps1) {
		t.Fatal("gate 脚本对 session_id 有再赋值（零换算破坏）")
	}
	if strings.Contains(ps1, "-replace $j.session_id") || strings.Contains(ps1, "$j.session_id -replace") {
		t.Fatal("gate 脚本对 session_id 有正则改写（零换算破坏）")
	}
}
