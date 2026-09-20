package installer

// mcpinstall_test.go — 票06 验收钉子：ferryman install-mcp（CC 用户级 MCP
// 配置注册，F6 冲突语义四件＋两断言）＋ doctor"MCP 注册在位"检查项（装/卸
// 两态）。
//
// 全程临时目录注入（configPath/exe 显式传入），绝不读写真实 ~/.claude.json。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runInstallMCP 安装命令包一层 stdout 捕获（安装面直接 fmt 打印——既有
// install-cc 同款测试形态）。
func runInstallMCP(t *testing.T, configPath, exe string, force bool) (code int, out string) {
	t.Helper()
	read := captureStdout(t)
	code = InstallMCP(configPath, exe, force)
	return code, read()
}

// readWhole 原样字节读（幂等/原样不动的字节面对比用）。
func readWhole(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// mcpEntryOf 解析出的 mcpServers.ferryman 条目（缺失即 fatal）。
func mcpEntryOf(t *testing.T, data map[string]any) map[string]any {
	t.Helper()
	servers, ok := data["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers 缺失或非对象: %v", data["mcpServers"])
	}
	entry, ok := servers["ferryman"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers.ferryman 缺失或非对象: %v", servers["ferryman"])
	}
	return entry
}

// ---- 首次执行写入用户级条目；重复执行幂等 ----

func TestInstallMCPFirstRunWritesUserEntry(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, ".claude.json")
	exe := filepath.Join(tmp, "ferryman.exe")
	// 预置无关顶层键与既有其他 server——安装不得惊动。
	writeJSONFile(t, cfg, map[string]any{
		"other": map[string]any{"keep": 1},
		"mcpServers": map[string]any{
			"someone-else": map[string]any{"type": "stdio", "command": "npx", "args": []any{"-y", "x"}},
		},
	})
	code, out := runInstallMCP(t, cfg, exe, false)
	if code != 0 {
		t.Fatalf("首次执行应退出 0, got %d:\n%s", code, out)
	}
	data := parseFile(t, cfg)
	entry := mcpEntryOf(t, data)
	if entry["type"] != "stdio" || entry["command"] != exe {
		t.Fatalf("条目 type/command 不符: %v", entry)
	}
	if got := entry["args"]; strings.TrimSpace(compactOf(t, got)) != `["mcp"]` {
		t.Fatalf("条目 args 应为 ["+"mcp"+"]: %v", got)
	}
	// 既有内容不惊动。
	if d, ok := data["other"].(map[string]any); !ok || d["keep"] != 1.0 {
		t.Fatalf("无关顶层键被动: %v", data["other"])
	}
	servers := data["mcpServers"].(map[string]any)
	if _, ok := servers["someone-else"]; !ok {
		t.Fatal("既有其他 server 条目被动")
	}
	// 成功回显：白名单形状（command/args）＋注册位置；无凭据类字样。
	for _, want := range []string{"mcpServers.ferryman", "mcp"} {
		if !strings.Contains(out, want) {
			t.Fatalf("成功回显应含 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "api_key") || strings.Contains(out, "Bearer") {
		t.Fatalf("成功回显不得出现凭据类字样:\n%s", out)
	}
}

func TestInstallMCPIdempotentTwoRunsIdentical(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, ".claude.json")
	exe := filepath.Join(tmp, "ferryman.exe")
	writeJSONFile(t, cfg, map[string]any{"keep": map[string]any{"a": 1}})
	if code, _ := runInstallMCP(t, cfg, exe, false); code != 0 {
		t.Fatal("第一次执行应退出 0")
	}
	after1 := readWhole(t, cfg)
	if code, _ := runInstallMCP(t, cfg, exe, false); code != 0 {
		t.Fatal("第二次执行应退出 0")
	}
	after2 := readWhole(t, cfg)
	if string(after1) != string(after2) {
		t.Fatalf("重复执行应幂等（配置字节一致）:\n一: %s\n二: %s", after1, after2)
	}
}

// 首次执行无文件：直接创建（含父目录）。
func TestInstallMCPFirstRunCreatesMissingFile(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "home", ".claude.json")
	exe := filepath.Join(tmp, "ferryman.exe")
	code, _ := runInstallMCP(t, cfg, exe, false)
	if code != 0 {
		t.Fatalf("无文件首跑应退出 0, got %d", code)
	}
	entry := mcpEntryOf(t, parseFile(t, cfg))
	if entry["command"] != exe {
		t.Fatalf("条目 command 不符: %v", entry)
	}
}

// ---- F6 ①：自有条目（形态兼容）→ 幂等覆盖（无需 --force） ----

func TestInstallMCPOwnStaleEntryOverwrittenWithoutForce(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, ".claude.json")
	exe := filepath.Join(tmp, "ferryman.exe")
	writeJSONFile(t, cfg, map[string]any{"mcpServers": map[string]any{
		"ferryman": map[string]any{ // 自建旧条目：exe 已挪窝（陈旧路径）
			"type": "stdio", "command": filepath.Join(tmp, "old", "ferryman.exe"),
			"args": []any{"mcp"}, "env": map[string]any{}},
	}})
	code, out := runInstallMCP(t, cfg, exe, false)
	if code != 0 {
		t.Fatalf("自有旧条目应幂等覆盖（默认即覆盖）, got %d:\n%s", code, out)
	}
	entry := mcpEntryOf(t, parseFile(t, cfg))
	if entry["command"] != exe {
		t.Fatalf("旧路径应被刷成当前 exe: %v", entry["command"])
	}
	// 第二次执行结果一致（①幂等覆盖的直接证据）。
	if code, _ := runInstallMCP(t, cfg, exe, false); code != 0 {
		t.Fatal("重复执行应退出 0")
	}
	if entry2 := mcpEntryOf(t, parseFile(t, cfg)); entry2["command"] != exe {
		t.Fatalf("重复执行后条目漂移: %v", entry2)
	}
}

// ---- F6 ② ＋ 断言一：外部条目默认拒绝、配置原样不动；--force 才覆盖 ----

func TestInstallMCPExternalEntryRefusedDefaultUntouched(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, ".claude.json")
	exe := filepath.Join(tmp, "ferryman.exe")
	writeJSONFile(t, cfg, map[string]any{"mcpServers": map[string]any{
		"ferryman": map[string]any{ // 外部条目：command 非 ferryman
			"type": "stdio", "command": "npx",
			"args": []any{"-y", "some-other-mcp"},
			"env":  map[string]any{"SOME_KEY": "v"},
		},
	}})
	before := readWhole(t, cfg)
	code, out := runInstallMCP(t, cfg, exe, false)
	if code == 0 {
		t.Fatalf("外部条目默认应拒绝（退出非零）:\n%s", out)
	}
	if string(readWhole(t, cfg)) != string(before) {
		t.Fatal("F6 断言一：拒绝路径配置必须原样不动（字节一致）")
	}
	for _, want := range []string{"拒绝", "外部", "--force"} {
		if !strings.Contains(out, want) {
			t.Fatalf("拒绝文案应含 %q（原因说明＋force 指引）:\n%s", want, out)
		}
	}

	// --force 才覆盖。
	code, out = runInstallMCP(t, cfg, exe, true)
	if code != 0 {
		t.Fatalf("--force 应覆盖, got %d:\n%s", code, out)
	}
	entry := mcpEntryOf(t, parseFile(t, cfg))
	if entry["command"] != exe {
		t.Fatalf("force 后条目应为当前 exe: %v", entry)
	}
}

// 不兼容桶：command 基名是 ferryman 但 args 无 "mcp" → 同样默认拒绝。
func TestInstallMCPIncompatibleEntryRefusedDefault(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, ".claude.json")
	exe := filepath.Join(tmp, "ferryman.exe")
	writeJSONFile(t, cfg, map[string]any{"mcpServers": map[string]any{
		"ferryman": map[string]any{
			"type": "stdio", "command": filepath.Join(tmp, "ferryman.exe"),
			"args": []any{"serve"}},
	}})
	before := readWhole(t, cfg)
	code, out := runInstallMCP(t, cfg, exe, false)
	if code == 0 {
		t.Fatalf("不兼容条目默认应拒绝:\n%s", out)
	}
	if string(readWhole(t, cfg)) != string(before) {
		t.Fatal("拒绝路径配置必须原样不动")
	}
	if !strings.Contains(out, "不兼容") && !strings.Contains(out, "mcp") {
		t.Fatalf("拒绝文案应说明不兼容原因:\n%s", out)
	}
	// force 后落位。
	if code, _ := runInstallMCP(t, cfg, exe, true); code != 0 {
		t.Fatal("--force 应覆盖")
	}
	if entry := mcpEntryOf(t, parseFile(t, cfg)); entry["command"] != exe {
		t.Fatalf("force 后条目不符: %v", entry)
	}
}

// 非对象条目（连形状都没有）→ 外部/不兼容路径。
func TestInstallMCPNonObjectEntryRefused(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, ".claude.json")
	exe := filepath.Join(tmp, "ferryman.exe")
	writeJSONFile(t, cfg, map[string]any{"mcpServers": map[string]any{"ferryman": "oops"}})
	before := readWhole(t, cfg)
	code, _ := runInstallMCP(t, cfg, exe, false)
	if code == 0 {
		t.Fatal("非对象条目默认应拒绝")
	}
	if string(readWhole(t, cfg)) != string(before) {
		t.Fatal("拒绝路径配置必须原样不动")
	}
	if code, _ := runInstallMCP(t, cfg, exe, true); code != 0 {
		t.Fatal("--force 应覆盖非对象条目")
	}
}

// ---- F6 ③④ ＋ 断言二：任何回显路径不出现明文凭据 ----

func TestInstallMCPNoPlaintextCredInAnyEchoPath(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, ".claude.json")
	exe := filepath.Join(tmp, "ferryman.exe")
	fakeEnv := "sk-fake-env-4b7e21"
	fakeHdr := "Bearer sk-fake-hdr-8c2d90"
	// R1 反例补强：凭据搭在 args（旗标值/嵌套对象）与 command 查询串里的
	// 三种形态——值级回显一律不得漏（票06 评审三个实测反例）。
	fakeArgFlag := "sk-fake-args-77aa"
	fakeArgMap := "sk-fake-argmap-5c1b"
	fakeCmdQ := "sk-fake-cmd-9d3e"
	writeJSONFile(t, cfg, map[string]any{"mcpServers": map[string]any{
		"ferryman": map[string]any{
			"type": "stdio", "command": "npx",
			"args": []any{"-y", "x", "--token", fakeArgFlag,
				map[string]any{"Authorization": "Bearer " + fakeArgMap}},
			"env":     map[string]any{"FAKE_API_TOKEN": fakeEnv},
			"headers": map[string]any{"Authorization": fakeHdr},
		},
	}})
	// 回显路径一：默认拒绝的原因说明＋脱敏摘要。
	_, out := runInstallMCP(t, cfg, exe, false)
	for _, bad := range []string{fakeEnv, fakeHdr, "sk-fake"} {
		if strings.Contains(out, bad) {
			t.Fatalf("F6 断言二（拒绝回显）泄漏明文凭据 %q:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "已隐藏") {
		t.Fatalf("脱敏摘要应带「<已隐藏 N 键>」式掩码:\n%s", out)
	}
	if !strings.Contains(out, "args=<5 元素>") {
		t.Fatalf("args 摘要应为形状级（元素计数）:\n%s", out)
	}
	// 回显路径二：--force 覆盖前被替换条目的脱敏摘要。
	_, out = runInstallMCP(t, cfg, exe, true)
	for _, bad := range []string{fakeEnv, fakeHdr, "sk-fake"} {
		if strings.Contains(out, bad) {
			t.Fatalf("F6 断言二（force 回显）泄漏明文凭据 %q:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "已隐藏") {
		t.Fatalf("force 覆盖前应打印脱敏摘要（含掩码）:\n%s", out)
	}
	// 回显路径三：自有旧条目携带 env 凭据被幂等覆盖——成功回显只含新条目形状。
	writeJSONFile(t, cfg, map[string]any{"mcpServers": map[string]any{
		"ferryman": map[string]any{
			"type": "stdio", "command": filepath.Join(tmp, "old", "ferryman.exe"),
			"args": []any{"mcp"},
			"env":  map[string]any{"FAKE_API_TOKEN": fakeEnv}},
	}})
	_, out = runInstallMCP(t, cfg, exe, false)
	if strings.Contains(out, fakeEnv) || strings.Contains(out, "sk-fake") {
		t.Fatalf("成功回显不得泄漏旧条目 env 凭据:\n%s", out)
	}
	// 回显路径四（R1）：command 查询串搭凭据的外部条目——拒绝 reason 与摘要
	// 只出净化基名，凭据不得出现。
	writeJSONFile(t, cfg, map[string]any{"mcpServers": map[string]any{
		"ferryman": map[string]any{
			"type": "stdio",
			"command": "C:/tools/ferryman.exe?token=" + fakeCmdQ,
			"args":   []any{"mcp"},
		},
	}})
	_, out = runInstallMCP(t, cfg, exe, false)
	if strings.Contains(out, "sk-fake") || strings.Contains(out, fakeCmdQ) {
		t.Fatalf("command 查询串凭据不得进回显:\n%s", out)
	}
	if !strings.Contains(out, "ferryman.exe") { // 净化基名仍在（可辨识）
		t.Fatalf("净化基名应保留（辨识用）:\n%s", out)
	}
	// doctor 面同判据：外部条目的失败 Detail（agent 可见）不含任何 canary。
	ch := CheckMCPRegistration(cfg)
	if ch.OK {
		t.Fatal("查询串 command 应判外部（基名不匹配）")
	}
	for _, bad := range []string{fakeCmdQ, "sk-fake"} {
		if strings.Contains(ch.Msg, bad) {
			t.Fatalf("doctor Detail 泄漏 %q: %s", bad, ch.Msg)
		}
	}
}

// TestInstallMCPNullEntryRequiresForce R1：null 限值视同占位（与 doctor 非对象
// 判定对齐）——默认拒绝、字节原样；--force 才覆盖。
func TestInstallMCPNullEntryRequiresForce(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, ".claude.json")
	exe := filepath.Join(tmp, "ferryman.exe")
	writeJSONFile(t, cfg, map[string]any{"mcpServers": map[string]any{"ferryman": nil}})
	before := readWhole(t, cfg)
	code, out := runInstallMCP(t, cfg, exe, false)
	if code != 1 || !strings.Contains(out, "拒绝") {
		t.Fatalf("null 条目默认应拒绝, got code=%d:\n%s", code, out)
	}
	if string(before) != string(readWhole(t, cfg)) {
		t.Fatal("拒绝路径配置字节必须原样不动")
	}
	code, _ = runInstallMCP(t, cfg, exe, true)
	if code != 0 {
		t.Fatalf("--force 覆盖 null 应成功, got %d", code)
	}
	if ch := CheckMCPRegistration(cfg); !ch.OK {
		t.Fatalf("force 覆盖后应判在位: %s", ch.Msg)
	}
}

// ---- 坏形状响亮拒绝（静默覆盖 = 数据丢失） ----

func TestInstallMCPBadJSONRefusedLoud(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, ".claude.json")
	exe := filepath.Join(tmp, "ferryman.exe")
	if err := os.WriteFile(cfg, []byte("not json {"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _ := runInstallMCP(t, cfg, exe, false)
	if code == 0 {
		t.Fatal("坏 JSON 应响亮拒绝")
	}
	if string(readWhole(t, cfg)) != "not json {" {
		t.Fatal("坏 JSON 拒绝路径不得改写文件")
	}
}

func TestInstallMCPNonObjectServersRefused(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, ".claude.json")
	exe := filepath.Join(tmp, "ferryman.exe")
	writeJSONFile(t, cfg, map[string]any{"mcpServers": "not-an-object"})
	before := readWhole(t, cfg)
	code, _ := runInstallMCP(t, cfg, exe, true)
	if code == 0 {
		t.Fatal("mcpServers 非对象应响亮拒绝（force 也不许）")
	}
	if string(readWhole(t, cfg)) != string(before) {
		t.Fatal("非对象拒绝路径不得改写文件")
	}
}

// ---- doctor"MCP 注册在位"检查项（装/卸两态各断言一次） ----

func TestCheckMCPRegistrationStates(t *testing.T) {
	tmp := t.TempDir()
	exe := filepath.Join(tmp, "ferryman.exe")

	// 卸态一：文件不存在 → 失败并说明＋修法。
	c := CheckMCPRegistration(filepath.Join(tmp, "no.json"))
	if c.OK || !strings.Contains(c.Msg, "不在位") || !strings.Contains(c.Msg, "install-mcp") {
		t.Fatalf("未装应失败并说明: %+v", c)
	}

	// 卸态二：文件在但无 ferryman 条目 → 失败。
	cfg := filepath.Join(tmp, ".claude.json")
	writeJSONFile(t, cfg, map[string]any{"mcpServers": map[string]any{}})
	c = CheckMCPRegistration(cfg)
	if c.OK || !strings.Contains(c.Msg, "不在位") {
		t.Fatalf("无条目应失败: %+v", c)
	}

	// 卸态三：外部条目占用 → 失败并说明（默认拒绝语义的一致面）。
	writeJSONFile(t, cfg, map[string]any{"mcpServers": map[string]any{
		"ferryman": map[string]any{"type": "stdio", "command": "npx", "args": []any{"-y", "x"}}}})
	c = CheckMCPRegistration(cfg)
	if c.OK || !strings.Contains(c.Msg, "install-mcp") {
		t.Fatalf("外部条目应失败并给修法: %+v", c)
	}
	if !strings.Contains(c.Msg, "npx") {
		t.Fatalf("外部条目失败说明应点名形态（白名单字段 command）: %+v", c)
	}

	// 装态：自有形态条目 → 通过。
	writeJSONFile(t, cfg, map[string]any{"mcpServers": map[string]any{
		"ferryman": map[string]any{
			"type": "stdio", "command": exe, "args": []any{"mcp"}, "env": map[string]any{}}}})
	c = CheckMCPRegistration(cfg)
	if !c.OK || !strings.Contains(c.Msg, "在位") {
		t.Fatalf("装态应通过: %+v", c)
	}

	// 坏 JSON → 失败（解析错误如实报）。
	if err := os.WriteFile(cfg, []byte("not json {"), 0o644); err != nil {
		t.Fatal(err)
	}
	c = CheckMCPRegistration(cfg)
	if c.OK || !strings.Contains(c.Msg, "解析失败") {
		t.Fatalf("坏 JSON 应失败: %+v", c)
	}
}

// doctorResults 聚合面：装/卸两态（greenDoctorDeps 的 HOME 下装过 → pass；
// 删掉 .claude.json → fail）。
func TestDoctorResultsMCPRegistrationTwoStates(t *testing.T) {
	deps, _ := greenDoctorDeps(t, func() map[string]any { return map[string]any{"health_alert": false} })
	byName := func(rs []CheckResult) CheckResult {
		for _, r := range rs {
			if r.Name == "mcp_registration" {
				return r
			}
		}
		t.Fatalf("缺 mcp_registration 项: %+v", rs)
		return CheckResult{}
	}
	// 装态（greenDoctorDeps 已装）。
	if r := byName(doctorResults(deps)); r.Status != StatusPass {
		t.Fatalf("装态应 pass: %+v", r)
	}
	// 卸态：删掉用户级 MCP 配置 → fail 并说明。
	if err := os.Remove(UserMCPConfigPath(deps.Home)); err != nil {
		t.Fatal(err)
	}
	if r := byName(doctorResults(deps)); r.Status != StatusFail || !strings.Contains(r.Detail, "不在位") {
		t.Fatalf("卸态应 fail 并说明: %+v", r)
	}
}
