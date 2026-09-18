// Package store 交接库（store）：handoffs/ + index.json v1 —— 唯一权威源，原子写
// （规格 ferryman/store.py 1:1）。
//
// DESIGN §6.15：{handoffs: [{handoff_id, session_id, agent, cwd, title,
// created_at, covers_until, status: fresh|skeleton|stale|pending, path,
// blocked_at, injected: []}], pending_prompts: [...]}；同 session 新交接覆盖；
// 30 天归档（TODO：定期清理任务）。
//
// 对齐纪律：
//   - Entry/PendingPrompt 的 json tag 显式钉死 snake_case——直接序列化不得输出
//     CamelCase；*string nil ≡ Python None（blocked_at/consumed_by）；
//   - 落盘 json.dumps(ensure_ascii=False, indent=2) 等价：SetEscapeHTML(false)
//     非 ASCII 直出、两空格缩进、无尾随换行；
//   - _atomic_write = 同后缀 .tmp + os.replace ≡ tmp + os.Rename；
//   - 并发：mu 覆盖所有 index 读写与 flush（Python threading.Lock 同构）；
//   - 时间：id/created_at/blocked_at 用本地时区；epoch 统一经 clock.Now
//     （float64 UTC epoch 秒，测试可注入）。
//
// Python 侧磁盘写失败会异常上抛；本包公共方法签名无 error，落盘失败 panic
// （同 viewer/demo「不可达」先例，正常盘况不可达）。
package store

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"ferryman/internal/clock"
	"ferryman/internal/extract"
	"ferryman/internal/mathx"
)

const (
	// FreshWindowS 归还注入新鲜度（DESIGN §6.9，可配留 TODO）。
	FreshWindowS = 86400.0
	// CoversToleranceS covers_until 与 last_write 的容差（transcript 异步落盘）。
	CoversToleranceS = 60.0

	idTimeFmt   = "20060102_150405"     // handoff_id 前缀（本地时区）
	dispTimeFmt = "2006-01-02 15:04:05" // created_at/blocked_at（本地时区）

	// pendingPromptCap save_pending_prompt 的 cap 默认参数（Python 局部导入
	// extract.token_estimate 防环；Go 包依赖无环，直接用）。
	pendingPromptCap = 500
	// pendingTruncMark 截断尾标（store.py 逐字，与 extract 的截断尾标不同形）。
	pendingTruncMark = "…(超长截断)"
)

// Entry 交接索引条目。json tag 显式 snake_case（直接序列化不得输出 CamelCase）。
type Entry struct {
	HandoffID    string   `json:"handoff_id"`
	SessionID    string   `json:"session_id"`
	Agent        string   `json:"agent"`
	Cwd          string   `json:"cwd"`
	Title        string   `json:"title"`
	CreatedAt    string   `json:"created_at"`
	CoversUntil  string   `json:"covers_until"`
	CoversUntilS float64  `json:"covers_until_s"`
	Status       string   `json:"status"`
	Path         string   `json:"path"`
	BlockedAt    *string  `json:"blocked_at"` // nil ≡ Python None
	Injected     []string `json:"injected"`
}

// PendingPrompt 待续 prompt（单源：index 内嵌，DESIGN §6.7）。
type PendingPrompt struct {
	SessionID  string  `json:"session_id"`
	Prompt     string  `json:"prompt"`
	BlockedAt  string  `json:"blocked_at"`
	ConsumedBy *string `json:"consumed_by"` // nil ≡ Python None
}

// indexFile index.json 的内存形（Python dict {"handoffs": [], "pending_prompts": []}）。
type indexFile struct {
	Handoffs       []Entry         `json:"handoffs"`
	PendingPrompts []PendingPrompt `json:"pending_prompts"`
}

// Store 交接库。
type Store struct {
	mu        sync.Mutex
	dir       string
	indexPath string
	index     indexFile
}

// New 建 handoffs/ 目录并加载 index.json；坏/缺 → 空索引。
func New(dataDir string) (*Store, error) {
	dir := filepath.Join(dataDir, "handoffs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		dir:       dir,
		indexPath: filepath.Join(dataDir, "index.json"),
		index:     indexFile{Handoffs: []Entry{}, PendingPrompts: []PendingPrompt{}},
	}
	s.load()
	return s, nil
}

// load Python _load：读 index.json；缺失/坏 JSON（ValueError/OSError）→ 空索引。
// 差异钉住：Python 容忍「合法 JSON 但非索引形」的脏数据（后续访问才炸）；
// Go 反序列化进结构体，缺键即空切片，天然更稳。
func (s *Store) load() {
	data, err := os.ReadFile(s.indexPath)
	if err != nil {
		return
	}
	var idx indexFile
	if err := json.Unmarshal(data, &idx); err != nil {
		return
	}
	if idx.Handoffs == nil {
		idx.Handoffs = []Entry{}
	}
	if idx.PendingPrompts == nil {
		idx.PendingPrompts = []PendingPrompt{}
	}
	s.index = idx
}

// flush Python _flush：index 原子落盘（indent 2、非 ASCII 直出）。
func (s *Store) flush() {
	if err := atomicWrite(s.indexPath, marshalIndent(s.index)); err != nil {
		panic("store: index.json 落盘失败（Python 同路径异常上抛）: " + err.Error())
	}
}

// atomicWrite Python _atomic_write：同后缀 .tmp + os.replace（Go os.Rename 在
// Windows 亦为替换语义）。
func atomicWrite(path, text string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// marshalIndent json.dumps(index, ensure_ascii=False, indent=2) 等价：
// 非 ASCII 直出、不转义 <>&、两空格缩进、裁掉 Encoder 尾随换行
// （纪律同 accounts.compactJSON）。
func marshalIndent(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		panic("store: index 序列化不可达失败: " + err.Error()) // 全为可序列化标量
	}
	return strings.TrimRight(buf.String(), "\n")
}

// parseISOUTC Python _parse_iso_utc：ISO 串（带 Z 或 ±00:00）→ epoch float；
// 坏/空 → 0.0。Python .replace("Z", "+00:00") 后 fromisoformat——Go RFC3339
// 直接吃两种后缀（含可选小数秒）；naive（无时区）Python .timestamp() 按本地，
// 同构回落本地解析。
func parseISOUTC(s string) float64 {
	if s == "" {
		return 0.0
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return float64(t.UnixNano()) / 1e9
	}
	for _, layout := range []string{"2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return float64(t.UnixNano()) / 1e9
		}
	}
	return 0.0
}

// resolvePath Python str(Path(cwd).resolve()) 的 Go 形：Abs + EvalSymlinks 尽力
// （路径可不存在——Python resolve(strict=False) 照常归一，EvalSymlinks 此时
// 报错，回落 Clean(Abs)；测试临时目录无符号链接，两形等价）。
func resolvePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return filepath.Clean(abs)
}

// randHex6 uuid.uuid4().hex[:6] 等价：3 字节随机源的 6 位 hex。
func randHex6() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		panic("store: 随机源不可达失败: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// ---------- 摆渡写入 ----------

// SaveHandoff 写入交接 MD + index（同 session+agent 覆盖旧记录）。返回 index 条目。
func (s *Store) SaveHandoff(sessionID, agent, cwd, title, coversUntilISO, status, handoffMD string) Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	hid := time.Now().Format(idTimeFmt) + "_" + randHex6()
	fname := hid + ".md"
	mdPath := filepath.Join(s.dir, fname)
	if err := os.WriteFile(mdPath, []byte(handoffMD), 0o644); err != nil {
		panic("store: 交接 MD 写入失败（Python 同路径异常上抛）: " + err.Error())
	}
	cwdNorm := ""
	if cwd != "" { // Python str(Path(cwd).resolve()) if cwd else ""
		cwdNorm = resolvePath(cwd)
	}
	entry := Entry{
		HandoffID:    hid,
		SessionID:    sessionID,
		Agent:        agent,
		Cwd:          cwdNorm,
		Title:        title, // Python title or ""；Go "" 即 None
		CreatedAt:    time.Now().Format(dispTimeFmt),
		CoversUntil:  coversUntilISO, // Python covers_until_iso or ""
		CoversUntilS: parseISOUTC(coversUntilISO),
		Status:       status,
		Path:         mdPath,
		BlockedAt:    nil,
		Injected:     []string{},
	}
	kept := make([]Entry, 0, len(s.index.Handoffs)+1)
	for _, e := range s.index.Handoffs {
		if !(e.SessionID == sessionID && e.Agent == agent) {
			kept = append(kept, e)
		}
	}
	kept = append(kept, entry)
	s.index.Handoffs = kept
	s.flush()
	return entry
}

// ---------- 闸门查询 ----------

// ValidHandoff DESIGN §6.10-5：同 agent+cwd、status∈{fresh,skeleton}、
// covers_until ≥ last_write（含 60s 容差）、在新鲜度窗口内；取 covers 最大。
// 命中返回条目副本（Python 返回共享 dict，Go 副本防跨锁读改写 -race）。
func (s *Store) ValidHandoff(agent, cwd string, lastWrite float64) *Entry {
	if cwd == "" {
		return nil
	}
	norm := strings.ToLower(resolvePath(cwd))
	now := clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *Entry
	for i := range s.index.Handoffs {
		e := &s.index.Handoffs[i]
		if e.Agent != agent || (e.Status != "fresh" && e.Status != "skeleton") {
			continue
		}
		if strings.ToLower(e.Cwd) != norm {
			continue
		}
		if now-e.CoversUntilS > FreshWindowS {
			continue
		}
		if e.CoversUntilS+CoversToleranceS < lastWrite {
			continue
		}
		if best == nil || e.CoversUntilS > best.CoversUntilS {
			best = e
		}
	}
	if best == nil {
		return nil
	}
	cp := *best
	cp.Injected = slices.Clone(best.Injected)
	return &cp
}

// MarkBlocked 按条目标记阻塞时刻（本地时间串）；未命中也照常 flush（Python 同）。
func (s *Store) MarkBlocked(handoffID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	blockedAt := time.Now().Format(dispTimeFmt)
	for i := range s.index.Handoffs {
		if s.index.Handoffs[i].HandoffID == handoffID {
			ba := blockedAt
			s.index.Handoffs[i].BlockedAt = &ba
		}
	}
	s.flush()
}

// ---------- 待续 prompt（单源：index 内嵌，DESIGN §6.7） ----------

// SavePendingPrompt 覆盖同 session 旧条目；超 500 token 截断加尾标。
func (s *Store) SavePendingPrompt(sessionID, prompt string) {
	if extract.TokenEstimate(prompt) > pendingPromptCap {
		prompt = mathx.RuneTrunc(prompt, pendingPromptCap) + pendingTruncMark
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := make([]PendingPrompt, 0, len(s.index.PendingPrompts)+1)
	for _, p := range s.index.PendingPrompts {
		if p.SessionID != sessionID {
			kept = append(kept, p)
		}
	}
	kept = append(kept, PendingPrompt{
		SessionID:  sessionID,
		Prompt:     prompt,
		BlockedAt:  time.Now().Format(dispTimeFmt),
		ConsumedBy: nil,
	})
	s.index.PendingPrompts = kept
	s.flush()
}

// PopPendingPrompt 取未消费的待续 prompt；consumeFor 非空时同时标记消费
// （注入一次后不再给）。返回 "" 表示无（Python None）。
func (s *Store) PopPendingPrompt(sessionID, consumeFor string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.index.PendingPrompts {
		p := &s.index.PendingPrompts[i]
		if p.SessionID == sessionID && p.ConsumedBy == nil {
			prompt := p.Prompt
			if consumeFor != "" {
				p.ConsumedBy = &consumeFor
				s.flush()
			}
			return prompt
		}
	}
	return ""
}

// ---------- 归还 ----------

// RestoreCandidates DESIGN §6.9：agent+cwd 双键过滤，covers 降序（最新在前），
// 24h 新鲜窗内且 status∈{fresh,skeleton}。
func (s *Store) RestoreCandidates(agent, cwd string) []Entry {
	if cwd == "" {
		return []Entry{}
	}
	norm := strings.ToLower(resolvePath(cwd))
	now := clock.Now()
	s.mu.Lock()
	cands := []Entry{}
	for _, e := range s.index.Handoffs {
		if e.Agent == agent && strings.ToLower(e.Cwd) == norm &&
			e.CoversUntilS > now-FreshWindowS &&
			(e.Status == "fresh" || e.Status == "skeleton") {
			cands = append(cands, e)
		}
	}
	s.mu.Unlock()
	// Python sort(key=-covers) Timsort 稳定；同 covers 保持原序（SliceStable）。
	sort.SliceStable(cands, func(i, j int) bool {
		return cands[i].CoversUntilS > cands[j].CoversUntilS
	})
	return cands
}

// MarkInjected 注入去重追加；未命中也照常 flush（Python 同）。
func (s *Store) MarkInjected(handoffID, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.index.Handoffs {
		e := &s.index.Handoffs[i]
		if e.HandoffID == handoffID && !slices.Contains(e.Injected, sessionID) {
			e.Injected = append(e.Injected, sessionID)
		}
	}
	s.flush()
}

// ReadHandoff 读条目正文；读失败（OSError；Python 另捕 KeyError，Go 结构体
// 恒有 Path 字段）→ ""。
func (s *Store) ReadHandoff(e Entry) string {
	data, err := os.ReadFile(e.Path)
	if err != nil {
		return ""
	}
	return string(data)
}
