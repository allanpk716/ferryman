// dock_retry.go — 渡口绑定有界重试（2026-09-29 复盘守护侧根修，
// docs/20260929_守护重启事故复盘.md）。
//
// 背景：升级/重启竞态里，旧守护优雅关停先排水（在途流最长
// [dock].drain_timeout_s=180s），期间渡口口仍被旧进程占用——新守护若在
// 排水窗内单发绑定失败就降级"无渡口"，看门只见控制口 15700 健康 → 半死
// 形态无人救。本件把单发改成有界重试；控制口先于渡口绑定（serve.go 装配
// 序），重试等待期间看门/ensure 探活看到的仍是健康控制面，watcher/worker
// 仅在竞态窗内晚起。
package daemon

import (
	"time"

	"ferryman/internal/dock"
)

// dockRetryInterval 生产重试间隔（测试注入更小值换速度）。
const dockRetryInterval = 2 * time.Second

// startDockWithRetry 渡口绑定有界重试。newSrv 每次尝试整体重建（dock.Start
// 绑定失败即返、不留半开资源——重试按构造即弃置）；retry<=0 = 旧单发行为；
// interval<=0 回落生产值。成功返回绑定好的 Server；耗尽返回最后一次错误
// （调用方走既有"无渡口"降级，文案注明重试过）。
func startDockWithRetry(newSrv func() (*dock.Server, error), retry, interval time.Duration,
	logf func(format string, args ...any)) (*dock.Server, error) {
	if interval <= 0 {
		interval = dockRetryInterval
	}
	deadline := time.Now().Add(retry)
	var lastErr error
	for attempt := 1; ; attempt++ {
		ds, err := newSrv()
		if err == nil {
			err = ds.Start()
		}
		if err == nil {
			if attempt > 1 && logf != nil {
				logf("渡口绑定于第 %d 次尝试成功（此前被占属排水竞态预期）", attempt)
			}
			return ds, nil
		}
		lastErr = err
		if retry <= 0 || time.Now().Add(interval).After(deadline) {
			return nil, lastErr
		}
		if logf != nil && attempt == 1 {
			logf("渡口监听暂失败(%v)——%.0fs 内每 %v 重试（升级/重启排水竞态属预期）",
				err, retry.Seconds(), interval)
		}
		time.Sleep(interval)
	}
}
