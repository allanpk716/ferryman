// Package ledger 读取 Ferryman 账本（accounts/YYYYMM.jsonl 流水，事实源
// ferryman/accounts.py——Entry 的 json tag 与其 _COMMON/各 kind 字段逐键一致）。
package ledger

import (
	"bufio"
	"encoding/json"
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

	// handoff / beat / block / inject / bypass
	Provider         string  `json:"provider"`
	Outcome          string  `json:"outcome"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	WallS            float64 `json:"wall_s"`
	Tokens           int64   `json:"tokens"` // inject
	IdleS            float64 `json:"idle_s"` // block
	Hit              bool    `json:"hit"`    // beat
	CostPred         float64 `json:"cost_pred"`
	CostActual       float64 `json:"cost_actual"`
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
// 空行静默跳过；损坏行 log 后跳过不中断（与 accounts.py read 同策略）。
// 任一文件不可读时返回错误。
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
		f, err := os.Open(filepath.Join(dir, de.Name()))
		if err != nil {
			return nil, err
		}
		// 单行上限 1 MiB：默认 Scanner 缓冲 64 KiB，长 title 的 usage 行可能超
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		ln := 0
		for sc.Scan() {
			ln++
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var e Entry
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				log.Printf("[ledger] 跳过损坏行 %s:%d: %v", de.Name(), ln, err)
				continue
			}
			entries = append(entries, e)
		}
		if err := sc.Err(); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	}
	return entries, nil
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
