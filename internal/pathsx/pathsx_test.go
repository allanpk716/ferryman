package pathsx

import "testing"

// TestNormPath lineage 唯一键形：反斜杠→正斜杠 + 小写。
func TestNormPath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`C:\A\B.MD`, `c:/a/b.md`},
		{`c:/a/b.md`, `c:/a/b.md`},                     // 正斜杠输入：仅小写化
		{`C:/A/B.MD`, `c:/a/b.md`},                     // 正斜杠 + 大写
		{`C:\A\MIXED/SLASH.MD`, `c:/a/mixed/slash.md`}, // 混合分隔符
		{``, ``}, // 空串
		{`D:\数据\报告.TXT`, `d:/数据/报告.txt`}, // 中文段 + 反斜杠
	}
	for _, c := range cases {
		if got := NormPath(c.in); got != c.want {
			t.Errorf("NormPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
