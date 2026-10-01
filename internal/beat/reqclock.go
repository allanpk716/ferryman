package beat

// reqclock.go — 票02:判热时钟数据源(ADR-0015 决定一);票03:快照持久化
// (D6,修同模型摆渡重启失忆)。
//
// 口径:判热时钟 = 距该会话最后一次上游请求(含体外心跳重放)的时长。
// 台账闲置不含心跳、闸门语义继续用台账闲置——两钟各司其职,本钟专供
// 同模型判热,绝不反喂闸门/排程。
//
// 喂入方(守望,见 daemon/watcher.go):
//   ① 主转录 usage 行的转录时间戳(harvestUsage——真实上游流量;子代理
//     转录的 usage 不计,子代理前缀 ≠ 主会话前缀,不刷主会话缓存);
//   ② 真发成功的心跳重放(settleBeat/settleWaitBeat:Sent 且 OK 才计——
//     miss 亦计,上游已处理全前缀、缓存重建;ERROR 缓存状态未知不计;
//     observe 演练未真发不计)。
//
// Note 取单调 max:两源乱序到达时以最近者为准。
//
// 票03 持久化(D6):带快照路径的钟在 Note 推进时把 sid→ts 快照落盘 dataDir
// 的 reqclock.json(临时文件+替换写,防半写——F9;写失败静默:丢快照=下次
// 重启保守判冷,方向安全);构造时读快照回种(缺失/损坏=空钟起步,现状语义
// fail-safe);回种值与后续 Note 取 max(单调语义不变)。守护重启后钟不归零:
// 重启观察窗的判热门按真实钟值判,闲置 20-24 分钟、缓存仍活的会话不再被
// 整批误判冷。sid 键有界(容量上限,按 ts 淘汰——见 reqClockSnapshotMaxSids)。
//
// 观察指标口径(F8):"-1 占比回落"(same_model_skip 事件里 clock_s=-1 无观测
// 的占比)自首次快照产生之后的那次守护重启起算——此前的重启无快照可回种,
// 占比维持旧高位属预期,不构成回归。

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
)

// reqClockSnapshot 快照文件 JSON 形态:{"sessions":{"<sid>":<ts>}}。
type reqClockSnapshot struct {
	Sessions map[string]float64 `json:"sessions"`
}

// reqClockSnapshotMaxSids 快照容量上限(F9):保留最近 512 个 sid(按 ts 降序
// 截断)。判热读者只关心最近活跃会话,512 远超单机同时活跃会话数;被淘汰者
// 下次重启保守判冷,方向安全。
const reqClockSnapshotMaxSids = 512

// LastRequestClock 每会话最后上游请求时刻(epoch 秒)。零值不可用,一律
// NewLastRequestClock/NewPersistentLastRequestClock 构造;全方法并发安全
// (单互斥锁;持久形临界区含写盘——Note 只在值推进时落盘,频度=真发/usage
// 行推进,量级极低)。
type LastRequestClock struct {
	mu       sync.Mutex
	last     map[string]float64
	snapPath string // 快照落盘路径;空 = 纯内存(票02 零行为不变)
}

// NewLastRequestClock 构造空钟(纯内存零 I/O;既有调用与测试零破坏)。
func NewLastRequestClock() *LastRequestClock {
	return &LastRequestClock{last: map[string]float64{}}
}

// NewPersistentLastRequestClock 构造带快照持久化的钟(票03,D6):先读 path
// 快照回种,此后每次 Note 推进即落盘。选构造器注入而非 SetSnapshotPath
// setter:回种必须先于一切 Note(单调 max 语义只在"先回种后推进"下成立),
// setter 允许推进后再设路径,会开出"半程内存半程持久"的歧义形态;路径空 =
// 等价 NewLastRequestClock。
func NewPersistentLastRequestClock(path string) *LastRequestClock {
	c := &LastRequestClock{last: map[string]float64{}, snapPath: path}
	c.mu.Lock()
	for sid, ts := range loadReqClockSnapshot(path) {
		if sid == "" || ts <= 0 {
			continue // 空 sid 不入(与 Note 同款);非正 ts 无判热意义,防御跳过
		}
		c.last[sid] = ts
	}
	c.evictLocked() // 回种同界:手工/异常大的快照文件也截断到容量上限
	c.mu.Unlock()
	return c
}

// Note 记一次上游请求观测(单调 max;空 sid 不入——无会话身份的观测无处
// 归属,照 SnapshotStore.Capture 的跳过语义静默忽略)。票03:值推进即落盘
// 快照(节流=仅值变才写,乱序/重复观测零落盘;写盘失败静默)。
func (c *LastRequestClock) Note(sessionID string, ts float64) {
	if sessionID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last[sessionID] >= ts {
		return // 旧观测不推进(单调 max),也不重写快照
	}
	c.last[sessionID] = ts
	c.evictLocked()
	c.writeSnapshotLocked()
}

// Last 会话最后上游请求时刻。无观测返回 false(调用方据此保守判冷)。
func (c *LastRequestClock) Last(sessionID string) (float64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ts, ok := c.last[sessionID]
	return ts, ok
}

// loadReqClockSnapshot 读快照(构造期一次):缺失/不可读/损坏 → nil(空钟起步
// fail-safe,现状语义,绝不伪造热)。
func loadReqClockSnapshot(path string) map[string]float64 {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var s reqClockSnapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil
	}
	return s.Sessions
}

// evictLocked 容量淘汰(F9):超上限按 ts 降序截断,只留最近
// reqClockSnapshotMaxSids 个 sid;内存与快照同界——淘汰即"忘掉"该会话的
// 上游请求观测,其下次重启保守判冷,方向安全。须持 c.mu(Note 内);构造期
// 持锁直调同款。
func (c *LastRequestClock) evictLocked() {
	if len(c.last) <= reqClockSnapshotMaxSids {
		return
	}
	type kv struct {
		sid string
		ts  float64
	}
	all := make([]kv, 0, len(c.last))
	for sid, ts := range c.last {
		all = append(all, kv{sid, ts})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ts > all[j].ts })
	for _, e := range all[reqClockSnapshotMaxSids:] {
		delete(c.last, e.sid)
	}
}

// writeSnapshotLocked 快照落盘(须持 c.mu):临时文件+os.Rename 替换写——
// 读者永远只见完整旧文件或完整新文件,绝不半写(F9;同目录原子替换,Windows
// 即 MoveFileEx REPLACE_EXISTING)。写失败一律静默:丢快照的最坏后果 = 下次
// 重启该会话保守判冷(F5 同款方向),绝不影响 Note 主路径。
func (c *LastRequestClock) writeSnapshotLocked() {
	if c.snapPath == "" {
		return
	}
	b, err := json.Marshal(reqClockSnapshot{Sessions: c.last})
	if err != nil {
		return // map[string]float64 无可失败形态;防御同款静默
	}
	tmp := c.snapPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, c.snapPath)
}
