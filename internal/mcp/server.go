// Package mcp — agent 面 v1：MCP 只读查询面（票04；ADR-0009；规格
// docs/superpowers/specs/20260920-agent-surface-mcp-readonly-spec.md）。
//
// `ferryman mcp` 子命令：stdio JSON-RPC 2.0 的 MCP server——initialize、
// tools/list、tools/call 三方法可被标准 MCP 客户端发现与调用。实现载体＝最
// 小手写（票面裁定：不新增第三方依赖时优先最小手写）：
//   - 分帧：按行（newline-delimited JSON，一行一消息）；
//   - 工具错误：daemon 不可达/超时/非 2xx/参数错 → result.isError=true（MCP
//     isError 语义）；协议级错误（坏 JSON -32700、未知方法 -32601、未知工具
//     -32602）走 JSON-RPC error；
//   - 通知（无 id 的消息，如 notifications/initialized）静默接收不回话；
//   - 处理序：单 goroutine 顺序收发（stdio 请求串行到达；每次工具调用真连
//     daemon，无任何缓存层）。
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"slices"

	"ferryman/internal/config"
)

const (
	serverName    = "ferryman"
	baseProtocol  = "2024-11-05" // 客户端版本不受支持时的回落基线
	maxLineLen    = 16 << 20     // 单行请求上限（工具参数为小 JSON，16MiB 已远超需要）
	jsonRPCMarker = "2.0"
)

// supportedProtocolVersions 受支持协议版本（客户端请求命中即回显）。
var supportedProtocolVersions = []string{"2024-11-05", "2025-03-26", "2025-06-18"}

// serverVersion serverInfo.version：模块版本（go build 无版本信息时 "dev"）。
var serverVersion = func() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" &&
		bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}()

// JSON-RPC 2.0 错误码（规范值）。
const (
	codeParseError     = -32700
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// Server stdio MCP server：工具注册表＋daemon 转发客户端。
type Server struct {
	client *DaemonClient
	tools  []Tool
}

// New 以既有配置解析产物装配（cfg 来自 config.Load——鉴权信息即由此取得）。
func New(cfg *config.Config) *Server {
	return &Server{client: NewDaemonClient(cfg), tools: ferrymanTools()}
}

// Run `ferryman mcp` 子命令主体：configPath 显式参数 > FERRYMAN_CONFIG > 默认
// 路径（config.Load 优先级原样复用）；失败只写 stderr（stdout 专留给
// JSON-RPC——stdio MCP 纪律），返回进程退出码。
func Run(configPath string) int {
	// config.Load 的校验警告经 fmt.Printf 写 os.Stdout（config.Validate 两处）
	// ——stdio 纪律下 stdout 专留给 JSON-RPC，握手前的非 JSON 行可能被客户端
	// 当传输错误。Load 期间把 stdout 临时换向 stderr（此刻单线程，Serve 尚未
	// 开始，无并发争用）。
	saved := os.Stdout
	os.Stdout = os.Stderr
	cfg, err := config.Load(configPath, false)
	os.Stdout = saved
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := New(cfg).Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0 // stdin EOF = 客户端收线，正常退出
}

// Serve 按行收发循环：读一行请求→回一行响应（通知不回）；in 读到 EOF 返回
// nil。写失败（客户端收线）原样上抛由调用方定夺。
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineLen)
	w := bufio.NewWriter(out)
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		resp := s.handleLine(line)
		if resp == nil {
			continue // 通知：静默接收
		}
		b, err := json.Marshal(resp)
		if err != nil { // map/raw 构造不可达，护底线
			b, _ = json.Marshal(errResp(nil, codeInternalError,
				"internal error: "+err.Error()))
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			return err
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	return sc.Err()
}

// ---- JSON-RPC 消息形状 ----

// rpcRequest 请求/通知通用形；ID 用 RawMessage 原样回显（数字/字符串皆可），
// 缺失（nil）＝通知。
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// rpcError JSON-RPC error 对象。
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// rpcResponse 响应形：Result 预编为 RawMessage（omitempty 对空对象 `{}` 不误伤）。
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// okResp result 响应装配（result 预编字节）。
func okResp(id json.RawMessage, result any) *rpcResponse {
	b, err := json.Marshal(result)
	if err != nil {
		return errResp(id, codeInternalError, "internal error: "+err.Error())
	}
	return &rpcResponse{JSONRPC: jsonRPCMarker, ID: id, Result: b}
}

// errResp 协议级错误响应（parse error 传 nil id——规范形）。
func errResp(id json.RawMessage, code int, msg string) *rpcResponse {
	return &rpcResponse{JSONRPC: jsonRPCMarker, ID: id,
		Error: &rpcError{Code: code, Message: msg}}
}

// toolError 工具执行错误：MCP isError 语义（result 面，非协议 error）。
func toolError(id json.RawMessage, msg string) *rpcResponse {
	return okResp(id, map[string]any{
		"content": []map[string]any{{"type": "text", "text": msg}},
		"isError": true,
	})
}

// ---- 方法分派 ----

// handleLine 单行消息处理；nil 返回＝通知不回话。
func (s *Server) handleLine(line []byte) *rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return errResp(json.RawMessage("null"), codeParseError,
			"parse error: "+err.Error())
	}
	if len(req.ID) == 0 { // 无 id ＝通知（notifications/* 等）：静默接收
		return nil
	}
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "ping":
		return okResp(req.ID, json.RawMessage("{}"))
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(req)
	default:
		return errResp(req.ID, codeMethodNotFound,
			fmt.Sprintf("method not found: %s", req.Method))
	}
}

// handleInitialize initialize：协议版本（客户端版本受支持即回显，否则回落
// 基线）＋capabilities.tools＋serverInfo。
func (s *Server) handleInitialize(req rpcRequest) *rpcResponse {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(req.Params, &p) // params 可空/可缺
	ver := p.ProtocolVersion
	if !slices.Contains(supportedProtocolVersions, ver) {
		ver = baseProtocol
	}
	return okResp(req.ID, map[string]any{
		"protocolVersion": ver,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": serverName, "version": serverVersion},
	})
}

// handleToolsList tools/list：五件工具的 name/description/inputSchema。
func (s *Server) handleToolsList(req rpcRequest) *rpcResponse {
	tools := make([]map[string]any, 0, len(s.tools))
	for _, t := range s.tools {
		tools = append(tools, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.InputSchema,
		})
	}
	return okResp(req.ID, map[string]any{"tools": tools})
}

// handleToolsCall tools/call：参数归一（透传）→ daemon 只读 GET → 响应
// JSON 原样进 content[0].text。未知工具＝协议级 -32602；参数/daemon 面
// 错误＝result.isError=true。
func (s *Server) handleToolsCall(req rpcRequest) *rpcResponse {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errResp(req.ID, codeInvalidParams, "invalid params: "+err.Error())
	}
	var tool *Tool
	for i := range s.tools {
		if s.tools[i].Name == p.Name {
			tool = &s.tools[i]
			break
		}
	}
	if tool == nil {
		return errResp(req.ID, codeInvalidParams,
			fmt.Sprintf("unknown tool: %s（可用：%v）", p.Name, s.toolNames()))
	}
	q, err := tool.buildQuery(p.Arguments) // arguments 缺省＝无参调用
	if err != nil {
		return toolError(req.ID, err.Error())
	}
	body, err := s.client.Get(tool.Endpoint, q)
	if err != nil {
		return toolError(req.ID, err.Error())
	}
	return okResp(req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(body)}},
		"isError": false,
	})
}

func (s *Server) toolNames() []string {
	out := make([]string, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, t.Name)
	}
	return out
}
