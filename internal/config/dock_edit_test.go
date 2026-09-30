// dock_edit_test.go — 票06：provider add/remove/import-ccswitch 的上游表增删
// 写回（AddDockUpstream/RemoveDockUpstream/MergeDockUpstreams）验收钉子。
//
// 验收口径：
//   - 文本手术只增删目标条目：其余节（[server]/[ferry] 等）与 [dock] 未知键
//     （drain_timeout_s 等）逐字保留；
//   - 前置校验（与 Validate 同单源）：写回会产生让守护拒启的表 → 拒写、原文件
//     字节不动；
//   - active 条目拒删；重复名拒增；无上游表（旧单值形态）拒绝；
//   - dialect/codex 新字段落盘与保留（renderUpstreamEntry 与解析互逆）；
//   - 原子写（临时文件＋rename）由实现保证，测试以读回内容为准。
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// editCfgSrc add/remove 共用底稿：两条既有条目（一条带 dialect/codex 新字段、
// 一条旧形态）+ dock 未知键 + 其余节，用于"写回不动它"验收。
const editCfgSrc = `[server]
port = 7399

[dock]
listen = "127.0.0.1:15722"
active = "zhipu"
drain_timeout_s = 90.0

[dock.upstreams.zhipu]
base_url = "https://open.bigmodel.cn/api/anthropic"
api_key = "sk-zhipu-00001234ef12"
model_map = { default = "glm-5.3", codex = "glm-5.3" }
dialect = "anthropic"

[dock.upstreams.native]
base_url = "https://api.example.com/v1"
api_key = "sk-native-5566aabb"
model_map = { default = "gpt-x", codex = "gpt-x" }
dialect = "openai_responses"
codex = "unsupported"

[ferry]
provider = "deepseek"
`

func readFileEdit(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustLoadEdit(t *testing.T, path string) *Config {
	t.Helper()
	cfg, err := Load(path, false)
	if err != nil {
		t.Fatalf("写回产物过不了 Load/Validate: %v", err)
	}
	return cfg
}

func TestAddDockUpstreamWritesEntryPreservesRest(t *testing.T) {
	f := writeCfg(t, editCfgSrc)
	before := readFileEdit(t, f)
	up := DockUpstream{
		BaseURL:  "https://api.kimi.com/coding/",
		APIKey:   "sk-kimi-9988aabbccd0",
		ModelMap: map[string]string{"default": "kimi-for-coding", "codex": "k3"},
		Dialect:  DialectAnthropic,
	}
	if err := AddDockUpstream(f, "kimi", up); err != nil {
		t.Fatalf("AddDockUpstream: %v", err)
	}
	after := readFileEdit(t, f)
	// 新条目在位（含 base_url/api_key/model_map）
	for _, want := range []string{
		"[dock.upstreams.kimi]", "https://api.kimi.com/coding/", "kimi-for-coding",
	} {
		if !strings.Contains(after, want) {
			t.Errorf("写回缺新条目内容 %q:\n%s", want, after)
		}
	}
	// 其余节与 [dock] 未知键逐字保留
	for _, keep := range []string{
		"[server]", "port = 7399", "drain_timeout_s = 90.0", "[ferry]",
		`provider = "deepseek"`, "sk-zhipu-00001234ef12", "sk-native-5566aabb",
		`dialect = "openai_responses"`, `codex = "unsupported"`,
	} {
		if !strings.Contains(after, keep) {
			t.Errorf("写回丢了应保留内容 %q:\n%s", keep, after)
		}
	}
	// 既有条目字节原样（zhipu 节整段还在，未重排）
	if !strings.Contains(after, "base_url = \"https://open.bigmodel.cn/api/anthropic\"") {
		t.Errorf("既有条目被改写:\n%s", after)
	}
	// active 未动
	if !strings.Contains(after, `active = "zhipu"`) {
		t.Errorf("active 被动:\n%s", after)
	}
	// 解析回读：新条目字段互逆、既有条目逐字段相等
	cfg := mustLoadEdit(t, f)
	got, ok := cfg.Dock.Upstreams["kimi"]
	if !ok {
		t.Fatalf("解析回读缺 kimi 条目: %+v", cfg.Dock.Upstreams)
	}
	if got.BaseURL != up.BaseURL || got.APIKey != up.APIKey ||
		got.Dialect != DialectAnthropic || got.ModelMap["codex"] != "k3" ||
		got.ModelMap["default"] != "kimi-for-coding" {
		t.Errorf("kimi 回读不符: %+v", got)
	}
	native, ok := cfg.Dock.Upstreams["native"]
	if !ok || native.Dialect != DialectOpenAIResponses || native.Codex != CodexUnsupported {
		t.Errorf("既有 openai_responses 条目回读不符: %+v", native)
	}
	_ = before
}

func TestAddDockUpstreamEmptyDialectNormalizesToAnthropic(t *testing.T) {
	f := writeCfg(t, editCfgSrc)
	up := DockUpstream{BaseURL: "https://api.deepseek.com/anthropic",
		APIKey:   "sk-deepseek-0000aabb",
		ModelMap: map[string]string{"default": "deepseek-flash"}}
	if err := AddDockUpstream(f, "deepseek", up); err != nil {
		t.Fatalf("AddDockUpstream: %v", err)
	}
	cfg := mustLoadEdit(t, f)
	got := cfg.Dock.Upstreams["deepseek"]
	if got.Dialect != DialectAnthropic {
		t.Errorf("空 dialect 应归一 anthropic, got %q", got.Dialect)
	}
	if !strings.Contains(readFileEdit(t, f), `dialect = "anthropic"`) {
		t.Errorf("归一后的 dialect 应显式落盘（避免解析歧义）:\n%s", readFileEdit(t, f))
	}
}

func TestAddDockUpstreamCodexUnsupportedFlagPersists(t *testing.T) {
	f := writeCfg(t, editCfgSrc)
	up := DockUpstream{BaseURL: "https://x.example.com/anthropic",
		APIKey:   "sk-x-0000aabb",
		ModelMap: map[string]string{"default": "m-x"},
		Dialect:  DialectAnthropic, Codex: CodexUnsupported}
	if err := AddDockUpstream(f, "xonly", up); err != nil {
		t.Fatalf("AddDockUpstream: %v", err)
	}
	cfg := mustLoadEdit(t, f)
	got := cfg.Dock.Upstreams["xonly"]
	if got.CodexAvailability() != CodexUnsupported {
		t.Errorf("codex=unsupported 应落盘并否决推导, got %q", got.CodexAvailability())
	}
	if !strings.Contains(readFileEdit(t, f), `codex = "unsupported"`) {
		t.Errorf("codex 否决位应落盘:\n%s", readFileEdit(t, f))
	}
}

func TestAddDockUpstreamRefusesDuplicate(t *testing.T) {
	f := writeCfg(t, editCfgSrc)
	before := readFileEdit(t, f)
	err := AddDockUpstream(f, "zhipu", DockUpstream{BaseURL: "https://z.example"})
	if err == nil {
		t.Fatal("重复名应拒绝")
	}
	if !strings.Contains(err.Error(), "zhipu") {
		t.Errorf("拒绝信息应含条目名: %v", err)
	}
	if readFileEdit(t, f) != before {
		t.Fatal("拒绝路径原文件不得动")
	}
}

func TestAddDockUpstreamRefusesEntryThatBreaksValidation(t *testing.T) {
	f := writeCfg(t, editCfgSrc)
	before := readFileEdit(t, f)
	// 非本地 base_url 缺 model_map.default → 写回会让守护拒启，前置校验必须拦
	err := AddDockUpstream(f, "broken", DockUpstream{BaseURL: "https://b.example"})
	if err == nil {
		t.Fatal("缺 default 的非本地条目应被前置校验拒绝")
	}
	if !strings.Contains(err.Error(), "default") {
		t.Errorf("拒绝信息应点名缺 default: %v", err)
	}
	if readFileEdit(t, f) != before {
		t.Fatal("校验拒绝原文件不得动")
	}
}

func TestAddDockUpstreamRefusesNoUpstreamTable(t *testing.T) {
	f := writeCfg(t, "[dock]\napi_key = \"k\"\n")
	err := AddDockUpstream(f, "a", DockUpstream{BaseURL: "https://a.example"})
	if err == nil {
		t.Fatal("旧单值形态（无上游表）应拒绝")
	}
}

func TestAddDockUpstreamCRLFKeepsLineEndings(t *testing.T) {
	f := writeCfg(t, strings.ReplaceAll(editCfgSrc, "\n", "\r\n"))
	up := DockUpstream{BaseURL: "https://api.kimi.com/coding/",
		APIKey:   "sk-kimi-9988aabbccd0",
		ModelMap: map[string]string{"default": "kimi-for-coding"}}
	if err := AddDockUpstream(f, "kimi", up); err != nil {
		t.Fatalf("AddDockUpstream(CRLF): %v", err)
	}
	cfg := mustLoadEdit(t, f)
	if _, ok := cfg.Dock.Upstreams["kimi"]; !ok {
		t.Fatal("CRLF 文件加条目后解析缺条目")
	}
	if after := readFileEdit(t, f); strings.Count(after, "\r\n") < strings.Count(editCfgSrc, "\n") {
		t.Errorf("CRLF 行尾应保留:\n%q", after)
	}
}

func TestRemoveDockUpstreamDeletesEntry(t *testing.T) {
	f := writeCfg(t, editCfgSrc)
	if err := RemoveDockUpstream(f, "native"); err != nil {
		t.Fatalf("RemoveDockUpstream: %v", err)
	}
	after := readFileEdit(t, f)
	if strings.Contains(after, "native") || strings.Contains(after, "sk-native-5566aabb") {
		t.Errorf("native 条目应整段删除:\n%s", after)
	}
	for _, keep := range []string{"zhipu", "sk-zhipu-00001234ef12", "[ferry]",
		`provider = "deepseek"`, "drain_timeout_s = 90.0"} {
		if !strings.Contains(after, keep) {
			t.Errorf("删除波及了无关内容 %q:\n%s", keep, after)
		}
	}
	cfg := mustLoadEdit(t, f)
	if _, ok := cfg.Dock.Upstreams["native"]; ok {
		t.Fatal("解析回读仍有 native")
	}
	if _, ok := cfg.Dock.Upstreams["zhipu"]; !ok {
		t.Fatal("zhipu 应保留")
	}
}

func TestRemoveDockUpstreamQuotedChineseName(t *testing.T) {
	src := strings.Replace(editCfgSrc, "[dock.upstreams.native]", `[dock.upstreams."智谱-2"]`, 1)
	f := writeCfg(t, src)
	if err := RemoveDockUpstream(f, "智谱-2"); err != nil {
		t.Fatalf("引号键条目删除: %v", err)
	}
	if after := readFileEdit(t, f); strings.Contains(after, "智谱-2") {
		t.Errorf("引号键条目应整段删除:\n%s", after)
	}
	mustLoadEdit(t, f)
}

func TestRemoveDockUpstreamRefusesActive(t *testing.T) {
	f := writeCfg(t, editCfgSrc)
	before := readFileEdit(t, f)
	err := RemoveDockUpstream(f, "zhipu") // active
	if err == nil {
		t.Fatal("active 条目应拒删")
	}
	if !strings.Contains(err.Error(), "active") {
		t.Errorf("拒绝信息应点名 active: %v", err)
	}
	if readFileEdit(t, f) != before {
		t.Fatal("拒绝路径原文件不得动")
	}
}

func TestRemoveDockUpstreamRefusesUnknown(t *testing.T) {
	f := writeCfg(t, editCfgSrc)
	before := readFileEdit(t, f)
	if err := RemoveDockUpstream(f, "ghost"); err == nil {
		t.Fatal("未知条目应拒绝")
	}
	if readFileEdit(t, f) != before {
		t.Fatal("拒绝路径原文件不得动")
	}
}

func TestMergeDockUpstreamsBatchAdds(t *testing.T) {
	f := writeCfg(t, editCfgSrc)
	ups := map[string]DockUpstream{
		"kimi": {BaseURL: "https://api.kimi.com/coding/",
			APIKey:   "sk-kimi-9988aabbccd0",
			ModelMap: map[string]string{"default": "kimi-for-coding"}},
		"deepseek": {BaseURL: "https://api.deepseek.com/anthropic",
			APIKey:   "sk-ds-0000aabb",
			ModelMap: map[string]string{"default": "deepseek-flash"}},
	}
	if err := MergeDockUpstreams(f, ups); err != nil {
		t.Fatalf("MergeDockUpstreams: %v", err)
	}
	cfg := mustLoadEdit(t, f)
	for name := range ups {
		if _, ok := cfg.Dock.Upstreams[name]; !ok {
			t.Errorf("批量导入缺 %s", name)
		}
	}
	if len(cfg.Dock.Upstreams) != 4 {
		t.Errorf("条目数 = %d, want 4（2 既有 + 2 新增）", len(cfg.Dock.Upstreams))
	}
	if cfg.Dock.Active != "zhipu" {
		t.Errorf("active 应保持 zhipu, got %q", cfg.Dock.Active)
	}
}

func TestMergeDockUpstreamsRefusesAnyConflict(t *testing.T) {
	f := writeCfg(t, editCfgSrc)
	before := readFileEdit(t, f)
	err := MergeDockUpstreams(f, map[string]DockUpstream{
		"zhipu":  {BaseURL: "https://evil.example", ModelMap: map[string]string{"default": "m"}},
		"fresha": {BaseURL: "https://fresh.example", ModelMap: map[string]string{"default": "m"}},
	})
	if err == nil {
		t.Fatal("批量含冲突名应整体拒绝（不部分写入）")
	}
	if !strings.Contains(err.Error(), "zhipu") {
		t.Errorf("拒绝信息应点名冲突条目: %v", err)
	}
	if readFileEdit(t, f) != before {
		t.Fatal("冲突拒绝原文件不得动")
	}
}

func TestCheckDockUpstreamEntryReportsProblems(t *testing.T) {
	cfg := mustLoadEdit(t, writeCfg(t, editCfgSrc))
	// 好条目：无问题
	if probs := CheckDockUpstreamEntry(cfg.Dock, "kimi", DockUpstream{
		BaseURL: "https://api.kimi.com/coding/", APIKey: "sk-k",
		ModelMap: map[string]string{"default": "kimi-for-coding"}}); len(probs) != 0 {
		t.Errorf("合法条目不应有问题: %v", probs)
	}
	// 坏条目：非本地缺 default
	probs := CheckDockUpstreamEntry(cfg.Dock, "broken", DockUpstream{BaseURL: "https://b.example"})
	if len(probs) == 0 {
		t.Fatal("缺 default 条目应报问题")
	}
	// 重复名（与既有表冲突）
	probs = CheckDockUpstreamEntry(cfg.Dock, "zhipu", DockUpstream{
		BaseURL: "https://z.example", ModelMap: map[string]string{"default": "m"}})
	if len(probs) == 0 {
		t.Fatal("与既有表重名应报问题")
	}
}

func TestEditDockUpstreamsMissingFileErrors(t *testing.T) {
	p := filepath.Join(t.TempDir(), "absent.toml")
	if err := AddDockUpstream(p, "a", DockUpstream{BaseURL: "https://a.example"}); err == nil {
		t.Fatal("配置不存在应报错")
	}
	if err := RemoveDockUpstream(p, "a"); err == nil {
		t.Fatal("配置不存在应报错")
	}
}

func TestAddDockUpstreamNoDockSectionRefused(t *testing.T) {
	f := writeCfg(t, "[server]\nport = 7399\n")
	if err := AddDockUpstream(f, "a", DockUpstream{BaseURL: "https://a.example",
		ModelMap: map[string]string{"default": "m"}}); err == nil {
		t.Fatal("无 [dock] 节应拒绝")
	}
}
