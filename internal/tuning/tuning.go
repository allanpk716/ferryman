// tuning.go — 票07(调参三态应用引擎+护栏+调参流水+CLI):状态与流水层。
//
// 决策依据:ADR-0015 决定四/决定六 + 夜链 D10/D11/D8;术语遵根目录
// CONTEXT.md「调参三态/调参流水/建议值」:
//   - D10 三态 manual|recommend|auto(默认 recommend),永不自动升档;
//     auto 五护栏:①只改公式输入不旁路计算器 ②建议值钳 [10, 总结阈值]
//     ③每上游每周至多一次生效 ④样本不足收敛只提醒 ⑤生效后通报+一键回滚。
//   - 决定五/D11:接受/拒绝唯一写路径在 CLI(apply/reject);托盘与面板永不
//     承担按钮。
//   - 决定六:接受/拒绝/自动应用逐条 append-only 落状态目录(调参流水),
//     只记变更与依据,不进账本(账本只记费用事件)。
//
// 护栏①的结构面保证:本包对外的全部写操作只产出 Calibration(公式输入校准
// ——计算器 SameModelObs 的 TTL 观测替代集);运行阈值一律由票05
// policy.SameModelEffectiveThreshold 现算(engine.go EffectiveThreshold),
// 不存在任何直接写运行阈值的路径。档位(mode)在各引擎函数中是只读入参,
// 流水事件类型全集固定(无 mode 事件)——升档只认 config.toml 手改。
//
// 落盘布局(<DataDir>/tuning/):
//
//	log.jsonl                    append-only 调参流水(唯一事实源)
//	calibration-<上游>.json       当前公式输入校准投影(运行侧速读;删了
//	                             也能从流水重放——投影非事实源)
//	reports/<上游>/               票06 扫参报告(证据面,CLI sweep 落)
package tuning

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
)

// 调参三态(D10;config.TuningCfg.Mode 直传,引擎只读)。
const (
	ModeManual    = "manual"
	ModeRecommend = "recommend"
	ModeAuto      = "auto"
)

// 流水事件类型(全集固定;不存在任何改档位事件——永不自动升档的结构面)。
const (
	EvSuggestionCreated = "suggestion_created" // 扫参产出建议(记录,非生效)
	EvAccepted          = "accepted"           // 人工接受(CLI apply)
	EvRejected          = "rejected"           // 人工拒绝(CLI reject)
	EvAutoApplied       = "auto_applied"       // auto 档护栏内自动应用
	EvRollbackApplied   = "rollback_applied"   // 一键回滚(护栏⑤)
)

// 建议状态(与对应事件类型同串,回放直接落)。
const (
	StPending     = "pending"
	StAccepted    = EvAccepted
	StRejected    = EvRejected
	StAutoApplied = EvAutoApplied
)

// 决策动作(Consider 三态产出;全集固定,无"改档位"动作)。
const (
	ActReportOnly = "report_only" // manual:只出报告
	ActRemind     = "remind"      // recommend / 护栏收敛:只提醒
	ActApply      = "apply"       // auto 护栏全过:自动应用
)

// FrequencyWindowS 护栏③频控窗:同一上游 7 天内已有生效记录则只提醒
// (每周至多一次生效;回滚也计入,防自动应用来回翻转)。
const FrequencyWindowS = 7 * 86400

// LogFileName 调参流水固定文件名。
const LogFileName = "log.jsonl"

// Suggestion 一条扫参建议(调参流水的业务主体;建议值出口=票06 投影)。
type Suggestion struct {
	ID          string    `json:"id"`           // s<装载时点戳>-<上游>(确定性)
	Upstream    string    `json:"upstream"`     // =[dock.upstreams] 条目键
	SuggestMin  float64   `json:"suggest_min"`  // 建议值(分钟;票05 公式出口钳 [10,总结])
	CurrentMin  float64   `json:"current_min"`  // 现值(CeilingFor;HasCurrent=false 时无意义)
	HasCurrent  bool      `json:"has_current"`
	BestMin     float64   `json:"best_min"`     // 扫参最优档(依据)
	NetSavings  float64   `json:"net_savings"`  // 扫参最优净额(依据)
	FerryEvents int       `json:"ferry_events"` // 滚动窗摆渡事件数(护栏④样本量)
	MinEvents   int       `json:"min_events"`   // 样本门槛(护栏④)
	TTLObsMin   []float64 `json:"ttl_obs_min"`  // 扫参所用 TTL 观测(应用时成为校准输入)
	ReportPath  string    `json:"report_path"`  // 证据报告落点(票06)
	CreatedAt   float64   `json:"created_at"`   // epoch 秒
}

// Calibration 公式输入校准(护栏①:应用只写这里)。生效值仍由策略计算器
// 按此输入现算——本结构永远不是运行阈值。
type Calibration struct {
	Upstream   string    `json:"upstream"`
	TTLObsMin  []float64 `json:"ttl_obs_min"` // 计算器 SameModelObs.TTLObsMin 的替代集(分钟)
	SuggestMin float64   `json:"suggest_min"` // 本次生效的建议值(记录)
	SourceID   string    `json:"source_id"`   // 来源建议 id
	AppliedAt  float64   `json:"applied_at"`  // epoch 秒
}

// Event 调参流水一行(JSONL;append-only,只记变更与依据)。
type Event struct {
	TS              float64      `json:"ts"`
	Type            string       `json:"type"`
	ID              string       `json:"id,omitempty"`
	Upstream        string       `json:"upstream,omitempty"`
	Mode            string       `json:"mode,omitempty"` // 事件发生时的档位(审计;永不改写)
	Reason          string       `json:"reason,omitempty"`
	Suggestion      *Suggestion  `json:"suggestion,omitempty"`
	Calibration     *Calibration `json:"calibration,omitempty"`     // 生效后校准(回滚事件=还原值)
	PrevCalibration *Calibration `json:"prev_calibration,omitempty"` // 生效前快照(回滚源)
}

// State 流水重放产物(建议台账+生效台账;全由 log.jsonl 重建)。
type State struct {
	Suggestions   map[string]*Suggestion  // id → 最近快照
	Status        map[string]string       // id → pending|accepted|rejected|auto_applied
	Calib         map[string]*Calibration // 上游 → 当前公式输入校准
	PrevCalib     map[string]*Calibration // 上游 → 最近一次生效前快照(回滚源)
	LastEffective map[string]float64      // 上游 → 最近生效/回滚 ts(护栏③频控输入)
	Events        int                     // 回放的有效事件数
}

func newState() *State {
	return &State{
		Suggestions:   map[string]*Suggestion{},
		Status:        map[string]string{},
		Calib:         map[string]*Calibration{},
		PrevCalib:     map[string]*Calibration{},
		LastEffective: map[string]float64{},
	}
}

// Store 状态目录库(<DataDir>/tuning)。
type Store struct{ Dir string }

// NewStore 以数据根装配(状态子目录固定 tuning)。
func NewStore(dataDir string) *Store { return &Store{Dir: filepath.Join(dataDir, "tuning")} }

func (s *Store) logPath() string { return filepath.Join(s.Dir, LogFileName) }

// Append 追加一条事件(O_APPEND 单行写;任何时刻不改写既有行)。
func (s *Store) Append(e *Event) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.logPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// Replay 重放流水重建状态(坏行 stderr 告警后跳过——backtest 装载同惯例;
// 文件不存在 = 空状态非错误)。校准投影文件不参与:流水是唯一事实源。
func (s *Store) Replay() (*State, error) {
	st := newState()
	b, err := os.ReadFile(s.logPath())
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return nil, err
	}
	for i, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e Event
		if json.Unmarshal([]byte(line), &e) != nil {
			fmt.Fprintf(os.Stderr, "[tuning] 跳过损坏流水行 %s:%d\n", LogFileName, i+1)
			continue
		}
		applyEvent(st, &e)
		st.Events++
	}
	return st, nil
}

// applyEvent 单事件状态迁移(回放语义;三种生效事件推进频控时钟)。
func applyEvent(st *State, e *Event) {
	switch e.Type {
	case EvSuggestionCreated:
		if e.Suggestion != nil {
			st.Suggestions[e.Suggestion.ID] = e.Suggestion
			if _, seen := st.Status[e.Suggestion.ID]; !seen {
				st.Status[e.Suggestion.ID] = StPending
			}
		}
	case EvAccepted, EvAutoApplied:
		if e.ID != "" {
			st.Status[e.ID] = e.Type
		}
		if e.Calibration != nil {
			st.Calib[e.Upstream] = e.Calibration
		}
		st.PrevCalib[e.Upstream] = e.PrevCalibration // nil = 生效前无校准
		st.LastEffective[e.Upstream] = e.TS
	case EvRejected:
		if e.ID != "" {
			st.Status[e.ID] = StRejected
		}
	case EvRollbackApplied:
		if e.Calibration == nil {
			delete(st.Calib, e.Upstream) // 还原到无校准态
		} else {
			st.Calib[e.Upstream] = e.Calibration
		}
		// undo 交换:回滚前值成为新的回滚源(再回滚=还原它,防单向丢历史)。
		st.PrevCalib[e.Upstream] = e.PrevCalibration
		st.LastEffective[e.Upstream] = e.TS // 回滚计入频控窗(防自动应用来回翻转)
	}
}

// calibFile 该上游的校准投影文件路径。
func (s *Store) calibFile(upstream string) string {
	return filepath.Join(s.Dir, "calibration-"+safeName(upstream)+".json")
}

// writeCalibFile 校准投影落盘(nil = 删除投影,还原无校准态);投影删除后
// 状态仍可由流水重放——投影非事实源。
func (s *Store) writeCalibFile(cal *Calibration) error {
	if cal == nil {
		err := os.Remove(s.calibFile(calUpstream(cal)))
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cal, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.calibFile(cal.Upstream), b, 0o644)
}

// calUpstream nil 安全取上游名(删除投影时 cal 为 nil)。
func calUpstream(cal *Calibration) string {
	if cal == nil {
		return ""
	}
	return cal.Upstream
}

// RecordSuggestion 记录一条新建议(EvSuggestionCreated;台账登记,非生效)。
func (s *Store) RecordSuggestion(sug *Suggestion, now float64) error {
	return s.Append(&Event{TS: now, Type: EvSuggestionCreated,
		ID: sug.ID, Upstream: sug.Upstream, Suggestion: sug})
}

// ReportDir 该上游扫参报告目录(票06 WriteSameModelReport 落点的父目录;
// 上游名经 safeName 折安全段)。
func (s *Store) ReportDir(upstream string) string {
	return filepath.Join(s.Dir, "reports", safeName(upstream))
}

// safeName 上游名 → 文件名安全段:非 [A-Za-z0-9._-] 折为 '_',并追加原名
// FNV-1a 前 6 位十六进制——中文名上游不互撞且可反查。
func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return b.String() + fmt.Sprintf("-%x", h.Sum32())
}
