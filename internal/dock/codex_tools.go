// codex_tools.go — 票03：codex 工具上下文（对照表 §1.3 / §2.1）。
//
// 参考实现 transform_codex_chat.rs 的 CodexToolContext：请求体 tools 数组 +
// input 项内声明的工具（tool_search_output / additional_tools 载体，Codex
// 0.154+ 私有形状）都收进同一注册表；function/custom/tool_search/namespace
// 四形折平成稳定上游名（namespace__工具名，>64 字符截断+短哈希后缀），请求
// 向按注册表换上游名，响应向按注册表还原 namespace/custom/tool_search 形状。
package dock

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const (
	// toolSearchProxyName tool_search 代理工具的固定上游名。
	toolSearchProxyName = "tool_search"
	// chatToolNameMaxLen 上游工具名长度上限（超限截断+短哈希后缀）。
	chatToolNameMaxLen = 64
	// customToolInputField 自定义工具代理 schema 的单参字段名。
	customToolInputField = "input"
	// customToolPreservedHeading 自定义工具 description 内嵌原始定义的标题行。
	customToolPreservedHeading = "Original tool definition:"
	// toolResultErrorMarker 工具结果错误标记（逐字节沿用参考实现：codex 历史
	// 里已存在该标记，换字面量＝is_error 识别断裂）。
	toolResultErrorMarker = "[cc-switch:tool-result-error]"
)

// codexToolKind 注册表内的工具形态。
type codexToolKind int

const (
	codexToolFunction codexToolKind = iota
	codexToolCustom
	codexToolToolSearch
	codexToolNamespace
)

// codexToolSpec 注册表条目：形态 + 原名 + 命名空间。
type codexToolSpec struct {
	kind      codexToolKind
	name      string
	namespace string // namespace 形非空
}

// toolContext 工具注册表（请求向换名/响应向还原的双向单源）。零值可用。
type toolContext struct {
	chatTools        []map[string]any         // 有序（去重后到达序）
	seen             map[string]bool          // 上游名去重
	specByName       map[string]codexToolSpec // 上游名 → 形态
	nsPairToChatName map[string]string        // "nsname" → 上游名（\x00 分隔符防撞）
}

func newToolContext() *toolContext {
	return &toolContext{
		seen:             map[string]bool{},
		specByName:       map[string]codexToolSpec{},
		nsPairToChatName: map[string]string{},
	}
}

// buildToolContext 从 responses 请求体构建工具注册表（tools 数组 + input 内
// 声明的工具）。
func buildToolContext(body map[string]any) *toolContext {
	tc := newToolContext()
	if tools, ok := asSlice(body["tools"]); ok {
		for _, t := range tools {
			tc.addResponseTool(t)
		}
	}
	if input, present := body["input"]; present {
		collectInputDeclaredTools(input, tc)
	}
	return tc
}

// chatNameForFunction responses 侧 function 名（可选 namespace）→ 上游名。
func (tc *toolContext) chatNameForFunction(name, namespace string) string {
	if namespace != "" {
		if cn, ok := tc.nsPairToChatName[namespace+"\x00"+name]; ok {
			return cn
		}
		return flattenNamespaceToolName(namespace, name)
	}
	return name
}

// isCustomTool 上游名是否自定义工具（流式增量不发、收口换事件名）。
func (tc *toolContext) isCustomTool(chatName string) bool {
	spec, ok := tc.specByName[chatName]
	return ok && spec.kind == codexToolCustom
}

// add 由上游名登记（空名/重名跳过）。
func (tc *toolContext) add(chatName string, spec codexToolSpec, chatTool map[string]any) {
	if chatName == "" || tc.seen[chatName] {
		return
	}
	tc.seen[chatName] = true
	if spec.namespace != "" {
		tc.nsPairToChatName[spec.namespace+"\x00"+spec.name] = chatName
	}
	tc.specByName[chatName] = spec
	tc.chatTools = append(tc.chatTools, chatTool)
}

func (tc *toolContext) addFunctionTool(tool map[string]any, namespace string) {
	name, ok := responsesToolName(tool)
	if !ok {
		return
	}
	chatName := name
	if namespace != "" {
		chatName = flattenNamespaceToolName(namespace, name)
	}
	chatTool, ok := responsesFunctionToolToChatTool(tool, chatName)
	if !ok {
		return
	}
	kind := codexToolFunction
	if namespace != "" {
		kind = codexToolNamespace
	}
	tc.add(chatName, codexToolSpec{kind: kind, name: name, namespace: namespace}, chatTool)
}

func (tc *toolContext) addCustomTool(tool map[string]any) {
	name, ok := responsesToolName(tool)
	if !ok {
		return
	}
	chatTool := map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        name,
			"description": responsesCustomToolDescription(tool),
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					customToolInputField: map[string]any{
						"type": "string",
						"description": "Raw string input for the original custom " +
							"tool. Preserve formatting exactly and follow the " +
							"original tool definition embedded in the description.",
					},
				},
				"required": []any{customToolInputField},
			},
		},
	}
	tc.add(name, codexToolSpec{kind: codexToolCustom, name: name}, chatTool)
}

func (tc *toolContext) addToolSearchTool() {
	chatTool := map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": toolSearchProxyName,
			"description": "Search and load Codex tools, plugins, connectors, " +
				"and MCP namespaces for the current task.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search query for tools or connectors to load.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of tool groups to return.",
					},
				},
				"required": []any{"query"},
			},
		},
	}
	tc.add(toolSearchProxyName, codexToolSpec{kind: codexToolToolSearch, name: toolSearchProxyName}, chatTool)
}

func (tc *toolContext) addNamespaceTool(tool map[string]any) {
	namespace := mapStr(tool, "name")
	if namespace == "" {
		return
	}
	children, _ := asSlice(tool["tools"])
	if children == nil {
		children, _ = asSlice(tool["children"]) // 同义载体
	}
	for _, child := range children {
		cm, ok := asMap(child)
		if !ok || mapStr(cm, "type") != "function" {
			continue
		}
		tc.addFunctionTool(cm, namespace)
	}
}

func (tc *toolContext) addResponseTool(tool any) {
	switch t := tool.(type) {
	case string:
		// 裸字符串工具名＝自定义工具（对照表 §1.3）。
		tc.addCustomTool(map[string]any{"type": "custom", "name": t})
	case map[string]any:
		switch mapStr(t, "type") {
		case "function":
			tc.addFunctionTool(t, "")
		case "custom":
			tc.addCustomTool(t)
		case "tool_search":
			tc.addToolSearchTool()
		case "namespace":
			tc.addNamespaceTool(t)
		}
	}
}

// collectInputDeclaredTools input 树内递归收集 tool_search_output /
// additional_tools 载体携带的工具声明。
func collectInputDeclaredTools(v any, tc *toolContext) {
	switch cur := v.(type) {
	case []any:
		for _, item := range cur {
			collectInputDeclaredTools(item, tc)
		}
	case map[string]any:
		if t := mapStr(cur, "type"); t == "tool_search_output" || t == "additional_tools" {
			if tools, ok := asSlice(cur["tools"]); ok {
				for _, tool := range tools {
					tc.addResponseTool(tool)
				}
			}
		}
		for _, val := range cur {
			collectInputDeclaredTools(val, tc)
		}
	}
}

// flattenNamespaceToolName 命名空间折平名（ns__name）；超 64 字符截断前缀+
// "__"+8 字节短哈希（确定性、可复现）。
func flattenNamespaceToolName(namespace, name string) string {
	full := namespace + "__" + name
	if len(full) <= chatToolNameMaxLen {
		return full
	}
	sum := sha256.Sum256([]byte(full))
	suffix := "__" + hex.EncodeToString(sum[:4])
	prefixLen := chatToolNameMaxLen - len(suffix)
	if prefixLen < 0 {
		prefixLen = 0
	}
	if prefixLen > len(full) {
		prefixLen = len(full)
	}
	return full[:prefixLen] + suffix
}

// responsesToolName 工具名提取（function.name 优先，次顶层 name；trim 后
// 非空才算）。
func responsesToolName(tool map[string]any) (string, bool) {
	if fn, ok := asMap(tool["function"]); ok {
		if n, ok := asStr(fn["name"]); ok {
			n = strings.TrimSpace(n)
			if n != "" {
				return n, true
			}
			return "", false
		}
	}
	if n, ok := asStr(tool["name"]); ok {
		n = strings.TrimSpace(n)
		if n != "" {
			return n, true
		}
	}
	return "", false
}

// responsesCustomToolDescription 自定义工具 description：内嵌原始定义
// （规范化 JSON，跨请求稳定）——chat 化上游只见 function 形，原文进描述。
func responsesCustomToolDescription(tool map[string]any) string {
	return customToolPreservedHeading + "\n```json\n" + canonicalJSONString(tool) + "\n```"
}

// normalizeFunctionParameters parameters 归一：缺失/非 object → 空 object
// schema；type 非 "object" 强制 object（严格上游要求）。
func normalizeFunctionParameters(params any) map[string]any {
	obj, ok := asMap(params)
	if !ok {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	out := make(map[string]any, len(obj))
	for k, v := range obj {
		out[k] = v
	}
	if t, _ := out["type"].(string); t != "object" {
		out["type"] = "object"
	}
	return out
}

// responsesFunctionToolToChatTool function 工具 → chat 形 {type:function,
// function:{name,description?,parameters,strict?}}。
func responsesFunctionToolToChatTool(tool map[string]any, chatName string) (map[string]any, bool) {
	if mapStr(tool, "type") != "function" {
		return nil, false
	}
	if fn, ok := asMap(tool["function"]); ok {
		out := make(map[string]any, len(fn))
		for k, v := range fn {
			out[k] = v
		}
		out["parameters"] = normalizeFunctionParameters(out["parameters"])
		out["name"] = chatName
		if strict, present := tool["strict"]; present {
			if _, has := out["strict"]; !has {
				out["strict"] = strict
			}
		}
		return map[string]any{"type": "function", "function": out}, true
	}
	fn := map[string]any{"name": chatName}
	if d, present := tool["description"]; present && d != nil {
		fn["description"] = d
	}
	fn["parameters"] = normalizeFunctionParameters(tool["parameters"])
	if strict, present := tool["strict"]; present {
		fn["strict"] = strict
	}
	return map[string]any{"type": "function", "function": fn}, true
}

// ---- 响应向：上游名 → responses 工具调用项形状（§2.1 / §2.2） ----

// responseToolCallItemID 项 id 前缀：自定义工具 ctc_，其余 fc_。
func (tc *toolContext) responseToolCallItemID(callID, chatName string) string {
	if tc.isCustomTool(chatName) {
		return "ctc_" + callID
	}
	return "fc_" + callID
}

// responseToolCallItem 按注册表还原 responses 工具调用项：
//   - tool_search → tool_search_call（arguments 对象形）；
//   - custom → custom_tool_call（input 从代理 schema 单参还原回字符串）；
//   - namespace → function_call + namespace 字段；
//   - 未登记名 → function_call 原名直传。
func (tc *toolContext) responseToolCallItem(itemID, status, callID, chatName, arguments string) map[string]any {
	spec, ok := tc.specByName[chatName]
	if !ok {
		return responseFunctionCallItem(itemID, status, callID, chatName, arguments)
	}
	switch spec.kind {
	case codexToolToolSearch:
		return map[string]any{
			"type":      "tool_search_call",
			"call_id":   callID,
			"status":    status,
			"execution": "client",
			"arguments": parseToolArgumentsObject(arguments),
		}
	case codexToolCustom:
		return map[string]any{
			"id":      itemID,
			"type":    "custom_tool_call",
			"status":  status,
			"call_id": callID,
			"name":    spec.name,
			"input":   customToolInputFromArguments(arguments),
		}
	case codexToolNamespace:
		item := responseFunctionCallItem(itemID, status, callID, spec.name, arguments)
		item["namespace"] = spec.namespace
		return item
	default:
		return responseFunctionCallItem(itemID, status, callID, spec.name, arguments)
	}
}

// responseFunctionCallItem 标准 function_call 项。
func responseFunctionCallItem(itemID, status, callID, name, arguments string) map[string]any {
	return map[string]any{
		"id":        itemID,
		"type":      "function_call",
		"status":    status,
		"call_id":   callID,
		"name":      name,
		"arguments": arguments,
	}
}

// parseToolArgumentsObject tool_search arguments 解析：空 → {}；对象形原样；
// 其余回退 {"query": 原文}。
func parseToolArgumentsObject(arguments string) map[string]any {
	if strings.TrimSpace(arguments) == "" {
		return map[string]any{}
	}
	v, ok := decodeJSONValue([]byte(arguments))
	if !ok {
		return map[string]any{"query": arguments}
	}
	if obj, isObj := asMap(v); isObj {
		return obj
	}
	return map[string]any{"query": arguments}
}

// customToolInputFromArguments 代理 schema 单参 input 还原回原始字符串。
func customToolInputFromArguments(arguments string) string {
	if strings.TrimSpace(arguments) == "" {
		return ""
	}
	v, ok := decodeJSONValue([]byte(arguments))
	if !ok {
		return arguments
	}
	obj, isObj := asMap(v)
	if !isObj {
		return arguments
	}
	if s, ok := asStr(obj[customToolInputField]); ok {
		return s
	}
	return arguments
}
