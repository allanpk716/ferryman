// Package mathx 提供与 CPython 语义对齐的数值与文本量词工具
// （规格§数值/§文本：舍入 half-even 跨语言一致；len/截断按码点）。
package mathx

import (
	"strconv"
	"unicode/utf8"
)

// Round 把 x 舍入到 places 位小数，half-even 语义与 CPython round 跨语言一致
// （规格§数值：14 万组对照 0 失配）。
//
// 必须走 FormatFloat 的十进制正确舍入（half-even），禁止
// math.RoundToEven(x*10ⁿ)/10ⁿ 的 naive 缩放——先乘再除引入误差，
// 同一批对照中该写法 109 例失配。
// places 须非负；负数位（Python round(x, -n)）不在实现范围。
func Round(x float64, places int) float64 {
	s := strconv.FormatFloat(x, 'f', places, 64)
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// RuneLen 返回 s 的码点数（= Python len(s)，非字节数）。
func RuneLen(s string) int {
	return utf8.RuneCountInString(s)
}

// RuneTrunc 按码点把 s 截断到至多 cap 个码点（Python s[:cap] 等价）；
// 不超长时原样返回。截断点必落在码点边界，不产生半码点乱码。
func RuneTrunc(s string, cap int) string {
	if cap <= 0 {
		return ""
	}
	if RuneLen(s) <= cap {
		return s
	}
	n := 0
	for i := range s {
		if n == cap {
			return s[:i]
		}
		n++
	}
	return s
}
