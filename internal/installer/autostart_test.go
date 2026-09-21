// autostart_test.go — 票02：HKCU Run 键自启 Install/Uninstall/Status 三操作验收。
//
// 全部走 fake 注册表面（runKeyStore 注入），绝不碰真注册表（本票副作用声明）。
// 命令构造逐字断言防两处漂移（Run 键值与状态比对共用 DaemonVBSPath 推导）。

package installer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// fakeRunKey 注册表 Run 键替身：值表 + 写/删计数（幂等断言的可观察面）。
type fakeRunKey struct {
	vals    map[string]string
	sets    int
	deletes int
}

func (f *fakeRunKey) GetStringValue(name string) (string, uint32, error) {
	v, ok := f.vals[name]
	if !ok {
		return "", 0, registry.ErrNotExist // 值缺（生产同形：FILE_NOT_FOUND）
	}
	return v, 0, nil // 值类型位生产逻辑不读，替身回 0
}

func (f *fakeRunKey) SetStringValue(name, value string) error {
	f.sets++
	f.vals[name] = value
	return nil
}

func (f *fakeRunKey) DeleteValue(name string) error {
	f.deletes++
	if _, ok := f.vals[name]; !ok {
		return registry.ErrNotExist
	}
	delete(f.vals, name)
	return nil
}

func (f *fakeRunKey) Close() error { return nil }

// fakeOpener 定格 opener（wantErr 非空 = 模拟键打不开/不存在）。
func fakeOpener(k runKeyStore, wantErr error) func(uint32) (runKeyStore, error) {
	return func(uint32) (runKeyStore, error) { return k, wantErr }
}

// testLauncher 带空格的点火脚本路径（引号包裹面的回归样本）。
const testLauncher = `C:\Program Files\Ferryman\start-daemon.cmd`

func testAutostartDeps(k *fakeRunKey) autostartDeps {
	return autostartDeps{open: fakeOpener(k, nil), launcher: testLauncher}
}

// 验收①：Install→Status=installed→再 Install 幂等（值不重复写）；
// Uninstall→Status=missing；再 Uninstall 幂等。
func TestAutostartInstallStatusIdempotentUninstall(t *testing.T) {
	k := &fakeRunKey{vals: map[string]string{}}
	d := testAutostartDeps(k)
	if err := installAutostart(d); err != nil {
		t.Fatalf("首次 install: %v", err)
	}
	if k.sets != 1 {
		t.Fatalf("首次 install 应恰好写一次值, sets=%d", k.sets)
	}
	st, err := autostartStatusOf(d)
	if err != nil || st != autostartInstalled {
		t.Fatalf("install 后应 installed, st=%v err=%v", st, err)
	}
	// 再 install：值已对 → 不重写（幂等的可观察面 = 写计数不动）
	if err := installAutostart(d); err != nil {
		t.Fatalf("二次 install: %v", err)
	}
	if k.sets != 1 {
		t.Fatalf("值已对时二次 install 不得重写, sets=%d", k.sets)
	}
	// 卸载 → missing；再卸载幂等（值已不在，删除调用不报错）
	if err := uninstallAutostart(d); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if st, _ := autostartStatusOf(d); st != autostartMissing {
		t.Fatalf("uninstall 后应 missing, st=%v", st)
	}
	if err := uninstallAutostart(d); err != nil {
		t.Fatalf("二次 uninstall 应幂等: %v", err)
	}
	if k.deletes != 2 {
		t.Fatalf("两次 uninstall 各删一次, deletes=%d", k.deletes)
	}
}

// 值不符 → mismatch；install 覆盖修复（exe 挪窝自愈）。
func TestAutostartStatusMismatchAndRepair(t *testing.T) {
	k := &fakeRunKey{vals: map[string]string{RunValueName: `"C:\old\ferryman.exe" serve`}}
	d := testAutostartDeps(k)
	st, err := autostartStatusOf(d)
	if err != nil || st != autostartMismatch {
		t.Fatalf("旧值应 mismatch, st=%v err=%v", st, err)
	}
	if err := installAutostart(d); err != nil {
		t.Fatalf("install 修复: %v", err)
	}
	if st, _ := autostartStatusOf(d); st != autostartInstalled {
		t.Fatalf("修复后应 installed, st=%v", st)
	}
}

// 键不存在 → missing（ErrNotExist 语义收口，不是错误）。
func TestAutostartStatusMissingWhenKeyAbsent(t *testing.T) {
	d := autostartDeps{open: fakeOpener(nil, registry.ErrNotExist), launcher: testLauncher}
	st, err := autostartStatusOf(d)
	if err != nil || st != autostartMissing {
		t.Fatalf("键缺应 missing, st=%v err=%v", st, err)
	}
	// 卸载对缺键幂等
	if err := uninstallAutostart(d); err != nil {
		t.Fatalf("缺键 uninstall 应幂等: %v", err)
	}
}

// 键打不开（非 ErrNotExist）→ 状态读取响亮报错，绝不误判 missing。
func TestAutostartStatusOpenErrorPropagates(t *testing.T) {
	d := autostartDeps{open: fakeOpener(nil, errors.New("access denied")), launcher: testLauncher}
	if _, err := autostartStatusOf(d); err == nil {
		t.Fatalf("键打不开应报错而非误判 missing")
	}
	if err := installAutostart(d); err == nil {
		t.Fatalf("键打不开 install 应失败（绝不报就绪假象）")
	}
}

// Run 键值逐字（wscript 隐身宿主零闪窗；VBS 落点与状态比对同一推导）。
func TestAutostartCommandVerbatim(t *testing.T) {
	got := AutostartCommand(testLauncher)
	want := `wscript.exe "C:\Program Files\Ferryman\start-daemon-hidden.vbs"`
	if got != want {
		t.Fatalf("Run 键值逐字不符:\n got: %s\nwant: %s", got, want)
	}
	// 隐身宿主必须在内（登录拉起零闪窗——旧 powershell -WindowStyle Hidden
	// 形态登录时闪一次黑窗，2026-09-21 事故后弃用）
	if !strings.Contains(got, "wscript.exe") {
		t.Fatalf("缺隐身宿主 wscript.exe: %s", got)
	}
	// 与 DaemonVBSPath 推导共用（防两处漂移的构造级断言）
	if got != fmt.Sprintf(`wscript.exe "%s"`, DaemonVBSPath(testLauncher)) {
		t.Fatalf("Run 键值应指向 DaemonVBSPath 推导落点: %s", got)
	}
}

// VBS 本体逐字：wscript 零闪窗形态（与看门 VBS 同款机制）。
func TestDaemonVBSBodyVerbatim(t *testing.T) {
	got := daemonVBSBody(testLauncher)
	want := "' Ferryman daemon hidden launcher (auto-generated, do not edit)\r\n" +
		"' wscript is a GUI-subsystem host: it never creates a console window.\r\n" +
		"' WshShell.Run style 0 = run child hidden; False = do not wait.\r\n" +
		"CreateObject(\"WScript.Shell\").Run \"\"\"C:\\Program Files\\Ferryman\\start-daemon.cmd\"\"\", 0, False\r\n"
	if got != want {
		t.Fatalf("VBS 本体逐字不符:\n got: %q\nwant: %q", got, want)
	}
}

// ensureDaemonVBS：落盘到 launcher 同目录（临时目录替身，绝不碰真 dataDir），
// 内容 = 本体构造。
func TestEnsureDaemonVBSWrites(t *testing.T) {
	dir := t.TempDir()
	launcher := filepath.Join(dir, LauncherName)
	if err := ensureDaemonVBS(launcher); err != nil {
		t.Fatalf("ensureDaemonVBS: %v", err)
	}
	raw, err := os.ReadFile(DaemonVBSPath(launcher))
	if err != nil {
		t.Fatalf("读回 VBS: %v", err)
	}
	if string(raw) != daemonVBSBody(launcher) {
		t.Fatalf("落盘内容与构造不符: %q", raw)
	}
}

// 键位常量钉死（规格逐字；写错键位＝自启静默失效）。
func TestAutostartKeyPathConstants(t *testing.T) {
	if RunKeyPath != `Software\Microsoft\Windows\CurrentVersion\Run` {
		t.Fatalf("Run 键路径不符: %s", RunKeyPath)
	}
	if RunValueName != "Ferryman" {
		t.Fatalf("值名不符: %s", RunValueName)
	}
}
