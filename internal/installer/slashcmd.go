// slashcmd.go — 票39（spec#34，ADR-0025 信号②）：用户级斜杠命令 /ferry-restore。
//
// CC 自定义命令形状（用户级 <home>/.claude/commands/ferry-restore.md）：
// YAML frontmatter（description/allowed-tools/argument-hint）+ `!` 反引号
// 动态命令行——CC 执行 bash 并把 stdout 带回会话，与终端跑 `ferryman restore`
// 同一条链路（守护 /restore?explicit=1，inject 记账与 mark_injected 照记）。
// 命令体嵌安装时的 exe 绝对路径（正斜杠形——bash 引号内可执行；settings.json
// 钩子与 MCP 注册同款纪律，exe 挪窝重跑 install-cc 即刷新）。
//
// 冲突语义（F6 同款单源纪律）：自有文件识别 = 文件内 ferryman:managed 标记
// （frontmatter YAML 注释行——CC 解析器忽略之，机器可寻）；自有 → 幂等刷新；
// 他人同名文件 → 不覆盖、响亮报告（install-cc 其余安装面照常），doctor 以
// 同一标记判定在位/冲突——绝不两套判据。

package installer

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// slashCommandName 斜杠命令名（/ferry-restore；spec#34：不与 CC 内置或
// 其他插件撞名——ferry- 前缀独占）。
const slashCommandName = "ferry-restore"

// slashCommandMarker 自有文件标记：frontmatter 首行 YAML 注释。install 的
// 覆盖判定与 doctor 的在位判定共用（单源）。
const slashCommandMarker = "ferryman:managed"

// CCSlashCommandPath 用户级斜杠命令文件路径（doctor 检查面）。
func CCSlashCommandPath(home string) string {
	return filepath.Join(home, ".claude", "commands", slashCommandName+".md")
}

// slashCommandPathForSettings InstallCC 的写入位：settings.json 同目录下
// commands/ferry-restore.md（settingsPath 注入时随行——测试密闭同形）。
func slashCommandPathForSettings(settingsPath string) string {
	return filepath.Join(filepath.Dir(settingsPath), "commands", slashCommandName+".md")
}

// slashCommandBody 命令文件内容（幂等——字节确定，重跑刷新即覆盖）。
// 命令体末位固定传 --session ${CLAUDE_SESSION_ID}（spec #34 评审 F5，US31：
// 显式取回登记真实会话 id，上一代交接判定 DropSuperseded 才能认出被接替的
// 会话）：CC 在命令文件内替换该变量＝登记真实会话；CC 若不替换，bash 把
// 未设变量展开为空词后消失＝干净回落守护侧合成（cli-restore-*，合成单源）。
// 末位放置（$ARGUMENTS 在前）：--session 空词消失后不吞用户旗标的值。
func slashCommandBody(exe string) string {
	p := filepath.ToSlash(exe) // bash 引号内的可执行形（反斜杠会被 bash 吃掉）
	return "---\n" +
		"# " + slashCommandMarker + " 由 ferryman install-cc 生成，重跑 install-cc 幂等刷新；勿手改\n" +
		"description: Ferryman 显式恢复——取回本目录最新交接（单份注入/多份列清单/无候选明说）\n" +
		"allowed-tools: Bash(ferryman restore:*), Bash(\"" + p + "\" restore:*)\n" +
		"argument-hint: [--cwd 目录]\n" +
		"---\n" +
		"\n" +
		"!`\"" + p + "\" restore $ARGUMENTS --session ${CLAUDE_SESSION_ID}`\n"
}

// ensureSlashCommand 写入/幂等刷新斜杠命令文件（票39）。冲突处置：既有文件
// 无自有标记 = 用户自定义 → 不覆盖、响亮报告（返回值只影响输出面，不挡
// InstallCC 其余安装）。读失败同样不动手（无证据不覆盖）。
func ensureSlashCommand(path, exe string) {
	if raw, err := os.ReadFile(path); err == nil {
		if !bytes.Contains(raw, []byte(slashCommandMarker)) {
			fmt.Printf("[!] %s 已存在且非 Ferryman 写入（同名冲突）——不覆盖；确认后改名或删除该文件再重跑 install-cc\n",
				path)
			return
		}
	} else if !os.IsNotExist(err) {
		fmt.Println(err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Println(err)
		return
	}
	if err := os.WriteFile(path, []byte(slashCommandBody(exe)), 0o644); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("已写入用户级斜杠命令 /%s（%s；命令体调本 exe 的 restore）\n", slashCommandName, path)
}

// CheckCCSlashCommand doctor 检查（票39）：斜杠命令在位性。三态：
//   - 缺位 → 提示不失败（spec#34「斜杠命令文件缺位同样提示」——与闸门事件
//     缺位同级处置：未跑新 install-cc 的存量用户不产红）；
//   - 自有标记在位 → pass；
//   - 他人同名文件 → 与缺位同级的提示（spec #34 评审 F7：install-cc 已响亮
//     报告不覆盖，doctor 再判 fail 只会把"用户自定义了同名命令"红成故障）
//     ——明说该文件非 Ferryman 写入、/ferry-restore 不是 Ferryman 的入口。
func CheckCCSlashCommand(path string) Check {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Check{true, "/" + slashCommandName + " 斜杠命令未装（跑 ferryman install-cc 补装" +
				"——提示不判失败，与闸门缺位同级）"}
		}
		return Check{false, fmt.Sprintf("%s 读取失败: %v", path, err)}
	}
	if !bytes.Contains(raw, []byte(slashCommandMarker)) {
		return Check{true, fmt.Sprintf("%s 存在但非 Ferryman 写入（同名冲突，install-cc 已跳过"+
			"覆盖——/ferry-restore 不是 Ferryman 的入口；确认后改名或删除该文件再重跑"+
			"install-cc。提示不判失败，与缺位同级）", path)}
	}
	return Check{true, "/" + slashCommandName + " 斜杠命令在位"}
}
