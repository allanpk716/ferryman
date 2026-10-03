package daemon

// dsh phase 2 P2-1 守望＋台账 daemon 面钉子：
//   - pollDsh 登记面：主会话 Touch("dsh")（cwd 来头行）+ 重启观察窗；子代理
//     会话（自头 origin=subagent）不 Touch 不入摆渡队；
//   - 四列入账：assistant/message TokenUsage → usage 科目（input/cache_read/
//     cache_creation/output 直接映射；不相交口径，计费输入=三者之和）；
//   - 子代理随父入账：session_id=父 sid、subagent=子 sid（CC 票01 同款）；
//   - 断点：轮间幂等（偏移推进）；daemon 重启后从账本恢复不重采；
//   - 台账 title/peak 由采集顺带回写（enrich 的 dsh 替身）。

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/klauspost/compress/zstd"

	"ferryman/internal/accounts"
	"ferryman/internal/config"
	"ferryman/internal/ledger"
)

const (
	dshMainID  = "session-11111111-1111-4111-8111-111111111111"
	dshChildID = "session-22222222-2222-4222-8222-222222222222"
)

// zstdBatches 每批一帧（checksummed 帧拼接，dsh 写方同构）。
func zstdBatches(batches ...string) []byte {
	var out bytes.Buffer
	for _, b := range batches {
		w, _ := zstd.NewWriter(&out, zstd.WithEncoderCRC(true), zstd.WithEncoderConcurrency(1))
		w.Write([]byte(b))
		w.Close()
	}
	return out.Bytes()
}

// writeDshSession 造一个会话目录（真机布局：root/--C-proj--/<id>/session.vN…）。
func writeDshSession(t *testing.T, root, id, header string, batches ...string) string {
	t.Helper()
	dir := filepath.Join(root, "--C-proj--", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "session.v4.jsonl.zstd")
	if err := os.WriteFile(p, zstdBatches(append([]string{header}, batches...)...), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const dshMainHeader = `{"type":"session","version":4,"id":"session-11111111-1111-4111-8111-111111111111","createdAt":1790905220031,"cwd":"C:\\proj","isSeeded":false,"delegationDepth":0}` + "\n"

const dshChildHeader = `{"type":"session","version":4,"id":"session-22222222-2222-4222-8222-222222222222","createdAt":1790905290000,"cwd":"C:\\proj","parentSession":"session-11111111-1111-4111-8111-111111111111","isSeeded":true,"origin":"subagent","delegationDepth":1}` + "\n"

// newDshWatcher 最小守望装配（dsh 根指临时目录；入队记录器验证不误入）。
func newDshWatcher(t *testing.T, root, accDir string) (*Watcher, *accounts.Accounts, *[]string, *sync.Mutex) {
	t.Helper()
	acc, err := accounts.New(accDir)
	if err != nil {
		t.Fatal(err)
	}
	led := ledger.New()
	var mu sync.Mutex
	ids := []string{}
	cfg := config.Default()
	cfg.Watch.DshSessionsDir = root
	w := NewWatcher(cfg, led, nil, func(st *ledger.SessionState) bool {
		mu.Lock()
		ids = append(ids, st.Agent+"/"+st.SessionID)
		mu.Unlock()
		return true
	}, 0, acc, nil, nil, nil)
	return w, acc, &ids, &mu
}

func dshUsageRows(t *testing.T, acc *accounts.Accounts) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, e := range acc.Read(accounts.ReadOpts{Kind: "usage"}) {
		if ag, _ := e["agent"].(string); ag == "dsh" {
			out = append(out, e)
		}
	}
	return out
}

func TestPollDshRegistersAndHarvests(t *testing.T) {
	root := t.TempDir()
	accDir := t.TempDir()
	mainPath := writeDshSession(t, root, dshMainID, dshMainHeader,
		// 批 1：标题 + 两条 usage（第二条带缓存列）。
		`{"type":"session/title","seq":2,"time":1790905230000,"data":{"title":"dsh 接线测试","messageSeqs":[1],"source":"user"}}`+"\n"+
			`{"type":"assistant/message","seq":3,"time":1790905227524,"data":{"message":{"source":{"kind":"model","provider":"ferryman-dock","model":"claude-opus-5","replayState":{"response":{"responseModel":"GLM-5.3"}}}},"usage":{"inputTokens":9169,"outputTokens":21,"totalTokens":9190}}}`+"\n"+
			`{"type":"assistant/message","seq":4,"time":1790905240000,"data":{"message":{"source":{"kind":"model","provider":"zai-coding-cn","model":"glm-5.3"}},"usage":{"inputTokens":120,"outputTokens":80,"cacheReadTokens":30000,"cacheWriteTokens":5000}}}`+"\n",
		// 批 2：无 usage 的 assistant 消息（不出行）。
		`{"type":"assistant/message","seq":5,"time":1790905250000,"data":{"message":{"source":{"kind":"model","provider":"x","model":"y"}}}}`+"\n")
	_ = mainPath
	writeDshSession(t, root, dshChildID, dshChildHeader,
		`{"type":"assistant/message","seq":1,"time":1790905300000,"data":{"message":{"source":{"kind":"model","provider":"zai-coding-cn","model":"glm-5.3"}},"usage":{"inputTokens":500,"outputTokens":50,"cacheReadTokens":8000}}}`+"\n")

	w, acc, enq, mu := newDshWatcher(t, root, accDir)
	w.pollDsh()

	// 登记面：主会话在台账（cwd 来头行、title/peak 由采集回写）；子会话不在。
	st := w.Ledger.Get("dsh", dshMainID)
	if st == nil {
		t.Fatal("主会话未登记")
	}
	if st.Cwd != "C:\\proj" {
		t.Errorf("cwd = %q, want C:\\proj", st.Cwd)
	}
	if st.Title != "dsh 接线测试" {
		t.Errorf("title = %q", st.Title)
	}
	if st.PeakCtx != 35120 { // 计费输入峰值：120+30000+5000
		t.Errorf("peak = %d, want 35120", st.PeakCtx)
	}
	if w.Ledger.Get("dsh", dshChildID) != nil {
		t.Error("子代理会话不应 Touch")
	}

	// 四列入账：主 2 行 + 子 1 行。
	rows := dshUsageRows(t, acc)
	if len(rows) != 3 {
		t.Fatalf("want 3 usage rows, got %d: %+v", len(rows), rows)
	}
	var mainRows, childRows int
	for _, r := range rows {
		sid, _ := r["session_id"].(string)
		sub, _ := r["subagent"].(string)
		agent, _ := r["agent"].(string)
		if agent != "dsh" {
			t.Errorf("agent = %v", r["agent"])
		}
		switch {
		case sid == dshMainID && sub == "":
			mainRows++
			if r["model"] != "GLM-5.3" && r["model"] != "glm-5.3" {
				t.Errorf("主行 model = %v", r["model"])
			}
			if r["title"] != "dsh 接线测试" {
				t.Errorf("主行 title = %v", r["title"])
			}
		case sid == dshMainID && sub == dshChildID:
			childRows++
			if r["input_tokens"] != float64(500) || r["cache_read_tokens"] != float64(8000) ||
				r["cache_creation_tokens"] != float64(0) || r["output_tokens"] != float64(50) {
				t.Errorf("子行四列 = %v/%v/%v/%v", r["input_tokens"], r["cache_read_tokens"],
					r["cache_creation_tokens"], r["output_tokens"])
			}
		default:
			t.Errorf("意外行: sid=%s sub=%s", sid, sub)
		}
	}
	if mainRows != 2 || childRows != 1 {
		t.Errorf("主 %d 行（want 2）、子 %d 行（want 1）", mainRows, childRows)
	}

	// 幂等：再轮无新行。
	w.pollDsh()
	if rows := dshUsageRows(t, acc); len(rows) != 3 {
		t.Errorf("重复轮多出 %d 行", len(rows)-3)
	}

	// 追加一批：恰一行新增。
	f, err := os.OpenFile(mainPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(zstdBatches(`{"type":"assistant/message","seq":6,"time":1790905260000,"data":{"message":{"source":{"kind":"model","provider":"x","model":"y"}},"usage":{"inputTokens":10,"outputTokens":5}}}` + "\n"))
	f.Close()
	w.pollDsh()
	if rows := dshUsageRows(t, acc); len(rows) != 4 {
		t.Errorf("追加后 want 4 行, got %d", len(rows))
	}

	// 不误入摆渡队。
	mu.Lock()
	defer mu.Unlock()
	for _, id := range *enq {
		if id == "dsh/"+dshMainID {
			t.Error("dsh 会话不应入摆渡队（P2-2/P2-3 接线前）")
		}
	}
}

func TestPollDshRestartResume(t *testing.T) {
	root := t.TempDir()
	accDir := t.TempDir()
	mainPath := writeDshSession(t, root, dshMainID, dshMainHeader,
		`{"type":"assistant/message","seq":3,"time":1790905227524,"data":{"message":{"source":{"model":"glm-5.3"}},"usage":{"inputTokens":9169,"outputTokens":21}}}`+"\n")
	w1, acc1, _, _ := newDshWatcher(t, root, accDir)
	w1.pollDsh()
	if rows := dshUsageRows(t, acc1); len(rows) != 1 {
		t.Fatalf("首轮 want 1 行, got %d", len(rows))
	}
	// daemon 重启（同账本）：断点从 usage 行恢复，不重采。
	w2, acc2, _, _ := newDshWatcher(t, root, accDir)
	w2.pollDsh()
	if rows := dshUsageRows(t, acc2); len(rows) != 1 {
		t.Errorf("重启后重采了 %d 行（want 1）", len(rows)-1)
	}
	// 台账登记照常（Touch 幂等）。
	if w2.Ledger.Get("dsh", dshMainID) == nil {
		t.Error("重启后主会话应再登记")
	}
	_ = mainPath
}

func TestPollDshRootAbsent(t *testing.T) {
	// 根不存在：静默零开销（未装 dsh 的机器）。
	w, _, _, _ := newDshWatcher(t, filepath.Join(t.TempDir(), "nope"), t.TempDir())
	w.pollDsh()
	if n := len(w.Ledger.AllSessions()); n != 0 {
		t.Errorf("空根不应登记: %d", n)
	}
}

func TestPollDshHarvestOff(t *testing.T) {
	// HarvestUsage 关：只登记不采集（w.dsh=nil 形态）。
	root := t.TempDir()
	accDir := t.TempDir()
	writeDshSession(t, root, dshMainID, dshMainHeader,
		`{"type":"assistant/message","seq":3,"time":1790905227524,"data":{"message":{"source":{"model":"glm-5.3"}},"usage":{"inputTokens":1,"outputTokens":1}}}`+"\n")
	acc, err := accounts.New(accDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Watch.DshSessionsDir = root
	cfg.Watch.HarvestUsage = false
	w := NewWatcher(cfg, ledger.New(), nil, func(*ledger.SessionState) bool { return true },
		0, acc, nil, nil, nil)
	if w.dsh != nil {
		t.Fatal("HarvestUsage 关时不应建 dsh 采集器")
	}
	w.pollDsh()
	if w.Ledger.Get("dsh", dshMainID) == nil {
		t.Error("登记面应照常")
	}
	if rows := dshUsageRows(t, acc); len(rows) != 0 {
		t.Errorf("不应采集: %d 行", len(rows))
	}
}

// TestPollDshRealSessions 真机 daemon 面实测（P2-1 完成定义）：FERRYMAN_DSH_
// REAL_SESSIONS 指到 ~/.dsh/sessions 时，pollDsh 对真会话完成登记＋四列
// 采集＋幂等（第二轮零新增）。默认跳过（hermetic CI）。
func TestPollDshRealSessions(t *testing.T) {
	root := os.Getenv("FERRYMAN_DSH_REAL_SESSIONS")
	if root == "" {
		t.Skip("FERRYMAN_DSH_REAL_SESSIONS 未置位——跳过真机 daemon 面实测")
	}
	w, acc, _, _ := newDshWatcher(t, root, t.TempDir())
	w.pollDsh()
	rows := dshUsageRows(t, acc)
	registered := len(w.Ledger.AllSessions())
	if registered == 0 {
		t.Fatalf("真机根 %s 下未登记任何 dsh 会话", root)
	}
	if len(rows) == 0 {
		t.Fatalf("真机根 %s 下零 usage 行（应至少有渡口测试会话的出行）", root)
	}
	for _, r := range rows {
		for _, k := range []string{"input_tokens", "cache_read_tokens",
			"cache_creation_tokens", "output_tokens", "offset", "subagent", "model"} {
			if _, ok := r[k]; !ok {
				t.Errorf("真机行缺 %s: %+v", k, r)
			}
		}
	}
	// 幂等：第二轮零新增（偏移断点生效）。
	w.pollDsh()
	if again := dshUsageRows(t, acc); len(again) != len(rows) {
		t.Errorf("第二轮新增 %d 行（want 0）", len(again)-len(rows))
	}
	t.Logf("真机 daemon 面：登记 %d 会话、usage %d 行、四列齐全、幂等通过",
		registered, len(rows))
}
