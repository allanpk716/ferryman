package beat

// reqclock.go — 票02:判热时钟数据源(本票钉死,ADR-0015 决定一)。
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
// Note 取单调 max:两源乱序到达时以最近者为准。纯内存,重启清零 = 重启
// 后无观测 → 调用方保守判冷(F5 同款保守,绝不伪造热)。

import "sync"

// LastRequestClock 每会话最后上游请求时刻(epoch 秒)。零值不可用,一律
// NewLastRequestClock 构造;全方法并发安全(单互斥锁,临界区纯内存)。
type LastRequestClock struct {
	mu   sync.Mutex
	last map[string]float64
}

// NewLastRequestClock 构造空钟。
func NewLastRequestClock() *LastRequestClock {
	return &LastRequestClock{last: map[string]float64{}}
}

// Note 记一次上游请求观测(单调 max;空 sid 不入——无会话身份的观测无处
// 归属,照 SnapshotStore.Capture 的跳过语义静默忽略)。
func (c *LastRequestClock) Note(sessionID string, ts float64) {
	if sessionID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last[sessionID] < ts {
		c.last[sessionID] = ts
	}
}

// Last 会话最后上游请求时刻。无观测返回 false(调用方据此保守判冷)。
func (c *LastRequestClock) Last(sessionID string) (float64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ts, ok := c.last[sessionID]
	return ts, ok
}
