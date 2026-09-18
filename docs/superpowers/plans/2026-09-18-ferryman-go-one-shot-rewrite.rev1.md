# Ferryman Go 一次性重写 · 执行计划（rev1）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 Python 后端（守护核心+摆渡执行器+CLI，21 模块/6631 行/316 个可移植测试）整体移植为根部单一 Go module，产出合并版 `ferryman.exe`，最后一次性替换 Python（无并行期、无烧机期）。

**Architecture:** 仓库根立 `module ferryman`；viewer 收编为 `internal/viewer` + 资产随 cmd 包；守护核心按 Python 模块一一对应拆包；公式收口到唯一 `internal/policy`；daemon 包内统一锁序 windowsMu→ledgerMu。行为规格 = 既有 Python 测试逐条移植（先测试后实现），内部结构允许自由重构。

**Tech Stack:** Go 1.23（net/http 标准库路由、BurntSushi/toml、getlantern/systray 既有依赖 + modernc.org/sqlite 新增；禁 CGO；**console 子系统构建**）。

**Spec:** `docs/adr/0003-backend-migrate-to-go.md`（路线）、`docs/adr/0005-one-shot-cutover-go-rewrite.md`（切换策略）、`docs/20260918_2311_Python到Go一次性迁移_评估.md`（评估）。
**本版修订（rev1，2026-09-19 夜）：** 消化 round-0 评审 16 条必改（.xcheck/20260918-235459/SUMMARY.md）——embed 资产随包、console 子系统取代 windowsgui、回退工件化+演练、json tag 显式化、无上限行读、正则 Unicode 化、成功路径装配测试、测试文件/重名规则、enforce 沙箱冒烟、测试账目列死、Round 实现改 FormatFloat（实验 140,168 组 0 失配）；另收纳用户评审中途指令：**切换后只装 SessionStart + SubagentStart/Stop 三类钩子，UserPromptSubmit（闸门）暂不装**（防"子代理久跑→交接异常→主会话输入被吞"复发；改进方向重构后再议）。

## Global Constraints（每任务隐含遵守）

- **C1 规格=测试**：目标模块的 Python 测试全量移植为 Go 测试（红→实现→绿→提交）。**账目（实测）**：总数 387 = 冻结面 71（test_e0c 18 + test_e0c_agents 15 + test_e0c_report 17 + test_e0c_session 16 + test_eval_checks 5，不迁）+ **可移植 316（含 test_hooks 16 在内）**；T26 验收按 316 对账。
- **C1b 测试文件与重名规则**：**一个 Python 测试文件 → 一个 Go 测试文件**（`test_x.py` → `x_test.go`，同包多文件共存；跨源合并不许并成单文件）。同包函数重名（Go 禁止）以来源前缀消歧；已知消歧点：T4 中 viewer 副本测试更名 `policy_viewer_test.go` 且其 `TestNoCachePriceRefuses` 等改带 `Viewer` 前缀，Python `test_policy.py` 侧保留原名映射。
- **C2 舍入**：Python `round()`=half-even。`mathx.Round(x, n)` **必须用 `strconv.FormatFloat(x, 'f', n, 64)` + `ParseFloat` 实现**（round-0 实验 exp/：与 CPython 140,168 组对照 0 失配）；**禁止** `RoundToEven(x*10ⁿ)/10ⁿ` naive 缩放（同实验 109 例失配，含 round(6.335,2)=6.33、round(0.0005,3)=0.001）。账本 ts=3、dur=1、beat 成本=6、报表净额=4 位。
- **C3 时间**：float64 epoch 秒；mtime=ModTime().UnixNano()/1e9；测试经 `clock.Now` 注入。
- **C4 码点**：Python len/切片按码点——Go 用 `mathx.RuneLen`/`mathx.RuneTrunc`，禁止裸 len/切片处理用户文本。
- **C5 路径键**：`pathsx.NormPath`（`\`→`/` + ToLower）是 lineage 唯一键形。
- **C6 锁序**：daemon 包内 **windowsMu（外）→ ledger.Mu()（内）**，双资源临界区两把同序全拿；ledger 公共方法自带锁、临界区用无锁内方法；禁止同 goroutine 重复加锁。
- **C7 兼容**：5 端点 + Bearer + JSON 字段名、账本白名单、**index.json 的 snake_case 键（结构体显式 json tag）**、交接 MD/警告/日志中文文案——逐字段/逐字平移；两版输出比对用语义 diff。
- **C8 正则**：RE2；`\Z`→`\z`，DOTALL→`(?s)`，IGNORECASE→`(?i)`；**字符类 Unicode 化（Python str 模式语义）**：`\s`→显式 Unicode 空白类 `[\t\n\v\f\r \x{1C}-\x{1F}\x{85}\x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}]`、`\d`→`\p{Nd}`、`\w`→`[\p{L}\p{Nd}_]`；每条正则移植时逐条审计并在测试里放全角空格/全角数字样本。包级 var 预编译。
- **C9 Windows**：**console 子系统构建**（`go build -o ferryman.exe ./cmd/ferryman`，**不带** `-H windowsgui`）——doctor/report/install/serve 横幅在终端全部可见；无窗口性交给启动方：start-daemon.cmd 用 `start "" /min`、桌面快捷方式 WindowStyle= minimized、钩子自举 ensure.ps1 的 `Start-Process -WindowStyle Hidden`（既有）。
- **C10 提交**：每任务一 commit，`feat(go): T<N> <pkg>——<一句话>（规格 tests/test_x.py）`。
- **C11 Python 侧只读**：T27 之前 Python 继续服役，只收 bugfix。
- **C12 钩子安装子集（用户指令）**：installer 支持按事件子集安装；切换日只装 SessionStart + SubagentStart/Stop（继续收数据），**UserPromptSubmit（闸门）暂不装**；doctor 对闸门事件缺位按"提示不失败"处理。

---

### Task 1: 仓库收形——根 module 化，viewer 搬入（资产随 cmd 包）

**Files:**
- Create: `go.mod`（根，`module ferryman`，依赖照抄 viewer/go.mod）
- Create: `cmd/viewer/main.go`（= viewer/main.go，改 import 前缀）
- Create: `cmd/viewer/web/`、`cmd/viewer/icon.ico`（**资产随 cmd 包搬移**——embed 只能引用包目录子树，不得放仓库根）
- Create: `internal/viewer/{server,demo,ledger}/`（原样搬移改前缀；policy 临时占位 T4 删）
- Delete: `viewer/`

**Interfaces:**
- Produces: import 前缀 `ferryman/internal/viewer/...`；embed 指令保持相对本包路径（`//go:embed web`、`//go:embed icon.ico` 在 cmd/viewer/main.go 内，资产在其旁）。

**Steps:**

- [ ] **Step 1**: git mv：`viewer/internal/server→internal/viewer/server`、`demo/ledger/policy` 同理、`viewer/main.go→cmd/viewer/main.go`、`viewer/main_test.go→cmd/viewer/main_test.go`、`viewer/web→cmd/viewer/web`、`viewer/icon.ico→cmd/viewer/icon.ico`。
- [ ] **Step 2**: 根 go.mod（module ferryman，require 照抄）+ `go mod tidy`；import 前缀全局替换。
- [ ] **Step 3**: `go build ./... && go test ./...` 全绿；`go run ./cmd/viewer --demo --no-tray --no-browser` 能起（embed 路径随包生效）。
- [ ] **Step 4**: Commit `feat(go): T1 仓库收形——viewer 并入根 module，资产随 cmd 包`。

---

### Task 2: 基础件 mathx / clock / pathsx

**Files:**
- Create: `internal/mathx/mathx.go`、`mathx_test.go`、`internal/clock/clock.go`、`clock_test.go`、`internal/pathsx/pathsx.go`、`pathsx_test.go`

**Interfaces:**
- Produces（全战役依赖）:

```go
// mathx —— Round 必须按 C2 用 FormatFloat 实现（勿用 naive 缩放）：
func Round(x float64, places int) float64 {
	s := strconv.FormatFloat(x, 'f', places, 64) // CPython round 等价（实验 140,168 组 0 失配）
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
func RuneLen(s string) int
func RuneTrunc(s string, cap int) string
// clock
var Now = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
// pathsx
func NormPath(p string) string
```

- [ ] **Step 1: 失败测试**：Round 边界电池**必须包含** round-0 实验的失配五例（期望值以 Python 实测为准）：`round(2.675,2)=2.67`、`round(3.175,2)=3.17`、`round(6.335,2)=6.33`、`round(0.0005,3)=0.001`、`round(123.4565,3)=123.457`（注意此例 naive 给 123.456，FormatFloat 给 123.457——先 `uv run python -c` 实测钉死）；另加 half-even 常规例（2.5→2、3.5→4、0.5→0）与负零归一例。RuneTrunc 中文截断、NormPath 大小写/斜杠用例。
- [ ] **Step 2**: 实现（Round 照上方实现；RuneTrunc 用 []rune）。
- [ ] **Step 3**: 绿 + Commit `feat(go): T2 基础件 mathx/clock/pathsx（Round=FormatFloat 方案）`。

---

### Task 3: internal/prices（规格 ferryman/prices.py + tests/test_prices.py）

**Files:** Create `internal/prices/prices.go`、`prices_test.go`（= test_prices.py 1:1）

**Interfaces:**

```go
type PriceVersion struct {
	EffectiveFrom string   // "YYYY-MM-DD"，UTC 零点起
	PIn           float64
	PCache        *float64 // nil = 未公布/无缓存价
	POut          float64
}
type PriceBook struct {
	Key      string
	Unit     string
	Per      int // 缺省 10000
	Versions []PriceVersion // EffectiveFrom 升序
}
func (b *PriceBook) At(ts float64) *PriceVersion
func PriceTag(bookKey string, pv PriceVersion) string // "key@YYYY-MM-DD"
func LoadPrices(path string) map[string]PriceBook     // "" → ~/ferryman/config.toml；无文件/无节 → 空 map
```

- [ ] **Step 1**: test_prices.py 全量移植。
- [ ] **Step 2**: 实现 + 绿 + Commit `feat(go): T3 prices 移植（规格 tests/test_prices.py）`。

---

### Task 4: internal/policy——公式单源（规格 policy.py + test_policy.py + viewer policy_test.go）

**Files:**
- Create: `internal/policy/policy.go`（Python policy.py + viewer 三函数并入同包）
- Create: `internal/policy/policy_test.go`（**test_policy.py 1:1**）+ `internal/policy/policy_viewer_test.go`（**viewer policy_test.go 原文更名**，其函数加 `Viewer` 前缀消歧——见 C1b）
- Delete: `internal/viewer/policy/`；Modify: viewer server/demo 引用改指 `ferryman/internal/policy`

**Interfaces:**

```go
type HeartbeatPolicy struct {
	TTLS, TauS, PerBeatCost, ExpireCost, WorthwhileCapS, GraceS float64
	MinPrefixTokens int
}
var ErrTTLUnset = errors.New("ttl_s 须 > 0（先跑 experiments/cache-ttl 套件实测）")
type NoCachePriceError struct{ Key string }
func Compute(b prices.PriceBook, pv prices.PriceVersion, ttlS float64, prefixTokens int,
	beatOutTokens, safety, graceS float64, minPrefixTokens int) (HeartbeatPolicy, error)
func TierFor(p HeartbeatPolicy, waitS float64) string // "none"|"beat"|"expire"
func StrategyCosts(b prices.PriceBook, pv prices.PriceVersion, ttlS, waitS float64,
	prefixTokens int, beatOutTokens, compactRatio float64) (map[string]float64, error)
// viewer 原三函数并入本包（签名不变）：
func Derive(p Params) (Result, error)
func SimulateBeats(t0, windowEnd float64, r Result) []float64
func DoNothingCost(durS, ttlS float64, r Result) float64
```

- [ ] **Step 1**: 两份测试各自成文件移植（重名按 C1b 消歧）。
- [ ] **Step 2**: 实现三函数（公式四式逐字：τ=safety·T；perBeat=S/per·pCache+beatOut/per·POut；expire=S/per·PIn；cap=τ·(PIn−PCache)/perBeat，perBeat≤0 → +Inf）。
- [ ] **Step 3**: viewer 副本销毁；全仓 grep 无第二份公式实现；`go test ./...` 全绿。
- [ ] **Step 4**: Commit `feat(go): T4 policy 移植+公式单源收口（规格 tests/test_policy.py）`。

---

### Task 5: internal/accounts（规格 accounts.py + test_accounts.py 19 例）

**Files:** Create `internal/accounts/accounts.go`、`accounts_test.go`

**Interfaces:**

```go
const SchemaV = 1
type Accounts struct{ dir string; mu sync.Mutex }
func New(dataDir string) (*Accounts, error)
type Fields map[string]any // 值仅 string/float64/int/nil
func (a *Accounts) Record(kind string, ts float64, f Fields) (map[string]any, error)
	// ts<0 → clock.Now()；中文报错原文；落盘行键序：v,kind,ts(Round 3),ts_iso,agent,session_id,lineage_id,project + kind 字段字母序
func (a *Accounts) Read(o ReadOpts) []map[string]any
type ReadOpts struct{ Since, Until float64; Project, Session, Lineage, Kind string }
```

- 白名单 10 科目逐字（accounts.py:20-45）；ts_iso 本地时区 `2006-01-02T15:04:05-0700`；月度文件 `200601`；坏行 stderr 告警。
- [ ] **Step 1**: 19 例全量移植（白名单拒绝/缺必填/保留字/月度滚动/过滤读）。
- [ ] **Step 2**: 实现（有序拼接 JSON）+ 绿 + Commit `feat(go): T5 accounts 移植（规格 tests/test_accounts.py）`。

---

### Task 6: internal/config（规格 config.py + test_config.py）

**Files:** Create `internal/config/config.go`、`config_test.go`

**Interfaces:**

```go
const FerryWallTimeoutS = 480.0
type WatchCfg / ThresholdCfg / ServerCfg / NotifyCfg / HeartbeatCfg / QuestionWatchCfg / Config struct{...} // 字段=Python dataclass 1:1，默认值逐字
func (c *Config) DataDir() string
func (c *Config) ThresholdFor(agent string) ThresholdCfg
func Load(path string, relaxMinGap bool) (*Config, error) // ""→$FERRYMAN_CONFIG→~/ferryman/config.toml
func Validate(c *Config, relaxMinGap bool) error          // 校验文案逐字；lead 下限夹取副作用+两类告警打印保留
```

- [ ] **Step 1**: test_config.py 全量移植。
- [ ] **Step 2**: 实现 + 绿 + Commit `feat(go): T6 config 移植（规格 tests/test_config.py）`。

---

### Task 7: internal/cctrans（规格 transcripts.py + test_transcripts.py）

**Files:** Create `internal/cctrans/cctrans.go`、`cctrans_test.go`

**Interfaces:**

```go
type Turn struct{ TS float64; CacheRead, CacheCreation, InputTokens int }
func (t Turn) CtxTokens() int
func AssistantTurns(path string) []Turn
func HasDanglingToolUse(path string) bool
func HasDanglingToolUseWindow(path string, tailBytes int64) bool
func AITitle(path string) string
func FirstUserMessageHash(path string) string
func HasAsyncLaunch(path string) bool
```

- **行读法（C7 纪律）**：`bufio.Reader.ReadBytes('\n')` 循环——**无行长上限**（对齐 Python 按行迭代；Scanner 的 ErrTooLong 会断流，禁用）。子串预筛（`'"usage"' in line` 等）保留。
- 尾窗读法：Stat→ReadAt 尾段→首行半行丢弃。
- [ ] **Step 1**: test_transcripts.py 全量移植。
- [ ] **Step 2**: 实现 + 绿 + Commit `feat(go): T7 cctrans 移植（规格 tests/test_transcripts.py）`。

---

### Task 8: internal/extract（规格 extract.py + test_extract.py）

**Files:** Create `internal/extract/extract.go`、`extract_test.go`

**Interfaces:**

```go
type FileCount struct{ Path string; Count int }
type Choice struct{ Question string; Labels []string }
type Facts struct {
	Source string; Title, Cwd string
	FirstTS, LastTS string; NTurns, PeakCtx int
	Files []FileCount; Commands []string; TotalInputTokens int
	FreezeUser, FreezeAsstText string; FreezeTools []string; FreezeChoice *Choice
}
type Item struct{ Role, Text string }
func TokenEstimate(text string) int
func Extract(path string) (Facts, []Item, []cctrans.Turn)
func MaterialText(f Facts, items []Item) string
func ChunkItems(items []Item, budgetTokens int) [][]Item
func (f *Facts) SkeletonText() string
```

- CJK 正则平移为 `[\x{3000}-\x{9FFF}\x{FF31}-\x{FFEF}]`（**按码点计数**）；截断全按码点（Item 4000/命令 160/FreezeUser 500/FreezeAsst 1500/标题 60，尾标逐字）；末段定格三档互斥；files 排序 (-count, path)；行读同 T7 无上限。
- [ ] **Step 1**: test_extract.py 全量移植（骨架文案断言逐字）。
- [ ] **Step 2**: 实现 + 绿 + Commit `feat(go): T8 extract 移植（规格 tests/test_extract.py）`。

---

### Task 9: internal/codextrans（规格 codex_transcripts.py + test_codex_transcripts.py / test_codex_extract.py）

**Files:** Create `internal/codextrans/codextrans.go`、`test_codex_transcripts_test.go`、`test_codex_extract_test.go`（**两源各自成文件**，C1b）

**Interfaces:**

```go
type XCTurn struct{ TS float64; InputTokens, Cached, CacheWrite int } // input 已含 cached
func TokenCountTurns(path string) []XCTurn
func SessionCwd(path string) string
func ExtractCodex(path string) (extract.Facts, []extract.Item)
```

- 五个注入前缀逐字；apply_patch 三前缀；行读无上限。
- [ ] **Step 1**: 两测试文件各自移植。
- [ ] **Step 2**: 实现 + 绿 + Commit `feat(go): T9 codextrans 移植（规格 tests/test_codex_transcripts.py + test_codex_extract.py）`。

---

### Task 10: internal/qwatch 检测器（规格 qwatch.py + test_qwatch.py）

**Files:** Create `internal/qwatch/qwatch.go`、`qwatch_test.go`

**Interfaces:**

```go
const AskUserQuestion = "AskUserQuestion"
const DefaultMinQuestions = 5
const TailBytes = 262144
const MissIdleS, MissLookbackS = 600.0, 1800.0
type Breakdown struct{ MarkerLines, QmarkLines, QualifiedNumberedLines int }
func (b Breakdown) Total() int
type Verdict struct{ IsSurge bool; UnitCount int; BD Breakdown; AskUserQuestionDangling bool }
func Detect(path string, minQuestions int) Verdict
func DetectTail(path string, minQuestions int, tailBytes int64) Verdict
func CorrelateMissSignals(rows []map[string]any) int
```

- 正则平移（C8 全规则，含 Unicode 字符类）：marker `(?i)\*\*Q[\t\n\v\f\r \x{1C}-\x{1F}\x{85}\x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}]?\p{Nd}+`；numbered `^[\t\n\v\f\r \x{...同上}...]*(?:\p{Nd}{1,3}[\t\n\v\f\r ...\x{3000}]*[.、)]|[-*•·])[\t\n\v\f\r ...]*`（Python `\s` 集合的显式展开，`{1,3}` 修饰 `\p{Nd}`）；code fence `(?s)"```".*?(```|\z)`；疑问词表 16 词逐字。
- **测试必须含全角样本**：`**Q １**`（全角数字）、`１． 步骤`（全角数字+全角句点+前导全角空格）等行，期望与 Python 实测分桶一致（写用例前先 `uv run python` 跑 qwatch._classify 钉死期望）。
- 行归桶优先级与真空真语义逐字。
- [ ] **Step 1**: test_qwatch.py 全量移植 + 全角样本补充用例。
- [ ] **Step 2**: 实现 + 绿 + Commit `feat(go): T10 qwatch 检测器移植（规格 tests/test_qwatch.py）`。

---

### Task 11: internal/beat 纯逻辑（规格 beat.py + scheduler 纯逻辑部分）

**Files:** Create `internal/beat/beat.go`、`beat_test.go`

**Interfaces:**

```go
const HitRatioThreshold = 0.5
const (OutHit = "hit"; OutMiss = "miss"; OutError = "error"; OutObserve = "observe")
type BeatPlan struct{ Agent, SessionID, TranscriptPath string; OpenedTS, LastWrite float64; Size, BeatIndex int; BeatTS float64 }
type BeatResult struct{ Sent, OK bool; InputTokens, CacheReadTokens, OutputTokens int; CostPred, CostActual float64; Provider, Model, Err string }
type Sender interface{ Send(BeatPlan) BeatResult }
type NoopSender struct{}
func (NoopSender) Send(BeatPlan) BeatResult
func Classify(r BeatResult) string
type Breaker struct{ MissStreak, ErrorStreak int } // MISS_LIMIT=2 ERROR_LIMIT=3
func (b *Breaker) Record(outcome string) string
type QWatchStats struct{ /* mu */ }
func NewQWatchStats() *QWatchStats
func (s *QWatchStats) RecordHit() / RecordWindowOpened() / RecordBeat(outcome string, costActual float64) / Snapshot() map[string]any
```

- HttpBeatSender 不移植（Q14 未授权，接口位保留）。
- [ ] **Step 1**: 纯逻辑用例移植。**Step 2**: 实现 + 绿 + Commit `feat(go): T11 beat 纯逻辑移植`。

---

### Task 12: internal/ledger（规格 ledger.py + test_ledger.py）

**Files:** Create `internal/ledger/ledger.go`、`ledger_test.go`

**Interfaces:**

```go
const SubagentEventLeakS = 3600.0
type QSnap struct{ MTime float64; Size int }
type SessionState struct {
	Agent, SessionID, TranscriptPath, Cwd, Title string
	LastWrite float64; Size, PeakCtx int
	ObservedActive bool; HandedOffAt, EnrichedWrite float64 // EnrichedWrite 初值 -1
	QWatchOpenedTS *float64; QWatchBeatsFired int
	QWatchPlan []float64; QWatchSnapshot *QSnap
}
type Ledger struct{ /* mu sync.Mutex; byKey/byPath/subagents */ }
func New() *Ledger
func (l *Ledger) Mu() *sync.Mutex
func (l *Ledger) Touch(agent, sid, path string, mtime float64, size int, daemonStartedAt float64) *SessionState
func (l *Ledger) TouchFull(agent, sid, path string, mtime float64, size int, cwd, title string, peakCtx int, daemonStartedAt float64) *SessionState
func (l *Ledger) Get / GetByPath / AllSessions / LastTranscriptWrite
func (l *Ledger) SubagentEvent(agent, sid, event string) int / SubagentActive / SubagentsActiveCount / MarkHandedOff
// 供 daemon 临界区用的无锁内方法（C6）：
func (l *Ledger) SubagentActiveLocked(agent, sid string) bool
```

- Touch 语义逐字（lineage 继承、size=0 不覆盖、mtime 推进+observed_active+清 qwatch 四字段）；返回共享可变引用（改动须持 Mu，注释注明）。
- [ ] **Step 1**: test_ledger.py 全量移植。**Step 2**: 实现 + 绿 + Commit `feat(go): T12 ledger 移植（规格 tests/test_ledger.py）`。

---

### Task 13: internal/store（规格 store.py + test_store.py）

**Files:** Create `internal/store/store.go`、`store_test.go`

**Interfaces:**

```go
const FreshWindowS, CoversToleranceS = 86400.0, 60.0
type Entry struct { // ← json tag 显式钉死 snake_case（C7；直接序列化会输出 CamelCase）
	HandoffID   string   `json:"handoff_id"`
	SessionID   string   `json:"session_id"`
	Agent       string   `json:"agent"`
	Cwd         string   `json:"cwd"`
	Title       string   `json:"title"`
	CreatedAt   string   `json:"created_at"`
	CoversUntil string   `json:"covers_until"`
	CoversUntilS float64 `json:"covers_until_s"`
	Status      string   `json:"status"`
	Path        string   `json:"path"`
	BlockedAt   *string  `json:"blocked_at"` // nil ↔ Python None
	Injected    []string `json:"injected"`
}
type Store struct{ /* mu; dir; indexPath; index */ }
func New(dataDir string) (*Store, error)
func (s *Store) SaveHandoff(sessionID, agent, cwd, title, coversUntilISO, status, md string) Entry
func (s *Store) ValidHandoff(agent, cwd string, lastWrite float64) *Entry
func (s *Store) MarkBlocked(id string)
func (s *Store) SavePendingPrompt(sessionID, prompt string) // >500 token 截断+「…(超长截断)」
func (s *Store) PopPendingPrompt(sessionID, consumeFor string) string
func (s *Store) RestoreCandidates(agent, cwd string) []Entry
func (s *Store) MarkInjected(id, sessionID string)
func (s *Store) ReadHandoff(e Entry) string
```

- 序列化断言：**测试必须直接比对落盘 JSON 键名**（含 blocked_at=null、injected=[] 空值语义）。
- [ ] **Step 1**: test_store.py 全量移植 + 键名断言。**Step 2**: 实现 + 绿 + Commit `feat(go): T13 store 移植（规格 tests/test_store.py）`。

---

### Task 14: internal/harvest（规格 harvest.py + test_harvest.py）

**Files:** Create `internal/harvest/harvest.go`、`harvest_test.go`

**Interfaces:**

```go
type Row struct {
	TS *float64 // nil=坏行无 timestamp
	Model, MsgID string
	InputTokens, CacheReadTokens, CacheCreationTokens, OutputTokens int
	Title, Project string; Offset int64
}
func ParseUsageChunk(text, title, cwd string) (rows []Row, outTitle, outCwd string)
type HarvestState struct{ /* offsets/titles/cwds/msgSeen */ }
func NewHarvestState(a *accounts.Accounts) *HarvestState
func (h *HarvestState) MaybeHarvest(path string, size int64, agent string) []Row
```

- 增量语义逐字（残行/重采/去重/at-most-once）。
- [ ] **Step 1**: test_harvest.py 全量移植。**Step 2**: 实现 + 绿 + Commit `feat(go): T14 harvest 移植（规格 tests/test_harvest.py）`。

---

### Task 15: internal/notify（规格 notify.py + test_notify.py）

**Files:** Create `internal/notify/notify.go`、`notify_test.go`

**Interfaces:**

```go
var PushoverURL = "https://api.pushover.net/1/messages.json" // 测试可换
func SendPushover(title, message, token, user string) bool
func SendToast(title, message string) bool // powershell WinRT；命令执行提为包级 var 供 mock
func NotifyAlert(title, message string, cfg *config.Config)
func NotifyBlock(handoffPath, agent, sessionID string, cfg *config.Config)
```

- [ ] **Step 1**: test_notify.py 全量移植（httptest + mock）。**Step 2**: 实现 + 绿 + Commit `feat(go): T15 notify 移植（规格 tests/test_notify.py）`。

---

### Task 16: internal/daemon·窗口与停车（规格 server.py:102-593 + test_subagent.py + test_merge_t51_parking_mutex.py）

**Files:** Create `internal/daemon/daemon.go`、`windows.go` + `subagent_test.go`、`test_merge_t51_parking_mutex_test.go`（两源各自成文件）

**Interfaces:**

```go
const (DegradeAfterBlocks = 3; PendingTTLs = 86400.0; WarnContextCap = 2000
	HealthGraceS = 600.0; ParkExpireS = 3600.0; AckGraceS = 90.0; QWatchMissScanS = 86400.0)
type GateStats struct{ /* mu; Total int; ByAgent map[string]int; Bypass, Blocks, Warns, SubagentEvents int; LastCall float64 */ }
func (s *GateStats) Hit(agent string)
type PendingTable struct{ /* mu */ }
func (p *PendingTable) Get / Set / BumpBlocks / Clear
type winKey = [2]string
type waitWindow struct{ OpenedTS float64; StopTS *float64; SawAsync bool }
type Daemon struct {
	Cfg *config.Config; Ledger *ledger.Ledger; Store *store.Store
	EnqueueFerry func(*ledger.SessionState) bool
	Accounts *accounts.Accounts
	Stats *GateStats; Pending *PendingTable
	QWatchStats *beat.QWatchStats
	windowsMu sync.Mutex // C6 外层锁
	windows map[winKey]*waitWindow
	StartedAt float64
}
func NewDaemon(...) *Daemon
func (d *Daemon) Subagent(body map[string]any) (map[string]any, error)
func (d *Daemon) WindowWait(agent, sid string) bool
func (d *Daemon) ParkingOpen(agent, sid string) bool
func (d *Daemon) NoteUsage(agent, sid string, ts float64)
func (d *Daemon) QWatchStop() map[string]any
func (d *Daemon) Acct(kind string, st *ledger.SessionState, agentOverride, sidOverride, lineageOverride string, fields accounts.Fields)
```

**锁范式（样板，T17/T19 沿用）**——Python"ledger.lock 临界区+GIL 无锁读"翻成 Go **双锁同序**：

```go
// 开等答复窗（watcher 侧）：
w.daemon.windowsMu.Lock(); defer w.daemon.windowsMu.Unlock()
w.ledger.Mu().Lock(); defer w.ledger.Mu().Unlock()
if st.QWatchOpenedTS != nil { return false }
if w.ledger.SubagentActiveLocked(st.Agent, st.SessionID) { return false }
if w.daemon.parkingOpenLocked(st.Agent, st.SessionID) { return false }
// …置窗字段
```

- 停车/重锚/锁存/闭窗四因语义逐字（server.py:365-562），注释一并搬运。
- [ ] **Step 1**: test_subagent.py + test_merge_t51_parking_mutex.py 各自成文件移植（并发用例 goroutine+WaitGroup；有 gcc 加 -race）。**Step 2**: 实现 + 绿 + Commit `feat(go): T16 daemon 窗口/停车移植——双锁同序范式（规格 tests/test_subagent.py + test_merge_t51_parking_mutex.py）`。

---

### Task 17: internal/daemon·闸门状态机与归还（规格 server.py:135-500 + test_gate.py 43 例）

**Files:** Create `internal/daemon/gate.go`、`gate_test.go`、`restore.go`、`restore_test.go`

**Interfaces:**

```go
func (d *Daemon) Gate(body map[string]any) map[string]any
	// 键：decision/reason/additional_context/suppressOriginalPrompt/handoff_path
func (d *Daemon) Restore(agent, cwd, sessionID string) map[string]any
func (d *Daemon) Health() map[string]any // /stats 字段名逐字（server.py:640-673）
```

- 七分支顺序/block reason 全文/warn 文案 2000 截断/cache_info 纯提醒/pending 清除三条件/observe 文案（will_block=False）——全部逐字（server.py:135-500）。
- [ ] **Step 1**: test_gate.py 全量 43 例（clock 注入+直构 Daemon）。**Step 2**: 实现 + 绿 + Commit `feat(go): T17 闸门状态机+归还移植（规格 tests/test_gate.py）`。

---

### Task 18: internal/daemon·httpapi（规格 server.py:676-781 + test_singleton.py）

**Files:** Create `internal/daemon/httpapi.go`、`httpapi_test.go`

**Interfaces:**

```go
func EnsureToken(dataDir string) (string, error)
func AlreadyRunning(port int, token string) bool
type DaemonLike interface {
	Gate(map[string]any) map[string]any
	Subagent(map[string]any) (map[string]any, error)
	QWatchStop() map[string]any
	Restore(agent, cwd, sessionID string) map[string]any
	Health() map[string]any
}
func ListenAndServe(d DaemonLike, port int, token string) (net.Listener, *http.Server, error)
```

- 401/404/400/200 全路径、Bearer、Content-Type、访问日志静默、/restore query 缺省——逐字；Go net.Listen 天然排他（Windows 双绑必败=Python 禁 SO_REUSEADDR 等价）。
- [ ] **Step 1**: test_singleton.py 移植。**Step 2**: 实现 + 绿 + Commit `feat(go): T18 httpapi 移植（规格 tests/test_singleton.py）`。

---

### Task 19: internal/daemon·watcher（规格 daemon.py + test_qwatch_window/scheduler/events/e2e.py）

**Files:** Create `internal/daemon/watcher.go` + 四个对应测试文件（`test_qwatch_window_test.go`、`test_qwatch_scheduler_test.go`、`test_qwatch_events_test.go`、`test_qwatch_e2e_test.go`，C1b 一对一）

**Interfaces:**

```go
func CodexWatchDirs(w config.WatchCfg, home string) []string // Orca runtime 目录路径逐字
type Watcher struct {
	Cfg *config.Config; Ledger *ledger.Ledger; Store *store.Store
	Enqueue func(*ledger.SessionState) bool; StartedAt float64
	Accounts *accounts.Accounts; Daemon *Daemon // nil=无接线（旧测试形态）
	BeatSender beat.Sender // nil→Noop+enforce 告警一次
	QWatchStats *beat.QWatchStats
	/* qwatchSeen/qwatchHitSeen 版本章；beatInFlight；breaker */
	harvest *harvest.HarvestState
}
func NewWatcher(...) *Watcher
func (w *Watcher) Run(ctx context.Context)
func (w *Watcher) PollOnce() // 测试直调
```

- 轮询序列/入队五道推迟/qwatch 四条件+版本章+双锁临界区/心跳调度两道验（临界区内 os.Stat 有意为之，注释搬运）/usage 喂入——逐字（daemon.py:35-490）。行读无上限。
- [ ] **Step 1**: 四文件全量移植（32+26+events+e2e）。**Step 2**: 实现 + 绿（有 gcc -race）+ Commit `feat(go): T19 watcher 移植（规格 test_qwatch_window/scheduler/events/e2e.py）`。

---

### Task 20: internal/daemon·worker 与 serve 装配（规格 daemon.py:493-663 + test_integration.py + test_big_session.py）

**Files:** Create `internal/daemon/worker.go`、`serve.go`、`test_integration_test.go`、`test_big_session_test.go`

**Interfaces:**

```go
type ferryFunc func(path string, pr Provider, timeoutS float64, agent string) (string, map[string]any, error)
	// T22 之前的依赖占位（Worker 经此注入；T22 接真实现）
type Worker struct{ /* tasks chan (cap 10); cfg; store; accounts; ferry ferryFunc; providers */ }
func NewWorker(cfg, st, acc, providers, f ferryFunc) *Worker
func (w *Worker) Run(ctx context.Context) // 墙钟 480s ctx 超时→骨架降级+记账 failed；成功→fresh+记账
func Serve(relaxMinGap bool) int // = python serve() 全装配
```

- 队列 `select default` 满延迟；记账 price_ver 取流水时刻版本；失败一行 failed 在骨架保存后；骨架头/横幅/pid 逐字。
- **成功路径装配测试（rev1 新增，#11）**：Worker 注入**恒成功** ferryFunc（httptest 假 provider 形状）跑一单——断言 fresh 交接落盘、账本 handoff 行 outcome=fresh 字段齐全、`Store.ValidHandoff` 命中。
- [ ] **Step 1**: test_integration.py + test_big_session.py 移植（恒败替身走骨架路径）+ 成功路径装配测试（恒成功替身）。**Step 2**: 实现 + 绿 + Commit `feat(go): T20 worker+serve 装配移植（含成功路径装配测试）（规格 tests/test_integration.py + test_big_session.py）`。

---

### Task 21: hooks 端到端测试移植（规格 tests/test_hooks.py 16 例——真跑 powershell）

**Files:** Create `internal/installer/test_hooks_test.go`

- [ ] **Step 1**: 16 例移植（powershell 子进程、stdin JSON、exit/断言照 test_hooks.py 实测；仅 Windows 执行其余 Skip）。
- [ ] **Step 2**: 绿 + Commit `feat(go): T21 hooks PS1 端到端测试移植（规格 tests/test_hooks.py）`。

---

### Task 22: internal/ferry 摆渡执行器（规格 ferry.py + test_ferry_providers.py）

**Files:** Create `internal/ferry/ferry.go`、`test_ferry_providers_test.go`

**Interfaces:**

```go
const (InjectOpen = "<<<INJECT>>>"; InjectClose = "<<</INJECT>>>"; InjectBudget = 2200
	FullBudget = 8000; PromptReserve = 4096; WindowGuard = 8192)
const SystemPrompt = `…逐字平移 ferry.py:31-58…`
type Provider struct{ Name, BaseURL, Model, APIKey string; Window int } // 缺省 131072
func LoadProviders(path string) map[string]Provider
func Chat(pr Provider, system, user string, timeoutS float64, maxTokens int) (string, map[string]any, error)
func TrimInjectLayer(text string) string
func ParseOutput(reply string) (inject, full string)
func HandoffMarkdown(title, inject, full string, meta map[string]any) string
func FerrySession(path string, pr Provider, timeoutS float64, agent string) (string, map[string]any, error)
	// L1/L2 语义与 meta 键逐字（wall_s=Round 1；usage 三键汇总）
```

- Chat 用 http.NewRequestWithContext（goroutine 不悬挂）。
- [ ] **Step 1**: test_ferry_providers.py 全量移植（httptest 假端点）。**Step 2**: 实现 + 绿 + 把 T20 占位换真调用（测试重跑）+ Commit `feat(go): T22 ferry 执行器移植（规格 tests/test_ferry_providers.py）`。

---

### Task 23: internal/report 报表（规格 report.py + test_report.py）

**Files:** Create `internal/report/report.go`、`test_report_test.go`

**Interfaces:**

```go
const (SavingsFormula = "v1"; StrategyFormula = "v1"; CompactRatio = 0.25)
var StrategyCaveats = [...]string{ /* 两段逐字 report.py:26-32 */ }
func HandoffCost(e map[string]any, books map[string]prices.PriceBook) *float64
func SavingsV1(entries []map[string]any, books, econBook) map[string]any // net=mathx.Round(...,4)
func StrategyTable(...) map[string]any
func RenderText(...) string // 中文模板逐字
func Run(args Args) int
```

- [ ] **Step 1**: test_report.py 全量移植。**Step 2**: 实现 + 绿 + Commit `feat(go): T23 report 移植（规格 tests/test_report.py）`。

---

### Task 24: internal/installer——install/doctor（规格 install.py + doctor.py + test_install/test_ccswitch/test_doctor.py）

**Files:** Create `internal/installer/install.go`、`ccswitch.go`、`doctor.go` + 三个测试文件（C1b 一对一）；Modify go.mod（+modernc.org/sqlite）

**Interfaces:**

```go
var (CCSwitchDBPath(), CodexHooksPath(), CodexConfigPath() /* HOME 注入 */; LauncherName = "start-daemon.cmd")
func EnsureLauncher(dataDir, exePath string) string // `start "" /min "<exe>" serve >> …`（C9 隐藏窗口归启动方）
func FerryHookEntries(repo string, events []string) map[string]any // 四事件条目结构逐字；events 子集筛选（C12）
func InstallCC(settingsPath, dbPath, dataDir, repo string, events []string) int // 默认全集；可传子集
func InjectCCSwitch(dbPath string, events []string) int  // sqlite 幂等注入；备份留 3
func InstallCodex(hooksPath, configPath, repo string) int
func CheckCCHooks(...) // 闸门事件缺位=提示不失败（C12）；其余事件缺失/路径失效/超时不足照旧失败
func CheckHookScripts / CheckCCSwitch / CheckCodex / CheckDaemon / CheckFerryProvider / CheckLauncher
func RunDoctor() int
```

- [ ] **Step 1**: 三个测试文件移植 + 子集安装用例（装 {restore, subagent} 三类后 settings.json 只含这三事件）。
- [ ] **Step 2**: 实现 + 绿 + Commit `feat(go): T24 installer 移植——点火脚本指 exe+事件子集安装（规格 tests/test_install+ccswitch+doctor.py）`。

---

### Task 25: cmd/ferryman 合并 exe（吸收 cmd/viewer）

**Files:**
- Create: `cmd/ferryman/main.go`（子命令+面板+托盘；资产 `cmd/ferryman/web/`、`cmd/ferryman/icon.ico` 随包搬入）
- Delete: `cmd/viewer/`
- Create: `build.ps1`（**console 子系统**：`go build -o ferryman.exe ./cmd/ferryman`；附 `--release` 开关产出 `-ldflags "-s -w"` 变体，均无 windowsgui）

**Interfaces（行为面）:**

```
ferryman.exe                          # = serve：守护(7311)+面板(15900)+托盘；console 窗口由启动方隐藏
ferryman.exe serve                    # 同上（点火脚本 start /min 调它）
ferryman.exe doctor / install-cc / install-ccswitch / install-codex / account report   # 终端输出全部可见（C9）
ferryman.exe --demo / --port N / --no-tray / --no-browser / --install-shortcuts
```

- 快捷方式安装：WindowStyle=minimized（双击不闪黑窗、托盘常驻）。
- [ ] **Step 1**: 手测矩阵：serve 全链（双端口+托盘）、doctor 终端输出、--demo、install-shortcuts、终端裸跑 `ferryman.exe serve` 能看到横幅（C9 验证点）。
- [ ] **Step 2**: `go vet ./... && go test ./...` 全绿。
- [ ] **Step 3**: Commit `feat(go): T25 合并 exe——console 子系统，CLI/守护/面板单进程`。

---

### Task 26: 预切换验收

- [ ] **Step 1**: `go test ./...` 全绿清点——**对账基准 316 个移植测试**（C1 实测账目：387 − 71 冻结；hooks 16 计入 316）+ viewer 既有测试；有 gcc 加 `-race ./internal/daemon/...`。
- [ ] **Step 2**: 真实会话冒烟三链路（observe 配置）：①gate 警告注入；②真 provider 摆渡落 handoff+账本行；③/clear 归还注入完整（含待续 prompt）。
- [ ] **Step 3**: **enforce 沙箱冒烟（rev1 修正 #13，不依赖 hook）**：临时数据目录+enforce 配置起沙箱守护，直打 `POST /gate`（闲置会话样本）——断言 decision=block、reason 全文、suppressOriginalPrompt=true、handoff_path 存在；账本 block 行字段齐全。
- [ ] **Step 4**: Python 版与 Go 版账本行并排语义 diff（人工，字段级），证据追加到评估文档。

---

### Task 27: 切换执行（含回退工件化，rev1 修正 #5/#6）

- [ ] **Step 1**: `git tag archive/python-final && git push --tags`。
- [ ] **Step 2**: build.ps1 出 exe → 仓库根固定路径。
- [ ] **Step 3**: **生成回退工件**：写 `~/ferryman/rollback-to-python.cmd`——内容：`git worktree add <repo>-py archive/python-final` + `cd` + `uv sync` + 把 start-daemon.cmd 启动行指回 `<repo>-py` 的 venv python。**回退触发条件**（写入文件头注释）：切换后 24h 内出现 gate 误拦/漏拦不可热修、摆渡产物损坏、账本字段错乱——任一即回退。
- [ ] **Step 4**: **回退演练（删除 Python 之前）**：临时目录跑一遍 rollback-to-python.cmd，确认 venv 重建 + `python -m ferryman doctor` 可跑 + 钩子自举能拉起 Python 版——演练通过才允许 Step 7 的删除；`.venv` 此前**不删**。
- [ ] **Step 5**: 停 Python 守护；删 daemon.pid。
- [ ] **Step 6**: `ferryman.exe install-cc --events SessionStart,SubagentStart,SubagentStop`（**C12：三类钩子，UserPromptSubmit 闸门暂不装**——用户指令：防子代理久跑场景下主会话输入被吞；改进方向重构后再议）+ `install-codex`（同款子集）+ `--install-shortcuts`。
- [ ] **Step 7**: `ferryman.exe serve` 前台首启验横幅（console 子系统下可见，C9）→ 退出 → 钩子自举接管；`ferryman.exe doctor` 全绿（闸门事件缺位仅提示）。
- [ ] **Step 8**: 删除 ferryman/、tests/、pyproject.toml、uv.lock（.venv 保留至回退演练通过后手动清）；commit `feat(go): T27 一次性切换——Python 退役，tag archive/python-final + rollback 工件在位`。
- [ ] **Step 9**: 24h 观察清单（评估文档 §6）执行；问题当日热修（`fix(go):` 前缀）；触发回退条件 → 跑 rollback-to-python.cmd。

---

## Self-Review 记录（rev1）

- **规格覆盖**：21 模块 ↔ T2-T25 一一对应；316 可移植测试全落在各任务 Step 1（账目列死：387=71+316，hooks 16 含于 316）；冻结面 71 有 ADR-0005 声明。
- **占位扫描**：无 TBD；T20 占位 ferryFunc 是设计（T22 接真），成功路径装配测试补齐（#11）。
- **类型一致性**：跨任务签名以 Interfaces 块为准；ledger.QSnap / Daemon.EnqueueFerry / ferryFunc 形状一致；T20→T22 依赖编号已修正。
- **rev1 修订对照**：#1/#2→T1/T25 资产随包；#3/#4→C9 console 子系统+启动方隐藏；#5/#6→T27 Step 3/4 回退工件+演练；#7→T13 json tag+键名断言；#8/#9→T7 及全部行读改 ReadBytes 无上限；#10→C8 Unicode 字符类+全角样本用例；#11→T20 成功装配测试+编号修正；#12→C1b 文件/重名规则+T4/T9/T19 落地；#13→T26 Step 3 enforce 沙箱直打；#14→C1 账目 316；#15/#16→C2/T2 Round=FormatFloat（实验证据）；用户钩子指令→C12+T24+T27 Step 6+doctor 容忍。

---

## xcheck 评审附录 · 20260919-001437

> **以下为评审参考,以实际执行为准**(验证证据是评审时点的快照,代码可能已演进);
> 但"撞上关注项"的动作不是参考 —— 停下反馈用户,别默默绕过。

两轮盲评（codex + pi）：round 0 → 16 条必改已全部消化进本 rev1；round 1（对本 rev1）→ 14 条查实问题如下，**下游 spec/实施必须全部烘焙**。

### ① 查实的（下游必须落实，共 14 条）

1. **InstallCodex 加事件子集参数** + CLI `install-codex --events`（证据：T24 接口无该参数 × T27 Step 6 要求同款三类子集）
2. **T20 的 Provider 前置**：ferryFunc/NewWorker 引用的 Provider 在 T22 才定义——T20 需先在 daemon 包内定义最小占位类型（或把 Provider 定义提前到 T20 之前）
3. **T25 删除 cmd/viewer/ 前先把 viewer 测试迁入/改写到 cmd/ferryman**（证据：T25 Files × T26 验收矛盾）
4. **回退脚本幂等 + 演练隔离**：worktree 已存在时先清理或换路径；演练用临时 worktree 路径 + 临时启动器副本，绝不碰真实 start-daemon.cmd
5. **回退窗口期保留离线重建条件**：.venv 清理延后到 24h 观察期结束之后（否则真回退只剩 uv sync 裸奔网络）
6. **切换前全量备份 ~/ferryman 数据**（accounts/handoffs/index/config/token → 带时间戳目录），兼容冒烟用副本——Go 写坏账本时 git 恢复不了数据
7/8. **测试文件命名规则统一**：`test_x.py → x_test.go`（去 `test_` 前缀，如 codextrans 包内 codextrans_transcripts_test.go / codextrans_extract_test.go），或写明例外表——消除 C1b 与任务清单的矛盾
9/10. **回退触发条件改写**为与三类钩子安装面匹配的可观察信号：摆渡产物损坏 / 账本字段错乱 / SessionStart/Subagent 钩子异常 / 守护或面板崩溃不可热修；"gate 误拦/漏拦"条款删除（生产无 gate 调用可观察）
11. **T26 冒烟写明沙箱机制**：Go 沙箱守护 = 独立端口 + 独立数据目录，三链路用直打 API 触发（不依赖钩子、不占 7311）
12. **C8 \w 映射补 Nl/No**：`[\p{L}\p{Nd}\p{Nl}\p{No}_]`（Python \w 语义含 ½、Ⅳ 类）
13. **T12 SessionState 注释改"读写均须持 Mu"**（Go 对无锁读也会 -race 报警）
14. **切换日文档/doctor 输出声明 HttpBeatSender 功能退化**（Q14 未授权，observe 演练兜底）

### ② 实验已做的

- 舍入跨语言对照（round 0，.xcheck/20260918-235459/exp/）：140,168 组——naive `RoundToEven(x*10ⁿ)/10ⁿ` 109 失配、FormatFloat/ParseFloat 0 失配 → C2 已按实验改 FormatFloat 方案（本 rev1 已落实）。

### ③ 存疑的（开发时要盯）

- "切换前是否强制在有 GCC 的环境跑 `-race ./internal/daemon/...`"（round 0 codex 提出，无客观判据）——**触发**：daemon 并发测试编写/切换验收时；**命中动作**：本机有 gcc 就跑，没有则以 C6 锁序人工评审补偿并记账，别默默跳过。

修订版路径：无 rev2（夜间收工＝本清单随 spec 交下游实施）。产物目录：`.xcheck/20260918-235459/`（round 0）、`.xcheck/20260919-001437/`（round 1）。
