package notify

// title_test.go — 票08：通知标题构造器（项目名＋会话标题降级链）与告警文案
// 接线形态。
//
// 钉死内容（票面验收）：
//   - BuildTitle 三种降级形态逐字输出（有标题 / 无标题有首问 / 两者皆无）；
//   - 中文 rune 级截断不切半个汉字（30 汉字首问案例）；
//   - 总长封顶 100 rune（防御）；
//   - 接线形态（AlertCopy）：标题不再含裸 session id，sid 移正文尾部小字。

import (
	"strings"
	"testing"
)

func TestBuildTitleForms(t *testing.T) {
	three := strings.Repeat("字", 30)
	cases := []struct {
		name                  string
		project, title, quest string
		want                  string
	}{
		{"有标题", "Ferryman", "心跳保真实验", "", "Ferryman｜Ferryman：心跳保真实验"},
		{"无标题有首问_短于24不截", "proj", "", "怎么让心跳不干扰测量", "Ferryman｜proj：怎么让心跳不干扰测量"},
		{"无标题有首问_长则截24加省略号", "proj", "", three, "Ferryman｜proj：" + strings.Repeat("字", 24) + "…"},
		{"皆无_仅项目名", "proj", "", "", "Ferryman｜proj"},
		{"防御_项目名空但有标题", "", "标题", "", "Ferryman：标题"},
		{"防御_全空", "", "", "", "Ferryman"},
	}
	for _, c := range cases {
		if got := BuildTitle(c.project, c.title, c.quest); got != c.want {
			t.Errorf("%s: BuildTitle(%q,%q,%q) = %q, want %q",
				c.name, c.project, c.title, c.quest, got, c.want)
		}
	}
}

func TestBuildTitleChineseTruncationRuneSafe(t *testing.T) {
	// 30 汉字首问：截前 24 个整字＋省略号——绝无半个汉字（U+FFFD）。
	got := BuildTitle("p", "", strings.Repeat("字", 30))
	want := "Ferryman｜p：" + strings.Repeat("字", 24) + "…"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Fatal("出现半个汉字替换符 U+FFFD")
	}
	if n := len([]rune(got)); n != len([]rune("Ferryman｜p："))+24+1 {
		t.Fatalf("rune 数 = %d, want 前缀+24 整字+省略号", n)
	}
}

func TestBuildTitleCapsAt100Runes(t *testing.T) {
	got := BuildTitle(strings.Repeat("目", 200), strings.Repeat("题", 200), "")
	rs := []rune(got)
	if len(rs) != 100 {
		t.Fatalf("封顶后 rune 数 = %d, want 100", len(rs))
	}
	if !strings.HasPrefix(got, "Ferryman｜") {
		t.Fatalf("前缀丢失: %q", got)
	}
}

func TestProjectName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"C:/WorkSpace/agent/Ferryman", "Ferryman"},
		{`C:\WorkSpace\agent\Ferryman`, "Ferryman"},
		{"proj", "proj"},
		{"C:/proj/", "proj"},
		{`C:\proj\`, "proj"},
		{"/", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := ProjectName(c.in); got != c.want {
			t.Errorf("ProjectName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAlertCopyWiringShape(t *testing.T) {
	// 接线形态钉死：标题走降级链（绝不含裸 session id）；正文＝事件名＋原消息
	// ＋尾部 sid 小字（8 rune 短形，与日志 [ferry] cc/xxxxxxxx 同口径）。
	sid := "e0c12345-abcd"
	title, body := AlertCopy("C:/WorkSpace/agent/Ferryman", "心跳保真实验",
		"等待窗心跳熔断", "1 跳 MISS：停本窗剩余跳", sid)
	if title != "Ferryman｜Ferryman：心跳保真实验" {
		t.Fatalf("title = %q", title)
	}
	if strings.Contains(title, sid) || strings.Contains(title, "e0c12345") {
		t.Fatal("标题不得含裸 session id")
	}
	wantBody := "等待窗心跳熔断：1 跳 MISS：停本窗剩余跳\n(sid=e0c12345)"
	if body != wantBody {
		t.Fatalf("body = %q, want %q", body, wantBody)
	}
	// 降级：cwd/标题皆空 → 标题退裸 Ferryman，sid 小字仍在。
	title, body = AlertCopy("", "", "问询守望错误熔断", "已暂停", sid)
	if title != "Ferryman" {
		t.Fatalf("降级 title = %q, want Ferryman", title)
	}
	if !strings.HasSuffix(body, "\n(sid=e0c12345)") {
		t.Fatalf("sid 小字缺失: %q", body)
	}
	// 空 sid：不追加空小字。
	_, body = AlertCopy("p", "", "ev", "msg", "")
	if strings.Contains(body, "(sid=") {
		t.Fatalf("空 sid 不应有小字: %q", body)
	}
}
