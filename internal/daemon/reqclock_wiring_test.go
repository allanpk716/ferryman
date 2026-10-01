package daemon

// reqclock_wiring_test.go — 票03:判热钟持久化装配钉子(D6;review_blocks
// F5/F8)。修同模型摆渡重启失忆:守护重启后判热钟不归零——快照回种后判热门
// 按真实钟值判,重启观察窗不再把"闲置 20-24 分钟、缓存仍活"的会话整批误判
// 冷(spec Problem Statement 2)。
//
// 验收对照:
//   - 回种生效:真快照文件 → 装配形 watcher → maybeSameModel 判热通过
//     (红钉:旧码纯内存恒无观测=-1 判冷——该旧行为由既有
//     TestSameModelSkipColdPath 钉死,与本钉构成红绿对照);
//   - 缺失快照 = 空钟起步(fail-safe,现状语义,保守判冷,绝不伪造热);
//   - 装配形 = serve.go serveConfig 票03 同款两行(NewWatcher 后替换
//     ReqClock)。装配放 serve 而 NewWatcher 保持内存形:测试直构 Watcher
//     的形态众多且 cfg.DataDir() 缺省解析到生产 ~/ferryman,装配下沉
//     NewWatcher 会让测试直写生产数据目录(本票副作用声明红线)。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"ferryman/internal/beat"
	"ferryman/internal/ferry"
	"ferryman/internal/ledger"
)

// reqClockBaseT 冻结时钟基点(任意 epoch 秒;与 smBaseT 同域独立命名)。
const reqClockBaseT = 1_800_000_000.0

// wireReqClockSnapshot serve.go serveConfig 票03 装配两行的镜像(文件名同生产
// reqclock.json;测试一律接 t.TempDir(),防写生产数据目录)。
func wireReqClockSnapshot(w *Watcher, dir string) {
	w.ReqClock = beat.NewPersistentLastRequestClock(filepath.Join(dir, "reqclock.json"))
}

// TestReqclockReseedPassesHeatGate 红绿钉二:快照钟=20 分钟前 + 台账闲置
// 20 分钟 → maybeSameModel 判热通过(1200s ≤ τ=0.8×1800=1440s;旧码红:
// 无观测 -1 判冷)。
func TestReqclockReseedPassesHeatGate(t *testing.T) {
	now := freezeClock(t, reqClockBaseT)
	led := ledger.New()
	tmp := t.TempDir()
	sink := newSkipSink()
	w := newTestWatcherW(smGateCfg(), led, nil, nil, nil, nil)
	w.bookSameModelSkipFn = sink.record
	w.smSyncExec = true // 同步直调形态:断言免竞态(生产恒异步)
	w.ArmVerdict = func(string) (bool, bool) { return true, true }
	// 真快照文件落盘(重启前的最后观测:该会话 20 分钟前被真发保温过)——
	// 持久化文件读回必须真测:JSON 从盘上读回,不经内存捷径。
	b, err := json.Marshal(map[string]any{
		"sessions": map[string]float64{"sm-reseed1": *now - 1200}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "reqclock.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	wireReqClockSnapshot(w, tmp) // serve.go 装配形
	// 回种真到钟:Last 返回快照值
	if ts, ok := w.ReqClock.Last("sm-reseed1"); !ok || ts != *now-1200 {
		t.Fatalf("装配应回种快照: Last = %v,%v want %v,true", ts, ok, *now-1200)
	}
	st, _ := bareSession(t, led, tmp, "sm-reseed1")
	setLastWrite(led, st, *now-1200) // 台账闲置 20 分钟:触发带内(生效阈值 6s)
	w.maybeSameModel(st)
	// 判热通过:不记跳过事件(无发送器降级=静默交还既有调度,执行档钉子另列)
	if got := sink.count("sm-reseed1"); got != 0 {
		t.Fatalf("回种后判热应通过(旧码 -1 判冷会记 cold), got %d 次, last=%q",
			got, sink.last("sm-reseed1"))
	}
}

// TestReqclockReseedMissingSnapshotStaysCold 缺失快照 = 空钟起步:装配后无
// 观测 → -1 判冷(fail-safe,现状语义;F5 同款保守,绝不伪造热)。
func TestReqclockReseedMissingSnapshotStaysCold(t *testing.T) {
	now := freezeClock(t, reqClockBaseT)
	led := ledger.New()
	tmp := t.TempDir()
	sink := newSkipSink()
	w := newTestWatcherW(smGateCfg(), led, nil, nil, nil, nil)
	w.bookSameModelSkipFn = sink.record
	w.ArmVerdict = func(string) (bool, bool) { return true, true }
	wireReqClockSnapshot(w, tmp) // tmp 下无 reqclock.json:缺失形态
	if _, ok := w.ReqClock.Last("sm-miss1"); ok {
		t.Fatal("缺失快照应空钟起步")
	}
	st, _ := bareSession(t, led, tmp, "sm-miss1")
	setLastWrite(led, st, *now-7)
	w.maybeSameModel(st)
	if r := sink.last("sm-miss1"); r != ferry.SameModelSkipCold {
		t.Fatalf("缺失快照应保守判冷(现状语义), got %q", r)
	}
}
