// tuning_test.go — 票07:调参两类托盘气泡的文案与开关纪律。
//
// 验收对映:「托盘两类气泡通知路径接通(internal/notify 单测)」——
//   - 有新建议待审:NotifyTuningPending(recommend 提醒 / auto 护栏收敛提醒);
//   - auto 已应用:NotifyTuningApplied(护栏⑤生效后通报,带回滚指引)。
// 通道桩同 notify_test.go 惯例:httptest 假端点 + mock runToast,不弹真气泡。
package notify

import (
	"strings"
	"testing"

	"ferryman/internal/config"
)

func TestTuningPendingText(t *testing.T) {
	title, msg := TuningPendingText("glm", 22, 25, true, "recommend 档:待人工审阅")
	if title != "Ferryman 调参" {
		t.Fatalf("title = %q, want Ferryman 调参", title)
	}
	for _, want := range []string{"glm", "22.0", "25.0", "ferryman tuning status", "recommend"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("正文缺 %q: %q", want, msg)
		}
	}
	// 无现值形态(上游无配置上限时)不出现"现值"字样。
	_, msg2 := TuningPendingText("glm", 22, 0, false, "")
	if strings.Contains(msg2, "现值") {
		t.Fatalf("无现值形态不应出现现值: %q", msg2)
	}
}

func TestTuningAppliedText(t *testing.T) {
	title, msg := TuningAppliedText("glm", 22, 24, true)
	if title != "Ferryman 调参" {
		t.Fatalf("title = %q, want Ferryman 调参", title)
	}
	for _, want := range []string{"glm", "22.0", "24.0", "ferryman tuning rollback glm", "计算器"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("正文缺 %q: %q", want, msg)
		}
	}
	// 首次生效(此前无校准)形态。
	_, msg2 := TuningAppliedText("glm", 22, 0, false)
	if !strings.Contains(msg2, "首次生效") {
		t.Fatalf("无前值应注明首次生效: %q", msg2)
	}
}

func TestNotifyTuningBubblesRespectFlags(t *testing.T) {
	ps := startPushStub(t, 200)
	toastCalls := mockToast(t, nil)
	cfg := config.Default()
	cfg.Notify = config.NotifyCfg{Enabled: true, Pushover: true,
		PushoverToken: "t", PushoverUser: "u", Toast: true}

	NotifyTuningPending(cfg, "glm", 22, 25, true, "护栏全过")
	NotifyTuningApplied(cfg, "glm", 22, 24, true)

	if n := ps.count(); n != 2 {
		t.Fatalf("pushover 应各发一次, got %d", n)
	}
	if len(*toastCalls) != 2 {
		t.Fatalf("toast 应各发一次, got %d", len(*toastCalls))
	}
	form := ps.form(t)
	if form.Get("title") != "Ferryman 调参" {
		t.Fatalf("title = %q, want Ferryman 调参", form.Get("title"))
	}

	// 总开关关 → 全静默(旁路纪律:通知永不影响决策)。
	cfg.Notify.Enabled = false
	ps.reset()
	*toastCalls = nil
	NotifyTuningPending(cfg, "glm", 22, 25, true, "")
	NotifyTuningApplied(cfg, "glm", 22, 24, true)
	if ps.count() != 0 || len(*toastCalls) != 0 {
		t.Fatalf("enabled=false 应全静默: push=%d toast=%d", ps.count(), len(*toastCalls))
	}
}

// TestTuningNoticeText 票07 评审缺陷3回归:样本不足/拒算分支的"只提醒"文案
// 不应出现建议值数字("建议:0.0 分钟"自相矛盾)。
func TestTuningNoticeText(t *testing.T) {
	title, msg := TuningNoticeText("glm", "样本不足（窗内摆渡事件 12 < 门槛 30），仅提醒不产出建议")
	if title != "Ferryman 调参" {
		t.Fatalf("标题不符: %s", title)
	}
	if strings.Contains(msg, "0.0 分钟") || strings.Contains(msg, "建议：") {
		t.Fatalf("只提醒文案不应出现建议值数字: %s", msg)
	}
	if !strings.Contains(msg, "样本不足") || !strings.Contains(msg, "ferryman tuning status") {
		t.Fatalf("文案缺关键信息: %s", msg)
	}
}
