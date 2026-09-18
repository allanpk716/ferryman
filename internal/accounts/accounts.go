// Package accounts 账本：accounts/YYYYMM.jsonl append-only 流水（ADR-0002，设计 §1.2/§1.3）。
//
// 铁律（Python ferryman/accounts.py docstring 逐字搬运）：
//   - 只记元数据与金额（字段白名单），永不落消息内容——隐私不变量；
//   - append-only，report 只读不改写；节省在 report 层由版本化公式重算；
//   - 按月滚动（本地时区）；每行带 schema 版本 v。
package accounts

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"ferryman/internal/clock"
	"ferryman/internal/mathx"
)

// SchemaV 每行带的 schema 版本。
const SchemaV = 1

// kindFields 十科目字段白名单（accounts.py:20-45 逐字照抄）。
var kindFields = map[string][]string{
	// 摆渡的每次模型调用（含分块/重试/失败）
	"handoff": {"provider", "model", "price_ver", "prompt_tokens",
		"completion_tokens", "outcome", "wall_s"},
	// 心跳（T41 占坑，T51 票03 起有生产者）：三态 outcome ∈ hit|miss|error
	// + observe（演练跳未真发）。T41 的 hit 布尔被三态取代（当时无生产者）。
	"beat": {"provider", "model", "price_ver", "prefix_tokens", "cache_read",
		"outcome", "cost_pred", "cost_actual"},
	"block":  {"prefix_tokens", "idle_s"},
	"inject": {"tokens", "handoff_id"},
	"bypass": {"prefix_tokens"},
	"window": {"opened_ts", "closed_ts", "dur_s", "prefix_tokens", "close_reason"},
	// 问询守望事件（T51 票04，spec 决策 8）：命中/开窗/关窗走本通道（每跳
	// 复用 beat 科目）。命中行带复核证据形态（unit_count＋breakdown 三桶
	// ＋转录绝对路径；session_id/命中时间是公共字段）——只记元数据与计数，
	// 消息正文永不入账（隐私铁律）。
	"qwatch_hit": {"unit_count", "marker_lines", "qmark_lines",
		"numbered_lines", "transcript_path"},
	"qwatch_open": {"unit_count", "prefix_tokens"},
	"qwatch_close": {"opened_ts", "closed_ts", "dur_s", "beats_fired",
		"close_reason"},
	// 逐次请求的用量遥测（设计 §3.7；会话文件 30 天清理后的审计地基）
	"usage": {"model", "title", "input_tokens", "cache_read_tokens",
		"cache_creation_tokens", "output_tokens", "offset"},
}

// kindOrder 科目顺序 = Python dict 插入序（KINDS 元组），未知科目报错文案用。
var kindOrder = []string{"handoff", "beat", "block", "inject", "bypass", "window",
	"qwatch_hit", "qwatch_open", "qwatch_close", "usage"}

// commonFields 公共字段（模块盖章；白名单校验不拒，但不随传入 Fields 覆盖）。
var commonFields = map[string]bool{
	"ts": true, "ts_iso": true, "kind": true, "v": true,
	"agent": true, "session_id": true, "lineage_id": true, "project": true,
}

// commonKeyOrder 落盘行公共八字段定序（票面验收：v,kind,ts,ts_iso,agent,session_id,lineage_id,project）。
var commonKeyOrder = []string{"v", "kind", "ts", "ts_iso", "agent", "session_id", "lineage_id", "project"}

// Fields 单条流水的科目字段集；值仅 string/float64/int/nil。
type Fields map[string]any

// Accounts append-only 账本；Record 走 mu 互斥。
type Accounts struct {
	dir string
	mu  sync.Mutex
}

// New 建 <dataDir>/accounts/ 目录（parents+exist_ok）。
func New(dataDir string) (*Accounts, error) {
	dir := filepath.Join(dataDir, "accounts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Accounts{dir: dir}, nil
}

// Record 记一条流水并落盘，返回完整行。
// ts<0 → clock.Now()（映射 Python ts=None → time.time()）。
// 校验三连顺序与 Python 一致：未知科目 → 白名单 → 缺必填 → 保留字。
// 落盘行键序：公共八字段定序 + 科目字段字母序（确定性序列化；Python 为
// 调用点 kwargs 序，Go 版取字典序——语义对齐 JSON 解析等价）。
func (a *Accounts) Record(kind string, ts float64, f Fields) (map[string]any, error) {
	allowed, ok := kindFields[kind]
	if !ok {
		return nil, fmt.Errorf("未知科目: '%s'（可选 %s）", kind, pyTuple(kindOrder))
	}
	var bad []string
	for k := range f {
		if !commonFields[k] && !contains(allowed, k) {
			bad = append(bad, k)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return nil, fmt.Errorf("账本不落这些字段（隐私不变量）: %s", pyList(bad))
	}
	var missing []string
	for _, k := range allowed {
		if _, present := f[k]; !present {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("%s 缺必填字段: %s", kind, pyList(missing))
	}
	var reserved []string
	for _, k := range []string{"v", "ts_iso"} {
		if _, present := f[k]; present {
			reserved = append(reserved, k)
		}
	}
	if len(reserved) > 0 {
		sort.Strings(reserved)
		return nil, fmt.Errorf("保留字段由模块盖章，不可传入: %s", pyList(reserved))
	}
	if ts < 0 {
		ts = clock.Now()
	}
	entry := map[string]any{
		"v":          SchemaV,
		"kind":       kind,
		"ts":         mathx.Round(ts, 3),
		"ts_iso":     time.Unix(int64(ts), 0).Format("2006-01-02T15:04:05-0700"),
		"agent":      fieldOr(f, "agent"),
		"session_id": fieldOr(f, "session_id"),
		"lineage_id": fieldOr(f, "lineage_id"),
		"project":    fieldOr(f, "project"),
	}
	// 科目字段按字母序拼入；公共章不随 f 覆盖（Python 具名参数绑定使
	// ts/kind 不可能进 fields；Go 无 kwargs，落此防御）。
	keys := make([]string, 0, len(f))
	for k := range f {
		if !commonFields[k] {
			entry[k] = f[k]
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	line, err := marshalLine(entry, keys)
	if err != nil {
		return nil, err
	}
	fname := time.Unix(int64(ts), 0).Format("200601") + ".jsonl"
	a.mu.Lock()
	defer a.mu.Unlock()
	fh, err := os.OpenFile(filepath.Join(a.dir, fname),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	if _, err := fh.Write(line); err != nil {
		return nil, err
	}
	return entry, nil
}

// ReadOpts 六维过滤；零值=不过滤（映射 Python None）。
type ReadOpts struct {
	Since, Until                    float64
	Project, Session, Lineage, Kind string
}

// Read 遍历目录 sorted *.jsonl；坏行跳过 + stderr 告警（stdout 保持机器可解析）。
// Read 不持 mu（Python read 亦无锁）。
func (a *Accounts) Read(o ReadOpts) []map[string]any {
	out := []map[string]any{}
	entries, err := os.ReadDir(a.dir)
	if err != nil {
		return out
	}
	for _, de := range entries { // ReadDir 已按文件名排序（= Python sorted(glob)）
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".jsonl") {
			continue
		}
		a.readFile(filepath.Join(a.dir, de.Name()), de.Name(), o, &out)
	}
	return out
}

func (a *Accounts) readFile(path, base string, o ReadOpts, out *[]map[string]any) {
	fh, err := os.Open(path)
	if err != nil {
		return
	}
	defer fh.Close()
	// jsonl 行读无上限：ReadBytes 循环（spec §I/O，禁 Scanner 行上限）。
	// splitlines 语义：按 \n 切，行号 1 起含空行；文件尾无换行时末段仍计一行。
	r := bufio.NewReader(fh)
	for i := 1; ; i++ {
		raw, err := r.ReadBytes('\n')
		if len(raw) > 0 {
			a.handleLine(base, i, bytes.TrimSuffix(raw, []byte("\n")), o, out)
		}
		if err != nil {
			break
		}
	}
}

func (a *Accounts) handleLine(base string, n int, line []byte, o ReadOpts, out *[]map[string]any) {
	if len(bytes.TrimSpace(line)) == 0 { // Python: not line.strip()
		return
	}
	var e map[string]any
	if err := json.Unmarshal(line, &e); err != nil || e == nil {
		// 终审#1：告警走 stderr——read() 的调用方（report --json）
		// 把 stdout 当机器可解析载荷，告警混入会撕裂输出。
		fmt.Fprintf(os.Stderr, "[accounts] 跳过损坏行 %s:%d\n", base, n)
		return
	}
	ts := numOr(e, "ts") // Python: e.get("ts", 0)；非数值落 0（防御）
	if o.Since != 0 && ts < o.Since {
		return
	}
	if o.Until != 0 && ts > o.Until {
		return
	}
	if o.Project != "" && strOr(e, "project") != o.Project {
		return
	}
	if o.Session != "" && strOr(e, "session_id") != o.Session {
		return
	}
	if o.Lineage != "" && strOr(e, "lineage_id") != o.Lineage {
		return
	}
	if o.Kind != "" && strOr(e, "kind") != o.Kind {
		return
	}
	*out = append(*out, e)
}

// marshalLine 有序拼接单行 JSON：公共八字段定序 + extraKeys（已字母序）。
// compactJSON 关 HTML 转义 = json.dumps(ensure_ascii=False)：非 ASCII 直出。
func marshalLine(entry map[string]any, extraKeys []string) ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	writePair := func(k string) error {
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		kb, err := compactJSON(k)
		if err != nil {
			return err
		}
		b.Write(kb)
		b.WriteByte(':')
		vb, err := compactJSON(entry[k])
		if err != nil {
			return err
		}
		b.Write(vb)
		return nil
	}
	for _, k := range commonKeyOrder {
		if err := writePair(k); err != nil {
			return nil, err
		}
	}
	for _, k := range extraKeys {
		if err := writePair(k); err != nil {
			return nil, err
		}
	}
	b.WriteString("}\n")
	return b.Bytes(), nil
}

// compactJSON 单值 JSON 编码：SetEscapeHTML(false) 对齐 json.dumps——
// 非 ASCII 直出，不转义 <>&；Encoder 尾随换行裁掉。
func compactJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}

func fieldOr(f Fields, k string) any {
	if v, present := f[k]; present {
		return v
	}
	return ""
}

func numOr(e map[string]any, k string) float64 {
	if v, ok := e[k].(float64); ok {
		return v
	}
	return 0
}

func strOr(e map[string]any, k string) string {
	if v, ok := e[k].(string); ok {
		return v
	}
	return ""
}

// pyList / pyTuple 渲染 Python repr 风格字面量（['a', 'b'] / ('a', 'b')），
// 报错文案与 Python 版逐字对齐。
func pyList(xs []string) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = "'" + x + "'"
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func pyTuple(xs []string) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = "'" + x + "'"
	}
	return "(" + strings.Join(parts, ", ") + ")"
}
