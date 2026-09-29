package main

// probe.go — 彩排观测面：代理面流量发生器兼探针（拒连窗与零截断的数据源）、
// SSE 长流探针。观测口径（D10）：
//
//   - 拒连窗 = 事务起点之后「首次失败 → 其后首次成功」的跨度。静默路径判
//     ≤10s；强制路径只记录不判。
//   - 零截断：非流式请求要么完整 200、要么拨号拒绝（黑窗正常形态）；响应
//     已开始后的失败/非 200 记 Cut（截断类）——静默路径必须为 0。
//   - SSE 流的 message_stop 到达 = 未截断。
//
// 间歇流量的节奏红线：门判据 last_request_ts 是代理面完成时刻，「间隙 ≥10s」
// 指相邻请求完成间隔——所以成功后睡满 period 才发下一发；失败（黑窗内拨号
// 拒绝）不改写 last_request_ts（从未到达守护），用 failRetry 密探测收口
// 拒连窗测量，不污染静默节律。

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// probeEvent 单次代理面请求的观测记录。
type probeEvent struct {
	At     time.Time `json:"at"`
	OK     bool      `json:"ok"`
	Status int       `json:"status"`
	Err    string    `json:"err,omitempty"`
	Cut    bool      `json:"cut,omitempty"` // 截断类：响应已开始后失败/非 200
}

// trafficProbe 代理面流量发生器兼观测器。
type trafficProbe struct {
	addr      string // 影子渡口（127.0.0.1:port）
	label     string
	period    time.Duration // 成功后的静默间隙（≥10s = 门判据口径）
	failRetry time.Duration // 黑窗内重试节奏（拒连窗测量密度）
	client    *http.Client
	body      []byte

	mu     sync.Mutex
	events []probeEvent
	stopCh chan struct{}
	doneCh chan struct{}
	once   sync.Once
}

// newTrafficProbe 构造（不启动）。
func newTrafficProbe(addr, label string, period, failRetry time.Duration) *trafficProbe {
	return &trafficProbe{
		addr:      addr,
		label:     label,
		period:    period,
		failRetry: failRetry,
		client:    &http.Client{Timeout: 20 * time.Second},
		body: []byte(`{"model":"rehearsal-model","stream":false,"max_tokens":8,` +
			`"messages":[{"role":"user","content":"rehearsal ping"}]}`),
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// start 启动发生循环（立即发第一发——给门一个完成基线）。
func (t *trafficProbe) start() {
	go func() {
		defer close(t.doneCh)
		for {
			ev := t.fire()
			t.mu.Lock()
			t.events = append(t.events, ev)
			t.mu.Unlock()
			wait := t.period
			if !ev.OK {
				wait = t.failRetry
			}
			select {
			case <-t.stopCh:
				return
			case <-time.After(wait):
			}
		}
	}()
}

// stop 停止并收尾（幂等）。
func (t *trafficProbe) stop() {
	t.once.Do(func() { close(t.stopCh) })
	select {
	case <-t.doneCh:
	case <-time.After(5 * time.Second):
	}
}

// snapshot 事件快照（只读副本）。
func (t *trafficProbe) snapshot() []probeEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]probeEvent, len(t.events))
	copy(out, t.events)
	return out
}

// truncationCount 自 from 起 Cut 类事件数（零截断断言的分子）。
func (t *trafficProbe) truncationCount(from time.Time) int {
	n := 0
	for _, e := range t.snapshot() {
		if e.Cut && !e.At.Before(from) {
			n++
		}
	}
	return n
}

// fire 发一发非流式请求并分类。
func (t *trafficProbe) fire() probeEvent {
	now := time.Now()
	req, err := http.NewRequest(http.MethodPost, "http://"+t.addr+"/v1/messages",
		bytes.NewReader(t.body))
	if err != nil {
		return probeEvent{At: now, Err: err.Error(), Cut: true}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		// 拨号拒绝 = 黑窗正常形态（拒连窗分子）；其余（EOF/超时/复位）= 截断类。
		return probeEvent{At: now, Err: err.Error(), Cut: !isDialRefused(err)}
	}
	b, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if rerr != nil {
		return probeEvent{At: now, Status: resp.StatusCode, Err: rerr.Error(), Cut: true}
	}
	ok := resp.StatusCode == http.StatusOK && len(b) > 0
	return probeEvent{At: now, OK: ok, Status: resp.StatusCode, Cut: !ok}
}

// isDialRefused 拨号拒绝分类：Windows connectex / POSIX connection refused。
// 只认「连接根本没建立」的形态——它们不构成截断（请求未达守护）。
func isDialRefused(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "connectex: No connection could be made") ||
		strings.Contains(s, "connection refused")
}

// measureRefusalWindow 拒连窗：events 中 At ≥ from 的首个失败到其后首个
// 成功的跨度。返回（跨度, 是否观测到失败, 失败后是否见到恢复）。
func measureRefusalWindow(events []probeEvent, from time.Time) (time.Duration, bool, bool) {
	firstFail := -1
	for i, e := range events {
		if !e.At.Before(from) && !e.OK {
			firstFail = i
			break
		}
	}
	if firstFail < 0 {
		return 0, false, false
	}
	for j := firstFail; j < len(events); j++ {
		if events[j].OK {
			return events[j].At.Sub(events[firstFail].At), true, true
		}
	}
	return 0, true, false // 失败后未见恢复（场景异常，调用方按末事件另行判断）
}

// sseProbe SSE 长流探针：一条流读到底，记事件数与 message_stop。
type sseProbe struct {
	addr string

	mu           sync.Mutex
	deltas       int // content_block_delta 行计数（chunk 到货量，event/data 各一行）
	sawStop      bool
	err          string
	startedAt    time.Time
	firstChunkAt time.Time
	endedAt      time.Time
	done         chan struct{}
}

// newSSEProbe 构造（不启动）。
func newSSEProbe(addr string) *sseProbe {
	return &sseProbe{
		addr: addr,
		done: make(chan struct{}),
	}
}

// start 发起流式请求并开始读取（异步）。
func (p *sseProbe) start() {
	go func() {
		defer close(p.done)
		p.mu.Lock()
		p.startedAt = time.Now()
		p.mu.Unlock()
		body := []byte(`{"model":"rehearsal-model","stream":true,"max_tokens":64,` +
			`"messages":[{"role":"user","content":"rehearsal long stream"}]}`)
		// 不设整体 Timeout：长流按需无限读（对齐渡口透传侧语义）；响应头等待
		// 10s——上游收了理不回话属桩故障，快速暴露。
		req, err := http.NewRequest(http.MethodPost, "http://"+p.addr+"/v1/messages",
			bytes.NewReader(body))
		if err != nil {
			p.finish(err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 10 * time.Second}}
		resp, err := client.Do(req)
		if err != nil {
			p.finish(err)
			return
		}
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64*1024), 64*1024)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "event:") && !strings.HasPrefix(line, "data:") {
				continue
			}
			p.mu.Lock()
			if p.firstChunkAt.IsZero() {
				p.firstChunkAt = time.Now()
			}
			if strings.Contains(line, "content_block_delta") {
				p.deltas++
			}
			if strings.Contains(line, "message_stop") {
				p.sawStop = true
			}
			p.mu.Unlock()
		}
		rerr := sc.Err()
		_ = resp.Body.Close()
		p.finish(rerr)
	}()
}

func (p *sseProbe) finish(err error) {
	p.mu.Lock()
	p.endedAt = time.Now()
	if err != nil {
		p.err = err.Error()
	}
	p.mu.Unlock()
}

// waitFirstChunk 等首个事件到达（流确立；长流场景的事务触发点）。
func (p *sseProbe) waitFirstChunk(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		ok := !p.firstChunkAt.IsZero()
		p.mu.Unlock()
		if ok {
			return nil
		}
		select {
		case <-p.done:
			p.mu.Lock()
			e := p.err
			p.mu.Unlock()
			return fmt.Errorf("流在首包前结束: %s", e)
		case <-time.After(50 * time.Millisecond):
		}
	}
	return fmt.Errorf("等首包超时 %v", timeout)
}

// wait 等流收尾（≤timeout；返回快照）。
func (p *sseProbe) wait(timeout time.Duration) sseSnapshot {
	select {
	case <-p.done:
	case <-time.After(timeout):
	}
	return p.snapshot()
}

// sseSnapshot 流收尾观测快照。
type sseSnapshot struct {
	Deltas      int
	SawStop     bool
	Err         string
	Dur         time.Duration
	FirstChunk  time.Time
	EndedAt     time.Time
	CompletedOK bool // 读尽且无错
}

func (p *sseProbe) snapshot() sseSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := sseSnapshot{
		Deltas:  p.deltas,
		SawStop: p.sawStop,
		Err:     p.err,
	}
	if !p.startedAt.IsZero() && !p.endedAt.IsZero() {
		s.Dur = p.endedAt.Sub(p.startedAt)
	}
	s.FirstChunk = p.firstChunkAt
	s.EndedAt = p.endedAt
	s.CompletedOK = p.err == "" && p.endedAt.After(p.startedAt)
	return s
}
