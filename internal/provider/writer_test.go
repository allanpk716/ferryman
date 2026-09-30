// writer_test.go — 票05（服务商接管）：外科式配置写入器验收。临时家目录复刻
// 真实世界三形态（cc-switch 代理形/直连形/orca 镜像形，蓝本见
// .scratch/provider-takeover/issues/05 背景材料），断言面：
//   - 外科性：他人键逐字节原样，恰只有目标行变更；
//   - 幂等：二跑零差异、零新增备份；
//   - 备份：三份齐（含 orca）、同戳成组、bak-ferryman 命名；
//   - F7：chatgpt-OAuth 形态拒绝转人工，全案零写盘。
//
// 绝不写真机 ~/.claude、~/.codex、orca 家——全部路径在 t.TempDir 下。
package provider

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	dockBase      = "http://127.0.0.1:15722"
	dockCodexBase = "http://127.0.0.1:15722/v1"
)

// ---- 真实世界三形态夹具（合成数据，零真实密钥） ----

// ccDirectForm 直连形（历史形态）：BASE_URL 直指 bigmodel。env 多档模型名键 +
// statusLine/hooks/enabledPlugins 等大量他人键——外科性断言的靶子。
const ccDirectForm = `{
  "env": {
    "ANTHROPIC_AUTH_TOKEN": "PROXY_MANAGED",
    "ANTHROPIC_BASE_URL": "https://open.bigmodel.cn/api/paas/cc",
    "ANTHROPIC_MODEL": "glm-5.3",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "glm-5.3",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "glm-5.3-airx",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "glm-5.3-air"
  },
  "statusLine": {
    "type": "command",
    "command": "C:/bin/statusline.cmd",
    "padding": 0
  },
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "C:/repo/ferryman-restore.ps1",
            "timeout": 30
          }
        ]
      }
    ]
  },
  "enabledPlugins": {
    "code-review@claude-plugins": true
  },
  "includeCoAuthoredBy": false
}
`

// ccProxyForm cc-switch 代理形（interim）：已指向渡口。
var ccProxyForm = strings.Replace(ccDirectForm,
	"https://open.bigmodel.cn/api/paas/cc", dockBase, 1)

// codexInterimForm cc-switch 代理形（interim 蓝本）：15721 + PROXY_MANAGED 占位。
const codexInterimForm = `model = "glm-5.3"
model_provider = "custom"
disable_response_storage = true

[model_providers.custom]
name = "cc-switch"
base_url = "http://127.0.0.1:15721/v1"
wire_api = "responses"
requires_openai_auth = true
experimental_bearer_token = "PROXY_MANAGED"

[features]
hooks = true

[mcp_servers.serena]
command = "uvx"
args = ["--from", "git+https://example.invalid/serena", "serena"]
startup_timeout_sec = 60
`

// codexDirectForm 直连形（历史形态）：base_url 直指 bigmodel，无 bearer 行。
const codexDirectForm = `model = "glm-5.3"
model_provider = "custom"
disable_response_storage = true

[model_providers.custom]
name = "bigmodel"
base_url = "https://open.bigmodel.cn/api/paas/coding/v1"
wire_api = "responses"
requires_openai_auth = true

[features]
hooks = true

[mcp_servers.serena]
command = "uvx"
args = ["--from", "git+https://example.invalid/serena", "serena"]
startup_timeout_sec = 60
`

// orcaHooksState orca 镜像形独有的他人节（[hooks.state.*]）。
const orcaHooksState = `
[hooks.state.C--WorkSpace-agent-Ferryman]
trusted = true

[hooks.state.C--WorkSpace-agent-Ferryman--xcheck]
trusted = false
`

// codexOrcaForm orca 镜像形：与普通份同构（interim），另有 [hooks.state.*]。
var codexOrcaForm = codexInterimForm + orcaHooksState

// codexOrcaDirectForm orca 镜像的直连变体。
var codexOrcaDirectForm = codexDirectForm + orcaHooksState

const authJSONAPIKey = `{"OPENAI_API_KEY": "sk-noop-placeholder"}` + "\n"
const authJSONOAuth = `{"tokens": {"id_token": "noop", "access_token": "noop", "refresh_token": "noop"}, "last_refresh": "2026-09-01T00:00:00Z"}` + "\n"

// codexInterimOAuthForm chatgpt-OAuth 形态蓝本（F7 的拒绝对象）：真 OAuth
// 登录态下 config.toml 无 bearer 行、auth.json 是 tokens——与 apikey/bearer
// 形态的区别就在这两处。
var codexInterimOAuthForm = strings.Replace(codexInterimForm,
	"experimental_bearer_token = \"PROXY_MANAGED\"\n", "", 1)

// ---- 夹具落位与快照小件 ----

type fixturePaths struct {
	home, cc, codexCfg, orcaHome, orcaCfg string
}

func authPathOf(cfgPath string) string {
	return filepath.Join(filepath.Dir(cfgPath), "auth.json")
}

func writeFixture(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixtureHome 临时家目录：三份配置按给定形态落位（路径全在 t.TempDir 下）。
func fixtureHome(t *testing.T, ccForm, codexForm, orcaForm, authJSON string) fixturePaths {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	fp := fixturePaths{
		home:     home,
		cc:       filepath.Join(home, ".claude", "settings.json"),
		codexCfg: filepath.Join(home, ".codex", "config.toml"),
		orcaHome: filepath.Join(home, "AppData", "Roaming", "orca", "codex-runtime-home", "home"),
	}
	fp.orcaCfg = filepath.Join(fp.orcaHome, "config.toml")
	writeFixture(t, fp.cc, ccForm)
	writeFixture(t, fp.codexCfg, codexForm)
	writeFixture(t, fp.orcaCfg, orcaForm)
	writeFixture(t, authPathOf(fp.codexCfg), authJSON)
	writeFixture(t, authPathOf(fp.orcaCfg), authJSON)
	return fp
}

// interimFixture interim 拓扑蓝本：CC 已指向渡口、codex 两份仍指 cc-switch 15721。
func interimFixture(t *testing.T) fixturePaths {
	return fixtureHome(t, ccProxyForm, codexInterimForm, codexOrcaForm, authJSONAPIKey)
}

func targetsOf(fp fixturePaths) Targets {
	return Targets{
		CCSettings:      fp.cc,
		CodexConfig:     fp.codexCfg,
		OrcaCodexConfig: fp.orcaCfg,
		DockBaseURL:     dockBase,
	}
}

func mustReadStr(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// snapshot 逐路径读盘快照（内容逐字节比对用）。
func snapshot(t *testing.T, paths ...string) map[string][]byte {
	t.Helper()
	m := map[string][]byte{}
	for _, p := range paths {
		m[p] = []byte(mustReadStr(t, p))
	}
	return m
}

// assertUnchanged 断言快照里所有文件逐字节原样（零写入断言）。
func assertUnchanged(t *testing.T, before map[string][]byte) {
	t.Helper()
	for p, b := range before {
		if got := mustReadStr(t, p); got != string(b) {
			t.Fatalf("%s 被改动（应零写入）", p)
		}
	}
}

// lineDiff 逐行 diff（行数不同即 fatal）——返回内容相异行的 {序号,旧行,新行}。
// 仅用于「不应增删行」的外科断言；插入/追加场景直接断言内容。
func lineDiff(t *testing.T, oldRaw, newRaw string) [][3]string {
	t.Helper()
	a, b := strings.Split(oldRaw, "\n"), strings.Split(newRaw, "\n")
	if len(a) != len(b) {
		t.Fatalf("行数变化 %d→%d（本断言场景外科写入不应增删行）\n--- old ---\n%s\n--- new ---\n%s",
			len(a), len(b), oldRaw, newRaw)
	}
	var out [][3]string
	for i := range a {
		if a[i] != b[i] {
			out = append(out, [3]string{strconv.Itoa(i), a[i], b[i]})
		}
	}
	return out
}

// bakFiles 三处目录里的全部 bak-ferryman 备份（完整路径）。
func bakFiles(t *testing.T, fp fixturePaths) []string {
	t.Helper()
	var out []string
	for _, dir := range []string{filepath.Dir(fp.cc), filepath.Dir(fp.codexCfg), filepath.Dir(fp.orcaCfg)} {
		matches, err := filepath.Glob(filepath.Join(dir, "*"+backupMarker+"*"))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, matches...)
	}
	return out
}

var bakStampRe = regexp.MustCompile(`\.bak-ferryman-(\d{8}-\d{6})$`)

// ---- 外科写入：三形态 ----

// interim 拓扑 apply：CC 已指向渡口（零写入），codex 两份恰两行变更
// （base_url + bearer 占位），其余行逐字节原样；三份备份同戳成组。
func TestApplyInterimTopologySurgical(t *testing.T) {
	fp := interimFixture(t)
	before := snapshot(t, fp.cc, fp.codexCfg, fp.orcaCfg,
		authPathOf(fp.codexCfg), authPathOf(fp.orcaCfg))
	rep, err := Apply(targetsOf(fp))
	if err != nil {
		t.Fatal(err)
	}
	// CC：interim 已指向渡口 → 零写入
	if got := mustReadStr(t, fp.cc); got != string(before[fp.cc]) {
		t.Fatalf("CC 应零写入（interim 已指向渡口）:\n%s", got)
	}
	if r := rep.Targets[0]; r.Name != targetCC || r.Action != ActionUnchanged {
		t.Fatalf("CC 报告应 unchanged: %+v", r)
	}
	// codex 两份：恰 2 行变更（base_url / experimental_bearer_token）
	for _, cfg := range []string{fp.codexCfg, fp.orcaCfg} {
		d := lineDiff(t, string(before[cfg]), mustReadStr(t, cfg))
		if len(d) != 2 {
			t.Fatalf("%s 应恰 2 行变更, got %v", cfg, d)
		}
		if !strings.Contains(d[0][1], "base_url") || !strings.Contains(d[1][1], "experimental_bearer_token") {
			t.Fatalf("变更行错位: %v", d)
		}
		if !strings.Contains(d[0][2], dockCodexBase) {
			t.Fatalf("base_url 未指向渡口: %q", d[0][2])
		}
		if !strings.Contains(d[1][2], PlaceholderToken) {
			t.Fatalf("占位未换: %q", d[1][2])
		}
	}
	// orca 他人节 [hooks.state.*] 逐字节原样
	if !strings.Contains(mustReadStr(t, fp.orcaCfg),
		"[hooks.state.C--WorkSpace-agent-Ferryman]\ntrusted = true") {
		t.Fatal("orca [hooks.state.*] 他人节必须原样保留")
	}
	// auth.json 零触碰
	for _, p := range []string{authPathOf(fp.codexCfg), authPathOf(fp.orcaCfg)} {
		if got := mustReadStr(t, p); got != string(before[p]) {
			t.Fatalf("%s 被改动（写入器不碰 auth.json）", p)
		}
	}
	// 备份三份齐、同戳、命名合规
	backs := bakFiles(t, fp)
	if len(backs) != 3 {
		t.Fatalf("备份应三份齐（含 orca）, got %d: %v", len(backs), backs)
	}
	stamps := map[string]bool{}
	for _, n := range backs {
		m := bakStampRe.FindStringSubmatch(n)
		if m == nil {
			t.Fatalf("备份命名不合 bak-ferryman 惯例: %s", n)
		}
		stamps[m[1]] = true
	}
	if len(stamps) != 1 {
		t.Fatalf("三份备份应同戳成组: %v", backs)
	}
}

// 直连形（历史形态）apply：CC 恰 1 行变更（BASE_URL），codex 恰 1 行变更
// （base_url），orca 同；hooks.state 他人节原样。
func TestApplyDirectFormsSurgical(t *testing.T) {
	fp := fixtureHome(t, ccDirectForm, codexDirectForm, codexOrcaDirectForm, authJSONAPIKey)
	before := snapshot(t, fp.cc, fp.codexCfg, fp.orcaCfg)
	if _, err := Apply(targetsOf(fp)); err != nil {
		t.Fatal(err)
	}
	d := lineDiff(t, string(before[fp.cc]), mustReadStr(t, fp.cc))
	if len(d) != 1 || !strings.Contains(d[0][1], "ANTHROPIC_BASE_URL") ||
		!strings.Contains(d[0][2], dockBase) {
		t.Fatalf("CC 外科变更不符: %v", d)
	}
	for _, cfg := range []string{fp.codexCfg, fp.orcaCfg} {
		d := lineDiff(t, string(before[cfg]), mustReadStr(t, cfg))
		if len(d) != 1 || !strings.Contains(d[0][1], "base_url") ||
			!strings.Contains(d[0][2], dockCodexBase) {
			t.Fatalf("codex 外科变更不符(%s): %v", cfg, d)
		}
	}
	if !strings.Contains(mustReadStr(t, fp.orcaCfg), "[hooks.state.C--WorkSpace-agent-Ferryman--xcheck]\ntrusted = false") {
		t.Fatal("orca [hooks.state.*] 他人节必须原样保留")
	}
}

// ---- 占位令牌 ----

// PROXY_MANAGED → FERRYMAN_MANAGED 只发生在 codex 配置；CC 的
// ANTHROPIC_AUTH_TOKEN="PROXY_MANAGED" 属他人键，必须原样。
func TestPlaceholderSwapScope(t *testing.T) {
	fp := interimFixture(t)
	if _, err := Apply(targetsOf(fp)); err != nil {
		t.Fatal(err)
	}
	got := mustReadStr(t, fp.codexCfg)
	if !strings.Contains(got, `experimental_bearer_token = "`+PlaceholderToken+`"`) {
		t.Fatalf("codex bearer 占位应为 %s: %s", PlaceholderToken, got)
	}
	if strings.Contains(got, "PROXY_MANAGED") {
		t.Fatal("codex 配置不应残留 PROXY_MANAGED")
	}
	if !strings.Contains(mustReadStr(t, fp.cc), `"ANTHROPIC_AUTH_TOKEN": "PROXY_MANAGED"`) {
		t.Fatal("CC 他人键 ANTHROPIC_AUTH_TOKEN 必须原样")
	}
}

// 非 PROXY_MANAGED 的 bearer 值（他人真钥/他占位）不碰。
func TestPlaceholderNonManagedValueUntouched(t *testing.T) {
	form := strings.Replace(codexInterimForm,
		`experimental_bearer_token = "PROXY_MANAGED"`,
		`experimental_bearer_token = "sk-someone-else"`, 1)
	fp := fixtureHome(t, ccProxyForm, form, codexOrcaForm, authJSONAPIKey)
	before := snapshot(t, fp.codexCfg)
	if _, err := Apply(targetsOf(fp)); err != nil {
		t.Fatal(err)
	}
	d := lineDiff(t, string(before[fp.codexCfg]), mustReadStr(t, fp.codexCfg))
	if len(d) != 1 || !strings.Contains(d[0][1], "base_url") {
		t.Fatalf("非占位 bearer 不应被碰, diff: %v", d)
	}
}

// ---- 幂等 ----

func TestApplyIdempotentSecondRunZeroWrite(t *testing.T) {
	fp := interimFixture(t)
	if _, err := Apply(targetsOf(fp)); err != nil {
		t.Fatal(err)
	}
	first := snapshot(t, fp.cc, fp.codexCfg, fp.orcaCfg,
		authPathOf(fp.codexCfg), authPathOf(fp.orcaCfg))
	n := len(bakFiles(t, fp))
	rep, err := Apply(targetsOf(fp))
	if err != nil {
		t.Fatal(err)
	}
	assertUnchanged(t, first)
	if got := len(bakFiles(t, fp)); got != n {
		t.Fatalf("幂等跑不得新增备份: %d→%d", n, got)
	}
	for _, r := range rep.Targets {
		if r.Action != ActionUnchanged && r.Action != ActionSkipped {
			t.Fatalf("二跑应全 unchanged: %+v", r)
		}
	}
}

// ---- 备份与还原（backup.go 配套；往返见 backup_test.go） ----

// ---- F7 认证前置校验 ----

func TestApplyRejectsOAuthForm(t *testing.T) {
	fp := fixtureHome(t, ccProxyForm, codexInterimOAuthForm,
		codexInterimOAuthForm+orcaHooksState, authJSONOAuth)
	before := snapshot(t, fp.cc, fp.codexCfg, fp.orcaCfg,
		authPathOf(fp.codexCfg), authPathOf(fp.orcaCfg))
	_, err := Apply(targetsOf(fp))
	if err == nil {
		t.Fatal("OAuth 形态应拒绝")
	}
	if !strings.Contains(err.Error(), "转人工") {
		t.Fatalf("报错应指转人工: %v", err)
	}
	assertUnchanged(t, before) // 全案零写盘
	if n := len(bakFiles(t, fp)); n != 0 {
		t.Fatalf("拒绝时零备份, got %d", n)
	}
}

// 仅 orca 份 OAuth（普通份 bearer 形态合法）同样全案拒绝——前置校验在任何
// 写盘之前。
func TestApplyRejectsOrcaOnlyOAuth(t *testing.T) {
	fp := interimFixture(t)
	writeFixture(t, fp.orcaCfg, codexInterimOAuthForm+orcaHooksState)
	writeFixture(t, authPathOf(fp.orcaCfg), authJSONOAuth)
	before := snapshot(t, fp.cc, fp.codexCfg, fp.orcaCfg, authPathOf(fp.codexCfg))
	_, err := Apply(targetsOf(fp))
	if err == nil || !strings.Contains(err.Error(), "转人工") {
		t.Fatalf("orca 份 OAuth 应全案拒绝: %v", err)
	}
	assertUnchanged(t, before)
}

// ---- 目标缺缺 ----

// orca 缺失 → skipped 行（不代建、不失败）；CC 缺失 → 报错（接管前置缺失）。
func TestApplyMissingTargets(t *testing.T) {
	fp := interimFixture(t)
	if err := os.Remove(fp.orcaCfg); err != nil {
		t.Fatal(err)
	}
	rep, err := Apply(targetsOf(fp))
	if err != nil {
		t.Fatal(err)
	}
	sawSkip := false
	for _, r := range rep.Targets {
		if r.Name == targetOrca {
			sawSkip = r.Action == ActionSkipped
		}
	}
	if !sawSkip {
		t.Fatalf("orca 缺失应 skipped: %+v", rep.Targets)
	}
	fp2 := interimFixture(t)
	if err := os.Remove(fp2.cc); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(targetsOf(fp2)); err == nil {
		t.Fatal("CC 缺失应报错")
	}
}

// ---- wire_api / hooks 旗标补缺（幂等语义的「校验+补缺」） ----

// wire_api 异值（chat）→ 定向改回 responses（替换行，不增删行）。
func TestApplyCodexWireApiRewritten(t *testing.T) {
	form := strings.Replace(codexInterimForm, `wire_api = "responses"`, `wire_api = "chat"`, 1)
	fp := fixtureHome(t, ccProxyForm, form, codexOrcaForm, authJSONAPIKey)
	before := snapshot(t, fp.codexCfg)
	if _, err := Apply(targetsOf(fp)); err != nil {
		t.Fatal(err)
	}
	got := mustReadStr(t, fp.codexCfg)
	if !strings.Contains(got, `wire_api = "responses"`) || strings.Contains(got, `"chat"`) {
		t.Fatalf("wire_api 应被定向改回 responses:\n%s", got)
	}
	if d := lineDiff(t, string(before[fp.codexCfg]), got); len(d) != 3 {
		t.Fatalf("应恰 3 行变更（base_url/wire_api/bearer）: %v", d)
	}
}

// wire_api 缺失 → base_url 行后补缺插入（他人行不挪动）。
func TestApplyCodexWireApiInserted(t *testing.T) {
	form := strings.Replace(codexInterimForm, "wire_api = \"responses\"\n", "", 1)
	fp := fixtureHome(t, ccProxyForm, form, codexOrcaForm, authJSONAPIKey)
	if _, err := Apply(targetsOf(fp)); err != nil {
		t.Fatal(err)
	}
	got := mustReadStr(t, fp.codexCfg)
	if !strings.Contains(got, `base_url = "`+dockCodexBase+`"`+"\n"+`wire_api = "responses"`) {
		t.Fatalf("wire_api 应插在 base_url 行后:\n%s", got)
	}
	if !strings.Contains(got, `[mcp_servers.serena]`) {
		t.Fatal("他人节不得丢")
	}
}

// [features] 节整个缺失 → 末尾补缺追加。
func TestApplyEnsuresHooksFlagSectionAppended(t *testing.T) {
	form := strings.Replace(codexInterimForm, "[features]\nhooks = true\n", "", 1)
	if strings.Contains(form, "[features]") {
		t.Fatal("夹具预检: [features] 应已移除")
	}
	fp := fixtureHome(t, ccProxyForm, form, codexOrcaForm, authJSONAPIKey)
	if _, err := Apply(targetsOf(fp)); err != nil {
		t.Fatal(err)
	}
	got := mustReadStr(t, fp.codexCfg)
	if !strings.Contains(got, "[features]\nhooks = true\n") {
		t.Fatalf("应补缺 [features] hooks = true:\n%s", got)
	}
	if !strings.Contains(got, `[mcp_servers.serena]`) {
		t.Fatal("他人节不得丢")
	}
}

// [features] 节在、旗标行缺 → 节头下插入（不新建节）。
func TestApplyEnsuresHooksFlagInserted(t *testing.T) {
	form := strings.Replace(codexInterimForm, "hooks = true", "", 1)
	fp := fixtureHome(t, ccProxyForm, form, codexOrcaForm, authJSONAPIKey)
	if _, err := Apply(targetsOf(fp)); err != nil {
		t.Fatal(err)
	}
	got := mustReadStr(t, fp.codexCfg)
	if strings.Count(got, "[features]") != 1 {
		t.Fatalf("[features] 节应仍恰一个:\n%s", got)
	}
	if !strings.Contains(got, "[features]\nhooks = true\n") {
		t.Fatalf("旗标应插在节头下:\n%s", got)
	}
}

// ---- 异形拒绝（外科铁律：宁拒不改） ----

// env 缺 ANTHROPIC_BASE_URL 键 → 报错转人工（写入器只做已有键的定向改值）。
func TestApplyCCMissingKeyRefuses(t *testing.T) {
	form := `{
  "env": {
    "ANTHROPIC_AUTH_TOKEN": "PROXY_MANAGED"
  },
  "hooks": {}
}
`
	fp := fixtureHome(t, form, codexInterimForm, codexOrcaForm, authJSONAPIKey)
	before := snapshot(t, fp.cc)
	_, err := Apply(targetsOf(fp))
	if err == nil || !strings.Contains(err.Error(), "转人工") {
		t.Fatalf("键缺失应报错转人工: %v", err)
	}
	if got := mustReadStr(t, fp.cc); got != string(before[fp.cc]) {
		t.Fatal("拒绝时 CC 不得被写")
	}
}

// model_provider 指向不存在的表 → 报错转人工。
func TestApplyCodexDanglingProviderRefuses(t *testing.T) {
	form := strings.Replace(codexInterimForm, `model_provider = "custom"`, `model_provider = "ghost"`, 1)
	fp := fixtureHome(t, ccProxyForm, form, codexOrcaForm, authJSONAPIKey)
	_, err := Apply(targetsOf(fp))
	if err == nil || !strings.Contains(err.Error(), "转人工") {
		t.Fatalf("悬空 provider 表应报错转人工: %v", err)
	}
}

// DockBaseURL 未设置 → 拒绝盲写。
func TestApplyRequiresDockBaseURL(t *testing.T) {
	fp := interimFixture(t)
	tg := targetsOf(fp)
	tg.DockBaseURL = ""
	if _, err := Apply(tg); err == nil {
		t.Fatal("无渡口地址应拒绝")
	}
}
