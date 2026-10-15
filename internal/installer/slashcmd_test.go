package installer

// slashcmd_test.go —— 票39（spec#34）：install-cc 写用户级斜杠命令
// /ferry-restore（~/.claude/commands/ferry-restore.md，命令体调 ferryman
// restore 把输出带回会话）＋ doctor 在位检查。纪律：全部走临时目录
// （settingsPath 注入派生 commands 目录），绝不触真实 ~/.claude。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// slashCmdFixture 临时 home + settings 路径 + 不存在的 cc-switch 库（密闭性：
// dbPath 显式指向临时区不存在的文件，绝不碰本机真实 CC Switch 库——
// install_test 同款纪律）。commands 目录从 settingsPath 派生，与真实
// <home>/.claude 同形。
func slashCmdFixture(t *testing.T) (settingsPath, noDB, dataDir, repo string) {
	t.Helper()
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath = filepath.Join(home, ".claude", "settings.json")
	noDB = filepath.Join(tmp, "no-ccswitch.db")
	dataDir = filepath.Join(tmp, "ferryman")
	repo = makeHookRepo(t, filepath.Join(tmp, "repo"))
	return settingsPath, noDB, dataDir, repo
}

func TestInstallCCWritesFerryRestoreSlashCommand(t *testing.T) {
	settingsPath, noDB, dataDir, repo := slashCmdFixture(t)
	read := captureStdout(t)
	InstallCC(settingsPath, noDB, dataDir, repo, nil)
	out := read()
	cmdPath := filepath.Join(filepath.Dir(settingsPath), "commands", "ferry-restore.md")
	raw, err := os.ReadFile(cmdPath)
	if err != nil {
		t.Fatalf("斜杠命令文件应写入 %s: %v", cmdPath, err)
	}
	body := string(raw)
	// 自有文件标记（幂等覆盖与 doctor 在位判定的单源判据）
	if !strings.Contains(body, slashCommandMarker) {
		t.Fatalf("缺自有标记 %q:\n%s", slashCommandMarker, body)
	}
	// CC 自定义命令形状：frontmatter（description/allowed-tools/argument-hint）
	// + `!` 动态命令行（bash 执行、输出带回会话）
	if !strings.HasPrefix(body, "---\n") {
		t.Fatalf("frontmatter 应在文件首:\n%s", body)
	}
	if !strings.Contains(body, "description:") || !strings.Contains(body, "argument-hint:") {
		t.Fatalf("frontmatter 缺 description/argument-hint:\n%s", body)
	}
	if !strings.Contains(body, "allowed-tools: Bash(ferryman restore:*)") {
		t.Fatalf("allowed-tools 应预授权 ferryman restore:\n%s", body)
	}
	if !strings.Contains(body, "!`") || !strings.Contains(body, "`") {
		t.Fatalf("命令体应为 !`…` 动态命令行:\n%s", body)
	}
	// 命令体调本 exe 的 restore（绝对路径正斜杠形——bash 引号内可执行）
	exeFwd := filepath.ToSlash(exePath())
	if !strings.Contains(body, "\""+exeFwd+"\" restore") {
		t.Fatalf("命令体应调 %q restore:\n%s", exeFwd, body)
	}
	if !strings.Contains(out, "ferry-restore") {
		t.Fatalf("安装输出应提及斜杠命令: %q", out)
	}
}

func TestInstallCCSlashCommandIdempotentRefresh(t *testing.T) {
	settingsPath, noDB, dataDir, repo := slashCmdFixture(t)
	read := captureStdout(t)
	InstallCC(settingsPath, noDB, dataDir, repo, nil)
	read()
	cmdPath := filepath.Join(filepath.Dir(settingsPath), "commands", "ferry-restore.md")
	first, err := os.ReadFile(cmdPath)
	if err != nil {
		t.Fatal(err)
	}
	read2 := captureStdout(t)
	InstallCC(settingsPath, noDB, dataDir, repo, nil)
	read2()
	second, err := os.ReadFile(cmdPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("重跑应幂等刷新（字节一致）:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

func TestInstallCCSlashCommandForeignFileNotOverwritten(t *testing.T) {
	settingsPath, noDB, dataDir, repo := slashCmdFixture(t)
	cmdDir := filepath.Join(filepath.Dir(settingsPath), "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmdPath := filepath.Join(cmdDir, "ferry-restore.md")
	foreign := "# 我自己的恢复命令\n别动\n"
	if err := os.WriteFile(cmdPath, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	read := captureStdout(t)
	code := InstallCC(settingsPath, noDB, dataDir, repo, nil)
	out := read()
	// 冲突处置：不覆盖、原样保留、响亮报告
	raw, err := os.ReadFile(cmdPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != foreign {
		t.Fatalf("用户同名文件不得被覆盖:\n%s", raw)
	}
	if !strings.Contains(out, "冲突") || !strings.Contains(out, "不覆盖") {
		t.Fatalf("安装输出应报告冲突: %q", out)
	}
	// 钩子照常安装（冲突只挡斜杠命令这一个文件）
	if code != 0 {
		t.Fatalf("冲突不拖垮整个安装: code=%d out=%q", code, out)
	}
	sraw, err := os.ReadFile(settingsPath)
	if err != nil || !strings.Contains(string(sraw), "ferryman") {
		t.Fatalf("钩子应照常写入: %v", err)
	}
}

func TestCheckCCSlashCommandStates(t *testing.T) {
	tmp := t.TempDir()
	// 缺位：提示不失败（与闸门事件同级——spec#34「斜杠命令文件缺位同样提示」）
	c := CheckCCSlashCommand(filepath.Join(tmp, "ferry-restore.md"))
	if !c.OK || !strings.Contains(c.Msg, "未装") || !strings.Contains(c.Msg, "install-cc") {
		t.Fatalf("缺位应为提示不失败: %+v", c)
	}
	// 自有文件在位
	p := filepath.Join(tmp, "ferry-restore.md")
	if err := os.WriteFile(p, []byte("---\n# "+slashCommandMarker+"\n---\n!`x`\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if c = CheckCCSlashCommand(p); !c.OK || !strings.Contains(c.Msg, "在位") {
		t.Fatalf("自有文件应在位: %+v", c)
	}
	// 他人同名文件：冲突（fail 方向——需要人处置）
	if err := os.WriteFile(p, []byte("用户自定义\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if c = CheckCCSlashCommand(p); c.OK || !strings.Contains(c.Msg, "非 Ferryman") {
		t.Fatalf("他人同名文件应 fail 点名冲突: %+v", c)
	}
}

func TestDoctorResultsIncludesSlashCommand(t *testing.T) {
	// greenDoctorDeps 经 InstallCC 装好命令文件 → cc_slash_command 在位 pass；
	// 删文件后同 deps 重算 → 缺位提示（pass 注记，不产红）。
	deps, _ := greenDoctorDeps(t, func() map[string]any { return nil })
	res := doctorResults(deps)
	var found bool
	for _, r := range res {
		if r.Name == "cc_slash_command" {
			found = true
			if r.Status != StatusPass || !strings.Contains(r.Detail, "在位") {
				t.Fatalf("装态应 pass 在位: %+v", r)
			}
		}
	}
	if !found {
		t.Fatal("doctorResults 应含 cc_slash_command 项")
	}
	cmdPath := CCSlashCommandPath(deps.Home)
	if err := os.Remove(cmdPath); err != nil {
		t.Fatal(err)
	}
	res = doctorResults(deps)
	for _, r := range res {
		if r.Name == "cc_slash_command" {
			if r.Status != StatusPass || !strings.Contains(r.Detail, "未装") {
				t.Fatalf("缺位应提示不产红: %+v", r)
			}
			return
		}
	}
	t.Fatal("删文件后仍应有 cc_slash_command 项")
}
