package accounts

// accounts_aggregate_test.go — 聚合索引语义（ADR-0027 改判）：随写折叠与
// 流式整建的双路径一致、与盘上事实（Read 现算参照）一致、外部改写侦测
// （追加/收缩/同长改写）、月份裁剪、快照隔离、ReadWindow 月份裁剪。

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// recUsage 记一条 usage 流水（四列给定；科目必填字段齐装）。
func recUsage(t *testing.T, acc *Accounts, ts float64, model string, in, cr, cc, out int) {
	t.Helper()
	_, err := acc.Record("usage", ts, Fields{
		"agent": "cc", "session_id": "su", "lineage_id": "", "project": "C:/proj",
		"model": model, "title": "t", "input_tokens": in, "cache_read_tokens": cr,
		"cache_creation_tokens": cc, "output_tokens": out, "offset": 0, "subagent": "",
	})
	if err != nil {
		t.Fatalf("recUsage: %v", err)
	}
}

// usageLine 直写用的 usage 行（绕过 Record——外部改写侦测测试）。
func usageLine(model string, ts float64, in, cr, cc, out int) []byte {
	return []byte(fmt.Sprintf(
		`{"v":1,"kind":"usage","ts":%.3f,"ts_iso":"x","agent":"cc","session_id":"su","lineage_id":"","project":"C:/proj","cache_creation_tokens":%d,"cache_read_tokens":%d,"input_tokens":%d,"model":%q,"offset":0,"output_tokens":%d,"subagent":"","title":"t"}`+"\n",
		ts, cc, cr, in, model, out))
}

// handoffLine 直写用的 handoff 行（科目必填字段齐装）。
func handoffLine(sid string, ts float64) []byte {
	return []byte(fmt.Sprintf(
		`{"v":1,"kind":"handoff","ts":%.3f,"ts_iso":"x","agent":"cc","session_id":%q,"lineage_id":"L","project":"C:/proj","completion_tokens":1,"outcome":"fresh","prompt_tokens":1,"provider":"glm","wall_s":1}`+"\n",
		ts, sid))
}

// rawWrite/rawAppend 直写月文件（与旧缓存测试同款手法）。
func rawWrite(t *testing.T, acc *Accounts, month string, content []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(acc.dir, month+".jsonl"),
		content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func rawAppend(t *testing.T, acc *Accounts, month string, content []byte) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(acc.dir, month+".jsonl"),
		os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// snapUsageSum 全快照四列总和（断言用小工具）。
func snapUsageSum(s AggSnapshot) float64 {
	var sum float64
	for _, c := range s.Usage {
		sum += c.In + c.CacheRead + c.CacheWrite + c.Out
	}
	return sum
}

func TestAggFoldMatchesRebuild(t *testing.T) {
	dir := t.TempDir()
	acc, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 混合：两月、同日同模型累加、handoff 计数、大小写模型名（折叠侧应小写）。
	recUsage(t, acc, AUG, "GLM-5.3", 100, 10, 20, 30)
	recHandoff(t, acc, AUG, "s1", nil)
	recUsage(t, acc, AUG+3600, "glm-5.3", 1, 1, 1, 1)
	recUsage(t, acc, SEP, "kimi-k3", 1, 2, 3, 4)
	recHandoff(t, acc, SEP, "s2", nil)
	recHandoff(t, acc, SEP, "s3", nil)
	fold := acc.AggregateSnapshot() // 随写折叠路径（未整建）

	acc2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	acc2.RebuildAggregates() // 流式解析路径
	rebuild := acc2.AggregateSnapshot()

	if !reflect.DeepEqual(fold, rebuild) {
		t.Fatalf("折叠路径与整建路径不一致:\n fold=%+v\n rebuild=%+v", fold, rebuild)
	}
	if len(rebuild.Usage) != 2 || len(rebuild.Handoff) != 2 {
		t.Fatalf("格数 usage=%d handoff=%d, want 2/2", len(rebuild.Usage), len(rebuild.Handoff))
	}
	for _, c := range rebuild.Usage {
		if c.Model != "glm-5.3" && c.Model != "kimi-k3" {
			t.Fatalf("模型键未小写: %q", c.Model)
		}
	}
}

func TestAggRebuildMatchesGroundTruth(t *testing.T) {
	acc := newAcc(t)
	recUsage(t, acc, AUG, "glm-5.3", 100, 10, 20, 30)
	recHandoff(t, acc, AUG, "s1", nil)
	recHandoff(t, acc, SEP, "s2", nil)
	raw := append(usageLine("kimi-k3", SEP+60, 5, 5, 5, 5), []byte("{broken\n")...)
	rawWrite(t, acc, "202609", raw)
	acc.RebuildAggregates()
	snap := acc.AggregateSnapshot()

	// 参照：Read 现算（盘上事实）。
	type uk struct {
		m     string
		d     int
		model string
	}
	usageRef := map[uk][4]float64{}
	handoffRef := map[uk]int{}
	for _, e := range acc.Read(ReadOpts{}) {
		ts, _ := e["ts"].(float64)
		tt := time.Unix(int64(ts), 0).In(time.Local)
		k := uk{m: tt.Format("200601"), d: tt.Day()}
		switch e["kind"] {
		case "usage":
			k.model = strOr(e, "model")
			v := usageRef[k]
			v[0] += numOr(e, "input_tokens")
			v[1] += numOr(e, "cache_read_tokens")
			v[2] += numOr(e, "cache_creation_tokens")
			v[3] += numOr(e, "output_tokens")
			usageRef[k] = v
		case "handoff":
			handoffRef[k]++
		}
	}
	if len(usageRef) != len(snap.Usage) || len(handoffRef) != len(snap.Handoff) {
		t.Fatalf("格数不符: usage %d/%d handoff %d/%d（坏行应两侧同跳）",
			len(snap.Usage), len(usageRef), len(snap.Handoff), len(handoffRef))
	}
	for _, c := range snap.Usage {
		k := uk{m: c.Month, d: c.Day, model: c.Model}
		v, ok := usageRef[k]
		if !ok {
			t.Fatalf("聚合格 %+v 在参照中缺失", k)
		}
		if c.In != v[0] || c.CacheRead != v[1] || c.CacheWrite != v[2] || c.Out != v[3] {
			t.Fatalf("格 %+v 四列不等: 聚合=%v 参照=%v", k, c, v)
		}
	}
	for _, c := range snap.Handoff {
		k := uk{m: c.Month, d: c.Day}
		if handoffRef[k] != c.Count {
			t.Fatalf("handoff 格 %+v = %d, want %d", k, c.Count, handoffRef[k])
		}
	}
}

func TestAggExternalAppendAndRewrite(t *testing.T) {
	acc := newAcc(t)
	rawWrite(t, acc, "202609", usageLine("glm-5.3", SEP, 100, 100, 100, 100))
	if sum := snapUsageSum(acc.AggregateSnapshot()); sum != 400 {
		t.Fatalf("外部直写首建 sum = %v, want 400", sum)
	}
	rawAppend(t, acc, "202609", usageLine("glm-5.3", SEP+60, 1, 1, 1, 1))
	if sum := snapUsageSum(acc.AggregateSnapshot()); sum != 404 {
		t.Fatalf("外部追加 sum = %v, want 404（盘面戳未侦测追加？）", sum)
	}
	time.Sleep(2 * time.Millisecond)                                          // mtime 粒度保险
	rawWrite(t, acc, "202609", usageLine("glm-5.3", SEP, 200, 200, 200, 200)) // 同长改写（收缩+同值宽）
	if sum := snapUsageSum(acc.AggregateSnapshot()); sum != 800 {
		t.Fatalf("外部改写 sum = %v, want 800（盘面戳未侦测改写？）", sum)
	}
}

func TestAggMonthTrim(t *testing.T) {
	acc := newAcc(t)
	jul := float64(time.Date(2026, 7, 15, 12, 0, 0, 0, time.Local).Unix())
	rawWrite(t, acc, "202607", handoffLine("j1", jul))
	rawWrite(t, acc, "202608", handoffLine("a1", AUG))
	rawWrite(t, acc, "202609", handoffLine("s1", SEP))
	snap := acc.AggregateSnapshot()
	if len(snap.Handoff) != 2 {
		t.Fatalf("裁剪后 handoff 格 = %d, want 2（留最新两月）", len(snap.Handoff))
	}
	for _, c := range snap.Handoff {
		if c.Month == "202607" {
			t.Fatalf("202607 未被裁剪: %+v", c)
		}
	}
}

func TestAggSnapshotIsolation(t *testing.T) {
	acc := newAcc(t)
	recUsage(t, acc, AUG, "glm-5.3", 100, 0, 0, 0)
	snap := acc.AggregateSnapshot()
	if len(snap.Usage) != 1 {
		t.Fatalf("格数 = %d, want 1", len(snap.Usage))
	}
	snap.Usage[0].In = 999999
	if again := acc.AggregateSnapshot(); again.Usage[0].In != 100 {
		t.Fatalf("快照被外部改动传染: In = %v, want 100（非深拷贝？）", again.Usage[0].In)
	}
}

func TestAggIgnoresOtherKinds(t *testing.T) {
	acc := newAcc(t)
	_, err := acc.Record("beat", AUG, Fields{
		"agent": "cc", "session_id": "s", "lineage_id": "", "project": "p",
		"provider": "glm", "model": "glm-5.3", "price_ver": "v",
		"prefix_tokens": 10, "cache_read": 5, "outcome": "hit",
		"cost_pred": 0.1, "cost_actual": 0.1, "lane": "qwatch",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap := acc.AggregateSnapshot(); len(snap.Usage) != 0 {
		t.Fatalf("beat 行入了聚合: %+v（聚合面只收 usage/handoff）", snap.Usage)
	}
}

func TestReadWindowScopesMonths(t *testing.T) {
	acc := newAcc(t)
	recHandoff(t, acc, AUG, "a", nil) // 202608
	recHandoff(t, acc, SEP, "s", nil) // 202609
	// 窗在 9 月 → 只点名 202609（行过滤与月裁剪双保险）
	if rows := acc.ReadWindow(ReadOpts{Since: SEP - 86400, Until: SEP + 86400}); len(rows) != 1 || rows[0]["session_id"] != "s" {
		t.Fatalf("窗内单月 rows = %v, want [s]", rows)
	}
	// 窗跨两月 → 两个月文件都读
	if rows := acc.ReadWindow(ReadOpts{Since: AUG, Until: SEP}); len(rows) != 2 {
		t.Fatalf("跨月窗 rows = %d, want 2", len(rows))
	}
	// Since 缺省（全时段）→ 回落全目录 Read
	if rows := acc.ReadWindow(ReadOpts{}); len(rows) != 2 {
		t.Fatalf("全时段 rows = %d, want 2", len(rows))
	}
}

// 尾段无 \n：splitlines 语义（末段计行）在聚合整建路径同样成立。
func TestAggTailSegmentCounts(t *testing.T) {
	acc := newAcc(t)
	rawWrite(t, acc, "202609", bytes.TrimSuffix(usageLine("glm-5.3", SEP, 10, 10, 10, 10), []byte("\n")))
	if sum := snapUsageSum(acc.AggregateSnapshot()); sum != 40 {
		t.Fatalf("尾段计行 sum = %v, want 40", sum)
	}
}
