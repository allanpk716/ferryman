// Package ledger 读取 Ferryman 账本（accounts/YYYYMM.jsonl 流水，事实源
// ferryman/accounts.py——Entry 的 json tag 与其 _COMMON/各 kind 字段逐键一致）。
package ledger

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Entry 是账本一行（扁平结构：字段按 kind 缺省零值）。
type Entry struct {
	V         int     `json:"v"`
	Kind      string  `json:"kind"`
	Ts        float64 `json:"ts"`
	TsISO     string  `json:"ts_iso"`
	Agent     string  `json:"agent"`
	SessionID string  `json:"session_id"`
	LineageID string  `json:"lineage_id"`
	Project   string  `json:"project"`

	// usage
	Model               string `json:"model"`
	Title               string `json:"title"`
	InputTokens         int64  `json:"input_tokens"`
	CacheReadTokens     int64  `json:"cache_read_tokens"`
	CacheCreationTokens int64  `json:"cache_creation_tokens"`
	OutputTokens        int64  `json:"output_tokens"`
	Offset              int64  `json:"offset"`

	// window
	OpenedTS     float64 `json:"opened_ts"`
	ClosedTS     float64 `json:"closed_ts"`
	DurS         float64 `json:"dur_s"`
	PrefixTokens int64   `json:"prefix_tokens"`
	CloseReason  string  `json:"close_reason"`

	// handoff / beat / block / inject / bypass / qwatch_*
	Provider         string  `json:"provider"`
	Outcome          string  `json:"outcome"`    // handoff: fresh|skeleton|failed · beat: hit|miss|error|observe（T51 票03 起，旧 hit 布尔已废）
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	WallS            float64 `json:"wall_s"`
	PriceVer         string  `json:"price_ver"`  // handoff / beat
	CacheRead        int64   `json:"cache_read"` // beat（注意与 usage 的 cache_read_tokens 是两个键）
	Tokens           int64   `json:"tokens"`     // inject
	HandoffID        string  `json:"handoff_id"` // inject
	IdleS            float64 `json:"idle_s"`     // block
	CostPred         float64 `json:"cost_pred"`
	CostActual       float64 `json:"cost_actual"`

	// qwatch_hit / qwatch_open / qwatch_close（T51 票04 问询守望事件）
	UnitCount     int    `json:"unit_count"`
	MarkerLines   int    `json:"marker_lines"`
	QmarkLines    int    `json:"qmark_lines"`
	NumberedLines int    `json:"numbered_lines"`
	Transcript    string `json:"transcript_path"`
	BeatsFired    int    `json:"beats_fired"`
}

// SessionSummary 是列表页一行。
type SessionSummary struct {
	LineageID string  `json:"lineage_id"`
	Title     string  `json:"title"`
	Project   string  `json:"project"`
	Agents    string  `json:"agents"` // 去重后逗号连缀（"cc" / "cc,codex"）
	FirstTS   float64 `json:"first_ts"`
	LastTS    float64 `json:"last_ts"`
	Input     int64   `json:"input"`
	CacheRead int64   `json:"cache_read"`
	Creation  int64   `json:"cache_creation"`
	Output    int64   `json:"output"`
	Requests  int     `json:"requests"` // usage 行数
	Windows   int     `json:"windows"`
	Beats     int     `json:"beats"`
	Handoffs  int     `json:"handoffs"`
	Blocks    int     `json:"blocks"`
}

// Load 读 dir 下按文件名排序的全部 *.jsonl，逐行解码为 Entry。
// 空行静默跳过；损坏行、超长行 log 后跳过不中断（对齐 accounts.py read 的容错并更进一步——
// 单条病态行不允许拖垮整个 Load）。文件打不开时返回错误。
func Load(dir string) ([]Entry, error) {
	names, err := os.ReadDir(dir) // os.ReadDir 保证按文件名排序
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, de := range names {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".jsonl") {
			continue
		}
		if err := loadFile(filepath.Join(dir, de.Name()), de.Name(), &entries); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

// maxLineBytes 之上的行按超长行告警跳过。正常账本行 ~300B，这是对病态输入的容错：
// 超限不中止 Load，只丢弃该行（内存有界，绝不整行读入）。
const maxLineBytes = 1 << 20

func loadFile(path, name string, out *[]Entry) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64*1024)
	ln := 0
	for {
		var line []byte
		overflow := false
		for {
			chunk, err := r.ReadSlice('\n')
			if !overflow {
				if len(line)+len(chunk) > maxLineBytes {
					overflow, line = true, nil // 超限：不再积攒，只继续排空到行尾
				} else {
					line = append(line, chunk...)
				}
			}
			if err == bufio.ErrBufferFull {
				continue // 行未终结，继续排空
			}
			ln++
			trimmed := bytes.TrimSpace(line)
			switch {
			case overflow:
				log.Printf("[ledger] 跳过超长行 %s:%d（>%d 字节）", name, ln, maxLineBytes)
			case len(trimmed) == 0:
				// 空行静默跳过（对齐 accounts.py read）
			default:
				var e Entry
				if uerr := json.Unmarshal(trimmed, &e); uerr != nil {
					log.Printf("[ledger] 跳过损坏行 %s:%d: %v", name, ln, uerr)
				} else {
					*out = append(*out, e)
				}
			}
			if err == nil {
				break // 本行已终结，读下一行
			}
			if errors.Is(err, io.EOF) {
				return nil // 文件尾（含无换行的最后一行，已在上面积攒处理）
			}
			// 底层读错误无法再取得进展：放弃本文件但不拖垮整个 Load
			log.Printf("[ledger] 读取中断 %s:%d: %v", name, ln, err)
			return nil
		}
	}
}

// agg 是 Summarize 的内部聚合结构。
type agg struct {
	sum       SessionSummary
	agents    []string // 首次出现序
	agentSeen map[string]bool
}

func (a *agg) addAgent(name string) {
	if name == "" || a.agentSeen[name] {
		return
	}
	a.agentSeen[name] = true
	a.agents = append(a.agents, name)
}

// Summarize 把账本行按 lineage_id 聚合成列表页行：
// usage 行累加四类 token；Title 取最后一个非空；FirstTS/LastTS 取全 kind 最小/最大 ts。
// 输出按 LastTS 倒序（同 ts 按 lineage_id 升序保证稳定）。
func Summarize(entries []Entry) []SessionSummary {
	byLin := map[string]*agg{}
	for _, e := range entries {
		a := byLin[e.LineageID]
		if a == nil {
			// 空 lineage_id 聚合进 "" 键是有意容错：账本当前所有 kind 都带 lineage，
			// 但缺省键也不丢数据（宁可在列表页看到一行空 lineage，也不静默丢弃）。
			a = &agg{agentSeen: map[string]bool{}}
			a.sum.LineageID = e.LineageID
			a.sum.FirstTS = e.Ts
			a.sum.LastTS = e.Ts
			byLin[e.LineageID] = a
		}
		if e.Ts < a.sum.FirstTS {
			a.sum.FirstTS = e.Ts
		}
		if e.Ts > a.sum.LastTS {
			a.sum.LastTS = e.Ts
		}
		if e.Project != "" {
			a.sum.Project = e.Project
		}
		a.addAgent(e.Agent)
		switch e.Kind {
		case "usage":
			a.sum.Input += e.InputTokens
			a.sum.CacheRead += e.CacheReadTokens
			a.sum.Creation += e.CacheCreationTokens
			a.sum.Output += e.OutputTokens
			a.sum.Requests++
			if e.Title != "" {
				a.sum.Title = e.Title
			}
		case "window":
			a.sum.Windows++
		case "beat":
			a.sum.Beats++
		case "handoff":
			a.sum.Handoffs++
		case "block":
			a.sum.Blocks++
		}
	}
	out := make([]SessionSummary, 0, len(byLin))
	for _, a := range byLin {
		a.sum.Agents = strings.Join(a.agents, ",")
		out = append(out, a.sum)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastTS != out[j].LastTS {
			return out[i].LastTS > out[j].LastTS
		}
		return out[i].LineageID < out[j].LineageID
	})
	return out
}
