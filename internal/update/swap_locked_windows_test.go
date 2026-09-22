//go:build windows

package update

// 2026-09-22 v0.1.4 生产事故回归(ADR-0015):
// ① 同映像多进程(agent 面 mcp 实例)锁住换装目标——单次
//   MOVEFILE_REPLACE_EXISTING 替换必 Access denied,升级必败;换装改改名
//   让位两步式。本文件以「节映射句柄」复刻运行映像的锁语义(可改名、
//   不可覆盖)钉住锁下仍能完成换装。
// ② 升级失败 restoreServiceQuiet 拉起的守护常驻一个可见控制台——
//   launchCmdImpl 对 cmd.exe 误用 DETACHED_PROCESS(cmd 无控制台时执行批
//   处理会自建可见控制台)。修复 = 优先 wscript 走隐藏点火 VBS。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// lockLikeRunningImage 以「文件节映射」持有 target,复刻运行映像的锁语义:
// 改名(移动)放行、覆盖(REPLACE_EXISTING)拒绝——映像加载器即此形态。
func lockLikeRunningImage(t *testing.T, path string) (cleanup func()) {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatalf("打开目标失败: %v", err)
	}
	m, err := windows.CreateFileMapping(h, nil, windows.PAGE_READONLY, 0, 0, nil)
	if err != nil {
		windows.CloseHandle(h)
		t.Fatalf("建节失败: %v", err)
	}
	base, err := windows.MapViewOfFile(m, windows.FILE_MAP_READ, 0, 0, 0)
	if err != nil {
		windows.CloseHandle(m)
		windows.CloseHandle(h)
		t.Fatalf("映射失败: %v", err)
	}
	return func() {
		windows.UnmapViewOfFile(base)
		windows.CloseHandle(m)
		windows.CloseHandle(h)
	}
}

// TestSwapLockedImageStillSwaps 映射锁(运行映像同款)下:单次替换必败
// (前提钉子,证明模拟不失真),两步换装仍成功,备份为旧字节。
func TestSwapLockedImageStillSwaps(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, nil)
	// 预置 .new(下载完成态)。
	if err := os.WriteFile(newExePath(w.exePath), w.newBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	release := lockLikeRunningImage(t, w.exePath)
	defer release()

	// 前提钉子:该锁必须拦下单次 REPLACE_EXISTING——否则未复刻生产锁
	// 语义,模拟失真须修测试(此分支里目标已被换走,测试世界即刻作废)。
	if err := moveFileReplace(newExePath(w.exePath), w.exePath); err == nil {
		t.Fatal("前提不成立:映射句柄未能拦下单次替换——模拟失真,须修测试")
	}
	// 被拦的替换未留残迹(目标未动、.new 未消费)。
	if got := fileBytes(t, w.exePath); string(got) != string(w.oldBytes) {
		t.Fatal("被拦替换不应动目标")
	}

	j := journal{Phase: PhaseSwap, From: "v0.1.0", To: "v0.2.0",
		TargetExe: w.exePath, NewExe: newExePath(w.exePath),
		Backup: backupPath(w.exeDir, "v0.1.0"), StartCmd: w.cmdPath}
	if err := sup.swapFiles(j); err != nil {
		t.Fatalf("锁下两步换装应成功: %v", err)
	}
	if got := fileBytes(t, w.exePath); string(got) != string(w.newBytes) {
		t.Fatal("锁下换装后目标不是新版")
	}
	if got := fileBytes(t, j.Backup); string(got) != string(w.oldBytes) {
		t.Fatal("让位备份不是旧版")
	}
	if _, err := os.Stat(j.NewExe); !os.IsNotExist(err) {
		t.Fatal(".new 应被消费")
	}
}

// TestSwapReplacesStaleBackup 陈旧同名备份在场时,让位改名应直接覆盖它
// (陈旧备份非运行映像,可覆盖;2026-09-22 失败现场即留有同名陈旧备份)。
func TestSwapReplacesStaleBackup(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, nil)
	stale := backupPath(w.exeDir, "v0.1.0")
	if err := os.WriteFile(stale, []byte("stale-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newExePath(w.exePath), w.newBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	j := journal{TargetExe: w.exePath, NewExe: newExePath(w.exePath), Backup: stale}
	if err := sup.swapFiles(j); err != nil {
		t.Fatalf("陈旧备份不应阻碍换装: %v", err)
	}
	if got := fileBytes(t, stale); string(got) != string(w.oldBytes) {
		t.Fatal("备份应被旧版覆盖(让位即备份)")
	}
}

// TestSwapColonizedBackupStillSwaps 2026-09-22 v0.2.0 部署事故回归:
// 同 from 版本第二次升级时,备份名已被先前换装的存活让位者(agent 面 mcp
// 实例的运行映像)占据——运行映像可改名、不可被 REPLACE,让位步必
// Access denied(v0.1.5→v0.2.0 两次实测)。换装应把占据者挪到 old-* 域内
// 的 stale 名(字节保全,待 prune 收账)后重试让位,整体仍成功。
func TestSwapColonizedBackupStillSwaps(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, nil)
	// 预置 .new(下载完成态)与占据中的备份位(先前让位者,独立字节以区分)。
	if err := os.WriteFile(newExePath(w.exePath), w.newBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	backup := backupPath(w.exeDir, "v0.1.0")
	colonizer := []byte("colonizer-bytes")
	if err := os.WriteFile(backup, colonizer, 0o755); err != nil {
		t.Fatal(err)
	}
	release := lockLikeRunningImage(t, backup)
	defer release()

	// 前提钉子:占据者必须拦下单次让位(REPLACE 到备份位)——否则未复刻
	// 生产锁语义,模拟失真须修测试。
	if err := moveFileReplace(w.exePath, backup); err == nil {
		t.Fatal("前提不成立:映射句柄未能拦下对备份位的替换——模拟失真,须修测试")
	}
	// 被拦的让位未动现场。
	if got := fileBytes(t, w.exePath); string(got) != string(w.oldBytes) {
		t.Fatal("被拦让位不应动目标")
	}

	j := journal{Phase: PhaseSwap, From: "v0.1.0", To: "v0.2.0",
		TargetExe: w.exePath, NewExe: newExePath(w.exePath),
		Backup: backup, StartCmd: w.cmdPath}
	if err := sup.swapFiles(j); err != nil {
		t.Fatalf("备份位被占据时换装应成功(占据者挪窝让路): %v", err)
	}
	if got := fileBytes(t, w.exePath); string(got) != string(w.newBytes) {
		t.Fatal("换装后目标不是新版")
	}
	if got := fileBytes(t, backup); string(got) != string(w.oldBytes) {
		t.Fatal("让位备份不是旧版")
	}
	// 占据者被挪到 old-* 域内(pruneBackups 收账域)的 stale 文件,字节保全。
	aside := soleStaleAside(t, w.exeDir, backup)
	if got := fileBytes(t, aside); string(got) != string(colonizer) {
		t.Fatal("占据者字节应保全在 stale 文件")
	}
	if _, err := os.Stat(j.NewExe); !os.IsNotExist(err) {
		t.Fatal(".new 应被消费")
	}
}

// soleStaleAside 找 backup 之外 old-* 域内的挪窝文件(应恰有一个)。
func soleStaleAside(t *testing.T, exeDir, backup string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(exeDir, oldPrefixBase+"*"))
	if err != nil {
		t.Fatal(err)
	}
	var asides []string
	for _, m := range matches {
		if !strings.EqualFold(m, backup) {
			asides = append(asides, m)
		}
	}
	if len(asides) != 1 {
		t.Fatalf("old-* 域内应恰有一个挪窝文件, got %v", asides)
	}
	return asides[0]
}

// TestHiddenLauncherSibling 隐藏 VBS 同目录探测:在位命中、不在空。
func TestHiddenLauncherSibling(t *testing.T) {
	dir := t.TempDir()
	cmdPath := filepath.Join(dir, "start-daemon.cmd")
	if err := os.WriteFile(cmdPath, []byte("@echo off"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := hiddenLauncherSibling(cmdPath); got != "" {
		t.Fatalf("无 VBS 时应空, got %q", got)
	}
	vbs := filepath.Join(dir, "start-daemon-hidden.vbs")
	if err := os.WriteFile(vbs, []byte("' x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := hiddenLauncherSibling(cmdPath); !strings.EqualFold(got, vbs) {
		t.Fatalf("应命中同目录 VBS, got %q want %q", got, vbs)
	}
}

// TestLaunchCmdImplPrefersHiddenVBS VBS 在位时 launchCmdImpl 应走 wscript
// 通道:VBS 先写自己的标记、再转拉 cmd 写 CMD 标记——两个标记都出现才
// 证明走的是 VBS 链(直拉 cmd 只会留下 CMD)。
func TestLaunchCmdImplPrefersHiddenVBS(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker.txt")
	cmdPath := filepath.Join(dir, "start-daemon.cmd")
	if err := os.WriteFile(cmdPath,
		[]byte("@echo off\r\necho CMD>> \""+marker+"\"\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	vbs := filepath.Join(dir, "start-daemon-hidden.vbs")
	// 第二行与生产 start-daemon-hidden.vbs 同款(三引号包路径,直拉 cmd 脚本)。
	// 注意 VBS 整脚本先解析后执行——一处引号错即全脚本不跑(wscript /B 还会
	// 吞掉报错,只剩"什么都没发生")。
	vbsSrc := "CreateObject(\"Scripting.FileSystemObject\").OpenTextFile(\"" + marker +
		"\", 8, True).WriteLine \"VBS\"\r\n" +
		"CreateObject(\"WScript.Shell\").Run \"\"\"" + cmdPath + "\"\"\", 0, False\r\n"
	if err := os.WriteFile(vbs, []byte(vbsSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := launchCmdImpl(cmdPath); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(marker); err == nil {
			s := string(b)
			if strings.Contains(s, "VBS") && strings.Contains(s, "CMD") {
				return // VBS 链走通
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("10s 内未见 VBS+CMD 标记——wscript/VBS 通道未走通")
}
