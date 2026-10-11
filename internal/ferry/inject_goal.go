package ferry

// inject_goal.go — 注入层「目标」程序补齐（2026-10-11 d0b927 案）：输出契约
// 要求注入层含目标/最新状态/下一步/关键文件/续接第一句话，但模型偶有漏写
// 目标（k3 实例：注入层只有当前状态/下一步/关键文件/续接）。新会话只读注入层，
// 漏了目标就不知道自己是谁、为何而干（该案：评审方/开发方分工只在全文里）。
// 补法纯程序、零模型调用、不判失败（判失败会把可用产物打回骨架，更差）：
// 注入层无目标标题 → 从全文「# 目标」节截取，插在状态段前（无状态段则尾附）。

import (
	"regexp"
	"strings"

	"ferryman/internal/mathx"
)

// InjectGoalCap 补入目标的码点上限（注入层预算 2200 token，目标只占小头）。
const InjectGoalCap = 300

var (
	// 注入层已有目标：行首的 **目标** / 【目标】 / #… 目标 / 目标：。
	injectGoalRe = regexp.MustCompile(`(?m)^\s*(\*\*目标\*\*|【目标】|#+\s*目标|目标\s*[:：])`)
	// 全文目标节标题行（六节契约 "# 目标"）。
	fullGoalHeadRe = regexp.MustCompile(`(?m)^#{1,3}\s*目标\s*$`)
	nextHeadRe     = regexp.MustCompile(`(?m)^#{1,3}\s`)
	// 插入点：状态段起始行。
	injectStateRe = regexp.MustCompile(`(?m)^\s*(\*\*(当前|最新)状态\*\*|【(当前|最新)状态】|#+\s*(当前|最新)状态)`)
)

// EnsureInjectGoal 注入层缺目标时从全文目标节补入；已有目标/全文无目标节
// 原样返回。
func EnsureInjectGoal(inject, full string) string {
	if injectGoalRe.MatchString(inject) {
		return inject
	}
	loc := fullGoalHeadRe.FindStringIndex(full)
	if loc == nil {
		return inject
	}
	body := full[loc[1]:]
	if n := nextHeadRe.FindStringIndex(body); n != nil {
		body = body[:n[0]]
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return inject
	}
	if mathx.RuneLen(body) > InjectGoalCap {
		body = mathx.RuneTrunc(body, InjectGoalCap) + "…"
	}
	goal := "**目标**（程序补自全文「目标」节）：" + body
	if s := injectStateRe.FindStringIndex(inject); s != nil {
		return inject[:s[0]] + goal + "\n\n" + inject[s[0]:]
	}
	return inject + "\n\n" + goal
}
