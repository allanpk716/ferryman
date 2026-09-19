// drift.go — 票06：形态漂移告警（spec「渡口·形态漂移告警」）。
//
// 为什么盯这两处：改写模式替 CC 做"本地翻译"，CC 版本升级带来新 beta 标记
// 或新顶层请求参数时，翻译层可能静默 miss（头没转发全、参数没透传）——漂移
// 追踪把"静默 miss"变成"第一时间告警"。登记是 daemon 生命周期内的内存全集
// （重启清零＝重新基线），每值只告警一次，不刷屏。
//
// 观察点在改写前（与快照同点，喂 CC 原始请求）；两种模式都观察——透传模式
// 漂移同样是 CC 升级信号（对账/面板要用）。纯透传 New() 不构造追踪器＝零行为
// （F11 同款精神）。
package dock

import (
	"encoding/json"
	"strings"
	"sync"

	"ferryman/internal/config"
	"ferryman/internal/notify"
)

// DriftTracker 形态漂移追踪器。零值不可用，一律 NewDriftTracker；nil 指针
// 方法安全（调用方无需判空）。
//
// 基线语义（票面验收"首次见 X 无告警；新 Y 出现→告警一次"）：首个 Observe
// 批次＝静默基线——daemon 刚起来收到的就是当前 CC 版本的形态，是已知好基线，
// 对它告警等于每次重启刷一屏噪声；此后任何新值出现才告警且每值只一次。
type DriftTracker struct {
	mu    sync.Mutex
	betas map[string]bool // 见过的 anthropic-beta 标记全集
	keys  map[string]bool // 见过的请求顶层参数键全集
	first bool            // true＝下一批次是基线（登记不告警）
	alert func(title, message string)
}

// NewDriftTracker 构造。alert 可为 nil（只记日志不推送）。
func NewDriftTracker(alert func(title, message string)) *DriftTracker {
	return &DriftTracker{
		betas: map[string]bool{}, keys: map[string]bool{},
		first: true, alert: alert,
	}
}

// Observe 喂一份请求的 anthropic-beta 头与原始体（改写前）。基线批次只登记；
// 其后的新标记/新顶层键＝日志告警＋（alert 非 nil 时）推送一次，此后同值
// 不再告警。
func (t *DriftTracker) Observe(betaHeader string, body []byte) {
	if t == nil {
		return
	}
	t.mu.Lock()
	baseline := t.first
	t.first = false
	t.mu.Unlock()
	t.observeBetas(betaHeader, baseline)
	t.observeKeys(body, baseline)
}

func (t *DriftTracker) observeBetas(raw string, baseline bool) {
	if raw == "" {
		return
	}
	for _, f := range strings.Split(raw, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		t.mu.Lock()
		isNew := !t.betas[f]
		t.betas[f] = true
		t.mu.Unlock()
		if isNew && !baseline {
			t.fire("新 anthropic-beta 标记: " + f)
		}
	}
}

// observeKeys 顶层参数键全集：RawMessage 解析只建键集不物化值（MB 级体仍是
// 一次线性扫描，代价可接受）。
func (t *DriftTracker) observeKeys(body []byte, baseline bool) {
	if len(body) == 0 {
		return
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(body, &obj) != nil || obj == nil {
		return // 非法体：改写/上游校验路径另行处置，这里静默
	}
	for k := range obj {
		t.mu.Lock()
		isNew := !t.keys[k]
		t.keys[k] = true
		t.mu.Unlock()
		if isNew && !baseline {
			t.fire("请求顶层新参数键: " + k)
		}
	}
}

// fire 每新值恰好一次：日志＋推送（alert 非 nil 时）。推送失败由 notify
// 内部吞掉（旁路原则），绝不影响转发。
func (t *DriftTracker) fire(msg string) {
	logger.Printf("形态漂移: %s", msg)
	if t.alert != nil {
		t.alert("Ferryman 渡口形态漂移", msg)
	}
}

// AlertViaNotify notify 接线闭包（daemon 侧 Options.Alert 用）：漂移推送走
// 既有 NotifyAlert 双通道。本包只调用不改 notify（文案归票08 并行泳道）。
func AlertViaNotify(cfg *config.Config) func(title, message string) {
	return func(title, message string) {
		notify.NotifyAlert(title, message, cfg)
	}
}
