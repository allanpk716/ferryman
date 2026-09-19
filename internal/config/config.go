// Package config Ferryman 配置：~/ferryman/config.toml（可选）+ 内置默认 + 启动校验
// （规格 ferryman/config.py 1:1，DESIGN §4）。
//
// 校验铁律（违例拒启，Python 逐字）：
//   - summarize_threshold < block_threshold（严格小于，按 Agent 分组）；
//   - block_threshold − summarize_threshold ≥ 2min（独立硬约束，不依赖 SLA 定义）；
//   - gate_mode ∈ {off, observe, enforce}。
//
// 副作用保留（config.py:164-205）：question_watch 开启时 ferry_deadline_lead_s
// 低于下限则就地夹取为 QwatchMinLeadS（打印告警）；上限不夹取——summarize_s+lead
// 超过 block_s 直接拒绝（摆渡死线必须赶在闸门拦截之前，spec 决策 3）。
package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

var (
	// GateModes 闸门模式合法值（Python GATE_MODES）。
	GateModes = [...]string{"off", "observe", "enforce"}
	// QWatchModes 问询守望模式合法值（Python QWATCH_MODES，同款三元）。
	QWatchModes = [...]string{"off", "observe", "enforce"}
	// WaitWindowModes 等待窗心跳模式合法值（票04，与 [question_watch] 平行的三元）。
	WaitWindowModes = [...]string{"off", "observe", "enforce"}
)

// QwatchMinLeadS ferry_deadline_lead_s 下限（spec 决策 3 夹取区间）。
const QwatchMinLeadS = 60.0

// FerryWallTimeoutS 摆渡墙钟总时限 8min（DESIGN §4；daemon 同名再导出）。
const FerryWallTimeoutS = 480.0

// WatchCfg 会话目录与轮询配置。
type WatchCfg struct {
	PollIntervalS    float64
	CCProjectsDir    string   // 空 = ~/.claude/projects（测试可指临时目录）
	CodexSessionsDir string   // 空 = ~/.codex/sessions
	CodexExtraDirs   []string // 额外 codex 会话目录（Orca 重定向的 CODEX_HOME，2026-09-17 实测）
	HarvestUsage     bool     // 用量采集（usage 科目）：30 天清理后的审计地基，隐私敏感可关
}

// ThresholdCfg 阈值组。
type ThresholdCfg struct {
	SummarizeS   float64
	BlockS       float64 // E0a 实测拐点+5min（reports/e0a-cc-glm.md）
	MinCtxTokens int
	CacheWarnS   float64 // 12min：缓存死线纯提醒（信息条，不拦不触发摆渡；0=关）
}

// ServerCfg 服务端口与数据目录。
type ServerCfg struct {
	Port    int
	DataDir string // 空 = ~/ferryman（token/handoffs/index 所在地）
}

// NotifyCfg 通知通道。
type NotifyCfg struct {
	Enabled       bool // 默认关：未配置不响，测试套件不弹 toast/不出网（T25）
	Pushover      bool
	PushoverToken string // 空 → 回落环境变量 PUSHOVER_TOKEN
	PushoverUser  string // 空 → 回落环境变量 PUSHOVER_USER
	Toast         bool
}

// HeartbeatCfg 心跳（T41 仅预留：执行器未实装，设计 §0 授权边界）。
type HeartbeatCfg struct {
	Enabled       bool
	TTLS          float64 // 0 = 未实测/未配置（report 策略对比跳过）
	TTLMeasuredAt string
	TTLSource     string
}

// QuestionWatchCfg T51 问询守望（spec 决策 9）：提问潮等答复窗口＋心跳保温，默认 off。
type QuestionWatchCfg struct {
	Mode               string  // off | observe | enforce
	MinQuestions       int     // 提问潮阈值（unit_count ≥ 此值命中）
	BeatIntervalS      float64 // 0.7×GLM 实测 TTL 600s（口径统一 0.7×）
	MaxBeats           int     // 每窗最多心跳跳数
	FerryDeadlineLeadS float64 // 摆渡死线提前量（校验见 Validate）
}

// WaitWindowCfg 等待窗心跳（票04，spec「心跳·配置」）：与 [question_watch]
// 平行的独立三态，默认 off。间隔/等待上限/最小前缀阈值全部由策略计算器
// （internal/policy）现算——本节不含也不得引入这些参数（公式单源红线）；
// manual_wait_cap_s 只能在使用点向下夹紧计算器输出的 cap（0=未配置）。
type WaitWindowCfg struct {
	Mode           string  // off | observe | enforce
	ManualWaitCapS float64 // >0 = 手动等待上限（只收小）；0 = 未配置
}

// DockCfg 渡口（本机 API 中转，票01）配置。注意语义是 opt-in：Config.Dock
// 为 nil 指针（[dock] 节缺失）＝渡口完全不启动——不绑端口、零行为变化
// （评审 F11 裁定）；节存在才构造本结构，缺字段回落默认值。
type DockCfg struct {
	UpstreamBaseURL string // 上游（默认 cc-switch）地址
	Listen          string // 渡口监听地址（绑本机）
}

// Config 全量配置（字段=Python dataclass 1:1）。
type Config struct {
	GateCC        string // 验证期默认 observe（DESIGN §6.2）
	GateCodex     string // E0b 后再议
	Thresholds    ThresholdCfg
	Watch         WatchCfg
	Server        ServerCfg
	Notify        NotifyCfg
	Heartbeat     HeartbeatCfg
	QuestionWatch QuestionWatchCfg
	WaitWindow    WaitWindowCfg // 票04：等待窗心跳三态（默认 off，缺节即 off）
	FerryProvider string        // 空=未配置：摆渡降级骨架（worker 警告，doctor 提示）
	Dock          *DockCfg      // nil=[dock] 节缺失＝渡口不启动（F11 opt-in）
}

// Default 内置全默认值（config.py 各 dataclass 默认逐字）。
func Default() *Config {
	return &Config{
		GateCC:    "observe",
		GateCodex: "off",
		Thresholds: ThresholdCfg{
			SummarizeS:   25 * 60,
			BlockS:       35 * 60,
			MinCtxTokens: 20_000,
			CacheWarnS:   720.0,
		},
		Watch: WatchCfg{
			PollIntervalS:  3.0,
			CodexExtraDirs: []string{},
			HarvestUsage:   true,
		},
		Server: ServerCfg{Port: 7311},
		Notify: NotifyCfg{Enabled: false, Pushover: true, Toast: true},
		Heartbeat: HeartbeatCfg{
			Enabled: false,
			TTLS:    0.0,
		},
		QuestionWatch: QuestionWatchCfg{
			Mode:               "off",
			MinQuestions:       5,
			BeatIntervalS:      420.0,
			MaxBeats:           2,
			FerryDeadlineLeadS: 480.0,
		},
		WaitWindow:    WaitWindowCfg{Mode: "off", ManualWaitCapS: 0},
		FerryProvider: "",
	}
}

// DataDir Server.DataDir 非空则用之，否则 ~/ferryman（Python data_dir property）。
func (c *Config) DataDir() string {
	if c.Server.DataDir != "" {
		return c.Server.DataDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return filepath.Join(home, "ferryman")
}

// ThresholdFor 按 Agent 分设留口：目前共用全局，Codex gate 本就 off。
func (c *Config) ThresholdFor(_ string) ThresholdCfg {
	return c.Thresholds
}

// Load 读配置并校验。路径优先级：显式参数 > 环境变量 FERRYMAN_CONFIG >
// ~/ferryman/config.toml；文件不存在 = 全默认；TOML 坏 = error。
// relaxMinGap=True：冒烟/测试用——放宽"阈值差≥120s"（仍强制 summarize<block）。
func Load(path string, relaxMinGap bool) (*Config, error) {
	cfg := Default()
	p := path
	if p == "" {
		p = os.Getenv("FERRYMAN_CONFIG")
	}
	if p == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = ""
		}
		p = filepath.Join(home, "ferryman", "config.toml")
	}
	if _, err := os.Stat(p); err == nil { // Python p.exists()：异常一律视为不存在
		data := map[string]any{}
		if _, err := toml.DecodeFile(p, &data); err != nil {
			return nil, err
		}
		if err := applyTOML(cfg, data); err != nil {
			return nil, err
		}
	}
	if err := Validate(cfg, relaxMinGap); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyTOML 各节覆盖（Python load 节内 .get 语义逐字：节存在才整节重建，
// 缺字段回落默认；部分字段容忍 = 宽松类型转换）。
func applyTOML(cfg *Config, data map[string]any) error {
	if raw, ok := data["gate"]; ok {
		g, err := asTable(raw, "gate")
		if err != nil {
			return err
		}
		// Python 原样赋值不做 str()；非法类型两语言都会被校验拒绝，渲染一致
		if v, ok := g["cc_mode"]; ok {
			cfg.GateCC = pyStr(v)
		}
		if v, ok := g["codex_mode"]; ok {
			cfg.GateCodex = pyStr(v)
		}
	}
	if raw, ok := data["thresholds"]; ok {
		t, err := asTable(raw, "thresholds")
		if err != nil {
			return err
		}
		s, err := pyFloat(get(t, "summarize_s", cfg.Thresholds.SummarizeS))
		if err != nil {
			return err
		}
		b, err := pyFloat(get(t, "block_s", cfg.Thresholds.BlockS))
		if err != nil {
			return err
		}
		mi, err := pyInt(get(t, "min_ctx_tokens", cfg.Thresholds.MinCtxTokens))
		if err != nil {
			return err
		}
		cw, err := pyFloat(get(t, "cache_warn_s", cfg.Thresholds.CacheWarnS))
		if err != nil {
			return err
		}
		cfg.Thresholds = ThresholdCfg{SummarizeS: s, BlockS: b, MinCtxTokens: mi, CacheWarnS: cw}
	}
	if raw, ok := data["watch"]; ok {
		w, err := asTable(raw, "watch")
		if err != nil {
			return err
		}
		pi, err := pyFloat(get(w, "poll_interval_s", cfg.Watch.PollIntervalS))
		if err != nil {
			return err
		}
		dirs := []string{}
		if rawDirs, ok := w["codex_extra_dirs"]; ok {
			arr, ok := rawDirs.([]any)
			if !ok {
				return errors.New("config: watch.codex_extra_dirs 不是数组")
			}
			for _, d := range arr { // Python [str(d) for d in ...]
				dirs = append(dirs, pyStr(d))
			}
		}
		cfg.Watch = WatchCfg{
			PollIntervalS:    pi,
			CCProjectsDir:    pyStr(get(w, "cc_projects_dir", "")),
			CodexSessionsDir: pyStr(get(w, "codex_sessions_dir", "")),
			CodexExtraDirs:   dirs,
			HarvestUsage:     pyBool(get(w, "harvest_usage", true)),
		}
	}
	if raw, ok := data["server"]; ok {
		s, err := asTable(raw, "server")
		if err != nil {
			return err
		}
		port, err := pyInt(get(s, "port", cfg.Server.Port))
		if err != nil {
			return err
		}
		cfg.Server = ServerCfg{Port: port, DataDir: pyStr(get(s, "data_dir", ""))}
	}
	if raw, ok := data["notify"]; ok {
		n, err := asTable(raw, "notify")
		if err != nil {
			return err
		}
		cfg.Notify = NotifyCfg{
			Enabled:       pyBool(get(n, "enabled", cfg.Notify.Enabled)),
			Pushover:      pyBool(get(n, "pushover", cfg.Notify.Pushover)),
			PushoverToken: pyStr(get(n, "pushover_token", "")),
			PushoverUser:  pyStr(get(n, "pushover_user", "")),
			Toast:         pyBool(get(n, "toast", cfg.Notify.Toast)),
		}
	}
	if raw, ok := data["heartbeat"]; ok {
		hb, err := asTable(raw, "heartbeat")
		if err != nil {
			return err
		}
		ttl, err := pyFloat(get(hb, "ttl_s", 0.0))
		if err != nil {
			return err
		}
		cfg.Heartbeat = HeartbeatCfg{
			Enabled:       pyBool(get(hb, "enabled", false)),
			TTLS:          ttl,
			TTLMeasuredAt: pyStr(get(hb, "ttl_measured_at", "")),
			TTLSource:     pyStr(get(hb, "ttl_source", "")),
		}
	}
	if raw, ok := data["question_watch"]; ok {
		q, err := asTable(raw, "question_watch")
		if err != nil {
			return err
		}
		beat, err := pyFloat(get(q, "beat_interval_s", cfg.QuestionWatch.BeatIntervalS))
		if err != nil {
			return err
		}
		lead, err := pyFloat(get(q, "ferry_deadline_lead_s", cfg.QuestionWatch.FerryDeadlineLeadS))
		if err != nil {
			return err
		}
		mq, err := pyInt(get(q, "min_questions", cfg.QuestionWatch.MinQuestions))
		if err != nil {
			return err
		}
		mb, err := pyInt(get(q, "max_beats", cfg.QuestionWatch.MaxBeats))
		if err != nil {
			return err
		}
		cfg.QuestionWatch = QuestionWatchCfg{
			Mode:               pyStr(get(q, "mode", cfg.QuestionWatch.Mode)),
			MinQuestions:       mq,
			BeatIntervalS:      beat,
			MaxBeats:           mb,
			FerryDeadlineLeadS: lead,
		}
	}
	// [wait_window]（票04）：缺字段回落默认（off/0）。夹紧不在此做——manual
	// 只能向下夹紧计算器输出，发生在 watcher 使用点（Validate 只拦负值）。
	if raw, ok := data["wait_window"]; ok {
		ww, err := asTable(raw, "wait_window")
		if err != nil {
			return err
		}
		capS, err := pyFloat(get(ww, "manual_wait_cap_s", cfg.WaitWindow.ManualWaitCapS))
		if err != nil {
			return err
		}
		cfg.WaitWindow = WaitWindowCfg{
			Mode:           pyStr(get(ww, "mode", cfg.WaitWindow.Mode)),
			ManualWaitCapS: capS,
		}
	}
	// Python: cfg.ferry_provider = str(data.get("ferry", {}).get("provider", cfg.ferry_provider))
	if raw, ok := data["ferry"]; ok {
		f, err := asTable(raw, "ferry")
		if err != nil {
			return err
		}
		cfg.FerryProvider = pyStr(get(f, "provider", cfg.FerryProvider))
	}
	// [dock]（票01）：节存在才构造（Default() 里 Dock 恒 nil——nil 即 F11 的
	// "完全不启动"判据，daemon 侧据此不绑端口）；节内缺字段回落默认值
	// （上游=cc-switch 15721，监听=本机 15722，与透传实验 forwarder.go 一致）。
	if raw, ok := data["dock"]; ok {
		dk, err := asTable(raw, "dock")
		if err != nil {
			return err
		}
		cfg.Dock = &DockCfg{
			UpstreamBaseURL: pyStr(get(dk, "upstream_base_url", "http://127.0.0.1:15721")),
			Listen:          pyStr(get(dk, "listen", "127.0.0.1:15722")),
		}
	}
	return nil
}

// Validate 违例拒启（config.py:164-205 逐字，含两类告警打印与 lead 就地夹取）。
func Validate(c *Config, relaxMinGap bool) error {
	var problems []string
	if !slices.Contains(GateModes[:], c.GateCC) {
		problems = append(problems, fmt.Sprintf("gate.cc_mode 非法: %s（可选 %s）",
			c.GateCC, pyTuple(GateModes[:])))
	}
	if !slices.Contains(GateModes[:], c.GateCodex) {
		problems = append(problems, fmt.Sprintf("gate.codex_mode 非法: %s", c.GateCodex))
	}
	qw := &c.QuestionWatch
	if !slices.Contains(QWatchModes[:], qw.Mode) {
		problems = append(problems, fmt.Sprintf("question_watch.mode 非法: %s（可选 %s）",
			qw.Mode, pyTuple(QWatchModes[:])))
	}
	if qw.BeatIntervalS <= 0 { // 票04 M5：≤0 排出的计划全是过去跳（开窗即狂跳）
		problems = append(problems, fmt.Sprintf("question_watch.beat_interval_s 须 > 0（当前 %gs）",
			qw.BeatIntervalS))
	}
	// [wait_window]（票04）：mode 三元；manual_wait_cap_s ≥0（0=未配置，
	// 负值拒绝——夹紧逻辑只认 >0）。enforce＋渡口关不在配置层拒——那是运行时
	// 降级（watcher 启动告警一次＋按 observe 对待），问询守望同不受此校验。
	ww := &c.WaitWindow
	if !slices.Contains(WaitWindowModes[:], ww.Mode) {
		problems = append(problems, fmt.Sprintf("wait_window.mode 非法: %s（可选 %s）",
			ww.Mode, pyTuple(WaitWindowModes[:])))
	}
	if ww.ManualWaitCapS < 0 {
		problems = append(problems, fmt.Sprintf("wait_window.manual_wait_cap_s 须 ≥ 0（当前 %gs；0=未配置）",
			ww.ManualWaitCapS))
	}
	if qw.Mode != "off" { // 功能关闭时不校验 lead（存量小阈值配置零影响）
		t := c.ThresholdFor("cc")
		if qw.FerryDeadlineLeadS < QwatchMinLeadS {
			fmt.Printf("[config] ⚠ question_watch.ferry_deadline_lead_s "+
				"%gs 低于下限，已夹取为 %gs\n", qw.FerryDeadlineLeadS, QwatchMinLeadS)
			qw.FerryDeadlineLeadS = QwatchMinLeadS
		}
		if qw.FerryDeadlineLeadS+t.SummarizeS > t.BlockS {
			problems = append(problems, fmt.Sprintf(
				"question_watch.ferry_deadline_lead_s 过大："+
					"summarize_s + lead（%g + %gs）须 ≤ block_s（%gs）"+
					"——摆渡死线必须赶在闸门拦截之前",
				t.SummarizeS, qw.FerryDeadlineLeadS, t.BlockS))
		} else if qw.FerryDeadlineLeadS <= FerryWallTimeoutS {
			// 票02 评审转来的配置补强：死线余量不大于摆渡墙钟，摆渡可能贴线
			// 被墙钟砍成骨架。只告警不改值（默认 480 恰在贴线位，属已知取舍）。
			fmt.Printf("[config] ⚠ question_watch.ferry_deadline_lead_s "+
				"%gs 不大于摆渡墙钟 %gs——死线余量不足，骨架可能贴线\n",
				qw.FerryDeadlineLeadS, FerryWallTimeoutS)
		}
	}
	for _, agent := range []string{"cc", "codex"} {
		t := c.ThresholdFor(agent)
		if !(t.SummarizeS < t.BlockS) {
			problems = append(problems, fmt.Sprintf("[%s] 总结阈值必须严格小于拦截阈值（当前 %ss / %ss）",
				agent, pyFloatStr(t.SummarizeS), pyFloatStr(t.BlockS)))
		} else if !relaxMinGap && t.BlockS-t.SummarizeS < 120 {
			problems = append(problems, fmt.Sprintf("[%s] 阈值差须 ≥120s（当前 %.0fs）",
				agent, t.BlockS-t.SummarizeS))
		}
	}
	if c.Watch.PollIntervalS <= 0 {
		problems = append(problems, "watch.poll_interval_s 须 > 0")
	}
	if len(problems) > 0 {
		return errors.New("配置校验失败，拒绝启动：\n  - " + strings.Join(problems, "\n  - "))
	}
	return nil
}

// ---- 以下为 .get 容忍语义与 Python 文案渲染的内部工具 ----

func asTable(v any, name string) (map[string]any, error) {
	if t, ok := v.(map[string]any); ok {
		return t, nil
	}
	return nil, fmt.Errorf("config: 节 %s 不是表", name)
}

// get 模拟 dict.get(key, default)。
func get(t map[string]any, key string, def any) any {
	if v, ok := t[key]; ok {
		return v
	}
	return def
}

// pyFloat 模拟 Python float()：数值/布尔/十进制串可转，其余报错（Load 上抛）。
func pyFloat(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case int64:
		return float64(x), nil
	case int:
		return float64(x), nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return 0, fmt.Errorf("config: 无法转换为浮点数: %q", x)
		}
		return f, nil
	default:
		return 0, fmt.Errorf("config: 无法转换为浮点数: %v", v)
	}
}

// pyInt 模拟 Python int()：浮点向零截断，串须为纯整数。
func pyInt(v any) (int, error) {
	switch x := v.(type) {
	case int64:
		return int(x), nil
	case int:
		return x, nil
	case float64:
		return int(math.Trunc(x)), nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("config: 无法转换为整数: %q", x)
		}
		return int(n), nil
	default:
		return 0, fmt.Errorf("config: 无法转换为整数: %v", v)
	}
}

// pyBool 模拟 Python bool() 真值：0/空串/空容器为假，其余为真（含 "false" 串）。
func pyBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int64:
		return x != 0
	case int:
		return x != 0
	case float64:
		return x != 0
	case string:
		return x != ""
	default:
		return true
	}
}

// pyStr 模拟 Python str()：布尔渲染 "True"/"False"，浮点走 str(float) 语义。
func pyStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case float64:
		return pyFloatStr(x)
	default:
		return fmt.Sprintf("%v", x)
	}
}

// PyFloatStr pyFloatStr 的导出面（票17 serve 横幅跨包复用 Python str(float)
// 渲染：1500 → "1500.0"，0.01 → "0.01"）。
func PyFloatStr(v float64) string { return pyFloatStr(v) }

// pyFloatStr 以 Python str(float) 语义渲染浮点：整值补 ".0"（100 → "100.0"），
// 其余最短往返。供校验文案与 Python 版逐字对齐。
func pyFloatStr(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e16 {
		return strconv.FormatFloat(v, 'f', -1, 64) + ".0"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// pyTuple 渲染 Python 元组 repr（校验文案中「可选 (...)」逐字对齐）。
func pyTuple(items []string) string {
	return "('" + strings.Join(items, "', '") + "')"
}
