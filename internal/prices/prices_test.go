package prices

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 测试 TOML 与 Python tests/test_prices.py 逐字一致。
const testTOML = `
[prices.glm]
unit = "智谱积分"
per = 10000

[[prices.glm.versions]]
effective_from = "2026-09-01"
p_in = 6.9
p_cache = 1.7
p_out = 24

[[prices.glm.versions]]
effective_from = "2026-09-17"
p_in = 6.9
p_cache = 1.7
p_out = 24

[prices.nocache]
unit = "元"
per = 1000000

[[prices.nocache.versions]]
effective_from = "2026-09-01"
p_in = 1.0
p_out = 2.0
`

// D16/D17 对应 Python 的 datetime(..., tzinfo=utc).timestamp()。
var (
	D16 = float64(time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC).Unix())
	D17 = float64(time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC).Unix())
)

// books 对应 pytest 的 books fixture：TOML 写进临时目录再加载。
func books(t *testing.T) map[string]PriceBook {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(testTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	return LoadPrices(p)
}

// TestMissingFileIsEmpty 无文件 → 空 dict。
func TestMissingFileIsEmpty(t *testing.T) {
	if got := LoadPrices(filepath.Join(t.TempDir(), "nope.toml")); len(got) != 0 {
		t.Fatalf("len(books) = %d, want 0", len(got))
	}
}

// TestVersionSelection 版本选择：生效日前一天 → 旧版；生效日起 → 新版；早于一切版本 → nil。
func TestVersionSelection(t *testing.T) {
	book := books(t)
	glm := book["glm"]
	if glm.Per != 10000 || glm.Unit != "智谱积分" {
		t.Fatalf("per/unit = (%d, %q), want (10000, 智谱积分)", glm.Per, glm.Unit)
	}
	if got := glm.At(D16); got == nil || got.EffectiveFrom != "2026-09-01" {
		t.Fatalf("at(D16) = %v, want 2026-09-01", got)
	}
	if got := glm.At(D17); got == nil || got.EffectiveFrom != "2026-09-17" {
		t.Fatalf("at(D17) = %v, want 2026-09-17", got)
	}
	if got := glm.At(0); got != nil {
		t.Fatalf("at(0) = %v, want nil", got)
	}
}

// TestPCacheOptional 缺省 = 无缓存经济。
func TestPCacheOptional(t *testing.T) {
	nocache := books(t)["nocache"]
	v := nocache.Versions[0]
	if v.PCache != nil {
		t.Fatalf("p_cache = %v, want nil", *v.PCache)
	}
}

// TestPriceTag 记账标签 "key@YYYY-MM-DD"。
func TestPriceTag(t *testing.T) {
	glm := books(t)["glm"]
	pv := glm.At(D17)
	if pv == nil {
		t.Fatal("at(D17) = nil")
	}
	if got := PriceTag("glm", *pv); got != "glm@2026-09-17" {
		t.Fatalf("price_tag = %q, want glm@2026-09-17", got)
	}
}

// captureStderr 换掉 os.Stderr 捕获告警输出。
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	w.Close()
	os.Stderr = old
	return <-done
}

// TestRequiredKeysMissingSkipped 票03评审M1：p_in/p_out 缺失或非数值的版本
// 跳过 + stderr 告警——杜绝零价行带着合法 price_tag 记账。
func TestRequiredKeysMissingSkipped(t *testing.T) {
	f := filepath.Join(t.TempDir(), "config.toml")
	tomlSrc := `
[prices.glm]
unit = "积分"
per = 10000

[[prices.glm.versions]]
effective_from = "2026-09-01"
p_cache = 1.7
p_out = 24

[[prices.glm.versions]]
effective_from = "2026-09-17"
p_in = 6.9
p_out = 24

[prices.bad]
unit = "元"

[[prices.bad.versions]]
effective_from = "2026-09-01"
p_in = "6.9"

[[prices.bad.versions]]
effective_from = "2026-09-02"
p_in = 1.0
p_out = 2.0
`
	if err := os.WriteFile(f, []byte(tomlSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	var got map[string]PriceBook
	errOut := captureStderr(t, func() { got = LoadPrices(f) })
	glm := got["glm"]
	if len(glm.Versions) != 1 || glm.Versions[0].EffectiveFrom != "2026-09-17" {
		t.Fatalf("glm 版本 = %v, want 仅 2026-09-17（缺 p_in 的被跳过）", glm.Versions)
	}
	bad := got["bad"]
	if len(bad.Versions) != 1 || bad.Versions[0].EffectiveFrom != "2026-09-02" {
		t.Fatalf("bad 版本 = %v, want 仅 2026-09-02（p_in 非数值的被跳过）", bad.Versions)
	}
	// 不产出 0 价书：任何存活版本的 p_in/p_out 都不得为 0 兜底值
	for key, b := range got {
		for _, v := range b.Versions {
			if v.PIn == 0 || v.POut == 0 {
				t.Fatalf("%s@%s 出现零价（PIn=%v POut=%v）", key, v.EffectiveFrom, v.PIn, v.POut)
			}
		}
	}
	// 每个被跳过的版本一条 stderr 告警
	for _, want := range []string{
		"[prices] 版本缺必填键，跳过: glm@2026-09-01",
		"[prices] 版本缺必填键，跳过: bad@2026-09-01",
	} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr 告警缺 %q:\n%s", want, errOut)
		}
	}
}
