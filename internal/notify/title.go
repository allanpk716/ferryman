// 票08 · 通知标题构造器与告警文案接线单源。
//
// 痛点：旧通知标题只有裸 session id（或与项目/会话无关的固定短语），手机
// 推送无法分辨是哪个项目哪个会话。
//
// 标题形态（规格 docs/superpowers/specs/20260919-dock-heartbeat-spec.md
// 「通知」节）：`Ferryman｜<项目名>：<会话标题>`。
// 降级链：会话标题（台账 ai-title，SessionState.Title）→ 首问截 24 rune 加
// 省略号 → 仅 `Ferryman｜<项目名>`；项目名再缺（cwd 空）退裸 `Ferryman`。
// 台账无首问现成字段——告警场合一律走标题/项目名降级（不新建 jsonl 解析）。
// rune 级截断不切半个汉字；总长封顶 100 rune（防御；Pushover title 上限
// 250 字符，构造层已留足余量，SendPushover 不再截断）。
package notify

import "strings"

const (
	// firstQuestionCap 首问降级截断长度（rune，省略号前的整字数）。
	firstQuestionCap = 24
	// titleCap 标题总长封顶（rune，防御值）。
	titleCap = 100
	// sidTailCap 正文尾部 sid 小字的截断（rune；与日志 runeCap8 同口径，
	// 便于推送与控制台日志互相照对）。
	sidTailCap = 8
)

// BuildTitle 标题构造（降级链见包注）。截断一律 rune 级——绝不切半个汉字。
func BuildTitle(project, sessionTitle, firstQuestion string) string {
	prefix := "Ferryman"
	if project != "" {
		prefix += "｜" + project
	}
	body := sessionTitle
	if body == "" {
		body = truncate(firstQuestion, firstQuestionCap, "…")
	}
	if body == "" {
		return truncate(prefix, titleCap, "")
	}
	return truncate(prefix+"："+body, titleCap, "")
}

// ProjectName 会话路径 → 项目基名（正反斜杠都认；末尾分隔符剥掉；空串或
// 纯分隔符回空）。
func ProjectName(path string) string {
	p := strings.TrimRight(path, `/\`)
	if p == "" {
		return ""
	}
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// AlertCopy 告警文案接线单源（daemon watcher.qwatchAlert 调用）：
//   - 标题＝BuildTitle 降级链（cwd→基名；台账无首问字段，首问恒空）；
//   - 正文＝事件名＋原消息＋尾部 sid 小字——裸 sid 不进标题、也不散落正文
//     中间；sid 截 8 rune 与日志同口径；空 sid 不追加小字。
func AlertCopy(cwd, sessionTitle, evTitle, msg, sessionID string) (string, string) {
	title := BuildTitle(ProjectName(cwd), sessionTitle, "")
	body := evTitle + "：" + msg
	if sessionID != "" {
		body += "\n(sid=" + truncate(sessionID, sidTailCap, "") + ")"
	}
	return title, body
}

// truncate rune 级截断：超 n 整字截到前 n 个并接 suffix（空 suffix 即纯
// 截断封顶）。
func truncate(s string, n int, suffix string) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + suffix
}
