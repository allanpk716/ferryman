package daemon

// dsh_liveness.go — verify-dsh 票01：daemon 侧 L1 挂载记账＋宿主进程旁证＋
// /dsh/health 只读查询端点（spec「三层检测与进程模型」L1 节＋「灯色」节，
// 决策 D1/D10/D13）。
//
// 职责边界（D1 钉死）：只记账、不判定、不告警、无常驻循环——/dsh/poll 到达
// 即盖章（钩子一处，dsh_receive.go 拦截器内），/dsh/health 被问才现算阈值
// 与超龄；灯色判定（绿/黄/红＋是否推送，D10）在票 04 消费方，本文件只供数
// 据（poll_overdue、host_processes_present 两布尔＋证据摘要）。记账全内存
// （daemon 重启冷启动可接受，票面明示；PendingTable 同款取舍）。
//
// 阈值语义（D13）：超龄阈值＝3×生效 poll 间隔；生效间隔取 hint（config
// poll_hint_s，即 daemon 经 poll 应答下发的建议间隔）优先、实测节律（相邻
// 两轮 poll 间隔的中位数，环形窗抗单轮毛刺）兜次；两者皆无＝90s 兜底并在
// 返回中注明 interval_assumed=true。poll_overdue 只在见过 poll 后才可能为
// true——从未见过 poll（重启冷启动窗）＝无证据不判定（last_poll_age_s 回
// null、poll_overdue 恒 false），"宿主在而从未 poll"的形态 A 判定由票 04
// 消费方结合宿主旁证与流水状态合成，本面如实报账不越权。
//
// 并发纪律：记账态包级＋独立小锁（dshCompactMissMu 同款——daemon.go 结构
// 不动，单守护进程现实下等价 Daemon 字段），临界区纯内存，绝不与其他锁嵌
// 套。宿主旁证在 /dsh/health 求值时现跑（tasklist ≤3s＋环回拨号 ≤1s，只读
// 手动诊断面可接受），无缓存无后台 goroutine（D1 同源）。

import (
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ferryman/internal/clock"
	"ferryman/internal/mathx"
)

// dshHarnessImage DSH 宿主进程映像名（.scratch/dsh-proc-audit.ps1 实锚：
// Win32_Process Name='DeepSeek Harness.exe'）。
const dshHarnessImage = "DeepSeek Harness.exe"

const (
	// dshLiveGapWindow 实测节律环形窗容量（近 N 轮间隔取中位数，抗单轮毛刺）。
	dshLiveGapWindow = 8
	// dshLiveSidTTL sid last-seen 陈账懒剪线（PendingTTLs 86400s 同值先例：
	// 常态几十个 sid，每轮到达顺手全扫成本可忽略）。
	dshLiveSidTTL = 86400.0
	// dshProbeDialTimeout 3080 监听旁证拨号上界（环回拒连即刻返回，超时仅
	// 防意外挂起拖死 /dsh/health）。
	dshProbeDialTimeout = time.Second
	// dshNoIntervalFallbackS D13：无任何已知间隔时的超龄阈值兜底（返回中
	// 注明 interval_assumed=true）。
	dshNoIntervalFallbackS = 90.0
)

// ---- L1 记账态（包级＋独立小锁，dshCompactMiss 同纪律） ----

var (
	dshLiveMu sync.Mutex
	// dshLiveLastPoll 全局最近 poll 到达时刻（0=本进程 lifecycle 内未见过 poll）。
	dshLiveLastPoll float64
	// dshLiveSidSeen sid → 最近被 poll 见到时刻（宿主近似粒度）。
	dshLiveSidSeen map[string]float64
	// dshLiveGaps 近若干轮 poll 间隔（实测节律原料，环形截断）。
	dshLiveGaps []float64
)

// noteDshLivenessPoll L1 挂载记账（/dsh/poll 到达即盖章；dsh_receive.go 拦
// 截器内钩子，DshPoll 业务口不加摊——compact.go 零改动）：全局最近 poll 时
// 刻＋按 sid 的最近被 poll 见到时刻＋实测节律（与上一轮的间隔入环形窗）。
// 坏形静默收窄（DshPoll 同纪律：sessions 非数组/元素缺 sid 跳过，不炸不进
// 脏账）；poll 到达本身无条件盖章全局时刻（payload 空也是"插件活着"证据）。
func noteDshLivenessPoll(body map[string]any) {
	now := clock.Now()
	dshLiveMu.Lock()
	defer dshLiveMu.Unlock()
	if dshLiveLastPoll > 0 {
		if gap := now - dshLiveLastPoll; gap > 0 { // 时钟回拨不记负间隔
			dshLiveGaps = append(dshLiveGaps, gap)
			if len(dshLiveGaps) > dshLiveGapWindow {
				dshLiveGaps = dshLiveGaps[len(dshLiveGaps)-dshLiveGapWindow:]
			}
		}
	}
	dshLiveLastPoll = now
	list, _ := body["sessions"].([]any)
	for _, s := range list {
		m, ok := s.(map[string]any)
		if !ok {
			continue
		}
		if sid := pyStr(m["sid"]); sid != "" {
			if dshLiveSidSeen == nil { // 直构/零值形态懒建（PendingTable 同款）
				dshLiveSidSeen = map[string]float64{}
			}
			dshLiveSidSeen[sid] = now
		}
	}
	for sid, ts := range dshLiveSidSeen { // 懒剪陈账
		if now-ts > dshLiveSidTTL {
			delete(dshLiveSidSeen, sid)
		}
	}
}

// DshHealth /dsh/health 数据源（verify-dsh 票01；灯色判定在票 04 消费方，
// 本面只供数据——文件头「职责边界」节）。只读：零状态写入、零消息内容、
// 零凭据（queryapi.go 红线）。字段：
//   - last_poll_age_s：全局最近 poll 年龄秒（null=本进程内未见过 poll）；
//   - sessions：sid → 最近被 poll 见到年龄秒；
//   - poll_hint_s / poll_rhythm_s：生效间隔两依据原值（rhythm null=不足两轮）；
//   - interval_source ＋ interval_assumed ＋ effective_interval_s ＋
//     overdue_threshold_s：生效间隔依据（hint|rhythm|none）与推导阈值（D13：
//     3×生效间隔，无已知间隔 90s 兜底且 assumed=true）；
//   - poll_overdue：全局最近 poll 年龄 > 阈值（从未 poll 恒 false，见上）；
//   - host_processes_present ＋ host_evidence：宿主进程旁证布尔＋两腿证据
//     摘要（harness 进程腿∨3080 端口腿，任一即"宿主在跑"）。
func (d *Daemon) DshHealth() map[string]any {
	now := clock.Now()
	dshLiveMu.Lock()
	lastPoll := dshLiveLastPoll
	sessions := make(map[string]float64, len(dshLiveSidSeen))
	for sid, ts := range dshLiveSidSeen {
		sessions[sid] = mathx.Round(now-ts, 1)
	}
	gaps := append([]float64(nil), dshLiveGaps...)
	dshLiveMu.Unlock()

	hint := 0.0
	if d.Cfg != nil {
		hint = d.Cfg.DshCompact.PollHintS
	}
	rhythm := 0.0
	if len(gaps) >= 2 { // 单轮间隔不称"节律"（无从核对）
		rhythm = dshMedianGap(gaps)
	}
	// 生效间隔（D13）：hint 优先（daemon 自己下发的建议值，最可知）、实测
	// 节律兜次（真跑出来的，比 90s 拍脑袋强）、全无才兜底并注明假设。
	eff, src, assumed := 0.0, "", false
	switch {
	case hint > 0:
		eff, src = hint, "hint"
	case rhythm > 0:
		eff, src = rhythm, "rhythm"
	default:
		eff, src, assumed = dshNoIntervalFallbackS, "none", true
	}
	threshold := 3 * eff
	var lastAge, rhythmOut any // null 形（Health 的 last_*_s_ago 同款）
	overdue := false
	if lastPoll > 0 {
		age := now - lastPoll
		lastAge = mathx.Round(age, 1)
		overdue = age > threshold
	}
	if rhythm > 0 {
		rhythmOut = mathx.Round(rhythm, 1)
	}
	present, evidence := dshHostProbe()
	return map[string]any{
		"last_poll_age_s":        lastAge,
		"sessions":               sessions,
		"poll_hint_s":            hint,
		"poll_rhythm_s":          rhythmOut,
		"interval_source":        src,
		"interval_assumed":       assumed,
		"effective_interval_s":   eff,
		"overdue_threshold_s":    threshold,
		"poll_overdue":           overdue,
		"host_processes_present": present,
		"host_evidence":          evidence,
	}
}

// handleDshHealth /dsh/health handler（queryapi.go 注册表挂载；鉴权与
// 127.0.0.1 绑定复用既有面，/stats 同域）。
func handleDshHealth(d *Daemon, w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.DshHealth())
}

// dshMedianGap 间隔中位数（偶数取中间两值均值；环形窗小，排序成本可忽略）。
func dshMedianGap(gaps []float64) float64 {
	s := append([]float64(nil), gaps...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// ---- 宿主进程旁证（spec L1：枚举 DeepSeek Harness 进程/3080 监听） ----

// dshHostProbePort 监听旁证的目标口（web 宿主 3080——DSH 宿主基线"桌面树＋
// web 3080 两处"；var 形＝测试可指向随身监听器）。
var dshHostProbePort = 3080

// dshHarnessCount DSH 宿主进程枚举缝：返回 (进程数, 枚举是否可知, 证据明细)。
// "不可知"（tasklist 失败/非 Windows）不冒充"不在"——旁证布尔由端口腿独立
// 裁决。平台分支在 dsh_liveness_windows.go / dsh_liveness_other.go 经 init
// 注入缺省（update.txLauncher 同惯例）。
var dshHarnessCount = func() (n int, known bool, detail string) {
	return 0, false, "harness=unknown(平台分支未装配)"
}

// dshProbePortListen 监听旁证腿：环回拨号探 3080（纯 Go 零 console——比
// netstat 解析省一个子进程面）。连接成功即刻关闭＝有监听（宿主侧至多一行
// 空请求日志）；拒连/超时＝quiet。
func dshProbePortListen(port int) (detail string, ok bool) {
	c, err := net.DialTimeout("tcp",
		net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), dshProbeDialTimeout)
	if err != nil {
		return fmt.Sprintf("port%d=quiet", port), false
	}
	_ = c.Close()
	return fmt.Sprintf("port%d=listening", port), true
}

// dshHostProbe 宿主旁证缝（var 形＝测试注入假实现，compactLogf 惯例）：
// 旁证布尔＝harness 进程腿 ∨ 3080 端口腿——桌面宿主形态 web 口未必开、映像
// 名改动时另一腿兜住，"宿主在跑"取并集；两腿皆无才报不在。证据摘要两腿并
// 报（票 04 灯色文案与人工排障的原料）。
var dshHostProbe = func() (present bool, evidence string) {
	portDetail, portOK := dshProbePortListen(dshHostProbePort)
	n, known, harnessDetail := dshHarnessCount()
	return portOK || (known && n > 0), harnessDetail + "; " + portDetail
}

// dshParseTasklistOutput tasklist /FO CSV /NH 输出解析：数 DeepSeek Harness
// 行。无匹配时的本地化 INFO 行（"信息:/INFO: …"）不以引号起头，前缀判定天
// 然排除；映像名含匹配大小写不敏感（回显大小写差异不吃惊）。行尾 \r 由
// TrimSpace 收掉。
func dshParseTasklistOutput(out string) int {
	n := 0
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, `"`) &&
			strings.Contains(strings.ToLower(ln), strings.ToLower(dshHarnessImage)) {
			n++
		}
	}
	return n
}
