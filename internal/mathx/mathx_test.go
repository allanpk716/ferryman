package mathx

import (
	"testing"
	"unicode/utf8"
)

// TestRoundBoundary 边界电池：五例为跨语言对照实验（14 万组，0 失配）中
// naive 缩放实现会失配的钉死样例。期望值由 CPython round 实测钉死
// （uv run python -c "print(round(2.675,2), round(3.175,2), round(6.335,2),
// round(0.0005,3), round(123.4565,3))" → 2.67 3.17 6.33 0.001 123.457）。
// 这些期望值均为 float64 可精确表示的十进制值，直接 == 比较即可。
func TestRoundBoundary(t *testing.T) {
	cases := []struct {
		name   string
		x      float64
		places int
		want   float64
	}{
		{"2.675→2位", 2.675, 2, 2.67},
		{"3.175→2位", 3.175, 2, 3.17},
		{"6.335→2位", 6.335, 2, 6.33},
		{"0.0005→3位", 0.0005, 3, 0.001},
		{"123.4565→3位", 123.4565, 3, 123.457},
	}
	for _, c := range cases {
		if got := Round(c.x, c.places); got != c.want {
			t.Errorf("Round(%v, %d) = %v, want %v (%s)", c.x, c.places, got, c.want, c.name)
		}
	}
}

// TestRoundHalfEven half-even 常规例（与 CPython round 一致，places=0）。
func TestRoundHalfEven(t *testing.T) {
	cases := []struct {
		x    float64
		want float64
	}{
		{2.5, 2},
		{3.5, 4},
		{0.5, 0},
		{-2.5, -2},
	}
	for _, c := range cases {
		if got := Round(c.x, 0); got != c.want {
			t.Errorf("Round(%v, 0) = %v, want %v", c.x, got, c.want)
		}
	}
}

// TestRuneLen 码点数语义（= Python len(s)，非字节数）。
func TestRuneLen(t *testing.T) {
	cases := []struct {
		s    string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"中文", 2},
		{"a中b文", 4},
		{"中文.md", 5},
		// 组合序列（e + U+0301 重音）按码点计 2，与 Python len 一致。
		{"é", 2},
	}
	for _, c := range cases {
		if got := RuneLen(c.s); got != c.want {
			t.Errorf("RuneLen(%q) = %d, want %d", c.s, got, c.want)
		}
	}
}

// TestRuneTrunc 按码点截断：中文不出乱码、不超长返回原串。
func TestRuneTrunc(t *testing.T) {
	const s = "中文标题abc"
	if got := RuneTrunc(s, 4); got != "中文标题" {
		t.Errorf("RuneTrunc(%q, 4) = %q, want %q", s, got, "中文标题")
	}
	if got := RuneTrunc("中文.md", 3); got != "中文." {
		t.Errorf(`RuneTrunc("中文.md", 3) = %q, want "中文."`, got)
	}
	if got := RuneTrunc(s, 0); got != "" {
		t.Errorf("RuneTrunc(%q, 0) = %q, want 空串", s, got)
	}
	if got := RuneTrunc(s, 100); got != s {
		t.Errorf("RuneTrunc 不超长应原样返回，got %q", got)
	}
	if got := RuneTrunc(s, 7); got != s {
		t.Errorf("RuneTrunc cap=码点数应原样返回，got %q", got)
	}
	// 截断结果必须是合法 UTF-8（不落在码点中间）。
	if tr := RuneTrunc("一二三四五", 2); !utf8.ValidString(tr) || tr != "一二" {
		t.Errorf("RuneTrunc(五中文, 2) = %q, want 一二 且为合法 UTF-8", tr)
	}
}
