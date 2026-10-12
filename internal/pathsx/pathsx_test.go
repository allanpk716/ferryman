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

// TestCCLaunchCwd 钩子 cwd 漂到子目录时按转录项目目录名校正回启动目录
//（2026-10-12 5bbc161a 漏拦案）。
func TestCCLaunchCwd(t *testing.T) {
	tp := `C:\Users\u\.claude\projects\C--Users-u-orca-work-daily-system\5bbc.jsonl`
	cases := []struct {
		name, cwd, tp, want string
	}{
		{"启动目录原样", `C:\Users\u\orca\work-daily-system`, tp, `C:\Users\u\orca\work-daily-system`},
		{"子目录校正", `C:\Users\u\orca\work-daily-system\apps\web\src`, tp, `C:\Users\u\orca\work-daily-system`},
		{"大小写不敏感", `c:\users\u\orca\work-daily-system\apps`, tp, `c:\users\u\orca\work-daily-system`},
		{"正斜杠", `C:/Users/u/orca/work-daily-system/apps`, `C:/x/projects/C--Users-u-orca-work-daily-system/a.jsonl`, `C:/Users/u/orca/work-daily-system`},
		{"cd 出项目树不动", `C:\Users\u\orca\other`, tp, `C:\Users\u\orca\other`},
		{"不认父目录项目", `C:\Users\u\orca`, tp, `C:\Users\u\orca`},
		{"中文段", `C:\W\漫画下载\a\b`, `C:\p\C--W-----\s.jsonl`, `C:\W\漫画下载`},
		{"无转录路径", `C:\a\b`, ``, `C:\a\b`},
		{"空 cwd", ``, tp, ``},
	}
	for _, c := range cases {
		if got := CCLaunchCwd(c.cwd, c.tp); got != c.want {
			t.Errorf("%s: CCLaunchCwd(%q) = %q, want %q", c.name, c.cwd, got, c.want)
		}
	}
}
