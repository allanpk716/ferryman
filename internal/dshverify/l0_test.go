// l0_test.go — L0 静态检查器验收（票 02）。断言面：
//   - 完整布局夹具 → 全 ok（三 profile × 4 项 ＋ 生产配置面 3 项 ＋ 版本 1 项）；
//   - 删 insert 行 / 坏 exports（缺 "."）/ 断 junction / 路由漂移 / 缺令牌行 /
//     版本缺失 → 对应项 fail 且 detail 指明位置；
//   - junction 夹具走 mklink /J（非 Windows 跳过该夹具——验收标准钉死）；
//   - 生产配置面判据与 internal/provider dsh 分支写入形状一一对应
//     （home patch 夹具＝writer 产物同形；env 名/占位令牌 import provider 单源，
//     writer 改形状时本套测试连同 provider 自己的字面量钉测试一起报红）；
//   - 根未传入 → 单项 guard 行；根目录不存在 → 逐项 fail 不 panic；
//     可选参数（DSHInstall）空 → 对应项整体省略。
//
// 绝不写真机 ~/.dsh 与真安装树——全部路径在 t.TempDir 下；mklink 子进程
// Windows 分支 HideWindow（零闪窗铁律，2026-10-06 事故）。
package dshverify

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"ferryman/internal/provider"
)

// 夹具常量：渡口地址与 DSH 版本（version 文件内容；真安装树实锚 "44.0.0"
// 无尾换行，读侧 TrimSpace 兜两种形态）。
const (
	fixtureDockURL    = "http://127.0.0.1:15722"
	fixtureDSHVersion = "44.0.0"
)

// ---- 夹具助手 ----

// hideWindowSysProcAttr 零闪窗铁律：console 子进程（cmd /c mklink）一律
// HideWindow。该字段是 Windows SysProcAttr 专属——为让非 Windows 照常编译
// （junction 夹具之外的检查项在非 Windows 照跑不跳编译），反射按字段名补位
// （非 Windows 无该字段＝IsValid 假＝零操作）。分文件先例在 internal/daemon/
// hideconsole_windows_test.go；本票涉及路径只许单测试文件，故取反射单文件
// 形态，Windows 侧行为面一致（恒置位）。
func hideWindowSysProcAttr() *syscall.SysProcAttr {
	a := new(syscall.SysProcAttr)
	if f := reflect.ValueOf(a).Elem().FieldByName("HideWindow"); f.IsValid() {
		f.SetBool(true)
	}
	return a
}

// mkJunction 测试夹具造 junction（验收标准钉死 mklink /J；非 Windows 由
// 调用方测试的 GOOS 守卫跳过，走不到这里）。cmd /c 子进程按零闪窗铁律
// 补 HideWindow。
func mkJunction(t *testing.T, link, target string) {
	t.Helper()
	cmd := exec.Command("cmd", "/c", "mklink", "/J", link, target)
	cmd.SysProcAttr = hideWindowSysProcAttr()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mklink /J %s → %s 失败: %v: %s", link, target, err, out)
	}
}

// copyDir 递归拷夹具目录（junction 目标树与 node_modules 三拷贝的造法）。
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// fixtureStr 读包内夹具文件（go test 的 cwd＝包目录）。
func fixtureStr(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fixtureEnvContent .env 夹具内容——与 provider.dshEnvNewContent 同形
// （标记注释行＋令牌行）；令牌 env 名与占位值 import provider 单源，写入器
// 换名时此处编译红。标记行字面量照 provider.dshEnvMarkerLine（未导出，注释
// 面对齐）。
func fixtureEnvContent() string {
	return "# ferryman-takeover —— 以下令牌行由 Ferryman provider apply 管理（--restore 剥离）\n" +
		provider.DSHTokenEnv + "=" + provider.PlaceholderToken + "\n"
}

// rewriteFile 变异夹具文件（读→改→写）。
func rewriteFile(t *testing.T, path string, mutate func(string) string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(mutate(string(raw))), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newFullFixture 完整生产布局夹具（全 ok 基线）。布局：
//
//	<tmp>/plugin-src/                  junction 指向的插件源树（fixtures/plugin）
//	<tmp>/install/version              DSH 安装树版本文件
//	<tmp>/dsh-home/cordis.patch.yml    home patch（writer 真相形状，__DOCK_BASE_URL__ 已替换）
//	<tmp>/dsh-home/.env                渡口令牌行
//	<tmp>/dsh-home/profiles/<p>/       三 profile：junction＋node_modules 拷贝＋profile patch
//
// junction 夹具仅 Windows——非 Windows 在此跳过（验收标准：mklink /J 夹具），
// 非 junction 检查项的行为面由 TestL0RootDirAbsentFailsPerItem 等全平台用例覆盖。
func newFullFixture(t *testing.T) (dshRoot, installDir, pluginSrc string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("junction 夹具仅 Windows（mklink /J）；非 Windows 行为由无 junction 用例覆盖")
	}
	root := t.TempDir()
	dshRoot = filepath.Join(root, "dsh-home")
	installDir = filepath.Join(root, "install")
	pluginSrc = filepath.Join(root, "plugin-src")
	copyDir(t, filepath.Join("fixtures", "plugin"), pluginSrc)
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installDir, "version"),
		[]byte(fixtureDSHVersion), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range DefaultProfiles {
		profDir := filepath.Join(dshRoot, "profiles", p)
		nmDir := filepath.Join(profDir, "node_modules", "ferryman-dsh")
		if err := os.MkdirAll(filepath.Dir(nmDir), 0o755); err != nil {
			t.Fatal(err)
		}
		mkJunction(t, filepath.Join(profDir, "ferryman-dsh"), pluginSrc)
		copyDir(t, filepath.Join("fixtures", "plugin"), nmDir)
		if err := os.WriteFile(filepath.Join(profDir, "cordis.patch.yml"),
			[]byte(fixtureStr(t, "profile-patch.yml")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	homePatch := strings.ReplaceAll(fixtureStr(t, "home-patch.yml"),
		"__DOCK_BASE_URL__", fixtureDockURL)
	if err := os.WriteFile(filepath.Join(dshRoot, "cordis.patch.yml"),
		[]byte(homePatch), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dshRoot, ".env"),
		[]byte(fixtureEnvContent()), 0o644); err != nil {
		t.Fatal(err)
	}
	return dshRoot, installDir, pluginSrc
}

// findResult 按名＋profile 取唯一检查项。项集完整性本身是契约：命中 0 或
// 多于 1 都 Fatal（多带全集方便人读）。
func findResult(t *testing.T, rs []CheckResult, name, profile string) CheckResult {
	t.Helper()
	var hits []CheckResult
	for _, r := range rs {
		if r.Name == name && (profile == "" || r.Profile == profile) {
			hits = append(hits, r)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("检查项 name=%s profile=%q 命中 %d 次 want 1；全集: %+v", name, profile, len(hits), rs)
	}
	return hits[0]
}

// wantFail 断言检查项 fail 且 detail 指明位置（含全部 wantSub 串）。
func wantFail(t *testing.T, r CheckResult, where string, wantSub ...string) {
	t.Helper()
	if r.OK {
		t.Fatalf("%s: 期望 fail 实得 ok: %+v", where, r)
	}
	for _, s := range wantSub {
		if !strings.Contains(r.Detail, s) {
			t.Fatalf("%s: detail 缺 %q: %q", where, s, r.Detail)
		}
	}
}

// ---- 用例 ----

// TestL0FullLayoutAllOK 完整布局 → 16 项全 ok；缺省三 profile 覆盖；
// junction detail 指向插件源树；版本 detail 带版本串。
func TestL0FullLayoutAllOK(t *testing.T) {
	dshRoot, installDir, _ := newFullFixture(t)
	rs := RunL0(Input{DSHRoot: dshRoot, DSHInstall: installDir, DockBaseURL: fixtureDockURL})
	if len(rs) != 16 {
		t.Fatalf("检查项数=%d want 16（3 profile×4＋home 3＋版本 1）: %+v", len(rs), rs)
	}
	for _, r := range rs {
		if !r.OK {
			t.Fatalf("期望全 ok，%s(profile=%s) fail: %s", r.Name, r.Profile, r.Detail)
		}
	}
	seen := map[string]bool{}
	for _, r := range rs {
		if r.Profile != "" {
			seen[r.Profile] = true
		}
	}
	if len(seen) != len(DefaultProfiles) {
		t.Fatalf("覆盖 profile=%v want %v", seen, DefaultProfiles)
	}
	// junction detail：指明链接本体路径（指向串原样透传 Readlink 的产物，
	// mklink 存的 substitute name 可能带 \??\ 前缀/短名展开——路径形态不钉，
	// 可解析性已由 ok 位钉住）。
	if jc := findResult(t, rs, ChkProfileJunction, "web"); !strings.Contains(jc.Detail,
		filepath.Join(dshRoot, "profiles", "web", "ferryman-dsh")) {
		t.Fatalf("junction detail 未指明链接路径: %q", jc.Detail)
	}
	if v := findResult(t, rs, ChkInstallVersion, ""); !strings.Contains(v.Detail, fixtureDSHVersion) {
		t.Fatalf("版本 detail 缺版本串: %q", v.Detail)
	}
}

// TestL0ProfileInsertMissing 删 web 的 insert 块 → 该项 fail 且 detail 指明
// 文件；同 profile 其余项与别 profile 的 insert 项不连坐。
func TestL0ProfileInsertMissing(t *testing.T) {
	dshRoot, _, _ := newFullFixture(t)
	webPatch := filepath.Join(dshRoot, "profiles", "web", "cordis.patch.yml")
	rewriteFile(t, webPatch, func(string) string { return "# insert 块被删（变异夹具）\n" })
	rs := RunL0(Input{DSHRoot: dshRoot, Profiles: DefaultProfiles, DockBaseURL: fixtureDockURL})
	wantFail(t, findResult(t, rs, ChkProfilePatchInsert, "web"), "web insert",
		"cordis.patch.yml", webPatch)
	if r := findResult(t, rs, ChkProfilePatchInsert, "desktop"); !r.OK {
		t.Fatalf("desktop insert 不应连坐: %+v", r)
	}
	if r := findResult(t, rs, ChkProfileJunction, "web"); !r.OK {
		t.Fatalf("web junction 不应连坐: %+v", r)
	}
	if r := findResult(t, rs, ChkProfileManifest, "web"); !r.OK {
		t.Fatalf("web manifest 不应连坐: %+v", r)
	}
}

// TestL0ManifestBadExports web 拷贝的 exports 删 "." 键（历史失活坑
// commit 5b3b476 的夹具复刻）→ manifest 项 fail 且 detail 点名 exports；
// 其他 profile 不连坐。
func TestL0ManifestBadExports(t *testing.T) {
	dshRoot, _, _ := newFullFixture(t)
	pkgPath := filepath.Join(dshRoot, "profiles", "web", "node_modules",
		"ferryman-dsh", "package.json")
	rewriteFile(t, pkgPath, func(s string) string {
		var doc map[string]any
		if err := json.Unmarshal([]byte(s), &doc); err != nil {
			t.Fatal(err)
		}
		exports, ok := doc["exports"].(map[string]any)
		if !ok {
			t.Fatalf("夹具缺 exports: %s", s)
		}
		delete(exports, ".")
		b, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	})
	rs := RunL0(Input{DSHRoot: dshRoot, Profiles: DefaultProfiles, DockBaseURL: fixtureDockURL})
	wantFail(t, findResult(t, rs, ChkProfileManifest, "web"), "web manifest",
		"exports", `"."`)
	if r := findResult(t, rs, ChkProfileManifest, "headless"); !r.OK {
		t.Fatalf("headless manifest 不应连坐: %+v", r)
	}
}

// TestL0ManifestMissingMainEntry main 入口文件被删 → manifest 项 fail 且
// detail 带入口文件路径。
func TestL0ManifestMissingMainEntry(t *testing.T) {
	dshRoot, _, _ := newFullFixture(t)
	entry := filepath.Join(dshRoot, "profiles", "desktop", "node_modules",
		"ferryman-dsh", "index.mjs")
	if err := os.Remove(entry); err != nil {
		t.Fatal(err)
	}
	rs := RunL0(Input{DSHRoot: dshRoot, Profiles: DefaultProfiles, DockBaseURL: fixtureDockURL})
	wantFail(t, findResult(t, rs, ChkProfileManifest, "desktop"), "desktop main",
		entry)
}

// TestL0JunctionDangling 断 junction（指向不存在的目标）→ 该项 fail 且
// detail 带目标路径与"不可解析"；其他 profile 不连坐。
func TestL0JunctionDangling(t *testing.T) {
	dshRoot, _, pluginSrc := newFullFixture(t)
	deskJunc := filepath.Join(dshRoot, "profiles", "desktop", "ferryman-dsh")
	if err := os.Remove(deskJunc); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(filepath.Dir(pluginSrc), "gone-target")
	mkJunction(t, deskJunc, dangling) // mklink /J 允许悬空目标
	rs := RunL0(Input{DSHRoot: dshRoot, Profiles: DefaultProfiles, DockBaseURL: fixtureDockURL})
	// 指向串断言用基名：mklink 存的 substitute name 路径形态（\??\ 前缀/
	// 短名展开）不受控，基名在一切形态下都在。
	wantFail(t, findResult(t, rs, ChkProfileJunction, "desktop"), "desktop junction",
		"不可解析", "gone-target")
	if r := findResult(t, rs, ChkProfileJunction, "web"); !r.OK {
		t.Fatalf("web junction 不应连坐: %+v", r)
	}
}

// TestL0JunctionMissing junction 整个不在 → fail detail 指明路径
// （desktop 生产实锚正是此形态）。
func TestL0JunctionMissing(t *testing.T) {
	dshRoot, _, _ := newFullFixture(t)
	deskJunc := filepath.Join(dshRoot, "profiles", "desktop", "ferryman-dsh")
	if err := os.Remove(deskJunc); err != nil {
		t.Fatal(err)
	}
	rs := RunL0(Input{DSHRoot: dshRoot, Profiles: DefaultProfiles, DockBaseURL: fixtureDockURL})
	wantFail(t, findResult(t, rs, ChkProfileJunction, "desktop"), "desktop junction",
		"不存在", deskJunc)
}

// TestL0JunctionReplacedByPlainDir junction 被实目录顶替（安装走形的另一种
// 姿势）→ fail：存在但不是链接。
func TestL0JunctionReplacedByPlainDir(t *testing.T) {
	dshRoot, _, _ := newFullFixture(t)
	deskJunc := filepath.Join(dshRoot, "profiles", "desktop", "ferryman-dsh")
	if err := os.Remove(deskJunc); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(deskJunc, 0o755); err != nil {
		t.Fatal(err)
	}
	rs := RunL0(Input{DSHRoot: dshRoot, Profiles: DefaultProfiles, DockBaseURL: fixtureDockURL})
	wantFail(t, findResult(t, rs, ChkProfileJunction, "desktop"), "desktop junction",
		"不是链接", deskJunc)
}

// TestL0HomeRouteDrift 生产配置面·渡口路由：baseURL 漂移 / apiKeyEnv 漂移 /
// 渡口地址未传入 → home_patch_route fail 且 detail 指明文件与差异；模型路由
// 项不受连坐。
func TestL0HomeRouteDrift(t *testing.T) {
	t.Run("baseURL漂移", func(t *testing.T) {
		dshRoot, _, _ := newFullFixture(t)
		rewriteFile(t, filepath.Join(dshRoot, "cordis.patch.yml"), func(s string) string {
			return strings.Replace(s, "baseURL: "+fixtureDockURL,
				"baseURL: http://127.0.0.1:9999", 1)
		})
		rs := RunL0(Input{DSHRoot: dshRoot, DockBaseURL: fixtureDockURL})
		wantFail(t, findResult(t, rs, ChkHomePatchRoute, ""), "route",
			"baseURL", "cordis.patch.yml")
		if r := findResult(t, rs, ChkHomeModelRoute, ""); !r.OK {
			t.Fatalf("模型路由不应连坐: %+v", r)
		}
	})
	t.Run("apiKeyEnv漂移", func(t *testing.T) {
		dshRoot, _, _ := newFullFixture(t)
		rewriteFile(t, filepath.Join(dshRoot, "cordis.patch.yml"), func(s string) string {
			return strings.Replace(s, "apiKeyEnv: "+provider.DSHTokenEnv,
				"apiKeyEnv: OTHER_TOKEN", 1)
		})
		rs := RunL0(Input{DSHRoot: dshRoot, DockBaseURL: fixtureDockURL})
		wantFail(t, findResult(t, rs, ChkHomePatchRoute, ""), "route",
			provider.DSHTokenEnv)
	})
	t.Run("渡口地址未传入", func(t *testing.T) {
		dshRoot, _, _ := newFullFixture(t)
		rs := RunL0(Input{DSHRoot: dshRoot})
		wantFail(t, findResult(t, rs, ChkHomePatchRoute, ""), "route", "未传入")
	})
}

// TestL0HomeModelRouteDrift 模型路由 provider 改指别家 → home_patch_model_route
// fail；渡口路由项不连坐。
func TestL0HomeModelRouteDrift(t *testing.T) {
	dshRoot, _, _ := newFullFixture(t)
	rewriteFile(t, filepath.Join(dshRoot, "cordis.patch.yml"), func(s string) string {
		return strings.Replace(s, "provider: deepseek-official",
			"provider: zai-coding-cn", 1)
	})
	rs := RunL0(Input{DSHRoot: dshRoot, DockBaseURL: fixtureDockURL})
	wantFail(t, findResult(t, rs, ChkHomeModelRoute, ""), "model route", "provider")
	if r := findResult(t, rs, ChkHomePatchRoute, ""); !r.OK {
		t.Fatalf("渡口路由不应连坐: %+v", r)
	}
}

// TestL0HomeEnvTokenMissing .env 删令牌行 → home_env_token fail 且 detail
// 指明 .env 路径（适配器 MISSING_CREDENTIAL 前置会炸的那一环）。
func TestL0HomeEnvTokenMissing(t *testing.T) {
	dshRoot, _, _ := newFullFixture(t)
	envPath := filepath.Join(dshRoot, ".env")
	rewriteFile(t, envPath, func(string) string { return "# 令牌行被删（变异夹具）\n" })
	rs := RunL0(Input{DSHRoot: dshRoot, DockBaseURL: fixtureDockURL})
	wantFail(t, findResult(t, rs, ChkHomeEnvToken, ""), "env token",
		provider.DSHTokenEnv, envPath)
	if r := findResult(t, rs, ChkHomePatchRoute, ""); !r.OK {
		t.Fatalf("渡口路由不应连坐: %+v", r)
	}
}

// TestL0InstallVersion 版本面：version 文件缺失 → fail 且 detail 指明路径；
// DSHInstall 未传入 → 该项整体省略（参数缺席≠检查失败，如实不 emit）。
func TestL0InstallVersion(t *testing.T) {
	t.Run("version文件缺失", func(t *testing.T) {
		dshRoot, installDir, _ := newFullFixture(t)
		verPath := filepath.Join(installDir, "version")
		if err := os.Remove(verPath); err != nil {
			t.Fatal(err)
		}
		rs := RunL0(Input{DSHRoot: dshRoot, DSHInstall: installDir, DockBaseURL: fixtureDockURL})
		wantFail(t, findResult(t, rs, ChkInstallVersion, ""), "version", verPath)
	})
	t.Run("DSHInstall未传入则省略", func(t *testing.T) {
		dshRoot, _, _ := newFullFixture(t)
		rs := RunL0(Input{DSHRoot: dshRoot, DockBaseURL: fixtureDockURL})
		for _, r := range rs {
			if r.Name == ChkInstallVersion {
				t.Fatalf("DSHInstall 空串不应 emit 版本项: %+v", rs)
			}
		}
		if n := len(rs); n != 15 {
			t.Fatalf("检查项数=%d want 15（无版本项）: %+v", n, rs)
		}
	})
}

// TestL0RootNotPassedGuard 根未传入 → 单项 guard 行（不读环境变量猜根的
// 纪律面：调用方缺陷在这里显形，不静默空转）。
func TestL0RootNotPassedGuard(t *testing.T) {
	rs := RunL0(Input{})
	if len(rs) != 1 {
		t.Fatalf("检查项数=%d want 1（guard 行）: %+v", len(rs), rs)
	}
	if rs[0].Name != ChkRoot || rs[0].OK {
		t.Fatalf("guard 行形状不符: %+v", rs[0])
	}
}

// TestL0RootDirAbsentFailsPerItem 根目录不存在 → 逐项 fail（15 项：无版本，
// DSHInstall 空）且 detail 各带路径，不 panic 不吞项。全平台可跑（无 junction
// 夹具依赖）。
func TestL0RootDirAbsentFailsPerItem(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "no-such-dsh")
	rs := RunL0(Input{DSHRoot: absent, DockBaseURL: fixtureDockURL})
	if len(rs) != 15 {
		t.Fatalf("检查项数=%d want 15: %+v", len(rs), rs)
	}
	for _, r := range rs {
		if r.OK {
			t.Fatalf("根不存在时不应有 ok 项: %+v", r)
		}
		if !strings.Contains(r.Detail, absent) {
			t.Fatalf("%s detail 未指明位置: %q", r.Name, r.Detail)
		}
	}
}

// TestL0EmptyHomePatchFiles 空根目录（目录在位但零文件）→ 生产配置面三项
// 逐一指名缺什么；junction/node_modules/patch/manifest 同理。全平台可跑。
func TestL0EmptyHomePatchFiles(t *testing.T) {
	rs := RunL0(Input{DSHRoot: t.TempDir(), DockBaseURL: fixtureDockURL})
	if len(rs) != 15 {
		t.Fatalf("检查项数=%d want 15: %+v", len(rs), rs)
	}
	for _, name := range []string{ChkHomePatchRoute, ChkHomeModelRoute, ChkHomeEnvToken} {
		wantFail(t, findResult(t, rs, name, ""), name, "不可读")
	}
}
