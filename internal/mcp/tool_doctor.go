// tool_doctor.go — 票05：第六件只读工具 doctor——进程内复用 installer 的结构化
// 体检（D6：不经 HTTP、不经 daemon 转发；daemon 活性目标经 config 解析）。
//
// doctor 例外语义（规格「错误面」节 + D10）：daemon 不可达/超时 → 照常返回
// 结构化体检结果，daemon 活性项＝fail；不缓存（注入的检查函数每次调用真跑，
// 独立重算）、不伪造、不自举 daemon。
//
// 检查函数经 Server.doctor 注入（New 装配真实面：HOME/exe 目标 + config 解析
// 的 daemon 活性目标 + 常驻保障两查真探测；测试替换为临时目标——绝不读真实
// 用户目录）。
//
// 红线同 tools.go：响应不含消息内容/凭据/token——逐项 Detail 是路径/状态/
// 修法文案（CLI doctor 人面同款输出，属运维元数据），无对话原文、无密物。

package mcp

import (
	"encoding/json"
	"fmt"

	"ferryman/internal/config"
	"ferryman/internal/installer"
)

// defaultDoctorFunc doctor 工具的真实装配（New 注入）：与 CLI RunDoctor 同缝
// （installer.HomeDir/RepoRoot）＋ cfg 的 daemon 活性目标；providers 与 CLI
// doctor 同位走默认路径（FERRYMAN_CONFIG 重定向时 providers 仍读默认路径——
// 既有 CLI 行为，不另造第二套优先级）；常驻保障两查真探测（只读注册表 /
// schtasks /Query，无写副作用）。
func defaultDoctorFunc(cfg *config.Config) func() []installer.CheckResult {
	return func() []installer.CheckResult {
		return installer.DoctorStructured(installer.HomeDir(), installer.RepoRoot(), cfg, "", true)
	}
}

// doctorSummary 体检汇总（从逐项结论推导的计数，非第二事实源）。
type doctorSummary struct {
	Total      int `json:"total"`
	Pass       int `json:"pass"`
	Fail       int `json:"fail"`
	NotChecked int `json:"not_checked"`
}

// handleInProcessTool 进程内工具分派（注册表 Endpoint 为空的项；当前仅
// doctor）。检查结果原样序列化进 content[0].text，isError=false——daemon
// 不可达也走这条路（活性项已按 fail 记入结果），绝不转成工具错误。
func (s *Server) handleInProcessTool(id json.RawMessage, tool *Tool, args map[string]any) *rpcResponse {
	if tool.Name != "doctor" {
		return toolError(id, fmt.Sprintf("工具 %s 未实现进程内处理", tool.Name))
	}
	if len(args) > 0 {
		return toolError(id, fmt.Sprintf("未知参数（doctor 无参数）: %v", argKeys(args)))
	}
	if s.doctor == nil {
		return toolError(id, "doctor 检查未装配（内部错误）")
	}
	checks := s.doctor()
	if checks == nil {
		checks = []installer.CheckResult{}
	}
	resp := struct {
		Checks  []installer.CheckResult `json:"checks"`
		Summary doctorSummary           `json:"summary"`
	}{Checks: checks}
	for _, c := range checks {
		resp.Summary.Total++
		switch c.Status {
		case installer.StatusPass:
			resp.Summary.Pass++
		case installer.StatusFail:
			resp.Summary.Fail++
		default:
			resp.Summary.NotChecked++
		}
	}
	b, err := json.Marshal(resp)
	if err != nil { // CheckResult 全为字符串字段，序列化不可达——护底线
		return toolError(id, "doctor 结果序列化失败: "+err.Error())
	}
	return okResp(id, map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(b)}},
		"isError": false,
	})
}

// argKeys 参数名清单（报错文案用，顺序不定无妨）。
func argKeys(args map[string]any) []string {
	out := make([]string, 0, len(args))
	for k := range args {
		out = append(out, k)
	}
	return out
}
