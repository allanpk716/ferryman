// statsprobe — 常驻缓存真数据探针（ADR-0027 实测数字用；只读，一次性）。
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
	acc.Prewarm()
	fmt.Printf("Prewarm 全量解析耗时: %v\n", time.Since(t0))
	mem("暖机后")

	for i := 0; i < 3; i++ {
		t := time.Now()
		rows := acc.Read(accounts.ReadOpts{})
		fmt.Printf("Read#%d 全量(无过滤): %v · %d 行\n", i, time.Since(t), len(rows))
	}
	t := time.Now()
	rows := acc.Read(accounts.ReadOpts{Kind: "usage"})
	fmt.Printf("Read Kind=usage: %v · %d 行\n", time.Since(t), len(rows))
	t = time.Now()
	rows = acc.Read(accounts.ReadOpts{Since: float64(time.Now().AddDate(0, 0, -7).Unix())})
	fmt.Printf("Read 近7天: %v · %d 行\n", time.Since(t), len(rows))
	_ = rows
	mem("终态")
}
