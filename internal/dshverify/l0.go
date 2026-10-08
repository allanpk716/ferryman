// Package dshverify DSH（DeepSeek Harness）插件验证（verify-dsh，spec 见
// .scratch/verify-dsh/spec.md）的 L0 静态检查器。
//
// L0＝不起进程、不发网络请求，只对调用方显式传入的根做文件系统静态检查：
// 三 profile 的插件安装三件套（junction／node_modules 拷贝／cordis.patch.yml
// insert 行）、插件 manifest 形状、生产配置面的渡口路由、DSH 安装树版本可读。
// 三条纪律（spec Implementation Decisions「L0 静态」节＋决策 D2/D6/D8）：
//
//   - 生产侧求值：DSH 根（~/.dsh）、DSH 安装树、渡口地址全是参数——本包
//     不读环境变量猜根；CLI（ferryman verify-dsh）与 doctor 各自装配传入；
//   - 写入器是真相源：生产配置面验的形状＝internal/provider dsh 分支
//     （dshHomePatchYAML／dshEnvNewContent）写什么就验什么；env 名与占位
//     令牌直接 import provider 单源，字面量（llm-deepseek／deepseek-official/
//     ferryman-takeover）与 provider 各自钉测试，漂移双双报红；
//   - 不判灯色：本包只出 {name, profile?, ok, detail} 结构化事实，绿/黄/红
//     与推送归 CLI/doctor 层。
package dshverify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"ferryman/internal/provider"
)

// DefaultProfiles 三 profile 缺省清单（生产在位三件；调用方未传 Profiles
// 时兜底，可配列表的缺省值）。
var DefaultProfiles = []string{"desktop", "web", "headless"}

// 检查项名（稳定标识：灯色判定与 doctor 吸收按名取项，改名＝破消费契约）。
const (
	// ChkRoot 根未传入的 guard 行（本包不读环境变量猜根——调用方缺陷在此显形）。
	ChkRoot = "dsh_root"
	// ChkProfileJunction profiles/<p>/ferryman-dsh junction 存在且指向可解析。
	ChkProfileJunction = "profile_junction"
	// ChkProfileNodeModules profiles/<p>/node_modules/ferryman-dsh 拷贝在位。
	ChkProfileNodeModules = "profile_node_modules"
	// ChkProfilePatchInsert profiles/<p>/cordis.patch.yml 含 id: ferryman-dsh
	// 的 insert 行。
	ChkProfilePatchInsert = "profile_patch_insert"
	// ChkProfileManifest 插件 manifest 形状（node_modules 拷贝上的 package.json）。
	ChkProfileManifest = "profile_manifest"
	// ChkHomePatchRoute home patch 的 llm-deepseek 路由块仍指向渡口。
	ChkHomePatchRoute = "home_patch_route"
	// ChkHomeModelRoute home patch 的 agent-default-model 仍指 deepseek-official
	// 路由（即渡口适配器）。
	ChkHomeModelRoute = "home_patch_model_route"
	// ChkHomeEnvToken ~/.dsh/.env 的渡口令牌行在位。
	ChkHomeEnvToken = "home_env_token"
	// ChkInstallVersion DSH 安装树版本可读。
	ChkInstallVersion = "dsh_install_version"
)

// CheckResult 单项检查结果（spec 钉死的四字段形状）。
type CheckResult struct {
	Name    string `json:"name"`              // 检查项名（见 Chk* 常量）
	Profile string `json:"profile,omitempty"` // profile 项才有；配置面/版本项为空
	OK      bool   `json:"ok"`
	Detail  string `json:"detail"` // 人读明细；fail 时必指明位置（哪个文件差什么）
}

// Input L0 求值参数——全部显式传入，本包不读环境变量（与 provider.Targets
// 同一纪律：测试用临时目录，真机路径只由 CLI/doctor 装配层解析）。
type Input struct {
	// DSHRoot DSH 生产根（~/.dsh）：其下既挂 profiles/<p>/（插件安装面），
	// 也挂 cordis.patch.yml＋.env（provider 接管的生产配置面）。空串＝
	// 整个 L0 无从求值，返回单项 ChkRoot guard 行。
	DSHRoot string
	// Profiles 待查 profile 列表；空＝DefaultProfiles。
	Profiles []string
	// DSHInstall DSH 安装树根（Windows 实锚
	// C:/Users/allan716/AppData/Local/Programs/DeepSeek Harness/）；空串＝
	// 版本检查项整体省略（参数缺席≠检查失败，如实不 emit）。
	DSHInstall string
	// DockBaseURL 渡口根地址（生产配置面「仍指向渡口」的比对基准）。
	// 空串＝路由项无从判定，按 fail 报出（调用方缺陷，fail 方向安全）。
	DockBaseURL string
}

// RunL0 执行 L0 静态检查（纯读：只 stat/read，零写盘零进程零网络）。返回项
// 序：profile 逐项（按 Profiles 序，每 profile 4 项）→ 生产配置面 3 项 →
// 安装树版本 1 项；永不返回顶层 error——每桩坏事的归属检查项自带 fail＋
// 指位 detail，顶层 error 只会逼调用方丢掉全部单项事实。
func RunL0(in Input) []CheckResult {
	if strings.TrimSpace(in.DSHRoot) == "" {
		return []CheckResult{{Name: ChkRoot,
			Detail: "DSH 根未传入——L0 无法求值（调用方须显式传 ~/.dsh 根；本包不读环境变量猜根）"}}
	}
	profiles := in.Profiles
	if len(profiles) == 0 {
		profiles = DefaultProfiles
	}
	var out []CheckResult
	for _, p := range profiles {
		profDir := filepath.Join(in.DSHRoot, "profiles", p)
		out = append(out,
			junctionCheck(filepath.Join(profDir, "ferryman-dsh"), p),
			nodeModulesCheck(filepath.Join(profDir, "node_modules", "ferryman-dsh"), p),
			patchInsertCheck(filepath.Join(profDir, "cordis.patch.yml"), p),
			manifestCheck(filepath.Join(profDir, "node_modules", "ferryman-dsh"), p),
		)
	}
	out = append(out,
		homeRouteCheck(filepath.Join(in.DSHRoot, "cordis.patch.yml"), in.DockBaseURL),
		homeModelRouteCheck(filepath.Join(in.DSHRoot, "cordis.patch.yml")),
		homeEnvTokenCheck(filepath.Join(in.DSHRoot, ".env")),
	)
	if r := installVersionCheck(in.DSHInstall); r != nil {
		out = append(out, *r)
	}
	return out
}

// ---- 三 profile 安装面 ----

// junctionCheck profiles/<p>/ferryman-dsh：存在、确为链接（junction/符号
// 链接——os.Readlink 成功即链接；实目录在此红）、指向可解析（目标归一后
// Stat 落地——悬空 junction 在此红）。判定序「Lstat 存在 → Readlink 是链接
// → Stat 可解析」三段各自给指位 detail。
func junctionCheck(linkPath, profile string) CheckResult {
	r := CheckResult{Name: ChkProfileJunction, Profile: profile}
	if _, err := os.Lstat(linkPath); err != nil {
		r.Detail = fmt.Sprintf("junction 不存在: %s", linkPath)
		return r
	}
	target, err := os.Readlink(linkPath)
	if err != nil {
		r.Detail = fmt.Sprintf("存在但不是链接（实目录/实文件？预期 mklink /J junction）: %s",
			linkPath)
		return r
	}
	resolved := resolveLinkTarget(filepath.Dir(linkPath), target)
	if _, err := os.Stat(resolved); err != nil {
		r.Detail = fmt.Sprintf("junction 指向不可解析: %s → %s: %v", linkPath, target, err)
		return r
	}
	r.OK = true
	r.Detail = fmt.Sprintf("junction 可解析: %s → %s", linkPath, target)
	return r
}

// resolveLinkTarget 链接目标归一：mklink /J 的 substitute name 可能带 NT
// 设备路径前缀（\??\），os.Readlink 原样吐出——剥前缀后 stat；UNC 形态
// （\??\UNC\server\share）剥前缀后须还原成 \\server\share；相对目标按链接
// 所在目录补全。
func resolveLinkTarget(linkDir, target string) string {
	t := target
	for _, pfx := range []string{`\??\`, `\\?\`} {
		if strings.HasPrefix(t, pfx) {
			t = strings.TrimPrefix(t, pfx)
			break
		}
	}
	if strings.HasPrefix(t, `UNC\`) {
		t = `\\` + strings.TrimPrefix(t, `UNC\`)
	}
	if t != "" && !filepath.IsAbs(t) {
		t = filepath.Join(linkDir, t)
	}
	return t
}

// nodeModulesCheck profiles/<p>/node_modules/ferryman-dsh 存在（三拷贝纪律：
// desktop/web/headless 三份 node_modules 同步是部署第一步；拷贝或链接皆可
// ——Stat 跟随链接，断链拷贝同样在此红）。
func nodeModulesCheck(dirPath, profile string) CheckResult {
	r := CheckResult{Name: ChkProfileNodeModules, Profile: profile}
	fi, err := os.Stat(dirPath)
	switch {
	case err != nil:
		r.Detail = fmt.Sprintf("node_modules 拷贝不存在: %s", dirPath)
		return r
	case !fi.IsDir():
		r.Detail = fmt.Sprintf("node_modules 路径不是目录: %s", dirPath)
		return r
	}
	r.OK = true
	r.Detail = fmt.Sprintf("node_modules 拷贝在位: %s", dirPath)
	return r
}

// insertIDLineRe insert 块内目标行（trim 后）：- id: ferryman-dsh。
var insertIDLineRe = regexp.MustCompile(`^- id:\s*ferryman-dsh\s*$`)

// patchInsertCheck profiles/<p>/cordis.patch.yml 含 id: ferryman-dsh 的
// insert 行（loader 的激活入口——dsh plugin add 时从插件自带 cordis.patch.yml
// 的 - insert: 块复制而来，形状锚 plugin/ferryman-dsh/cordis.patch.yml:11-13
// 与生产 web/headless profile 实锚；desktop 生产缺失的正是这一行）。行式
// 扫描：列 0 的 "- insert:" 开块，下一列 0 顶层项或 EOF 收块，块内找
// "- id: ferryman-dsh"。
func patchInsertCheck(patchPath, profile string) CheckResult {
	r := CheckResult{Name: ChkProfilePatchInsert, Profile: profile}
	raw, err := os.ReadFile(patchPath)
	if err != nil {
		r.Detail = fmt.Sprintf("cordis.patch.yml 不可读: %s: %v", patchPath, err)
		return r
	}
	inInsert, found := false, false
	for _, ln := range strings.Split(string(raw), "\n") {
		ln = strings.TrimRight(ln, "\r")
		if strings.HasPrefix(ln, "- ") { // 列 0＝顶层补丁项
			inInsert = strings.TrimSpace(ln) == "- insert:"
			continue
		}
		if inInsert && insertIDLineRe.MatchString(strings.TrimSpace(ln)) {
			found = true
			break
		}
	}
	if !found {
		r.Detail = fmt.Sprintf("cordis.patch.yml 未找到含 id: ferryman-dsh 的 insert 块: %s",
			patchPath)
		return r
	}
	r.OK = true
	r.Detail = fmt.Sprintf("insert 行在位（id: ferryman-dsh）: %s", patchPath)
	return r
}

// manifestCheck 插件 manifest 形状（真相源＝plugin/ferryman-dsh/package.json
// 真实形状；查 node_modules 拷贝上的那份——宿主实际加载面，三拷贝各自漂移
// 各自现形）。判据：
//   - JSON 可解析；
//   - "dsh" 键为对象，manifestVersion／bundle.patch／client.platform 在位
//     （无 dsh 声明的包只作普通依赖安装、不激活任何层——dsh publish.md:56-58）；
//   - exports 含 "." 与 "./package.json" 两键（历史坑：exports 缺 "." 致宿主
//     加载失活，commit 5b3b476）；
//   - main 非空且入口文件相对 manifest 所在目录存在。
func manifestCheck(pluginDir, profile string) CheckResult {
	r := CheckResult{Name: ChkProfileManifest, Profile: profile}
	pkgPath := filepath.Join(pluginDir, "package.json")
	raw, err := os.ReadFile(pkgPath)
	if err != nil {
		r.Detail = fmt.Sprintf("package.json 不可读: %s: %v", pkgPath, err)
		return r
	}
	fail := func(format string, a ...any) CheckResult {
		r.Detail = fmt.Sprintf("%s（%s）", fmt.Sprintf(format, a...), pkgPath)
		return r
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fail("package.json 解析失败: %v", err)
	}
	dsh, ok := doc["dsh"].(map[string]any)
	if !ok {
		return fail(`缺 "dsh" 键或非对象——无 dsh 声明的包不激活任何层`)
	}
	if _, ok := dsh["manifestVersion"]; !ok {
		return fail("dsh.manifestVersion 缺失")
	}
	bundle, ok := dsh["bundle"].(map[string]any)
	if !ok {
		return fail("dsh.bundle 缺失或非对象")
	}
	if s, _ := bundle["patch"].(string); strings.TrimSpace(s) == "" {
		return fail("dsh.bundle.patch 缺失或为空")
	}
	client, ok := dsh["client"].(map[string]any)
	if !ok {
		return fail("dsh.client 缺失或非对象")
	}
	if s, _ := client["platform"].(string); strings.TrimSpace(s) == "" {
		return fail("dsh.client.platform 缺失或为空")
	}
	exports, ok := doc["exports"].(map[string]any)
	if !ok {
		return fail("exports 缺失或非对象")
	}
	for _, k := range []string{".", "./package.json"} {
		v, present := exports[k]
		if !present {
			return fail("exports 缺 %q 键——历史坑：exports 缺 \".\" 宿主加载失活（commit 5b3b476）", k)
		}
		if s, isStr := v.(string); isStr && strings.TrimSpace(s) == "" {
			return fail("exports[%q] 为空串", k)
		}
	}
	main, _ := doc["main"].(string)
	if strings.TrimSpace(main) == "" {
		return fail("main 缺失或为空")
	}
	entry := filepath.Join(pluginDir, main)
	if _, err := os.Stat(entry); err != nil {
		return fail("main 入口文件不存在: %s: %v", entry, err)
	}
	r.OK = true
	r.Detail = fmt.Sprintf("manifest 形状齐（dsh 键/exports 两键/main=%s）: %s", main, pkgPath)
	return r
}

// ---- 生产配置面（写入器＝internal/provider dsh 分支是真相源） ----

// 写入器产物的字面量锚（provider.dsh.go 内为未导出常量/内联字面量——此处
// 镜像并注释出处；provider 侧与双侧测试各自钉住，漂移双双报红）。
const (
	// dshTakeoverMarker provider.dshMarker 的镜像：接管标记（首段判定）。
	dshTakeoverMarker = "ferryman-takeover"
	// dshAdapterID dshHomePatchYAML 的 llm-deepseek 路由块 id（适配器插件注册 id）。
	dshAdapterID = "llm-deepseek"
	// dshRouteID 适配器硬编码的路由 id＝deepseek-official（dsh.go 实锚）——
	// agent-default-model 块的 provider 须指向它，模型流量才落渡口适配器。
	dshRouteID = "deepseek-official"
)

// topIDLineRe 顶层补丁项行（trim 后）：- id: <值>。
var topIDLineRe = regexp.MustCompile(`^- id:\s*(.+?)\s*$`)

// blockValueRe 块内 "key: value" 行（缩进不限——config 下的嵌套键亦匹配；
// models 列表项 "- id: ..." 以 - 打头，不匹配键式）。
var blockValueRe = regexp.MustCompile(`^\s*([A-Za-z][A-Za-z0-9_]*):\s*(.*?)\s*$`)

// dshPatchBlocks 行式扫描补丁文本，取顶层 "- id: <id>" 块的行区间（下一列 0
// "- " 项或 EOF 为界）。行式而非 YAML 树：与写入器的行式外科纪律同构，且
// 不引入 YAML 依赖。
func dshPatchBlocks(text string) map[string][]string {
	blocks := map[string][]string{}
	cur := ""
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimRight(ln, "\r")
		if strings.HasPrefix(ln, "- ") { // 列 0＝顶层项
			if m := topIDLineRe.FindStringSubmatch(strings.TrimSpace(ln)); m != nil {
				cur = m[1]
				blocks[cur] = nil
				continue
			}
			cur = "" // 顶层 - insert: 等非 id 项：换块但不收编
			continue
		}
		if cur != "" {
			blocks[cur] = append(blocks[cur], ln)
		}
	}
	return blocks
}

// blockKey 块内首个同名 "key: value" 行的值。写入器产物里 baseURL/apiKeyEnv
// （llm-deepseek 块）与 provider（agent-default-model 块）块内唯一，形状由
// dshHomePatchYAML 保证。
func blockKey(lines []string, key string) (string, bool) {
	for _, ln := range lines {
		if m := blockValueRe.FindStringSubmatch(ln); m != nil && m[1] == key {
			return m[2], true
		}
	}
	return "", false
}

// hasTakeoverMarker 首段（前 4 行内）含接管标记＝Ferryman 管理域文件（判定
// 窗口与 provider.hasDSHMarker 同口径：只认首段，正文引用链接不算）。
func hasTakeoverMarker(text string) bool {
	for i, ln := range strings.Split(text, "\n") {
		if i >= 4 {
			return false
		}
		if strings.Contains(ln, dshTakeoverMarker) {
			return true
		}
	}
	return false
}

// homeRouteCheck home patch 的 llm-deepseek 路由块仍指向渡口：baseURL 逐字
// 等于传入渡口地址（尾斜杠不计）＋ apiKeyEnv＝provider.DSHTokenEnv。这两环
// 任何一环脱钩＝dsh 流量绕渡口（形态 C，无感漏钱）。判据与 dshHomePatchYAML
// 写入形状一一对应。
func homeRouteCheck(patchPath, dockBaseURL string) CheckResult {
	r := CheckResult{Name: ChkHomePatchRoute}
	if strings.TrimSpace(dockBaseURL) == "" {
		r.Detail = "渡口地址未传入——路由指向无从判定（调用方须传 DockBaseURL）"
		return r
	}
	raw, err := os.ReadFile(patchPath)
	if err != nil {
		r.Detail = fmt.Sprintf("home cordis.patch.yml 不可读: %s: %v", patchPath, err)
		return r
	}
	text := string(raw)
	blk, ok := dshPatchBlocks(text)[dshAdapterID]
	if !ok {
		r.Detail = fmt.Sprintf("home patch 缺 %s 路由块（- id: %s）: %s",
			dshAdapterID, dshAdapterID, patchPath)
		return r
	}
	wantDock := strings.TrimRight(dockBaseURL, "/")
	if baseURL, ok := blockKey(blk, "baseURL"); !ok || strings.TrimRight(baseURL, "/") != wantDock {
		r.Detail = fmt.Sprintf("%s 块 baseURL 未指向渡口（got %q want %q）: %s",
			dshAdapterID, baseURL, dockBaseURL, patchPath)
		return r
	}
	if env, ok := blockKey(blk, "apiKeyEnv"); !ok || env != provider.DSHTokenEnv {
		r.Detail = fmt.Sprintf("%s 块 apiKeyEnv 非 %s（got %q）: %s",
			dshAdapterID, provider.DSHTokenEnv, env, patchPath)
		return r
	}
	r.OK = true
	r.Detail = fmt.Sprintf("%s 路由指向渡口 %s（apiKeyEnv=%s；接管标记在位=%v）: %s",
		dshAdapterID, dockBaseURL, provider.DSHTokenEnv, hasTakeoverMarker(text), patchPath)
	return r
}

// homeModelRouteCheck home patch 的 agent-default-model 块仍指 deepseek-official
// 路由（渡口适配器注册 id）。model 值不钉：默认模型是用户可正当改动的面，
// provider 指向才是路由事实。
func homeModelRouteCheck(patchPath string) CheckResult {
	r := CheckResult{Name: ChkHomeModelRoute}
	raw, err := os.ReadFile(patchPath)
	if err != nil {
		r.Detail = fmt.Sprintf("home cordis.patch.yml 不可读: %s: %v", patchPath, err)
		return r
	}
	blk, ok := dshPatchBlocks(string(raw))["agent-default-model"]
	if !ok {
		r.Detail = fmt.Sprintf("home patch 缺 agent-default-model 块: %s", patchPath)
		return r
	}
	if routeVal, ok := blockKey(blk, "provider"); !ok || routeVal != dshRouteID {
		r.Detail = fmt.Sprintf("agent-default-model 的 provider 未指向 %s 路由（got %q）: %s",
			dshRouteID, routeVal, patchPath)
		return r
	}
	r.OK = true
	r.Detail = fmt.Sprintf("模型路由仍指 %s: %s", dshRouteID, patchPath)
	return r
}

// homeEnvTokenCheck ~/.dsh/.env 的渡口令牌行在位（写入器真相：DSHTokenEnv=
// 占位令牌；值非空即过 dsh 的 MISSING_CREDENTIAL 前置——渡口入站不鉴权）。
func homeEnvTokenCheck(envPath string) CheckResult {
	r := CheckResult{Name: ChkHomeEnvToken}
	raw, err := os.ReadFile(envPath)
	if err != nil {
		r.Detail = fmt.Sprintf(".env 不可读: %s: %v", envPath, err)
		return r
	}
	prefix := provider.DSHTokenEnv + "="
	for _, ln := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(ln, prefix) && strings.TrimSpace(strings.TrimPrefix(ln, prefix)) != "" {
			r.OK = true
			r.Detail = fmt.Sprintf("令牌行在位（%s=<非空>）: %s", provider.DSHTokenEnv, envPath)
			return r
		}
	}
	r.Detail = fmt.Sprintf("缺 %s 令牌行或值为空——适配器 MISSING_CREDENTIAL 前置会炸: %s",
		provider.DSHTokenEnv, envPath)
	return r
}

// ---- DSH 安装树版本 ----

// installVersionCheck DSH 安装树版本可读：version 文件（真安装树实锚
// "C:/Users/allan716/AppData/Local/Programs/DeepSeek Harness/version"＝
// "44.0.0" 无尾换行）。exe 版本资源读取不做——背景材料钉死「实现读文件版本
// 即可」。installRoot 空＝nil（该项整体省略：参数缺席≠检查失败）。
func installVersionCheck(installRoot string) *CheckResult {
	if strings.TrimSpace(installRoot) == "" {
		return nil
	}
	r := &CheckResult{Name: ChkInstallVersion}
	verPath := filepath.Join(installRoot, "version")
	raw, err := os.ReadFile(verPath)
	if err != nil {
		r.Detail = fmt.Sprintf("version 文件不可读: %s: %v", verPath, err)
		return r
	}
	v := strings.TrimSpace(string(raw))
	if v == "" {
		r.Detail = fmt.Sprintf("version 文件为空: %s", verPath)
		return r
	}
	r.OK = true
	r.Detail = fmt.Sprintf("DSH 版本 %s（%s）", v, verPath)
	return r
}
