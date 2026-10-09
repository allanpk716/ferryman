// tool_doctor.go — 票05：第六件只读工具 doctor——进程内复用 installer 的结构化
// 体检（D6：不经 HTTP、不经 daemon 转发；daemon 活性目标经 config 解析）。
//
// doctor 例外语义（规格「错误面」节 + D10）：daemon 不可达/超时 → 照常返回
// 结构化体检结果，daemon 活性项＝fail；不缓存（注入的检查函数每次调用真跑，
// 独立重算）、不伪造、不自举 daemon。顶层 version 字段同纪律（票07）：经既有
// /stats 通道现读守护自报版本，不可达时如实标注。
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
	"os"

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
		cfg = freshCfgOrFrozen(cfg)
		return installer.DoctorStructured(installer.HomeDir(), installer.RepoRoot(), cfg, "", true)
	}
}

// freshCfgOrFrozen MCP 冻结探针目标解冻（dsh-host-guard 晨报后续票04，
// 2026-10-09）：MCP 服务进程活整个会话，New 注入的 cfg 是会话启动时的冻结
// 快照——会话中途守护换端口/换数据目录后，冻结口打旧目标（假死/假活残留：
// 2026-10-09 实况＝/clear 长链会话的 doctor 恒报「daemon 未运行」）。每次
// 调用现载配置（config.Load 优先级原样：显式参数 > FERRYMAN_CONFIG > 默认
// 路径）；装载失败回落冻结快照——行为不差于修前。
func freshCfgOrFrozen(frozen *config.Config) *config.Config {
	// stdio 纪律（Run 装配同款，见 server.go）：config.Load 的校验警告经
	// fmt.Printf 写 os.Stdout，会把非 JSON 行混进 JSON-RPC 流——Load 期间
	// 把 stdout 临时换向 stderr（Serve 逐行串行，无并发争用）。
	saved := os.Stdout
	os.Stdout = os.Stderr
	fresh, err := config.Load("", false)
	os.Stdout = saved
	if err != nil {
		return frozen
	}
	return fresh
}

// daemon 版本如实标注（票07，A5②/D9）：不可达/HTTP 错误与「响应读不出
// version」分列——不伪造、不回退本进程版本。
const (
	daemonVersionUnreachable = "daemon 不可达（版本未知）"
	daemonVersionUnknown     = "daemon 版本未知（/stats 未自报或不可解析）"
)

// fetchDaemonVersion 守护自报版本取值（票07 真实装配）：经既有 daemon 通信面
// （DaemonClient.Get /stats——五件转发工具同一条只读 GET 通道）读守护版本。
// 每次 doctor 调用现连（不缓存，同本工具既有纪律）；任何失败如实标注（见上
// 两常量），绝不回退本进程版本——MCP exe 可能是旧版，报它会误导排障。
func fetchDaemonVersion(c *DaemonClient) string {
	body, err := c.Get("/stats", nil)
	if err != nil {
		return daemonVersionUnreachable
	}
	var stats struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &stats); err != nil || stats.Version == "" {
		return daemonVersionUnknown
	}
	return stats.Version
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
		// Version 版本号（票07 修订）：daemon（守护）自报版本——经既有 /stats
		// 通道现读（Server.daemonVersion 注入缝，每次调用独立重算），不再取本
		// 进程装配版本（MCP exe 可能是旧版，报它误导排障）；daemon 不可达时
		// 如实标注，不伪造。
		Version string                 `json:"version"`
		Checks  []installer.CheckResult `json:"checks"`
		Summary doctorSummary           `json:"summary"`
	}{Version: s.daemonVersionOrUnknown(), Checks: checks}
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

// daemonVersionOrUnknown 守护版本注入缝取值（nil 防线：未装配时如实标注——
// 不 panic、不回退本进程版本，票07 不伪造红线同缝）。
func (s *Server) daemonVersionOrUnknown() string {
	if s.daemonVersion == nil {
		return daemonVersionUnknown
	}
	return s.daemonVersion()
}
