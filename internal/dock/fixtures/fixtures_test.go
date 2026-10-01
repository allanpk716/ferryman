// fixtures_test.go — 票04 夹具加载器钉子：四形全量可载、零真实密钥扫描
// （真形密钥负例必拦、合成占位放行）、夹具目录全文件干净（F3 夹具纪律）。
package fixtures

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// badSKKey 真形密钥负例串（拼接构造——本文件在被扫目录里，密钥形字面量
// 必须拆开写才不自我触雷；扫描器认形状不认内容，拼接后的运行时串照样该被拦）。
func badSKKey() string {
	return "sk-" + "a1B2c3D4e5F6g7H8i9J0k1L2m3n4O5p6"
}

// badGHToken GitHub token 形负例（同样拼接构造）。
func badGHToken() string {
	return "ghp_" + "A1b2C3d4E5f6G7h8I9j0K1l2M3n4"
}

func TestLoadAllFourShapes(t *testing.T) {
	fs, err := LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != len(Names) {
		t.Fatalf("夹具数 = %d, want %d", len(fs), len(Names))
	}
	for i, f := range fs {
		if f.Name != Names[i] {
			t.Fatalf("第 %d 夹具名 = %q, want %q", i, f.Name, Names[i])
		}
		if f.CodexRequest == nil || f.CodexRequest["model"] == "" || f.CodexRequest["input"] == nil {
			t.Fatalf("%s: codex_request 缺 model/input", f.Name)
		}
		if f.AnthropicUpstream == nil || len(f.AnthropicUpstream.SSE) == 0 {
			t.Fatalf("%s: anthropic_upstream 缺 SSE 回放数据", f.Name)
		}
		if f.ResponsesUpstream == nil {
			t.Fatalf("%s: 缺 responses_upstream（原生透传分支回放数据）", f.Name)
		}
		if f.ResponsesUpstream.Body == "" && len(f.ResponsesUpstream.SSE) == 0 {
			t.Fatalf("%s: responses_upstream 缺 SSE/body", f.Name)
		}
		all := append(append([]SSEEvent{}, f.AnthropicUpstream.SSE...), f.ResponsesUpstream.SSE...)
		for j, ev := range all {
			if ev.Event == "" || len(ev.Data) == 0 {
				t.Fatalf("%s: 第 %d 个 SSE 事件缺 event/data", f.Name, j)
			}
		}
	}
}

func TestLoadUnknownNameRejected(t *testing.T) {
	if _, err := Load("no_such_shape"); err == nil {
		t.Fatal("未知夹具名应报错")
	}
}

// TestScanRejectsRealShapedKey 零真实密钥扫描负例：真形密钥串（虽是假内容，
// 形状逼真）必须被拦；LoadDir 对含毒夹具目录拒绝加载。
func TestScanRejectsRealShapedKey(t *testing.T) {
	payload := `{"note":"负例：真形密钥串必须被拦","k":"` + badSKKey() + `","t":"` + badGHToken() + `"}`
	err := ScanForRealKeys([]byte(payload))
	if err == nil {
		t.Fatal("真形密钥串未被拦截")
	}
	if !strings.Contains(err.Error(), "密钥") {
		t.Fatalf("错误信息应说明密钥违规: %v", err)
	}

	dir := t.TempDir()
	poisoned := filepath.Join(dir, "poisoned.json")
	if err := os.WriteFile(poisoned, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDir(dir); err == nil {
		t.Fatal("LoadDir 应拒绝含真形密钥的夹具目录")
	} else if !strings.Contains(err.Error(), "poisoned.json") {
		t.Fatalf("错误应指名违规文件: %v", err)
	}
}

// TestScanAllowsSyntheticPlaceholders 合成占位放行：sk-test-* 前缀与显式
// 占位串（票面纪律：假钥一律 sk-test-* 或占位）不得误拦。
func TestScanAllowsSyntheticPlaceholders(t *testing.T) {
	clean := []string{
		`"k":"sk-test-0123456789abcdef"`,
		`"auth":"Bearer codex-placeholder-key"`,
		`"auth":"Bearer sk-test-0123456789abcdef"`,
		`"h":"x-api-key: placeholder"`,
		`"note":"普通文本与 sk- 短前缀不算密钥形"`,
	}
	for _, c := range clean {
		if err := ScanForRealKeys([]byte(c)); err != nil {
			t.Fatalf("合成占位被误拦 %q: %v", c, err)
		}
	}
}

// TestFixturesDirClean 扫描器对夹具目录全文件跑（含加载器/测试代码自身）：
// 目录内任何文件不得含真形密钥串。
func TestFixturesDirClean(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		if err := ScanForRealKeys(data); err != nil {
			t.Fatalf("夹具目录文件 %s 未过零真实密钥扫描: %v", e.Name(), err)
		}
	}
}
