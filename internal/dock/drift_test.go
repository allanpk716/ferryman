// drift_test.go — 票06：形态漂移告警验收钉子。
// 首见不告警；新值出现告警一次且不重复；beta 标记与顶层参数键同型；
// nil 追踪器安全（纯透传 New 不构造＝零行为）。
package dock

import (
	"strings"
	"testing"

	"ferryman/internal/config"
)

type alertRecorder struct{ msgs []string }

func (a *alertRecorder) fire(title, message string) {
	a.msgs = append(a.msgs, title+"|"+message)
}

func TestDriftBetaFirstSeenSilentThenOnce(t *testing.T) {
	var rec alertRecorder
	dt := NewDriftTracker(rec.fire)
	dt.Observe("beta-x", []byte(`{"model":"m"}`))
	if len(rec.msgs) != 0 {
		t.Fatalf("首见不应告警: %v", rec.msgs)
	}
	dt.Observe("beta-x,beta-y", []byte(`{"model":"m"}`))
	if len(rec.msgs) != 1 || !strings.Contains(rec.msgs[0], "beta-y") {
		t.Fatalf("新标记应告警一次且点名: %v", rec.msgs)
	}
	dt.Observe("beta-x,beta-y", []byte(`{"model":"m"}`))
	if len(rec.msgs) != 1 {
		t.Fatalf("重复值不得再告警: %v", rec.msgs)
	}
}

func TestDriftTopLevelKeysSameShape(t *testing.T) {
	var rec alertRecorder
	dt := NewDriftTracker(rec.fire)
	dt.Observe("", []byte(`{"model":"m","max_tokens":10}`))
	if len(rec.msgs) != 0 {
		t.Fatalf("首见顶层键不应告警: %v", rec.msgs)
	}
	dt.Observe("", []byte(`{"model":"m","max_tokens":10,"tools":[{"name":"t"}]}`))
	if len(rec.msgs) != 1 || !strings.Contains(rec.msgs[0], "tools") {
		t.Fatalf("新顶层键应告警一次且点名: %v", rec.msgs)
	}
	dt.Observe("", []byte(`{"max_tokens":99,"tools":null,"model":"x"}`))
	if len(rec.msgs) != 1 {
		t.Fatalf("已知键不得再告警: %v", rec.msgs)
	}
}

func TestDriftNilTrackerAndBadBodySafe(t *testing.T) {
	var dt *DriftTracker
	dt.Observe("beta-z", []byte(`{"a":1}`)) // nil 安全
	ok := NewDriftTracker(nil)
	ok.Observe("beta-new", []byte(`totally not json`)) // 坏体/无告警函数均不 panic
}

func TestAlertViaNotifyDisabledIsNoop(t *testing.T) {
	fn := AlertViaNotify(new(config.Config)) // Enabled=false：不出网不弹窗
	fn("t", "m")                             // 仅要求不 panic（通道内部吞掉）
}
