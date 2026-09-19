// rollback.go — 回退工件生成器 + 隔离演练（票23；评审附录#4/#5）。
//
// 职责边界（票面铁律）：
//   - WriteRollbackScript 只**生成** ~/ferryman/rollback-to-python.cmd（幂等批
//     处理工件），不执行它——生产切换留待用户晨间拍板；
//   - DrillRollback 在调用方给定的临时目录里演练同款机制（临时 worktree 路径 +
//     临时启动器副本），**结构性不接触**真实 start-daemon.cmd（它根本不知道
//     真实数据目录在哪）；
//   - 真实 tag archive/python-final 切换日才打——tag 缺失时生成器照常出脚本
//     （脚本自身的失败分支会提示打 tag），演练降级为路径探测、不因 tag 缺失
//     而失败（输出"tag 未打，演练降级为路径探测"）。
//
// 幂等（附录#4）：worktree 已存在且合法 → 复用不重建；路径被非 worktree 占用
// → 响亮报错请人工处理（绝不破坏现场）；uv sync 可重复执行；启动器重写为
// 固定内容（重复写无差）。

package cutover

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// rollbackTag 回退锚定的 Python 最终态 tag（spec Task 27 Step 1 切换日打）；
// 真实回退 worktree 路径 = <repo>-py（rev1 Task 27，见模板 REPO_PY 行）。
const rollbackTag = "archive/python-final"

// pyLauncherBody Python 形态点火脚本正文（CRLF）：与内部/installer.EnsureLauncher
// 的 Go 新形态同骨架，启动行指回 <repoPy> 的 venv python（rev1 Task 27 Step 3）。
// logDir 参数化 = 演练可把日志重定向进临时目录（真实回退传数据目录）。
// 正文纯 ASCII：批处理文件在非 UTF-8 代码页控制台下会被重解码，中文注释有
// 解析级风险（实测），ASCII 对任意代码页无条件安全。
func pyLauncherBody(repoPy, py, logDir string) string {
	return "@echo off\r\n" +
		"rem Ferryman daemon launcher (rewritten by rollback-to-python.cmd -> Python build)\r\n" +
		"cd /d \"" + repoPy + "\"\r\n" +
		fmt.Sprintf("\"%s\" -m ferryman serve >> \"%s\" 2>> \"%s\"\r\n",
			py,
			filepath.Join(logDir, "serve.out.log"),
			filepath.Join(logDir, "serve.err.log"))
}

// crlf 文本规范化为 CRLF 字节（批处理工件行尾铁律）。
func crlf(s string) []byte {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return []byte(strings.ReplaceAll(s, "\n", "\r\n"))
}

// ensureASCII 全 ASCII 铁律的生成器侧防线：模板全 ASCII 只是必要条件——占位
// 值（repoDir/dataDir/exePath 来自磁盘真实路径）若含非 ASCII 字节，工件照样会
// 在非 UTF-8 代码页控制台下炸（票23 实测教训）。任一入参含非 ASCII 字节 →
// 响亮拒绝生成，绝不产出一个注定解析失败的批处理。
func ensureASCII(what, s string) error {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return fmt.Errorf("%s 含非 ASCII 字节 @%d（回退批处理全 ASCII 铁律拒绝生成；"+
				"请用纯 ASCII 路径）: %q", what, i, s)
		}
	}
	return nil
}

// WriteRollbackScript 生成回退工件 <dataDir>/rollback-to-python.cmd（CRLF、
// UTF-8 无 BOM）。内容三步（rev1 Task 27 Step 3 逐字语义）：worktree add
// <repo>-py archive/python-final（幂等复用）→ uv sync → 点火脚本启动行指回
// <repo>-py venv python。文件头含回退触发条件（附录#9/#10 定稿）与演练指引。
// 返回工件路径。exePath 只进头注释（生成工具溯源），空则取当前 exe。
func WriteRollbackScript(repoDir, dataDir, exePath string) (string, error) {
	if repoDir == "" || dataDir == "" {
		return "", fmt.Errorf("repoDir/dataDir 均必填")
	}
	if exePath == "" {
		if p, err := os.Executable(); err == nil {
			exePath = p
		} else {
			exePath = "ferryman.exe"
		}
	}
	for _, c := range [...]struct{ what, val string }{
		{"repoDir", repoDir}, {"dataDir", dataDir}, {"exePath", exePath},
	} {
		if err := ensureASCII(c.what, c.val); err != nil {
			return "", err
		}
	}
	tpl := strings.NewReplacer(
		"{{TS}}", time.Now().Format("2006-01-02 15:04:05"),
		"{{EXE}}", exePath,
		"{{REPO}}", repoDir,
		"{{DATA}}", dataDir,
		"{{TAG}}", rollbackTag,
	).Replace(rollbackScriptTemplate)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dataDir, "rollback-to-python.cmd")
	if err := os.WriteFile(path, crlf(tpl), 0o644); err != nil {
		return "", err
	}
	fmt.Printf("[rollback] 回退工件已生成: %s\n", path)
	fmt.Printf("[rollback] 切换日前先演练: %q cutover rollback-drill --repo %q --dir <临时目录>\n",
		exePath, repoDir)
	return path, nil
}

// rollbackScriptTemplate 回退批处理模板（占位符 {{TS}}/{{EXE}}/{{REPO}}/{{DATA}}/
// {{TAG}}）。**全 ASCII**（票23 实测教训）：批处理文件在 GBK 等非 UTF-8 代码页
// 控制台下会被重解码，UTF-8 中文注释即使"字节不含 ASCII 元字符"也可能破坏
// rem 记号与行结构（生成器单测拦不住，真跑才炸）——ASCII 对任意代码页无条件
// 安全；中文说明由 Go 生成时打印 + runbook 承载。批处理转义注记：echo 文本里
// 的重定向用 ^> 、逻辑与用 ^& 转义；失败分支走 goto 标签而非括号块（块内
// echo 遇路径含半角括号会截断块解析，goto 形对任意路径安全）。
const rollbackScriptTemplate = `@echo off
rem ================================================================
rem Ferryman rollback artifact: one-shot rollback to the Python build.
rem Generated {{TS}} by: {{EXE}} cutover rollback-write  (do not hand-edit)
rem
rem [Rollback triggers (review appendix #9/#10, final)] any one fires a rollback:
rem   1. Ferry output corrupted - handoff MD garbage / empty / missing sections,
rem      or skeleton-fallback rate abnormally high
rem   2. Ledger fields broken - accounts/*.jsonl missing fields / wrong types /
rem      unknown subject keys
rem   3. Installed-hook failures - SessionStart / SubagentStart / SubagentStop
rem      not firing, timing out, or persistently erroring
rem   4. Daemon or panel crashes that cannot be hot-fixed
rem   NOTE: gate false-block/miss is NOT a trigger - the UserPromptSubmit gate
rem   hook is intentionally not installed (user decision C12), that channel is
rem   unobservable in production.
rem
rem [Drill] rehearse BEFORE cutover day (never touches this file):
rem   {{EXE}} cutover rollback-drill --repo "{{REPO}}" --dir <tmp-dir>
rem   drill uses a temp worktree + a temp launcher copy; if the tag is not
rem   pushed yet the drill degrades to path probing (still succeeds).
rem
rem [Idempotent] safe to re-run: existing worktree is reused; uv sync is
rem incremental when .venv survives; launcher rewrite is deterministic.
rem ================================================================
setlocal
set "REPO={{REPO}}"
set "REPO_PY={{REPO}}-py"
set "DATA={{DATA}}"
set "TAG={{TAG}}"
set "LNK_TMP=%DATA%\start-daemon.cmd.tmp"

if not exist "%DATA%" goto fail_data

rem ---- 1. Python-side worktree (idempotent: reuse when valid) ----
if not exist "%REPO_PY%\.git" goto wt_check
echo [rollback] worktree exists, reuse: %REPO_PY%
goto wt_ok
:wt_check
if exist "%REPO_PY%" goto fail_wt_occupied
git -C "%REPO%" worktree add "%REPO_PY%" %TAG%
if errorlevel 1 goto fail_wt_add
:wt_ok

rem ---- 2. Rebuild venv (uv sync is incremental; appendix #5: keep .venv
rem         through the 24h observation window so offline rollback survives) ----
pushd "%REPO_PY%"
uv sync
if errorlevel 1 goto fail_uv
popd
set "PY=%REPO_PY%\.venv\Scripts\python.exe"
if not exist "%PY%" goto fail_no_py

rem ---- 3. Point the daemon launcher back to Python (tmp+move, deterministic) ----
>"%LNK_TMP%" echo @echo off
>>"%LNK_TMP%" echo rem Ferryman daemon launcher (rewritten by rollback-to-python.cmd -^> Python build)
>>"%LNK_TMP%" echo cd /d "%REPO_PY%"
>>"%LNK_TMP%" echo "%PY%" -m ferryman serve ^>^> "%DATA%\serve.out.log" 2^>^> "%DATA%\serve.err.log"
move /y "%LNK_TMP%" "%DATA%\start-daemon.cmd" >nul
if errorlevel 1 goto fail_move

echo [rollback] done: launcher start line now points to %PY%
echo [rollback] next: stop the Go daemon (tray Exit / close the start-daemon window),
echo [rollback] then run "%DATA%\start-daemon.cmd" to bring the Python daemon back,
echo [rollback] and walk the runbook checklist to verify recovery.
echo [rollback] hooks need no change: ferryman-*.ps1 probe 127.0.0.1:7311 - whoever listens, wins.
exit /b 0

:fail_data
echo [rollback] data dir not found: %DATA%
exit /b 1
:fail_wt_occupied
echo [rollback] %REPO_PY% exists but is not a git worktree - inspect/remove it manually, then re-run.
exit /b 1
:fail_wt_add
echo [rollback] git worktree add failed - make sure the tag is pushed: git tag archive/python-final ^&^& git push --tags
exit /b 1
:fail_uv
echo [rollback] uv sync failed - uv missing or offline. If %REPO_PY%\.venv still exists it can be used as-is;
echo [rollback] otherwise restore network / install uv, then re-run this script.
popd
exit /b 1
:fail_no_py
echo [rollback] venv python missing: %PY%
exit /b 1
:fail_move
echo [rollback] move failed: %LNK_TMP% -^> %DATA%\start-daemon.cmd
echo [rollback] target locked or path unreachable - close the daemon window / check perms, then re-run.
exit /b 1
`

// DrillRollback 回退演练（附录#4/#5 隔离要求）：在 drillDir（临时目录）里走
// worktree 建立（或复用）+ uv sync --dry-run（或环境探测）+ 临时启动器副本。
// 结构性隔离：worktree 路径 = <drillDir>/ferryman-py-drill，启动器副本 =
// <drillDir>/start-daemon.cmd，日志重定向进 drillDir——真实 start-daemon.cmd
// 与真实数据目录不在本函数任何参数里，无从触碰。重复执行幂等。
//
// tag 缺失（切换日前常态）→ 打印"tag 未打，演练降级为路径探测"，只验路径可写
// 与工件生成，不建 worktree、不跑 uv，返回 nil。
func DrillRollback(repoDir, drillDir string) error {
	if repoDir == "" {
		return fmt.Errorf("repoDir 必填")
	}
	if drillDir == "" {
		d, err := os.MkdirTemp("", "ferryman-rollback-drill-")
		if err != nil {
			return err
		}
		drillDir = d
	}
	if err := os.MkdirAll(drillDir, 0o755); err != nil {
		return err
	}

	// git 在位核验（回退机制的第一依赖）
	if _, err := runCmd(drillDir, 30*time.Second, "git", "--version"); err != nil {
		return fmt.Errorf("git 不可用（回退与演练都依赖它）: %w", err)
	}
	// 自愈：清掉失效 worktree 登记（先前演练的临时目录被系统清掉会留陈旧注册）
	if _, err := runCmd(repoDir, 30*time.Second, "git", "worktree", "prune"); err != nil {
		return fmt.Errorf("git worktree prune 失败（repoDir 是否为 git 仓库？）: %w", err)
	}

	// tag 在位检查（切换日前缺失 = 常态，降级不失败）
	tagOK := true
	if _, err := runCmd(repoDir, 30*time.Second, "git", "rev-parse", "-q", "--verify",
		"refs/tags/"+rollbackTag); err != nil {
		tagOK = false
		fmt.Printf("[drill] tag %s 未打，演练降级为路径探测\n", rollbackTag)
	}

	wt := filepath.Join(drillDir, "ferryman-py-drill")
	if tagOK {
		// worktree 建立（或复用）——幂等
		switch _, statErr := os.Stat(filepath.Join(wt, ".git")); {
		case statErr == nil:
			fmt.Printf("[drill] 演练 worktree 已存在，复用: %s\n", wt)
		case os.IsNotExist(statErr):
			if _, err := os.Stat(wt); err == nil {
				return fmt.Errorf("%s 已存在但不是 git worktree——请换 --dir 或清空后重试", wt)
			}
			out, err := runCmd(repoDir, 120*time.Second,
				"git", "worktree", "add", wt, rollbackTag)
			if err != nil {
				return fmt.Errorf("演练 worktree 建立失败: %v\n%s", err, out)
			}
			fmt.Printf("[drill] 演练 worktree 已建立: %s\n", wt)
		default:
			return statErr
		}
		// uv sync --dry-run（附录#5 演练语义：不真装，只验证 uv+锁文件可用）
		if _, lookErr := exec.LookPath("uv"); lookErr != nil {
			fmt.Println("[drill] uv 不在 PATH——降级为环境探测（真实回退时 uv sync 需可用；" +
				".venv 若保留可直接续用）")
		} else if out, err := runCmd(wt, 180*time.Second, "uv", "sync", "--dry-run"); err != nil {
			return fmt.Errorf("uv sync --dry-run 失败（真实回退同款步骤会失败，须先修）: %v\n%s",
				err, out)
		} else {
			fmt.Println("[drill] uv sync --dry-run 通过（依赖可解析，未真装）")
		}
	} else {
		// 降级路径探测：drillDir 可写 + worktree 目标位无阻塞
		probe := filepath.Join(drillDir, ".probe")
		if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
			return fmt.Errorf("演练目录不可写: %w", err)
		}
		_ = os.Remove(probe)
		fmt.Printf("[drill] 路径探测通过（演练目录可写）: %s\n", drillDir)
	}

	// 临时启动器副本（与真实回退 Step 3 同款内容；日志重定向进演练目录）
	py := filepath.Join(wt, ".venv", "Scripts", "python.exe")
	body := pyLauncherBody(wt, py, drillDir)
	launcher := filepath.Join(drillDir, "start-daemon.cmd")
	if err := os.WriteFile(launcher, []byte(body), 0o644); err != nil {
		return err
	}
	fmt.Printf("[drill] 临时启动器副本已写: %s（真实 start-daemon.cmd 未触碰）\n", launcher)
	fmt.Printf("[drill] 演练通过（幂等可重跑）；演练 worktree 保留于临时目录，可随系统清理，"+
		"或 git -C %q worktree remove %q 回收\n", repoDir, wt)
	return nil
}

// runCmd 带超时的合并输出命令执行。
func runCmd(dir string, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}
