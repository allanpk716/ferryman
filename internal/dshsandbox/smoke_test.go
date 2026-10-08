// smoke_test.go — 真机端到端冒烟（票面验收标准⑥；env 门控——缺省跳过，
// FERRYMAN_VERIFY_DSH_SMOKE=1 才真跑）。
//
// 形态：从夜链工作树 go build 一个 ferryman.exe（hiddenCmd 隐藏 go 工具链窗口）
// → 生产 config 抄渡口上游表（「沙箱 daemon 配置的最便宜上游」＝active 条目）
// → 起 L2 沙箱栈跑探针 → 打印 L2 行（票面：跑一次 verify-dsh 得到 L2 行——
// main.go 装配由协调者接线前，本测试即 CLI 装配的同形受载体）。冒烟会花真
// 消息的钱（几厘×2：四痕主消息＋服务面腿消息）——每次运行须人工显式开启。
//
// 断言面：探针完整收口（error=nil）＋四面锚形状齐；Green 与否如实输出
// （不为绿灯放宽断言——环境不可用/断言未过如实报，见包注「合成组成」节）。
package dshsandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"ferryman/internal/config"
	"ferryman/internal/dshledger"
)

// TestVerifyDshRealSmoke 真机冒烟（FERRYMAN_VERIFY_DSH_SMOKE=1 显式开启）。
func TestVerifyDshRealSmoke(t *testing.T) {
	if os.Getenv("FERRYMAN_VERIFY_DSH_SMOKE") != "1" {
		t.Skip("真机冒烟未开启（FERRYMAN_VERIFY_DSH_SMOKE=1 才跑——会花真消息的钱）")
	}
	if runtime.GOOS != "windows" {
		t.Skip("冒烟面向 Windows 本机（DSH 安装形态）")
	}

	// 生产 config：渡口上游表来源（沙箱模型路由＝active 上游承载面）。
	cfg, err := config.Load("", false)
	if err != nil {
		t.Fatalf("生产 config 加载失败: %v", err)
	}
	if cfg.Dock == nil {
		t.Fatalf("生产配置无 [dock] 节——沙箱模型路由无从指（RunL0 同款纪律）")
	}
	active, up := cfg.Dock.ActiveUpstream()
	if up == nil {
		t.Fatalf("渡口 active 上游不可得")
	}
	upstreams := map[string]DockUpstream{active: {
		BaseURL: up.BaseURL, APIKey: up.APIKey, ModelMap: up.ModelMap,
		TextOnly: up.TextOnly, BalanceURL: up.BalanceURL, Dialect: up.Dialect,
		Codex: up.Codex, Pi: up.Pi,
	}}

	// 夜链工作树定位（测试文件在 internal/dshsandbox/ 下——仓库根上跳两层）。
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	pluginSrc := filepath.Join(repoRoot, "plugin", "ferryman-dsh")
	if !dirExists(pluginSrc) {
		t.Fatalf("工作树插件不可得: %s", pluginSrc)
	}

	// go build 夜链 exe（hiddenCmd——零闪窗铁律同样罩住 go 工具链）。
	tmp := t.TempDir()
	exe := filepath.Join(tmp, "ferryman-smoke.exe")
	build := hiddenCmd(procSpec{Path: "go", Args: []string{"build",
		"-o", exe, "./cmd/ferryman"}, Dir: repoRoot})
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build 失败: %v: %s", err, out)
	}

	o := Options{
		ExePath:       exe, // 夜链构建（生产 exe 第二实例的同一形态）
		PluginSrc:     pluginSrc,
		DockUpstreams: upstreams,
		DockActive:    active,
		KeepRoot:      true, // 冒烟现场保留（诊断；绿时也留一轮人工复核）
	}
	t.Logf("冒烟装配: exe=%s plugin=%s active=%s base_url=<redacted>", exe, pluginSrc, active)

	p := NewProbe(o)
	started := time.Now()
	res, err := p.Run()
	if err != nil {
		t.Fatalf("L2 探针执行出错（未完成验证）: %v", err)
	}
	// L2 行（与 run.go printReport 的 L2 单行同义内容）。
	green := "未过"
	if res.Green {
		green = "全过"
	}
	t.Logf("L2 探针（仅代表 web 宿主形态（沙箱））: %s——%s（耗时 %.0fs）",
		green, res.Summary, time.Since(started).Seconds())
	// 四面锚形状齐（Write 结构校验面）。
	for _, key := range []string{dshledger.FaceDiscovery, dshledger.FaceEvents,
		dshledger.FaceDuck, dshledger.FaceBrowser} {
		if _, ok := res.Faces[key]; !ok {
			t.Errorf("契约面 %s 缺失", key)
		}
	}
	if raw, err := json.MarshalIndent(res.Faces, "", "  "); err == nil {
		t.Logf("契约面形状:\n%s", raw)
	}
}
