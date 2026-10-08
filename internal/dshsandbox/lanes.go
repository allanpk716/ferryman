// Package dshsandbox verify-dsh 票05：L2 沙箱插件探针。
//
// 职责（spec「L2 插件探针」节逐句契约＋票面钉死项，一条不许放宽）：
//   - 沙箱起栈（stack.go）：沙箱 daemon＝os.Executable() 自身 exe 起第二实例
//     （绝无 go build——不依赖 Go 工具链/仓库洁净），带沙箱 env＋
//     `serve --port <panel> --no-tray --no-browser --smoke` 形态（参数与 env 钉法
//     照 tools/e2e_dsh/start.sh:85-98 既有形状，Go 重写不 shell 出 bash）；
//     沙箱 DSH 宿主＝$DSH_CLI web --no-open --port <web>（DSH_HOME 钉沙箱）；
//     插件备料同构＝junction 指向备料快照（仓库 plugin/ferryman-dsh 拷贝）＋
//     node_modules 拷贝保持（镜像生产安装形态）。
//   - 探针（probe.go）：建探针会话→发一条真消息（真模型，走沙箱 daemon 配置
//     的渡口上游）→断言四痕（闸门到达/事件上报/会话键/注入路径）＋压缩链两道。
//   - 断言器（lanes.go，本文件）：纯函数，表驱动单测的受测面。
//   - 契约锚采集（anchor_collect.go）：从沙箱实际形状采集四类契约面签名交
//     dshledger（票03 Write/DiffFaces；键＝FaceDiscovery/FaceEvents/FaceDuck/
//     FaceBrowser 四常量）。
//
// 压缩链两道互不抵消（用户拍板「都做」，D9；任何放宽需用户重新确认）：
// 命令道（插件经 ctx.commands execute(agent,'/compact',…)）与服务面（插件经
// compaction.compactNow）**各自**执行一次压缩并**各自**见到 /dsh/compacted
// 上报；任一道失败该道红灯，另一道通过不抵消；某道契约面不可达记
// 「契约缺失：××道」红灯（发现契约漂移的信号，不是放宽验收的理由）。
// 归因证据＝会话转录：命令道经宿主命令注册表执行，executor 落
// command/run・command/done 生命周期事件于转录（dsh interaction/commands
// service.ts「durably logged the lifecycle」钉点）；服务面直调 compactNow
// 无命令生命周期。真机 web 宿主把 compaction 服务隔离在 preset 组内、插件域
// 恒不可达（2026-10-07 E2E 实锚）——探针因此为服务面腿启用合成组成：沙箱
// profile 追加 profile 层 compaction-basic insert（服务面对插件可见）＋
// agentPreset=minimal 的会话（无 /compact 命令注册，命令道 undefined 落服务面）。
// 合成组成腿失败按观察事实记（fail/契约缺失），绝不以命令道通过抵消。
//
// 零闪窗铁律：一切子进程（沙箱 exe/DSH CLI/cmd mklink/taskkill）经本包唯一
// 进程创建工厂 spawnProc（proc.go），Windows 分支恒 SysProcAttr{HideWindow:true}。
// 隔离铁闸：全部端口落 25xxx 段（生产 15700/15722/3080/3081/15900 绝不触碰）；
// 起栈前对生成面做生产端口残迹扫描（isolation_scan 同款）；端口避让走文件锁
// （与 e2e 并跑时抢不到换 25904-25909 段并记录）。
package dshsandbox

import (
	"fmt"
	"strings"
)

// ---- 四痕＋两道的断言项名（稳定标识：Summary/诊断面按名取项） ----

const (
	ChkGateArrival = "gate_arrival" // 闸门到达：该消息的 /dsh/gate 问询在沙箱 daemon 落痕
	ChkEventReport = "event_report" // 事件上报：usage 科目有该 sid 行
	ChkSessionKey  = "session_key"  // 会话键：sid 呈 session-<uuid> 形态
	ChkInjectPath  = "inject_path"  // 注入路径：/dsh/handoff 通道可达（零副作用探针）
	ChkLaneCommand = "lane_command" // 压缩链·命令道（execute '/compact'）独立判定
	ChkLaneService = "lane_service" // 压缩链·服务面（compactNow）独立判定
)

// TraceFacts 四痕观察事实（probe.go 从沙箱 daemon/账本/转录采集；纯数据）。
type TraceFacts struct {
	// GateDelta 探针消息窗内沙箱 daemon /stats gate_calls_by_agent["dsh"] 增量
	//（>0＝该消息的闸门问询到达。allow 路径不落 gate.log/账本，stats 计数是
	// daemon 面对每次 Gate() 调用的唯一普适落痕——health.go Stats.Hit）。
	GateDelta int
	// GateDetail 到达证据明细（如「前后快照 3→4」）。
	GateDetail string
	// UsageRows usage 科目该 sid 的行数（>0＝事件上报在账；行带 token 四列）。
	UsageRows int
	// UsageDetail 明细（如行内的计费输入合计）。
	UsageDetail string
	// SID 探针会话 sid（形态断言的受验值）。
	SID string
	// SIDSource sid 来源明细（宿主 session/create 签发——非探针自造，防自证）。
	SIDSource string
	// HandoffOK /dsh/handoff 零副作用探针（空 cwd，插件 selfcheck 同款先例）200。
	HandoffOK bool
	// HandoffDetail 明细（HTTP 状态或失败因）。
	HandoffDetail string
}

// laneName 两道的用户可见名（红灯文案「契约缺失：××道」的 ××）。
const (
	LaneCommandName = "命令道"
	LaneServiceName = "服务面"
)

// LaneFact 单道压缩链观察事实。归因面：ExecViaCommand＝该道执行在会话转录
// 留下 /compact 命令生命周期（command/run…compact）；ExecViaService＝有压缩
// 而无命令生命周期（compactNow 直调形态）。两者互斥由采集侧保证（转录扫描）。
type LaneFact struct {
	Name string // LaneCommandName / LaneServiceName
	// SessionID 该道派发目标会话（命令道＝探针主会话；服务面＝合成组成会话）。
	SessionID string
	// Dispatched daemon 侧压缩指令已派发（触发日志/领取窗起算）且被插件领取。
	Dispatched bool
	// ReportOK 该道执行后见到 ok=true 的 kind=compacted 上报（该 sid）。
	ReportOK bool
	// ReportReason 非 ok 上报的 reason（busy/error/no-agent/no-compaction-channel…；
	// 多条以「;」连接；空＝无失败上报）。
	ReportReason string
	// ExecViaCommand 转录含 compact 命令生命周期（命令道执行痕）。
	ExecViaCommand bool
	// ExecViaService 有压缩事件而无命令生命周期（服务面执行痕）。
	ExecViaService bool
	// ContractMissing 该道契约面不可达（如 compactNow 在插件域取不到、
	// 服务面合成组成起不来）——记「契约缺失：××道」红灯，不算跳过。
	ContractMissing bool
	// Detail 补充明细（失败时必指位）。
	Detail string
}

// ItemResult 单项断言结果（lanes 与四痕共用形状；Name＝Chk* 常量）。
type ItemResult struct {
	Name   string
	OK     bool
	Detail string
}

// SIDIsSessionUUID sid 是否呈 session-<uuid> 形态（宿主 session-controller
// `session-${randomUUID()}` 签发形状，node:crypto randomUUID 恒小写 hex）。
func SIDIsSessionUUID(sid string) bool {
	if len(sid) != len("session-")+36 {
		return false
	}
	if !strings.HasPrefix(sid, "session-") {
		return false
	}
	u := sid[len("session-"):]
	for i, c := range u {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
	}
	return true
}

// Evaluate 四痕＋压缩链两道 → 断言项列表（表驱动单测的受测面；纯函数）。
// 项序：闸门到达/事件上报/会话键/注入路径/命令道/服务面（输出层按序直出）。
// 两道各自独立判定——任一道失败该道 fail，另一道通过不抵消；契约缺失也是
// fail（「契约缺失：××道」进 detail），绝不是跳过（票面钉死）。
func Evaluate(t TraceFacts, cmd, svc LaneFact) []ItemResult {
	items := []ItemResult{
		{
			Name:   ChkGateArrival,
			OK:     t.GateDelta > 0,
			Detail: gateDetail(t),
		},
		{
			Name:   ChkEventReport,
			OK:     t.UsageRows > 0,
			Detail: usageDetail(t),
		},
		{
			Name:   ChkSessionKey,
			OK:     SIDIsSessionUUID(t.SID),
			Detail: fmt.Sprintf("sid=%s（来源：%s）", t.SID, t.SIDSource),
		},
		{
			Name:   ChkInjectPath,
			OK:     t.HandoffOK,
			Detail: t.HandoffDetail,
		},
		evalLane(ChkLaneCommand, cmd),
		evalLane(ChkLaneService, svc),
	}
	return items
}

// evalLane 单道判定（两道共用；独立判定互不抵消的落点）。
func evalLane(name string, l LaneFact) ItemResult {
	r := ItemResult{Name: name}
	switch {
	case l.ContractMissing:
		r.Detail = fmt.Sprintf("契约缺失：%s——%s", l.Name, l.Detail)
		return r
	case !l.Dispatched:
		r.Detail = fmt.Sprintf("%s未见派发（daemon 未触发或插件未领取）——%s",
			l.Name, l.Detail)
		return r
	case !l.ReportOK && l.ReportReason != "":
		r.Detail = fmt.Sprintf("%s上报未过（reason=%s）——%s",
			l.Name, l.ReportReason, l.Detail)
		return r
	case !l.ReportOK:
		r.Detail = fmt.Sprintf("%s未见 ok=true 上报——%s", l.Name, l.Detail)
		return r
	}
	// 上报 ok：归因到本道才算过（命令道要命令生命周期；服务面要无命令生命
	// 周期的压缩痕）——上报 ok 但归因到另一道＝本道未执行，不抵消。
	if name == ChkLaneCommand && !l.ExecViaCommand {
		r.Detail = fmt.Sprintf("上报 ok 但未见命令道执行痕（转录无 /compact 命令生命周期）"+
			"——执行走了%s——%s", laneOtherName(l), l.Detail)
		return r
	}
	if name == ChkLaneService && !l.ExecViaService {
		r.Detail = fmt.Sprintf("上报 ok 但未见服务面执行痕（转录含 /compact 命令生命周期＝"+
			"执行走了命令道，服务面未执行）——%s", l.Detail)
		return r
	}
	r.OK = true
	r.Detail = fmt.Sprintf("%s执行＋上报 ok（sid=%s）——%s",
		l.Name, l.SessionID, l.Detail)
	return r
}

// laneOtherName 归因不在本道时的另一道名（detail 指因用）。
func laneOtherName(l LaneFact) string {
	if l.ExecViaService {
		return "服务面"
	}
	return "非两道已知形态"
}

func gateDetail(t TraceFacts) string {
	if t.GateDetail != "" {
		return t.GateDetail
	}
	if t.GateDelta > 0 {
		return fmt.Sprintf("/dsh/gate 问询到达（计数 +%d）", t.GateDelta)
	}
	return "探针消息窗内 /dsh/gate 计数零增量（插件未问询？）"
}

func usageDetail(t TraceFacts) string {
	if t.UsageDetail != "" {
		return t.UsageDetail
	}
	if t.UsageRows > 0 {
		return fmt.Sprintf("usage 科目该 sid %d 行", t.UsageRows)
	}
	return "usage 科目无该 sid 行（事件上报断链？）"
}

// AllGreen 全部断言项过（ProbeResult.Green 的判定源）。
func AllGreen(items []ItemResult) bool {
	for _, it := range items {
		if !it.OK {
			return false
		}
	}
	return true
}

// Summarize 断言项 → 单行摘要（ProbeResult.Summary；未过项逐项点名，绿时
// 给全过短句）。压缩链两道在摘要里各自可见（互不抵消的输出面）。
func Summarize(items []ItemResult) string {
	if AllGreen(items) {
		return fmt.Sprintf("四痕＋压缩链两道全过（%d 项）", len(items))
	}
	var fails []string
	for _, it := range items {
		if it.OK {
			continue
		}
		short := it.Detail
		if i := strings.Index(short, "——"); i > 0 {
			short = short[:i]
		}
		fails = append(fails, fmt.Sprintf("%s: %s", it.Name, short))
	}
	return fmt.Sprintf("%d/%d 过；未过: %s", len(items)-len(fails), len(items),
		strings.Join(fails, "；"))
}
