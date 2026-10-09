// accounts_cache.go — 常驻解析缓存 + 增量追加（统计卡顿票，ADR-0027）。
//
// 动机：daemon 每次查询（翻页/30s 轮询/dsh 事件/gate 扫描）都从头解析
// 全量账本（~95MB、19.5 万行），固定成本吃满每次请求。本文件把「每文件
// 解析一次、常驻内存」下沉到 accounts 读取层：Read/ReadMonths 的六维过滤
// 改在缓存条目上进行，/stats/*、/widget/summary、/report、dsh/gate 的全部
// 账本读取一起受益。
//
// 铁律不动：账本仍是 append-only JSONL 唯一事实源（ADR-0002）；缓存只是
// 可随时重建的读侧派生物——命中不了下述侦测条件时整文件重解，正确性
// 永远以盘上字节为准。
//
// 换文件侦测（三重，任一命中 → 整文件重解）：
//   1. size < parsed   —— 收缩（T39 式重写/截断）；
//   2. size == parsed 且 mtime 变 —— 同长改写（原地覆盖）；
//   3. 尾样 64 字节不符 —— 换了文件或前缀被改写（重建后更长也在此拦）。
//
// 撕裂行语义（与旧 readFile 的差异，ADR-0027 如实声明）：
//   - 旧：并发写下读到的半行当坏行丢（每轮 stderr 告警）；
//   - 新：无 \n 收尾的尾段计为一行（旧 splitlines 语义保留），但记
//     rescan 锚——后续追加字节若续写该行，回退到行首重解，写完即正确。
//
// 坏行告警语义：从「每次 Read 都报」变为「首次解析时报一次」（缓存不再
// 重复读同一段字节；stdout 机器可解析的纪律不变）。

package accounts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// tailSampleN 换文件侦测的尾样字节数。
const tailSampleN = 64

// fileState 单文件增量解析状态。entries 为共享只读（消费方不改性约定，
// 见 Accounts.cmu 注释）；lineEnd/parsed/nAtTail 三元组实现撕裂行重解锚。
type fileState struct {
	entries []map[string]any
	lineEnd int64 // 最后一个 '\n' 收尾的完整行之后的字节位
	parsed  int64 // 已解析到的字节位（尾段计行时可越过 lineEnd）
	nAtTail int   // parsed > lineEnd 时的截断锚（entries 合法长度）
	lineNo  int   // 行号计数（含空行，与从头整读的行号一致——告警文案用）
	modTime time.Time
	tail    []byte // [max(0,parsed-64), parsed) 尾样（换文件侦测）
}

// initCacheLocked 懒初始化（零值 Accounts 直接构造的测试形态兜底）。
// 调用方须持 cmu。
func (a *Accounts) initCacheLocked() {
	if a.files == nil {
		a.files = map[string]*fileState{}
	}
	if a.keys == nil {
		a.keys = map[string]string{}
	}
	if a.vals == nil {
		a.vals = map[string]string{}
	}
}

// refresh 刷新单文件缓存并返回其状态；文件消失（stat 失败）→ 作废缓存
// 返回 nil。调用方须持 cmu（stat + 小读 + 解析都在锁内——暖机后每次刷新
// 只是 stat + 增量字节，临界区极短；首次全量解析的秒级临界区由启动
// Prewarm 提前消化）。
func (a *Accounts) refresh(path, base string) *fileState {
	fi, err := os.Stat(path)
	if err != nil {
		delete(a.files, base) // 文件没了：缓存随之作废
		return nil
	}
	fs := a.files[base]
	if fs == nil {
		fs = &fileState{}
		a.files[base] = fs
	}
	size := fi.Size()
	if size < fs.parsed ||
		(size == fs.parsed && !fs.modTime.Equal(fi.ModTime())) ||
		!bytes.Equal(fs.tail, readTailSample(path, fs.parsed)) {
		*fs = fileState{} // 三重侦测任一命中：整文件重解
	}
	fs.modTime = fi.ModTime()
	if size == fs.parsed {
		return fs // 无新字节
	}

	// 上一轮把尾段计过行：新字节可能是该行的续写 → 先回退到完整行边界，
	// 再从回退位起读（顺序不可反——否则尾段字节不在本轮 buf 里，行会丢）。
	if fs.parsed > fs.lineEnd {
		fs.entries = fs.entries[:fs.nAtTail]
		fs.parsed = fs.lineEnd
	}

	// 增量读 [parsed, size)。读与 stat 之间文件可能又长——按实际读到为准
	// （下一轮刷新自然跟进）。
	buf := make([]byte, size-fs.parsed)
	fh, err := os.Open(path)
	if err != nil {
		return fs // 打不开：保留现状，下轮再试（与旧 readFile 静默跳过同纪律）
	}
	n, _ := fh.ReadAt(buf, fs.parsed)
	_ = fh.Close()
	buf = buf[:n]
	if len(buf) == 0 {
		return fs
	}

	baseParsed := fs.parsed
	lastNL := bytes.LastIndexByte(buf, '\n')
	var complete, tail []byte
	if lastNL >= 0 {
		complete, tail = buf[:lastNL+1], buf[lastNL+1:]
	} else {
		tail = buf
	}
	for len(complete) > 0 {
		i := bytes.IndexByte(complete, '\n')
		fs.lineNo++
		if e := a.parseLine(base, fs.lineNo, complete[:i]); e != nil {
			fs.entries = append(fs.entries, e)
		}
		complete = complete[i+1:]
	}
	if lastNL >= 0 {
		fs.lineEnd = baseParsed + int64(lastNL) + 1
	}
	if len(tail) > 0 { // 尾段计行（旧 splitlines 语义）+ 重解锚
		fs.nAtTail = len(fs.entries)
		fs.lineNo++
		if e := a.parseLine(base, fs.lineNo, tail); e != nil {
			fs.entries = append(fs.entries, e)
		}
	} else {
		fs.nAtTail = len(fs.entries)
	}
	fs.parsed = baseParsed + int64(len(buf))
	fs.tail = readTailSample(path, fs.parsed)
	return fs
}

// readTailSample 读 [max(0, parsed-N), parsed) 的尾样字节；读失败返回 nil
// （下一轮触发整文件重解——保守侧）。
func readTailSample(path string, parsed int64) []byte {
	n := int64(tailSampleN)
	if parsed < n {
		n = parsed
	}
	if n == 0 {
		return nil
	}
	buf := make([]byte, n)
	fh, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer fh.Close()
	if _, err := fh.ReadAt(buf, parsed-n); err != nil {
		return nil
	}
	return buf
}

// parseLine 单行解析（空行/坏行返回 nil；坏行 stderr 告警一次）+ 驻留。
func (a *Accounts) parseLine(base string, n int, line []byte) map[string]any {
	if len(bytes.TrimSpace(line)) == 0 { // Python: not line.strip()
		return nil
	}
	var e map[string]any
	if err := json.Unmarshal(line, &e); err != nil || e == nil {
		// 告警走 stderr——read() 的调用方（report --json）把 stdout 当
		// 机器可解析载荷，告警混入会撕裂输出（终审#1 原纪律）。
		fmt.Fprintf(os.Stderr, "[accounts] 跳过损坏行 %s:%d\n", base, n)
		return nil
	}
	return a.internEntry(e)
}

// filterEntries 六维过滤（旧 handleLine 的过滤段原样，解析已提前）。
func filterEntries(entries []map[string]any, o ReadOpts, out *[]map[string]any) {
	for _, e := range entries {
		ts := numOr(e, "ts") // Python: e.get("ts", 0)；非数值落 0（防御）
		if o.Since != 0 && ts < o.Since {
			continue
		}
		if o.Until != 0 && ts > o.Until {
			continue
		}
		if o.Project != "" && strOr(e, "project") != o.Project {
			continue
		}
		if o.Session != "" && strOr(e, "session_id") != o.Session {
			continue
		}
		if o.Lineage != "" && strOr(e, "lineage_id") != o.Lineage {
			continue
		}
		if o.Kind != "" && strOr(e, "kind") != o.Kind {
			continue
		}
		*out = append(*out, e)
	}
}

// Prewarm 预热：把目录下全部 *.jsonl 解析进缓存（serve 启动后 goroutine
// 调用——把首次全量解析的秒级成本从首个用户请求挪到启动期）。
func (a *Accounts) Prewarm() {
	entries, err := os.ReadDir(a.dir)
	if err != nil {
		return
	}
	a.cmu.Lock()
	defer a.cmu.Unlock()
	a.initCacheLocked()
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".jsonl") {
			continue
		}
		a.refresh(filepath.Join(a.dir, de.Name()), de.Name())
	}
}

// ---- 字符串驻留（内存面：19.5 万行 × ~16 键的键字符串是纯重复开销） ----

// internTableCap 值驻留表封顶：满了只查不增（防近唯一字符串灌表）。
const internTableCap = 8192

// internValueKeys 值驻留白名单：高重复字段才驻留；ts_iso/title/err/
// transcript_path 等近唯一字段不进（表会被一次性字符串灌满失去意义）。
var internValueKeys = map[string]bool{
	"kind": true, "agent": true, "session_id": true, "lineage_id": true,
	"project": true, "provider": true, "model": true, "price_ver": true,
	"outcome": true, "lane": true, "reason": true, "upstream": true,
	"status": true, "mode": true, "close_reason": true, "source": true,
}

// internEntry 键全量驻留 + 白名单值驻留，返回重建行（内容等价、字符串
// 共享——strings 不可变，共享只读安全）。
func (a *Accounts) internEntry(e map[string]any) map[string]any {
	out := make(map[string]any, len(e))
	for k, v := range e {
		ik := a.internKey(k)
		if s, ok := v.(string); ok && internValueKeys[ik] {
			v = a.internVal(s)
		}
		out[ik] = v
	}
	return out
}

func (a *Accounts) internKey(s string) string { // 键表不封顶（域内字段有限）
	if v, ok := a.keys[s]; ok {
		return v
	}
	a.keys[s] = s
	return s
}

func (a *Accounts) internVal(s string) string { // 值表封顶：满了只查不增
	if v, ok := a.vals[s]; ok {
		return v
	}
	if len(a.vals) >= internTableCap {
		return s
	}
	a.vals[s] = s
	return s
}
