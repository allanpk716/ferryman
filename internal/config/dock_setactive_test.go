// dock_setactive_test.go — 票02：`upstream use` 的配置写回（SetActiveUpstream）
// 验收钉子：只改 [dock].active 一个键、其余节逐字保留、原子写（失败不动原文件）。
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// setActiveSrc 多节底稿：dock 主表（listen+active）＋上游子表＋其余节与注释，
// 用于"只动一行"的逐字节验收。
const setActiveSrc = `# 顶部注释保留
[gate]
cc_mode = "observe"

[server]
port = 7399

[dock]
listen = "127.0.0.1:15922"
active = "cc-switch"

[dock.upstreams."cc-switch"]
base_url = "http://127.0.0.1:15721"
api_key = "sk-local-1593574628abcd"

[dock.upstreams.zhipu]
base_url = "https://open.bigmodel.cn/api/anthropic"
api_key = "sk-zhipu-key-9876abcd"
model_map = { default = "glm-5.3", haiku = "glm-5.3-flash" }

[ferry]
provider = "deepseek"
`

func TestSetActiveUpstreamRewritesOnlyActiveLine(t *testing.T) {
	f := writeCfg(t, setActiveSrc)
	before := readFileT(t, f)

	if err := SetActiveUpstream(f, "zhipu"); err != nil {
		t.Fatalf("SetActiveUpstream: %v", err)
	}
	after := readFileT(t, f)

	// 逐行 diff：恰好一行不同，且是 active 行
	beforeLines := strings.Split(before, "\n")
	afterLines := strings.Split(after, "\n")
	if len(beforeLines) != len(afterLines) {
		t.Fatalf("行数变了: %d → %d（只许原地改一行）\n--- before ---\n%s\n--- after ---\n%s",
			len(beforeLines), len(afterLines), before, after)
	}
	diff := 0
	for i := range beforeLines {
		if beforeLines[i] != afterLines[i] {
			diff++
			if !strings.HasPrefix(strings.TrimSpace(afterLines[i]), "active") {
				t.Fatalf("第 %d 行被改且不是 active 行: %q → %q", i+1, beforeLines[i], afterLines[i])
			}
		}
	}
	if diff != 1 {
		t.Fatalf("改动行数 = %d, want 1\n--- after ---\n%s", diff, after)
	}
	if !strings.Contains(after, `active = "zhipu"`) {
		t.Fatalf("active 未写为新名:\n%s", after)
	}
	// 其余节逐字保留（注释、其他节、上游条目）
	for _, keep := range []string{"# 顶部注释保留", "[ferry]", `provider = "deepseek"`,
		"sk-local-1593574628abcd", "sk-zhipu-key-9876abcd", "glm-5.3-flash"} {
		if !strings.Contains(after, keep) {
			t.Fatalf("写回丢了应保留内容 %q:\n%s", keep, after)
		}
	}

	// 解码层：active 指向 zhipu，上游表逐字段不变；全文件仍过校验
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("写回后 Load: %v", err)
	}
	if cfg.Dock.Active != "zhipu" {
		t.Fatalf("active = %q, want zhipu", cfg.Dock.Active)
	}
	if len(cfg.Dock.Upstreams) != 2 ||
		cfg.Dock.Upstreams["cc-switch"].APIKey != "sk-local-1593574628abcd" ||
		cfg.Dock.Upstreams["zhipu"].ModelMap["haiku"] != "glm-5.3-flash" {
		t.Fatalf("上游表被破坏: %+v", cfg.Dock.Upstreams)
	}
	if cfg.FerryProvider != "deepseek" || cfg.GateCC != "observe" {
		t.Fatalf("其余节被破坏: %+v", cfg)
	}

	// 幂等：同名重写逐字节稳定
	snap := readFileT(t, f)
	if err := SetActiveUpstream(f, "zhipu"); err != nil {
		t.Fatalf("幂等重写: %v", err)
	}
	if got := readFileT(t, f); got != snap {
		t.Fatal("同名重写改动了文件（应逐字节稳定）")
	}
}

func TestSetActiveUpstreamInsertsWhenActiveMissing(t *testing.T) {
	src := `
[dock]
listen = "127.0.0.1:15922"

[dock.upstreams.a]
base_url = "https://a.example"
model_map = { default = "m1" }
`
	f := writeCfg(t, src)
	if err := SetActiveUpstream(f, "a"); err != nil {
		t.Fatalf("SetActiveUpstream: %v", err)
	}
	after := readFileT(t, f)
	if !strings.Contains(after, `active = "a"`) {
		t.Fatalf("active 未插入:\n%s", after)
	}
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("写回后 Load: %v", err)
	}
	if cfg.Dock.Active != "a" || cfg.Dock.Listen != "127.0.0.1:15922" {
		t.Fatalf("active/listen = %q/%q", cfg.Dock.Active, cfg.Dock.Listen)
	}
}

func TestSetActiveUpstreamPreservesCRLF(t *testing.T) {
	f := writeCfg(t, strings.ReplaceAll(setActiveSrc, "\n", "\r\n"))
	if err := SetActiveUpstream(f, "zhipu"); err != nil {
		t.Fatalf("SetActiveUpstream: %v", err)
	}
	after := readFileT(t, f)
	if !strings.Contains(after, "active = \"zhipu\"\r\n") {
		t.Fatalf("新 active 行未跟随 CRLF:\n%s", after)
	}
	if strings.Count(after, "\r\n") != strings.Count(strings.ReplaceAll(setActiveSrc, "\n", "\r\n"), "\r\n") {
		t.Fatal("CRLF 行数变了（只许原地改一行）")
	}
}

func TestSetActiveUpstreamRejectsAndKeepsFile(t *testing.T) {
	table := []struct {
		name string
		src  string
		to   string
	}{
		{"未知条目名", setActiveSrc, "nope"},
		{"无上游表（旧单值形态）", "[dock]\napi_key = \"k\"\n", "cc-switch"},
		{"无 [dock] 节", "[server]\nport = 7399\n", "a"},
		{"引号键 active 异形", "[dock]\n\"active\" = \"cc-switch\"\n\n[dock.upstreams.a]\nbase_url = \"https://a.example\"\nmodel_map = { default = \"m1\" }\n", "a"},
	}
	for _, tc := range table {
		f := writeCfg(t, tc.src)
		before := readFileT(t, f)
		err := SetActiveUpstream(f, tc.to)
		if err == nil {
			t.Errorf("%s: err = nil, want 非空", tc.name)
		}
		if got := readFileT(t, f); got != before {
			t.Errorf("%s: 拒写不得改原文件\n--- got ---\n%s", tc.name, got)
		}
	}
}

func TestSetActiveUpstreamMissingFileErrors(t *testing.T) {
	f := filepath.Join(t.TempDir(), "absent.toml")
	if err := SetActiveUpstream(f, "a"); err == nil {
		t.Fatal("文件不存在应报错")
	}
}

func TestSetActiveUpstreamAtomicWriteLeavesNoTemp(t *testing.T) {
	f := writeCfg(t, setActiveSrc)
	if err := SetActiveUpstream(f, "zhipu"); err != nil {
		t.Fatalf("SetActiveUpstream: %v", err)
	}
	des, err := os.ReadDir(filepath.Dir(f))
	if err != nil {
		t.Fatal(err)
	}
	for _, de := range des {
		if strings.Contains(de.Name(), ".active-tmp") {
			t.Fatalf("临时文件残留: %s", de.Name())
		}
	}
	var data map[string]any
	if _, err := toml.DecodeFile(f, &data); err != nil {
		t.Fatalf("产物非合法 TOML: %v", err)
	}
}

// TestSetActiveUpstreamWriteFailureCleansTemp 注入写失败：tmp 路径被空目录占住
// ——WriteFile 必败，而 os.Remove 恰能清掉空目录。写失败分支也要清 .active-tmp
// （评审 #2：此前只有 rename 失败分支清理）。
func TestSetActiveUpstreamWriteFailureCleansTemp(t *testing.T) {
	f := writeCfg(t, setActiveSrc)
	if err := os.Mkdir(f+".active-tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SetActiveUpstream(f, "zhipu"); err == nil {
		t.Fatal("注入写失败后应报错")
	}
	des, err := os.ReadDir(filepath.Dir(f))
	if err != nil {
		t.Fatal(err)
	}
	for _, de := range des {
		if strings.Contains(de.Name(), ".active-tmp") {
			t.Fatalf("写失败分支残留临时文件: %s", de.Name())
		}
	}
	if got := readFileT(t, f); got != setActiveSrc {
		t.Fatalf("写失败不得动原文件\n--- got ---\n%s", got)
	}
}

// TestSetActiveUpstreamInsertsAfterHeaderWithoutTrailingNewline [dock] 表头为
// 末行且无行尾：插入分支若直接粘连会拼出 `[dock]active = "a"` 非法 TOML
// （评审 #5：lineEnding 为空时须补 \n）。
func TestSetActiveUpstreamInsertsAfterHeaderWithoutTrailingNewline(t *testing.T) {
	src := "[dock.upstreams.a]\nbase_url = \"https://a.example\"\nmodel_map = { default = \"m1\" }\n\n[dock]"
	f := writeCfg(t, src)
	if err := SetActiveUpstream(f, "a"); err != nil {
		t.Fatalf("表头末行无行尾时插入应成功: %v", err)
	}
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("写回后 Load: %v", err)
	}
	if cfg.Dock.Active != "a" {
		t.Fatalf("active = %q, want a", cfg.Dock.Active)
	}
	if after := readFileT(t, f); !strings.HasSuffix(after, "active = \"a\"\n") {
		t.Fatalf("插入行应带行尾:\n%s", after)
	}
}
