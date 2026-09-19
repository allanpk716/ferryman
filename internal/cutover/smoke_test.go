// smoke_test.go — 票23：沙箱冒烟单测（附录#11）。SmokeAll 全四链路即单测本体：
// 独立端口+独立数据目录+假会话样本+恒成功假 provider（httptest），真实 7311
// 与真实数据目录结构性不在参数面。另附启动器体形状单测。

package cutover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSmokeAllFourChains(t *testing.T) {
	if err := SmokeAll(""); err != nil {
		t.Fatalf("沙箱冒烟失败: %v", err)
	}
	// 沙箱纪律自证：本进程环境变量已还原（SmokeAll 的 defer 恢复语义）
	for _, key := range []string{"FERRYMAN_CONFIG", "FERRYMAN_DATA"} {
		if v, ok := os.LookupEnv(key); ok && strings.Contains(v, "ferryman-smoke-") {
			t.Fatalf("%s 未还原: %s", key, v)
		}
	}
}

func TestSmokeAllRejectsBadConfig(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(bad, []byte("gate.cc_mode = 123\n[thresholds\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SmokeAll(bad); err == nil {
		t.Fatal("坏沙箱配置应报错")
	}
}

func TestPyLauncherBodyShape(t *testing.T) {
	body := pyLauncherBody(`C:\repo-py`, `C:\repo-py\.venv\Scripts\python.exe`, `C:\Users\x\ferryman`)
	for _, want := range []string{
		"@echo off\r\n",
		`cd /d "C:\repo-py"` + "\r\n",
		`"C:\repo-py\.venv\Scripts\python.exe" -m ferryman serve`,
		`>> "C:\Users\x\ferryman\serve.out.log" 2>> "C:\Users\x\ferryman\serve.err.log"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("启动器体缺 %q:\n%s", want, body)
		}
	}
	if strings.HasSuffix(body, "\n") && !strings.HasSuffix(body, "\r\n") {
		t.Fatal("启动器体存在孤立 LF")
	}
}
