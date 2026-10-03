package daemon

// watcher_dsh_real_test.go — P2-2 真会话对账（完成定义：渡口账面与
// ~/.dsh/sessions 实物双向核对）。
//
// 门（缺任一即跳过——hermetic CI 不依赖本机数据；本机验收时置位跑）：
//   - FERRYMAN_DSH_REAL_SESSIONS → ~/.dsh/sessions
//   - FERRYMAN_DSH_REAL_LEDGER   → Ferryman 数据根（含 accounts/，如 ~/ferryman）
//   - FERRYMAN_DSH_REAL_SINCE    → 可选 RFC3339：接管时刻之后的文件 usage 行
//     **必须**在渡口账面找到匹配行（硬断言）；之前的行机会式匹配（只对账
//     不强制——接管前流量走直连，无渡口行是正常态）。
//
// 对账法（P2-1 真机对账法推广到渡口面）：文件侧 assistant/message usage 行与
// 渡口 dock 行同属不相交四列口径（input=未缓存/cache_read/cache_write/output），
// 以「四列签名＋±120 秒窗口」匹配。键不变式（对齐断言，跨时代恒成立）：
// 匹配行的 session_id ∈ {"", 会话头行 id}——渡口对 dsh 的归因键要么为空
//（pi-ai 路现状：请求体无 metadata.session_id、头回落无键，见
// dock.HeaderDeepSeekHarnessSessionID 注记），要么恰等于守望/台账键
// session-<uuid>；绝不出现第三种值（键污染即失配）。
//
// 反向（渡口→文件）只记数不强制：dsh 的 session-title/compaction 类小请求在
// 渡口有行、会话文件无 usage 行（title 响应不入 assistant/message），属预期。

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/dshtrans"
)

func TestRealDshSessionsVsDockLedger(t *testing.T) {
	root := os.Getenv("FERRYMAN_DSH_REAL_SESSIONS")
	ledgerDir := os.Getenv("FERRYMAN_DSH_REAL_LEDGER")
	if root == "" || ledgerDir == "" {
		t.Skip("FERRYMAN_DSH_REAL_SESSIONS / FERRYMAN_DSH_REAL_LEDGER 未置位——跳过真机对账（本机验收时分别指到 ~/.dsh/sessions 与 ~/ferryman）")
	}
	var since float64
	if s := os.Getenv("FERRYMAN_DSH_REAL_SINCE"); s != "" {
		tm, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("FERRYMAN_DSH_REAL_SINCE 非法 RFC3339: %v", err)
		}
		since = float64(tm.Unix())
	}

	acc, err := accounts.New(ledgerDir)
	if err != nil {
		t.Fatalf("开账本: %v", err)
	}
	type dockRow struct {
		ts      float64
		sid     string
		agent   string
		input   float64
		cacheR  float64
		cacheW  float64
		output  float64
		matched bool
	}
	var dockRows []dockRow
	for _, e := range acc.Read(accounts.ReadOpts{Kind: "dock"}) {
		dockRows = append(dockRows, dockRow{
			ts:     numField(e, "ts"),
			sid:    strField(e, "session_id"),
			agent:  strField(e, "agent"),
			input:  numField(e, "input_tokens"),
			cacheR: numField(e, "cache_read_tokens"),
			cacheW: numField(e, "cache_creation_tokens"),
			output: numField(e, "output_tokens"),
		})
	}

	var sessions, fileRows, matchedRows, keyedRows int
	var fileTS []float64 // 文件 usage 行时间戳（反向邻域筛选用）
	projects, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("读会话根: %v", err)
	}
	for _, proj := range projects {
		if !proj.IsDir() {
			continue
		}
		dirs, err := os.ReadDir(filepath.Join(root, proj.Name()))
		if err != nil {
			continue
		}
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			sessions++
			dir := filepath.Join(root, proj.Name(), d.Name())
			gen, ok := dshtrans.LatestGeneration(dir)
			if !ok {
				continue
			}
			line, ok := dshtrans.ReadHeaderLine(gen.Path, gen.Zstd)
			if !ok {
				continue
			}
			h, ok := dshtrans.ParseHeaderLine(line)
			if !ok {
				continue
			}
			res := dshtrans.TailText(gen.Path, gen.Zstd, 0)
			rows, _, _ := dshtrans.ParseChunk(res.Text, "")
			for _, r := range rows {
				if !r.HasTS {
					continue // 无时间戳无法对窗口
				}
				fileRows++
				fileTS = append(fileTS, r.TS)
				matched := false
				for i := range dockRows {
					dr := &dockRows[i]
					if dr.matched || math.Abs(dr.ts-r.TS) > 120 {
						continue
					}
					if dr.input != float64(r.InputTokens) || dr.output != float64(r.OutputTokens) ||
						dr.cacheR != float64(r.CacheReadTokens) || dr.cacheW != float64(r.CacheWriteTokens) {
						continue
					}
					// 键不变式：渡口归因键要么空（pi-ai 路现状）要么恰等于守望键。
					if dr.sid != "" && dr.sid != h.ID {
						t.Errorf("%s: 渡口行(ts=%.1f) session_id=%q 为第三种键（既非空也非会话键 %q）——键污染",
							d.Name(), dr.ts, dr.sid, h.ID)
					}
					dr.matched = true
					matched = true
					if dr.sid == h.ID {
						keyedRows++
					}
					break
				}
				if matched {
					matchedRows++
				} else if since > 0 && r.TS >= since {
					t.Errorf("%s: 接管后 usage 行(ts=%.1f, in=%d out=%d) 在渡口账面无匹配——四列+窗口对不上",
						d.Name(), r.TS, r.InputTokens, r.OutputTokens)
				}
			}
		}
	}
	if sessions == 0 {
		t.Fatalf("根目录 %s 下无会话目录", root)
	}
	// 反向邻域计数（只记数不强制）：dsh 的 title/compaction 类小请求在渡口有
	// 行、文件无 usage 行，属预期。邻域＝文件行 ±120s 内且（agent=dsh 或键空
	// ——发版前 dsh 行被记 cc 且键空，发版后 agent=dsh 可直认；键非空的 CC
	// 邻居行不算 dsh 面）。
	near := func(ts float64) bool {
		for _, f := range fileTS {
			if math.Abs(ts-f) <= 120 {
				return true
			}
		}
		return false
	}
	unmatched := 0
	for i := range dockRows {
		dr := &dockRows[i]
		if !dr.matched && near(dr.ts) && (dr.agent == "dsh" || dr.sid == "") {
			unmatched++
		}
	}
	t.Logf("真机对账：会话 %d、文件 usage 行 %d、渡口匹配 %d（其中按会话键归因 %d、空键 %d）、邻域未匹配行 %d（title/compaction 类属预期）",
		sessions, fileRows, matchedRows, keyedRows, matchedRows-keyedRows, unmatched)
}

// numField 账本行数值字段（缺省 0，JSON 数一律 float64）。
func numField(e map[string]any, k string) float64 {
	if v, ok := e[k].(float64); ok {
		return v
	}
	return 0
}

// strField 账本行字符串字段（缺省 ""）。
func strField(e map[string]any, k string) string {
	s, _ := e[k].(string)
	return s
}
