// Package server 提供 T43 查看器的只读 HTTP 服务：账本 JSON API + 反跑仿真端点。
// 只读铁律：本包对数据目录只有读操作（ledger.Load 幂等只读，每次请求现读不缓存），
// 绝不向数据目录写入任何文件。
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"time"

	"ferryman/viewer/internal/ledger"
	"ferryman/viewer/internal/policy"
)

// Server 持有数据目录；handler 内现读账本（6.6 万行量级解析 ~百毫秒，可接受）。
type Server struct {
	dataDir string
	note    string // 非空时随 sessions/timeline 响应下发（--demo 的"合成数据"标注）
}

// New 构造服务。dataDir 为账本目录（accounts/*.jsonl 所在）。
func New(dataDir string) *Server { return &Server{dataDir: dataDir} }

// SetNote 设置响应提示条（演示模式的"非真实流水"标注）。走 API 而非页面：
// 前端零改动即可拿到，且两个 JSON 端点行为一致。
func (s *Server) SetNote(note string) { s.note = note }

// noteSuffix 组装响应 note 值：目录缺失提示（可空）在前、本服务标注在后，"；"连接；
// 两者皆空返回空串（调用方据此整体省略 note 键）。sessions/timeline 共用保证对称。
func (s *Server) noteSuffix(base string) string {
	switch {
	case base == "":
		return s.note
	case s.note == "":
		return base
	default:
		return base + "；" + s.note
	}
}

// Routes 组装 API 路由并返回 mux。"/"（静态文件）不在此注册——
// 由 main 用 //go:embed 的 web FS 追加挂载。
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions", s.handleSessions)
	mux.HandleFunc("GET /api/timeline", s.handleTimeline)
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("POST /api/backtest", s.handleBacktest)
	return mux
}

// handleSessions GET /api/sessions → {"sessions":[...],"generated_at":<ts>}。
// 数据目录不存在 → 200 空列表 + note（前端首启友好，不算错误）；其余读失败 → 500。
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	entries, err := ledger.Load(s.dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, map[string]any{
				"sessions": []ledger.SessionSummary{},
				"note":     s.noteSuffix(fmt.Sprintf("数据目录不存在：%s", s.dataDir)),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	resp := map[string]any{
		"sessions":     ledger.Summarize(entries),
		"generated_at": float64(time.Now().Unix()),
	}
	if n := s.noteSuffix(""); n != "" {
		resp["note"] = n
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleTimeline GET /api/timeline?lineage=<id> → 三数组（各自按 ts 升序）：
// requests=usage 行；windows=window 行；events=其余 kind（beat/handoff/block/inject…）。
// 三数组两两不交，合并即该 lineage 的全量行。
func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	entries, err := ledger.Load(s.dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, map[string]any{
				"requests": []ledger.Entry{},
				"events":   []ledger.Entry{},
				"windows":  []ledger.Entry{},
				"note":     s.noteSuffix(fmt.Sprintf("数据目录不存在：%s", s.dataDir)),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	lin := r.URL.Query().Get("lineage")
	var requests, events, windows []ledger.Entry
	for _, e := range entries {
		if e.LineageID != lin {
			continue
		}
		switch e.Kind {
		case "usage":
			requests = append(requests, e)
		case "window":
			windows = append(windows, e)
		default:
			events = append(events, e)
		}
	}
	sortByTs(requests)
	sortByTs(events)
	sortByTs(windows)
	// 空结果也序列化为 [] 而非 null，前端免判空
	if requests == nil {
		requests = []ledger.Entry{}
	}
	if events == nil {
		events = []ledger.Entry{}
	}
	if windows == nil {
		windows = []ledger.Entry{}
	}
	resp := map[string]any{
		"requests": requests,
		"events":   events,
		"windows":  windows,
	}
	if n := s.noteSuffix(""); n != "" {
		resp["note"] = n
	}
	writeJSON(w, http.StatusOK, resp)
}

// backtestReq 是 POST /api/backtest 的请求体。params 键名全小写下划线，
// 与 policy.Params 的 Go 字段名无法自动对应，故单列 wire 结构再手工映射；
// 缺省值（per/safety/beat_out 等）不在本层兜底，统一交给 policy.Derive 的缺省规则。
type backtestReq struct {
	Lineage      string         `json:"lineage"`
	OpenedTS     float64        `json:"opened_ts"`
	ClosedTS     float64        `json:"closed_ts"`
	PrefixTokens float64        `json:"prefix_tokens"`
	Params       backtestParams `json:"params"`
}

type backtestParams struct {
	PIn           float64 `json:"p_in"`
	PCache        float64 `json:"p_cache"`
	POut          float64 `json:"p_out"`
	Per           float64 `json:"per"`
	TTLS          float64 `json:"ttl_s"`
	Safety        float64 `json:"safety"`
	BeatOutTokens float64 `json:"beat_out_tokens"`
	MaxWaitS      float64 `json:"max_wait_s"`
}

// maxBacktestBody 反跑请求体上限（1 MiB）：窗口参数级 JSON 远用不满，防病态大包耗内存。
const maxBacktestBody = 1 << 20

// handleBacktest POST /api/backtest → 反跑仿真。窗口与 prefix 由前端从 window 行带出。
// policy.Derive 的参数错误（p_cache<=0、ttl_s<=0）是业务拒绝而非服务故障 → 200 + ok:false；
// 只有请求体不合法才 400。请求体经 MaxBytesReader 限幅，且解码后不允许尾随垃圾
// （两条 JSON、多余字符一律拒绝）。
func (s *Server) handleBacktest(w http.ResponseWriter, r *http.Request) {
	var req backtestReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBacktestBody))
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": "请求体不是合法 JSON: " + err.Error(),
		})
		return
	}
	if _, err := dec.Token(); err != io.EOF { // 尾随垃圾拒绝：合法体此处必须恰好 EOF
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": "请求体 JSON 后有多余内容",
		})
		return
	}
	p := policy.Params{
		PIn:           req.Params.PIn,
		PCache:        req.Params.PCache,
		POut:          req.Params.POut,
		Per:           req.Params.Per,
		PrefixTokens:  req.PrefixTokens,
		TTLS:          req.Params.TTLS,
		Safety:        req.Params.Safety,
		BeatOutTokens: req.Params.BeatOutTokens,
		MaxWaitS:      req.Params.MaxWaitS,
	}
	res, err := policy.Derive(p)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	beats := policy.SimulateBeats(req.OpenedTS, req.ClosedTS, res) // 绝对时间戳
	if beats == nil {
		beats = []float64{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"result":          res,
		"beats":           beats,
		"beats_cost":      float64(len(beats)) * res.PerBeat,
		"do_nothing_cost": policy.DoNothingCost(req.ClosedTS-req.OpenedTS, p.TTLS, res),
		"note":            capNote(p, res),
	})
}

// capNote 说明上限语义：手动只能往下收；auto 值 = 去掉 MaxWaitS 重新推导。
func capNote(p policy.Params, res policy.Result) string {
	auto := res
	if p.MaxWaitS > 0 {
		pNoMax := p
		pNoMax.MaxWaitS = 0
		if r, err := policy.Derive(pNoMax); err == nil {
			auto = r
		}
	}
	if p.MaxWaitS > 0 {
		return fmt.Sprintf("手动上限只能往下收；cap=%.1fs（auto≈%.1fs）", res.CapS, auto.CapS)
	}
	return fmt.Sprintf("手动上限只能往下收；cap=auto≈%.1fs", auto.CapS)
}

func sortByTs(es []ledger.Entry) {
	sort.SliceStable(es, func(i, j int) bool { return es[i].Ts < es[j].Ts })
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
