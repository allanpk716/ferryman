// run.go — verify-dsh 票04：编排＋灯色判定＋输出分层＋--status＋红灯告警。
//
// 编排序（票面）：L0（本包 RunL0，票02）→ L1（daemon 管理口 GET /dsh/health，
// 票01，经 health_client.go）→ 灯色判定（Judge，表驱动纯函数）→ dshledger 记
// 流水（票03；daemon 版本取生产 daemon /stats 自报，守护不在则记 CLI 自身版本
// 并以 "cli:" 前缀注明）。L2 接缝（ProbeRunner）已由票05 填充：实现＝
// internal/dshsandbox.Probe（沙箱起栈＋四痕＋压缩链两道探针），装配点在 CLI
// 层（cmd/ferryman cmdVerifyDsh 传 Options.Probe——本包与 dshsandbox 是被装配
// 关系，不互相 import）。未装配时输出「L2 探针: 未装配」，整体判定降为未完成
// 验证（黄）：不落锚、不滚已知良好指针、不推告警（没有红灯依据）。
//
// 灯色规则（spec「灯色、输出与告警」节＝契约；红压倒黄、绿优先于「未验证」黄
// ——verify 跑完即落新行消除未验证态）：
//   - 红＝L0 任一检查项 fail（spec 口径「断言失败」，含配置面三项）／poll 超龄
//     且宿主进程在跑／从未 poll 且宿主在跑（形态 A，票01 合成语义）／L2 装配
//     但探针未过；
//   - 黄＝poll 超龄（或从未 poll）但宿主未运行／当前 DSH 版本不在流水（未验证
//     状态，非验证结果）／L1 无从求值（守护不在线/鉴权失败）／L2 未装配或执行
//     出错（未完成验证）；
//   - 绿＝L0 全过＋L1 已见 poll 且未超龄＋L2 装配且全绿；绿时落锚＋流水行带锚
//     哈希＋已知良好指针随绿行滚动（黄/红行永不抬指针——dshledger 契约）。
//
// 求值与推送时机：一切判定与推送只发生在本命令运行时（无常驻循环，D1）；
// 黄不推红才推（D10）。守护不在线≠红——Ferryman 自身掉线在 doctor 面可见，
// 在此冒充插件红灯会毁告警可信度（用户故事 4 的同源纪律）。
package dshverify

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"ferryman/internal/dshledger"
)

// ---- L1 判定事实 ----

// L1Facts 从 Health 提取的灯色判定事实（ Judge 的 L1 输入；nil＝无从求值）。
type L1Facts struct {
	NeverPolled       bool    // last_poll_age_s null＝守护本进程内从未见 poll
	LastPollAgeS      float64 // 有值时＝全局最近 poll 年龄秒
	PollOverdue       bool
	HostPresent       bool
	HostEvidence      string
	OverdueThresholdS float64
	IntervalSource    string
	IntervalAssumed   bool
	SessionCount      int // 被 poll 见到的会话数
}

// FactsFromHealth 应答形状 → 判定事实（纯提取，不判定）。
func FactsFromHealth(h *Health) *L1Facts {
	f := &L1Facts{
		PollOverdue:       h.PollOverdue,
		HostPresent:       h.HostProcessesPresent,
		HostEvidence:      h.HostEvidence,
		OverdueThresholdS: h.OverdueThresholdS,
		IntervalSource:    h.IntervalSource,
		IntervalAssumed:   h.IntervalAssumed,
		SessionCount:      len(h.Sessions),
	}
	if h.LastPollAgeS != nil {
		f.LastPollAgeS = *h.LastPollAgeS
	} else {
		f.NeverPolled = true
	}
	return f
}

// ---- L2 接缝（票05 已填充） ----

// ProbeRunner L2 沙箱探针接缝：nil＝未装配（本轮输出「未装配」、整体判定降为
// 未完成验证）。实现＝internal/dshsandbox.Probe（NewProbe 装配、经 CLI 层注回
// Options.Probe）；本包只消费结果。
type ProbeRunner interface {
	// Run 起沙箱栈跑探针断言。error＝执行出错（沙箱起栈失败等基础设施故障，
	// 未完成验证非断言失败）；断言失败用 ProbeResult.Green=false 表达。
	Run() (*ProbeResult, error)
}

// ProbeResult L2 探针结果。
type ProbeResult struct {
	// Green 五断言全过（闸门到达/事件上报/会话键/注入路径/压缩链两道各自）。
	Green bool
	// Summary 单行摘要（输出层与红理由直用）。
	Summary string
	// Faces 四类契约面形状（全绿落锚原料，票05 采集；键＝dshledger.Face*）。
	Faces map[string]any
}

// ---- 灯色判定（表驱动纯函数） ----

// JudgeInput Judge 输入（全部事实显式传入，不读环境不猜）。
type JudgeInput struct {
	L0 []CheckResult // L0 结果（RunL0 产物）
	L1 *L1Facts      // nil＝无从求值（守护不在线/鉴权失败）
	// VersionInLedger 当前 DSH 版本是否已在判定流水（预跑状态；跑完即消除）。
	VersionInLedger bool
	Probe           *ProbeResult // nil＝未装配
	ProbeErr        error        // 装配了但执行出错
}

// Judgment 判定结果：灯色＋理由清单（理由已带分类前缀，告警文本直接拼）。
type Judgment struct {
	Verdict dshledger.Verdict
	Reds    []string // 红理由（「插件失联（有宿主无 poll）」/「验证失败（N 项未过）」两分类）
	Yellows []string // 黄理由（红判定时不填——红压倒黄）
}

// failName 检查项的简名（profile 项带 profile 前缀，告警/输出可定位）。
func failName(r CheckResult) string {
	if r.Profile != "" {
		return r.Profile + "/" + r.Name
	}
	return r.Name
}

// Judge 灯色判定（纯函数，矩阵单测的受测面）。优先级：红 > 绿 > 黄。
func Judge(in JudgeInput) Judgment {
	j := Judgment{}
	l0Fails := 0
	var failNames []string
	for _, r := range in.L0 {
		if !r.OK {
			l0Fails++
			failNames = append(failNames, failName(r))
		}
	}
	if l0Fails > 0 {
		j.Reds = append(j.Reds, fmt.Sprintf("验证失败（%d 项未过）: %s",
			l0Fails, strings.Join(failNames, ", ")))
	}
	if in.L1 != nil {
		switch {
		case in.L1.PollOverdue && in.L1.HostPresent:
			j.Reds = append(j.Reds, fmt.Sprintf(
				"插件失联（有宿主无 poll）: poll 年龄 %.0fs 已超阈值 %.0fs；宿主旁证: %s",
				in.L1.LastPollAgeS, in.L1.OverdueThresholdS, in.L1.HostEvidence))
		case in.L1.NeverPolled && in.L1.HostPresent:
			j.Reds = append(j.Reds, fmt.Sprintf(
				"插件失联（有宿主无 poll）: 宿主旁证在跑（%s）但守护从未收到 poll（形态 A）",
				in.L1.HostEvidence))
		}
	}
	if in.ProbeErr == nil && in.Probe != nil && !in.Probe.Green {
		j.Reds = append(j.Reds, fmt.Sprintf("验证失败（L2 探针未过）: %s", in.Probe.Summary))
	}
	if len(j.Reds) > 0 {
		j.Verdict = dshledger.VerdictRed
		return j
	}
	// 绿条件：L0 全过＋L1 可求值且已见 poll 未超龄＋L2 装配且全绿（执行无错）。
	if l0Fails == 0 && in.L1 != nil && !in.L1.NeverPolled && !in.L1.PollOverdue &&
		in.Probe != nil && in.ProbeErr == nil && in.Probe.Green {
		j.Verdict = dshledger.VerdictGreen
		return j
	}
	// 黄理由清单（全平台如实列出；两条黄都不推送——D10）。
	if in.L1 == nil {
		j.Yellows = append(j.Yellows, "L1 无从求值（守护不在线/鉴权失败）——未完成验证")
	}
	if in.L1 != nil && !in.L1.HostPresent {
		switch {
		case in.L1.PollOverdue:
			j.Yellows = append(j.Yellows, fmt.Sprintf(
				"宿主未运行: poll 超龄（%.0fs > 阈值 %.0fs）但无宿主旁证——不推送",
				in.L1.LastPollAgeS, in.L1.OverdueThresholdS))
		case in.L1.NeverPolled:
			j.Yellows = append(j.Yellows, "宿主未运行: 无 poll 记录且无宿主旁证——不推送")
		}
	}
	if !in.VersionInLedger {
		j.Yellows = append(j.Yellows, "版本未验证: 当前 DSH 版本不在流水（verify 跑完即落行消除）")
	}
	if in.ProbeErr != nil {
		j.Yellows = append(j.Yellows, fmt.Sprintf("未完成验证: L2 探针执行出错: %v", in.ProbeErr))
	}
	if in.Probe == nil {
		j.Yellows = append(j.Yellows, "未完成验证: L2 探针未装配（不落锚不滚已知良好）")
	}
	j.Verdict = dshledger.VerdictYellow
	return j
}

// ---- 版本读取（流水三元组的 DSH/插件份） ----

// readDSHVersion 安装树 version 文件（与 L0 ChkInstallVersion 同源；空＝不可读，
// 展示层译「未知」）。
func readDSHVersion(installRoot string) string {
	if strings.TrimSpace(installRoot) == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(installRoot, "version"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// readPluginVersion 插件版本：首个可读 profiles/<p>/node_modules/ferryman-dsh/
// package.json 的 version（三拷贝同步是部署纪律，取首个；全不可读＝空串）。
// profiles 空＝DefaultProfiles。
func readPluginVersion(dshRoot string, profiles []string) string {
	if strings.TrimSpace(dshRoot) == "" {
		return ""
	}
	if len(profiles) == 0 {
		profiles = DefaultProfiles
	}
	for _, p := range profiles {
		raw, err := os.ReadFile(filepath.Join(dshRoot, "profiles", p,
			"node_modules", "ferryman-dsh", "package.json"))
		if err != nil {
			continue
		}
		var doc struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(raw, &doc) == nil && strings.TrimSpace(doc.Version) != "" {
			return doc.Version
		}
	}
	return ""
}

// ---- 编排 ----

// Options verify-dsh 一次运行的装配参数（CLI 层解析真机路径后传入；本包不读
// 环境变量猜根——与 Input 同一纪律）。
type Options struct {
	// L0 装配（生产根）。
	DSHRoot     string
	DSHInstall  string
	DockBaseURL string
	Profiles    []string
	// Health daemon 管理口客户端（nil＝按守护不在线处理——防御位，CLI 常装配）。
	Health *HealthClient
	// DataDir daemon 数据目录（dshledger 落盘处；空＝拒绝运行）。
	DataDir string
	// Probe L2 接缝（nil＝未装配）。
	Probe ProbeRunner
	// CLIVersion daemon 不可达时流水行 daemon 份的代记来源。
	CLIVersion string
	// Alert 红灯告警缝（nil＝不推；CLI 装 notify.NotifyEvent(drift) 双通道）。
	Alert func(title, message string)
	// Out 输出（CLI 传 os.Stdout；测试注 buffer）。
	Out io.Writer
}

// Run 跑一次完整验证。返回进程退出码：红＝1（验证失败/失联），绿/黄＝0，
// 基础设施错（流水打不开/落盘失败）＝1 且如实报告。
func Run(o Options) int {
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	if strings.TrimSpace(o.DataDir) == "" {
		fmt.Fprintln(out, "拒绝：数据目录未传入——判定流水无从落盘")
		return 1
	}
	// L0（生产侧求值：根由装配层显式传入）。
	l0 := RunL0(Input{DSHRoot: o.DSHRoot, Profiles: o.Profiles,
		DSHInstall: o.DSHInstall, DockBaseURL: o.DockBaseURL})
	dshVer := readDSHVersion(o.DSHInstall)
	pluginVer := readPluginVersion(o.DSHRoot, o.Profiles)

	// L1＋ daemon 版本自报（两口独立失败独立交代）。
	var l1 *L1Facts
	var l1Err, statsErr error
	daemonVer := ""
	if o.Health != nil {
		var h *Health
		if h, l1Err = o.Health.Health(); l1Err == nil {
			l1 = FactsFromHealth(h)
		}
		if daemonVer, statsErr = o.Health.StatsVersion(); statsErr != nil {
			daemonVer = ""
		}
	} else {
		l1Err = ErrDaemonUnreachable
		statsErr = ErrDaemonUnreachable
	}
	daemonVerFromCLI := false
	if daemonVer == "" { // 守护不在线/自报缺失：记 CLI 自身版本并注明（cli: 前缀）
		daemonVer = "cli:" + o.CLIVersion
		daemonVerFromCLI = true
	}

	// 流水/锚（daemon 数据目录 dshledger/ 子区，票03）。
	log, err := dshledger.New(o.DataDir)
	if err != nil {
		fmt.Fprintf(out, "判定流水打不开: %v\n", err)
		return 1
	}
	anchors, err := dshledger.NewAnchorStore(o.DataDir, 0)
	if err != nil {
		fmt.Fprintf(out, "契约锚存储打不开: %v\n", err)
		return 1
	}
	entries, err := log.List()
	if err != nil {
		fmt.Fprintf(out, "判定流水读失败: %v\n", err)
		return 1
	}
	// 版本在流水＝流水里有同版本行（版本不可读＝无从匹配，按不在流水处理——
	// 未验证黄如实列出，与 L0 版本项 fail 的红并列不冲突）。
	versionInLedger := false
	if dshVer != "" {
		for _, e := range entries {
			if e.DSHVersion == dshVer {
				versionInLedger = true
				break
			}
		}
	}
	// 已知良好（降级目标）先读——本次落行（非绿）不影响它。
	knownGood, err := log.RecentKnownGood()
	if err != nil {
		fmt.Fprintf(out, "已知良好指针读失败: %v\n", err)
		return 1
	}

	// L2 接缝：未装配＝nil 探针结果（Judge 按未完成验证处理）。
	var probeRes *ProbeResult
	var probeErr error
	if o.Probe != nil {
		probeRes, probeErr = o.Probe.Run()
	}

	j := Judge(JudgeInput{L0: l0, L1: l1, VersionInLedger: versionInLedger,
		Probe: probeRes, ProbeErr: probeErr})

	// 绿→落锚（四面形状由探针采集；未供形状时如实注明不落锚——票05 装配后
	// 恒有）。落锚失败＝不留半状态：不落流水行，如实报错退出（重跑即恢复）。
	anchorHash := ""
	if j.Verdict == dshledger.VerdictGreen {
		if probeRes != nil && probeRes.Faces != nil {
			anchorHash, err = anchors.Write(dshVer, probeRes.Faces)
			if err != nil {
				fmt.Fprintf(out, "落锚失败（流水行未落，请重跑）: %v\n", err)
				return 1
			}
		} else {
			fmt.Fprintln(out, "注意: 全绿但探针未供契约面形状——本轮未落锚（探针装配缺陷）")
		}
	}
	entry, err := log.Append(dshVer, pluginVer, daemonVer, j.Verdict, anchorHash, "")
	if err != nil {
		fmt.Fprintf(out, "判定流水落盘失败: %v\n", err)
		return 1
	}

	// 输出分层（票面：三 profile 各一行＋L2 单行＋灯色；绿时尾注总义）。
	printReport(out, o, l0, l1, l1Err, probeRes, probeErr, j, entry, daemonVerFromCLI, dshVer, pluginVer)

	// 红灯告警（恰一条；黄不推；只在命令运行时判定与推送——无常驻循环）。
	if j.Verdict == dshledger.VerdictRed && o.Alert != nil {
		o.Alert("Ferryman DSH 验证红灯", alertBody(j, knownGood))
	}
	if j.Verdict == dshledger.VerdictRed {
		return 1
	}
	return 0
}

// alertBody 红灯告警文本：红理由两分类直列＋降级目标（已知良好三元组＋
// installer 路径若登记）。
func alertBody(j Judgment, knownGood *dshledger.Entry) string {
	var b strings.Builder
	b.WriteString("DSH 插件验证红灯（ferryman verify-dsh）:\n")
	for _, r := range j.Reds {
		b.WriteString("- " + r + "\n")
	}
	switch {
	case knownGood == nil:
		b.WriteString("降级目标（已知良好）: 尚无已知良好档案（从未全绿）——无降级目标可给\n")
	default:
		fmt.Fprintf(&b, "降级目标（已知良好）: DSH %s ＋ 插件 %s ＋ daemon %s（%s）\n",
			knownGood.DSHVersion, knownGood.PluginVersion, knownGood.DaemonVersion,
			knownGood.TSISO)
		if knownGood.InstallerPath != "" {
			fmt.Fprintf(&b, "安装包: %s\n", knownGood.InstallerPath)
		}
	}
	return b.String()
}

// ---- 输出分层 ----

// verdictCN 灯色中文名。
func verdictCN(v dshledger.Verdict) string {
	switch v {
	case dshledger.VerdictGreen:
		return "绿"
	case dshledger.VerdictRed:
		return "红"
	default:
		return "黄"
	}
}

// orUnknown 空版本串的展示形态。
func orUnknown(v string) string {
	if v == "" {
		return "未知"
	}
	return v
}

// printReport 分层输出：L0（三 profile 各一行＋配置面＋版本）→ L1 → L2 单行 →
// 判定与理由 → 流水行（绿时尾注绿灯总义）。
func printReport(out io.Writer, o Options, l0 []CheckResult, l1 *L1Facts, l1Err error,
	probeRes *ProbeResult, probeErr error, j Judgment, entry dshledger.Entry,
	daemonVerFromCLI bool, dshVer, pluginVer string) {
	fmt.Fprintln(out, "DSH 插件验证（ferryman verify-dsh）")
	fmt.Fprintln(out, "L0 静态（生产侧求值）:")
	if len(l0) == 1 && l0[0].Name == ChkRoot { // 根未传入的 guard 行：单项直出
		fmt.Fprintf(out, "  %s\n", l0[0].Detail)
	} else {
		profiles := o.Profiles
		if len(profiles) == 0 {
			profiles = DefaultProfiles
		}
		for _, p := range profiles {
			printProfileLine(out, l0, p)
		}
		printConfigFaceLine(out, l0)
		if dshVer != "" {
			fmt.Fprintf(out, "  安装树版本: %s（插件 %s）\n", dshVer, orUnknown(pluginVer))
		} else {
			fmt.Fprintf(out, "  安装树版本: 不可读（插件 %s）\n", orUnknown(pluginVer))
		}
	}
	fmt.Fprintln(out, "L1 挂载（daemon /dsh/health）:")
	switch {
	case l1Err != nil:
		fmt.Fprintf(out, "  L1 无从求值: %v\n", l1Err)
	case l1.NeverPolled:
		fmt.Fprintf(out, "  poll: 从未到达（守护冷启动窗或插件从未活）；宿主旁证: %s\n",
			hostCN(l1))
	case l1.PollOverdue:
		fmt.Fprintf(out, "  poll: %.0fs 前到达——已超龄（阈值 %.0fs%s）；宿主旁证: %s；被 poll 见到的会话 %d 个\n",
			l1.LastPollAgeS, l1.OverdueThresholdS, intervalNote(l1), hostCN(l1), l1.SessionCount)
	default:
		fmt.Fprintf(out, "  poll: %.0fs 前到达（阈值 %.0fs%s）；宿主旁证: %s；被 poll 见到的会话 %d 个\n",
			l1.LastPollAgeS, l1.OverdueThresholdS, intervalNote(l1), hostCN(l1), l1.SessionCount)
	}
	// L2 单行：明标「仅代表 web 宿主形态（沙箱）」（desktop 功能层验证是明示
	// 的后置边界——用户故事 7）。
	switch {
	case probeRes == nil && probeErr == nil:
		fmt.Fprintln(out, "L2 探针（仅代表 web 宿主形态（沙箱））: 未装配——整体判定降为未完成验证（不落锚不滚已知良好）")
	case probeErr != nil:
		fmt.Fprintf(out, "L2 探针（仅代表 web 宿主形态（沙箱））: 执行出错——%v\n", probeErr)
	case probeRes.Green:
		fmt.Fprintf(out, "L2 探针（仅代表 web 宿主形态（沙箱））: 全过——%s\n", probeRes.Summary)
	default:
		fmt.Fprintf(out, "L2 探针（仅代表 web 宿主形态（沙箱））: 未过——%s\n", probeRes.Summary)
	}
	fmt.Fprintf(out, "判定: %s\n", verdictCN(j.Verdict))
	for _, r := range j.Reds {
		fmt.Fprintf(out, "  - %s\n", r)
	}
	for _, y := range j.Yellows {
		fmt.Fprintf(out, "  - %s\n", y)
	}
	if daemonVerFromCLI {
		fmt.Fprintf(out, "daemon 版本: 守护不在线——流水行按 CLI 自身版本记（%s）\n",
			entry.DaemonVersion)
	}
	fmt.Fprintf(out, "流水: 已落行 %s（dsh %s＋插件 %s＋daemon %s，锚哈希 %s）\n",
		verdictCN(entry.Verdict), orUnknown(entry.DSHVersion), orUnknown(entry.PluginVersion),
		entry.DaemonVersion, orShort(entry.AnchorHash))
	if j.Verdict == dshledger.VerdictGreen {
		fmt.Fprintln(out, "绿灯总义: web 沙箱功能全验证＋生产三 profile 静态/挂载验证")
	}
}

// hostCN 宿主旁证中文短句。
func hostCN(l1 *L1Facts) string {
	if l1.HostPresent {
		return "在跑（" + l1.HostEvidence + "）"
	}
	return "不在（" + l1.HostEvidence + "）"
}

// intervalNote 阈值来源注（90s 假设兜底时明示——D13「注明假设」）。
func intervalNote(l1 *L1Facts) string {
	if l1.IntervalAssumed {
		return "，无已知间隔按 90s 假设兜底"
	}
	return ""
}

// orShort 锚哈希短形（空＝—；非空取前 12 位——人读定位够用，全文在流水行）。
func orShort(h string) string {
	if h == "" {
		return "—"
	}
	if len(h) > 12 {
		return h[:12] + "…"
	}
	return h
}

// printProfileLine 单 profile 一行：4 项过几项；未过项各缩进一行带 detail
// （票面「三 profile 各一行（L0/L1 各自结果）」的 L0 份——L1 是 daemon 全局
// 面，独立成节）。
func printProfileLine(out io.Writer, rs []CheckResult, profile string) {
	total, ok := 0, 0
	for _, r := range rs {
		if r.Profile != profile {
			continue
		}
		total++
		if r.OK {
			ok++
		}
	}
	if total == 0 {
		fmt.Fprintf(out, "  profile %s: 无检查项（profile 清单与 L0 输入不一致?）\n", profile)
		return
	}
	fmt.Fprintf(out, "  profile %s: %d/%d 过", profile, ok, total)
	if ok == total {
		fmt.Fprintln(out)
		return
	}
	fmt.Fprintln(out, "——未过项:")
	for _, r := range rs {
		if r.Profile == profile && !r.OK {
			fmt.Fprintf(out, "    %s: %s\n", r.Name, r.Detail)
		}
	}
}

// printConfigFaceLine 生产配置面一行（渡口路由/模型路由/令牌三项；版本项另出）。
func printConfigFaceLine(out io.Writer, rs []CheckResult) {
	var hits []CheckResult
	for _, r := range rs {
		if r.Profile == "" && (r.Name == ChkHomePatchRoute ||
			r.Name == ChkHomeModelRoute || r.Name == ChkHomeEnvToken) {
			hits = append(hits, r)
		}
	}
	ok := 0
	for _, r := range hits {
		if r.OK {
			ok++
		}
	}
	fmt.Fprintf(out, "  生产配置面: %d/%d 过（渡口路由/模型路由/令牌）", ok, len(hits))
	if ok == len(hits) {
		fmt.Fprintln(out)
		return
	}
	fmt.Fprintln(out, "——未过项:")
	for _, r := range hits {
		if !r.OK {
			fmt.Fprintf(out, "    %s: %s\n", r.Name, r.Detail)
		}
	}
}

// ---- --status（只读档案面） ----

// StatusOptions --status 装配参数（只读：不跑 L0/L1/L2、不落任何盘）。
type StatusOptions struct {
	DSHRoot    string
	DSHInstall string
	Profiles   []string
	DataDir    string
	Out        io.Writer
}

// Status 只读显示：当前 DSH 版本（含插件版本）vs 已知良好 vs 最近流水行；
// 当前版本不在流水时提醒未验证（黄）。恒 exit 0（状态查询不是失败，
// cmd status 同纪律）；流水打开失败如实报 exit 1。
func Status(o StatusOptions) int {
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	dshVer := readDSHVersion(o.DSHInstall)
	pluginVer := readPluginVersion(o.DSHRoot, o.Profiles)
	fmt.Fprintln(out, "DSH 验证档案（ferryman verify-dsh --status，只读）")
	fmt.Fprintf(out, "当前 DSH 版本: %s（插件 %s）\n", orUnknown(dshVer), orUnknown(pluginVer))
	if strings.TrimSpace(o.DataDir) == "" {
		fmt.Fprintln(out, "已知良好: 数据目录未传入——无从读取")
		fmt.Fprintln(out, "最近流水: 数据目录未传入——无从读取")
		return 0
	}
	log, err := dshledger.New(o.DataDir)
	if err != nil {
		fmt.Fprintf(out, "判定流水打不开: %v\n", err)
		return 1
	}
	entries, err := log.List()
	if err != nil {
		fmt.Fprintf(out, "判定流水读失败: %v\n", err)
		return 1
	}
	if kg, err := log.RecentKnownGood(); err != nil {
		fmt.Fprintf(out, "已知良好: 读取失败: %v\n", err)
	} else if kg == nil {
		fmt.Fprintln(out, "已知良好: 无（从未全绿——不声称支持范围）")
	} else {
		fmt.Fprintf(out, "已知良好: DSH %s ＋ 插件 %s ＋ daemon %s（%s）\n",
			kg.DSHVersion, kg.PluginVersion, kg.DaemonVersion, kg.TSISO)
		if kg.InstallerPath != "" {
			fmt.Fprintf(out, "  登记安装包: %s\n", kg.InstallerPath)
		}
	}
	if len(entries) == 0 {
		fmt.Fprintln(out, "最近流水: 无（首跑——ferryman verify-dsh 落第一行）")
	} else {
		e := entries[len(entries)-1]
		fmt.Fprintf(out, "最近流水: %s %s（dsh %s＋插件 %s＋daemon %s）\n",
			e.TSISO, verdictCN(e.Verdict), orUnknown(e.DSHVersion),
			orUnknown(e.PluginVersion), e.DaemonVersion)
	}
	inLedger := false
	if dshVer != "" {
		for _, e := range entries {
			if e.DSHVersion == dshVer {
				inLedger = true
				break
			}
		}
	}
	if !inLedger {
		fmt.Fprintln(out, "提醒: 未验证（当前版本不在流水）——跑 ferryman verify-dsh 验证并落行消除")
	}
	return 0
}
