package daemon

// gate_log.go — 闸门警告落盘（P2 切 enforce 前置小项，2026-09-29 问题清单）：
// observe/enforce 的警告类事件此前无持久化日志（只能靠 CC 转录反查），enforce
// 验收线"误拦 ≤1 次/周"与 observe 期对照都需要逐条可数。block/bypass 已有账本
// 科目（Acct），本文件只补警告三站点：observe 警告、enforce 分支 7 首拦前警告、
// 连续 block 降级警告。
//
// 形态：追加一行到 <DataDir>/gate.log，尽力而为——失败静默（闸门面绝不因日志
// 失败受扰，fail-open 同纪律）；逐行开合（频率＝各会话钩子问询，不值得常开句柄）。

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// appendGateWarn 警告行落盘。字段：时间/决策/agent/会话短 id/模式/闲置秒。
// 会话 id 取前 16 位（v0.9.3 票4：前 8 位对 dsh 的 "session-" 前缀正好全吃掉，
// 生产 gate.log 全是 sid=session- 无法对照台账——16 位含 8 位 uuid 前缀可辨）；
// idle 取整秒。
func appendGateWarn(dataDir, agent, sid, mode string, idleS float64) {
	if dataDir == "" {
		return
	}
	line := fmt.Sprintf("%s [warn] %s sid=%s mode=%s idle=%.0fs\n",
		time.Now().Format("2006-01-02 15:04:05"), agent, shortSid(sid), mode, idleS)
	_ = appendLineBestEffort(filepath.Join(dataDir, "gate.log"), line)
}

// appendLineBestEffort 追加一行（建文件，失败静默）。var 形＝测试缝（对齐
// logShutdownSource 惯例）。
var appendLineBestEffort = func(path, line string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	return err
}

// shortSid 会话短 id（前 16 位；短于 16 原样。v0.9.3 票4：8→16，见
// appendGateWarn 头注）。
func shortSid(sid string) string {
	if len(sid) > 16 {
		return sid[:16]
	}
	return sid
}

// gateWarn 警告站点共用入口（Daemon 形，取数据目录；无配置＝不落）。
func (d *Daemon) gateWarn(agent, sid, mode string, idleS float64) {
	if d.Cfg == nil {
		return
	}
	appendGateWarn(d.Cfg.DataDir(), agent, sid, mode, idleS)
}
