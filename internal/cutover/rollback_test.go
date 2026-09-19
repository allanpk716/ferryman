// rollback_test.go — 票23：回退工件生成器与演练单测（脚本形状/CRLF/触发条件
// 定稿文案；演练幂等与降级路径；真实启动配置零改动由 DrillRollback 的参数面
// 结构性保证，此处再以"调用后仓库工作区无变化"自证）。

package cutover

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteRollbackScriptShape(t *testing.T) {
	repo := t.TempDir()
	data := t.TempDir()
	path, err := WriteRollbackScript(repo, data, "C:\\bin\\ferryman.exe")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(data, "rollback-to-python.cmd") {
		t.Fatalf("工件路径 = %s", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// CRLF 行尾 + 无 BOM
	if bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatal("工件带 BOM——cmd 首行 @echo off 会失效")
	}
	if !bytes.Contains(raw, []byte("\r\n")) {
		t.Fatal("工件非 CRLF")
	}
	if bytes.Contains(bytes.ReplaceAll(raw, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatal("工件存在孤立 LF")
	}
	s := string(raw)
	// 全 ASCII 不变量（票23 实测教训：非 UTF-8 代码页控制台下 UTF-8 中文会破坏
	// rem 记号/行结构——生成器必须产 ASCII 批处理，中文说明由 runbook 承载）
	for i, b := range raw {
		if b >= 0x80 {
			t.Fatalf("工件含非 ASCII 字节 @%d: %q", i, raw[max(0, i-10):min(len(raw), i+10)])
		}
	}
	// 幂等三件套：worktree 复用 / uv sync / 启动行指回 venv python
	// （失败分支走 goto 标签：块内 echo 遇路径含半角括号会截断块解析）
	for _, want := range []string{
		`if not exist "%REPO_PY%\.git" goto wt_check`,
		"worktree add \"%REPO_PY%\" %TAG%",
		"uv sync",
		`>"%LNK_TMP%" echo @echo off`,
		`-m ferryman serve`,
		`set "REPO=` + repo + `"`,
		`set "REPO_PY=` + repo + `-py"`,
		`set "DATA=` + data + `"`,
		`set "TAG=archive/python-final"`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("工件缺幂等要件 %q", want)
		}
	}
	// 触发条件定稿（附录#9/#10）：四类在列 + gate 误拦/漏拦不在列的显式声明
	for _, want := range []string{
		"Ferry output corrupted", "Ledger fields broken",
		"SessionStart / SubagentStart / SubagentStop",
		"cannot be hot-fixed", "gate false-block/miss is NOT a trigger",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("工件头缺触发条件 %q", want)
		}
	}
	// 演练指引（--drill 参数说明）
	if !strings.Contains(s, "rollback-drill") || !strings.Contains(s, "--dir") {
		t.Fatal("工件头缺演练指引")
	}
	// 占位符全部已替换（无残留 {{）
	if strings.Contains(s, "{{") {
		t.Fatal("工件残留未替换占位符")
	}
}

// initTempRepo 造带提交的临时 git 仓库（tag 可选）。返回仓库路径。
func initTempRepo(t *testing.T, withTag bool) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 不在位，跳过演练单测")
	}
	repo := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		out, err := runCmd(repo, 60*time.Second, "git", args...)
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return out
	}
	run("init", "-b", "main")
	run("config", "user.email", "smoke@example.invalid")
	run("config", "user.name", "smoke")
	files := map[string]string{
		"pyproject.toml": "[project]\nname = \"drill-fixture\"\nversion = \"0.0.0\"\n" +
			"requires-python = \">=3.9\"\ndependencies = []\n",
		"README.md": "drill fixture",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", ".")
	run("commit", "-m", "fixture")
	if withTag {
		run("tag", rollbackTag)
	}
	return repo
}

func TestDrillRollbackIdempotentWithWorktree(t *testing.T) {
	repo := initTempRepo(t, true)
	drill := t.TempDir()
	wt := filepath.Join(drill, "ferryman-py-drill")

	if err := DrillRollback(repo, drill); err != nil {
		t.Fatalf("第一次演练: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, ".git")); err != nil {
		t.Fatalf("演练 worktree 未建立: %v", err)
	}
	// 临时启动器副本：Python 形态、指向演练 venv、日志重定向在演练目录内
	launcherRaw, err := os.ReadFile(filepath.Join(drill, "start-daemon.cmd"))
	if err != nil {
		t.Fatal(err)
	}
	launcher := string(launcherRaw)
	wantPy := filepath.Join(wt, ".venv", "Scripts", "python.exe")
	for _, want := range []string{wantPy, "-m ferryman serve", "rewritten by rollback-to-python.cmd", drill} {
		if !strings.Contains(launcher, want) {
			t.Fatalf("临时启动器缺 %q:\n%s", want, launcher)
		}
	}
	if !strings.Contains(launcher, "\r\n") {
		t.Fatal("临时启动器非 CRLF")
	}
	// 真实启动配置零改动自证：仓库工作区无变化（worktree 登记是 .git 元数据，
	// 不进工作区；演练目录里的启动器副本与真实 start-daemon.cmd 无交集）
	if out, err := runCmd(repo, 30*time.Second, "git", "status", "--porcelain"); err != nil || out != "" {
		t.Fatalf("仓库工作区被污染: %q err=%v", out, err)
	}

	// 第二次演练：复用不重建（幂等）
	if err := DrillRollback(repo, drill); err != nil {
		t.Fatalf("第二次演练（幂等）: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, ".git")); err != nil {
		t.Fatalf("第二次演练后 worktree 消失: %v", err)
	}
}

func TestDrillRollbackDegradedWithoutTag(t *testing.T) {
	repo := initTempRepo(t, false) // 不打 tag——切换日前常态
	drill := t.TempDir()

	if err := DrillRollback(repo, drill); err != nil {
		t.Fatalf("tag 缺失时演练不得失败: %v", err)
	}
	// 降级：不建 worktree
	if _, err := os.Stat(filepath.Join(drill, "ferryman-py-drill")); err == nil {
		t.Fatal("降级模式不应建立 worktree")
	}
	// 但临时启动器副本照常产出（生成器链路被演练）
	if _, err := os.Stat(filepath.Join(drill, "start-daemon.cmd")); err != nil {
		t.Fatalf("降级模式仍应产出启动器副本: %v", err)
	}
}

func TestDrillRollbackRejectsNonWorktreeOccupiedPath(t *testing.T) {
	repo := initTempRepo(t, true)
	drill := t.TempDir()
	wt := filepath.Join(drill, "ferryman-py-drill")
	// 路径被普通目录占用（非 worktree）→ 响亮报错，不破坏现场
	if err := os.MkdirAll(filepath.Join(wt, "junk"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := DrillRollback(repo, drill); err == nil {
		t.Fatal("路径被非 worktree 占用应报错")
	}
	if _, err := os.Stat(filepath.Join(wt, "junk")); err != nil {
		t.Fatal("现场不应被破坏")
	}
}
