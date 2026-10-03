// provider.go — 票06：`ferryman provider` 命令族（CLI 操作面，spec
// Implementation Decisions 5；D3 决定 CLI 是唯一写路径、D9 面向本机单人）。
//
//	list            全部条目 + active 标注 + dialect/codex/pi 可用性（需翻译/
//	                原生透传/不支持等）+ 模型位概要 + 密钥脱敏（只露尾 4 位，
//	                整钥零回显——T39）；--json 出同构机器可读表（票04，字段表
//	                见 providerUsage 注释；F3 脱敏契约：无明文密钥）。
//	switch <名>     热切换活跃供应商：走守护管理口 POST /provider_switch
//	                （票02），守护不重启、无端口空窗、在跑会话不断流；条目
//	                不存在→拒绝并列可用；codex="unsupported" 或 pi 不可用
//	                （票12：显式否决/缺 pi 主模型键/dialect 非 anthropic）→
//	                默认拒绝并逐因报明（两 agent 同时断供都列），--cc-only
//	                显式放行并分开列明各自断供面；成功回显新 active 与 codex
//	                车道模式；守护不在线如实报错给拉起指引，不静默失败。
//	                持久化由端点侧落盘（SetActiveUpstream），CLI 零写配置——
//	                与 upstream use（排水重启）语义不同，两族并存。
//	add <名>        编辑本机 config 供应商表：密钥经 --key 或 --key-env 传入，
//	                输出永不回显全钥；--codex/--pi 只收 "unsupported" 否决位
//	                （票12 补 pi 写入面）；写入走 config.AddDockUpstream
//	                （文本手术＋前置校验＋原子写）。
//	remove <名>     删除条目；active 条目拒删（先 switch 再删）。
//	import-ccswitch 读 cc-switch 库（sqlite，票面 D1：只收 claude/codex 两类）
//	                映射入供应商表；dialect 按端点线协议推断（internal/provider
//	                .ImportCCSwitch）；重名跳过不覆盖；库路径默认 ~/.cc-switch/
//	                cc-switch.db（--db 可指；测试全走假库，绝不触真目录）。
//	apply           跑票05 写入器（provider.Apply：F7 前置校验+各目标同戳备份+
//	                外科写入；票10 起含 pi 两文件成对目标）并逐份回显；pi 不可
//	                用时该目标跳过并如实回显、其余目标照常（票12）。--restore
//	                按接管前备份还原（provider.Restore）。目标路径从家目录与
//	                [dock].listen 派生（provider.DockURLFromListen 单源，不自造
//	                拼接）。
//
// 可注入面：providerSwitchDeps（管理口调用）、osUserHomeDir/providerApplyFn/
// providerRestoreFn（apply 缝）——单测注桩，绝不真拉进程、绝不触真用户目录。
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ferryman/internal/config"
	"ferryman/internal/daemon"
	"ferryman/internal/installer"
	"ferryman/internal/provider"
)

const providerUsage string = `用法:
  ferryman provider list [--config 路径] [--json]
                                                # 供应商表：active 标注/dialect/
                                                #   codex/pi 可用性/模型位/密钥脱敏
                                                #   --json 字段: config/active/
                                                #   providers[name/active/base_url/
                                                #   dialect/codex/codex_model/pi/
                                                #   pi_model/model_map/api_key=尾4
                                                #   位掩码/key_status/balance_url]
  ferryman provider switch <名> [--cc-only] [--config 路径]
                                                # 热切换活跃供应商（走守护管理口：
                                                #   不重启、在跑会话不断流）；codex/pi
                                                #   不可用条目默认拒绝并报因，
                                                #   --cc-only 显式放行；守护不在线如实报错
  ferryman provider add <名> --base-url <端点> [--key <钥>|--key-env <环境变量>]
                        [--dialect anthropic|openai_responses] [--codex unsupported]
                        [--pi unsupported] [--model-map k=v,k=v] [--balance-url <端点>]
                        [--config 路径]
                                                # 新增条目（密钥只落本机 config；
                                                #   输出永不回显全钥）
  ferryman provider remove <名> [--config 路径]   # 删除条目（active 条目拒删）
  ferryman provider import-ccswitch [--db 路径] [--config 路径]
                                                # 从 cc-switch 库导入 claude/codex
                                                #   两类供应商（重名跳过不覆盖）；
                                                #   搬家工具，保留不弃用（票13）
  ferryman provider apply [--restore] [--config 路径]
                                                # 三份编辑器配置与 pi 两文件外科写入
                                                #   渡口指向；pi 不可用时该目标跳过
                                                #   （--restore 按接管前备份还原）
`

// cmdProvider 子命令分发（缺省/未知 = 用法退出 2；-h/--help = 帮助面打印
// usage 退 0，本票）。
func cmdProvider(args []string, w io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, providerUsage)
		return 2
	}
	switch args[0] {
	case "-h", "--help":
		fmt.Fprint(w, providerUsage)
		return 0
	case "list":
		return cmdProviderList(args[1:], w)
	case "switch":
		return cmdProviderSwitch(args[1:], w)
	case "add":
		return cmdProviderAdd(args[1:], w)
	case "remove":
		return cmdProviderRemove(args[1:], w)
	case "import-ccswitch":
		return cmdProviderImportCCSwitch(args[1:], w)
	case "apply":
		return cmdProviderApplyCmd(args[1:], w)
	}
	fmt.Fprintf(os.Stderr, "未知 provider 子命令: %q\n%s", args[0], providerUsage)
	return 2
}

// ---- list ----

// parseProviderArgs 摘出 --config（含 -config/= 形态，名前名后皆可；-- 终止符
// 之后全按位置参数交回），其余原样交回各子命令的 FlagSet。与 parseUpstreamFlags
// 的扫描段同形，但不内嵌 FlagSet 兜底解析——provider 各子命令有自有旗标
// （--cc-only/--restore/--base-url/…），须由各自 FlagSet 定义与报错。
func parseProviderArgs(args []string) (string, []string, bool) {
	cfg := ""
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" { // flag 终止符：其后全是位置参数
			rest = append(rest, args[i:]...)
			break
		}
		switch {
		case a == "--config" || a == "-config":
			if i+1 >= len(args) {
				return "", nil, false // 旗标缺值
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
	return cfg, rest, true
}

func cmdProviderList(args []string, w io.Writer) int {
	// 票04：--json 手工摘出（parseProviderArgs 本不管旗标，文本面既有行为——
	// 未知旗标/位置参数被忽略——原样保留，只新认 --json/-json 一个布尔旗标）。
	asJSON := false
	rest := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--json" || a == "-json" {
			asJSON = true
			continue
		}
		rest = append(rest, a)
	}
	cfgPath, _, ok := parseProviderArgs(rest)
	if !ok {
		return 2
	}
	return providerListOut(cfgPath, w, asJSON)
}

// providerList list 文本出口（票04 前的既有签名与行为原样保留——文本面零漂移）。
func providerList(cfgPath string, w io.Writer) int {
	return providerListOut(cfgPath, w, false)
}

// providerEntryView provider list 的结构化行（票04 --json；与文本面同一取数）。
// codex/pi 装可用性裁决值（CodexAvailability/PiAvailability 单源——票09
// providerPiLine 人话行的同一素材）与主模型位；脱敏契约（F3/T39）：api_key 只
// 装 maskKey 尾 4 位形态（空钥＝空串，状态看 key_status）——装配即脱敏，整钥
// 绝不进本结构、不经过任何渲染层。
type providerEntryView struct {
	Name       string            `json:"name"`
	Active     bool              `json:"active"`
	BaseURL    string            `json:"base_url"`
	Dialect    string            `json:"dialect"`
	Codex      string            `json:"codex"`
	CodexModel string            `json:"codex_model"`
	Pi         string            `json:"pi"`
	PiModel    string            `json:"pi_model"`
	ModelMap   map[string]string `json:"model_map"`
	APIKey     string            `json:"api_key"`
	KeyStatus  string            `json:"key_status"`
	BalanceURL string            `json:"balance_url,omitempty"`
}

// newProviderEntryView 条目 → 结构化行（可用性单源裁决＋装配即脱敏）。
func newProviderEntryView(name string, active bool, up config.DockUpstream) providerEntryView {
	key := ""
	if up.APIKey != "" {
		key = maskKey(up.APIKey)
	}
	return providerEntryView{
		Name: name, Active: active, BaseURL: up.BaseURL, Dialect: up.Dialect,
		Codex: up.CodexAvailability(), CodexModel: up.CodexModel(),
		Pi: up.PiAvailability(), PiModel: up.PiModel(),
		ModelMap: copyModelMap(up.ModelMap), APIKey: key,
		KeyStatus: keyStatusOf(up.APIKey, up.BaseURL), BalanceURL: up.BalanceURL,
	}
}

// providerListReport provider list --json 顶层（票04）：providers 恒非 null；
// note 非空＝无 [dock]/旧单值形态等如实说明。
type providerListReport struct {
	Config    string              `json:"config"`
	Active    string              `json:"active"`
	Note      string              `json:"note,omitempty"`
	Providers []providerEntryView `json:"providers"`
}

// providerListOut list 可测核心（票04 起 asJSON 分渲染）：只读解析（绝不写
// 配置）；文本/JSON 共用同一份 cfg 与同一组判据（可用性三态单源/
// maskKey/renderKeyStatus），只分渲染。
func providerListOut(cfgPath string, w io.Writer, asJSON bool) int {
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
			return writeJSONLine(w, providerListReport{Config: resolved,
				Note: "配置无 [dock] 节，渡口未启用，无供应商条目",
				Providers: []providerEntryView{}})
		}
		fmt.Fprintf(w, "供应商表：配置无 [dock] 节，渡口未启用，无供应商条目（%s）\n", resolved)
		return 0
	}
	d := cfg.Dock
	if len(d.Upstreams) == 0 {
		if asJSON {
			return writeJSONLine(w, providerListReport{Config: resolved,
				Note: "无 [dock.upstreams] 表（旧单值形态——守护下次启动自动迁移出上游表后再用 ferryman provider 管理）",
				Providers: []providerEntryView{}})
		}
		fmt.Fprintf(w, "供应商表：无 [dock.upstreams] 表（旧单值形态——守护下次启动自动迁移出上游表后再用 ferryman provider 管理）\n")
		return 0
	}
	rows := make([]providerEntryView, 0, len(d.Upstreams))
	for _, name := range sortedNames(d.Upstreams) {
		rows = append(rows, newProviderEntryView(name, name == d.Active, d.Upstreams[name]))
	}
	if asJSON {
		return writeJSONLine(w, providerListReport{Config: resolved,
			Active: d.Active, Providers: rows})
	}
	fmt.Fprintf(w, "渡口供应商表（config: %s；active = %s）:\n", resolved, d.Active)
	for _, name := range sortedNames(d.Upstreams) {
		up := d.Upstreams[name]
		marker := "  "
		if name == d.Active {
			marker = "* "
		}
		fmt.Fprintf(w, "%s%s%s\n", marker, name, activeMark(name == d.Active))
		fmt.Fprintf(w, "    base_url: %s\n", up.BaseURL)
		fmt.Fprintf(w, "    dialect: %s\n", up.Dialect)
		fmt.Fprintf(w, "    codex: %s\n", providerCodexLine(up))
		fmt.Fprintf(w, "    pi: %s\n", providerPiLine(up))
		fmt.Fprintf(w, "    model_map: %s\n", renderModelMapBrief(up.ModelMap))
		fmt.Fprintf(w, "    api_key: %s\n", renderKeyStatus(up.APIKey, up.BaseURL))
		if up.BalanceURL != "" { // 不配不显示（D11）
			fmt.Fprintf(w, "    balance_url: %s\n", up.BalanceURL)
		}
	}
	return 0
}

// providerCodexLine codex 可用性行（三态人话 + codex 主模型位概要）。
func providerCodexLine(up config.DockUpstream) string {
	if up.CodexAvailability() == config.CodexUnsupported {
		return fmt.Sprintf("不支持（codex = %q；switch 默认拒绝，--cc-only 可显式放行）",
			config.CodexUnsupported)
	}
	modelPart := "codex 主模型位未配"
	if m := up.CodexModel(); m != "" {
		modelPart = "codex 主模型 " + m
	}
	if up.CodexAvailability() == config.CodexNative {
		return "原生透传（openai_responses）· " + modelPart
	}
	return "需翻译（anthropic 方言 → 渡口翻译车道）· " + modelPart
}

// providerPiLine pi 可用性行（票09，与 codex 行并列的三态人话 + pi 主模型位
// 概要；票12 起 switch/apply 对 pi 不可用同 codex 先例拒绝/放行/跳过）。
func providerPiLine(up config.DockUpstream) string {
	if up.PiAvailability() == config.PiUnsupported {
		return fmt.Sprintf("不支持（pi = %q）", config.PiUnsupported)
	}
	modelPart := "pi 主模型位未配"
	if m := up.PiModel(); m != "" {
		modelPart = "pi 主模型 " + m
	}
	if up.PiAvailability() == config.PiUnavailable {
		return "不可用（openai_responses 方言，pi 无入站车道）· " + modelPart
	}
	return "可用（anthropic 方言，pi 复用 CC 车道）· " + modelPart
}

// ---- switch ----

func cmdProviderSwitch(args []string, w io.Writer) int {
	cfgPath, rest, ok := parseProviderArgs(args)
	if !ok {
		return 2
	}
	rest = orderFlagPairsFirst(rest)
	fs := flag.NewFlagSet("provider switch", flag.ContinueOnError)
	ccOnly := fs.Bool("cc-only", false, "对 codex/pi 不可用条目显式放行"+
		"（明示各自断供面：codex 暂断供仅 CC；pi 暂断供仅 CC/codex）")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprint(os.Stderr, providerUsage)
		return 2
	}
	return providerSwitch(cfgPath, fs.Arg(0), *ccOnly, w, nil) // deps nil = 真装配
}

// providerSwitchDeps 管理口调用可注入面（单测注桩不触网络；真装配经
// providerSwitchRequest 打守护管理口）。
type providerSwitchDeps struct {
	Port   int
	Token  string
	Switch func(name string) (map[string]any, error)
}

// realProviderSwitchDeps 真装配：port/token 与守护同源（Server.Port 与数据
// 目录 daemon.token——upstream 族同纪律）。
func realProviderSwitchDeps(cfg *config.Config) *providerSwitchDeps {
	port := cfg.Server.Port
	if port == 0 {
		port = installer.DefaultDaemonPort
	}
	token, _ := daemon.EnsureToken(cfg.DataDir()) // 失败留空：端点会 401，如实报
	return &providerSwitchDeps{Port: port, Token: token,
		Switch: func(name string) (map[string]any, error) {
			return providerSwitchRequest(port, token, name)
		}}
}

// providerSwitch switch 可测核心：拒绝分支（不存在/codex/pi 不可用，票12 扩
// pi）先于任何网络；热切换走管理口（配置写与内存换绑都在端点侧，CLI 零写配置）。
func providerSwitch(cfgPath, name string, ccOnly bool, w io.Writer, deps *providerSwitchDeps) int {
	if name == "" {
		fmt.Fprint(os.Stderr, providerUsage)
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
		fmt.Fprintf(w, "拒绝：条目 %q 不存在。可用条目: %s（ferryman provider list 查看）\n",
			name, strings.Join(sortedNames(cfg.Dock.Upstreams), ", "))
		return 1
	}
	// 可用性拒绝面（F2；票12 扩 pi）：codex＝显式否决位（票06 先例）；pi＝
	// 显式否决/方言非 anthropic/缺 pi 主模型键（票09 PiAvailability/PiModel
	// 单源裁决）。默认拒绝并逐因报明——两 agent 同时断供时都列，不混写；
	// --cc-only 显式放行并逐 agent 明示断供面。拒绝先于任何网络。
	type providerOutage struct{ agent, why string }
	var outages []providerOutage
	if up.CodexAvailability() == config.CodexUnsupported {
		outages = append(outages, providerOutage{"codex",
			fmt.Sprintf("codex = %q", config.CodexUnsupported)})
	}
	switch av := up.PiAvailability(); {
	case av == config.PiUnsupported:
		outages = append(outages, providerOutage{"pi",
			fmt.Sprintf("pi = %q", config.PiUnsupported)})
	case av == config.PiUnavailable:
		outages = append(outages, providerOutage{"pi",
			fmt.Sprintf("dialect = %q，pi 无入站车道", up.Dialect)})
	case up.PiModel() == "":
		outages = append(outages, providerOutage{"pi", "model_map 缺 pi 主模型键"})
	}
	if len(outages) > 0 {
		whys := make([]string, 0, len(outages))
		agents := make([]string, 0, len(outages))
		for _, o := range outages {
			whys = append(whys, fmt.Sprintf("对 %s 不可用（%s）", o.agent, o.why))
			agents = append(agents, o.agent)
		}
		if !ccOnly {
			fmt.Fprintf(w, "拒绝：条目 %q %s——切换默认拒绝，避免不知不觉把 %s 弄断。"+
				"若确认接受上述断供面，加 --cc-only 显式放行。\n",
				name, strings.Join(whys, "；"), strings.Join(agents, "、"))
			return 1
		}
		for _, o := range outages {
			if o.agent == "codex" {
				fmt.Fprintf(w, "注意：codex 暂断供，仅 CC 走该供应商（--cc-only 显式放行）——"+
					"codex 请求到渡口将收到带原因的显式错误，不会挂起。\n")
			} else {
				fmt.Fprintf(w, "注意：pi 暂断供，仅 CC/codex 走该供应商（--cc-only 显式放行）。\n")
			}
		}
	}
	if deps == nil {
		deps = realProviderSwitchDeps(cfg)
	}
	resp, err := deps.Switch(name)
	if err != nil {
		// 不静默失败：如实报因。守护不在线给拉起指引；端点业务拒绝（400/
		// 401 等，如 CLI 所读配置与守护在跑的表不同步）如实透传报因，不冒充
		// "不在线"。
		if msg := err.Error(); strings.Contains(msg, "守护不在线") {
			fmt.Fprintf(w, "热切换失败: %v\n配置未动、活跃供应商未变——守护不在线或管理口不可达时无法热切换。\n"+
				"拉起守护：ferryman serve（或等看门补位：ferryman watchdog），起来后再试。\n", err)
		} else {
			fmt.Fprintf(w, "热切换失败: %v\n配置未动、活跃供应商未变（守护侧拒绝，以上为端点报因；"+
				"以守护在跑的配置为准，ferryman provider list 可对照）。\n", err)
		}
		return 1
	}
	active := name
	if s, ok := resp["active"].(string); ok && s != "" {
		active = s // 以端点回执为准
	}
	fmt.Fprintf(w, "已热切换 active = %s（守护不重启、无端口空窗，新请求即刻生效；已落盘持久化）\n", active)
	if bu, ok := resp["base_url"].(string); ok && bu != "" {
		fmt.Fprintf(w, "  base_url: %s\n", bu)
	}
	lane := "未知"
	if c, ok := resp["codex"].(string); ok {
		lane = codexLaneText(c)
	}
	fmt.Fprintf(w, "  codex 车道: %s\n", lane)
	return 0
}

// codexLaneText 端点回执的 codex 可用性 → 人话。
func codexLaneText(codex string) string {
	switch codex {
	case config.CodexTranslation:
		return "需翻译（anthropic 方言 → 渡口翻译车道）"
	case config.CodexNative:
		return "原生透传（openai_responses）"
	case config.CodexUnsupported:
		return "不支持（已 --cc-only 显式放行，codex 暂断供）"
	}
	return codex
}

// providerSwitchTimeout 管理口单请求超时（端点侧做落盘＋内存换绑，5s 足够；
// 比停旧预算宽松——upstreamShutdownTimeout 是 2s 但那边还有等端口释放）。
const providerSwitchTimeout = 5 * time.Second

// providerSwitchRequest POST /provider_switch（票02 端点；守门 loopback→POST→
// Bearer）。死口/拒绝连接 = 守护不在线（如实点名）；401 = 令牌失配；400 =
// 端点业务拒绝（报因透传）。
func providerSwitchRequest(port int, token, name string) (map[string]any, error) {
	body, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/provider_switch", port), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: providerSwitchTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("守护不在线（%v）", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		data = nil // 响应体读不全不掩盖状态码判断
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		var out map[string]any
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, fmt.Errorf("管理口 200 响应非 JSON: %v", err)
		}
		return out, nil
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, errors.New("鉴权失败（Bearer 与守护 daemon.token 不匹配——令牌以数据目录 daemon.token 为准）")
	case resp.StatusCode == http.StatusForbidden:
		return nil, errors.New("管理口拒绝（仅 loopback 来源）")
	default:
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
}

// ---- add / remove ----

// providerAddOpts add 的旗标集（providerAddArgs 组装后的纯数据）。
type providerAddOpts struct {
	BaseURL    string
	Key        string // 字面量密钥（不回显；仅落本机 config）
	KeyEnv     string // 环境变量名（优先级低于 Key）
	Dialect    string // anthropic（缺省）| openai_responses
	Codex      string // 仅 "unsupported" 否决位
	Pi         string // 仅 "unsupported" 否决位（票12，对标 Codex 同款）
	ModelMap   string // "k=v[,k=v]"
	BalanceURL string
}

func cmdProviderAdd(args []string, w io.Writer) int {
	cfgPath, rest, ok := parseProviderArgs(args)
	if !ok {
		return 2
	}
	rest = orderFlagPairsFirst(rest, "base-url", "key", "key-env", "dialect",
		"codex", "pi", "model-map", "balance-url")
	fs := flag.NewFlagSet("provider add", flag.ContinueOnError)
	baseURL := fs.String("base-url", "", "上游端点（必填）")
	key := fs.String("key", "", "API 密钥字面量（或 --key-env；都不给＝未激活预置）")
	keyEnv := fs.String("key-env", "", "从该环境变量读 API 密钥（密钥不进 shell 历史）")
	dialect := fs.String("dialect", "", "线协议方言：anthropic（缺省）| openai_responses")
	codex := fs.String("codex", "", "codex 否决位：仅 unsupported（可用性缺省按 dialect 推导）")
	pi := fs.String("pi", "", "pi 否决位：仅 unsupported（可用性缺省按 dialect 推导）")
	modelMap := fs.String("model-map", "", "模型位 k=v[,k=v]（非本地端点必含 default；codex 键＝codex 主模型）")
	balanceURL := fs.String("balance-url", "", "余额端点（不配不显示）")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprint(os.Stderr, providerUsage)
		return 2
	}
	return providerAdd(cfgPath, fs.Arg(0), providerAddOpts{
		BaseURL: *baseURL, Key: *key, KeyEnv: *keyEnv, Dialect: *dialect,
		Codex: *codex, Pi: *pi, ModelMap: *modelMap, BalanceURL: *balanceURL,
	}, w)
}

// providerAdd add 可测核心：旗标校验 → 配置上下文校验（重名/可写性，借票02
// 校验单源）→ config.AddDockUpstream 写回。输出永不含全钥（T39）。
func providerAdd(cfgPath, name string, opts providerAddOpts, w io.Writer) int {
	resolved := config.ResolveConfigPath(cfgPath)
	if name == "" {
		fmt.Fprint(os.Stderr, providerUsage)
		return 2
	}
	if strings.TrimSpace(opts.BaseURL) == "" {
		fmt.Fprintln(w, "拒绝：--base-url 必填（上游端点不明不写入）")
		return 1
	}
	dialect := opts.Dialect
	if dialect == "" {
		dialect = config.DialectAnthropic
	}
	if dialect != config.DialectAnthropic && dialect != config.DialectOpenAIResponses {
		fmt.Fprintf(w, "拒绝：dialect 非法 %q（可选 anthropic（缺省）/openai_responses）\n", opts.Dialect)
		return 1
	}
	if opts.Codex != "" && opts.Codex != config.CodexUnsupported {
		fmt.Fprintf(w, "拒绝：codex 仅可选 \"unsupported\" 否决位（可用性缺省按 dialect 推导）\n")
		return 1
	}
	if opts.Pi != "" && opts.Pi != config.PiUnsupported {
		fmt.Fprintf(w, "拒绝：pi 仅可选 \"unsupported\" 否决位（可用性缺省按 dialect 推导）\n")
		return 1
	}
	mm := map[string]string{}
	for _, part := range strings.Split(opts.ModelMap, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			fmt.Fprintf(w, "拒绝：--model-map 形态非法 %q（须 k=v 逗号分隔）\n", part)
			return 1
		}
		mm[k] = strings.TrimSpace(v)
	}
	key := ""
	switch {
	case opts.Key != "":
		key = opts.Key
	case opts.KeyEnv != "":
		v, ok := os.LookupEnv(opts.KeyEnv)
		if !ok || strings.TrimSpace(v) == "" {
			fmt.Fprintf(w, "拒绝：环境变量 %s 未设置或为空——密钥读不到，未写入任何内容\n", opts.KeyEnv)
			return 1
		}
		key = v
	}
	up := config.DockUpstream{BaseURL: opts.BaseURL, APIKey: key, ModelMap: mm,
		Dialect: dialect, Codex: opts.Codex, Pi: opts.Pi, BalanceURL: opts.BalanceURL}
	cfg, err := config.Load(cfgPath, false)
	if err != nil {
		fmt.Fprintf(w, "配置加载失败（%s）: %v\n", resolved, err)
		return 1
	}
	if cfg.Dock == nil || len(cfg.Dock.Upstreams) == 0 {
		fmt.Fprintf(w, "拒绝：配置无 [dock.upstreams] 上游表（%s；旧单值形态由守护首启迁移）\n", resolved)
		return 1
	}
	if _, exists := cfg.Dock.Upstreams[name]; exists {
		fmt.Fprintf(w, "拒绝：条目 %q 已存在（不覆盖——先 provider remove 或另取名）\n", name)
		return 1
	}
	if probs := config.CheckDockUpstreamEntry(cfg.Dock, name, up); len(probs) > 0 {
		fmt.Fprintf(w, "拒绝：条目写回前置校验失败（未写入）:\n  - %s\n", strings.Join(probs, "\n  - "))
		return 1
	}
	if err := config.AddDockUpstream(cfgPath, name, up); err != nil {
		fmt.Fprintf(w, "写入失败（配置未动）: %v\n", err)
		return 1
	}
	fmt.Fprintf(w, "已写入条目 %s（config: %s；密钥只落本机 config，T39）\n", name, resolved)
	fmt.Fprintf(w, "  base_url: %s\n", up.BaseURL)
	fmt.Fprintf(w, "  dialect: %s\n", up.Dialect)
	if up.Codex != "" {
		fmt.Fprintf(w, "  codex: 否决位 %q（switch 默认拒绝）\n", up.Codex)
	}
	if up.Pi != "" {
		fmt.Fprintf(w, "  pi: 否决位 %q（switch 默认拒绝）\n", up.Pi)
	}
	fmt.Fprintf(w, "  model_map: %s\n", renderModelMapBrief(up.ModelMap))
	fmt.Fprintf(w, "  api_key: %s\n", renderKeyStatus(up.APIKey, up.BaseURL))
	if up.BalanceURL != "" {
		fmt.Fprintf(w, "  balance_url: %s\n", up.BalanceURL)
	}
	return 0
}

func cmdProviderRemove(args []string, w io.Writer) int {
	cfgPath, rest, ok := parseProviderArgs(args)
	if !ok {
		return 2
	}
	if len(rest) != 1 {
		fmt.Fprint(os.Stderr, providerUsage)
		return 2
	}
	return providerRemove(cfgPath, rest[0], w)
}

// providerRemove remove 可测核心：存在性 + active 保护先行（拒绝给 switch
// 指引），删除走 config.RemoveDockUpstream（文本手术＋校验＋原子写）。
func providerRemove(cfgPath, name string, w io.Writer) int {
	resolved := config.ResolveConfigPath(cfgPath)
	cfg, err := config.Load(cfgPath, false)
	if err != nil {
		fmt.Fprintf(w, "配置加载失败（%s）: %v\n", resolved, err)
		return 1
	}
	if cfg.Dock == nil || len(cfg.Dock.Upstreams) == 0 {
		fmt.Fprintf(w, "拒绝：配置无 [dock.upstreams] 上游表（%s），无可删条目\n", resolved)
		return 1
	}
	if _, ok := cfg.Dock.Upstreams[name]; !ok {
		fmt.Fprintf(w, "拒绝：条目 %q 不存在。可用条目: %s\n",
			name, strings.Join(sortedNames(cfg.Dock.Upstreams), ", "))
		return 1
	}
	if name == cfg.Dock.Active {
		fmt.Fprintf(w, "拒绝：条目 %q 是当前 active——先 ferryman provider switch 到其他条目再删除"+
			"（删了 active 会让守护拒启）\n", name)
		return 1
	}
	if err := config.RemoveDockUpstream(cfgPath, name); err != nil {
		fmt.Fprintf(w, "删除失败（配置未动）: %v\n", err)
		return 1
	}
	remaining := make([]string, 0, len(cfg.Dock.Upstreams))
	for n := range cfg.Dock.Upstreams {
		if n != name {
			remaining = append(remaining, n)
		}
	}
	sort.Strings(remaining)
	fmt.Fprintf(w, "已删除条目 %s（config: %s）。剩余条目: %s\n", name, resolved,
		strings.Join(remaining, ", "))
	return 0
}

// ---- import-ccswitch ----

func cmdProviderImportCCSwitch(args []string, w io.Writer) int {
	cfgPath, rest, ok := parseProviderArgs(args)
	if !ok {
		return 2
	}
	rest = orderFlagPairsFirst(rest, "db")
	fs := flag.NewFlagSet("provider import-ccswitch", flag.ContinueOnError)
	db := fs.String("db", "", "cc-switch 数据库路径（缺省 ~/.cc-switch/cc-switch.db）")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprint(os.Stderr, providerUsage)
		return 2
	}
	return providerImportCCSwitch(cfgPath, *db, w)
}

// providerImportCCSwitch import 可测核心：库读取与映射在 internal/provider
// （测试走假库）；本层做冲突取舍（重名跳过）与一次原子批量写入。
func providerImportCCSwitch(cfgPath, dbPath string, w io.Writer) int {
	resolved := config.ResolveConfigPath(cfgPath)
	if dbPath == "" {
		home, err := osUserHomeDir()
		if err != nil {
			fmt.Fprintf(w, "家目录解析失败: %v（可用 --db 直接指定 cc-switch 库路径）\n", err)
			return 1
		}
		dbPath = filepath.Join(home, ".cc-switch", "cc-switch.db")
	}
	cfg, err := config.Load(cfgPath, false)
	if err != nil {
		fmt.Fprintf(w, "配置加载失败（%s）: %v\n", resolved, err)
		return 1
	}
	if cfg.Dock == nil || len(cfg.Dock.Upstreams) == 0 {
		fmt.Fprintf(w, "拒绝：配置无 [dock.upstreams] 上游表（%s；旧单值形态由守护首启迁移后再导入）\n", resolved)
		return 1
	}
	cands, err := provider.ImportCCSwitch(dbPath)
	if err != nil {
		fmt.Fprintf(w, "%v\n（cc-switch 未安装或库不在默认位置时用 --db 指定路径）\n", err)
		return 1
	}
	accepted := map[string]config.DockUpstream{}
	skipped := 0
	for _, c := range cands {
		if c.Skip {
			fmt.Fprintf(w, "跳过 %s（%s）: %s\n", c.Name, c.AppType, c.Reason)
			skipped++
			continue
		}
		if _, conflict := cfg.Dock.Upstreams[c.Name]; conflict {
			fmt.Fprintf(w, "跳过 %s（%s）: 重名冲突——既有条目保持不动，不覆盖\n", c.Name, c.AppType)
			skipped++
			continue
		}
		if _, dup := accepted[c.Name]; dup {
			fmt.Fprintf(w, "跳过 %s（%s）: 批内重名（保留前一条同名候选）\n", c.Name, c.AppType)
			skipped++
			continue
		}
		accepted[c.Name] = c.Up
		fmt.Fprintf(w, "导入 %s（%s）: %s · %s\n", c.Name, c.AppType, c.Up.BaseURL,
			providerDialectBrief(c.Up))
	}
	if len(accepted) == 0 {
		fmt.Fprintf(w, "无可导入条目（库空或全部跳过，共 %d 行；配置未动）\n", len(cands))
		return 0
	}
	if err := config.MergeDockUpstreams(cfgPath, accepted); err != nil {
		fmt.Fprintf(w, "批量写入失败（配置未动）: %v\n", err)
		return 1
	}
	fmt.Fprintf(w, "导入完成：新增 %d 条，跳过 %d 条（config: %s；密钥只落本机 config，T39）\n",
		len(accepted), skipped, resolved)
	return 0
}

// providerDialectBrief 导入行的人话方言标注。
func providerDialectBrief(up config.DockUpstream) string {
	switch up.CodexAvailability() {
	case config.CodexNative:
		return "原生透传（openai_responses）"
	case config.CodexUnsupported:
		return "codex 不可用（否决位）"
	}
	return "需翻译（anthropic）"
}

// ---- apply / --restore ----

// osUserHomeDir 缝（测试注入临时家目录——真实家目录下的 ~/.claude ~/.codex
// 测试零触碰）。
var osUserHomeDir = os.UserHomeDir

// providerHooksDirFn CC 钩子脚本目录缝（票01 P2-3）：exe 同根 hooks/——
// installer.repoRoot 同位惯例（build.ps1 把 exe 出到仓库根，hooks/ 与 exe
// 同根）。测试注入临时目录，绝不触真 exe 目录。
var providerHooksDirFn = func() string {
	exe, err := os.Executable()
	if err != nil {
		return "hooks"
	}
	return filepath.Join(filepath.Dir(exe), "hooks")
}

// providerApplyFn / providerRestoreFn 写入器缝（票05 单测已覆盖其本体；CLI 层
// 只钉"参数透传＋逐份回显"）。
var (
	providerApplyFn   = provider.Apply
	providerRestoreFn = provider.Restore
)

// providerTargetsFromHome 各配置目标 + 渡口地址派生（纯函数）：orca 份路径
// 与 installer doctor 同位（<Home>/AppData/Roaming/orca/codex-runtime-home/
// home/config.toml）；pi 两文件在 <Home>/.pi/agent/ 下（票10 第四目标，成对
// 派生）；dsh 份＝<Home>/.dsh（目录不在位由写入器 skip，不代建）；dsh 钩子桥
// 配置＝<Home>/ferryman/dsh-hooks/hooks.json（Ferryman 自家目录，绝不
// ~/.dsh——D12；票01 P2-3）；钩子脚本目录不经本函数（exe 派生，由
// providerApply 经 providerHooksDirFn 注入）；渡口地址走
// provider.DockURLFromListen 单源（评审留话：不自造拼接、尾斜杠不在 CLI
// 层归一）。pi 主模型位与可用性位由调用方自 active 上游条目派生后透传
//（票09 PiModel/PiAvailability；票12 起可用性位进行为面——写入器据此跳过
// pi 目标）。
func providerTargetsFromHome(home, dockListen, piModel, piAvail string) provider.Targets {
	return provider.Targets{
		CCSettings:     filepath.Join(home, ".claude", "settings.json"),
		CodexConfig:    filepath.Join(home, ".codex", "config.toml"),
		OrcaCodexConfig: filepath.Join(home, "AppData", "Roaming", "orca",
			"codex-runtime-home", "home", "config.toml"),
		PiModels:       filepath.Join(home, ".pi", "agent", "models.json"),
		PiSettings:     filepath.Join(home, ".pi", "agent", "settings.json"),
		PiModel:        piModel,
		PiAvailability: piAvail,
		DSHHome:        filepath.Join(home, ".dsh"),
		DSHHooksJSON:   filepath.Join(home, "ferryman", "dsh-hooks", "hooks.json"),
		DockBaseURL:    provider.DockURLFromListen(dockListen),
	}
}

func cmdProviderApplyCmd(args []string, w io.Writer) int {
	cfgPath, rest, ok := parseProviderArgs(args)
	if !ok {
		return 2
	}
	rest = orderFlagPairsFirst(rest)
	fs := flag.NewFlagSet("provider apply", flag.ContinueOnError)
	restore := fs.Bool("restore", false, "按最近一组接管前备份还原三份（回 interim 拓扑）")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprint(os.Stderr, providerUsage)
		return 2
	}
	return providerApply(cfgPath, *restore, w)
}

// providerApply apply/--restore 可测核心：目标派生（[dock].listen 单源）→
// 写入器缝调用 → 逐份回显（名/路径/动作/备份/明细）。
func providerApply(cfgPath string, restore bool, w io.Writer) int {
	cfg, err := config.Load(cfgPath, false)
	if err != nil {
		fmt.Fprintf(w, "配置加载失败（%s）: %v\n", config.ResolveConfigPath(cfgPath), err)
		return 1
	}
	if cfg.Dock == nil {
		fmt.Fprintf(w, "拒绝：配置无 [dock] 节——渡口未启用，apply 无目标地址可派生"+
			"（先配 [dock] 并让守护跑起来）\n")
		return 1
	}
	home, err := osUserHomeDir()
	if err != nil {
		fmt.Fprintf(w, "家目录解析失败: %v\n", err)
		return 1
	}
	// pi 主模型位与可用性位（票10/票12）：自 active 上游条目 model_map 的 pi 键
	// 派生（票09 PiModel），可用性走票09 PiAvailability 单源。active 悬空/旧
	// 单值兜底条目 → 空串/可用推导。pi 主模型缺 → 写入器按异形拒绝转人工
	//（票10 语义，不变）；可用性不可用 → 写入器跳过 pi 目标并如实回显（票12），
	// 其余目标照常——两态分开，本层只透传不裁决。
	var piModel, piAvail string
	if _, up := cfg.Dock.ActiveUpstream(); up != nil {
		piModel = up.PiModel()
		piAvail = up.PiAvailability()
	}
	targets := providerTargetsFromHome(home, cfg.Dock.Listen, piModel, piAvail)
	targets.FerrymanHooksDir = providerHooksDirFn()
	var (
		rep provider.ApplyReport
	)
	if restore {
		fmt.Fprintf(w, "接管还原（--restore；按最近一组接管前备份还原，回 interim 拓扑）:\n")
		rep, err = providerRestoreFn(targets)
	} else {
		fmt.Fprintf(w, "接管 apply（配置外科写入；F7 认证前置校验；备份同戳成组；pi 两文件成对落盘）:\n")
		rep, err = providerApplyFn(targets)
	}
	for _, r := range rep.Targets {
		printTargetReport(w, r)
	}
	if err != nil {
		fmt.Fprintf(w, "失败: %v（以上为已完成部分；转人工按明细处置）\n", err)
		return 1
	}
	// dsh 写入后的一次性提示：配置热生效依赖 dsh-hmr，未开启的实例要重启
	// 才看到 home 层（桌面端与 CLI web 实例各一次）。
	if !restore {
		for _, r := range rep.Targets {
			if strings.HasPrefix(r.Name, "dsh-") && r.Action == "written" {
				fmt.Fprintf(w, "提示: dsh 配置需重启 dsh 实例生效（CLI web 实例与桌面端；"+
					"开 HMR 的实例下一请求即生效）。\n")
				break
			}
		}
		// dsh 钩子桥（票01 P2-3）：文件已单发到位，桥插件的安装是晨间人工
		// 步骤（本命令绝不代执行安装）。unchanged 也提示——晨间可能跑两次
		// apply，第二次不能丢安装指引。
		for _, r := range rep.Targets {
			if r.Name == "dsh-hooks" && (r.Action == "written" || r.Action == "unchanged") {
				fmt.Fprintf(w, "dsh 钩子桥配置已就绪: %s\n", r.Path)
				fmt.Fprintf(w, "晨间人工安装（本命令不代执行安装）:\n")
				fmt.Fprintf(w, "  1. dsh plugin --profile web add @deepseek-ai/dsh-hooks-claude-code\n")
				fmt.Fprintf(w, "  2. 插件配置把 configPath 指向上述 hooks.json——用绝对路径"+
					"（桥在进程加载时读一次配置，相对路径按 dsh 启动目录解析）。\n")
				break
			}
		}
	}
	fmt.Fprintf(w, "完成（%d 份目标；幂等——重跑只补缺）。\n", len(rep.Targets))
	return 0
}

// printTargetReport 逐份结果行（动作/备份/明细如实透传写入器报告）。
func printTargetReport(w io.Writer, r provider.TargetReport) {
	line := fmt.Sprintf("  [%s] %s — %s", r.Name, r.Path, string(r.Action))
	if r.Backup != "" {
		line += "（备份: " + r.Backup + "）"
	}
	fmt.Fprintln(w, line)
	if r.Detail != "" {
		fmt.Fprintf(w, "      %s\n", r.Detail)
	}
}
