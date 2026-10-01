// doctor_probe_test.go — 票05 doctor 三项体检真/假形态验收（只读探针）+ F7
// 认证形态判定表。全部临时目录，绝不写真机配置。
package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- ① CC 指向渡口 ----

func TestCheckCCPointsDock(t *testing.T) {
	// 真：interim（已指向渡口）
	fp := interimFixture(t)
	if v := CheckCCPointsDock(fp.cc, dockBase); !v.OK {
		t.Fatalf("interim 应通过: %s", v.Detail)
	}
	// 假：直连形（未指向渡口）
	fp2 := fixtureHome(t, ccDirectForm, codexInterimForm, codexOrcaForm, authJSONAPIKey)
	if v := CheckCCPointsDock(fp2.cc, dockBase); v.OK {
		t.Fatalf("直连形应失败: %s", v.Detail)
	}
	// 假：键缺失
	p := filepath.Join(t.TempDir(), "settings.json")
	writeFixture(t, p, "{\"env\": {\"ANTHROPIC_AUTH_TOKEN\": \"x\"}}")
	if v := CheckCCPointsDock(p, dockBase); v.OK || !strings.Contains(v.Detail, "缺失") {
		t.Fatalf("键缺失应失败并点名: %+v", v)
	}
	// 假：文件缺失
	if v := CheckCCPointsDock(filepath.Join(t.TempDir(), "nope.json"), dockBase); v.OK {
		t.Fatalf("文件缺失应失败: %+v", v)
	}
	// 假：坏 JSON
	bad := filepath.Join(t.TempDir(), "settings.json")
	writeFixture(t, bad, "{oops")
	if v := CheckCCPointsDock(bad, dockBase); v.OK || !strings.Contains(v.Detail, "解析失败") {
		t.Fatalf("坏 JSON 应失败并报解析: %+v", v)
	}
}

// ---- ② codex 两份指向渡口 + wire_api + hooks 旗标 ----

func TestCheckCodexPointsDock(t *testing.T) {
	// 真：apply 后的 interim（两份接管形态）
	fp := interimFixture(t)
	if _, err := Apply(targetsOf(fp)); err != nil {
		t.Fatal(err)
	}
	if v := CheckCodexPointsDock(fp.codexCfg, fp.orcaCfg, dockCodexBase); !v.OK {
		t.Fatalf("接管形态应通过: %s", v.Detail)
	}
	// 假：未接管（两份仍指 15721）
	fp2 := interimFixture(t)
	v := CheckCodexPointsDock(fp2.codexCfg, fp2.orcaCfg, dockCodexBase)
	if v.OK || !strings.Contains(v.Detail, "15721") {
		t.Fatalf("未接管应失败并点名现值: %+v", v)
	}
	// 假：orca 份缺失
	fp3 := interimFixture(t)
	if err := os.Remove(fp3.orcaCfg); err != nil {
		t.Fatal(err)
	}
	if v := CheckCodexPointsDock(fp3.codexCfg, fp3.orcaCfg, dockCodexBase); v.OK ||
		!strings.Contains(v.Detail, "orca-codex") {
		t.Fatalf("orca 缺失应失败并点名: %+v", v)
	}
	// 假：hooks 旗标缺
	form := strings.Replace(codexInterimForm, "[features]\nhooks = true\n", "", 1)
	fp4 := fixtureHome(t, ccProxyForm, form, codexOrcaForm, authJSONAPIKey)
	writeFixture(t, fp4.codexCfg, strings.Replace(mustReadStr(t, fp4.codexCfg),
		"http://127.0.0.1:15721/v1", dockCodexBase, 1))
	writeFixture(t, fp4.orcaCfg, strings.Replace(mustReadStr(t, fp4.orcaCfg),
		"http://127.0.0.1:15721/v1", dockCodexBase, 1))
	if v := CheckCodexPointsDock(fp4.codexCfg, fp4.orcaCfg, dockCodexBase); v.OK ||
		!strings.Contains(v.Detail, "hooks") {
		t.Fatalf("旗标缺应失败并点名: %+v", v)
	}
	// 假：wire_api 非 responses
	form5 := strings.Replace(codexInterimForm, `wire_api = "responses"`, `wire_api = "chat"`, 1)
	fp5 := fixtureHome(t, ccProxyForm, form5, codexOrcaForm, authJSONAPIKey)
	writeFixture(t, fp5.codexCfg, strings.Replace(mustReadStr(t, fp5.codexCfg),
		"http://127.0.0.1:15721/v1", dockCodexBase, 1))
	writeFixture(t, fp5.orcaCfg, strings.Replace(mustReadStr(t, fp5.orcaCfg),
		"http://127.0.0.1:15721/v1", dockCodexBase, 1))
	if v := CheckCodexPointsDock(fp5.codexCfg, fp5.orcaCfg, dockCodexBase); v.OK ||
		!strings.Contains(v.Detail, "wire_api") {
		t.Fatalf("wire_api 异值应失败并点名: %+v", v)
	}
}

// ---- ③ orca codex 健康 ----

func TestCheckOrcaCodexHealth(t *testing.T) {
	// 真：接管形态 + apikey 形态
	fp := interimFixture(t)
	if _, err := Apply(targetsOf(fp)); err != nil {
		t.Fatal(err)
	}
	if v := CheckOrcaCodexHealth(fp.orcaCfg, dockCodexBase); !v.OK {
		t.Fatalf("健康形态应通过: %s", v.Detail)
	}
	// 假：配置不存在
	if v := CheckOrcaCodexHealth(filepath.Join(t.TempDir(), "config.toml"), dockCodexBase); v.OK ||
		!strings.Contains(v.Detail, "不存在") {
		t.Fatalf("缺失应失败并点名: %+v", v)
	}
	// 假：OAuth 形态 → 转人工（真 OAuth 登录态 config 无 bearer 行）
	fp2 := fixtureHome(t, ccProxyForm, codexInterimForm, codexOrcaForm, authJSONOAuth)
	oauthOrca := strings.Replace(codexOrcaForm,
		"experimental_bearer_token = \"PROXY_MANAGED\"\n", "", 1)
	writeFixture(t, fp2.orcaCfg, strings.Replace(oauthOrca,
		"http://127.0.0.1:15721/v1", dockCodexBase, 1))
	if v := CheckOrcaCodexHealth(fp2.orcaCfg, dockCodexBase); v.OK ||
		!strings.Contains(v.Detail, "转人工") {
		t.Fatalf("OAuth 形态应失败并指转人工: %+v", v)
	}
	// 假：未指向渡口
	fp3 := interimFixture(t)
	if v := CheckOrcaCodexHealth(fp3.orcaCfg, dockCodexBase); v.OK ||
		!strings.Contains(v.Detail, "15721") {
		t.Fatalf("未指向应失败并点名现值: %+v", v)
	}
}

// ---- F7 认证形态判定表 ----

func TestCodexAuthForm(t *testing.T) {
	// bearer（config 层 experimental_bearer_token）优先
	fp := interimFixture(t)
	if got := CodexAuthForm(fp.codexCfg); got != AuthBearer {
		t.Fatalf("PROXY_MANAGED bearer 应判 bearer, got %q", got)
	}
	// auth.json apikey
	fp2 := fixtureHome(t, ccProxyForm, codexDirectForm, codexOrcaForm, authJSONAPIKey)
	if got := CodexAuthForm(fp2.codexCfg); got != AuthAPIKey {
		t.Fatalf("auth.json apikey 应判 apikey, got %q", got)
	}
	// OAuth：auth.json tokens 且无 bearer
	fp3 := fixtureHome(t, ccProxyForm, codexDirectForm, codexOrcaForm, authJSONOAuth)
	if got := CodexAuthForm(fp3.codexCfg); got != AuthOAuth {
		t.Fatalf("tokens 形态应判 chatgpt-oauth, got %q", got)
	}
	// 两处皆无 → unknown
	home := t.TempDir()
	p := filepath.Join(home, "config.toml")
	writeFixture(t, p, codexDirectForm)
	if got := CodexAuthForm(p); got != AuthUnknown {
		t.Fatalf("无 auth.json 无 bearer 应判 unknown, got %q", got)
	}
	// 配置不存在 → unknown
	if got := CodexAuthForm(filepath.Join(t.TempDir(), "nope.toml")); got != AuthUnknown {
		t.Fatalf("缺失应判 unknown, got %q", got)
	}
}
