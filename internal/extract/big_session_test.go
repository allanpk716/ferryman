package extract_test

// big_session_test.go — 票17：tests/test_big_session.py（T18 大会话 L0）1:1。
// Python slow 标记（默认 -m 'not slow' 跳过）的 Go 形 = testing.Short() 跳过；
// 用本机真实最大会话验证 L0 完成时限、材料缩比、内存不炸。

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"ferryman/internal/extract"
)

// biggestSession Python _biggest_session 1:1：~/.claude/projects 下最大的 jsonl。
func biggestSession() (string, int64, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", 0, false
	}
	root := filepath.Join(home, ".claude", "projects")
	if _, err := os.Stat(root); err != nil {
		return "", 0, false
	}
	type fs struct {
		p string
		s int64
	}
	var files []fs
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".jsonl" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		files = append(files, fs{p, info.Size()})
		return nil
	})
	if len(files) == 0 {
		return "", 0, false
	}
	sort.Slice(files, func(i, j int) bool { return files[i].s > files[j].s })
	return files[0].p, files[0].s, true
}

// ---- Python: test_big_session.py::test_t18_big_session_l0 ----

func TestT18BigSessionL0(t *testing.T) {
	if testing.Short() { // Python @pytest.mark.slow（默认 not slow 跳过）的 Go 形
		t.Skip("slow 标记：go test -short 跳过（默认套件不带 -slow 同位）")
	}
	f, size, ok := biggestSession()
	if !ok || size < 5*1024*1024 {
		t.Skip("本机无 >5MB 会话可测")
	}
	t0 := time.Now()
	facts, items, _ := extract.Extract(f)
	elapsed := time.Since(t0)
	mat := extract.MaterialText(facts, items)
	matTokens := extract.TokenEstimate(mat)
	if elapsed > 120*time.Second {
		t.Fatalf("L0 超时 %.0fs", elapsed.Seconds())
	}
	if facts.PeakCtx <= 100_000 { // 大会话确实大
		t.Fatalf("peak_ctx = %d, want > 100000", facts.PeakCtx)
	}
	// 材料相对上下文的缩比 ≥2×（DESIGN §6.4 的 3-10× 假设下界放宽）
	if matTokens*2 > facts.PeakCtx {
		t.Fatalf("缩比不足: material=%d ctx=%d", matTokens, facts.PeakCtx)
	}
}
