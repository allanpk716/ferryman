package daemon

// dsh_dedup.go — 票05 跨源 usage 去重（链内修订自票06 改钉本票）：策略 (a)
// 事件接管单源化。事件口（/dsh/event）与 pollDsh 文件守望面对同一
// assistant/message 各记一行＝账面双计（report 按 lineage/session 聚合同组
// 翻倍）——通流量前必须消除。
//
// 裁定理由（为何 (a) 不是 (b),实施侧留痕）：
//   - (b) 记账侧去重键要把 (agent, sid, seq/时间窗+四列签名) 写进账本白名单
//     （新键）或靠时间窗启发式（同窗同签名误合——真实风险:连续两条同形
//     usage 并非罕见）;且重启后去重表回种仍需从行内可辨识双源与序号。
//   - (a) 以「该会话有无插件事件流量」为单源切换开关：事件行 lineage_id 恒空
//     （dsh_receive.go 注记——无文件事件没有代文件偏移/谱系）、文件行 lineage
//     恒为真代文件路径,双源在账本内天然可辨识,零新键、零启发式、账本即唯一
//     状态（newDshHarvest 断点回种同纪律）。
//   - 单调接管语义：插件在位＝同宿主同进程面,该会话事件全量到,文件面让位;
//     插件卸载后新会话自然回文件面;旧接管会话延续单源（少记不重记——对账侧
//     而言漏记可补、重记污染聚合,宁缺毋错）。
//   - 残余竞窗（如实留痕,不装不存在的不变量）：接管首条事件（turn/start,无
//     usage）文件落盘→HTTP 标记落账之间若恰有轮询完成 fed 查询并读尾,理论上
//     可双记一条带 usage 的历史行;窗宽毫秒级且需轮询恰落其中,rc 期接受。
//     注意不可经 /dsh/gate 标记收窄——CC 桥（票01 hooks.json）同问 /dsh/gate
//     而桥不报事件,gate 侧标记会把纯桥会话错剔出文件面（usage 全丢）。
//
// 归位：Daemon 持表（d.dshFed,NewDaemon 自账本回种）;DshEvent 标记;
// pollDshSession 查询让位（经 w.Daemon——serve.go:282 生产装配本就同传 d）。

import (
	"sync"

	"ferryman/internal/accounts"
)

// dshEventFed 事件接管表：session id → 是否已有插件事件流量（单源切换开关）。
// 叶子锁（RWMutex）,临界区只有 map 读写,绝不嵌其他锁（包锁序纪律）。
// nil 接收者安全（直接字面量构造 Daemon/Watcher 的旧测试形态零行为）。
type dshEventFed struct {
	mu  sync.RWMutex
	fed map[string]bool
}

// mark 标记会话已被事件面接管；空键跳过。
func (s *dshEventFed) mark(sid string) {
	if s == nil || sid == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fed == nil {
		s.fed = map[string]bool{}
	}
	s.fed[sid] = true
}

// has 查询接管（nil 接收者＝未接管——fail-safe 到文件面）。
func (s *dshEventFed) has(sid string) bool {
	if s == nil || sid == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fed[sid]
}

// seedFromAccounts 账本回种（daemon 重启后接管表的持久恢复）：dsh usage 行中
// lineage_id 为空者＝事件行——其 session_id 与 subagent（子会话直报随父入账,
// 子键记在 subagent 列）都标记。newDshHarvest 断点回种同款的一次全量读。
func (s *dshEventFed) seedFromAccounts(acc *accounts.Accounts) {
	if s == nil || acc == nil {
		return
	}
	for _, e := range acc.Read(accounts.ReadOpts{Kind: "usage"}) {
		if ag, _ := e["agent"].(string); ag != "dsh" {
			continue
		}
		if lineage, _ := e["lineage_id"].(string); lineage != "" {
			continue // 文件面行（真代文件路径）——不是事件流量
		}
		if sid, _ := e["session_id"].(string); sid != "" {
			s.mark(sid)
		}
		if sub, _ := e["subagent"].(string); sub != "" {
			s.mark(sub)
		}
	}
}
