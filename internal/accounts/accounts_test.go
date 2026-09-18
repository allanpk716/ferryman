package accounts

// 规格：tests/test_accounts.py 19 例 1:1（前 9 例为 accounts 单元行为，本文件
// 直接转绿；后 10 例为 e2e——Python Harness 起全链 daemon（gate/window/worker），
// 归票 13/14/17：4 个窗口例已由票13 回填转绿于本目录外部测试包
// window_e2e_test.go，余 6 例 t.Skip 占位保清点，随归属票装配后转绿）。
// 另加 2 例验收专测：落盘行键序（票面验收#2）与 Record 并发安全（票面验收#3）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// 对应 Python 的 time.mktime(strptime(...))——本地时区构造。
var (
	AUG = float64(time.Date(2026, 8, 15, 12, 0, 0, 0, time.Local).Unix())
	SEP = float64(time.Date(2026, 9, 16, 12, 0, 0, 0, time.Local).Unix())
	MID = float64(time.Date(2026, 9, 1, 12, 0, 0, 0, time.Local).Unix()) // 落在 (AUG, SEP) 内，since/until 断言与运行日期解耦
)

// recHandoff 对应 Python 的 rec_handoff(acc, ts=None, sid="s1", outcome="fresh", **kw)；
// ts<0 即 ts=None（模块内取 clock.Now()）。
func recHandoff(t *testing.T, acc *Accounts, ts float64, sid string, extra Fields) map[string]any {
	t.Helper()
	f := Fields{
		"agent": "cc", "session_id": sid, "lineage_id": "L-" + sid,
		"project":  "C:/proj",
		"provider": "glm", "model": "glm-5.3", "price_ver": "glm@2026-09-17",
		"prompt_tokens": 100, "completion_tokens": 50, "outcome": "fresh", "wall_s": 1.2,
	}
	for k, v := range extra {
		f[k] = v
	}
	e, err := acc.Record("handoff", ts, f)
	if err != nil {
		t.Fatalf("recHandoff: %v", err)
	}
	return e
}

func newAcc(t *testing.T) *Accounts {
	t.Helper()
	acc, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return acc
}

func TestRecordAndRead(t *testing.T) {
	acc := newAcc(t)
	e := recHandoff(t, acc, -1, "s1", nil)
	if e["kind"] != "handoff" || e["v"] != 1 || e["ts"].(float64) <= 0 {
		t.Fatalf("entry = %v", e)
	}
	rows := acc.Read(ReadOpts{})
	if len(rows) != 1 || rows[0]["prompt_tokens"] != float64(100) {
		t.Fatalf("rows = %v", rows)
	}
	if rows[0]["ts_iso"] == "" { // 人读时间戳非空
		t.Fatalf("ts_iso 空: %v", rows[0])
	}
}

func TestAppendOnlyTwoLines(t *testing.T) {
	dir := t.TempDir()
	acc, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	recHandoff(t, acc, -1, "s1", nil)
	recHandoff(t, acc, -1, "s2", nil)
	data, err := os.ReadFile(filepath.Join(dir, "accounts", time.Now().Format("200601")+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(data, []byte{'\n'}); n != 2 { // 只增不改
		t.Fatalf("行数 = %d, want 2", n)
	}
}

func TestMonthlyRollover(t *testing.T) {
	dir := t.TempDir()
	acc, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	recHandoff(t, acc, AUG, "s1", nil)
	recHandoff(t, acc, SEP, "s1", nil)
	entries, err := os.ReadDir(filepath.Join(dir, "accounts"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, de := range entries {
		if strings.HasSuffix(de.Name(), ".jsonl") {
			names = append(names, de.Name())
		}
	}
	if len(names) != 2 || names[0] != "202608.jsonl" || names[1] != "202609.jsonl" {
		t.Fatalf("files = %v", names)
	}
}

func TestPrivacyWhitelist(t *testing.T) {
	acc := newAcc(t)
	_, err := acc.Record("handoff", -1, Fields{
		"agent": "cc", "session_id": "s", "lineage_id": "L", "project": "p",
		"provider": "x", "model": "m", "price_ver": nil,
		"prompt_tokens": 1, "completion_tokens": 1, "outcome": "fresh",
		"wall_s": 0.1, "content": "用户原话不应入账",
	})
	if err == nil || !strings.Contains(err.Error(), "隐私") {
		t.Fatalf("err = %v, want 含 隐私", err)
	}
	_, err = acc.Record("block", -1, Fields{"agent": "cc", "session_id": "s"}) // 缺 prefix_tokens/idle_s
	if err == nil || !strings.Contains(err.Error(), "缺必填") {
		t.Fatalf("err = %v, want 含 缺必填", err)
	}
}

func TestUnknownKind(t *testing.T) {
	acc := newAcc(t)
	_, err := acc.Record("wage", -1, Fields{"agent": "cc", "session_id": "s"})
	if err == nil || !strings.Contains(err.Error(), "未知科目") {
		t.Fatalf("err = %v, want 含 未知科目", err)
	}
}

func TestFilters(t *testing.T) {
	acc := newAcc(t)
	recHandoff(t, acc, MID, "s1", nil)
	_, err := acc.Record("block", MID, Fields{
		"agent": "cc", "session_id": "s2", "lineage_id": "L2",
		"project": "C:/q", "prefix_tokens": 150_000, "idle_s": 2100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rows := acc.Read(ReadOpts{Kind: "block"}); len(rows) != 1 {
		t.Fatalf("kind=block: %d 行", len(rows))
	} else if rows[0]["prefix_tokens"] != float64(150_000) {
		t.Fatalf("prefix_tokens = %v", rows[0]["prefix_tokens"])
	}
	if rows := acc.Read(ReadOpts{Session: "s1"}); len(rows) != 1 {
		t.Fatalf("session=s1: %d 行", len(rows))
	}
	if rows := acc.Read(ReadOpts{Project: "C:/q"}); len(rows) != 1 {
		t.Fatalf("project: %d 行", len(rows))
	}
	if rows := acc.Read(ReadOpts{Since: SEP + 1}); len(rows) != 0 {
		t.Fatalf("since=SEP+1: %d 行, want 0", len(rows))
	}
	if rows := acc.Read(ReadOpts{Until: AUG + 1}); len(rows) != 0 {
		t.Fatalf("until=AUG+1: %d 行, want 0", len(rows))
	}
	if rows := acc.Read(ReadOpts{Lineage: "L2"}); len(rows) != 1 {
		t.Fatalf("lineage=L2: %d 行", len(rows))
	}
}

func TestReservedFieldsStampedNotPassable(t *testing.T) {
	acc := newAcc(t)
	_, err := recHandoffErr(acc, -1, "s1", Fields{"v": 2})
	if err == nil || !strings.Contains(err.Error(), "保留字段") {
		t.Fatalf("err = %v, want 含 保留字段", err)
	}
	_, err = recHandoffErr(acc, -1, "s1", Fields{"ts_iso": "2020-01-01"})
	if err == nil || !strings.Contains(err.Error(), "保留字段") {
		t.Fatalf("err = %v, want 含 保留字段", err)
	}
}

// recHandoffErr 不 Fatal 的变体，供断言报错用。
func recHandoffErr(acc *Accounts, ts float64, sid string, extra Fields) (map[string]any, error) {
	f := Fields{
		"agent": "cc", "session_id": sid, "lineage_id": "L-" + sid,
		"project":  "C:/proj",
		"provider": "glm", "model": "glm-5.3", "price_ver": "glm@2026-09-17",
		"prompt_tokens": 100, "completion_tokens": 50, "outcome": "fresh", "wall_s": 1.2,
	}
	for k, v := range extra {
		f[k] = v
	}
	return acc.Record("handoff", ts, f)
}

// capturePipe 换掉 *os.Stderr / *os.Stdout，返回读回已捕获输出的函数。
func capturePipe(t *testing.T, target **os.File) (read func() string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := *target
	*target = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
		_ = r.Close()
	}()
	return func() string {
		_ = w.Close()
		*target = old
		return <-done
	}
}

func TestReadSkipsCorruptTailLine(t *testing.T) {
	dir := t.TempDir()
	acc, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	recHandoff(t, acc, -1, "s1", nil)
	f, err := os.OpenFile(filepath.Join(dir, "accounts", time.Now().Format("200601")+".jsonl"),
		os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"v": 1, "kind": "handoff", TRUN`); err != nil { // 模拟崩溃撕裂的尾行
		t.Fatal(err)
	}
	_ = f.Close()
	readErr := capturePipe(t, &os.Stderr)
	readOut := capturePipe(t, &os.Stdout)
	rows := acc.Read(ReadOpts{}) // 好行仍在，坏行被跳过
	errStr := readErr()
	outStr := readOut()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if !strings.Contains(errStr, "跳过损坏行") { // 终审：告警走 stderr——不污染 --json 的 stdout
		t.Fatalf("stderr = %q, want 含 跳过损坏行", errStr)
	}
	if outStr != "" { // stdout 保持机器可解析（无撕裂尾线）
		t.Fatalf("stdout = %q, want 空", outStr)
	}
}

func TestUsageKindRoundtrip(t *testing.T) {
	acc := newAcc(t)
	e, err := acc.Record("usage", 1758150005.0, Fields{
		"agent": "cc", "session_id": "s1", "lineage_id": "L1", "project": "C:/proj",
		"model": "glm-5.3", "title": "修登录bug", "input_tokens": 100,
		"cache_read_tokens": 9000, "cache_creation_tokens": 0,
		"output_tokens": 50, "offset": 2048,
	})
	if err != nil {
		t.Fatal(err)
	}
	if e["kind"] != "usage" || e["offset"] != 2048 {
		t.Fatalf("entry = %v", e)
	}
	_, err = acc.Record("usage", -1, Fields{
		"agent": "cc", "session_id": "s1", "model": "m", "title": "",
		"input_tokens": 1, "cache_read_tokens": 0, "cache_creation_tokens": 0,
		"output_tokens": 0, "offset": 1, "message_content": "泄漏",
	})
	if err == nil || !strings.Contains(err.Error(), "不落这些字段") {
		t.Fatalf("err = %v, want 含 不落这些字段", err)
	}
}

// ===== 票面附加验收：落盘行键序 + 并发安全 =====

func TestLineKeyOrder(t *testing.T) {
	acc := newAcc(t)
	recHandoff(t, acc, -1, "s1", nil)
	monthFile := filepath.Join(acc.dir, time.Now().Format("200601")+".jsonl")
	data, err := os.ReadFile(monthFile)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimRight(string(data), "\n")
	dec := json.NewDecoder(strings.NewReader(line))
	if _, err := dec.Token(); err != nil { // '{'
		t.Fatal(err)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token() // 键
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, tok.(string))
		if _, err := dec.Token(); err != nil { // 值（白名单均为标量）
			t.Fatal(err)
		}
	}
	// 公共八字段定序 + kind 字段字母序（确定性序列化）。
	want := []string{"v", "kind", "ts", "ts_iso", "agent", "session_id", "lineage_id", "project",
		"completion_tokens", "model", "outcome", "price_ver", "prompt_tokens", "provider", "wall_s"}
	if fmt.Sprint(keys) != fmt.Sprint(want) {
		t.Fatalf("键序 = %v, want %v", keys, want)
	}

	// 非 ASCII 直出（= ensure_ascii=False）：usage 的中文 title 不转义。
	acc2 := newAcc(t)
	if _, err := acc2.Record("usage", 1758150005.0, Fields{
		"agent": "cc", "session_id": "s1", "lineage_id": "L1", "project": "C:/proj",
		"model": "glm-5.3", "title": "修登录bug", "input_tokens": 100,
		"cache_read_tokens": 9000, "cache_creation_tokens": 0,
		"output_tokens": 50, "offset": 2048,
	}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(acc2.dir)
	raw, err := os.ReadFile(filepath.Join(acc2.dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("修登录bug")) {
		t.Fatalf("非 ASCII 被转义: %s", raw)
	}
}

func TestRecordConcurrentSafety(t *testing.T) {
	acc := newAcc(t)
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := acc.Record("handoff", -1, Fields{
				"agent": "cc", "session_id": fmt.Sprintf("s%d", i), "lineage_id": "L",
				"project": "C:/proj", "provider": "glm", "model": "glm-5.3",
				"price_ver": "glm@2026-09-17", "prompt_tokens": 100,
				"completion_tokens": 50, "outcome": "fresh", "wall_s": 1.2,
			}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	entries, err := os.ReadDir(acc.dir)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, de := range entries {
		data, err := os.ReadFile(filepath.Join(acc.dir, de.Name()))
		if err != nil {
			t.Fatal(err)
		}
		total += bytes.Count(data, []byte{'\n'})
	}
	if total != n {
		t.Fatalf("总行数 = %d, want %d", total, n)
	}
}

// ===== 以下 10 例为 e2e 占位（Python Harness 全链）=====
// 依赖 internal/daemon 的 Harness（gate/window/worker/serve，票 13/14/17），
// 仅 internal/accounts 就位时无法转绿。按票面 19 例 1:1 清点要求先落占位，
// 随归属票装配后移植转绿（届时删除 Skip、补全断言）。

func TestFerryCompletionBooksHandoff(t *testing.T) {
	t.Skip("e2e→票17：合成会话达总结阈值→摆渡→账本 handoff 行（session_id/lineage 尾缀/Provider=fake/outcome 三选一/无 content 无 md）；Python: test_accounts.py::test_ferry_completion_books_handoff")
}

func TestFailedFerryBooksExactlyOneRow(t *testing.T) {
	t.Skip("e2e→票17：失败摆渡只记一行 outcome=failed，骨架产物不另记行（终审：append-only 双行无法事后修复）；Python: test_accounts.py::test_failed_ferry_books_exactly_one_row")
}

func TestBookingFailureNeverBreaksFerry(t *testing.T) {
	t.Skip("e2e→票17：记账抛异常（坏价格表）时摆渡照常产出交接、worker 线程不死——骨架兜底不变量优先；Python: test_accounts.py::test_booking_failure_never_breaks_ferry")
}

func TestBlockBooksEntry(t *testing.T) {
	t.Skip("e2e→票14：enforce 拦截记 block 行（session_id/prefix_tokens 尽力而为/idle_s>0）；Python: test_accounts.py::test_block_books_entry")
}

func TestBypassBooksEntry(t *testing.T) {
	t.Skip("e2e→票14：强续 bypass 记 bypass 行；Python: test_accounts.py::test_bypass_books_entry")
}

func TestRestoreBooksInject(t *testing.T) {
	t.Skip("e2e→票14：归还记 inject 行（session_id=newsid/tokens>0/handoff_id/inject 与 block 同谱系 lineage_id——R9 Q7 因果链）；Python: test_accounts.py::test_restore_books_inject")
}

// 票13 回填注：4 个窗口 e2e（test_window_books_on_subagent_cycle /
// test_window_closes_on_prompt / test_window_closes_on_bypass_prompt /
// test_window_reanchors_after_leak_gap）已转绿——因 daemon→accounts 依赖方向
// 不可被内部测试包引用，落在本目录外部测试包 window_e2e_test.go（最小装配器：
// 临时目录 Accounts+Ledger+Daemon，直驱 Subagent/NoteGatePrompt）。
