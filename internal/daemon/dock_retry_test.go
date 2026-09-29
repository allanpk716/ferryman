// dock_retry_test.go — 渡口绑定有界重试验收钉子（2026-09-29 复盘件）。
// 四态：立即成功 / 被占 K 次后成功 / 恒败耗尽 / retry=0 旧单发。
package daemon

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"ferryman/internal/dock"
)

// freeLocalAddr 拿一个当前空闲的本机口（先绑后关留地址——测试内竞态窗口
// 极小，且重试语义本就容忍撞占）。
func freeLocalAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func newDockOn(addr string) func() (*dock.Server, error) {
	return func() (*dock.Server, error) { return dock.New(addr, "http://127.0.0.1:1") }
}

func TestStartDockWithRetryImmediateSuccess(t *testing.T) {
	addr := freeLocalAddr(t)
	var logs []string
	var mu sync.Mutex
	ds, err := startDockWithRetry(newDockOn(addr), time.Second, 20*time.Millisecond,
		func(f string, a ...any) { mu.Lock(); logs = append(logs, fmt.Sprintf(f, a...)); mu.Unlock() })
	if err != nil {
		t.Fatalf("立即成功路径不应报错: %v", err)
	}
	defer ds.Close()
	if len(logs) != 0 {
		t.Fatalf("首试成功不应有重试日志: %v", logs)
	}
}

func TestStartDockWithRetrySucceedsAfterOccupationClears(t *testing.T) {
	addr := freeLocalAddr(t)
	// 占住口两轮（interval=25ms），随后释放——模拟旧守护排水完毕让出渡口口。
	hold, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(60 * time.Millisecond)
		hold.Close()
	}()
	var logs []string
	ds, err := startDockWithRetry(newDockOn(addr), 2*time.Second, 25*time.Millisecond,
		func(f string, a ...any) { logs = append(logs, f) })
	if err != nil {
		t.Fatalf("占用释放后应绑定成功: %v", err)
	}
	defer ds.Close()
	if len(logs) == 0 {
		t.Fatal("首试失败应留'暂失败重试中'日志")
	}
	if logs[0][:4] != "渡口监听暂失败"[:4] && len(logs[0]) < 8 {
		t.Fatalf("日志形态异常: %v", logs)
	}
}

func TestStartDockWithRetryExhaustsAndErrors(t *testing.T) {
	addr := freeLocalAddr(t)
	hold, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	start := time.Now()
	attempts := 0
	newSrv := func() (*dock.Server, error) { attempts++; return newDockOn(addr)() }
	_, err = startDockWithRetry(newSrv, 100*time.Millisecond, 25*time.Millisecond, nil)
	if err == nil {
		t.Fatal("恒败应耗尽报错(调用方走无渡口降级)")
	}
	if attempts < 3 {
		t.Fatalf("应多轮重试, attempts=%d", attempts)
	}
	// 语义:不起超出预算的尝试——总耗时可短于上限至多一个间隔(25ms)。
	if el := time.Since(start); el < 50*time.Millisecond {
		t.Fatalf("应实际重试多轮: elapsed=%v", el)
	}
}

func TestStartDockWithRetryZeroIsSingleShot(t *testing.T) {
	addr := freeLocalAddr(t)
	hold, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	attempts := 0
	newSrv := func() (*dock.Server, error) { attempts++; return newDockOn(addr)() }
	start := time.Now()
	_, err = startDockWithRetry(newSrv, 0, 10*time.Millisecond, nil)
	if err == nil {
		t.Fatal("单发失败应报错")
	}
	if attempts != 1 {
		t.Fatalf("retry=0 应恰好尝试一次(旧单发行为), attempts=%d", attempts)
	}
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Fatalf("单发不应等待: elapsed=%v", el)
	}
}
