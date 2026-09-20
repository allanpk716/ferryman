package daemon

// query_beats.go — 票03：GET /beats 实现（注册于 queryapi.go 的
// queryEndpoints 分派表）。
//
// 在飞窗两源：等待窗（windows.go 窗口表，waitWindowOpenLocked 零副作用只读
// 探测——懒过期闭账留给正规路径，票01 同款）＋问询窗（台账 QWatchOpenedTS，
// 锁内快照）。遥测逐窗聚合账本 beat 科目流水（验收口径：遥测计数与 beat 行
// 一致；窗口内存态 waitLane/QWatchStats 是守望私有计数器，Daemon 无引用，
// 账本行是唯一跨面只读事实源）。
//
// 预估 close_reason（收尾四态见 CONTEXT「等待窗口」）：活跃等待窗→
// subagents_done（常规收口：计数归零同步闭窗；主会话提前来讯走 prompt——
// 预估取常规路径）；停车等待窗→main_resumed（预期 async 真身完成主会话恢复；
// 超 1h 未恢复走 expired 懒过期——已超者不在在飞清单）；问询窗→write（随任
// 何新写入立即关闭）。
//
// TTL 观测值（CONTEXT「遥测」：每次心跳实收 cacheRead 构成的线上 TTL 观测）
// ＝本窗最强 hit 证据：hit 跳证明缓存存活至少（该跳时刻−开窗）秒，取最大深
// 度；无 hit（全 miss/error/observe）→ null——miss 不反推 TTL，不造数。
//
// 红线（queryapi.go 顶部块全文适用）：无消息内容、无凭据字段、纯只读；
// windowsMu/ledgerMu 各自独立短暂获取绝不嵌套，账本盘 I/O 锁外进行。

import (
	"net/http"
	"sort"

	"ferryman/internal/accounts"
	"ferryman/internal/beat"
	"ferryman/internal/mathx"
)

// handleBeats GET /beats：在飞等待窗/问询窗清单＋逐窗心跳遥测；无在飞窗返回
// 空数组。
func handleBeats(d *Daemon, w http.ResponseWriter, r *http.Request) {
	// 快照一：等待窗（windowsMu 下只读探测，一次抄齐）。
	type winSnap struct {
		agent, sid, kind string
		openedTS         float64
		parked           bool
		estClose         string
	}
	var snaps []winSnap
	d.windowsMu.Lock()
	for key, wrec := range d.windows {
		openedTS, open := d.waitWindowOpenLocked(key[0], key[1])
		if !open { // 泄漏过期/停车满 1h：懒过期闭账留给正规路径，本面不列不闭
			continue
		}
		parked := wrec.StopTS != nil
		est := "subagents_done"
		if parked {
			est = "main_resumed"
		}
		snaps = append(snaps, winSnap{agent: key[0], sid: key[1], kind: "wait",
			openedTS: openedTS, parked: parked, estClose: est})
	}
	d.windowsMu.Unlock()

	// 快照二：问询窗（台账锁内抄齐，锁外读盘——锁序纪律）。
	d.Ledger.Mu().Lock()
	for _, st := range d.Ledger.AllSessionsLocked() {
		if st.QWatchOpenedTS == nil {
			continue
		}
		snaps = append(snaps, winSnap{agent: st.Agent, sid: st.SessionID,
			kind: "qwatch", openedTS: *st.QWatchOpenedTS, parked: false,
			estClose: "write"})
	}
	d.Ledger.Mu().Unlock()

	// 遥测源：beat 科目流水一次只读遍历（锁外盘 I/O），内存逐窗聚合。
	var beats []map[string]any
	if d.Accounts != nil && len(snaps) > 0 {
		beats = d.Accounts.Read(accounts.ReadOpts{Kind: "beat"})
	}

	out := make([]map[string]any, 0, len(snaps)) // 空在飞 → [] 而非 null
	for _, s := range snaps {
		var fired, hit, miss, errMsg, observe int
		var cost float64
		ttlObserved, hasTTL := 0.0, false
		for _, b := range beats {
			lane, _ := b["lane"].(string)
			sid, _ := b["session_id"].(string)
			agent, _ := b["agent"].(string)
			if lane != s.kind || sid != s.sid || agent != s.agent {
				continue // 泳道/会话不对号不串窗
			}
			ts := acctNum(b, "ts")
			if ts < s.openedTS {
				continue // 窗开前的心跳属上一窗（同会话重开窗不虚计）
			}
			fired++
			switch b["outcome"] {
			case beat.OutHit:
				hit++
				if depth := ts - s.openedTS; !hasTTL || depth > ttlObserved {
					ttlObserved, hasTTL = depth, true // 最强 hit 证据
				}
			case beat.OutMiss:
				miss++
			case beat.OutError:
				errMsg++
			case beat.OutObserve:
				observe++
			}
			cost += acctNum(b, "cost_actual")
		}
		var ttl any // 无 hit → null（不可算，不造数）
		if hasTTL {
			ttl = mathx.Round(ttlObserved, 1)
		}
		out = append(out, map[string]any{
			"session_id":       s.sid,
			"agent":            s.agent,
			"kind":             s.kind,
			"opened_ts":        mathx.Round(s.openedTS, 3),
			"parked":           s.parked,
			"est_close_reason": s.estClose,
			"telemetry": map[string]any{
				"beats_fired":    fired,
				"hit":            hit,
				"miss":           miss,
				"error":          errMsg,
				"observe":        observe,
				"ttl_observed_s": ttl,
				"cost_actual":    mathx.Round(cost, 6),
			},
		})
	}
	// 稳定序（map 迭代序随机，须钉）：开窗时刻升序，并列按 kind、session_id
	// 字典序。
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a["opened_ts"] != b["opened_ts"] {
			return a["opened_ts"].(float64) < b["opened_ts"].(float64)
		}
		if a["kind"] != b["kind"] {
			return a["kind"].(string) < b["kind"].(string)
		}
		return a["session_id"].(string) < b["session_id"].(string)
	})
	writeJSON(w, http.StatusOK, map[string]any{"windows": out})
}
