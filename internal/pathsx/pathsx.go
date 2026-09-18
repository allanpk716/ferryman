// Package pathsx 提供路径键形归一，供 lineage 唯一键等场景使用。
package pathsx

import "strings"

// NormPath 把路径归一成 lineage 唯一键形：反斜杠→正斜杠 + 小写。
// Python 参照实现：str(PureWindowsPath(p)).replace("\\", "/").lower()。
func NormPath(p string) string {
	return strings.ToLower(strings.ReplaceAll(p, "\\", "/"))
}
