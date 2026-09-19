// Package clock 提供可注入的 float64 epoch 秒时钟
// （规格§数值：时间 = float64 epoch 秒，mtime = UnixNano/1e9，测试注入时钟）。
package clock

import "time"

// Now 返回当前时刻的 float64 epoch 秒。刻意为包级变量而非常量函数，
// 测试可整体替换（Now = func() float64 { return fixed }），用毕恢复原值。
var Now = func() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}
