package installer

// watchdog_forensics.go — 看门复位取证（P1 守护无痕死亡，2026-09-30）。
//
// 两起死因未定的实证（问题清单 P1）：09-29 08:37 守护静默消失、09-25
// 00:57-02:47 断续不可达 110 分钟——serve.err.log 在两个窗口全干净（无
// panic/fatal），serve.out.log 全史零引导失败行。进程内无痕，取证只能靠
// 外部观察者。看门恰是那个每 5 分钟一次的外部观察者，此前却只记一行
// "拉起 daemon"——前守护是谁、何时还活着、是否优雅退出，全丢。
//
// 取证三源（全部已在盘上，零新增状态）：
//   - daemon.pid：daemon 优雅停机必删（serveConfig 退出路径 os.Remove），
//     残留 + PID 已死 = 未走优雅关停的无痕死亡实锤；PID 仍在但口无监听 =
//     半死形态或 PID 复用。
//   - watchdog.log：最后一行「daemon 有响应」把死亡窗夹逼到 ≤5 分钟
//     （09-29 那次据此可夹到 08:37:00-08:42:00）。
//   - serve.out.log / serve.err.log：日志行里指名，人工顺着看。
//
// 尽力而为：任何一步失败只少一段信息，绝不影响分支②的拉起判定（取证
// 是旁路观察，不是裁判——与 gate.log 同纪律）。

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// wdPidFile daemon.pid 的只读消费形（daemon 侧 pidFileJSON 同形：pid/
// port/started_at——写方在 internal/daemon，此处只读不复制写逻辑）。
type wdPidFile struct {
	PID       int    `json:"pid"`
	Port      int    `json:"port"`
	StartedAt string `json:"started_at"`
}

// logRespawnForensics 分支②拉起前的复位取证：读 daemon.pid 残留与
// watchdog.log 末次应答，落一行结论。dataDir 空 = 不取证（最小装配/既有
// 测试零改动）；pidAlive nil = PID 活性不可判（如实标注，不猜）。行文钉死
// 关键词「复位取证」——watchdog.log 可 grep 的唯一锚。
func logRespawnForensics(dataDir string, pidAlive func(int) bool, logf func(string, ...any)) {
	if dataDir == "" || logf == nil {
		return
	}
	raw, rerr := os.ReadFile(filepath.Join(dataDir, "daemon.pid"))
	if rerr != nil {
		logf("[watchdog] 复位取证: daemon.pid 不在——上次退出已走优雅关停（优雅停会删 pid 文件）或从未启动，本次拉起即常规补位")
		return
	}
	lastSeen := orNoRecord(lastWatchdogOK(filepath.Join(dataDir, watchdogLogName)))
	var pj wdPidFile
	if jerr := json.Unmarshal(raw, &pj); jerr != nil || pj.PID <= 0 {
		logf("[watchdog] 复位取证: daemon.pid 残留但不可读（%s）——原样保留；上次探活应答≈%s",
			truncHint(raw), lastSeen)
		return
	}
	if pidAlive == nil {
		logf("[watchdog] 复位取证: daemon.pid 残留 pid=%d started=%s（PID 活性未装配不可判）；上次探活应答≈%s",
			pj.PID, pj.StartedAt, lastSeen)
		return
	}
	if !pidAlive(pj.PID) {
		logf("[watchdog] 复位取证: daemon.pid 残留 pid=%d started=%s——PID 已消失，守护未经优雅关停退出"+
			"（无痕死亡实锤）；死亡发生在上次探活应答≈%s 与本次探测之间；痕迹: serve.out.log / serve.err.log",
			pj.PID, pj.StartedAt, lastSeen)
		return
	}
	logf("[watchdog] 复位取证: daemon.pid 残留 pid=%d started=%s——PID 仍在但口无监听（半死形态或 PID 复用）；"+
		"上次探活应答≈%s；痕迹: serve.out.log / serve.err.log",
		pj.PID, pj.StartedAt, lastSeen)
}

// lastWatchdogOK watchdog.log 末次「daemon 有响应」行的时间戳（看门每
// 5 分钟一跳，死亡窗由此夹逼）。只读尾窗 64KiB（几百跳足够，几 MB 的全量
// 日志不值得全扫）；无匹配/文件不在 = 空串。匹配串耦合分支①行文
// 「daemon 有响应」——行文变更时此处同步（找不到只是少一段信息，不炸）。
func lastWatchdogOK(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return ""
	}
	const win = 64 << 10
	off := st.Size() - win
	if off < 0 {
		off = 0
	}
	buf := make([]byte, st.Size()-off)
	if _, rerr := f.ReadAt(buf, off); rerr != nil && rerr != io.EOF {
		return ""
	}
	last := ""
	for _, line := range strings.Split(string(buf), "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.Contains(line, "daemon 有响应") || len(line) < 19 {
			continue
		}
		if _, perr := time.ParseInLocation("2006-01-02 15:04:05", line[:19], time.Local); perr == nil {
			last = line[:19]
		}
	}
	return last
}

// orNoRecord 空时间戳的展示兜底。
func orNoRecord(s string) string {
	if s == "" {
		return "无记录"
	}
	return s
}

// truncHint 坏 pid 文件的内容提示（截 40 字符——日志行不灌全文）。
func truncHint(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return fmt.Sprintf("%q", s)
}
