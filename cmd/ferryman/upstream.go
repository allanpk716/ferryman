// upstream.go — 票02：`ferryman upstream list / use`（渡口多上游直连，ADR-0012；
// CLI 契约见 docs/superpowers/specs/20260921-渡口多上游直连-spec.md「CLI 契约」节）。
//
//	list  全部条目 + active 标注 + base_url + model_map 概要 + 可用状态
//	      （缺 key 显示"未配置，需手编 config 填 api_key"；本地中转地址空
//	      key＝合法常态，显示"本地中转（无需 key）"）+ 密钥脱敏（只露尾
//	      4 位）。
//	use <名> [--config 路径]   （--config 在名前名后皆可）
//	      条目不存在→拒绝并列出可用条目；非本地条目缺 api_key→拒绝并提示
//	      先填 key（本地中转地址空 key 豁免——回退通道不出站鉴权）；
//	      有效→提示"在途请求将被中断"→校验配置（写回会让守护拒启的先拦下）→
//	      原子写 active（internal/config.SetActiveUpstream：临时文件+rename）→
//	      触发守护重启（POST /shutdown 停旧；detached 隐藏拉起 serve，复用钩子
//	      自举同款机制 start-daemon.cmd）→轮询健康检查（/stats）→成功输出新
//	      active 与冷启动提示（停旧曾报错时降级为"健康检查有应答（未能确认
//	      是否为新进程）"——应答可能来自旧守护）。
//	      自定义 --config 路径（解析结果≠默认路径）→切换成功后不自动重启
//	      （重启不透传路径，新守护会读默认配置），如实指引手动重启，退出 0。
//	      健康检查失败→非零退出，如实报告"配置已切换为 <名>，守护进程未起来"，
//	      给手动拉起（ferryman serve/看门）与回退（upstream use cc-switch）指引；
//	      不自动回滚、不自动重试。
//
// 可注入面：upstreamRestartDeps（启/停/健康检查三接口）——单测注桩，绝不真拉
// 长驻进程；真装配 realUpstreamRestartDeps 才触 HTTP 与进程派生。Windows 零
// 闪窗铁律：拉起走 installer.LaunchDaemon（PS Start-Process -WindowStyle
// Hidden，与 Run 键/看门同源）；本进程自己的派生兜底用 spawnDetachedHidden
// （SysProcAttr HideWindow，不闪窗）。
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ferryman/internal/config"
	"ferryman/internal/daemon"
	"ferryman/internal/installer"
)

// 停旧/健康检查的预算（票面纪律：轮询必须带上限，绝不无限等）。
const (
	upstreamShutdownTimeout = 2 * time.Second  // POST /shutdown 单请求超时（看门同款）
	upstreamPortFreeBudget  = 8 * time.Second  // 停旧后等端口释放上限（避免拉起撞口）
	upstreamHealthBudget    = 15 * time.Second // 健康检查轮询总上限
	upstreamHealthInterval  = 300 * time.Millisecond
)

// cmdUpstream 子命令分发（缺省/未知 = 用法退出 2）。
func cmdUpstream(args []string, w io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, upstreamUsage)
		return 2
	}
	switch args[0] {
	case "list":
		return cmdUpstreamList(args[1:], w)
	case "use":
		return cmdUpstreamUse(args[1:], w)
	}
	fmt.Fprintf(os.Stderr, "未知 upstream 子命令: %q\n%s", args[0], upstreamUsage)
	return 2
}

const upstreamUsage = `用法:
  ferryman upstream list [--config 路径]      # 渡口上游表：active 标注/base_url/
                                            #   model_map 概要/可用状态/密钥脱敏
  ferryman upstream use <名> [--config 路径]  # 切换 active 并自动重启守护
                                            #   （--config 在名前名后皆可；自定义
                                            #   配置路径不自动重启；在途请求中断；
                                            #   守护未起来时如实报告，不自动回滚/
                                            #   重试）
`

// parseUpstreamFlags --config 旗标（缺省 = FERRYMAN_CONFIG 或 ~/ferryman/config.toml）。
// 文档顺序 `use <名> [--config 路径]` 与 `use [--config 路径] <名>` 皆可：Go flag
// 在首个位置参数处停止解析，故先手动全扫摘出 --config（含 = 形态、位置参数之后
// 也认；-- 终止符之后仍按位置参数），余下再交 FlagSet——未知旗标依旧报错退出 2。
func parseUpstreamFlags(name string, args []string) (string, []string, int) {
	cfg := ""
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" { // flag 终止符：其后全是位置参数，交回 FlagSet 同语义处理
			rest = append(rest, args[i:]...)
			break
		}
		switch {
		case a == "--config" || a == "-config":
			if i+1 >= len(args) {
				return "", nil, 2 // 旗标缺值
			}
			i++
			cfg = args[i]
		case strings.HasPrefix(a, "--config="):
			cfg = strings.TrimPrefix(a, "--config=")
		case strings.HasPrefix(a, "-config="):
			cfg = strings.TrimPrefix(a, "-config=")
		default:
			rest = append(rest, a)
		}
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	if err := fs.Parse(rest); err != nil {
		return "", nil, 2
	}
	return cfg, fs.Args(), 0
}

// ---- list ----

func cmdUpstreamList(args []string, w io.Writer) int {
	cfgPath, _, code := parseUpstreamFlags("upstream list", args)
	if code != 0 {
		return code
	}
	return upstreamList(cfgPath, w)
}

// upstreamList list 可测核心：只读解析（绝不写配置），逐条渲染状态。
func upstreamList(cfgPath string, w io.Writer) int {
	cfg, err := config.Load(cfgPath, false)
	if err != nil {
		fmt.Fprintf(w, "配置加载失败（%s）: %v\n", config.ResolveConfigPath(cfgPath), err)
		return 1
	}
	resolved := config.ResolveConfigPath(cfgPath)
	if cfg.Dock == nil {
		fmt.Fprintf(w, "渡口上游表：配置无 [dock] 节，渡口未启用，无上游条目（%s）\n", resolved)
		return 0
	}
	d := cfg.Dock
	if len(d.Upstreams) == 0 {
		// 旧单值形态（未迁移/迁移失败回退）：如实说明＋兜底条目脱敏可见
		_, up := d.ActiveUpstream()
		fmt.Fprintf(w, "渡口上游表：无 [dock.upstreams] 表（旧单值形态——守护下次启动自动迁移出 cc-switch 回退条目＋三条预置）\n")
		fmt.Fprintf(w, "  旧单值（兜底生效） base_url: %s\n", up.BaseURL)
		fmt.Fprintf(w, "  api_key: %s\n", renderKeyStatus(up.APIKey, up.BaseURL))
		return 0
	}
	fmt.Fprintf(w, "渡口上游表（config: %s；active = %s）:\n", resolved, d.Active)
	for _, name := range sortedNames(d.Upstreams) {
		up := d.Upstreams[name]
		marker := "  "
		if name == d.Active {
			marker = "* "
		}
		fmt.Fprintf(w, "%s%s%s\n", marker, name, activeMark(name == d.Active))
		fmt.Fprintf(w, "    base_url: %s\n", up.BaseURL)
		fmt.Fprintf(w, "    model_map: %s\n", renderModelMapBrief(up.ModelMap))
		fmt.Fprintf(w, "    api_key: %s\n", renderKeyStatus(up.APIKey, up.BaseURL))
		if up.BalanceURL != "" { // 不配不显示（D11）
			fmt.Fprintf(w, "    balance_url: %s\n", up.BalanceURL)
		}
	}
	return 0
}

func activeMark(isActive bool) string {
	if isActive {
		return "（active）"
	}
	return ""
}

// sortedNames 条目名排序（list 渲染确定性）。
func sortedNames(m map[string]config.DockUpstream) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// renderModelMapBrief model_map 概要：k=v 逗号连接（键序 default 优先、其余字典序）。
func renderModelMapBrief(mm map[string]string) string {
	if len(mm) == 0 {
		return "（空——本地中转条目守卫透传域豁免）"
	}
	others := make([]string, 0, len(mm))
	for k := range mm {
		if k != "default" {
			others = append(others, k)
		}
	}
	sort.Strings(others)
	parts := make([]string, 0, len(mm))
	if v, ok := mm["default"]; ok {
		parts = append(parts, "default="+v)
	}
	for _, k := range others {
		parts = append(parts, k+"="+mm[k])
	}
	return strings.Join(parts, ", ")
}

// renderKeyStatus 密钥脱敏（T39 纪律：整钥绝不出站，只露尾 4 位）。
// 本地中转地址（守卫透传域）的空 key 是合法常态——回退通道不出站鉴权、纯
// 透传存量迁移继承空值——豁免"未配置"警告，显示豁免文案（与 doctor 缺 key
// 提示同单源 config.IsLocalRelayAddr，绝不两套判据）。
func renderKeyStatus(key, baseURL string) string {
	if key == "" {
		if config.IsLocalRelayAddr(baseURL) {
			return "本地中转（无需 key）"
		}
		return "未配置（需手编 config 填 api_key）"
	}
	return maskKey(key) + "（已配置）"
}

// maskKey 只露尾 4 位；不足 4 位全遮。
func maskKey(key string) string {
	if len(key) <= 4 {
		return "****"
	}
	return "****" + key[len(key)-4:]
}

// ---- use ----

func cmdUpstreamUse(args []string, w io.Writer) int {
	cfgPath, rest, code := parseUpstreamFlags("upstream use", args)
	if code != 0 {
		return code
	}
	if len(rest) != 1 {
		fmt.Fprint(os.Stderr, upstreamUsage)
		return 2
	}
	return upstreamUse(cfgPath, rest[0], w, nil) // deps nil = 真装配
}

// upstreamRestartDeps 守护重启可注入面（票02 验收：启/停/健康检查接口可替换
// ——单测注桩不拉长驻进程；真装配 realUpstreamRestartDeps）。
type upstreamRestartDeps struct {
	Port     int
	Token    string
	Shutdown func() error                    // 停旧（POST /shutdown＋等端口释放）
	Launch   func() error                    // detached 隐藏拉起 serve
	Health   func(budget time.Duration) bool // 轮询 /stats 至有应答或超时
}

// upstreamCfgIsDefault 当前 cfgPath 是否解析为默认配置路径（与 config.
// ResolveConfigPath 同源判断：显式自定义路径 ≠ 默认解析结果即拒绝自动重启——
// 测试注入临时路径用，可覆写；真默认在用户主目录，测试不写那里）。
var upstreamCfgIsDefault = func(cfgPath string) bool {
	return filepath.Clean(config.ResolveConfigPath(cfgPath)) ==
		filepath.Clean(config.ResolveConfigPath(""))
}

// upstreamUse `upstream use <名>` 可测核心。deps nil = 真装配（真 HTTP＋真
// 派生）。流程与拒绝分支见文件头；配置保持已写状态（不自动回滚）。
func upstreamUse(cfgPath, name string, w io.Writer, deps *upstreamRestartDeps) int {
	if name == "" {
		fmt.Fprint(os.Stderr, upstreamUsage)
		return 2
	}
	cfg, err := config.Load(cfgPath, false)
	if err != nil {
		fmt.Fprintf(w, "配置加载失败（%s）: %v\n", config.ResolveConfigPath(cfgPath), err)
		return 1
	}
	resolved := config.ResolveConfigPath(cfgPath)
	if cfg.Dock == nil || len(cfg.Dock.Upstreams) == 0 {
		fmt.Fprintf(w, "拒绝：配置无 [dock.upstreams] 上游表（%s；旧单值形态由守护首启迁移），无可切换条目\n", resolved)
		return 1
	}
	up, ok := cfg.Dock.Upstreams[name]
	if !ok {
		fmt.Fprintf(w, "拒绝：条目 %q 不存在。可用条目: %s（ferryman upstream list 查看）\n",
			name, strings.Join(sortedNames(cfg.Dock.Upstreams), ", "))
		return 1
	}
	if up.APIKey == "" && !config.IsLocalRelayAddr(up.BaseURL) {
		fmt.Fprintf(w, "拒绝：条目 %q 未配置 api_key——请先手编 config 填 api_key 再 use（%s）\n",
			name, resolved)
		return 1
	}
	// 校验（写前拦）：active 换名后的全量配置必须仍过 Validate——写回会让
	// 守护拒启的条目（缺 base_url/非本地缺 default 等）在这里就拒绝。
	probe := *cfg
	dockCopy := *cfg.Dock
	dockCopy.Active = name
	probe.Dock = &dockCopy
	if err := config.Validate(&probe, false); err != nil {
		fmt.Fprintf(w, "拒绝：切换为 %q 后配置校验失败（未写入）:\n%v\n", name, err)
		return 1
	}

	// 在途请求中断提示（票面钉死：输出必须含此提示，后才动配置/守护）。
	fmt.Fprintf(w, "提示：切换将中断在途请求（SSE/长请求随守护重启断开）。\n")
	if err := config.SetActiveUpstream(cfgPath, name); err != nil {
		fmt.Fprintf(w, "写回 active 失败（配置未动）: %v\n", err)
		return 1
	}
	fmt.Fprintf(w, "已写回 active = %q（%s，原子写）\n", name, resolved)

	// 自定义配置路径（解析结果 ≠ 默认路径）：自动重启不透传该路径，新守护会读
	// 默认配置——明示拒绝自动重启（评审 #7 最小修）。切换本身已成功，如实指引
	// 手动重启后退出 0。
	if !upstreamCfgIsDefault(cfgPath) {
		fmt.Fprintf(w, "已切换 active=%s；当前使用自定义配置路径，未尝试自动重启——"+
			"请手动重启守护（如 FERRYMAN_CONFIG=%s ferryman serve 或看门）。\n", name, resolved)
		return 0
	}

	// 守护重启：停旧 → 拉起 → 健康检查（deps 注入）。
	if deps == nil {
		deps = realUpstreamRestartDeps(cfg)
	}
	shutdownErr := error(nil)
	fmt.Fprintf(w, "停旧守护…（POST /shutdown）\n")
	if serr := deps.Shutdown(); serr != nil {
		// 未应答 ≠ 失败终点：守护可能本就未在跑（看门也没拉过）——如实记一笔，
		// 继续拉起（拉起自己会绑定端口）。记下这笔：停旧未确认时健康检查的
		// 应答可能来自旧守护，成功话术须降级（评审 #3 假成功防线）。
		shutdownErr = serr
		fmt.Fprintf(w, "  旧守护未应答（%v；可能未在跑）——继续拉起\n", serr)
	}
	fmt.Fprintf(w, "拉起新守护…（detached 隐藏）\n")
	if lerr := deps.Launch(); lerr != nil {
		fmt.Fprintf(w, "配置已切换为 %s，守护进程未起来（拉起失败: %v）。\n"+
			"手动拉起：ferryman serve（或等看门补位：ferryman watchdog）\n"+
			"回退：ferryman upstream use cc-switch\n", name, lerr)
		return 1
	}
	fmt.Fprintf(w, "健康检查…（GET /stats，≤%s）\n", upstreamHealthBudget)
	if !deps.Health(upstreamHealthBudget) {
		fmt.Fprintf(w, "配置已切换为 %s，守护进程未起来（健康检查 %s 内无应答）。\n"+
			"手动拉起：ferryman serve（或等看门补位：ferryman watchdog）\n"+
			"回退：ferryman upstream use cc-switch\n", name, upstreamHealthBudget)
		return 1
	}
	if shutdownErr != nil {
		fmt.Fprintf(w, "已切换为 %s：健康检查有应答（未能确认是否为新进程；"+
			"此前停旧失败: %v）。\n", name, shutdownErr)
	} else {
		fmt.Fprintf(w, "已切换为 %s：守护已重启，健康检查通过。\n", name)
	}
	fmt.Fprintf(w, "内存缓存快照已清空，旧会话按冷启动全量重付。\n")
	return 0
}

// ---- 真装配（真 HTTP / 真派生；单测不触） ----

// realUpstreamRestartDeps 真装配：token 取守护数据目录（daemon.token 与守护
// 同源——新守护启动时读同一文件，鉴权天然一致）。
func realUpstreamRestartDeps(cfg *config.Config) *upstreamRestartDeps {
	port := cfg.Server.Port
	if port == 0 {
		port = installer.DefaultDaemonPort
	}
	token, _ := daemon.EnsureToken(cfg.DataDir()) // 失败留空 token：探活仍可判"有应答"
	return &upstreamRestartDeps{
		Port:     port,
		Token:    token,
		Shutdown: func() error { return upstreamShutdown(port, token) },
		Launch:   upstreamLaunch,
		Health:   func(budget time.Duration) bool { return upstreamHealth(port, token, budget) },
	}
}

// upstreamShutdown 停旧：POST /shutdown（Bearer；2s 超时）→ 等端口释放
// （≤8s；端口先释才拉起，避免新守护撞口被唯一化跳过）。
func upstreamShutdown(port int, token string) error {
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/shutdown", port), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: upstreamShutdownTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)) // 排水礼节（RST 纪律）
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if !upstreamWaitPortFree(port, upstreamPortFreeBudget) {
		return fmt.Errorf("端口 %d 停旧后 %s 内未释放", port, upstreamPortFreeBudget)
	}
	return nil
}

// upstreamWaitPortFree 拨号探端口释放：连接成功＝仍被占；拒绝/任何拨号错误＝
// 已释放（Windows 拒绝是 WSAECONNREFUSED， POSIX ECONNREFUSED——拨号错误一律
// 视为释放，与看门"无监听才拉起"同判向）。
func upstreamWaitPortFree(port int, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for {
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 500*time.Millisecond)
		if err != nil {
			return true // 无监听＝已释放
		}
		_ = c.Close()
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// upstreamLaunch 拉起新守护（复用钩子自举同款机制）：~/ferryman/start-daemon.cmd
// 经 PS Start-Process -WindowStyle Hidden（与 Run 键/看门同源，零闪窗、日志
// 落 serve.out.log）；点火脚本不在（未跑过 install-cc 的开发环境）→ 兜底本
// exe `serve` 直接 detached 隐藏派生。
func upstreamLaunch() error {
	if home, err := os.UserHomeDir(); err == nil {
		launcher := filepath.Join(home, "ferryman", installer.LauncherName)
		if _, serr := os.Stat(launcher); serr == nil {
			return installer.LaunchDaemon(launcher)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return errors.New("点火脚本不在且拿不到 exe 路径: " + err.Error())
	}
	return spawnDetachedHidden(exe, "serve")
}

// upstreamHealth 轮询 /stats 至有应答（任何 HTTP 状态码都算守护在——与看门
// /update supervisor 的"有应答即活"同判向）或预算耗尽。
func upstreamHealth(port int, token string, budget time.Duration) bool {
	url := fmt.Sprintf("http://127.0.0.1:%d/stats", port)
	deadline := time.Now().Add(budget)
	client := &http.Client{Timeout: upstreamShutdownTimeout}
	for {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return false
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if resp, err := client.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(upstreamHealthInterval)
	}
}
