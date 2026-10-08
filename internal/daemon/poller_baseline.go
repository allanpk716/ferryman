package daemon

// poller_baseline.go — dsh-host-guard 票02：poller 身份心跳基线＋持久化＋生命
// 周期状态机＋跃迁日志（spec A/E/F;票面验收六条）。
//
// 基线（spec E）：daemon 数据目录维护 dsh-pollers.json（poller → {first_seen,
// last_seen, offline}）;变化时惰性落盘（心环节律下至多每轮一次——/dsh/poll
// 入账后统一收口）;损坏即弃、按后续心跳重建（读不动/解析败→空表起步＋一行
// 日志,绝不炸接收面）;启动加载＝首口心跳/首问读面时自盘恢复（跨重启记忆,
// 「插件先死、daemon 后重启」可检出——重启后无新流量,检出正落在读面问询,
// 故加载同时挂心跳面与读面）。
//
// 生命周期（spec E 矩阵,判定优先级自上而下）：
//   offline＝显式下线标记（插件 dispose 尽力上报一次;显式证据压过一切推断,
//             静默再久都不算 stale/retired）
//   retired＝静默>24h（自然退役——F8 检测视界外,doctor pass 注记,不作 fail）
//   stale  ＝24h 内有心跳 ∧ 无下线标记 ∧ 静默>90s（应在线而沉默）
//   online ＝其余（最近心跳在新鲜窗内）
//
// 跃迁日志（spec F）：「活→静默」跃迁（online→stale）打一行
// `[poller] <名> 静默>90s(最近心跳 <时间>)`——跃迁制,持续静默不重复打;
// 复活（任一真实心跳,含下线标记撤销）后再度沉默＝新的一轮静默,可再打。
// 自盘恢复条目的内存评估态为空——首轮判定 stale 属状态确立非跃迁,不打行
//（「只在 online→stale 跃迁时打」的严格口径;该形态的检出走 /stats 读面,
// 票 03 消费）。
//
// 评估节律（如实声明）：守望主循环（watcher.go Run/pollGuarded）不在本票涉及
// 路径,周期扫描挂不进——评估挂接收面心环节律：每次 /dsh/poll 入账后全表评估
// 一轮（任一插件活着时至少每 30s 一轮,跃迁行打点延迟≤一轮 poll 间隔）。全员
// 死亡形态（再无 poll 流量→无评估轮）由 /stats 读面兜底检出（票 03 watchdog
// 每 5min 消费,spec F 既有面）——读面为纯计算,不依赖评估轮。
//
// 并发纪律：包级态＋独立小锁（compact.go dshLiveMu 票01 同款——daemon.go 不
// 在本票涉及路径、Daemon 字段挂不进;单守护进程现实下等价 Daemon 字段,测试
// 经 resetDshPollers 复位防跨用例渗漏）。锁内纯内存;文件 IO（加载/落盘）与
// 日志一律锁外（snapshot 锁内值拷贝,写盘不与心跳写共享指针）;评估只经本小锁
// 串行化,不嵌套其他锁（无锁序约束）。
//
// 心跳只认接收面真流量：入账挂 doDshReceive 拦截器（/dsh/poll 分支,dsh_receive.go）,
// poll 体缺 poller（旧协议体）或非字符串坏形不入账不判（spec A 键语义）。dispose
// 下线上报＝同口 offline 变体 {agent:"dsh", poller, offline:true, sessions:[]}
//（协议沿用票 01 poll 体形状;daemon 收到置 offline 标记,下一轮真实心跳即消除,
// F9「下轮心跳即消除」）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"ferryman/internal/clock"
)

// 时间常量（spec E 钉死,带依据）：
const (
	// dshPollerFreshS 心跳新鲜窗（90s＝3× 缺省轮询间隔 30s——连续 3 轮未到即
	// 视为沉默;spec E「静默 >90 秒(3× 默认轮询间隔)」逐字）。
	dshPollerFreshS = 90.0
	// dshPollerRetireS 退役窗（24h 检测视界——F8:静默超 24h 的 poller 按自然
	// 退役处理,doctor pass 注记,不作 fail）。
	dshPollerRetireS = 86400.0
)

// 生命周期状态值（spec F /stats state 闭集）。
const (
	pollerOnline  = "online"
	pollerStale   = "stale"
	pollerRetired = "retired"
	pollerOffline = "offline"
)

// pollerEntry 基线一条。FirstSeen/LastSeen/Offline 落盘;lastState＝跃迁评估
// 的内存态（json 忽略非导出字段——不落盘;重启后首轮评估＝状态确立非跃迁）。
type pollerEntry struct {
	FirstSeen float64 `json:"first_seen"`
	LastSeen  float64 `json:"last_seen"`
	Offline   bool    `json:"offline"`
	lastState string
}

var (
	dshPollerMu      sync.Mutex
	dshPollerEntries = map[string]*pollerEntry{}
	dshPollerLoaded  bool // 进程内只加载一次（test-and-set;坏盘也不重读——每轮重读纯浪费）
	dshPollerDirty   bool // 变化标记（惰性落盘:评估轮收口时才写）
)

// pollerLogf poller 日志缝（compactLogf 同款 var 形＝测试捕获）。缺省 stdout。
var pollerLogf = func(format string, args ...any) {
	fmt.Printf(format, args...)
}

// dshPollerPath 基线文件路径（<DataDir>/dsh-pollers.json;Cfg nil＝无盘面,只
// 内存——旧测试直构形态）。
func (d *Daemon) dshPollerPath() string {
	if d.Cfg == nil {
		return ""
	}
	return filepath.Join(d.Cfg.DataDir(), "dsh-pollers.json")
}

// dshPollerEnsureLoaded 自盘加载（进程内一次;首口心跳/首问读面惰性触发——
// NewDaemon 在 daemon.go 本票路径外、启动钩子不可达,DshGate 族系回种的同款
// 惰性装配先例）。文件不存在＝首建前盲区（F8:不判任何沉默）;读不动/损坏即弃
//（空表起步＋一行日志,后续心跳重建）。加载标记在锁内先行 test-and-set,文件
// IO 在锁外（小锁内纯内存纪律）;合并只补缺——不覆盖进程内已入账条目。
func (d *Daemon) dshPollerEnsureLoaded() {
	dshPollerMu.Lock()
	if dshPollerLoaded {
		dshPollerMu.Unlock()
		return
	}
	dshPollerLoaded = true
	path := d.dshPollerPath()
	dshPollerMu.Unlock()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return // 不存在（首建盲区）/读不动：空表起步
	}
	var fromDisk map[string]*pollerEntry
	if err := json.Unmarshal(data, &fromDisk); err != nil {
		pollerLogf("[poller] 基线损坏已弃置,按后续心跳重建:%s\n", path)
		return
	}
	dshPollerMu.Lock()
	for name, e := range fromDisk {
		if e == nil {
			continue
		}
		if _, ok := dshPollerEntries[name]; !ok {
			e.lastState = "" // 未评估态:首轮 sweep 确立现状,不判跃迁不打行
			dshPollerEntries[name] = e
		}
	}
	dshPollerMu.Unlock()
}

// dshPollerBeat 心跳入账（接收面 /dsh/poll 调用;offline=true＝插件 dispose 的
// 下线上报变体）。真实心跳即活的证据：撤销下线标记（F9「下轮心跳即消除」）并
// 把评估态复位 online——复活后再沉默是新的一轮静默,跃迁行可再打。入账后全表
// 评估一轮＋惰性落盘（心环节律收口,至多每轮一次）。
func (d *Daemon) dshPollerBeat(name string, offline bool, now float64) {
	d.dshPollerEnsureLoaded()
	dshPollerMu.Lock()
	e := dshPollerEntries[name]
	if e == nil {
		e = &pollerEntry{FirstSeen: now}
		dshPollerEntries[name] = e
	}
	e.LastSeen = now
	e.Offline = offline
	if !offline {
		e.lastState = pollerOnline // 复活:评估态复位,再沉默可再打跃迁行
	}
	dshPollerDirty = true // last_seen 前进也是变化（落盘保真;至多每轮一次）
	dshPollerMu.Unlock()
	d.dshPollerSweep(now)
}

// dshPollerBeatFromPoll /dsh/poll 体→心跳（doDshReceive 拦截器调用）。poller
// 只认字符串非空（缺键＝旧协议体、非字符串＝坏形:一律不入账不判,坏形静默
// 收窄纪律）;offline 非布尔/缺省＝false。
func (d *Daemon) dshPollerBeatFromPoll(body map[string]any) {
	name, _ := body["poller"].(string)
	if name == "" {
		return
	}
	offline := false
	if b, ok := body["offline"].(bool); ok {
		offline = b
	}
	d.dshPollerBeat(name, offline, clock.Now())
}

// dshPollerSweep 全表生命周期评估（心环节律收口;HTTP 线程经小锁串行）：
// 逐条算现态、更新内存评估态、收「online→stale」跃迁行;日志与落盘在锁外
//（小锁内纯内存纪律——noteDshCompactUndelivered 同款收口形态）。
func (d *Daemon) dshPollerSweep(now float64) {
	var transitions []string
	dshPollerMu.Lock()
	for name, e := range dshPollerEntries {
		st := dshPollerStateOf(e, now)
		if st != e.lastState {
			if e.lastState == pollerOnline && st == pollerStale {
				transitions = append(transitions, fmt.Sprintf(
					"[poller] %s 静默>90s(最近心跳 %s)", name,
					time.Unix(int64(e.LastSeen), 0).Format("2006-01-02 15:04:05")))
			}
			e.lastState = st
		}
	}
	dirty := dshPollerDirty
	dshPollerDirty = false
	dshPollerMu.Unlock()
	for _, line := range transitions {
		pollerLogf("%s\n", line)
	}
	if dirty {
		d.dshPollerSave()
	}
}

// dshPollerStateOf 生命周期状态判定（spec E 矩阵单源;/stats 读面与评估轮共
// 用同一判定）。判定用严格大于：恰 90s 仍 online、恰 24h 仍 stale。
func dshPollerStateOf(e *pollerEntry, now float64) string {
	if e.Offline {
		return pollerOffline // 显式下线压过一切推断（静默再久都不算 stale/retired）
	}
	silence := now - e.LastSeen
	if silence > dshPollerRetireS {
		return pollerRetired // 静默>24h:自然退役（F8 视界外,pass 注记）
	}
	if silence > dshPollerFreshS {
		return pollerStale // 24h 内有心跳 ∧ 无下线 ∧ 静默>90s:应在线而沉默
	}
	return pollerOnline
}

// dshPollerInfo 读面快照一条（/stats pollers 段与 doctor 检查项的数据源——
// 票 03 消费;spec E/F）。State 为纯计算现值：读面不评估、不落盘、不打跃迁行。
type dshPollerInfo struct {
	Name      string
	FirstSeen float64
	LastSeen  float64
	Offline   bool
	State     string // online|stale|retired|offline
}

// dshPollerStates 全表读面（按名字排序;首问触发惰性加载——「插件先死、
// daemon 后重启」的检出正是读面问到的形态,加载必须也挂读面）。
func (d *Daemon) dshPollerStates() []dshPollerInfo {
	d.dshPollerEnsureLoaded()
	now := clock.Now()
	dshPollerMu.Lock()
	out := make([]dshPollerInfo, 0, len(dshPollerEntries))
	for name, e := range dshPollerEntries {
		out = append(out, dshPollerInfo{Name: name, FirstSeen: e.FirstSeen,
			LastSeen: e.LastSeen, Offline: e.Offline, State: dshPollerStateOf(e, now)})
	}
	dshPollerMu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// dshPollerSave 惰性落盘（snapshot 锁内值拷贝、写盘锁外;失败静默——基线尽力
// 而为,绝不弄断接收面;MkdirAll 兜底数据目录未建形态;失败等下轮心跳重写）。
func (d *Daemon) dshPollerSave() {
	dshPollerMu.Lock()
	path := d.dshPollerPath()
	snap := make(map[string]pollerEntry, len(dshPollerEntries))
	for name, e := range dshPollerEntries {
		snap[name] = *e // 值拷贝——锁外 marshal 不与心跳写共享指针
	}
	dshPollerMu.Unlock()
	if path == "" {
		return
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

// resetDshPollers 包级基线态复位（测试卫生,resetCompactMiss 同纪律）。
func resetDshPollers() {
	dshPollerMu.Lock()
	dshPollerEntries = map[string]*pollerEntry{}
	dshPollerLoaded = false
	dshPollerDirty = false
	dshPollerMu.Unlock()
}
