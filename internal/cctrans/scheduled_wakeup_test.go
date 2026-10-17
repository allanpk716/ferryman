package cctrans

// scheduled_wakeup_test.go — IsScheduledWakeup 钉子（fix/gate-machine-exempt，
// 2026-10-17 08:09:29 de9af594 误拦案）：命中（真实形状）/prompt 不同/超 60s/
// 其后有真实用户输入/记录缺失/二次读取补到/taskKind 不认/isMeta 与纯
// tool_result 不算真实输入/锚=最近一条 fire。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wakeupPrompt de9af594 案的唤醒 prompt 原文（中文、含空格，考验逐字比对）。
const wakeupPrompt = "继续 /implement-spec-split spec #26 编排：检查 fix worker（review-fix 分支）状态"

// fireLine 真实形状的 scheduled_task_fire 行（taskKind 可变；其余字段照抄
// 案发现场的转录）。
func fireLine(ts, prompt, taskKind string) map[string]any {
	return map[string]any{
		"type": "system", "subtype": "scheduled_task_fire",
		"content":   "Claude resuming /loop wakeup (Oct 17 8:09am)",
		"timestamp": ts,
		"taskId":    "ebb52f87",
		"cron":      "46 0 * * *",
		"prompt":    prompt,
		"taskKind":  taskKind,
		"cronKind":  "loop",
		"sessionId": "de9af594-1111-2222-3333-444444444444",
		"cwd":       "C:\\WorkSpace\\agent-taskboard",
	}
}

// queueLine 唤醒前的队列回放记录（真实形状：enqueue/dequeue 夹在 fire 之前）。
func queueLine(op string) map[string]any {
	return map[string]any{"type": "queue-operation", "operation": op,
		"content": wakeupPrompt, "timestamp": "2026-10-17T00:09:29.500Z"}
}

// typedUserLine 真实用户打字行（type=user、content 为字符串）。
func typedUserLine(text string) map[string]any {
	return map[string]any{"type": "user", "timestamp": "2026-10-17T00:09:30.000Z",
		"message": map[string]any{"role": "user", "content": text}}
}

func writeWakeup(t *testing.T, blocks []map[string]any) string {
	t.Helper()
	lines := make([]string, 0, len(blocks))
	for _, b := range blocks {
		lines = append(lines, lineOf(b))
	}
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestIsScheduledWakeupHit(t *testing.T) {
	// 真实形状：queue enqueue/dequeue 之后一条 fire；now = fire 后 0.5s（钩子
	// 拿到 prompt 时记录已落盘的实测时序）。
	p := writeWakeup(t, []map[string]any{
		queueLine("enqueue"), queueLine("dequeue"),
		fireLine("2026-10-17T00:09:29.651Z", wakeupPrompt, "loop"),
	})
	now := tsOf(t, "2026-10-17T00:09:30.151Z")
	if !IsScheduledWakeup(p, wakeupPrompt, now) {
		t.Fatal("逐字相同且新鲜的 fire 应命中")
	}
	// prompt 首尾空白容差（TrimSpace 口径）
	if !IsScheduledWakeup(p, "  "+wakeupPrompt+"\n", now) {
		t.Fatal("TrimSpace 后相等应命中")
	}
}

func TestIsScheduledWakeupPromptMismatch(t *testing.T) {
	p := writeWakeup(t, []map[string]any{
		fireLine("2026-10-17T00:09:29.651Z", wakeupPrompt, "loop"),
	})
	now := tsOf(t, "2026-10-17T00:09:30.151Z")
	if IsScheduledWakeup(p, "用户自己的另一条消息", now) {
		t.Fatal("prompt 不同不应命中")
	}
}

func TestIsScheduledWakeupStale(t *testing.T) {
	// 超出 WakeupFreshS（60s）：10 分钟前的 fire 是旧唤醒，不是本次提交
	p := writeWakeup(t, []map[string]any{
		fireLine("2026-10-17T00:00:00.000Z", wakeupPrompt, "loop"),
	})
	now := tsOf(t, "2026-10-17T00:09:30.000Z")
	if IsScheduledWakeup(p, wakeupPrompt, now) {
		t.Fatal("超 60s 的 fire 不应命中")
	}
}

func TestIsScheduledWakeupUserInputAfterFire(t *testing.T) {
	// fire 之后有真实打字（人接手了）→ 不算机器唤醒
	p := writeWakeup(t, []map[string]any{
		queueLine("enqueue"),
		fireLine("2026-10-17T00:09:29.651Z", wakeupPrompt, "loop"),
		typedUserLine("我自己来"),
	})
	now := tsOf(t, "2026-10-17T00:09:30.151Z")
	if IsScheduledWakeup(p, wakeupPrompt, now) {
		t.Fatal("fire 之后有真实用户输入不应命中")
	}
}

func TestIsScheduledWakeupNonHumanLinesAfterFireStillHit(t *testing.T) {
	// 纯 tool_result（工具回写）与 isMeta（队列回放等机器产生）的 user 行都
	// 不是真实打字——其后仍算机器唤醒。
	meta := typedUserLine(wakeupPrompt)
	meta["isMeta"] = true
	p := writeWakeup(t, []map[string]any{
		fireLine("2026-10-17T00:09:29.651Z", wakeupPrompt, "loop"),
		taskResult("t1", "ok"), // 纯 tool_result 回写
		meta,
	})
	now := tsOf(t, "2026-10-17T00:09:30.151Z")
	if !IsScheduledWakeup(p, wakeupPrompt, now) {
		t.Fatal("tool_result/isMeta 行不应打断唤醒判定")
	}
}

func TestIsScheduledWakeupNoRecord(t *testing.T) {
	// 尾窗根本没有 fire 记录（正常人工 prompt 的常态）→ 非唤醒。now 取远离
	// 文件真实 mtime 的冻结钟：重读资格窗不满足，直接快路径返回。
	p := writeWakeup(t, []map[string]any{typedUserLine("普通消息")})
	if IsScheduledWakeup(p, wakeupPrompt, 1.8e9) {
		t.Fatal("无 fire 记录不应命中")
	}
	if IsScheduledWakeup(filepath.Join(t.TempDir(), "absent.jsonl"), wakeupPrompt, 1.8e9) {
		t.Fatal("文件缺失不应命中")
	}
	if IsScheduledWakeup(p, "   ", 1.8e9) {
		t.Fatal("空 prompt 不应命中")
	}
}

func TestIsScheduledWakeupSecondReadCatchesUp(t *testing.T) {
	// 落盘竞态：第一次读没扫到 fire（尚未落盘），等待窗内补写、第二次读到。
	// now/mtime 都用真实墙钟（资格窗内），后台 goroutine 在重读前补写记录。
	p := writeWakeup(t, []map[string]any{queueLine("enqueue")})
	now := float64(time.Now().UnixNano()) / 1e9
	go func() {
		time.Sleep(30 * time.Millisecond)
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		defer f.Close()
		ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		_, _ = f.WriteString(lineOf(fireLine(ts, wakeupPrompt, "loop")) + "\n")
	}()
	if !IsScheduledWakeup(p, wakeupPrompt, now) {
		t.Fatal("迟到落盘的 fire 应在第二次读取时补到")
	}
}

func TestIsScheduledWakeupCronKindCounts(t *testing.T) {
	// 不认 taskKind：cron 与 loop 同属机器自发
	for _, kind := range []string{"cron", "loop"} {
		p := writeWakeup(t, []map[string]any{
			fireLine("2026-10-17T00:09:29.651Z", wakeupPrompt, kind),
		})
		now := tsOf(t, "2026-10-17T00:09:30.151Z")
		if !IsScheduledWakeup(p, wakeupPrompt, now) {
			t.Fatalf("taskKind=%s 应同样命中", kind)
		}
	}
}

func TestIsScheduledWakeupAnchorIsNewestFire(t *testing.T) {
	// 锚 = 最近一条 fire：最近的不匹配时，更早的匹配也不算（防拿旧唤醒认新提交）
	p := writeWakeup(t, []map[string]any{
		fireLine("2026-10-17T00:08:00.000Z", wakeupPrompt, "loop"),
		fireLine("2026-10-17T00:09:29.651Z", "另一条定时任务", "cron"),
	})
	now := tsOf(t, "2026-10-17T00:09:30.151Z")
	if IsScheduledWakeup(p, wakeupPrompt, now) {
		t.Fatal("锚是最近一条 fire：最近的不匹配即不命中")
	}
}

// tsOf ISO 串 → epoch 秒（测试夹具用，与 TSToEpoch 同源解析）。
func tsOf(t *testing.T, iso string) float64 {
	t.Helper()
	ts, ok := TSToEpoch(iso)
	if !ok {
		t.Fatalf("测试时间戳解析失败: %s", iso)
	}
	return ts
}
