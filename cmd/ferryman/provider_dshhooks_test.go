// provider_dshhooks_test.go — 票01（P2-3 桥仓库侧，2026-10-03）：CLI 层的
// dsh-hooks 装配与晨间安装说明输出。钉三件事：
//   - Targets 派生：hooks.json 落 ~/ferryman/dsh-hooks/hooks.json（Ferryman
//     自家目录，绝不 ~/.dsh——D12）；
//   - FerrymanHooksDir 经 providerHooksDirFn 缝注入（exe 同根 hooks/ 惯例，
//     测试注桩不触真 exe 目录）；
//   - apply 输出晨间安装说明（plugin add 命令＋configPath 绝对路径指向），
//     --restore 路径不输出（还原不引导安装）。
//
// 全程桩面（providerApplyFn/providerHooksDirFn/osUserHomeDir），不触真用户
// 目录、不真写配置。
package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"ferryman/internal/provider"
)

// TestProviderTargetsFromHomeDSHHooks hooks.json 派生：home ⊕ ferryman/dsh-hooks
// ⊕ hooks.json；脚本目录不在本函数职责内（由 providerApply 组装）。
func TestProviderTargetsFromHomeDSHHooks(t *testing.T) {
	home := filepath.Join("some", "home")
	tg := providerTargetsFromHome(home, "127.0.0.1:15999", "", "")
	want := filepath.Join(home, "ferryman", "dsh-hooks", "hooks.json")
	if tg.DSHHooksJSON != want {
		t.Errorf("DSHHooksJSON = %q, want %q", tg.DSHHooksJSON, want)
	}
}

// TestProviderApplyWiresHooksDirAndHint apply 组装：FerrymanHooksDir 来自
// providerHooksDirFn 缝；输出含晨间安装说明（plugin add＋configPath 绝对路径）。
func TestProviderApplyWiresHooksDirAndHint(t *testing.T) {
	home := t.TempDir()
	hooksDir := filepath.Join(home, "fakehooks")
	oldHome := osUserHomeDir
	osUserHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { osUserHomeDir = oldHome })
	oldHooksDir := providerHooksDirFn
	providerHooksDirFn = func() string { return hooksDir }
	t.Cleanup(func() { providerHooksDirFn = oldHooksDir })

	var gotTargets provider.Targets
	oldApply := providerApplyFn
	providerApplyFn = func(tg provider.Targets) (provider.ApplyReport, error) {
		gotTargets = tg
		return provider.ApplyReport{Targets: []provider.TargetReport{
			{Name: "cc", Path: tg.CCSettings, Action: provider.ActionUnchanged,
				Detail: "已是目标形态（零写入）"},
			{Name: "dsh-hooks", Path: tg.DSHHooksJSON, Action: provider.ActionWritten,
				Detail: "新建（dsh CC 钩子桥配置——接管前不存在，无备份）"},
		}}, nil
	}
	t.Cleanup(func() { providerApplyFn = oldApply })

	f := writeProviderCfg(t, providerCfgSrc)
	var buf bytes.Buffer
	if code := providerApply(f, false, &buf); code != 0 {
		t.Fatalf("apply 退出码 = %d\n%s", code, buf.String())
	}
	// 装配：hooks 目录走缝、hooks.json 路径自家目录。
	if gotTargets.FerrymanHooksDir != hooksDir {
		t.Errorf("FerrymanHooksDir = %q, want %q", gotTargets.FerrymanHooksDir, hooksDir)
	}
	wantHooks := filepath.Join(home, "ferryman", "dsh-hooks", "hooks.json")
	if gotTargets.DSHHooksJSON != wantHooks {
		t.Errorf("DSHHooksJSON = %q, want %q", gotTargets.DSHHooksJSON, wantHooks)
	}
	// 晨间安装说明三件：plugin add 命令、configPath 指向、绝对路径缘由。
	out := buf.String()
	for _, want := range []string{
		"dsh plugin --profile web add @deepseek-ai/dsh-hooks-claude-code",
		"configPath",
		wantHooks,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("apply 输出缺晨间安装说明要素 %q:\n%s", want, out)
		}
	}
}

// TestProviderApplyHooksHintOnUnchangedToo 幂等二跑（unchanged）也出说明——
// 晨间人工可能跑两次 apply，第二次不能丢安装指引。
func TestProviderApplyHooksHintOnUnchangedToo(t *testing.T) {
	home := t.TempDir()
	oldHome := osUserHomeDir
	osUserHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { osUserHomeDir = oldHome })
	oldHooksDir := providerHooksDirFn
	providerHooksDirFn = func() string { return filepath.Join(home, "fakehooks") }
	t.Cleanup(func() { providerHooksDirFn = oldHooksDir })

	oldApply := providerApplyFn
	providerApplyFn = func(tg provider.Targets) (provider.ApplyReport, error) {
		return provider.ApplyReport{Targets: []provider.TargetReport{
			{Name: "dsh-hooks", Path: tg.DSHHooksJSON, Action: provider.ActionUnchanged,
				Detail: "已是目标形态（零写入）"},
		}}, nil
	}
	t.Cleanup(func() { providerApplyFn = oldApply })

	f := writeProviderCfg(t, providerCfgSrc)
	var buf bytes.Buffer
	if code := providerApply(f, false, &buf); code != 0 {
		t.Fatalf("apply 退出码 = %d\n%s", code, buf.String())
	}
	if out := buf.String(); !strings.Contains(out, "@deepseek-ai/dsh-hooks-claude-code") {
		t.Errorf("二跑（unchanged）仍应出安装说明:\n%s", out)
	}
}

// TestProviderRestoreNoHooksHint --restore 路径不引导安装（还原语义＝拆桥）。
func TestProviderRestoreNoHooksHint(t *testing.T) {
	home := t.TempDir()
	oldHome := osUserHomeDir
	osUserHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { osUserHomeDir = oldHome })

	oldRestore := providerRestoreFn
	providerRestoreFn = func(tg provider.Targets) (provider.ApplyReport, error) {
		return provider.ApplyReport{Targets: []provider.TargetReport{
			{Name: "dsh-hooks", Path: tg.DSHHooksJSON, Action: provider.ActionRestored,
				Detail: "本组无备份＋带接管标记＝apply 新建——已删除还原（接管前无此文件）"},
		}}, nil
	}
	t.Cleanup(func() { providerRestoreFn = oldRestore })

	f := writeProviderCfg(t, providerCfgSrc)
	var buf bytes.Buffer
	if code := providerApply(f, true, &buf); code != 0 {
		t.Fatalf("--restore 退出码 = %d\n%s", code, buf.String())
	}
	if out := buf.String(); strings.Contains(out, "@deepseek-ai/dsh-hooks-claude-code") {
		t.Errorf("--restore 不应输出安装说明:\n%s", out)
	}
}
