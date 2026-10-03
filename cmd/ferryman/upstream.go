// upstream.go — 票02：`ferryman upstream list / use`（渡口多上游直连，ADR-0012；
// CLI 契约见 docs/superpowers/specs/20260921-渡口多上游直连-spec.md「CLI 契约」节）。
//
//	list  全部条目 + active 标注 + base_url + model_map 概要 + 可用状态
//	      （缺 key 显示"未配置，需手编 config 填 api_key"；本地中转地址空
//	      key＝合法常态，显示"本地中转（无需 key）"）+ 密钥脱敏（只露尾
//	      4 位）；--json 出同构机器可读表（票04，字段表见 upstreamUsage
//	      注释；F3 脱敏契约：无明文密钥）。
//	use <名> [--config 路径]   （--config 在名前名后皆可；票13 起弃用——
//	      冷切换重启守护已由 provider switch（热切换零重启）替代，命令保留
//	      一版仍可用，输出首行打弃用警示）
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
	"encoding/json"
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

// cmdUpstream 子命令分发（缺省/未知 = 用法退出 2；-h/--help = 帮助面打印
// usage 退 0，本票）。
func cmdUpstream(args []string, w io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, upstreamUsage)
		return 2
	}
	switch args[0] {
	case "-h", "--help":
		fmt.Fprint(w, upstreamUsage)
		return 0
	case "list":
		return cmdUpstreamList(args[1:], w)
	case "use":
		return cmdUpstreamUse(args[1:], w)
	}
	fmt.Fprintf(os.Stderr, "未知 upstream 子命令: %q\n%s", args[0], upstreamUsage)
	return 2
}

const upstreamUsage string = `用法:
  ferryman upstream list [--config 路径] [--json]
                                            # 渡口上游表：active 标注/base_url/
                                            #   model_map 概要/可用状态/密钥脱敏
                                            #   --json 字段: config/active/
                                            #   upstreams[name/active/base_url/
                                            #   model_map/api_key=尾4位掩码/
                                            #   key_status/balance_url]
  ferryman upstream use <名> [--config 路径]  # （弃用）切换 active 并自动重启守护
                                            #   →请用 ferryman provider switch
                                            #   （热切换零重启；--config 在名前名后
                                            #   皆可；自定义配置路径不自动重启；
                                            #   在途请求中断；守护未起来时如实
                                            #   报告，不自动回滚/重试）
`

// upstreamUseDeprecatedNotice 弃用警示（票13，D15）：upstream use 每次执行输出
// 的首行——冷切换重启守护已由 provider switch（热切换零重启）替代；命令本体
// 保留一版不删（退役随 cc-switch 替换收官另行移除）。
const upstreamUseDeprecatedNotice = "弃用警示：冷切换重启守护已弃用，请用 ferryman provider switch（热切换零重启）。"

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
	// 票04：--json 为 list 专属旗标，手工摘出（不进 parseUpstreamFlags 共用面
	// ——use 不得误认 --json）；其余仍交 parseUpstreamFlags（未知旗标维持退 2，
	// 位置参数忽略——文本面既有行为原样）。
	asJSON := false
	rest := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--json" || a == "-json" {
			asJSON = true
			continue
		}
		rest = append(rest, a)
	}
	cfgPath, _, code := parseUpstreamFlags("upstream list", rest)
	if code != 0 {
		return code
	}
	return upstreamListOut(cfgPath, w, asJSON)
}

// upstreamList list 文本出口（票04 前的既有签名与行为原样保留——文本面零漂移）。
func upstreamList(cfgPath string, w io.Writer) int {
	return upstreamListOut(cfgPath, w, false)
}

// upstreamEntryView upstream list 的结构化行（票04 --json；与文本面同一取数）。
// 脱敏契约（F3/T39）：api_key 只装 maskKey 尾 4 位形态（空钥＝空串，状态看
// key_status）——装配即脱敏，整钥绝不进本结构、不经过任何渲染层。
type upstreamEntryView struct {
	Name       string            `json:"name"`
	Active     bool              `json:"active"`
	BaseURL    string            `json:"base_url"`
	ModelMap   map[string]string `json:"model_map"`
	APIKey     string            `json:"api_key"`
	KeyStatus  string            `json:"key_status"`
	BalanceURL string            `json:"balance_url,omitempty"`
}

// --json 的 key_status 枚举（与文本面 renderKeyStatus 三态同判据：key 空 +
// IsLocalRelayAddr 豁免），机器可读形。
const (
	upstreamKeyConfigured = "configured"
	upstreamKeyNotSet     = "not_configured"
	upstreamKeyLocalRelay = "local_relay_exempt"
)

// keyStatusOf 密钥状态枚举（renderKeyStatus 的机器可读同判据版本）。
func keyStatusOf(key, baseURL string) string {
	if key == "" {
		if config.IsLocalRelayAddr(baseURL) {
			return upstreamKeyLocalRelay
		}
		return upstreamKeyNotSet
	}
	return upstreamKeyConfigured
}

// copyModelMap model_map 拷贝（JSON 出口恒非 null——空表落 {}）。
func copyModelMap(mm map[string]string) map[string]string {
	out := make(map[string]string, len(mm))
	for k, v := range mm {
		out[k] = v
	}
	return out
}

// newUpstreamEntryView 条目 → 结构化行（装配即脱敏：api_key 只装 maskKey 形态）。
func newUpstreamEntryView(name string, active bool, up config.DockUpstream) upstreamEntryView {
	key := ""
	if up.APIKey != "" {
		key = maskKey(up.APIKey)
	}
	return upstreamEntryView{
		Name: name, Active: active, BaseURL: up.BaseURL,
		ModelMap: copyModelMap(up.ModelMap), APIKey: key,
		KeyStatus: keyStatusOf(up.APIKey, up.BaseURL), BalanceURL: up.BalanceURL,
	}
}

// collectUpstreamEntryViews 上游表 → 结构化行序列（sortedNames 确定性序）。
func collectUpstreamEntryViews(d *config.DockCfg) []upstreamEntryView {
	rows := make([]upstreamEntryView, 0, len(d.Upstreams))
	for _, name := range sortedNames(d.Upstreams) {
		rows = append(rows, newUpstreamEntryView(name, name == d.Active, d.Upstreams[name]))
	}
	return rows
}

// upstreamListReport upstream list --json 顶层（票04）：config＝解析后的配置
// 路径；note 非空＝无 [dock]/旧单值形态等如实说明；upstreams 恒非 null。
type upstreamListReport struct {
	Config    string              `json:"config"`
	Active    string              `json:"active"`
	Note      string              `json:"note,omitempty"`
	Upstreams []upstreamEntryView `json:"upstreams"`
}

// upstreamListOut list 可测核心（票04 起 asJSON 分渲染）：只读解析（绝不写
// 配置）；文本/JSON 共用同一份 cfg 与同一组判据（maskKey/renderKeyStatus/
// IsLocalRelayAddr），只分渲染。
func upstreamListOut(cfgPath string, w io.Writer, asJSON bool) int {
	resolved := config.ResolveConfigPath(cfgPath)
	cfg, err := config.Load(cfgPath, false)
	if err != nil {
		if asJSON {
			writeJSONLine(w, map[string]string{
				"config": resolved, "error": fmt.Sprintf("配置加载失败: %v", err)})
			return 1 // 与文本面同判——机器可读面不静默成功
		}
		fmt.Fprintf(w, "配置加载失败（%s）: %v\n", resolved, err)
		return 1
	}
	if cfg.Dock == nil {
		if asJSON {
			return writeJSONLine(w, upstreamListReport{Config: resolved,
				Note: "配置无 [dock] 节，渡口未启用，无上游条目",
				Upstreams: []upstreamEntryView{}})
		}
		fmt.Fprintf(w, "渡口上游表：配置无 [dock] 节，渡口未启用，无上游条目（%s）\n", resolved)
		return 0
	}
	d := cfg.Dock
	if len(d.Upstreams) == 0 {
		// 旧单值形态（未迁移/迁移失败回退）：如实说明＋兜底条目脱敏可见
		name, up := d.ActiveUpstream()
		if asJSON {
			return writeJSONLine(w, upstreamListReport{Config: resolved,
				Active: d.Active,
				Note:   "无 [dock.upstreams] 表（旧单值形态——守护下次启动自动迁移出 cc-switch 回退条目＋三条预置）",
				Upstreams: []upstreamEntryView{newUpstreamEntryView(name, true, *up)}})
		}
		fmt.Fprintf(w, "渡口上游表：无 [dock.upstreams] 表（旧单值形态——守护下次启动自动迁移出 cc-switch 回退条目＋三条预置）\n")
		fmt.Fprintf(w, "  旧单值（兜底生效） base_url: %s\n", up.BaseURL)
		fmt.Fprintf(w, "  api_key: %s\n", renderKeyStatus(up.APIKey, up.BaseURL))
		return 0
	}
	if asJSON {
		return writeJSONLine(w, upstreamListReport{Config: resolved,
			Active: d.Active, Upstreams: collectUpstreamEntryViews(d)})
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

// writeJSONLine 票04 共用 JSON 出口（upstream/provider list --json 与 doctor
// --json 的 cmd 面装配各走各的；本助手服务前两者）：单行 JSON＋换行写 w；
// 序列化失败理论不可达（结构全可序列化）也如实退 1——护底线不静默。
func writeJSONLine(w io.Writer, v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintln(w, string(b))
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
	// 票13 弃用处置（D15）：输出首行打弃用警示——行为面零改动，命令保留一版
	// 仍可用（热切换替代见 provider switch）。
	fmt.Fprintln(w, upstreamUseDeprecatedNotice)
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
