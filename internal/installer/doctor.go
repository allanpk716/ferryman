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
//   - 服务商接管票05（2026-09-30 夜链）：新三项 provider_cc_dock /
//     provider_codex_dock / provider_orca_codex——CC 指向渡口、codex 两份指向
//     渡口且 wire_api=responses 且 hooks 旗标在位、orca codex 健康（配置存在/
//     指向渡口/认证形态合法）。判定单源 internal/provider（与接管写入器同一套
//     解析）；[dock] 未配置 → 三项显式 not_checked（清单 23→26 的计数同步见
//     doctor_test 的结论行公式）。
//   - 票11（pi 生效链）：第四项 provider_pi_dock——绿=完整生效链（settings.json
//     的 defaultProvider 解析到渡口条目 ∧ defaultModel ∈ 该条目 models ∧
//     api=anthropic-messages ∧ baseUrl=渡口根地址）；~/.pi 未装 → not_checked
//     不产红；残留旧 15721 条目但生效链正确 → 绿+警告（F9，非生效残留不阻断）。
//     结论清单计数再 +1（全绿计数 26→27，见 doctor_test 的结论行公式）。
//   - verify-dsh 票06（2026-10-08 夜链，DSH 插件验证吸收面，D6/D10）：
//     dsh_plugin_static——internal/dshverify L0 静态检查汇总（三 profile 安装面
//     ＋生产配置面＋版本可读，纯读零副作用；~/.dsh 未装或 [dock] 未配置 =
//     not_checked 不产红）＋ ferryman-gate-dsh.ps1 补进脚本清单（七→八）＋
//     版本黄灯提示（当前 DSH 版本不在 dshledger 判定流水 → detail 挂黄提示行，
//     黄只提示不告警不判失败）。协调注：与并行链 dsh_poller_sentinel（dsh-host
//     -guard 泳道，宿主插件哨兵）互补不重复——本项管安装面静态完整性，彼项管
//     挂载活性；本票不加任何 poll/活性检查项（原 dsh_poll_age 已撤销，避免同
//     能力两份实现）。全绿计数 27→29（+1=脚本清单七→八，+1=dsh_plugin_static；
//     见 doctor_test 的结论行公式）。
package installer

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"ferryman/internal/config"
	"ferryman/internal/dock"
	"ferryman/internal/dshledger"
	"ferryman/internal/dshverify"
	"ferryman/internal/ferry"
	"ferryman/internal/jsonl"
	"ferryman/internal/prices"
	"ferryman/internal/provider"
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

// HttpBeatNotice 心跳能力声明（2026-09-29 修订文案：旧文"未实装/Q14 未授权"
// 已与现实不符——Q14 早已通过、HttpBeatSender 已实装接线；实际发跳由
// [heartbeat].enabled 与等待窗 opt-in 控制）。信息行，不计入检查项、不判 FAIL。
const HttpBeatNotice = "[提示] 心跳真发送已实装（Q14 已过）；实际发跳由 [heartbeat].enabled 与等待窗 opt-in 控制"

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

// CCSwitchDeprecationRoute 弃用路线注记（票13，D15）：ccswitch_snapshots 检查
// 项处于退役路径——cc-switch 替换完成后，此项随 cc-switch 卸载一并移除；在那
// 之前检查逻辑保留（机上还有 cc-switch 时照常体检），逐条文案尾挂注记。
const CCSwitchDeprecationRoute = "（弃用路线：cc-switch 卸载后此项随卸载移除）"

// CheckCCSwitch 全部 claude 供应商快照都带 ferryman 钩子（切换=逐字写入，
// 缺了就会被抹）（doctor.py check_ccswitch 逐字 + 票22 骑手 M2：闸门事件
// 豁免同 CheckCCHooks——UserPromptSubmit 缺位 = 提示不失败；其余三事件硬性）。
// 票13：检查逻辑不删（机上仍有 cc-switch 时仍有用），各出口文案尾加弃用路线
// 注记 CCSwitchDeprecationRoute。
func CheckCCSwitch(dbPath string) Check {
	if _, err := os.Stat(dbPath); err != nil {
		return Check{true, "未装 CC Switch（跳过）" + CCSwitchDeprecationRoute}
	}
	rows, err := readClaudeProviders(dbPath, 5000)
	if err != nil {
		return Check{false, fmt.Sprintf("cc-switch.db 读取失败: %v", err) + CCSwitchDeprecationRoute}
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
		return Check{false, fmt.Sprintf("供应商快照缺钩子: %s（重跑 install-ccswitch）%s",
			strings.Join(lacking, ", "), CCSwitchDeprecationRoute)}
	}
	if gateMissing {
		// C12 用户策略：闸门钩子按用户指令未安装——提示不失败
		return Check{true, fmt.Sprintf("CC Switch %d 个 claude 快照钩子在位"+
			"（缺闸门 UserPromptSubmit——闸门钩子按用户指令未安装，提示不判失败）%s",
			len(rows), CCSwitchDeprecationRoute)}
	}
	return Check{true, fmt.Sprintf("CC Switch %d 个 claude 快照全带钩子%s",
		len(rows), CCSwitchDeprecationRoute)}
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

// CheckDaemon daemon 活性判定（dsh-host-guard 票04 修正：监听事实优先）。
//
// 现行判据（读码结论，2026-10-08）：修正前＝/stats 探针单源——probe() 即
// realStatsProbe（读 <dataDir>/daemon.token → GET 127.0.0.1:<port>/stats，2s
// 超时），返回 nil 即判「daemon 未运行」；pid 文件只进文案、从不进判定——
// 「pid 文件陈旧误配致误判」候选经读码排除。probe 的 nil 把四种互异成因压成
// 一个布尔：①token 文件读不到；②拨号失败（口拒绝=目标口不对/守护不在；2s
// 超时=守护忙，Health 侧锁面+账本扫描）；③应答非 JSON；④读体失败。
//
// 误报成因（2026-10-08 16:0x 实测：daemon 15700/15722 双口 LISTENING、台账
// 持续更新，MCP 面 doctor 仍报 fail——即 probe() nil 而守护事实活着；具体踩中
// ①~④ 哪条已不可事后分辨，probe 不留成因痕迹）。候选归因（推理，非实测）：
// MCP 进程在自身启动时经 config.Load 解析一次并冻结探针目标（internal/mcp
// Run→New→defaultDoctorFunc）——守护换口/换数据目录后，长命 MCP 进程仍探旧
// 目标即恒 nil（7311→15700 迁移有「旧会话 MCP 钉死旧口」先例）；守护忙时 2s
// 超时为同症状次候选。
//
// 修正（spec G「监听事实优先」）：probe nil 时对控制口（cfg.Server.Port，只绑
// 127.0.0.1）做 TCP 拨号见证——口在听即 pass（监听＝事实活着，/stats 存取级
// 失败只作注记不作死刑）；口也不听才判死。判死文案对两类形态各归其位、不谎报：
//   - pid 文件在而口无监听＝残留（进程死）或启动窗/半死（进程在端口未就绪；
//     排水窗「监听口先关、进程后走」为在库实测先例，v0.1.1 演练）——doctor
//     座位两者不可分辨，点名形态、同判死：不因残留误 pass，不因启动窗误 pass；
//   - 见证未装配（dial nil 或目标口未解析）＝探针单源旧判（配置坏由
//     ferry_provider 项如实报，此处不二次归因）。
//
// 已知既有误报面（读码发现）：realStatsProbe 曾不看 HTTP 状态码——401 的
// {"error":"unauthorized"} 是合法 JSON，probe 返回非 nil，误 pass 方向（假
// 活）；2026-10-09 晨报后续票01 已修（非 200 → nil），与本案误 fail 反向的
// 假活面就此关闭。
func CheckDaemon(probe func() map[string]any, pidFile string,
	dial func(addr string, timeout time.Duration) error, dialAddr string) Check {
	st := probe()
	pidNote := ""
	if rawData, err := os.ReadFile(pidFile); err == nil {
		var pid struct {
			PID int `json:"pid"`
		}
		if json.Unmarshal(rawData, &pid) == nil {
			pidNote = fmt.Sprintf("pid %d", pid.PID)
		}
	}
	if st == nil {
		// 票04「监听事实优先」：探针 nil 先问控制口——口在听即守护事实活着
		//（控制口排他绑定 127.0.0.1，internal/daemon ListenAndServe）；拨号
		// 超时同渡口监听检查（dockListenTimeout，同款 2s TCP 拨号语义）。
		if dial != nil && dialAddr != "" && dial(dialAddr, dockListenTimeout) == nil {
			extra := ""
			if pidNote != "" {
				extra = "（" + pidNote + "）"
			}
			return Check{true, "daemon 在听 " + dialAddr +
				"（/stats 探测未答——存取级异常不判死，监听事实优先）" + extra}
		}
		if pidNote != "" {
			return Check{false, "daemon 未运行（pid 文件残留 " + pidNote +
				" 而控制口无监听——进程已死或启动窗/半死形态；钩子自举会拉起，或手动 start-daemon.cmd）"}
		}
		return Check{false, "daemon 未运行（钩子自举会拉起，或手动 start-daemon.cmd）"}
	}
	extra := ""
	if pidNote != "" {
		extra = "（" + pidNote + "）"
	}
	alert := " · ok"
	if v, ok := st["health_alert"].(bool); ok && v {
		alert = " · ⚠ 健康告警: 疑似钩子失效"
	}
	return Check{true, "daemon 活着" + extra + alert}
}

// realDialTCP 渡口监听真探针：TCP 拨号，连上即 nil（watchdog.probeTCPPort 同
// 语义；渡口是流式端点，可连＝在听）。
func realDialTCP(addr string, timeout time.Duration) error {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return err
	}
	return conn.Close()
}

// dockListenTimeout CheckDockListening 拨号超时。
const dockListenTimeout = 2 * time.Second

// CheckDockListening 半死形态权威检查（2026-09-29 复盘件）：daemon 活着且
// 配置了渡口 → 渡口必须在听。典型成因：升级/重启排水竞态里新守护渡口绑定
// 失败降级"无渡口"（控制口活/渡口死，看门探活看不见）；dial 未装配时由
// 调用方落 not_checked（本函数不伪造）。
func CheckDockListening(listen string, dial func(addr string, timeout time.Duration) error) Check {
	if dial == nil {
		return Check{false, "渡口探针未装配"}
	}
	err := dial(listen, dockListenTimeout)
	if err == nil {
		return Check{true, fmt.Sprintf("渡口在听 %s", listen)}
	}
	return Check{false, fmt.Sprintf("半死形态：daemon 活着但渡口 %s 无监听（%v）——"+
		"处置: restart-daemon.ps1（docs/20260929_守护重启事故复盘.md）", listen, err)}
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
var updateResiduePatterns = []string{"ferryman.exe.new", "ferryman.exe.new.part", "ferryman.exe.swap-tmp*"}

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
	// 票01（ADR-0015）：价格表源（tuning=auto 的 p_cache 检查用）；nil =
	// 未装配 → 该项显式 not_checked（如实标注不伪造）。ArmVerdict 实跳臂
	// 结论查询缝（票04 启用门实施后注入）；nil = 状态源未装配 → 按无结论
	// 对待（启用硬门槛：无结论 = 未启用）。
	LoadPrices func() map[string]prices.PriceBook
	ArmVerdict config.ArmVerdictResolver
	Probe      func() map[string]any
	// DialTCP 渡口监听探针（2026-09-29 复盘件 dock_listening 检查用）；
	// nil = 未装配 → 该项显式 not_checked（如实标注不伪造，LoadPrices 同款）。
	DialTCP func(addr string, timeout time.Duration) error
	// 票02：常驻保障两查（Run 键三态 + 看门任务在位/缺失）。
	Autostart    func() (autostartStatus, error)
	WatchdogTask func() (TaskStatus, error)
	// Version 版本行（票02，规格 §A）：cmd/ferryman 的 main.version 经装配
	// 参数传入（internal 包不 import cmd——显式传参不做全局单例）；空串/dev
	// 都按「非 release 构建」呈现。
	Version string
	// OrcaCodexHome orca 生态 CODEX_HOME（服务商接管体检用，票05）；空 = 按
	// <Home>/AppData/Roaming/orca/codex-runtime-home/home 解析（与
	// daemon.CodexWatchDirs 的 orca 目录解析同位）。测试注入临时目录。
	OrcaCodexHome string
	// DSHL0 dsh_plugin_static 检查缝（verify-dsh 票06）：internal/dshverify 的
	// L0 静态检查产物（RunL0 原样返回——纯读零副作用，agent 面 MCP doctor
	// 进程内复用安全）。nil = 检查目标未装配（未装 DSH/[dock] 未配置/测试
	// 密闭形态）→ 该项显式 not_checked（autostart/watchdog 同款，如实标注
	// 不伪造）。真装配 dshL0Seam 单源（CLI 与 agent 面同一公式）。
	DSHL0 func() []dshverify.CheckResult
	Out   io.Writer
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
	return runDoctor(realDoctorDeps(version))
}

// RunDoctorJSON doctor --json 真实入口（票04）：真装配与 RunDoctor 同一套
// （realDoctorDeps 单源——两出口绝不各拼一套 deps）；结果经 doctorJSON 序列化
// （与 agent 面 MCP doctor 同源——CheckResult 逐项 + summary 计数，顶层加
// version 与 ok 总判定），单行 JSON 到 stdout；退出码与文本面同判（有 fail → 1）。
func RunDoctorJSON(version string) int {
	d := realDoctorDeps(version)
	b, code := doctorJSON(d, version)
	if b != nil {
		fmt.Fprintln(d.Out, string(b))
	}
	return code
}

// realDoctorDeps RunDoctor/RunDoctorJSON 共用的真装配（HOME/exe 面 + config
// 解析的探针目标；公式单源纪律）。
func realDoctorDeps(version string) doctorDeps {
	home := homeDir()
	// 票05：daemon 活性目标经 config 解析（config.Load 优先级：显式参数 >
	// FERRYMAN_CONFIG > 默认路径）；加载失败回落内置默认口（探针目标与既有
	// 行为同位），配置坏本身由体检项如实报出。
	cfg, cfgErr := config.Load("", false)
	port := config.Default().Server.Port
	if cfgErr == nil {
		port = cfg.Server.Port
	}
	// verify-dsh 票06：dsh_plugin_static 的渡口目标（[dock] 未配置/配置坏＝
	// 空串 → dshL0Seam 返回 nil → 该项 not_checked，服务商接管四项同款先例）
	dockListen := ""
	if cfgErr == nil && cfg != nil && cfg.Dock != nil {
		dockListen = cfg.Dock.Listen
	}
	return doctorDeps{
		Home:        home,
		Repo:        repoRoot(),
		CCSwitchDB:  CCSwitchDBPath(home),
		CodexHooks:  CodexHooksPath(home),
		CodexConfig: CodexConfigPath(home),
		LoadCfg:     func() (*config.Config, error) { return cfg, cfgErr },
		LoadProviders: func() (map[string]ferry.Provider, error) {
			return ferry.LoadProviders("")
		},
		// 票01：价格表源（CLI 面 "" → ~/ferryman/config.toml）
		LoadPrices: func() map[string]prices.PriceBook { return prices.LoadPrices("") },
		// 票04 收口：实跳臂结论真源（无状态文件→条目未启用,如实体检）
		ArmVerdict: ferry.ArmVerdictResolverFor(ferry.DefaultArmVerdictPath()),
		Probe:      realStatsProbe(filepath.Join(home, "ferryman"), port),
		// 2026-09-29 复盘件：半死形态检查真探针（CLI 面与 agent 面同源）
		DialTCP: realDialTCP,
		// 票02：常驻保障两查真探测（只读注册表 / schtasks /Query，无写副作用）
		Autostart:    func() (autostartStatus, error) { return autostartStatusOf(realAutostartDeps()) },
		WatchdogTask: func() (TaskStatus, error) { return queryTask(realTaskDeps()) },
		// verify-dsh 票06：DSH 插件 L0 静态缝（~/.dsh 在位且 [dock] 已配置才
		// 装配；RunL0 纯读零副作用——与 agent 面 DoctorStructured 同缝单源）
		DSHL0:   dshL0Seam(home, dockListen),
		Version: version,
		Out:     os.Stdout,
	}
}

// doctorScriptNames 体检的钩子脚本清单（doctor.py run_doctor scripts 逐字）。
// verify-dsh 票06 起 +1：ferryman-gate-dsh.ps1（dsh CC 钩子桥指向的闸门变体
// ——hooks/ 已在位但此前不在清单，BOM/控制字符检查对它缺位；七→八）。
func doctorScriptNames() []string {
	return []string{
		"ferryman-gate.ps1", "ferryman-gate-dsh.ps1", "ferryman-restore.ps1",
		"ferryman-subagent.ps1", "ferryman-ensure.ps1", "ferryman-gate-codex.ps1",
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
		// 票01（ADR-0015）：同模型/调参配置矛盾四查——判定与文案单源
		// internal/config.SameModelDoctorChecks，此处只换装 CheckResult（与
		// dock 检查组同款公式单源）。默认配置（off/recommend/空白名单）零新增
		// 行；books 未装配 → p_cache 项 not_checked；实跳臂结论缝未装配（票04
		// 启用门）→ 按无结论如实报 fail。
		var books map[string]prices.PriceBook
		if d.LoadPrices != nil {
			books = d.LoadPrices()
		}
		for _, dc := range config.SameModelDoctorChecks(cfg, books, d.ArmVerdict) {
			st := StatusPass
			switch {
			case dc.Skip:
				st = StatusNotChecked
			case !dc.OK:
				st = StatusFail
			}
			out = append(out, CheckResult{Name: dc.Name, Status: st, Detail: dc.Detail})
		}
		// 终局修复(终局评审普通建议,防静默死档):same_model 开而判热 TTL 未设
		// ([heartbeat].ttl_s = 0)→ ferry.PredictHot 恒判冷,同模型档永不触发
		// ——如实警告并指向实测(判热界 τ = 0.8×ttl_s)。same_model 关 = 零新增
		// 行(存量用户 doctor 输出零漂移)。
		if cfg.SameModel.Enabled {
			if cfg.Heartbeat.TTLS <= 0 {
				out = append(out, CheckResult{Name: "same_model_heat_ttl", Status: StatusFail,
					Detail: "same_model 已开启但 [heartbeat].ttl_s 未设（=0）——判热将恒冷，" +
						"同模型档永不触发，请实测填 ttl_s（experiments/cache-ttl 套件）"})
			} else {
				out = append(out, CheckResult{Name: "same_model_heat_ttl", Status: StatusPass,
					Detail: fmt.Sprintf("判热 TTL 已设 %g 秒（判热界 τ=0.8×TTL=%g 秒）",
						cfg.Heartbeat.TTLS, 0.8*cfg.Heartbeat.TTLS)})
			}
		}
		// 票06：渡口配置了才查（无 [dock] 的存量用户零新增检查行）
		if cfg.Dock != nil {
			out = append(out, CheckDockRewrite(cfg.Dock).named("dock_rewrite"))
			// 票04：渡口上游检查组（CheckResult 自带名：dock_upstream /
			// dock_upstream_rewrite_hint / dock_upstream_key:<条目名> /
			// dock_deepseek_boundary——主判紧随 dock_rewrite）。
			out = append(out, CheckDockUpstreams(cfg.Dock)...)
			// 2026-09-29 复盘件：半死形态权威检查——daemon 活着且配置了渡口 →
			// 渡口必须在听（daemon 未跑时渡口不在属预期，不查，由 daemon_liveness
			// 自报；探针未装配 → not_checked 如实标注）。放 dock 组末位。
			if d.Probe() != nil {
				if d.DialTCP == nil {
					out = append(out, CheckResult{Name: "dock_listening", Status: StatusNotChecked,
						Detail: "渡口监听未检查（探针未装配——如实标注不伪造）"})
				} else {
					out = append(out, CheckDockListening(cfg.Dock.Listen, d.DialTCP).named("dock_listening"))
				}
			}
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
	// 票04：daemon_liveness 增控制口拨号见证（监听事实优先）——目标口与探针
	// 同源（cfg.Server.Port）；config 加载失败/cfg nil → 目标口未解析＝探针单源
	// 旧判（坏配置由 ferry_provider 项如实报，此处不二次归因）。
	livenessDialAddr := ""
	if err == nil && cfg != nil {
		livenessDialAddr = fmt.Sprintf("127.0.0.1:%d", cfg.Server.Port)
	}
	out = append(out, CheckDaemon(d.Probe, filepath.Join(dataDir, "daemon.pid"),
		d.DialTCP, livenessDialAddr).named("daemon_liveness"))
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
	// 服务商接管体检（票05 三项＋票11 pi 生效链第四项，spec Implementation
	// Decisions 7/4）：provider_cc_dock / provider_codex_dock /
	// provider_orca_codex / provider_pi_dock——判定单源 internal/provider（与
	// 写入器同一套解析与目标地址派生，绝不两套判据）。[dock] 未配置/配置加载
	// 失败 → 四项显式 not_checked（接管目标不可判——如实标注不伪造）。续接
	// 末位：既有检查项顺序零漂移。
	out = append(out, providerCheckResults(cfg, err, d)...)
	// ---- verify-dsh 票06（DSH 插件验证吸收面）：dsh_plugin_static ＋ 版本黄灯 ----
	// 协调注（票06 改票，合并 main 后复核）：与 dsh_poller_sentinel（dsh-host-guard
	// 泳道，宿主插件哨兵，见下）互补不重复——本项管安装面静态完整性（L0 纯读），
	// 彼项管挂载活性（poll 心跳状态机）；本泳道不加任何 poll/活性检查项（原
	// dsh_poll_age 已撤销，避免同能力两份实现）。
	if d.DSHL0 == nil {
		out = append(out, CheckResult{Name: "dsh_plugin_static", Status: StatusNotChecked,
			Detail: "DSH 插件静态检查未执行（检查目标未装配：~/.dsh 不在位或 [dock] 未配置" +
				"——如实标注不伪造）"})
	} else {
		// 判定流水只读（黄灯判据用；零副作用红线——绝不写盘，读失败降级）
		rows, ledgerErr := dshVerdictRows(dataDir)
		out = append(out, dshPluginStaticResult(d.DSHL0(), rows, ledgerErr))
	}
	// dsh-host-guard 票03：宿主插件哨兵——续接末位（顺序零漂移纪律）。与
	// daemon_liveness 同一探针（d.Probe）；daemon 不可达时本项 pass 注记不叠加
	// 误报（活性 fail 由 daemon_liveness 独报，见 CheckDshPollerSentinel）。
	out = append(out, CheckDshPollerSentinel(d.Probe).named("dsh_poller_sentinel"))
	return out
}

// providerCheckResults 服务商接管四项（票05 三项＋票11 pi 生效链）：dock 未配
// 置 → 四行 not_checked；否则按 cfg.Dock.Listen 派生渡口目标（CC/pi=http 根、
// codex=http+/v1，单源 provider.DockURLFromListen / DockCodexURLFromListen）
// 交 provider 探针判定。pi 两路径与 CLI 装配层 providerTargetsFromHome 同口径
// （~/.pi/agent 下成对两文件，home 派生、无配置项）。
func providerCheckResults(cfg *config.Config, cfgErr error, d doctorDeps) []CheckResult {
	notChecked := func() []CheckResult {
		rows := make([]CheckResult, 0, 4)
		for _, n := range []string{"provider_cc_dock", "provider_codex_dock",
			"provider_orca_codex", "provider_pi_dock"} {
			rows = append(rows, CheckResult{Name: n, Status: StatusNotChecked,
				Detail: "服务商接管体检未检查（[dock] 未配置——接管目标不可判，如实标注不伪造）"})
		}
		return rows
	}
	if cfgErr != nil || cfg == nil || cfg.Dock == nil {
		return notChecked()
	}
	orcaHome := d.OrcaCodexHome
	if orcaHome == "" {
		orcaHome = filepath.Join(d.Home, "AppData", "Roaming", "orca",
			"codex-runtime-home", "home")
	}
	orcaCfg := filepath.Join(orcaHome, "config.toml")
	return []CheckResult{
		providerVerdict("provider_cc_dock", provider.CheckCCPointsDock(
			filepath.Join(d.Home, ".claude", "settings.json"),
			provider.DockURLFromListen(cfg.Dock.Listen))),
		providerVerdict("provider_codex_dock", provider.CheckCodexPointsDock(
			CodexConfigPath(d.Home), orcaCfg,
			provider.DockCodexURLFromListen(cfg.Dock.Listen))),
		providerVerdict("provider_orca_codex", provider.CheckOrcaCodexHealth(
			orcaCfg, provider.DockCodexURLFromListen(cfg.Dock.Listen))),
		providerVerdict("provider_pi_dock", provider.CheckPiPointsDock(
			filepath.Join(d.Home, ".pi", "agent", "models.json"),
			filepath.Join(d.Home, ".pi", "agent", "settings.json"),
			provider.DockURLFromListen(cfg.Dock.Listen))),
	}
}

// providerVerdict provider.Verdict → CheckResult 换装（与 Check.named 同款：
// NotChecked→not_checked、OK→pass、!OK→fail；Detail 原样透传——判定在
// provider 单源，本包不重复）。
func providerVerdict(name string, v provider.Verdict) CheckResult {
	st := StatusFail
	switch {
	case v.NotChecked:
		st = StatusNotChecked
	case v.OK:
		st = StatusPass
	}
	return CheckResult{Name: name, Status: st, Detail: v.Detail}
}

// ---- verify-dsh 票06：dsh_plugin_static（L0 吸收＋版本黄灯） ----
// 协调注：与并行链 dsh_poller_sentinel 互补不重复——本节管安装面静态完整性
// （L0 纯读），彼项管挂载活性（poll 心跳状态机）；本泳道无任何 poll/活性
// 检查项（原 dsh_poll_age 已撤销）。

// dshL0Seam dsh_plugin_static 生产装配（CLI realDoctorDeps 与 agent 面
// DoctorStructured 同缝——公式单源，绝不两套判据）。nil 返回＝检查目标未装配
// → 该项 not_checked 不产红，两种情形：
//   - ~/.dsh 不在位＝未装 DSH（provider planDSH「dsh 未安装——跳过」同语义）；
//   - [dock] 未配置＝渡口目标不可判（服务商接管四项 not_checked 同款先例——
//     L0 生产配置面三项须比对渡口地址）。
//
// 生产根定位照仓库既有惯例（cmd/ferryman providerTargetsFromHome 同位：
// <Home>/.dsh）；渡口地址 provider.DockURLFromListen 单源（不自造拼接）；DSH
// 安装树＝%LocalAppData%/Programs/DeepSeek Harness（dshverify Input 注释的
// Windows 实锚；env 缺失＝空串＝版本项整体省略，参数缺席≠检查失败）。
// dshverify.RunL0 纯读零副作用（只 stat/read）——agent 面 MCP doctor 进程内
// 复用安全（tool_doctor 零副作用红线同守）。
func dshL0Seam(home, dockListen string) func() []dshverify.CheckResult {
	if strings.TrimSpace(dockListen) == "" {
		return nil
	}
	dshRoot := filepath.Join(home, ".dsh")
	if fi, err := os.Stat(dshRoot); err != nil || !fi.IsDir() {
		return nil
	}
	install := filepath.Join(os.Getenv("LocalAppData"), "Programs", "DeepSeek Harness")
	return func() []dshverify.CheckResult {
		return dshverify.RunL0(dshverify.Input{
			DSHRoot:     dshRoot,
			DSHInstall:  install,
			DockBaseURL: provider.DockURLFromListen(dockListen),
		})
	}
}

// dshPluginStaticResult internal/dshverify L0 产物 → 单个 doctor 检查项
// dsh_plugin_static（verify-dsh 票06 吸收面）。汇总语义：
//   - 任一 L0 项 fail（含配置面/版本项）→ StatusFail，detail 按 profile 列失败项
//     （<profile>/<项名>: <指位>；配置面/版本项不带 profile 前缀）；
//   - 全过 → StatusPass，detail 按 profile 汇总各 profile 过项数；
//   - 黄灯（D10：黄不推，只在 pass 面挂）：当前 DSH 版本（版本项解析）不在判定
//     流水任何行 → detail 追加黄灯提示行——只提示不告警不判失败、退出码不动；
//     流水读取失败降级「无法判定」（doctor 只读流水，绝不写盘——零副作用红线）；
//     版本项缺席/解析不出＝版本未知，黄灯判据无从判定，如实不出提示。
func dshPluginStaticResult(l0 []dshverify.CheckResult, rows []dshledger.Entry, ledgerErr error) CheckResult {
	r := CheckResult{Name: "dsh_plugin_static"}
	var fails []string
	okByProfile := map[string]int{}
	profileOrder := []string{}
	seenProfile := map[string]bool{}
	for _, c := range l0 {
		if c.Profile != "" {
			if !seenProfile[c.Profile] {
				seenProfile[c.Profile] = true
				profileOrder = append(profileOrder, c.Profile)
			}
			if c.OK {
				okByProfile[c.Profile]++
			}
		}
		if c.OK {
			continue
		}
		label := c.Name
		if c.Profile != "" {
			label = c.Profile + "/" + c.Name
		}
		fails = append(fails, label+": "+c.Detail)
	}
	if len(fails) > 0 {
		r.Status = StatusFail
		r.Detail = fmt.Sprintf("DSH 插件静态检查 %d 项失败: %s", len(fails), strings.Join(fails, "；"))
		return r
	}
	r.Status = StatusPass
	parts := make([]string, 0, len(profileOrder))
	for _, p := range profileOrder {
		parts = append(parts, fmt.Sprintf("%s %d 项全过", p, okByProfile[p]))
	}
	r.Detail = fmt.Sprintf("DSH 插件静态检查全过（%s；生产配置面与版本项随 L0 验毕）",
		strings.Join(parts, "、"))
	// 黄灯（D10：黄不推）——只挂 pass 面
	ver := dshCurrentVersion(l0)
	if ver == "" {
		return r
	}
	switch {
	case ledgerErr != nil:
		r.Detail += "；判定流水读取失败——版本验证状态无法判定（" + ledgerErr.Error() + "）"
	case !dshVersionInLedger(rows, ver):
		r.Detail += "；黄灯提示: 当前 DSH 版本 " + ver + " 不在判定流水中（版本未验证——" +
			"跑 ferryman verify-dsh 完成一次全绿验证后入册；黄灯只提示不告警）"
	}
	return r
}

// dshCurrentVersion L0 产物里解析当前 DSH 版本（判据＝版本项 ok）。detail 文案
// 前缀锚＝dshverify installVersionCheck 的 "DSH 版本 %s（%s）"——dshverify 未
// 导出专门取值函数，票面指定消费其检查结果（本包不另读版本文件造第二判据）；
// 解析不出＝版本未知（黄灯判据无从判定，如实不出提示）。
func dshCurrentVersion(l0 []dshverify.CheckResult) string {
	for _, c := range l0 {
		if c.Name != dshverify.ChkInstallVersion || !c.OK {
			continue
		}
		s := strings.TrimPrefix(c.Detail, "DSH 版本 ")
		if i := strings.Index(s, "（"); i > 0 {
			return strings.TrimSpace(s[:i])
		}
	}
	return ""
}

// dshVersionInLedger 黄灯判据：当前 DSH 版本是否在任何流水行（含黄/红行——
// 「验证过」与「全绿」两回事，黄灯只问"这版本验没验过"）。
func dshVersionInLedger(rows []dshledger.Entry, version string) bool {
	for _, e := range rows {
		if e.DSHVersion == version {
			return true
		}
	}
	return false
}

// dshVerdictRows 判定流水只读读取（doctor 零副作用红线：不走 dshledger.New
// ——它 MkdirAll 建目录；只读打开已落盘的 verdicts.jsonl，坏行跳过/缺文件＝
// 首跑空表，语义与 dshledger.List 同款）。目录布局与 dshledger.New 同名同值
// （<dataDir>/dshledger/verdicts.jsonl），行形状单源 dshledger.Entry、行读
// 单源 internal/jsonl——两包测试各自钉住，漂移双双报红。
func dshVerdictRows(dataDir string) (rows []dshledger.Entry, err error) {
	err = jsonl.ReadLines(filepath.Join(dataDir, "dshledger", "verdicts.jsonl"), func(line string) bool {
		line = strings.TrimSpace(line)
		if line == "" {
			return true
		}
		var e dshledger.Entry
		if json.Unmarshal([]byte(line), &e) != nil {
			return true // 坏行跳过（宁缺勿炸——dshledger.List 读侧宽容同款）
		}
		rows = append(rows, e)
		return true
	})
	if err != nil && os.IsNotExist(err) {
		return []dshledger.Entry{}, nil
	}
	return rows, err
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
	// verify-dsh 票06：dsh_plugin_static 渡口目标（[dock] 未配置 → dshL0Seam
	// 返回 nil → 该项 not_checked——与 CLI realDoctorDeps 同缝单源）
	dockListen := ""
	if cfg.Dock != nil {
		dockListen = cfg.Dock.Listen
	}
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
		// 票01：价格表源（与 providers 同一配置路径——"" 回落默认路径）
		LoadPrices: func() map[string]prices.PriceBook { return prices.LoadPrices(cfgPath) },
		// 票04 收口：实跳臂结论真源（agent 面与 CLI 面同源）
		ArmVerdict: ferry.ArmVerdictResolverFor(ferry.DefaultArmVerdictPath()),
		Probe:      realStatsProbe(cfg.DataDir(), cfg.Server.Port),
		// 2026-09-29 复盘件：半死形态检查真探针（渡口 TCP 拨号）
		DialTCP: realDialTCP,
		// verify-dsh 票06：DSH 插件 L0 静态缝（RunL0 纯读零副作用——agent 面
		// MCP doctor 进程内复用安全；未装 DSH/[dock] 未配置 → nil → not_checked）
		DSHL0: dshL0Seam(home, dockListen),
	}
	if residency {
		d.Autostart = func() (autostartStatus, error) { return autostartStatusOf(realAutostartDeps()) }
		d.WatchdogTask = func() (TaskStatus, error) { return queryTask(realTaskDeps()) }
	}
	return doctorResults(d)
}

// DoctorJSONSummary --json 顶层汇总（票04）：与 agent 面 MCP doctor 工具的
// summary 同键同口径（计数从逐项结论推导，非第二事实源）。
type DoctorJSONSummary struct {
	Total      int `json:"total"`
	Pass       int `json:"pass"`
	Fail       int `json:"fail"`
	NotChecked int `json:"not_checked"`
}

// DoctorJSONReport doctor --json 顶层形状（票04）：version（票02 装配参数，
// dev＝非 release 构建）+ ok 总判定（零 fail，与退出码同源）+ checks（
// CheckResult 三要素与 MCP 面同源）+ summary 计数。脱敏契约（F3）：逐项
// Detail 只报路径/状态/修法文案，密钥只允许 maskKey 尾 4 位形态经此出口。
type DoctorJSONReport struct {
	Version string            `json:"version"`
	OK      bool              `json:"ok"`
	Checks  []CheckResult     `json:"checks"`
	Summary DoctorJSONSummary `json:"summary"`
}

// doctorJSON 结构化体检的 --json 序列化（票04；deps 注入可测，真装配见
// RunDoctorJSON）：doctorResults 单源计算 → 聚合 summary 与 ok 总判定 →
// 单行 JSON 字节；退出码与 runDoctor 同判（有 fail → 1，ok 与退出码同源）。
// 纯函数：不打印、不写盘。
func doctorJSON(d doctorDeps, ver string) ([]byte, int) {
	checks := doctorResults(d)
	if checks == nil {
		checks = []CheckResult{}
	}
	rep := DoctorJSONReport{Version: ver, Checks: checks}
	for _, r := range checks {
		rep.Summary.Total++
		switch r.Status {
		case StatusPass:
			rep.Summary.Pass++
		case StatusFail:
			rep.Summary.Fail++
		default:
			rep.Summary.NotChecked++
		}
	}
	rep.OK = rep.Summary.Fail == 0
	b, err := json.Marshal(rep)
	if err != nil { // 全字符串/计数字段，理论不可达——护底线不静默
		return nil, 1
	}
	if !rep.OK {
		return b, 1
	}
	return b, 0
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

// CheckDshPollerSentinel 宿主插件哨兵（dsh-host-guard 票03，spec E 判定/F 暴露面）：
// 读 daemon /stats 的 pollers 段（与 daemon_liveness/dock_listening 同一 Probe——
// realStatsProbe 解析产物，数字为 float64）。判定单源 daemon 侧生命周期状态机
// （poller_baseline.go dshPollerStateOf，state 字段即现值），本处只做换装不重算：
//   - stale（24h 内有心跳 ∧ 无下线标记 ∧ 静默>90s＝应在线而沉默）→ fail 且点名；
//   - offline/retired（显式下线/24h 自然退役，spec E 视界外）→ pass 注记；
//   - 无 poller 数据（首建前盲区/无任何心跳）→ pass 注记不误报；
//   - daemon 不可达（probe nil）→ pass 注记不叠加误报——daemon 死由既有
//     daemon_liveness 报，哨兵只管「daemon 活着时宿主插件死没死」。
func CheckDshPollerSentinel(probe func() map[string]any) Check {
	st := probe()
	if st == nil {
		return Check{true, "daemon 不可达——宿主插件哨兵不判（daemon 活性由 daemon_liveness 报）"}
	}
	raw, _ := st["pollers"].([]any)
	if len(raw) == 0 {
		return Check{true, "无宿主插件心跳记录（poller 基线为空——首建前盲区不判沉默）"}
	}
	var stale, notes []string
	for _, it := range raw {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		state, _ := m["state"].(string)
		lastSeen, _ := m["last_seen"].(float64)
		seen := "时刻未知"
		if lastSeen > 0 {
			seen = time.Unix(int64(lastSeen), 0).Format("2006-01-02 15:04:05")
		}
		switch state {
		case "stale":
			stale = append(stale, fmt.Sprintf("%s（最近心跳 %s，静默>90s）", name, seen))
		case "online":
			notes = append(notes, fmt.Sprintf("%s 在线", name))
		case "offline":
			notes = append(notes, fmt.Sprintf("%s 已下线（最近心跳 %s）", name, seen))
		case "retired":
			notes = append(notes, fmt.Sprintf("%s 已退役（>24h 无心跳，最近 %s）", name, seen))
		default:
			notes = append(notes, fmt.Sprintf("%s 状态 %q（形态异常，不判）", name, state))
		}
	}
	if len(stale) > 0 {
		return Check{false, "宿主插件静默（应在线而无心跳——查宿主进程是否存活，重开宿主/插件即恢复）: " +
			strings.Join(stale, "；")}
	}
	if len(notes) == 0 { // pollers 段在但条目全坏形——如实注记，不误报
		return Check{true, "poller 数据形态异常（无可用条目——不判沉默）"}
	}
	return Check{true, "宿主插件哨兵: " + strings.Join(notes, "；")}
}

// realStatsProbe /stats 探针：Bearer token 读 data_dir，2s 超时；任何失败 → nil
// （doctor.py real_probe 逐字；端口票05 起由调用方经 config 解析传入——同
// internal/config 优先级，默认 15700 与 Python 硬编码同位）。
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
		// 401 假活面修复（dsh-host-guard 晨报后续票01，2026-10-09）：401 的
		// {"error":"unauthorized"} 是合法 JSON，不看状态码会被当活体（误 pass
		// 方向）——非 200 一律 nil（token 不对＝存取级异常，活性另由拨号见证
		// 分支裁决，见 CheckDaemon「监听事实优先」）。
		if resp.StatusCode != http.StatusOK {
			return nil
		}
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
