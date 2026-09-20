package update

// 换装目标解析(seam E,规格 §C 第4条):解析 ~/ferryman/start-daemon.cmd
// 引号内的 exe 路径为换装目标;解析失败回落 os.Executable()。installer.
// EnsureLauncher 生成的形态(见其注释):
//
//	@echo off
//	rem Ferryman daemon launcher (...)
//	"<exe 绝对路径>" serve >> "<out.log>" 2>> "<err.log>"
//
// 判定规则:@/rem 开头行跳过;取第一个「引号内、以 .exe 结尾(不分大小写)、
// 盘上存在」的路径——同行日志重定向的引号与 set 语句的引号值都不以 .exe
// 结尾,天然不误中;存在性检查防 cmd 指向已挪窝的旧路径时悄悄换错目标。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ParseStartDaemonExe 解析点火脚本,返回引号内 exe 绝对路径;任何不满足
// (文件缺失/无合格引号路径/路径不可达)都报错,由调用方决定回落。
func ParseStartDaemonExe(cmdPath string) (string, error) {
	data, err := os.ReadFile(cmdPath)
	if err != nil {
		return "", fmt.Errorf("读点火脚本: %w", err)
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(line, "@") || strings.HasPrefix(lower, "rem ") || strings.HasPrefix(lower, "rem\t") {
			continue // @echo off / rem 注释行
		}
		// 行内逐个引号对找候选;不以 .exe 结尾的跳过(日志/set 引号)。
		for rest := line; ; {
			i := strings.IndexByte(rest, '"')
			if i < 0 {
				break
			}
			j := strings.IndexByte(rest[i+1:], '"')
			if j < 0 {
				break // 引号未闭合,本行无完整候选
			}
			cand := strings.TrimSpace(rest[i+1 : i+1+j])
			rest = rest[i+1+j+1:]
			if strings.HasSuffix(strings.ToLower(cand), ".exe") {
				if _, err := os.Stat(cand); err != nil {
					return "", fmt.Errorf("点火脚本 exe 路径不可达: %s", cand)
				}
				return cand, nil
			}
		}
	}
	return "", fmt.Errorf("点火脚本无可识别的引号 exe 路径: %s", cmdPath)
}

// ResolveSwapTarget seam E 全路径:点火脚本优先;任何失败回落本进程映像
// os.Executable()(监督者通常就是被换装的那个 exe 拉起的,回落即同位)。
func ResolveSwapTarget(cmdPath string) (string, error) {
	if p, err := ParseStartDaemonExe(cmdPath); err == nil {
		return p, nil
	}
	return os.Executable()
}

// samePath Windows 路径同义判定:Clean 规整 + 全串大小写不敏感比较
// (NTFS 大小写不敏感;Clean 在 Windows 统一反斜杠,正斜杠先归一)。
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(filepath.Clean(normSlash(a)), filepath.Clean(normSlash(b)))
}

// normSlash 正斜杠归一为反斜杠(比较用;QueryFullProcessImageName 与脚本里
// 手写的路径可能两种分割符混用)。
func normSlash(p string) string {
	return strings.ReplaceAll(p, "/", `\`)
}
