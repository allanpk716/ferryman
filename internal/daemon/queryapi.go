package daemon

// queryapi.go — 票01：daemon 只读查询面骨架（规格 docs/superpowers/specs/
// 20260920-agent-surface-mcp-readonly-spec.md「daemon 只读端点」节，ADR-0009
// agent 面：一切读经 daemon）。
//
// 五个只读 GET 端点（/sessions、/session、/gate_check、/report、/beats）统一
// 注册于 queryEndpoints 分派表；鉴权（Bearer）与 127.0.0.1 绑定复用既有面
// （httpapi.doGet 在 auth 之后经 dispatchQuery 单块接线进来，ListenAndServe
// 不动——不新开监听、不复制鉴权）。
//
// 红线（本面全部 handler 共同遵守）：响应永不包含消息内容（台账标题/路径/
// 计数/金额可以，对话原文不行）、永不包含凭据字段；全部只读，无任何状态
// 写入（懒过期闭窗等带副作用的既有方法一律禁入，只读探测用 *Locked 无副
// 产品）；/stats 既有字段名逐字契约不动。
//
// 票间路径互斥：每端点一个独立 handler 文件。票01 落地 /sessions+/session
// （query_sessions.go），票02 落地 /gate_check（query_gate_check.go），票03
// 落地 /report+/beats（query_report.go / query_beats.go）——五端点全部为真实现，
// 票01 的 stubNotImplemented 已随最后一个 stub 的替换退役删除。

import (
	"net/http"
)

// queryEndpoint 查询面 handler 形状（端点注册表的值类型）。
type queryEndpoint func(d *Daemon, w http.ResponseWriter, r *http.Request)

// queryEndpoints 只读 GET 端点注册表（票01：两实现＋三 stub）。
var queryEndpoints = map[string]queryEndpoint{
	"/sessions":   handleSessions,
	"/session":    handleSessionDetail,
	"/gate_check": handleGateCheck,
	"/report":     handleReport, // 票03 实现（query_report.go）
	"/beats":      handleBeats,  // 票03 实现（query_beats.go）
}

// dispatchQuery 查询面分派入口（httpapi.doGet default 分支的单块接线点）：
// 路径命中注册表则回话（含 200/400/404/501 一切查询面回话）并返回 true；
// 未命中返回 false 由调用方走 notFound。DaemonLike 非 *Daemon（测试替身）时
// 视同未命中——查询面只在真 Daemon 上存在。
func dispatchQuery(dl DaemonLike, w http.ResponseWriter, r *http.Request) bool {
	h, ok := queryEndpoints[r.URL.Path]
	if !ok {
		return false
	}
	d, ok := dl.(*Daemon)
	if !ok {
		return false
	}
	h(d, w, r)
	return true
}
