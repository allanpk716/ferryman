package main

// restart_test.go — 票07 `ferryman restart [--from N] [--to M]` 旗标解析纯函数
// 钉子（跟随 parseEvents 先例：解析抽纯函数直测；RunRestart/notify 不在 cmd
// 单测里真跑）。

import (
	"strings"
	"testing"
)

func TestParseRestartArgs(t *testing.T) {
	// 空参：0/0=未给（调用方回落盘上配置口）。
	from, to, err := parseRestartArgs(nil)
	if err != nil || from != 0 || to != 0 {
		t.Fatalf("空参 = (%d,%d,%v), want (0,0,nil)", from, to, err)
	}
	// 分离值形态。
	from, to, err = parseRestartArgs([]string{"--from", "15700", "--to", "15800"})
	if err != nil || from != 15700 || to != 15800 {
		t.Fatalf("分离值 = (%d,%d,%v), want (15700,15800,nil)", from, to, err)
	}
	// 自包含 --x=y 形态 + 单旗标。
	from, to, err = parseRestartArgs([]string{"--to=15800"})
	if err != nil || from != 0 || to != 15800 {
		t.Fatalf("自包含 = (%d,%d,%v), want (0,15800,nil)", from, to, err)
	}
	// 未知旗标 = 用法错。
	if _, _, err := parseRestartArgs([]string{"--bogus"}); err == nil {
		t.Fatal("未知旗标应报错")
	}
	// 多余位置参数 = 用法错。
	if _, _, err := parseRestartArgs([]string{"extra"}); err == nil {
		t.Fatal("多余位置参数应报错")
	}
	// 负端口 = 用法错（端口必须为正）。
	if _, _, err := parseRestartArgs([]string{"--from", "-3"}); err == nil ||
		!strings.Contains(err.Error(), "端口") {
		t.Fatalf("负端口应报端口错: %v", err)
	}
}
