// Package pathsx 提供路径键形归一，供 lineage 唯一键等场景使用。
package pathsx

import "strings"

// NormPath 把路径归一成 lineage 唯一键形：反斜杠→正斜杠 + 小写。
// Python 参照实现：str(PureWindowsPath(p)).replace("\\", "/").lower()。
func NormPath(p string) string {
	return strings.ToLower(strings.ReplaceAll(p, "\\", "/"))
}

// CCLaunchCwd 把 CC 钩子上报的 cwd 校正回会话启动目录（2026-10-12 漏拦案：
// 5bbc161a 会话里助手 Bash cd 进 apps\web\src，钩子 cwd 跟着漂到子目录，
// 交接/台账记的却是启动目录，ValidHandoff 按 cwd 精确匹配落空 → 闲置
// 4.5h 只警告不拦）。CC 转录所在项目目录名＝启动 cwd 的编码
//（非 [A-Za-z0-9] 字符逐 UTF-16 码元换 '-'），不随 cd 漂移：自 cwd 起逐级
// 向上找编码与之相等的祖先即启动目录。找不到（cd 出了项目树、转录路径
// 缺失、超长路径被 CC 截断加哈希）→ 原样返回＝旧行为。
func CCLaunchCwd(cwd, transcriptPath string) string {
	proj := lastSeg(parentPath(transcriptPath))
	if cwd == "" || proj == "" {
		return cwd
	}
	p := strings.TrimRight(cwd, `\/`)
	for p != "" {
		if strings.EqualFold(ccProjectName(p), proj) {
			return p
		}
		parent := parentPath(p)
		if parent == p {
			break
		}
		p = parent
	}
	return cwd
}

// ccProjectName CC 项目目录名编码（与 CC 同法：非 ASCII 字母数字一律 '-'，
// 按 UTF-16 码元计——BMP 外字符占两个码元出两个 '-'）。
func ccProjectName(p string) string {
	var b strings.Builder
	for _, r := range p {
		switch {
		case r < 0x80 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'):
			b.WriteRune(r)
		case r > 0xFFFF:
			b.WriteString("--")
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// parentPath 去掉末段（两种分隔符都认，不依赖运行平台）；无分隔符返回 ""。
func parentPath(p string) string {
	p = strings.TrimRight(p, `\/`)
	i := strings.LastIndexAny(p, `\/`)
	if i < 0 {
		return ""
	}
	return p[:i]
}

// lastSeg 末段（两种分隔符都认）。
func lastSeg(p string) string {
	p = strings.TrimRight(p, `\/`)
	return p[strings.LastIndexAny(p, `\/`)+1:]
}
