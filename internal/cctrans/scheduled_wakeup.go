package cctrans

// scheduled_wakeup.go — /loop 定时唤醒判定（2026-10-17 08:09:29 de9af594 误拦案）。
//
// CC 的定时唤醒（/loop 与 cron 同族）是机器自发 prompt：转录里它是
// type=="system" && subtype=="scheduled_task_fire" 行，prompt 字段与钩子拿到的
// prompt 逐字相同，且实测钩子触发前约 0.5s 已落盘。闸门按"人打字"拦它＝误拦
// （当天任务没跑）；更糟的是拦截会走待续保存、按会话覆盖——唤醒文本把 0.6s
// 前作答闸门刚存下的用户选择顶掉。判定三条件（全满足才算唤醒）：
//   1. 尾窗内最近一条 scheduled_task_fire 的 prompt 与入参 TrimSpace 后相等；
//   2. 其 timestamp 距 now ≤ WakeupFreshS；
//   3. 其后（文件序）没有真实用户输入（type=="user" 且非 tool_result、非 isMeta
//      ——工具回写不是打字，队列回放等机器产生的 user 行带 isMeta）。
//
// 不认 taskKind：loop 与 cron 同属机器自发，一视同仁。时间比较只用入参 now
// （调用方传 clock.Now，测试可冻结），本文件不直接 time.Now。

import (
	"math"
	"os"
	"strings"
	"time"

	"ferryman/internal/jsonl"
)

// WakeupFreshS 唤醒判定时效窗（秒）：fire 记录 timestamp 距 now 的上界。
// 实测钩子触发前 ~0.5s 已落盘；60s 容纳转录时钟偏移与慢盘。
const WakeupFreshS = 60.0

// wakeupRetryS 落盘竞态重读等待（秒）：第一次没读到等一小会儿再读一次
// （唤醒行晚于钩子读盘的罕见时序）。
const wakeupRetryS = 0.3

// wakeupRetryWindowS 重读竞态资格窗（秒）：尾窗没扫到 fire 记录、且转录文件
// mtime 距 now 超过该窗，即认定"没有迟到写入在路上"，跳过重读等待——正常
// 人工 prompt 的尾窗永远没有 fire 记录，无条件重等会给每次提交白添延迟。
// fire 记录实测钩子前 ~0.5s 落盘，15s 窗足够覆盖慢盘与时钟小偏移。
const wakeupRetryWindowS = 15.0

// IsScheduledWakeup 判定 path 转录尾窗里，prompt 是否是刚落盘的 CC 定时唤醒
// （scheduled_task_fire）。落盘竞态：第一次没读到（且文件近期有写、迟到记录
// 还可能在路上）→ 等 wakeupRetryS 再读一次，仍没有 → 非唤醒（走正常闸门）。
// 已扫到 fire 记录但不满足条件时不重读：锚记录的 prompt/timestamp 落盘后
// 不可变，"其后有真实用户输入"只随追加从无到有（false→false 恒定），重读
// 改变不了判定——白等 300ms。
func IsScheduledWakeup(path, prompt string, now float64) bool {
	if path == "" || strings.TrimSpace(prompt) == "" {
		return false
	}
	hit, sawFire := scanScheduledWakeup(path, prompt, now)
	if hit || sawFire {
		return hit
	}
	if !wakeupWriteRecent(path, now) {
		return false // 文件近期没写：没有迟到记录在路上，不白等
	}
	time.Sleep(time.Duration(wakeupRetryS * float64(time.Second)))
	hit, _ = scanScheduledWakeup(path, prompt, now)
	return hit
}

// scanScheduledWakeup 单次扫描：尾窗倒扫找最近一条 scheduled_task_fire，命中
// 后做三条件判定（prompt 相等、时效、其后无真实用户输入）。返回（是否唤醒，
// 是否扫到过 fire 记录——后者是重读资格的否定项，见 IsScheduledWakeup）。
func scanScheduledWakeup(path, prompt string, now float64) (bool, bool) {
	lines, ok := jsonl.TailWindow(path, DefaultTailBytes)
	if !ok {
		return false, false
	}
	want := strings.TrimSpace(prompt)
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if !strings.Contains(line, "scheduled_task_fire") {
			continue // 子串预筛快速跳行
		}
		d, ok := jsonl.DecodeDict(line)
		if !ok {
			continue
		}
		if typ, _ := d["type"].(string); typ != "system" {
			continue
		}
		if sub, _ := d["subtype"].(string); sub != "scheduled_task_fire" {
			continue
		}
		// 最近一条 fire 即锚：prompt 逐字（TrimSpace 后）相等才认
		got, _ := d["prompt"].(string)
		if strings.TrimSpace(got) != want {
			return false, true
		}
		ts, ok := TSToEpoch(d["timestamp"])
		if !ok || math.Abs(now-ts) > WakeupFreshS {
			return false, true
		}
		// 其后（文件序）不得有真实用户输入——有＝会话已被人接手，不算机器唤醒
		for _, later := range lines[i+1:] {
			if dd, ok := jsonl.DecodeDict(later); ok && realUserInput(dd) {
				return false, true
			}
		}
		return true, true
	}
	return false, false
}

// wakeupWriteRecent 转录 mtime 距 now 是否在竞态资格窗内（重读的前置闸）。
// 取不到 Stat → false（连文件都打不开，没有迟到写入可言）。
func wakeupWriteRecent(path string, now float64) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	mt := float64(fi.ModTime().UnixNano()) / 1e9
	return math.Abs(now-mt) <= wakeupRetryWindowS
}

// realUserInput 真实用户输入行：type=="user" 且非 isMeta、非纯 tool_result
// （工具回写不是打字；answerGateRealContent 同款口径加 isMeta 维度——
// 队列回放等机器产生的 user 行带 isMeta 标记）。
func realUserInput(d map[string]any) bool {
	if typ, _ := d["type"].(string); typ != "user" {
		return false
	}
	if jsonl.Truthy(d["isMeta"]) {
		return false
	}
	msg, ok := d["message"].(map[string]any)
	if !ok {
		return false
	}
	switch c := msg["content"].(type) {
	case string:
		return c != ""
	case []any:
		for _, bAny := range c {
			if b, ok := bAny.(map[string]any); ok {
				if t, _ := b["type"].(string); t != "tool_result" {
					return true
				}
			}
		}
	}
	return false
}
