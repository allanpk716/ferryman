// evalferry_test.go — 票04:eval-ferry 子命令接线的入口例(不真跑 CLI,
// 测 cmdEvalFerry 函数;上游用 httptest 假端点)。
package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// evalTestEnv 装配:假上游 + 供应商配置 TOML + 样本目录 + 输出目录。
type evalTestEnv struct {
	srv      *httptest.Server
	cfgPath  string
	handoffs string
	out      string
}

func newEvalTestEnv(t *testing.T, narrative string) *evalTestEnv {
	t.Helper()
	e := &evalTestEnv{}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(
			`{"choices":[{"message":{"content":%q}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
			narrative)))
	}))
	t.Cleanup(e.srv.Close)

	e.cfgPath = filepath.Join(t.TempDir(), "config.toml")
	cfg := fmt.Sprintf("[providers.fake]\nbase_url = %q\nmodel = \"glm-5.3-flash\"\n", e.srv.URL)
	if err := os.WriteFile(e.cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	e.handoffs = t.TempDir()
	e.out = t.TempDir()
	return e
}

func (e *evalTestEnv) writeSample(t *testing.T, stamp, sid, material string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.handoffs, stamp+"_"+sid+".md"),
		[]byte(material), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCmdEvalFerryHappyPath(t *testing.T) {
	e := newEvalTestEnv(t, "<<<INJECT>>>\n叙事\n<<</INJECT>>>")
	e.writeSample(t, "20261001_074613", "aaa111", "素材甲")

	var stdout, stderr bytes.Buffer
	code := cmdEvalFerry([]string{
		"--provider", "fake", "--n", "1", "--out", e.out,
		"--handoffs", e.handoffs, "--config", e.cfgPath,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, stderr: %s", code, stderr.String())
	}
	// 文件对并排落盘,文件名含时间戳+短 ID。
	for _, suffix := range []string{"_骨架.md", "_叙事.md"} {
		p := filepath.Join(e.out, "20261001_074613_aaa111"+suffix)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("产物缺失 %s: %v", p, err)
		}
		if !strings.Contains(string(b), "素材甲") && !strings.Contains(string(b), "叙事") {
			t.Fatalf("产物内容缺素材/叙事: %s", b)
		}
	}
	if !strings.Contains(stdout.String(), "实取 1") {
		t.Fatalf("stdout 应有摘要: %s", stdout.String())
	}
}

func TestCmdEvalFerryMissingProviderName(t *testing.T) {
	e := newEvalTestEnv(t, "叙事")
	e.writeSample(t, "20261001_074613", "aaa111", "素材甲")
	var stdout, stderr bytes.Buffer
	code := cmdEvalFerry([]string{"--n", "1", "--out", e.out, "--handoffs", e.handoffs,
		"--config", e.cfgPath}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("缺 --provider 应退出 1, 得 %d", code)
	}
	if !strings.Contains(stderr.String(), "供应商名") {
		t.Fatalf("stderr 应点名供应商名: %s", stderr.String())
	}
}

func TestCmdEvalFerryUnknownProvider(t *testing.T) {
	e := newEvalTestEnv(t, "叙事")
	e.writeSample(t, "20261001_074613", "aaa111", "素材甲")
	var stdout, stderr bytes.Buffer
	code := cmdEvalFerry([]string{"--provider", "ghost", "--n", "1", "--out", e.out,
		"--handoffs", e.handoffs, "--config", e.cfgPath}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("未知供应商应退出 1, 得 %d", code)
	}
	if !strings.Contains(stderr.String(), "ghost") || !strings.Contains(stderr.String(), "fake") {
		t.Fatalf("stderr 应含缺失名与可用名: %s", stderr.String())
	}
}

func TestCmdEvalFerryMissingOut(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdEvalFerry([]string{"--provider", "fake"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("缺 --out 应用法退出 2, 得 %d", code)
	}
}
