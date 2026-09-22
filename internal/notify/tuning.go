// tuning.go — 票07:调参两类托盘气泡(经既有双通道 best-effort 旁路)。
//
//   - 有新建议待审:recommend 档提醒 / auto 档护栏收敛为只提醒的提醒;
//   - auto 已应用:护栏⑤生效后通报,带回滚指引(写路径永不在托盘——
//     只提示 CLI 命令,ADR-0015 决定五)。
//
// 文案构造为纯函数(TuningPendingText/TuningAppliedText)供单测直测;
// 发送复用 NotifyAlert 双通道与总开关纪律(enabled=false 全静默)。
package notify

import (
	"fmt"

	"ferryman/internal/config"
)

// tuningTitle 调参气泡固定标题(两类共用,面板/手机一眼归组)。
const tuningTitle = "Ferryman 调参"

// TuningPendingText 「有新建议待审」文案。
func TuningPendingText(upstream string, suggestMin, currentMin float64,
	hasCurrent bool, note string) (string, string) {
	msg := fmt.Sprintf("%s 上游有新的同模型阈值建议:%.1f 分钟", upstream, suggestMin)
	if hasCurrent {
		msg += fmt.Sprintf("(现值 %.1f 分钟)", currentMin)
	}
	msg += "\n审阅:ferryman tuning status"
	if note != "" {
		msg += "\n" + note
	}
	return tuningTitle, msg
}

// NotifyTuningPending 发送「有新建议待审」气泡(尽力而为,绝不影响决策)。
func NotifyTuningPending(cfg *config.Config, upstream string, suggestMin,
	currentMin float64, hasCurrent bool, note string) {
	title, msg := TuningPendingText(upstream, suggestMin, currentMin, hasCurrent, note)
	NotifyAlert(title, msg, cfg)
}

// TuningNoticeText 「只提醒不产出建议」文案(样本不足/建议值拒算两分支专用;
// 没有建议值就不印建议值数字——不走 TuningPendingText 防"建议:0.0 分钟"
// 自相矛盾,票07 评审缺陷3)。
func TuningNoticeText(upstream, note string) (string, string) {
	return tuningTitle, fmt.Sprintf("%s 上游调参提醒:%s\n详见 ferryman tuning status", upstream, note)
}

// NotifyTuningNotice 发送「只提醒不产出建议」气泡。
func NotifyTuningNotice(cfg *config.Config, upstream, note string) {
	title, msg := TuningNoticeText(upstream, note)
	NotifyAlert(title, msg, cfg)
}

// TuningAppliedText 「auto 已应用」文案(含此前值与一键回滚指引)。
func TuningAppliedText(upstream string, appliedMin, prevMin float64,
	hasPrev bool) (string, string) {
	prev := "首次生效(此前无校准)"
	if hasPrev {
		prev = fmt.Sprintf("此前 %.1f 分钟", prevMin)
	}
	msg := fmt.Sprintf("%s 上游已按建议自动更新同模型阈值公式输入:%.1f 分钟(%s)。\n"+
		"生效值仍由策略计算器现算;不满意可一键回滚:ferryman tuning rollback %s",
		upstream, appliedMin, prev, upstream)
	return tuningTitle, msg
}

// NotifyTuningApplied 发送「auto 已应用」气泡(护栏⑤:生效后必须通报)。
func NotifyTuningApplied(cfg *config.Config, upstream string, appliedMin,
	prevMin float64, hasPrev bool) {
	title, msg := TuningAppliedText(upstream, appliedMin, prevMin, hasPrev)
	NotifyAlert(title, msg, cfg)
}
