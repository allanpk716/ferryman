// statsprobe — 聚合索引真数据探针（ADR-0027 改判实测数字用；只读，一次性）。
// 用法：go run ./.scratch/statsprobe/main.go
package main

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"ferryman/internal/accounts"
)

func mem(tag string) {
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	fmt.Printf("[mem:%s] HeapAlloc=%.1fMB Sys=%.1fMB\n",
		tag, float64(m.HeapAlloc)/1e6, float64(m.Sys)/1e6)
}

func main() {
	dir := os.Getenv("USERPROFILE") + `/ferryman`
	acc, err := accounts.New(dir)
	if err != nil {
		fmt.Println("new:", err)
		return
	}
	mem("起点")

	t0 := time.Now()
	acc.RebuildAggregates()
	fmt.Printf("RebuildAggregates 流式整建耗时: %v\n", time.Since(t0))
	mem("整建后（聚合常驻）")

	for i := 0; i < 3; i++ {
		t := time.Now()
		snap := acc.AggregateSnapshot()
		fmt.Printf("Snapshot#%d: %v · usage格=%d handoff格=%d\n",
			i, time.Since(t), len(snap.Usage), len(snap.Handoff))
	}

	t := time.Now()
	rows := acc.Read(accounts.ReadOpts{})
	fmt.Printf("Read 全量(无过滤,按需解析): %v · %d 行\n", time.Since(t), len(rows))
	mem("全量读后（明细应已可回收）")

	t = time.Now()
	rows = acc.ReadWindow(accounts.ReadOpts{Kind: "usage",
		Since: float64(time.Now().AddDate(0, 0, -7).Unix())})
	fmt.Printf("ReadWindow 近7天 kind=usage: %v · %d 行\n", time.Since(t), len(rows))
	_ = rows

	t = time.Now()
	cur := time.Now().Format("200601")
	prev := time.Now().AddDate(0, -1, 0).Format("200601")
	rows = acc.ReadMonths(accounts.ReadOpts{Kind: "usage"}, cur, prev)
	fmt.Printf("ReadMonths 当月+上月 kind=usage: %v · %d 行\n", time.Since(t), len(rows))
	_ = rows
	mem("终态")
}
