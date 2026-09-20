// mcpinstall.go — 票06：ferryman install-mcp——把 agent 面 MCP server 注册进
// CC 用户级 MCP 配置（`claude mcp add --scope user` 等效：写 <home>/.claude.json
// 顶层 mcpServers 的 ferryman 条目，command 指向当前 ferryman 可执行、
// args=["mcp"]）。
//
// F6 冲突语义（spec「install-mcp」节已烘焙，逐条实现）：
//
//	① 自有条目识别＝形状匹配（command 指向 ferryman 可执行且 args 含 "mcp"）
//	   → 幂等覆盖（重复执行结果一致，exe 挪窝后重跑即刷新）；
//	② 外部或不兼容条目 → 默认拒绝并说明原因（配置原样不动——连备份都不留，
//	   拒绝路径零痕迹），仅显式 --force 才覆盖；
//	③ 回显白名单＝command/args 形状摘要；env/headers/其余键一律掩码为
//	   「<已隐藏 N 键>」——凭据值绝不输出（含成功路径——成功回显只含新条目
//	   自身形状）；
//	④ --force 覆盖前输出被替换条目的脱敏摘要。
//
// 读写目标全部经参数注入（configPath/exe 空串 = 真实缺省 homeDir()/exePath()）
// ——测试注入临时目录，绝不读写真实 ~/.claude.json。风格对齐既有安装命令
// （install.go：备份先例、坏 JSON 响亮拒绝、dumpJSON 落盘）。
package installer

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// mcpServerKey 用户级 mcpServers 下的注册键名（serverInfo.name 同名 mcp.serverName）。
const mcpServerKey = "ferryman"

// UserMCPConfigPath CC 用户级 MCP 配置路径：<home>/.claude.json 顶层
// mcpServers 键（`claude mcp add --scope user` 的写入位）。读取与写入都走
// 可注入的 home（测试传临时 HOME，不自读真实 HOME）。
func UserMCPConfigPath(home string) string {
	return filepath.Join(home, ".claude.json")
}

// mcpEntry 欲写入的条目形（对齐 claude CLI 用户级注册产物：type/command/args/env）。
func mcpEntry(exe string) map[string]any {
	return map[string]any{
		"type":    "stdio",
		"command": exe,
		"args":    []any{"mcp"},
		"env":     map[string]any{},
	}
}

// classifyMCPEntry F6 ①② 单源判定：own=true＝能识别为 Ferryman 自建且形态
// 兼容（command 基名为 ferryman 可执行、args 含 "mcp"）→ 幂等覆盖；否则
// reason 说明外部/不兼容的具体原因（默认拒绝的文案依据）。doctor"MCP 注册
// 在位"检查复用同一判定（单源——绝不出现两套判据）。
func classifyMCPEntry(entry map[string]any) (own bool, reason string) {
	cmd, _ := entry["command"].(string)
	if cmd == "" {
		return false, "条目无 command（形态不兼容）"
	}
	// 基名判定兼容两种分隔形（JSON 里反斜杠转义/正斜杠都可能出现）。
	base := path.Base(strings.ReplaceAll(cmd, "\\", "/"))
	if base != "ferryman" && base != "ferryman.exe" {
		// R1：reason 不输出 command 原值（旗标/查询串可搭载凭据）——只给净化基名。
		return false, fmt.Sprintf("command <%s> 非 ferryman 可执行（外部条目）",
			sanitizedBase(base))
	}
	for _, a := range asList(entry["args"]) {
		if s, ok := a.(string); ok && s == "mcp" {
			return true, ""
		}
	}
	return false, "args 不含 \"mcp\"（形态不兼容）"
}

// sanitizedBase 基名净化：剥首个 '?' 或空白起的旗标/查询串段——该段可搭载
// 凭据（评审反例：ferryman.exe?token=sk-…），任何回显路径只输出净化基名。
func sanitizedBase(base string) string {
	if i := strings.IndexAny(base, "? \t"); i >= 0 {
		return base[:i]
	}
	return base
}

// maskedMCPSummary F6 ③④ 条目脱敏摘要：形状级——command 只出净化基名，args
// 只出元素计数（元素值/嵌套对象一律不输出——R1：值级回显可漏 --token 类凭据）；
// 其余键（env/headers/type/…）一律掩码为「<已隐藏 N 键>」。任何值都不出。
func maskedMCPSummary(entry map[string]any) string {
	var parts []string
	if cmd, ok := entry["command"].(string); ok && cmd != "" {
		parts = append(parts, fmt.Sprintf("command=<%s>",
			sanitizedBase(path.Base(strings.ReplaceAll(cmd, "\\", "/")))))
	}
	if args := asList(entry["args"]); len(args) > 0 {
		parts = append(parts, fmt.Sprintf("args=<%d 元素>", len(args)))
	}
	hidden := 0
	for k := range entry {
		if k != "command" && k != "args" {
			hidden++
		}
	}
	if hidden > 0 {
		parts = append(parts, fmt.Sprintf("<已隐藏 %d 键>", hidden))
	}
	if len(parts) == 0 {
		return "（空条目）"
	}
	return strings.Join(parts, " ")
}

// InstallMCP `ferryman install-mcp` 主体：注册/幂等覆盖用户级 mcpServers.ferryman
// （F6 四件见文件头）。configPath/exe 空串 = 真实缺省（用户 HOME / 当前 exe）。
// 返回进程退出码（拒绝＝1——配置原样不动）。
func InstallMCP(configPath, exe string, force bool) int {
	if configPath == "" {
		configPath = UserMCPConfigPath(homeDir())
	}
	if exe == "" {
		exe = exePath()
	}

	// 读既有配置（缺文件＝空配置首装；坏 JSON 响亮拒绝——静默覆盖 = 数据丢失）。
	fileExisted := false
	var data map[string]any
	if rawData, err := os.ReadFile(configPath); err == nil {
		fileExisted = true
		if err := json.Unmarshal(rawData, &data); err != nil {
			fmt.Printf("%s 解析失败: %v（拒绝改写）\n", configPath, err)
			return 1
		}
	} else if !os.IsNotExist(err) {
		fmt.Println(err)
		return 1
	} else {
		data = map[string]any{}
	}

	// mcpServers 取形（缺键建空表；既有值非对象 → 响亮拒绝，force 也不许）。
	servers, ok := data["mcpServers"].(map[string]any)
	if data["mcpServers"] != nil && !ok {
		fmt.Println(".claude.json 的 mcpServers 不是对象，拒绝改写")
		return 1
	}
	if servers == nil {
		servers = map[string]any{}
		data["mcpServers"] = servers
	}

	// F6 冲突判定（既有 ferryman 键才走；其余键一概不惊动）。null 限值视同
	// 占位（R1：与 doctor 的非对象判定对齐——默认拒绝，仅 --force 覆盖）。
	if existing, exists := servers[mcpServerKey]; exists {
		if entry, isObj := existing.(map[string]any); isObj {
			if own, reason := classifyMCPEntry(entry); !own {
				// ② 外部/不兼容：默认拒绝＋原因＋脱敏摘要；仅 --force 覆盖
				// （④ 覆盖前打印被替换条目的脱敏摘要）。拒绝路径零写入。
				if !force {
					fmt.Printf("[拒绝] mcpServers.ferryman 已有条目与 Ferryman 不兼容: %s\n", reason)
					fmt.Printf("  既有条目（脱敏摘要）: %s\n", maskedMCPSummary(entry))
					fmt.Println("  默认不覆盖（配置原样不动）；确认替换请加 --force")
					return 1
				}
				fmt.Printf("[force] 将覆盖 mcpServers.ferryman 既有条目，被替换条目（脱敏摘要）: %s\n",
					maskedMCPSummary(entry))
			}
			// ① 自有条目：幂等覆盖（无前置回显要求；成功回显只含新条目形状）。
		} else {
			// 非对象条目（连形状都没有）：外部/不兼容同路径。
			if !force {
				fmt.Println("[拒绝] mcpServers.ferryman 既有限值不是对象（无法识别为 Ferryman 自建）")
				fmt.Println("  默认不覆盖（配置原样不动）；确认替换请加 --force")
				return 1
			}
			fmt.Println("[force] 将覆盖 mcpServers.ferryman 既有限值（非对象，无形状可摘）")
		}
	}

	// 落盘：既有文件先备份（install-cc 先例），写目标条目（dumpJSON 幂等——
	// Go JSON 键序确定，重复执行字节一致）。
	if fileExisted {
		backup := filepath.Join(filepath.Dir(configPath),
			filepath.Base(configPath)+".bak-ferryman-"+stamp())
		if err := backupFile(configPath, backup); err != nil {
			fmt.Println(err)
			return 1
		}
	}
	servers[mcpServerKey] = mcpEntry(exe)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		fmt.Println(err)
		return 1
	}
	if err := os.WriteFile(configPath, dumpJSON(data), 0o644); err != nil {
		fmt.Println(err)
		return 1
	}
	// ③ 成功回显：白名单形状（command/args）＋注册位置；无其他键值。
	fmt.Printf("已注册 ferryman MCP server 到 %s（用户级 mcpServers.ferryman: "+
		"type=stdio command=%q args=[mcp]）\n", configPath, exe)
	fmt.Println("CC 等 MCP 客户端重载配置后生效；doctor 可查注册在位")
	return 0
}
