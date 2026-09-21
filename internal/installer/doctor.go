// doctor.go — ferryman doctor：一键体检（规格 ferryman/doctor.py 1:1，票20）。
//
// 2026-09-17 两类事故的直接解药——都是"静默失效"，出问题时表面毫无异常：
//   - CC Switch 切换/重写抹掉 settings.json 里的钩子（T21）；
//   - 生成文件时反斜杠转义被写成控制字符（\a→BEL、\f→FF），钩子指向不存在路径。
//
// 检查项全部纯函数化（路径/探针可注入）；runDoctor 聚合并打印 ✓/✗。
//
// Go 新形态：
//   - CheckCCHooks 闸门事件（UserPromptSubmit）缺位 = 提示不失败（C12 用户
//     策略：正文注明"闸门钩子按用户指令未安装"）；其余三事件缺失/路径不
//     存在/SessionStart 超时 <10s 照旧失败；
//   - CheckLauncher 查点火脚本启动行的 exe 路径有效性（Python 时代查
//     venv python.exe，Go 新形态查合并 exe——票22 起脚本为裸形态
//     `"<exe>" serve >> …`）；
//   - CheckCCSwitch / CheckCodex 同款闸门豁免（票22 骑手 M2：子集安装后
//     doctor 全绿）；
//   - HttpBeatSender 功能退化声明（评审附录#14）：信息行输出，不判 FAIL。
//   - CheckUpdateResidues 升级事务残留（本票，规格 §C 第9条）：journal/换装
//     旁路残留 = 上次升级中断现场，提示 `ferryman update` 一键恢复/清理。
//   - 票04（渡口多上游）：CheckDockRewrite 按新语义重构——rewrite_enabled 废弃
//     （迁移后恒缺省，不得再据此判"纯透传"），改写隐含开启，doctor 把 active
//     条目装进"开关显式开"的探针交 dock.ResolveRewrite 单源裁决；新增渡口上游
//     检查组 CheckDockUpstreams（active 可解析/非本地条目 default/缺钥逐条提示/
//     deepseek 官方边界声明）。
package installer

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"ferryman/internal/config"
	"ferryman/internal/dock"
	"ferryman/internal/ferry"
	"ferryman/internal/update"
)

// Check 单检查项结论。
type Check struct {
	OK  bool
	Msg string
}

// CheckStatus 结构化检查项状态（票05 agent 面结构化出口）：pass/fail/
// not_checked 三态。not_checked = 检查目标未装配或按策略跳过——如实标注，
// 绝不伪造通过/失败（CLI 面按非 fail 信息行打印、不计入失败数）。
type CheckStatus string

const (
	StatusPass       CheckStatus = "pass"
	StatusFail       CheckStatus = "fail"
	StatusNotChecked CheckStatus = "not_checked"
)

// CheckResult 结构化检查项三要素（票05）：名称/状态/一句话说明——agent 面
// MCP doctor 工具响应的逐项形状；CLI 面打印同一份计算的 Detail。
type CheckResult struct {
	Name   string      `json:"name"`
	Status CheckStatus `json:"status"`
	Detail string      `json:"detail"`
}

// named Check → CheckResult 换装（name 为稳定检查项名；CLI 面结论语义不变：
// OK→pass、!OK→fail——人面 tag 打印与退出码零漂移）。
func (c Check) named(name string) CheckResult {
	st := StatusFail
	if c.OK {
		st = StatusPass
	}
	return CheckResult{Name: name, Status: st, Detail: c.Msg}
}

// HttpBeatNotice HttpBeatSender 功能退化声明（评审附录#14）：心跳真实发送
// 未实装（Q14 未授权），enforce 模式自动回落 observe 演练。信息行，不计入
// 检查项、不判 FAIL。
const HttpBeatNotice = "[提示] 心跳真实发送未实装（Q14 未授权），enforce 模式自动回落 observe 演练"

// 条目 JSON 里 -File "<path>" 的两种形（json 转义串 / 原文；doctor.py 正则逐字）。
var (
	escPathRe   = regexp.MustCompile(`-File \\"(.*?)\\"`)
	plainPathRe = regexp.MustCompile(`-File "(.*?)"`)
	// startExeRe 点火脚本启动行的 exe 路径（裸形态 EnsureLauncher 产物，票22
	// 骑手1/M6：`"<exe>" serve >> …`——` serve` 尾注保证日志路径不会被误捕）。
	startExeRe = regexp.MustCompile(`"([^"]+)" serve`)
)

// CheckCCHooks settings.json 四事件齐全、路径存在、restore 超时够自举
// （doctor.py check_cc_hooks 逐字 + C12 闸门缺位提示不失败）。Python 形参
// repo 从未参与检查（路径取 settings.json 内嵌绝对路径），Go 面去除。
func CheckCCHooks(settingsPath string) Check {
	if _, err := os.Stat(settingsPath); err != nil {
		return Check{false, fmt.Sprintf("%s 不存在", settingsPath)}
	}
	rawData, err := os.ReadFile(settingsPath)
	if err != nil {
		return Check{false, fmt.Sprintf("%s 不存在", settingsPath)}
	}
	var root map[string]any
	if err := json.Unmarshal(rawData, &root); err != nil {
		return Check{false, fmt.Sprintf("settings.json 解析失败: %v", err)}
	}
	hooks, _ := root["hooks"].(map[string]any)
	var missing []string
	for _, evt := range FerryEvents {
		if !eventHasFerryman(hooks, evt) {
			missing = append(missing, evt)
		}
	}
	gateMissing := false
	var others []string
	for _, m := range missing {
		if m == GateEvent {
			gateMissing = true
		} else {
			others = append(others, m)
		}
	}
	if len(others) > 0 {
		// 文案逐字（missing 列全量——含闸门；闸门缺位只在"仅缺闸门"时放行）
		return Check{false, fmt.Sprintf("settings.json 缺 ferryman 钩子事件: %s"+
			"（CC Switch 切换会抹钩子——跑 install-cc / install-ccswitch）",
			strings.Join(missing, ", "))}
	}
	// 路径存在性 + restore 超时（自举等待预算）
	for _, evt := range FerryEvents {
		for _, entry := range asList(hooks[evt]) {
			entryObj, isObj := entry.(map[string]any)
			if !isObj {
				continue
			}
			blob := marshalCompact(entry)
			if !strings.Contains(blob, "ferryman") {
				continue
			}
			m := escPathRe.FindStringSubmatch(blob)
			if m == nil {
				m = plainPathRe.FindStringSubmatch(blob)
			}
			if m == nil || !pathExists(m[1]) {
				detail := ""
				if m != nil {
					detail = m[1]
				} else {
					detail = truncateRunes(blob, 60)
				}
				return Check{false, fmt.Sprintf("%s: 钩子脚本路径不存在（%s）", evt, detail)}
			}
			if evt == "SessionStart" && firstTimeout(entryObj) < 10 {
				return Check{false, "SessionStart 超时 <10s（含 daemon 自举等待会不够——重跑 install-cc）"}
			}
		}
	}
	if gateMissing {
		// C12 用户策略：闸门钩子按用户指令未安装——提示不失败
		return Check{true, "settings.json 缺闸门钩子 UserPromptSubmit——" +
			"闸门钩子按用户指令未安装（C12：防子代理久跑场景主会话输入被吞），提示不判失败；其余三事件在位"}
	}
	return Check{true, "settings.json 四钩子在位"}
}

// CheckHookScripts BOM 在位（PS5.1 中文注释地雷）+ 无控制字符（\a→BEL / \f→FF
// 事故）（doctor.py check_hook_scripts 逐字节）。
func CheckHookScripts(paths []string) []Check {
	out := []Check{}
	for _, p := range paths {
		name := filepath.Base(p)
		rawData, err := os.ReadFile(p)
		if err != nil {
			out = append(out, Check{false, name + ": 不存在"})
			continue
		}
		if !bytes.HasPrefix(rawData, []byte{0xEF, 0xBB, 0xBF}) {
			out = append(out, Check{false, name + ": 缺 UTF-8 BOM（PS5.1 按 ANSI 解析中文注释会炸）"})
			continue
		}
		var bad []string
		for _, b := range []byte{0x07, 0x08, 0x0B, 0x0C, 0x1B} {
			if bytes.IndexByte(rawData, b) >= 0 {
				bad = append(bad, fmt.Sprintf("'0x%x'", b))
			}
		}
		if len(bad) > 0 {
			out = append(out, Check{false, fmt.Sprintf("%s: 含控制字符 [%s]"+
				"（路径转义事故——重跑 install 修复）", name, strings.Join(bad, ", "))})
			continue
		}
		out = append(out, Check{true, name + ": BOM/无控制字符"})
	}
	return out
}

// CheckCCSwitch 全部 claude 供应商快照都带 ferryman 钩子（切换=逐字写入，
// 缺了就会被抹）（doctor.py check_ccswitch 逐字 + 票22 骑手 M2：闸门事件
// 豁免同 CheckCCHooks——UserPromptSubmit 缺位 = 提示不失败；其余三事件硬性）。
func CheckCCSwitch(dbPath string) Check {
	if _, err := os.Stat(dbPath); err != nil {
		return Check{true, "未装 CC Switch（跳过）"}
	}
	rows, err := readClaudeProviders(dbPath, 5000)
	if err != nil {
		return Check{false, fmt.Sprintf("cc-switch.db 读取失败: %v", err)}
	}
	var lacking []string
	gateMissing := false
	for _, r := range rows {
		others, gate := snapshotMissing(r.Raw)
		if len(others) > 0 {
			lacking = append(lacking, r.Name)
		} else if gate {
			gateMissing = true
		}
	}
	if len(lacking) > 0 {
		return Check{false, fmt.Sprintf("供应商快照缺钩子: %s（重跑 install-ccswitch）",
			strings.Join(lacking, ", "))}
	}
	if gateMissing {
		// C12 用户策略：闸门钩子按用户指令未安装——提示不失败
		return Check{true, fmt.Sprintf("CC Switch %d 个 claude 快照钩子在位"+
			"（缺闸门 UserPromptSubmit——闸门钩子按用户指令未安装，提示不判失败）", len(rows))}
	}
	return Check{true, fmt.Sprintf("CC Switch %d 个 claude 快照全带钩子", len(rows))}
}

// snapshotMissing 单快照缺位清单分两桶（票22 骑手 M2）：others = 硬性三事件
// （SessionStart/SubagentStart/SubagentStop，缺一即该快照需重注入）；gate =
// 闸门 UserPromptSubmit 缺位（C12 豁免）。NULL/坏 JSON/非 dict = 全缺（照旧
// 记缺——该快照需要的正是重注入）。
func snapshotMissing(raw sql.NullString) (others []string, gate bool) {
	var root map[string]any
	if !raw.Valid || json.Unmarshal([]byte(raw.String), &root) != nil {
		root = nil
	}
	hooks, _ := root["hooks"].(map[string]any)
	for _, evt := range FerryEvents {
		if eventHasFerryman(hooks, evt) {
			continue
		}
		if evt == GateEvent {
			gate = true
		} else {
			others = append(others, evt)
		}
	}
	return
}

// CheckCodex Codex：钩子条目在位 + [features] hooks = true（默认关，不开则
// 整包静默失效）（doctor.py check_codex 逐字 + 票22 骑手：M5 读失败与解析
// 失败分开、单次 unmarshal；M2 闸门事件豁免同 CheckCCHooks——缺位 = 提示
// 不失败，其余三事件硬性）。
func CheckCodex(hooksPath, configPath string) Check {
	var problems []string
	gateMissing := false
	if _, err := os.Stat(hooksPath); err != nil {
		problems = append(problems, "hooks.json 不存在（跑 install-codex）")
	} else if rawData, err := os.ReadFile(hooksPath); err != nil {
		// M5：读失败（权限/IO）不是解析失败——文案分开，不再把 nil err 误报成解析错
		problems = append(problems, fmt.Sprintf("hooks.json 读取失败: %v", err))
	} else {
		var root map[string]any
		if err := json.Unmarshal(rawData, &root); err != nil { // M5：单次 unmarshal
			problems = append(problems, fmt.Sprintf("hooks.json 解析失败: %v", err))
		} else {
			hooks, _ := root["hooks"].(map[string]any)
			var others []string
			others, gateMissing = splitGateMissing(hooks)
			if len(others) > 0 {
				// 文案逐字（缺位列全量——含闸门；闸门缺位只在"仅缺闸门"时放行）
				missing := append([]string{}, others...)
				if gateMissing {
					missing = append(missing, GateEvent)
				}
				problems = append(problems, fmt.Sprintf("hooks.json 缺: %s（跑 install-codex）",
					strings.Join(missing, ", ")))
			}
		}
	}
	tomlRaw, _ := os.ReadFile(configPath) // 不存在 = 空（Python 同位）
	if !tomlHasHooksFlag(string(tomlRaw)) {
		problems = append(problems, "[features] hooks = true 未开（Codex 钩子默认关闭）")
	}
	if len(problems) > 0 {
		return Check{false, strings.Join(problems, "；")}
	}
	if gateMissing {
		// C12 用户策略：闸门钩子按用户指令未安装——提示不失败
		return Check{true, "Codex 钩子+旗标在位（缺闸门 UserPromptSubmit——" +
			"闸门钩子按用户指令未安装，提示不判失败）"}
	}
	return Check{true, "Codex 钩子+旗标在位"}
}

// splitGateMissing FerryEvents 缺位清单分两桶（票22 骑手 M2 共享件）：others =
// 硬性事件缺位（闸门除外）；gate = 闸门 UserPromptSubmit 缺位（C12 豁免）。
func splitGateMissing(hooks map[string]any) (others []string, gate bool) {
	for _, evt := range FerryEvents {
		if eventHasFerryman(hooks, evt) {
			continue
		}
		if evt == GateEvent {
			gate = true
		} else {
			others = append(others, evt)
		}
	}
	return
}

// CheckDaemon probe() 返回 /stats dict（活）或 nil（死）（doctor.py check_daemon 逐字）。
func CheckDaemon(probe func() map[string]any, pidFile string) Check {
	st := probe()
	if st == nil {
		return Check{false, "daemon 未运行（钩子自举会拉起，或手动 start-daemon.cmd）"}
	}
	extra := ""
	if rawData, err := os.ReadFile(pidFile); err == nil {
		var pid struct {
			PID int `json:"pid"`
		}
		if json.Unmarshal(rawData, &pid) == nil {
			extra = fmt.Sprintf("（pid %d）", pid.PID)
		}
	}
	alert := " · ok"
	if v, ok := st["health_alert"].(bool); ok && v {
		alert = " · ⚠ 健康告警: 疑似钩子失效"
	}
	return Check{true, "daemon 活着" + extra + alert}
}

// CheckFerryProvider 摆渡 provider 已配置且在 [providers.*] 有定义（T39 去内置
// 默认后的新静默失效点）（doctor.py check_ferry_provider 逐字）。
func CheckFerryProvider(name string, providers map[string]ferry.Provider) Check {
	if name == "" {
		return Check{false, "[ferry] provider 未配置——摆渡永远降级骨架" +
			"（复制 config.example.toml 到 ~/ferryman/config.toml）"}
	}
	if _, ok := providers[name]; !ok {
		return Check{false, fmt.Sprintf("[ferry] provider '%s' 未在 [providers.*] 定义", name)}
	}
	return Check{true, fmt.Sprintf("摆渡 provider '%s' 在位", name)}
}

// CheckDockRewrite 渡口改写守卫体检（票04 按票01 新语义重构）：判定单源在
// dock.ResolveRewrite——doctor 与 daemon 构造期读同一函数，绝不出现两套判据。
// rewrite_enabled 已废弃（迁移后的新配置该字段恒缺省，不得再据此判"纯透传"
// ——旧实现会误报）；改写隐含开启，doctor 把 active 条目（ActiveUpstream 单源）
// 装进"开关显式开"的探针配置交守卫裁决，结论按成因三分：
//   - 守卫放行 ＝ 改写模式在位；
//   - 上游为本地中转地址（回环＋15721/15722/15723，cc-switch 回退通道）＝ 守卫
//     强制透传防双重改写——设计内，不判失败；
//   - 非本地上游未进改写（缺 default）＝ 表形态下配置错误，FAIL 带守卫告警
//     文案；旧单值未迁移形态属合法回退透传态，提示迁移不判失败。
func CheckDockRewrite(dockCfg *config.DockCfg) Check {
	if dockCfg == nil {
		return Check{true, "渡口未配置（[dock] 节缺失，零行为）"}
	}
	name, up := dockCfg.ActiveUpstream()
	if up == nil {
		return Check{false, fmt.Sprintf("[dock].active %q 未指向上游表中的任何条目"+
			"——透传/改写模式未判定（见渡口上游检查）", dockCfg.Active)}
	}
	probe := &config.DockCfg{
		RewriteEnabled:  true, // 显式开关内核：新语义改写隐含开启（D13/D15 无开关）
		UpstreamBaseURL: up.BaseURL,
		ModelMap:        up.ModelMap,
		TextOnly:        up.TextOnly,
	}
	_, ok, reason := dock.ResolveRewrite(probe)
	if ok {
		label := name
		if label == "" {
			label = "旧单值" // 无表兜底（未迁移/迁移失败回退，serve 横幅同款标签）
		}
		return Check{true, fmt.Sprintf("渡口改写模式在位（active=%s；default 键在、上游非本地中转）", label)}
	}
	if config.IsLocalRelayAddr(up.BaseURL) {
		return Check{true, "渡口纯透传（上游为本地中转地址，守卫强制透传防双重改写——设计内）"}
	}
	if len(dockCfg.Upstreams) == 0 {
		return Check{true, "渡口纯透传（旧单值配置未迁移，无 model_map——首启迁移后由守卫按条目裁决）"}
	}
	return Check{false, "渡口改写模式未生效: " + reason}
}

// DeepSeekBoundaryNotice DeepSeek 官方已知边界声明（票04，review block F2 收敛
// 口径；docs/ 多上游使用说明同文照录）：激活 deepseek 条目时 doctor 输出一行。
// 信息行不判 FAIL——遇不支持负载渡口如实透传上游错误（不做协议转换，D5），
// 不是本实现的故障。
const DeepSeekBoundaryNotice = "DeepSeek 官方已知边界（上游侧限制，非本实现故障）: " +
	"不支持消息类型 document、search_result、redacted_thinking、mcp_tool_use、mcp_tool_result；" +
	"忽略语义 tool_result.is_error、tool_choice.disable_parallel_tool_use、thinking.budget_tokens；" +
	"遇不支持负载如实透传上游错误（不做协议转换），处置＝人手换上游（ferryman upstream use <其他条目>）"

// CheckDockUpstreams 渡口上游检查组（票04）。返回的 CheckResult 自带稳定名：
//   - dock_upstream——主判：表形态下 active 存在且可解析（缺 active/悬空＝FAIL，
//     点名可用条目）、非本地条目 model_map 含非空 default（本地中转地址条目＝
//     守卫透传域豁免——与 config.validateDockUpstreams 同判据域）；旧单值形态
//     （未迁移/迁移失败回退）＝合法回退态，提示迁移反复失败的异形排查面，不判
//     失败（serve 侧迁移失败有告警，doctor 只读不迁移）；
//   - dock_upstream_rewrite_hint——迁移行为变化提示（评审低危备注）：active 为
//     迁移出的 cc-switch 条目且上游非本地＝升级后改写已隐含开启（旧
//     rewrite_enabled=false 的纯透传不再保留）；
//   - dock_upstream_key:<条目名>——缺 api_key 条目逐条提示"未配置（手编 config
//     填 api_key）"；预置未激活属正常，提示不判 FAIL。本地中转地址条目豁免
//     （守卫强制透传不出站鉴权，cc-switch 回退通道常态无钥）；
//   - dock_deepseek_boundary——active 为 deepseek（预置名或端点命中）时输出
//     DeepSeekBoundaryNotice。
func CheckDockUpstreams(dockCfg *config.DockCfg) []CheckResult {
	out := []CheckResult{}
	if len(dockCfg.Upstreams) == 0 {
		// 旧单值形态：ActiveUpstream 兜底包装仍在服务（升级当天零行为变化）。
		out = append(out, CheckResult{Name: "dock_upstream", Status: StatusPass,
			Detail: "渡口上游为旧单值形态（未迁移出上游表）——旧行为兜底继续服务" +
				"（守护重启触发首启迁移；迁移反复失败查 serve 日志，" +
				"检查 config [dock] 节是否含空值键等异形）"})
		name, up := dockCfg.ActiveUpstream()
		if up != nil && isDeepSeekUpstream(name, up.BaseURL) {
			out = append(out, CheckResult{Name: "dock_deepseek_boundary", Status: StatusPass,
				Detail: DeepSeekBoundaryNotice})
		}
		return out
	}
	// 表形态主判（缺 active/悬空/缺 base_url/非本地缺 default，问题列表确定性）
	var probs []string
	if dockCfg.Active == "" {
		probs = append(probs, "已配上游表但缺 active（单选键，须指向一条渡口上游）")
	} else if _, ok := dockCfg.Upstreams[dockCfg.Active]; !ok {
		probs = append(probs, fmt.Sprintf("[dock].active 指向不存在的条目 %s（可用: %s）",
			dockCfg.Active, strings.Join(sortedUpstreamNames(dockCfg), ", ")))
	}
	var noDefault []string
	for _, n := range sortedUpstreamNames(dockCfg) {
		up := dockCfg.Upstreams[n]
		if strings.TrimSpace(up.BaseURL) == "" {
			probs = append(probs, fmt.Sprintf("[dock.upstreams.%s] 缺 base_url（必填）", n))
			continue
		}
		if !config.IsLocalRelayAddr(up.BaseURL) && up.ModelMap["default"] == "" {
			noDefault = append(noDefault, n)
		}
	}
	if len(noDefault) > 0 {
		probs = append(probs, "非本地条目 model_map 缺非空 default: "+strings.Join(noDefault, ", "))
	}
	if len(probs) > 0 {
		out = append(out, CheckResult{Name: "dock_upstream", Status: StatusFail,
			Detail: "渡口上游配置有问题: " + strings.Join(probs, "；")})
	} else {
		up := dockCfg.Upstreams[dockCfg.Active] // 上面已验证存在
		out = append(out, CheckResult{Name: "dock_upstream", Status: StatusPass,
			Detail: fmt.Sprintf("渡口上游 active=%s 在位（%s；模式由守卫按上游地址裁决）",
				dockCfg.Active, up.BaseURL)})
	}
	// 迁移行为变化提示（active=迁移条目且上游非本地）
	if up, ok := dockCfg.Upstreams[dockCfg.Active]; ok && dockCfg.Active == "cc-switch" &&
		!config.IsLocalRelayAddr(up.BaseURL) {
		out = append(out, CheckResult{Name: "dock_upstream_rewrite_hint", Status: StatusPass,
			Detail: "渡口上游 cc-switch 迁移自旧单值且上游非本地：升级后改写已隐含开启" +
				"（旧 rewrite_enabled=false 的纯透传不再保留）——行为变化提示"})
	}
	// 缺 api_key 逐条提示（本地透传域豁免）
	for _, n := range sortedUpstreamNames(dockCfg) {
		up := dockCfg.Upstreams[n]
		if up.APIKey == "" && !config.IsLocalRelayAddr(up.BaseURL) {
			out = append(out, CheckResult{Name: "dock_upstream_key:" + n, Status: StatusPass,
				Detail: fmt.Sprintf("渡口上游 %s 未配置（手编 config 填 api_key）", n)})
		}
	}
	// DeepSeek 官方边界声明
	if up, ok := dockCfg.Upstreams[dockCfg.Active]; ok && isDeepSeekUpstream(dockCfg.Active, up.BaseURL) {
		out = append(out, CheckResult{Name: "dock_deepseek_boundary", Status: StatusPass,
			Detail: DeepSeekBoundaryNotice})
	}
	return out
}

// isDeepSeekUpstream active 条目是否 DeepSeek：预置名 deepseek 或端点命中
// deepseek（手改名条目按端点识别）。
func isDeepSeekUpstream(name, baseURL string) bool {
	return name == "deepseek" || strings.Contains(baseURL, "deepseek")
}

// sortedUpstreamNames 条目名排序（map 迭代无序——检查输出面必须确定）。
func sortedUpstreamNames(d *config.DockCfg) []string {
	names := make([]string, 0, len(d.Upstreams))
	for k := range d.Upstreams {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// CheckAutostart Run 键自启三态（票02）：installed=在位；missing/mismatch=
// 失败并给修法（登录自启是常驻保障第 1 腿，缺位与钩子缺失同级）。
func CheckAutostart(f func() (autostartStatus, error)) Check {
	st, err := f()
	if err != nil {
		return Check{false, fmt.Sprintf("Run 键自启状态读取失败: %v", err)}
	}
	switch st {
	case autostartInstalled:
		return Check{true, "Run 键自启在位（HKCU Run\\Ferryman）"}
	case autostartMismatch:
		return Check{false, "Run 键自启值不符（exe 挪窝或手改——重跑 ferryman autostart install）"}
	default:
		return Check{false, "Run 键自启缺失（跑 ferryman autostart install）"}
	}
}

// CheckWatchdogTask 看门计划任务两态（票02）：在位含下次运行时间（解析不出
// 只附注不扣分——存在性才是承重信息）；缺失=失败并给修法。
func CheckWatchdogTask(f func() (TaskStatus, error)) Check {
	st, err := f()
	if err != nil {
		return Check{false, fmt.Sprintf("看门计划任务查询失败: %v", err)}
	}
	if !st.Exists {
		return Check{false, "看门计划任务缺失（跑 ferryman watchdog install）"}
	}
	if st.NextRun == "" {
		return Check{true, "看门计划任务在位（下次运行时间解析不出/未排）"}
	}
	return Check{true, fmt.Sprintf("看门计划任务在位（下次运行: %s）", st.NextRun)}
}

// CheckMCPRegistration "MCP 注册在位"（票06）：用户级 MCP 配置（与
// install-mcp 同 scope：<home>/.claude.json）里 mcpServers.ferryman 条目在位
// 且形态自洽。判定复用 classifyMCPEntry（F6 ① 单源——install-mcp 的覆盖
// 豁免与 doctor 的在位认定是同一套形状匹配，绝不两套判据）。CC 配置被外部
// 工具重写抹掉注册时由此暴露（静默失效同族）。
func CheckMCPRegistration(configPath string) Check {
	rawData, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Check{false, fmt.Sprintf("MCP 注册不在位（%s 不存在——跑 ferryman install-mcp）",
				configPath)}
		}
		return Check{false, fmt.Sprintf("%s 读取失败: %v", configPath, err)}
	}
	var root map[string]any
	if err := json.Unmarshal(rawData, &root); err != nil {
		return Check{false, fmt.Sprintf(".claude.json 解析失败: %v（MCP 注册状态未知）", err)}
	}
	servers, ok := root["mcpServers"].(map[string]any)
	if root["mcpServers"] != nil && !ok {
		return Check{false, "MCP 注册不在位（mcpServers 非对象——配置异常，人工核）"}
	}
	if !ok {
		return Check{false, "MCP 注册不在位（无 mcpServers.ferryman——跑 ferryman install-mcp）"}
	}
	entry, isObj := servers[mcpServerKey].(map[string]any)
	if servers[mcpServerKey] == nil || !isObj {
		return Check{false, "MCP 注册不在位（mcpServers.ferryman 缺失或非对象——跑 ferryman install-mcp）"}
	}
	if own, reason := classifyMCPEntry(entry); !own {
		return Check{false, fmt.Sprintf("mcpServers.ferryman 条目非 Ferryman 自建形态（%s；%s）"+
			"——install-mcp 默认拒绝覆盖，--force 可强制", reason, maskedMCPSummary(entry))}
	}
	cmd, _ := entry["command"].(string)
	return Check{true, fmt.Sprintf("MCP 注册在位（用户级 mcpServers.ferryman → %q mcp）", cmd)}
}

// 升级事务残留命名约定（本票，规格 §C 第9条）：与 internal/update 落地对齐
// ——journal.go 的 journalName（数据目录在册账）与 saveJournal 的半写临时
// （journalPath+".tmp"）；swap.go cleanSwapResidues 的清扫域（.new/.new.part/
// .swap-tmp*）。update 侧常量未导出、本票涉及路径不含该包——同名同值落此，
// 注释即对齐来源（两包测试各自锁定字面量/行为，漂移双双报红）。.old-* 备份
// 不在残留域：D9 安全底线里备份留 2 份是回滚保障，不是垃圾。
const (
	journalName        = "update-journal.json"     // update.journalName 同名同值
	journalHalfWritten = "update-journal.json.tmp" // update.saveJournal 半写临时
)

// updateResiduePatterns exe 旁换装残留清扫域（update.cleanSwapResidues 同域）。
var updateResiduePatterns = []string{"ferryman.exe.new", "ferryman.exe.new.part", "ferryman.exe.swap-tmp*", "ferryman.exe.supervisor-copy*"}

// CheckUpdateResidues 升级事务残留检查（本票，规格 §C 第9条崩溃恢复，review
// block F4 配套检测）：journal 在册/半写 + 换装目标 exe 旁 .new/.swap-tmp 残留
// ——上次升级中断的现场痕迹（出问题时表面毫无异常的静默形态同族）。处置
// 一行直达：`ferryman update` 启动即读 journal 自动恢复/清理（staging 清残留
// 续跑；swap/verify 健康清账、不健康按备份回滚）；极端缺位（exe 已不在、仅剩
// swap-tmp）人工把 swap-tmp 改回正式 exe。
func CheckUpdateResidues(dataDir, exeDir string) Check {
	var found []string
	for _, n := range []string{journalName, journalHalfWritten} {
		if pathExists(filepath.Join(dataDir, n)) {
			found = append(found, filepath.Join(dataDir, n))
		}
	}
	for _, pat := range updateResiduePatterns {
		matches, err := filepath.Glob(filepath.Join(exeDir, pat))
		if err != nil {
			continue
		}
		found = append(found, matches...)
	}
	if len(found) == 0 {
		return Check{true, "无升级事务残留（journal/.new/.swap-tmp 皆净）"}
	}
	return Check{false, fmt.Sprintf("发现升级事务残留: %s（上次升级中断——运行 ferryman update 自动恢复/清理；"+
		"极端缺位 exe 不在时把 ferryman.exe.swap-tmp 改回 ferryman.exe）", strings.Join(found, ", "))}
}

// CheckLauncher 点火脚本在位且其 exe 路径有效（钩子自举的地基；doctor.py
// check_launcher 的 Go 新形态：脚本内启动行的 exe 路径存在）。
func CheckLauncher(path string) Check {
	if _, err := os.Stat(path); err != nil {
		return Check{false, fmt.Sprintf("%s 不存在（重跑 install-cc 生成点火脚本）", path)}
	}
	rawData, err := os.ReadFile(path)
	if err != nil {
		rawData = nil
	}
	if m := startExeRe.FindStringSubmatch(string(rawData)); m != nil {
		if _, err := os.Stat(m[1]); err != nil {
			return Check{false, fmt.Sprintf("点火脚本 exe 路径失效: %s", m[1])}
		}
	}
	return Check{true, LauncherName + " 在位"}
}

// doctorDeps runDoctor 的可注入面（测试密闭；真实入口 RunDoctor 装配真值）。
type doctorDeps struct {
	Home, Repo              string
	CCSwitchDB              string
	CodexHooks, CodexConfig string
	LoadCfg                 func() (*config.Config, error)
	LoadProviders           func() (map[string]ferry.Provider, error)
	Probe                   func() map[string]any
	// 票02：常驻保障两查（Run 键三态 + 看门任务在位/缺失）。
	Autostart    func() (autostartStatus, error)
	WatchdogTask func() (TaskStatus, error)
	// Version 版本行（票02，规格 §A）：cmd/ferryman 的 main.version 经装配
	// 参数传入（internal 包不 import cmd——显式传参不做全局单例）；空串/dev
	// 都按「非 release 构建」呈现。
	Version string
	Out     io.Writer
}

// HomeDir / RepoRoot 目标解析导出面（票05：agent 面 MCP doctor 经此取 HOME/
// exe 面目标——与 CLI RunDoctor 同缝；repoRoot 是包级测试缝，wrapper 透传
// 即测试可整体替换）。
func HomeDir() string { return homeDir() }

// RepoRoot 见 HomeDir 注（同一导出面）。
func RepoRoot() string { return repoRoot() }

// RunDoctor 一键体检真实入口（HOME/exe 面）；返回进程退出码（有 FAIL → 1）。
// version 版本号经装配参数传入（cmd/ferryman 的 main.version——票02，规格 §A）。
func RunDoctor(version string) int {
	home := homeDir()
	// 票05：daemon 活性目标经 config 解析（config.Load 优先级：显式参数 >
	// FERRYMAN_CONFIG > 默认路径）；加载失败回落内置默认口（探针目标与既有
	// 行为同位），配置坏本身由体检项如实报出。
	cfg, cfgErr := config.Load("", false)
	port := config.Default().Server.Port
	if cfgErr == nil {
		port = cfg.Server.Port
	}
	return runDoctor(doctorDeps{
		Home:        home,
		Repo:        repoRoot(),
		CCSwitchDB:  CCSwitchDBPath(home),
		CodexHooks:  CodexHooksPath(home),
		CodexConfig: CodexConfigPath(home),
		LoadCfg:     func() (*config.Config, error) { return cfg, cfgErr },
		LoadProviders: func() (map[string]ferry.Provider, error) {
			return ferry.LoadProviders("")
		},
		Probe: realStatsProbe(filepath.Join(home, "ferryman"), port),
		// 票02：常驻保障两查真探测（只读注册表 / schtasks /Query，无写副作用）
		Autostart:    func() (autostartStatus, error) { return autostartStatusOf(realAutostartDeps()) },
		WatchdogTask: func() (TaskStatus, error) { return queryTask(realTaskDeps()) },
		Version:      version,
		Out:          os.Stdout,
	})
}

// doctorScriptNames 体检的钩子脚本清单（doctor.py run_doctor scripts 逐字）。
func doctorScriptNames() []string {
	return []string{
		"ferryman-gate.ps1", "ferryman-restore.ps1", "ferryman-subagent.ps1",
		"ferryman-ensure.ps1", "ferryman-gate-codex.ps1",
		"ferryman-restore-codex.ps1", "ferryman-subagent-codex.ps1"}
}

// doctorResults 全项结构化体检（票05）：CLI 面 runDoctor 与 agent 面 MCP
// doctor 工具共用同一份计算（公式单源——CONTEXT.md 词条：同一检查全仓只许
// 一份实现）；项目名称稳定、顺序＝CLI 打印序。deps 检查字段 nil＝该项目标
// 未装配 → 显式 not_checked（如实标注，绝不伪造通过/失败）。纯只读：不打印、
// 不写盘、不拉起 daemon；每次调用独立重算（无缓存）。
func doctorResults(d doctorDeps) []CheckResult {
	dataDir := filepath.Join(d.Home, "ferryman")
	out := []CheckResult{}
	out = append(out, CheckCCHooks(filepath.Join(d.Home, ".claude", "settings.json")).named("cc_hooks"))
	out = append(out, CheckLauncher(filepath.Join(dataDir, LauncherName)).named("launcher"))
	out = append(out, CheckCCSwitch(d.CCSwitchDB).named("ccswitch_snapshots"))
	// 配置坏要让 doctor 报出来而非崩（Python try/except 同形）
	cfg, err := d.LoadCfg()
	if err != nil {
		out = append(out, Check{false, fmt.Sprintf("摆渡配置加载失败: %v", err)}.named("ferry_provider"))
	} else if providers, err := d.LoadProviders(); err != nil {
		out = append(out, Check{false, fmt.Sprintf("摆渡配置加载失败: %v", err)}.named("ferry_provider"))
	} else {
		out = append(out, CheckFerryProvider(cfg.FerryProvider, providers).named("ferry_provider"))
		// 票06：渡口配置了才查（无 [dock] 的存量用户零新增检查行）
		if cfg.Dock != nil {
			out = append(out, CheckDockRewrite(cfg.Dock).named("dock_rewrite"))
			// 票04：渡口上游检查组（CheckResult 自带名：dock_upstream /
			// dock_upstream_rewrite_hint / dock_upstream_key:<条目名> /
			// dock_deepseek_boundary——主判紧随 dock_rewrite）。
			out = append(out, CheckDockUpstreams(cfg.Dock)...)
		}
	}

	scripts := []string{}
	for _, n := range doctorScriptNames() {
		scripts = append(scripts, filepath.Join(d.Repo, "hooks", n))
	}
	for i, c := range CheckHookScripts(scripts) {
		out = append(out, c.named("hook_script:"+filepath.Base(scripts[i])))
	}
	out = append(out, CheckCodex(d.CodexHooks, d.CodexConfig).named("codex_hooks"))
	out = append(out, CheckDaemon(d.Probe, filepath.Join(dataDir, "daemon.pid")).named("daemon_liveness"))
	// 票02 常驻保障两查：deps 未装配（agent 面测试密闭形态）→ not_checked；
	// CLI 面恒装配，人面输出不变。
	if d.Autostart == nil {
		out = append(out, CheckResult{Name: "autostart", Status: StatusNotChecked,
			Detail: "Run 键自启未检查（检查目标未装配——如实标注不伪造）"})
	} else {
		out = append(out, CheckAutostart(d.Autostart).named("autostart"))
	}
	if d.WatchdogTask == nil {
		out = append(out, CheckResult{Name: "watchdog_task", Status: StatusNotChecked,
			Detail: "看门计划任务未检查（检查目标未装配——如实标注不伪造）"})
	} else {
		out = append(out, CheckWatchdogTask(d.WatchdogTask).named("watchdog_task"))
	}
	// 票06：MCP 注册在位（用户级 .claude.json——与 install-mcp 同 scope）。
	// 追加在末位：既有检查项的顺序零漂移，CLI 人面仅多一行（预期行为）。
	out = append(out, CheckMCPRegistration(UserMCPConfigPath(d.Home)).named("mcp_registration"))
	// 升级链票06：升级事务残留（规格 §C 第9条）续接末位。残留物落点两处：
	// journal 在数据目录；.new/.swap-tmp 在换装目标 exe 旁——换装目标与
	// update 同缝解析（seam E：点火脚本引号 exe 优先，任何失败回落本进程
	// 映像 update.ResolveSwapTarget），绝不两套判据。
	exeDir := dataDir
	if targetExe, err := update.ResolveSwapTarget(filepath.Join(dataDir, LauncherName)); err == nil {
		exeDir = filepath.Dir(targetExe)
	}
	out = append(out, CheckUpdateResidues(dataDir, exeDir).named("update_residues"))
	return out
}

// DoctorStructured 票05：结构化体检导出入口——agent 面 MCP doctor 工具进程内
// 复用（D6：不经 HTTP）；与 CLI runDoctor 同一套检查函数与聚合序（公式单源）。
//
// 目标解析面（测试传临时目录/临时 config，绝不读真实用户目录）：
//   - home/repo：用户目录与仓库根相关检查的目标（同 RunDoctor 的 HOME/exe 面）；
//   - cfg：已解析配置（config.Load 优先级的产物；daemon 活性目标 =
//     cfg.DataDir()/cfg.Server.Port，本函数不二次解析）；非 nil 契约；
//   - cfgPath：providers 解析路径（"" = 默认路径 ~/ferryman/config.toml——与
//     CLI doctor 同位的既有行为）；测试传临时 config；
//   - residency：常驻保障两查（Run 键自启＋看门任务）——true 走真探测（只读
//     注册表 / schtasks /Query，无写副作用）；false 两项显式 not_checked
//     （零子进程、零注册表读——测试密闭形态）。
//
// 纯只读：不打印、不写盘、不拉起 daemon；每次调用独立重算（无缓存）。
func DoctorStructured(home, repo string, cfg *config.Config, cfgPath string, residency bool) []CheckResult {
	d := doctorDeps{
		Home:        home,
		Repo:        repo,
		CCSwitchDB:  CCSwitchDBPath(home),
		CodexHooks:  CodexHooksPath(home),
		CodexConfig: CodexConfigPath(home),
		LoadCfg:     func() (*config.Config, error) { return cfg, nil },
		LoadProviders: func() (map[string]ferry.Provider, error) {
			return ferry.LoadProviders(cfgPath)
		},
		Probe: realStatsProbe(cfg.DataDir(), cfg.Server.Port),
	}
	if residency {
		d.Autostart = func() (autostartStatus, error) { return autostartStatusOf(realAutostartDeps()) }
		d.WatchdogTask = func() (TaskStatus, error) { return queryTask(realTaskDeps()) }
	}
	return doctorResults(d)
}

// runDoctor 聚合检查并打印（doctor.py run_doctor 逐字 + 附录#14 声明行）。
// 票05 起结论计算单源 doctorResults——本函数只负责人面打印与退出码，格式
// 与拆分前逐字一致（tag/空行/声明行/结论行）。
func runDoctor(d doctorDeps) int {
	results := doctorResults(d)
	fails := 0
	for _, r := range results {
		tag := "[FAIL] "
		if r.Status != StatusFail {
			tag = "[OK]   "
		}
		fmt.Fprintln(d.Out, tag+r.Detail)
		if r.Status == StatusFail {
			fails++
		}
	}
	fmt.Fprintln(d.Out)
	fmt.Fprintln(d.Out, HttpBeatNotice) // 附录#14：功能退化声明——信息行不判 FAIL
	// 版本行（票02，规格 §A）：结论清单里报当前版本——信息行不计检查项；
	// dev/未装配都显「非 release 构建」（与 `ferryman version` 的提示同款）。
	if d.Version == "" || d.Version == "dev" {
		fmt.Fprintln(d.Out, "版本: dev（非 release 构建）")
	} else {
		fmt.Fprintln(d.Out, "版本: "+d.Version)
	}
	conclusion := fmt.Sprintf("体检结论: %d/%d 通过", len(results)-fails, len(results))
	if fails > 0 {
		conclusion += "——有问题见上"
	}
	fmt.Fprintln(d.Out, conclusion)
	if fails > 0 {
		return 1
	}
	return 0
}

// realStatsProbe /stats 探针：Bearer token 读 data_dir，2s 超时；任何失败 → nil
// （doctor.py real_probe 逐字；端口票05 起由调用方经 config 解析传入——同
// internal/config 优先级，默认 7311 与 Python 硬编码同位）。
func realStatsProbe(dataDir string, port int) func() map[string]any {
	return func() map[string]any {
		tokenRaw, err := os.ReadFile(filepath.Join(dataDir, "daemon.token"))
		if err != nil {
			return nil
		}
		req, err := http.NewRequest(http.MethodGet,
			fmt.Sprintf("http://127.0.0.1:%d/stats", port), nil)
		if err != nil {
			return nil
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tokenRaw)))
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return nil
		}
		var st map[string]any
		if json.Unmarshal(data, &st) != nil {
			return nil
		}
		return st
	}
}

// ---- 检查器共享小件 ----

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// eventHasFerryman 事件条目数组里任一 JSON 含 "ferryman"（Python any(...) 逐字）。
func eventHasFerryman(hooks map[string]any, evt string) bool {
	for _, e := range asList(hooks[evt]) {
		if strings.Contains(marshalCompact(e), "ferryman") {
			return true
		}
	}
	return false
}

func asList(v any) []any {
	lst, _ := v.([]any)
	return lst
}

// firstTimeout entry["hooks"][0]["timeout"]（Python entry.get("hooks",[{}])[0]
// .get("timeout",0) 同位；形状不齐 → 0）。
func firstTimeout(entry map[string]any) int {
	inner := asList(entry["hooks"])
	if len(inner) == 0 {
		return 0
	}
	hook, _ := inner[0].(map[string]any)
	if v, ok := hook["timeout"].(float64); ok {
		return int(v)
	}
	return 0
}

// truncateRunes 前 n 个码点（Python blob[:60] 同为码点切）。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
