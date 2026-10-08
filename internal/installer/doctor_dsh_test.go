// doctor_dsh_test.go — verify-dsh 票06：doctor 吸收 L0 常驻检查项验收
// （dsh_plugin_static 三态＋per-profile detail ＋ ferryman-gate-dsh.ps1 入
// 脚本清单 ＋ 版本黄灯「黄不告警」D10）。
//
// 协调注（票06 改票）：与并行链 dsh_poller_sentinel（dsh-host-guard 泳道，
// 宿主插件哨兵）互补不重复——本文件只钉安装面静态完整性与黄灯判据；本泳道
// 不新增任何 poll/活性检查项（原 dsh_poll_age 已撤销，见
// TestDoctorDshNoPollLivenessItems 防回归钉子）。

package installer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ferryman/internal/dshledger"
	"ferryman/internal/dshverify"
)

// okL0 全过假返回（三 profile × 4 项＋配置面 3 项＋版本项——形状照 RunL0 全绿
// 产出序；版本项 detail 文案前缀锚＝dshverify installVersionCheck 的
// "DSH 版本 %s（%s）"。version 空串＝版本项整体省略，与 DSHInstall 空串同形）。
func okL0(version string) []dshverify.CheckResult {
	rs := []dshverify.CheckResult{}
	for _, p := range dshverify.DefaultProfiles {
		for _, n := range []string{dshverify.ChkProfileJunction, dshverify.ChkProfileNodeModules,
			dshverify.ChkProfilePatchInsert, dshverify.ChkProfileManifest} {
			rs = append(rs, dshverify.CheckResult{Name: n, Profile: p, OK: true,
				Detail: n + " ok（" + p + "）"})
		}
	}
	rs = append(rs,
		dshverify.CheckResult{Name: dshverify.ChkHomePatchRoute, OK: true, Detail: "llm-deepseek 路由指向渡口"},
		dshverify.CheckResult{Name: dshverify.ChkHomeModelRoute, OK: true, Detail: "模型路由仍指 deepseek-official"},
		dshverify.CheckResult{Name: dshverify.ChkHomeEnvToken, OK: true, Detail: "令牌行在位"},
	)
	if version != "" {
		rs = append(rs, dshverify.CheckResult{Name: dshverify.ChkInstallVersion, OK: true,
			Detail: "DSH 版本 " + version + "（C:/install/version）"})
	}
	return rs
}

// dshCheckByName doctorResults 结果按名取 dsh_plugin_static 行（找不到＝测试失败）。
func dshCheckByName(t *testing.T, res []CheckResult) CheckResult {
	t.Helper()
	for _, r := range res {
		if r.Name == "dsh_plugin_static" {
			return r
		}
	}
	t.Fatalf("doctorResults 缺 dsh_plugin_static 行: %+v", res)
	return CheckResult{}
}

// TestDoctorDshPluginStaticStates 表驱动三态（验收①：插桩 L0 假返回，断言
// ok/warn/fail 与 per-profile detail）：
//   - fail：任一 L0 项 fail → StatusFail，detail 按 profile 列失败项
//     （profile 名＋项名＋指位 detail；配置面项不带 profile 前缀）；
//   - warn（黄）：全过＋版本不在流水 → StatusPass＋黄灯提示行（不告警不判失败）；
//   - ok：全过＋版本在流水 → StatusPass，detail 无黄灯提示。
func TestDoctorDshPluginStaticStates(t *testing.T) {
	deps, _ := greenDoctorDeps(t, func() map[string]any { return map[string]any{"health_alert": false} })

	// ① fail：web 的 insert 行缺失（profile 项）＋配置面路由 fail——两项都列。
	l0Fail := okL0("44.0.0")
	for i := range l0Fail {
		if l0Fail[i].Name == dshverify.ChkProfilePatchInsert && l0Fail[i].Profile == "web" {
			l0Fail[i].OK = false
			l0Fail[i].Detail = "cordis.patch.yml 未找到含 id: ferryman-dsh 的 insert 块: Z:/nope/web/cordis.patch.yml"
		}
		if l0Fail[i].Name == dshverify.ChkHomePatchRoute {
			l0Fail[i].OK = false
			l0Fail[i].Detail = "home cordis.patch.yml 不可读: Z:/nope/cordis.patch.yml"
		}
	}
	deps.DSHL0 = func() []dshverify.CheckResult { return l0Fail }
	r := dshCheckByName(t, doctorResults(deps))
	if r.Status != StatusFail {
		t.Fatalf("任一 L0 项 fail 应判 fail: %+v", r)
	}
	for _, tok := range []string{"2 项失败", "web/" + dshverify.ChkProfilePatchInsert,
		"cordis.patch.yml 未找到", dshverify.ChkHomePatchRoute, "home cordis.patch.yml 不可读"} {
		if !strings.Contains(r.Detail, tok) {
			t.Fatalf("fail detail 缺 %q（per-profile 列表）: %s", tok, r.Detail)
		}
	}

	// ② warn（黄）：全过＋版本不在流水（首跑无判定流水）→ pass＋黄灯提示，
	//    退出码不受影响（黄不告警——D10）。
	deps.DSHL0 = func() []dshverify.CheckResult { return okL0("44.0.0") }
	res := doctorResults(deps)
	r = dshCheckByName(t, res)
	if r.Status != StatusPass {
		t.Fatalf("全过应 pass（黄灯不判失败）: %+v", r)
	}
	if !strings.Contains(r.Detail, "黄灯") || !strings.Contains(r.Detail, "44.0.0") ||
		!strings.Contains(r.Detail, "verify-dsh") {
		t.Fatalf("黄灯提示缺版本与修法: %s", r.Detail)
	}
	var out strings.Builder
	deps.Out = &out
	if code := runDoctor(deps); code != 0 {
		t.Fatalf("黄灯不应影响退出码（D10 黄不推）, got %d:\n%s", code, out.String())
	}

	// ③ ok：版本在流水（真 dshledger 夹具落 green 行）→ pass 且无黄灯提示。
	dataDir := filepath.Join(deps.Home, "ferryman")
	vl, err := dshledger.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vl.Append("44.0.0", "0.9.0", "v0.9.8", dshledger.VerdictGreen, "anchorhash", ""); err != nil {
		t.Fatal(err)
	}
	r = dshCheckByName(t, doctorResults(deps))
	if r.Status != StatusPass {
		t.Fatalf("版本在流水应 pass: %+v", r)
	}
	if strings.Contains(r.Detail, "黄灯") {
		t.Fatalf("版本在流水不应出黄灯提示（在流水→无提示）: %s", r.Detail)
	}
}

// TestDoctorDshPluginStaticPassDetailPerProfile pass 面 per-profile 汇总：
// detail 按 profile 列各 profile 的过项数（三 profile 全点名）。
func TestDoctorDshPluginStaticPassDetailPerProfile(t *testing.T) {
	deps, _ := greenDoctorDeps(t, func() map[string]any { return map[string]any{"health_alert": false} })
	deps.DSHL0 = func() []dshverify.CheckResult { return okL0("") } // 无版本项（DSHInstall 未传形态）
	r := dshCheckByName(t, doctorResults(deps))
	if r.Status != StatusPass {
		t.Fatalf("全过应 pass: %+v", r)
	}
	for _, p := range dshverify.DefaultProfiles {
		if !strings.Contains(r.Detail, p) {
			t.Fatalf("pass detail 应按 profile 列（缺 %s）: %s", p, r.Detail)
		}
	}
	if strings.Contains(r.Detail, "黄灯") {
		t.Fatalf("版本项缺席＝版本未知，黄灯判据无从判定不应出提示: %s", r.Detail)
	}
}

// TestDoctorDshLedgerReadFailureDegrades 流水读取失败 → 降级"无法判定"
// （零副作用红线：doctor 只读流水；读失败不改灯色、不判失败）。
func TestDoctorDshLedgerReadFailureDegrades(t *testing.T) {
	deps, dataDir := greenDoctorDeps(t, func() map[string]any { return map[string]any{"health_alert": false} })
	deps.DSHL0 = func() []dshverify.CheckResult { return okL0("44.0.0") }
	// verdicts.jsonl 落成目录 → 打开/读取失败（非 NotExist）→ 无法判定
	if err := os.MkdirAll(filepath.Join(dataDir, "dshledger", "verdicts.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := dshCheckByName(t, doctorResults(deps))
	if r.Status != StatusPass {
		t.Fatalf("流水读取失败应降级不判失败: %+v", r)
	}
	if !strings.Contains(r.Detail, "无法判定") {
		t.Fatalf("流水读取失败应注明无法判定: %s", r.Detail)
	}
}

// TestDoctorDshPluginStaticNotCheckedWithoutAssembly 缝未装配 → not_checked
// 如实标注（autostart/watchdog 同款），不产红、退出码不受影响；且该项目标
// 装配时是 doctorResults 末位（连续块追加位——与并行链合并冲突面最小化）。
func TestDoctorDshPluginStaticNotCheckedWithoutAssembly(t *testing.T) {
	deps, _ := greenDoctorDeps(t, func() map[string]any { return map[string]any{"health_alert": false} })
	res := doctorResults(deps)
	r := dshCheckByName(t, res)
	if r.Status != StatusNotChecked {
		t.Fatalf("缝未装配应 not_checked（如实标注不伪造）: %+v", r)
	}
	if res[len(res)-1].Name != "dsh_plugin_static" {
		t.Fatalf("dsh_plugin_static 应居末位（连续块）: 末项=%s", res[len(res)-1].Name)
	}
	var out strings.Builder
	deps.Out = &out
	if code := runDoctor(deps); code != 0 {
		t.Fatalf("not_checked 不判失败，应退出 0:\n%s", out.String())
	}
}

// TestDoctorDshPluginStaticInJSON --json 出口含新项（票面验收③：--json 输出
// 含 dsh_plugin_static）。
func TestDoctorDshPluginStaticInJSON(t *testing.T) {
	deps, _ := greenDoctorDeps(t, func() map[string]any { return map[string]any{"health_alert": false} })
	deps.DSHL0 = func() []dshverify.CheckResult { return okL0("44.0.0") }
	b, _ := doctorJSON(deps, "dev")
	var rep DoctorJSONReport
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatalf("非法 JSON: %v\n%s", err, string(b))
	}
	found := false
	for _, c := range rep.Checks {
		if c.Name == "dsh_plugin_static" {
			found = true
		}
	}
	if !found {
		t.Fatalf("doctor --json checks 缺 dsh_plugin_static: %+v", rep.Checks)
	}
}

// TestDoctorDshGateScriptInScriptList 验收②：ferryman-gate-dsh.ps1 入
// doctorScriptNames 清单；缺文件时与其他脚本同款报 fail（"不存在"）且退出 1。
func TestDoctorDshGateScriptInScriptList(t *testing.T) {
	inList := false
	for _, n := range doctorScriptNames() {
		if n == "ferryman-gate-dsh.ps1" {
			inList = true
		}
	}
	if !inList {
		t.Fatalf("doctorScriptNames 缺 ferryman-gate-dsh.ps1: %v", doctorScriptNames())
	}
	deps, _ := greenDoctorDeps(t, func() map[string]any { return map[string]any{"health_alert": false} })
	if err := os.Remove(filepath.Join(deps.Repo, "hooks", "ferryman-gate-dsh.ps1")); err != nil {
		t.Fatal(err)
	}
	var row *CheckResult
	res := doctorResults(deps)
	for i := range res {
		if res[i].Name == "hook_script:ferryman-gate-dsh.ps1" {
			row = &res[i]
		}
	}
	if row == nil || row.Status != StatusFail || !strings.Contains(row.Detail, "不存在") {
		t.Fatalf("gate-dsh 脚本缺文件应报 fail（与其他脚本同款）: %+v", row)
	}
	var out strings.Builder
	deps.Out = &out
	if code := runDoctor(deps); code != 1 {
		t.Fatalf("脚本缺文件应退出 1, got %d:\n%s", code, out.String())
	}
}

// TestDoctorDshL0SeamAssembly 生产装配缝（dshL0Seam——CLI 与 agent 面同缝）：
// ~/.dsh 不在位＝未装 DSH → nil（not_checked 不产红）；[dock] 未配置 → nil
// （接管目标不可判，服务商四项同款先例）；两者齐备 → 非 nil 且调用返回 L0 行
// （近空根＝逐项 fail 行，恒非空——RunL0 纯读零副作用）。
func TestDoctorDshL0SeamAssembly(t *testing.T) {
	tmp := t.TempDir()
	if got := dshL0Seam(tmp, "127.0.0.1:15799"); got != nil {
		t.Fatalf("~/.dsh 不在位应返回 nil（未装 DSH），got non-nil seam")
	}
	dshRoot := filepath.Join(tmp, ".dsh")
	if err := os.MkdirAll(dshRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := dshL0Seam(tmp, ""); got != nil {
		t.Fatalf("[dock] 未配置应返回 nil（接管目标不可判），got non-nil seam")
	}
	seam := dshL0Seam(tmp, "127.0.0.1:15799")
	if seam == nil {
		t.Fatal("~/.dsh 在位且 dock 已配置应装配缝")
	}
	if rs := seam(); len(rs) == 0 {
		t.Fatalf("缝调用应返回 L0 行（近空根＝逐项 fail 行）: %+v", rs)
	}
}

// TestDoctorDshNoPollLivenessItems 撤销项防回归钉子（票06 改票）：本泳道不
// 新增任何 poll/活性检查项——挂载活性归并行链 dsh_poller_sentinel（互补不
// 重复），doctor 检查项名面不得出现 dsh_poll*。
func TestDoctorDshNoPollLivenessItems(t *testing.T) {
	deps, _ := greenDoctorDeps(t, func() map[string]any { return map[string]any{"health_alert": false} })
	deps.DSHL0 = func() []dshverify.CheckResult { return okL0("44.0.0") }
	for _, r := range doctorResults(deps) {
		if strings.Contains(r.Name, "dsh_poll") {
			t.Fatalf("不得新增 poll/活性检查项（票06 撤销项——归并行链 dsh_poller_sentinel）: %s", r.Name)
		}
	}
}
