// dsh_test.go — dsh（DeepSeek Harness）接管分支验收（2026-10-02）。断言面：
//   - 新建：home patch + .env 从无到有，内容确定性（字面量钉）；
//   - 幂等：二跑零差异、零新增备份；
//   - 他人文件（无标记）拒绝：全案零写盘（cc/codex/dsh 全不动）；
//   - 带标记旧版本重铸：备份成组、目标内容到位；
//   - .env 补行：他人行逐字节保留；
//   - 目录不在位＝skip；DSHHome 空＝零行（旧调用零变化）；
//   - Restore：备份在组→还原；无备份＋带标记→删除/剥离还原。
//
// 绝不写真机 ~/.dsh——全部路径在 t.TempDir 下。
package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dshTargetsOf 在既有夹具上叠加 DSHHome（复用 writer_test 的三份在位配置）。
func dshTargetsOf(fp fixturePaths) Targets {
	t := targetsOf(fp)
	t.DSHHome = filepath.Join(fp.home, ".dsh")
	return t
}

// mkDSHHome 建 dsh 家目录（返回路径）。
func mkDSHHome(t *testing.T, fp fixturePaths) string {
	t.Helper()
	p := filepath.Join(fp.home, ".dsh")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func dshPatchOf(fp fixturePaths) string { return filepath.Join(fp.home, ".dsh", dshHomePatchName) }
func dshEnvOf(fp fixturePaths) string   { return filepath.Join(fp.home, ".dsh", dshEnvName) }

func findRow(t *testing.T, rep ApplyReport, name string) TargetReport {
	t.Helper()
	for _, r := range rep.Targets {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("报告中缺 %s 行: %+v", name, rep.Targets)
	return TargetReport{}
}

func countBackups(t *testing.T, dir, base string) int {
	t.Helper()
	ms, err := filepath.Glob(filepath.Join(dir, base+backupMarker+"*"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range ms {
		s := strings.TrimPrefix(filepath.Base(m), base+backupMarker)
		if backupStampRe.MatchString(s) {
			n++
		}
	}
	return n
}

// TestApplyDSHCreatesFiles 新建：首跑落 home patch + .env，内容钉字面量；
// cc/codex/orca 行为不因 dsh 分支改变（interim 夹具照常改写）。
func TestApplyDSHCreatesFiles(t *testing.T) {
	fp := interimFixture(t)
	mkDSHHome(t, fp)
	rep, err := Apply(dshTargetsOf(fp))
	if err != nil {
		t.Fatal(err)
	}
	pr := findRow(t, rep, targetDSHPatch)
	if pr.Action != ActionWritten {
		t.Fatalf("dsh-patch 动作=%s want written", pr.Action)
	}
	er := findRow(t, rep, targetDSHEnv)
	if er.Action != ActionWritten {
		t.Fatalf("dsh-env 动作=%s want written", er.Action)
	}
	if got := mustReadStr(t, dshPatchOf(fp)); got != dshHomePatchYAML(dockBase) {
		t.Fatalf("home patch 内容漂移:\n%q\nwant\n%q", got, dshHomePatchYAML(dockBase))
	}
	if got := mustReadStr(t, dshEnvOf(fp)); got != dshEnvNewContent() {
		t.Fatalf(".env 内容漂移: %q", got)
	}
	// 新建文件无备份可落（Restore 走删除还原）。
	if pr.Backup != "" || er.Backup != "" {
		t.Fatalf("新建文件不应带备份: %+v %+v", pr, er)
	}
	// 内容要素抽查：路由名/协议/baseURL/模型别名/默认模型。
	for _, want := range []string{
		"ferryman-dock:", "api: anthropic-messages", "baseURL: " + dockBase,
		"- id: claude-opus-5", "- id: claude-sonnet-5",
		"provider: ferryman-dock", "model: claude-opus-5",
	} {
		if !strings.Contains(mustReadStr(t, dshPatchOf(fp)), want) {
			t.Fatalf("home patch 缺要素 %q", want)
		}
	}
}

// TestApplyDSHIdempotent 二跑：全目标零差异、dsh 侧零新增备份。
func TestApplyDSHIdempotent(t *testing.T) {
	fp := interimFixture(t)
	mkDSHHome(t, fp)
	if _, err := Apply(dshTargetsOf(fp)); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, dshPatchOf(fp), dshEnvOf(fp))
	rep, err := Apply(dshTargetsOf(fp))
	if err != nil {
		t.Fatal(err)
	}
	after := snapshot(t, dshPatchOf(fp), dshEnvOf(fp))
	if string(before[dshPatchOf(fp)]) != string(after[dshPatchOf(fp)]) ||
		string(before[dshEnvOf(fp)]) != string(after[dshEnvOf(fp)]) {
		t.Fatal("二跑改动了 dsh 文件（幂等破坏）")
	}
	for _, name := range []string{targetDSHPatch, targetDSHEnv} {
		if r := findRow(t, rep, name); r.Action != ActionUnchanged {
			t.Fatalf("%s 二跑动作=%s want unchanged", name, r.Action)
		}
	}
	if n := countBackups(t, filepath.Dir(dshPatchOf(fp)), dshHomePatchName); n != 0 {
		t.Fatalf("dsh 侧不应有备份（新建文件二跑仍零备份）: %d", n)
	}
}

// TestApplyDSHForeignPatchRejected 他人文件（无标记）：全案拒绝，零写盘。
func TestApplyDSHForeignPatchRejected(t *testing.T) {
	fp := interimFixture(t)
	mkDSHHome(t, fp)
	foreign := "# 用户自己的补丁层\n- id: ui-chat\n  name: '@deepseek-ai/dsh-client-ui-chat'\n"
	writeFixture(t, dshPatchOf(fp), foreign)
	writeFixture(t, dshEnvOf(fp), "ZAI_CODING_CN_API_KEY=sk-user\n")
	before := snapshot(t, fp.cc, fp.codexCfg, fp.orcaCfg, dshPatchOf(fp), dshEnvOf(fp))
	_, err := Apply(dshTargetsOf(fp))
	if err == nil || !strings.Contains(err.Error(), "他人文件") {
		t.Fatalf("want 他人文件拒绝错误, got %v", err)
	}
	after := snapshot(t, fp.cc, fp.codexCfg, fp.orcaCfg, dshPatchOf(fp), dshEnvOf(fp))
	for p, b := range before {
		if string(after[p]) != string(b) {
			t.Fatalf("拒绝路径仍有写盘: %s", p)
		}
	}
	for _, dir := range []string{filepath.Dir(fp.cc), filepath.Dir(fp.codexCfg), filepath.Dir(dshPatchOf(fp))} {
		if n := countBackupsAny(t, dir); n != 0 {
			t.Fatalf("拒绝路径不应有备份: %s 有 %d", dir, n)
		}
	}
}

// countBackupsAny 目录下任意 bak-ferryman 备份计数（不挑 base）。
func countBackupsAny(t *testing.T, dir string) int {
	t.Helper()
	ms, err := filepath.Glob(filepath.Join(dir, "*"+backupMarker+"*"))
	if err != nil {
		t.Fatal(err)
	}
	return len(ms)
}

// TestApplyDSHMarkerRewrite 带标记旧版本（dock 地址变更）：整体重铸＋备份。
func TestApplyDSHMarkerRewrite(t *testing.T) {
	fp := interimFixture(t)
	mkDSHHome(t, fp)
	// 旧地址版本（带标记——apply 生成的历史形态）。
	writeFixture(t, dshPatchOf(fp), dshHomePatchYAML("http://127.0.0.1:15723"))
	writeFixture(t, dshEnvOf(fp), dshEnvNewContent())
	rep, err := Apply(dshTargetsOf(fp))
	if err != nil {
		t.Fatal(err)
	}
	if r := findRow(t, rep, targetDSHPatch); r.Action != ActionWritten || r.Backup == "" {
		t.Fatalf("旧版本应重铸＋备份: %+v", r)
	}
	if got := mustReadStr(t, dshPatchOf(fp)); got != dshHomePatchYAML(dockBase) {
		t.Fatal("重铸后内容未到位")
	}
	if old := mustReadStr(t, findRow(t, rep, targetDSHPatch).Backup); old != dshHomePatchYAML("http://127.0.0.1:15723") {
		t.Fatal("备份不是接管前内容")
	}
	// .env 已含令牌行 → 动作零写入（备份可随组成组——组语义：有任一改动，
	// 在位文件同戳全备）。
	if r := findRow(t, rep, targetDSHEnv); r.Action != ActionUnchanged {
		t.Fatalf(".env 应零写入: %+v", r)
	}
}

// TestApplyDSHEnvAppendPreservesUserLines .env 补行：他人行逐字节保留。
func TestApplyDSHEnvAppendPreservesUserLines(t *testing.T) {
	fp := interimFixture(t)
	mkDSHHome(t, fp)
	userEnv := "ZAI_CODING_CN_API_KEY=sk-user\nHTTPS_PROXY=http://127.0.0.1:7890\n"
	writeFixture(t, dshEnvOf(fp), userEnv)
	rep, err := Apply(dshTargetsOf(fp))
	if err != nil {
		t.Fatal(err)
	}
	if r := findRow(t, rep, targetDSHEnv); r.Action != ActionWritten {
		t.Fatalf("want written: %+v", r)
	}
	got := mustReadStr(t, dshEnvOf(fp))
	if !strings.HasPrefix(got, userEnv) {
		t.Fatalf("他人行被改动: %q", got)
	}
	if !dshEnvHasToken(got) {
		t.Fatal("令牌行未补上")
	}
}

// TestApplyDSHDirAbsentSkips 目录不在位：两行 skip、不代建、其余目标照常。
func TestApplyDSHDirAbsentSkips(t *testing.T) {
	fp := interimFixture(t) // 不建 .dsh 目录
	rep, err := Apply(dshTargetsOf(fp))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{targetDSHPatch, targetDSHEnv} {
		if r := findRow(t, rep, name); r.Action != ActionSkipped {
			t.Fatalf("%s 动作=%s want skipped", name, r.Action)
		}
	}
	if _, err := os.Stat(filepath.Join(fp.home, ".dsh")); !os.IsNotExist(err) {
		t.Fatal("skip 路径不应代建目录")
	}
	// codex 照常接管（interim 夹具 cc 本就指向渡口＝unchanged；15721→dock 的
	// 是 codex）——dsh 缺席不拖累主链路。
	if r := findRow(t, rep, targetCodex); r.Action != ActionWritten {
		t.Fatalf("codex 应照常写入: %+v", r)
	}
}

// TestApplyDSHEmptyFieldNoRows DSHHome 空＝零 dsh 行（旧调用零变化）。
func TestApplyDSHEmptyFieldNoRows(t *testing.T) {
	fp := interimFixture(t)
	rep, err := Apply(targetsOf(fp)) // DSHHome 空
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Targets {
		if strings.HasPrefix(r.Name, "dsh-") {
			t.Fatalf("DSHHome 空不应有 dsh 行: %+v", r)
		}
	}
}

// TestRestoreDSHCreatedDeleted 无备份＋带标记＝新建文件 → 删除还原；
// cc/codex 同组照常还原。
func TestRestoreDSHCreatedDeleted(t *testing.T) {
	fp := interimFixture(t)
	mkDSHHome(t, fp)
	if _, err := Apply(dshTargetsOf(fp)); err != nil {
		t.Fatal(err)
	}
	rep, err := Restore(dshTargetsOf(fp))
	if err != nil {
		t.Fatal(err)
	}
	if r := findRow(t, rep, targetDSHPatch); r.Action != ActionRestored {
		t.Fatalf("dsh-patch want restored(删除), got %+v", r)
	}
	if _, err := os.Stat(dshPatchOf(fp)); !os.IsNotExist(err) {
		t.Fatal("新建的 home patch 应被删除还原")
	}
	if _, err := os.Stat(dshEnvOf(fp)); !os.IsNotExist(err) {
		t.Fatal("新建的 .env 应被删除还原")
	}
	if r := findRow(t, rep, targetCC); r.Action != ActionRestored {
		t.Fatalf("cc 应同组还原: %+v", r)
	}
}

// TestRestoreDSHEnvFromBackup .env 接管前有用户行（备份在组）→ 还原用户行；
// 剥离路径（无备份＋带标记＋含用户行）单独验。
func TestRestoreDSHEnvFromBackup(t *testing.T) {
	fp := interimFixture(t)
	mkDSHHome(t, fp)
	userEnv := "ZAI_CODING_CN_API_KEY=sk-user\n"
	writeFixture(t, dshEnvOf(fp), userEnv)
	if _, err := Apply(dshTargetsOf(fp)); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(dshTargetsOf(fp)); err != nil {
		t.Fatal(err)
	}
	if got := mustReadStr(t, dshEnvOf(fp)); got != userEnv {
		t.Fatalf(".env 还原漂移: %q", got)
	}
}

// TestDSHEnvStripTokenSurgical 剥离只动接管行：用户行与序保留；纯接管文件剥空。
func TestDSHEnvStripTokenSurgical(t *testing.T) {
	user := "A=1\nB=2\n"
	got := dshEnvStripToken(user + "\n" + dshEnvMarkerLine() + DSHTokenEnv + "=" + PlaceholderToken + "\n")
	if got != user {
		t.Fatalf("剥离不净/误伤: %q", got)
	}
	if got := dshEnvStripToken(dshEnvNewContent()); strings.TrimSpace(got) != "" {
		t.Fatalf("纯接管文件应剥空: %q", got)
	}
	// 注释里的键名不算令牌行。
	if dshEnvHasToken("# " + DSHTokenEnv + "=x\n") {
		t.Fatal("注释行误判为令牌行")
	}
}

// TestDSHMarkerFirstLinesOnly 标记只认首 4 行（正文提及不算我们的文件）。
func TestDSHMarkerFirstLinesOnly(t *testing.T) {
	if !hasDSHMarker("# 无关\n# ferryman-takeover\n") {
		t.Fatal("首 4 行内应命中")
	}
	if hasDSHMarker("# 无关\n\n\n\n# ferryman-takeover\n") {
		t.Fatal("第 5 行不应命中")
	}
	if hasDSHMarker("# 用户文件\n# 第二行\n# 第三行\n# 第四行\n提到 ferryman-takeover 链接") {
		t.Fatal("首 4 行之外的提及不应命中")
	}
}
