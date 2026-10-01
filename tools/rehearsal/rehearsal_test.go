package main

// rehearsal_test.go — 彩排台测试（票05）。两层：
//  1. 快速单测：生产端口护栏、点火脚本可被换装目标解析器消费、拒连窗计算、
//     拨号拒绝分类——这些是装配正确性的地基，先红后绿。
//  2. TestRehearsalQuick：全量彩排冒烟（quick 形态：静默路径 3 次事务），
//     真构建两版 exe、真跑监督者事务、真注五项故障。跑前须先实现全部装配。
//
// 跑法：go test ./tools/rehearsal/ -timeout 15m
// （长测试有内部截止；设 REHEARSAL_SKIP_QUICK=1 可只跑单测层。）

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ferryman/internal/update"
)

// TestForbiddenPorts 生产端口黑名单：彩排的一切端口从高位随机分配，代码层面
// 禁止触碰 15700/15721/15722/15723/15900（票05 验收「零生产端口访问」的护栏）。
func TestForbiddenPorts(t *testing.T) {
	for _, p := range []int{15700, 15721, 15722, 15723, 15900} {
		if !isForbiddenPort(p) {
			t.Errorf("端口 %d 应判生产口（彩排禁用）", p)
		}
	}
	for _, p := range []int{25789, 31511, 40000, 49152} {
		if isForbiddenPort(p) {
			t.Errorf("高位端口 %d 不应判生产口", p)
		}
	}
}

// TestStartCmdParses 影子点火脚本必须能被 update.ParseStartDaemonExe 消费——
// 监督者的换装目标就从这个引号 exe 路径来（seam E），脚本形态错=整个彩排失真。
func TestStartCmdParses(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "ferryman.exe")
	if err := os.WriteFile(exe, []byte{0}, 0o644); err != nil { // 解析器要求盘上存在
		t.Fatal(err)
	}
	cmdPath := filepath.Join(dir, "start-daemon.cmd")
	if err := writeStartCmd(cmdPath, exe,
		filepath.Join(dir, "config.toml"), filepath.Join(dir, "home"),
		filepath.Join(dir, "o.log"), filepath.Join(dir, "e.log")); err != nil {
		t.Fatal(err)
	}
	got, err := update.ParseStartDaemonExe(cmdPath)
	if err != nil {
		t.Fatalf("点火脚本解析失败: %v", err)
	}
	if !strings.EqualFold(filepath.Clean(got), filepath.Clean(exe)) {
		t.Fatalf("解析出的换装目标 %q != 写入的 exe %q", got, exe)
	}
	// 脚本必须全 ASCII：非 UTF-8 代码页控制台下中文会被重解码破坏行结构
	// （installer 同款铁律）。
	b, _ := os.ReadFile(cmdPath)
	for _, c := range b {
		if c > 0x7F {
			t.Fatalf("点火脚本含非 ASCII 字节 %q——代码页脆弱", c)
		}
	}
}

// TestMeasureRefusalWindow 拒连窗口径：自事务起首次失败 → 其后首次成功的跨度；
// 无失败 = 未观测到（静默路径探针间隙大于黑窗时的合法形态）。
func TestMeasureRefusalWindow(t *testing.T) {
	t0 := time.Now()
	mk := func(off time.Duration, ok bool) probeEvent {
		return probeEvent{At: t0.Add(off), OK: ok}
	}
	// 无失败 → 未观测
	w, obs, rec := measureRefusalWindow([]probeEvent{mk(0, true), mk(1, true)}, t0)
	if obs {
		t.Fatalf("无失败不应判观测到拒连窗")
	}
	_ = w
	_ = rec
	// 2s 起连续失败，5s 恢复 → 窗 3s
	ev := []probeEvent{mk(0, true), mk(2*time.Second, false), mk(2500*ms(), false), mk(5*time.Second, true)}
	w, obs, rec = measureRefusalWindow(ev, t0.Add(1*time.Second))
	if !obs || !rec {
		t.Fatalf("应观测到窗口: obs=%v rec=%v", obs, rec)
	}
	if d := w - 3*time.Second; d < -50*ms() || d > 50*ms() {
		t.Fatalf("拒连窗 %v != 3s", w)
	}
	// from 之后的老失败不计入：from=2.2s → 首个失败 2.5s、恢复 5s → 窗 2.5s
	w, obs, _ = measureRefusalWindow(ev, t0.Add(2200*ms()))
	if !obs || w != 2500*ms() {
		t.Fatalf("from 过滤后窗口应为 2.5s，实得 obs=%v w=%v", obs, w)
	}
}

// TestIsDialRefused 拨号拒绝（黑窗正常形态）与中途截断（事故形态）的分类：
// 拒连窗分子只算拒绝；响应已开始后的失败是截断，另账处理。
func TestIsDialRefused(t *testing.T) {
	refused := fmt.Errorf(`Post "http://127.0.0.1:25789/v1/messages": dial tcp 127.0.0.1:25789: connectex: No connection could be made because the target machine actively refused it.`)
	if !isDialRefused(refused) {
		t.Fatalf("Windows 拒绝形态应判拨号拒绝")
	}
	if !isDialRefused(fmt.Errorf("dial tcp 127.0.0.1:1: connection refused")) {
		t.Fatalf("POSIX 拒绝形态应判拨号拒绝")
	}
	if isDialRefused(fmt.Errorf("unexpected EOF")) {
		t.Fatalf("中途 EOF 应判截断类，不是拨号拒绝")
	}
	if isDialRefused(fmt.Errorf("net/http: timeout awaiting response headers")) {
		t.Fatalf("超时类应判截断类")
	}
}

// TestRehearsalQuick 全量彩排（quick）：真构建两版 exe → 三场景 + 五故障注入
// → 断言全绿 → 结果文件落盘。这是票05 验收面的自动化形态（-all 为完整验收，
// quick 为 CI 形态：静默路径 3 次事务，其余不变）。
func TestRehearsalQuick(t *testing.T) {
	if os.Getenv("REHEARSAL_SKIP_QUICK") != "" {
		t.Skip("REHEARSAL_SKIP_QUICK=1：只跑单测层")
	}
	rep, err := runRehearsal(rehearsalOpts{Quick: true, Deadline: 8 * time.Minute})
	if err != nil {
		t.Fatalf("彩排基础设施失败: %v", err)
	}
	for _, ph := range rep.Phases {
		if !ph.Pass {
			for _, c := range ph.Checks {
				if !c.Pass {
					t.Errorf("[%s] 断言未过: %s — %s", ph.Name, c.Name, c.Detail)
				}
			}
			if ph.Err != "" {
				t.Errorf("[%s] 阶段错误: %s", ph.Name, ph.Err)
			}
		}
	}
	if !rep.OverallPass {
		t.Fatalf("彩排整体未全绿（ phases=%d ）", len(rep.Phases))
	}
	if rep.ResultsJSONPath == "" || !fileExists(rep.ResultsJSONPath) {
		t.Errorf("结果 JSON 未落盘: %q", rep.ResultsJSONPath)
	}
	if rep.ResultsMDPath == "" || !fileExists(rep.ResultsMDPath) {
		t.Errorf("结果 MD 未落盘: %q", rep.ResultsMDPath)
	}
	// 零生产端口：报告里记录的每个端口都必须来自高位随机分配。
	for _, p := range rep.PortsUsed {
		if isForbiddenPort(p) {
			t.Errorf("彩排触碰了生产端口 %d", p)
		}
	}
}

// ms 毫秒缩写（测试内可读性）。
func ms() time.Duration { return time.Millisecond }
