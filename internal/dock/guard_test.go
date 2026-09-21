// guard_test.go — 票06：双改写守卫与改写准入验收钉子（spec F10/S3）。
// 覆盖票面验收：rewrite=true＋upstream 三种回环形态（127.0.0.1:15721 /
// localhost:15721 / [::1]:15722）→ 全部拒绝改写；open.bigmodel.cn → 放行；
// 回环别名归一化与本地中转判定单源已迁 config（票01：config.IsLocalRelayAddr
// ——校验豁免与守卫共用），归一化表见 internal/config/dock_upstreams_test.go。
package dock

import (
	"strings"
	"testing"

	"ferryman/internal/config"
)

func TestDoubleRewriteRiskForms(t *testing.T) {
	table := []struct {
		upstream string
		want     bool
	}{
		{"http://127.0.0.1:15721", true},
		{"http://localhost:15721", true},
		{"http://[::1]:15722", true},
		{"http://127.0.0.1:15723", true},
		{"https://open.bigmodel.cn/api/paas/v4", false},
		{"http://localhost:8080", false}, // 回环但非中转端口
		{"http://10.0.0.5:15721", false}, // 中转端口但非回环
		{"http://127.0.0.1", false},      // 无端口不算
	}
	for _, tc := range table {
		if got := DoubleRewriteRisk(tc.upstream); got != tc.want {
			t.Errorf("DoubleRewriteRisk(%q) = %v, want %v", tc.upstream, got, tc.want)
		}
	}
}

// dockCfgFor 组装请求改写的 [dock] 节（model_map 形状由调用方定）。
func dockCfgFor(upstream string, modelMap map[string]string) *config.DockCfg {
	return &config.DockCfg{
		Listen:          "127.0.0.1:15722",
		UpstreamBaseURL: upstream,
		RewriteEnabled:  true,
		APIKey:          "sk-real-key",
		ModelMap:        modelMap,
		TextOnly:        []string{"glm-5.3-air"},
	}
}

func TestResolveRewriteTable(t *testing.T) {
	full := map[string]string{"claude-opus-5": "glm-5.5", "default": "glm-4.7-flash"}
	noDefault := map[string]string{"claude-opus-5": "glm-5.5"}
	emptyDefault := map[string]string{"default": ""}

	table := []struct {
		name       string
		dcfg       *config.DockCfg
		wantOK     bool
		wantSubstr string // ok=false 时 reason 须含（空串＝不查 reason）
	}{
		{"开关关＝未请求改写（reason 空，不算告警）",
			&config.DockCfg{UpstreamBaseURL: "https://open.bigmodel.cn"}, false, ""},
		{"大模型上游＋default 齐备→放行",
			dockCfgFor("https://open.bigmodel.cn/api/paas/v4", full), true, ""},
		{"127.0.0.1:15721 → 拒", dockCfgFor("http://127.0.0.1:15721", full), false, "透传"},
		{"localhost:15721 → 拒", dockCfgFor("http://localhost:15721", full), false, "透传"},
		{"[::1]:15722 → 拒", dockCfgFor("http://[::1]:15722", full), false, "透传"},
		{"缺 default 键 → 拒", dockCfgFor("https://open.bigmodel.cn", noDefault), false, "default"},
		{"default 值为空 → 拒", dockCfgFor("https://open.bigmodel.cn", emptyDefault), false, "default"},
	}
	for _, tc := range table {
		_, ok, reason := ResolveRewrite(tc.dcfg)
		if ok != tc.wantOK {
			t.Errorf("%s: ok = %v, want %v（reason=%q）", tc.name, ok, tc.wantOK, reason)
			continue
		}
		if !ok && tc.wantSubstr != "" && !strings.Contains(reason, tc.wantSubstr) {
			t.Errorf("%s: reason = %q, want 含 %q", tc.name, reason, tc.wantSubstr)
		}
	}
}

func TestResolveRewriteConfigConversion(t *testing.T) {
	// 换算语义：default 键摘出成 Default（不残留在别名表）、text_only 克隆。
	dcfg := dockCfgFor("https://open.bigmodel.cn", map[string]string{
		"claude-opus-5": "glm-5.5", "default": "glm-4.7-flash",
	})
	rw, ok, _ := ResolveRewrite(dcfg)
	if !ok {
		t.Fatal("应放行")
	}
	if rw.Default != "glm-4.7-flash" {
		t.Fatalf("Default = %q", rw.Default)
	}
	if _, clash := rw.ModelMap["default"]; clash {
		t.Fatal("default 键不得残留在别名表")
	}
	if rw.ModelMap["claude-opus-5"] != "glm-5.5" {
		t.Fatalf("别名映射丢失: %v", rw.ModelMap)
	}
	if len(rw.TextOnly) != 1 || rw.TextOnly[0] != "glm-5.3-air" {
		t.Fatalf("text_only 转换不符: %v", rw.TextOnly)
	}
}
