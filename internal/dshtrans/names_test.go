package dshtrans

// P2-1 第二块砖：会话目录名规范化规则，逐条对 dsh 源码
// packages/session/session-persistence-jsonl/src/format.ts（master@639ed015）
// 钉死。真机目录回钉（2026-10-03 本机 ~/.dsh/sessions 实物）：
//   C:\Users\allan716\orca\workspaces\Ferryman\支持-deepseek-harness-服务商配置
//     → --C-Users-allan716-orca-workspaces-Ferryman-~652F~6301-deepseek-harness-~670D~52A1~5546~914D~7F6E--
//   C:\WorkSpace\ca_things → --C-WorkSpace-ca_things--
// （中文码点 4 位大写十六进制；盘符冒号+分隔符连续合并单 -。）

import (
	"strings"
	"testing"
)

func TestEncodeSegmentPinned(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"session-6e360520-09b8-46b6-9d82-cc9446ec8bd9", "session-6e360520-09b8-46b6-9d82-cc9446ec8bd9"}, // 全安全字符原样
		{".", "~002E"},                      // 防穿越特判
		{"..", "~002E~002E"},                // 防穿越特判
		{"a~b", "a~007Eb"},                  // ~ 自身也要转义（单射性）
		{"a b", "a~0020b"},                  // 空格
		{"支持", "~652F~6301"},                // BMP 中文（真机目录回钉值）
		{"\U0001F600", "~D83D~DE00"},        // 非 BMP：高低代理对各占一码元
		{"a/b\\c:d", "a~002Fb~005Cc~003Ad"}, // 分隔符在 segment 里不特殊（仅 projectKey 合并）
		{"", ""},                            // 空串报错（want 仅占位，下方单独断言 err）
	}
	for _, c := range cases {
		got, err := EncodeSegment(c.raw)
		if c.raw == "" {
			if err == nil {
				t.Errorf("EncodeSegment(空串) 应报错，得 %q", got)
			}
			continue
		}
		if err != nil {
			t.Errorf("EncodeSegment(%q) 报错: %v", c.raw, err)
			continue
		}
		if got != c.want {
			t.Errorf("EncodeSegment(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestDecodeSegmentRoundtrip(t *testing.T) {
	raws := []string{
		"session-6e360520-09b8-46b6-9d82-cc9446ec8bd9",
		"a~b", "支持", "\U0001F600", "a b", "x/y\\z", "普通-混排 Path_1.2",
	}
	for _, raw := range raws {
		seg, err := EncodeSegment(raw)
		if err != nil {
			t.Fatalf("EncodeSegment(%q): %v", raw, err)
		}
		back, ok := DecodeSegment(seg)
		if !ok || back != raw {
			t.Errorf("DecodeSegment(EncodeSegment(%q)) = (%q,%v), want (%q,true)", raw, back, ok, raw)
		}
	}
	// 非 ~XXXX 形态与非法输入拒绝。
	for _, bad := range []string{"~123", "~12G4", "~", "abc~xy", "naïve", "~D83D~0"} {
		if _, ok := DecodeSegment(bad); ok {
			t.Errorf("DecodeSegment(%q) 应拒绝", bad)
		}
	}
	// 真机目录名回钉：session-<uuid> 全安全字符，自反。
	seg, _ := EncodeSegment("session-5e78cf1d-e357-40f6-bae7-dda2e846fe8e")
	if seg != "session-5e78cf1d-e357-40f6-bae7-dda2e846fe8e" {
		t.Errorf("uuid 段应原样: %q", seg)
	}
}

func TestProjectKeyPinned(t *testing.T) {
	cases := []struct{ cwd, want string }{
		// 真机回钉两枚（磁盘实物）。
		{"C:\\WorkSpace\\ca_things", "--C-WorkSpace-ca_things--"},
		{"C:/Users/allan716/orca/workspaces/Ferryman/支持-deepseek-harness-服务商配置",
			"--C-Users-allan716-orca-workspaces-Ferryman-~652F~6301-deepseek-harness-~670D~52A1~5546~914D~7F6E--"},
		// 分隔符合并：盘符冒号+首分隔符连续 → 单 -；// 与 \\ 折叠。
		{"C://Users\\x", "--C-Users-x--"},
		{"/home/user", "--home-user--"},                             // 首部分隔符被去首 - 剥掉
		{"///", "--root--"},                                         // 全分隔符 → 空 slug → root
		{"C:\\WorkSpace\\项目~x", "--C-WorkSpace-~9879~76EE~007Ex--"}, // ~ 与中文都转义
		{"", ""}, // 空串报错（占位）
	}
	for _, c := range cases {
		got, err := ProjectKey(c.cwd)
		if c.cwd == "" {
			if err == nil {
				t.Errorf("ProjectKey(空串) 应报错，得 %q", got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ProjectKey(%q) 报错: %v", c.cwd, err)
			continue
		}
		if got != c.want {
			t.Errorf("ProjectKey(%q) = %q, want %q", c.cwd, got, c.want)
		}
	}
	// 截断 251 码元：长 cwd 的 slug 尾部截断、外包 -- 恒在。
	long := "C:\\" + strings.Repeat("a", 400)
	got, err := ProjectKey(long)
	if err != nil {
		t.Fatal(err)
	}
	want := "--" + ("C-" + strings.Repeat("a", 400))[:251] + "--"
	if got != want {
		t.Errorf("长路径截断不符：len(got)=%d want len=%d", len(got), len(want))
	}
}
