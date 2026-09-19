package daemon

// serve_beat_test.go — 票03：serve 注入钉子。
//
// 渡口开（配了 [dock] 且快照句柄在）→ newBeatSender 产 HttpBeatSender；
// 渡口关（含配了 [dock] 但渡口启动失败、快照句柄 nil 的降级情形）→ nil，
// watcher 既有 "enforce 无 sender→observe 演练＋告警一次" 路径不变——
// qwatch 既有测试零回归由全量 ./internal/daemon/... 兜底验收。

import (
	"testing"

	"ferryman/internal/beat"
	"ferryman/internal/config"
	"ferryman/internal/dock"
)

func TestNewBeatSenderDockOff(t *testing.T) {
	// 无 [dock]（Default() 不构造 Dock——F11）：BeatSender 保持 nil
	if got := newBeatSender(config.Default(), nil); got != nil {
		t.Fatalf("渡口关 newBeatSender = %T, want nil", got)
	}
}

func TestNewBeatSenderDockCfgButNoSnapshots(t *testing.T) {
	// 配了 [dock] 但渡口构造/绑定失败（快照句柄 nil）：不得注入半真身——
	// 保持既有降级路径（观察演练），杜绝"有 sender 无快照源"的空转 enforce。
	cfg := config.Default()
	cfg.Dock = &config.DockCfg{Listen: "127.0.0.1:15722"}
	if got := newBeatSender(cfg, nil); got != nil {
		t.Fatalf("快照句柄缺失 newBeatSender = %T, want nil", got)
	}
}

func TestNewBeatSenderDockOn(t *testing.T) {
	cfg := config.Default()
	cfg.Dock = &config.DockCfg{
		Listen:          "127.0.0.1:15722",
		UpstreamBaseURL: "http://127.0.0.1:15721",
	}
	got := newBeatSender(cfg, dock.NewSnapshotStore())
	if got == nil {
		t.Fatal("渡口开 newBeatSender = nil, want *beat.HttpBeatSender")
	}
	if _, ok := got.(*beat.HttpBeatSender); !ok {
		t.Fatalf("newBeatSender = %T, want *beat.HttpBeatSender", got)
	}
}
