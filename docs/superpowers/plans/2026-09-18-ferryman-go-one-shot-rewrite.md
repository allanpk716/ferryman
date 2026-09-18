# Ferryman Go 一次性重写 · 执行计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 Python 后端（守护核心+摆渡执行器+CLI，21 模块/6631 行/约 330 个测试）整体移植为根部单一 Go module，产出合并版 `ferryman.exe`，最后一次性替换 Python（无并行期、无烧机期）。

**Architecture:** 仓库根立 `module ferryman`；viewer 收编为 `internal/viewer` + `web/`；守护核心按 Python 模块一一对应拆包；公式收口到唯一 `internal/policy`；daemon 包内统一锁序 windowsMu→ledgerMu。行为规格 = 既有 Python 测试逐条移植（先测试后实现），内部结构允许自由重构。

**Tech Stack:** Go 1.23（net/http 标准库路由、BurntSushi/toml、getlantern/systray 既有依赖 + modernc.org/sqlite 新增；禁 CGO）。

**Spec:** `docs/adr/0003-backend-migrate-to-go.md`（路线）、`docs/adr/0005-one-shot-cutover-go-rewrite.md`（切换策略）、`docs/20260918_2311_Python到Go一次性迁移_评估.md`（评估，含风险与切换手册）。Python 源码与测试即逐字段规格。

## Global Constraints（每任务隐含遵守）

- **C1 规格=测试**：目标模块的 Python 测试全量移植为 Go 测试（文件级 1:1，每个 `def test_X` 对应一个 `func TestX`，用例数据逐条照抄），红→实现→绿→提交。冻结面（e0*/eval*）测试不迁。
- **C2 舍入**：Python `round()`=half-even。一律 `mathx.Round(x, n)`；账本 ts=3、dur=1、beat 成本=6、报表净额=4 位。
- **C3 时间**：float64 epoch 秒；mtime=ModTime().UnixNano()/1e9；测试经 `clock.Now` 注入。
- **C4 码点**：Python len/切片按码点——Go 用 `mathx.RuneLen`/`mathx.RuneTrunc`（T2 提供），禁止裸 len/切片处理用户文本。
- **C5 路径键**：`pathsx.NormPath`（`\`→`/` + ToLower）是 lineage 唯一键形。
- **C6 锁序**：daemon 包内 **windowsMu（外）→ ledger.Mu()（内）**，双资源临界区两把同序全拿；ledger 公共方法自带锁、临界区用无锁内方法；Go mutex 不可重入，禁止同 goroutine 重复加锁。
- **C7 兼容**：5 端点（/gate /subagent /qwatch_stop /stats /restore）+ Bearer + JSON 字段名、账本白名单、index.json 结构、交接 MD/警告/日志中文文案——逐字段/逐字平移。
- **C8 正则**：RE2；`\Z`→`\z`，DOTALL→`(?s)`，IGNORECASE→`(?i)`；包级 var 预编译。
- **C9 无 CGO**；构建 `go build -ldflags "-H windowsgui" -o ferryman.exe ./cmd/ferryman`。
- **C10 提交**：每任务一 commit，`feat(go): T<N> <pkg>——<一句话>（规格 tests/test_x.py）`。
- **C11 Python 侧只读**：本战役不修 Python 行为（bugfix 例外需用户点头）；T27 之前 Python 继续服役。

---

### Task 1: 仓库收形——根 module 化，viewer 搬入

**Files:**
- Create: `go.mod`（根，`module ferryman`，依赖/版本照抄 viewer/go.mod）
- Create: `cmd/viewer/main.go`（= viewer/main.go，仅改 import 前缀）
- Create: `internal/viewer/{server,demo,ledger}/`（= viewer/internal/ 同名包原样搬移改前缀）
- Create: `web/`、`icon.ico`（= viewer/web、viewer/icon.ico 原样搬移，main.go 的 embed 路径相应调整）
- Delete: `viewer/`（go.mod/go.sum/main.go/internal/web 全部，exe 产物不入库）
- Test: 各包 `_test.go` 随包搬移

**Interfaces:**
- Produces: `ferryman/viewer/internal/...` → `ferryman/internal/viewer/...`；后续任务全部 import `ferryman/internal/...` 前缀。T4 将删除 `internal/viewer/policy`（现仅保留搬移占位）。

**Steps:**

- [ ] **Step 1**: 移动文件（git mv 保历史）：`viewer/internal/server→internal/viewer/server`、`demo→internal/viewer/demo`、`ledger→internal/viewer/ledger`、`policy→internal/viewer/policy`（临时，T4 删）、`viewer/web→web`、`viewer/icon.ico→icon.ico`、`viewer/main.go→cmd/viewer/main.go`、`viewer/main_test.go→cmd/viewer/main_test.go`。
- [ ] **Step 2**: 根 `go.mod`：module 名 `ferryman`，require 照抄（BurntSushi/toml、getlantern/systray 及 indirect）。`go mod tidy`。
- [ ] **Step 3**: 全局替换 import：`ferryman/viewer/internal/` → `ferryman/internal/viewer/`；embed 路径 `web`/`icon.ico` 对齐新位置。
- [ ] **Step 4**: `go build ./... && go test ./...` 全绿（此刻仍只有 viewer 家族测试）。
- [ ] **Step 5**: 手工冒烟 `go run ./cmd/viewer --demo --no-tray --no-browser` 能起。
- [ ] **Step 6**: Commit `feat(go): T1 仓库收形——viewer 并入根 module ferryman`。

---

### Task 2: 基础件 mathx / clock / pathsx

**Files:**
- Create: `internal/mathx/mathx.go`、`internal/mathx/mathx_test.go`
- Create: `internal/clock/clock.go`、`internal/clock/clock_test.go`
- Create: `internal/pathsx/pathsx.go`、`internal/pathsx/pathsx_test.go`

**Interfaces:**
- Produces（全战役依赖）:

```go
// mathx
func Round(x float64, places int) float64          // half-even：math.RoundToEven(x*10^n)/10^n
func RuneLen(s string) int                          // utf8.RuneCountInString
func RuneTrunc(s string, cap int) string            // 按码点截断；s 不超长返回原串
// clock
var Now = func() float64 { return float64(time.Now().UnixNano()) / 1e9 } // 测试可整体替换
// pathsx
func NormPath(p string) string                      // 反斜杠→正斜杠 + strings.ToLower
```

- [ ] **Step 1: 失败测试**（对照 Python round 语义，钉死边界）：

```go
func TestRoundHalfEven(t *testing.T) {
	cases := []struct{ x float64; p int; want float64 }{
		{2.5, 0, 2}, {3.5, 0, 4}, {0.125, 2, 0.12}, {0.135, 2, 0.14}, // Python round(0.125,2)==0.12
		{-2.5, 0, -2}, {1.0005, 3, 1.0}, // 浮点表示陷阱：以实际 Python 输出为准逐条核对
	}
	for _, c := range cases {
		if got := mathx.Round(c.x, c.p); got != c.want {
			t.Errorf("Round(%v,%d)=%v want %v", c.x, c.p, got, c.want)
		}
	}
}
```

外加：RuneTrunc 截中文不出乱码、超长返回原串、NormPath(`C:\A\B.MD`)==`c:/a/b.md`。
（注：`{1.0005,3}` 这类浮点边界，写测试前先用 `uv run python -c "print(round(1.0005,3))"` 实测期望值——float64 表示决定 half-even 的落点，不许想当然。）

- [ ] **Step 2**: 实现（Round 用 RoundToEven；RuneTrunc 用 `[]rune`）。
- [ ] **Step 3**: `go test ./internal/mathx/ ./internal/clock/ ./internal/pathsx/ -v` 绿。
- [ ] **Step 4**: Commit `feat(go): T2 基础件 mathx/clock/pathsx`。

---

### Task 3: internal/prices（规格 ferryman/prices.py + tests/test_prices.py）

**Files:**
- Create: `internal/prices/prices.go`、`prices_test.go`

**Interfaces:**
- Consumes: BurntSushi/toml（解析 [prices.*]）
- Produces:

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
func (b *PriceBook) At(ts float64) *PriceVersion // 最后一个生效日 ≤ ts；无则 nil
func PriceTag(bookKey string, pv PriceVersion) string // "key@YYYY-MM-DD"
func LoadPrices(path string) map[string]PriceBook     // path=="" → ~/ferryman/config.toml；无文件/无节 → 空 map（不报错）
```

- [ ] **Step 1**: test_prices.py 全量移植（含：版本选价边界、p_cache 缺省、升序排序、tag 格式）。日期解析 `2006-01-02` + UTC。
- [ ] **Step 2**: 实现 + 绿 + Commit `feat(go): T3 prices 移植（规格 tests/test_prices.py）`。

---

### Task 4: internal/policy——公式单源落地点（规格 ferryman/policy.py + test_policy.py + viewer policy_test.go）

**Files:**
- Create: `internal/policy/policy.go`（Python policy.py 移植 + viewer 副本三函数并入）
- Create: `internal/policy/policy_test.go`（两份测试合一）
- Delete: `internal/viewer/policy/`（副本销毁）
- Modify: `internal/viewer/{server,demo}` 及 `cmd/viewer` 中对 viewer/policy 的引用 → `ferryman/internal/policy`

**Interfaces:**
- Consumes: internal/prices
- Produces:

```go
type HeartbeatPolicy struct {
	TTLS, TauS, PerBeatCost, ExpireCost, WorthwhileCapS, GraceS float64
	MinPrefixTokens int
}
var ErrTTLUnset = errors.New("ttl_s 须 > 0（先跑 experiments/cache-ttl 套件实测）")
type NoCachePriceError struct{ Key string } // Error(): "<key> 无 p_cache，拒绝推导心跳参数"
func Compute(b prices.PriceBook, pv prices.PriceVersion, ttlS float64, prefixTokens int,
	beatOutTokens, safety, graceS float64, minPrefixTokens int) (HeartbeatPolicy, error)
	// 缺省值调用方给 300/0.8/300/30000；cap=tau*(pin-pcache)/perBeat，perBeat<=0 → +Inf
func TierFor(p HeartbeatPolicy, waitS float64) string // "none"|"beat"|"expire"
func StrategyCosts(b prices.PriceBook, pv prices.PriceVersion, ttlS, waitS float64,
	prefixTokens int, beatOutTokens, compactRatio float64) (map[string]float64, error)
	// keys: none/beat/expire/expire_compact；beats=ceil(wait/tau)，wait<=0 → 0 跳
// 原 viewer 副本三函数，签名不变挪入本包（面板/反跑/演示共用）：
func Derive(p Params) (Result, error)
func SimulateBeats(t0, windowEnd float64, r Result) []float64
func DoNothingCost(durS, ttlS float64, r Result) float64
```

- [ ] **Step 1**: 移植 test_policy.py 全量 + viewer policy_test.go 全量到同一测试文件。特别注意 Python `math.ceil(wait/tau)` 与 `math.inf` 语义（Go：`math.Ceil`、`math.Inf(1)`）。
- [ ] **Step 2**: 实现 Compute/TierFor/StrategyCosts（公式四式逐字平移：τ=safety·T；perBeat=S/per·pCache+beatOut/per·POut；expire=S/per·PIn；cap=τ·(PIn−PCache)/perBeat）。
- [ ] **Step 3**: 把 viewer 的 Derive/SimulateBeats/DoNothingCost 搬入本包；全仓 grep 确认无第二份公式实现；`internal/viewer/policy` 删除。
- [ ] **Step 4**: `go test ./...` 全绿（含 viewer 家族改接线后）。
- [ ] **Step 5**: Commit `feat(go): T4 policy 移植+公式单源收口——viewer 副本销毁（规格 tests/test_policy.py）`。

---

### Task 5: internal/accounts（规格 ferryman/accounts.py + test_accounts.py，19 例）

**Files:**
- Create: `internal/accounts/accounts.go`、`accounts_test.go`

**Interfaces:**
- Produces:

```go
const SchemaV = 1
type Accounts struct{ dir string; mu sync.Mutex }
func New(dataDir string) (*Accounts, error) // 建 <dataDir>/accounts/
// Fields 值只允许：string / float64 / int / nil；必填校验按白名单
type Fields map[string]any
func (a *Accounts) Record(kind string, ts float64, f Fields) (map[string]any, error)
	// ts<0 → clock.Now()；未知 kind / 越白名单字段 / 缺必填 / 保留字(v,ts_iso) → error（中文报错原文平移）
	// 落盘行：v,kind,ts(Round 3),ts_iso,agent,session_id,lineage_id,project + 按字母序的 kind 字段
func (a *Accounts) Read(o ReadOpts) []map[string]any
type ReadOpts struct{ Since, Until float64; Project, Session, Lineage, Kind string } // 零值=不过滤
```

- 白名单 10 科目 + 字段集合**逐字照抄** accounts.py:20-45（handoff/beat/block/inject/bypass/window/qwatch_hit/qwatch_open/qwatch_close/usage）；公共字段 ts/ts_iso/kind/v/agent/session_id/lineage_id/project。
- ts_iso：本地时区 `time.Unix(ts,0).Format("2006-01-02T15:04:05-0700")`；月度文件名 `Format("200601")`。
- Read 的坏行：跳过并向 stderr 打 `[accounts] 跳过损坏行 <file>:<n>`（stdout 保持机器可解析）。

- [ ] **Step 1**: test_accounts.py 19 例全量移植。示例（白名单拒绝）：

```go
func TestRecordRejectsUnknownField(t *testing.T) {
	a, _ := accounts.New(t.TempDir())
	_, err := a.Record("block", -1, accounts.Fields{
		"agent": "cc", "session_id": "s", "lineage_id": "l", "project": "p",
		"prefix_tokens": 100, "idle_s": 1.0, "message_body": "隐私铁律"})
	if err == nil || !strings.Contains(err.Error(), "隐私不变量") {
		t.Fatalf("want whitelist violation error, got %v", err)
	}
}
```

- [ ] **Step 2**: 实现。JSON 落盘用有序拼接（公共字段定序 + kind 字段字母序）保证确定性；读回语义 diff。
- [ ] **Step 3**: 绿 + Commit `feat(go): T5 accounts 移植（规格 tests/test_accounts.py）`。

---

### Task 6: internal/config（规格 ferryman/config.py + test_config.py）

**Files:**
- Create: `internal/config/config.go`、`config_test.go`

**Interfaces:**
- Produces:

```go
const FerryWallTimeoutS = 480.0
var GateModes = [...]string{"off", "observe", "enforce"} // QWatchModes 同
type WatchCfg struct{ PollIntervalS float64; CCProjectsDir, CodexSessionsDir string; CodexExtraDirs []string; HarvestUsage bool }
type ThresholdCfg struct{ SummarizeS, BlockS float64; MinCtxTokens int; CacheWarnS float64 }
type ServerCfg struct{ Port int; DataDir string }
type NotifyCfg struct{ Enabled, Pushover bool; PushoverToken, PushoverUser string; Toast bool }
type HeartbeatCfg struct{ Enabled bool; TTLS float64; TTLMeasuredAt, TTLSource string }
type QuestionWatchCfg struct{ Mode string; MinQuestions int; BeatIntervalS float64; MaxBeats int; FerryDeadlineLeadS float64 }
type Config struct{ GateCC, GateCodex string; Thresholds ThresholdCfg; Watch WatchCfg; Server ServerCfg; Notify NotifyCfg; Heartbeat HeartbeatCfg; QuestionWatch QuestionWatchCfg; FerryProvider string }
func (c *Config) DataDir() string                       // Server.DataDir 或 ~/ferryman
func (c *Config) ThresholdFor(agent string) ThresholdCfg // 现阶段返回全局
func Load(path string, relaxMinGap bool) (*Config, error) // "" → $FERRYMAN_CONFIG → ~/ferryman/config.toml；不存在=全默认
func Validate(c *Config, relaxMinGap bool) error          // 校验违例 error 文案逐字；lead 下限夹取副作用+打印告警（config.py:164-205）
```

- 默认值逐字：summarize 1500/block 2100/minCtx 20000/cacheWarn 720/port 7311/poll 3.0/qw(mode off,min 5,interval 420,max 2,lead 480)/harvest true。

- [ ] **Step 1**: test_config.py 全量移植（默认值、各节覆盖、全部校验失败分支、relax_min_gap、环境变量路径、lead 夹取与打印）。
- [ ] **Step 2**: 实现（BurntSushi/toml 解析到 map[string]any 再逐节取，容忍部分字段——语义对齐 Python `.get`）。**注意**：Python Validate 会就地夹取 `FerryDeadlineLeadS` 并打印两类告警——副作用必须保留。
- [ ] **Step 3**: 绿 + Commit `feat(go): T6 config 移植（规格 tests/test_config.py）`。

---

### Task 7: internal/cctrans（规格 ferryman/transcripts.py + test_transcripts.py）

**Files:**
- Create: `internal/cctrans/cctrans.go`、`cctrans_test.go`

**Interfaces:**
- Produces:

```go
type Turn struct{ TS float64; CacheRead, CacheCreation, InputTokens int }
func (t Turn) CtxTokens() int
func AssistantTurns(path string) []Turn                  // "usage" 行过滤；无时区按 UTC；坏行静默跳
func HasDanglingToolUse(path string) bool                // 尾部 262144 字节窗口；used−served≠∅
func HasDanglingToolUseWindow(path string, tailBytes int64) bool // 测试注窗口
func AITitle(path string) string                          // 末条 ai-title；"" = 无
func FirstUserMessageHash(path string) string             // 首条 user 正文 strip 后 sha256 hex
func HasAsyncLaunch(path string) bool                     // 尾窗最后一个 Task/Agent 派发；run_in_background/background 或 "Async agent launched" 严格前缀
```

- 防御纪律：一切 OSError/坏行/缺字段静默（返回零值），绝不向调用方抛——tests 里有坏行/空文件/缺字段用例逐条照抄。
- `'"usage"' not in line` 这类**子串预筛**必须保留（性能语义同款）；Go 读文件按行扫大文件用 bufio.Scanner（提高缓冲上限 10MB）。
- 尾窗读法：`os.Stat` 大小 → `ReadAt` 尾段 → 首行可能半行丢弃（`end > tail` 时）。

- [ ] **Step 1**: test_transcripts.py 全量移植（含 t48 async 系列用例名见文件）。
- [ ] **Step 2**: 实现 + 绿 + Commit `feat(go): T7 cctrans 移植（规格 tests/test_transcripts.py）`。

---

### Task 8: internal/extract（规格 ferryman/extract.py + test_extract.py）

**Files:**
- Create: `internal/extract/extract.go`、`extract_test.go`

**Interfaces:**
- Consumes: internal/cctrans（AssistantTurns/AITitle）
- Produces:

```go
type FileCount struct{ Path string; Count int }
type Choice struct{ Question string; Labels []string }
type Facts struct {
	Source string; Title, Cwd string // "" ≡ Python None
	FirstTS, LastTS string; NTurns, PeakCtx int
	Files []FileCount; Commands []string; TotalInputTokens int
	FreezeUser, FreezeAsstText string; FreezeTools []string; FreezeChoice *Choice
}
type Item struct{ Role, Text string } // role: "user"|"assistant"
func TokenEstimate(text string) int   // CJK 码点×1 + 其余码点/3.5 + 1（int 向下取整）
func Extract(path string) (Facts, []Item, []cctrans.Turn)
func MaterialText(f Facts, items []Item) string
func ChunkItems(items []Item, budgetTokens int) [][]Item // 每条 +8 token 开销
func (f *Facts) SkeletonText() string                    // 中文骨架文案逐字（extract.py:59-86）
```

- **TokenEstimate 的 CJK 正则**：Python `[　-鿿Ｑ-￯]` = 码点范围 U+3000-U+9FFF 与 U+FF31-U+FFEF。Go RE2：`[\x{3000}-\x{9FFF}\x{FF31}-\x{FFEF}]`。**按码点计数**（C4）。
- **截断全部按码点**：Item 4000、命令 160、FreezeUser 500、FreezeAsst 1500、标题 60；截断尾标逐字（`（已截断，全文见会话文件）`）。
- 末段定格三档互斥（choice > 工具摘要 > 逐字）、AskUserQuestion 多问只存第一问、files 排序 `(-count, path)`、命令去重保序、上限 MAX_COMMANDS 2000/MAX_FILES 200。
- user 行含 tool_result 整块丢弃；ai-title 回落链（title None → ai_title）。

- [ ] **Step 1**: test_extract.py 全量移植（骨架文案断言逐字——Python 测试里有多行字符串比对，照抄为 Go 原始字符串）。
- [ ] **Step 2**: 实现 + 绿 + Commit `feat(go): T8 extract 移植（规格 tests/test_extract.py）`。

---

### Task 9: internal/codextrans（规格 ferryman/codex_transcripts.py + test_codex_transcripts.py / test_codex_extract.py）

**Files:**
- Create: `internal/codextrans/codextrans.go`、`codextrans_test.go`

**Interfaces:**
- Consumes: internal/extract（Facts/Item/常量）
- Produces:

```go
type XCTurn struct{ TS float64; InputTokens, Cached, CacheWrite int } // input 已含 cached（OpenAI 口径）
func TokenCountTurns(path string) []XCTurn
func SessionCwd(path string) string        // 头 10 行内的 session_meta.payload.cwd
func ExtractCodex(path string) (extract.Facts, []extract.Item)
```

- 五个环境注入 user 前缀逐字（`# AGENTS.md instructions` / `<turn_aborted>` / `<environment_context>` / `<user_instructions>` / `<skills_instructions>`）；developer/system 角色丢弃；apply_patch 补丁头三种前缀抽文件；cmd/command 参数抽命令；标题=首条 user 首行截 60。
- [ ] **Step 1**: 两个测试文件全量移植。
- [ ] **Step 2**: 实现 + 绿 + Commit `feat(go): T9 codextrans 移植（规格 tests/test_codex_transcripts.py + test_codex_extract.py）`。

---

### Task 10: internal/qwatch 检测器（规格 ferryman/qwatch.py + test_qwatch.py）

**Files:**
- Create: `internal/qwatch/qwatch.go`、`qwatch_test.go`

**Interfaces:**
- Consumes: beat 常量 `OUT_OBSERVE`（T11 先建最小包或本任务内联常量、T11 复用）
- Produces:

```go
const AskUserQuestion = "AskUserQuestion"
const DefaultMinQuestions = 5
const TailBytes = 262144
const MissIdleS, MissLookbackS = 600.0, 1800.0
type Breakdown struct{ MarkerLines, QmarkLines, QualifiedNumberedLines int }
func (b Breakdown) Total() int
type Verdict struct{ IsSurge bool; UnitCount int; BD Breakdown; AskUserQuestionDangling bool }
func Detect(path string, minQuestions int) Verdict         // 读失败/空 → Verdict{AQD:true}（真空真）
func DetectTail(path string, minQuestions int, tailBytes int64) Verdict // 测试注窗口
func CorrelateMissSignals(rows []map[string]any) int        // 票06 漏检关联纯计数
```

- 正则平移表（C8）：
  - marker：`(?i)\*\*Q\s?\d+`
  - numbered：`^\s*(?:\d{1,3}\s*[.、)]|[-*•·])\s*`（Python `.match`=前缀匹配，Go 用 `^` 锚定 MatchString）
  - code fence：Python `` ```.*?(?:```|\Z) `` DOTALL → Go `(?s)"`"”`"`. *?(```|\z)`（反引号在双引号串内直写）
  - 疑问词表 16 词逐字（什么/怎么/为何/如何/哪个/哪些/是否/能不能/要不要/还是不是/还是说/what/how/why/which/whether）
- 行归桶优先级 marker > numbered(带问号/疑问词才计) > qmark(非编号行只认问号)；一行只进一桶。
- 末条 assistant 按 message id 聚合文本块；块边界视作换行；悬空 tool_use 集合差 + 名字收集；`used[id]==AskUserQuestion` 全真才 AQD=true（空集真空真）。
- [ ] **Step 1**: test_qwatch.py 全量移植。
- [ ] **Step 2**: 实现 + 绿 + Commit `feat(go): T10 qwatch 检测器移植（规格 tests/test_qwatch.py）`。

---

### Task 11: internal/beat 纯逻辑（规格 ferryman/beat.py + test_qwatch_scheduler.py 中纯逻辑部分）

**Files:**
- Create: `internal/beat/beat.go`、`beat_test.go`

**Interfaces:**
- Produces:

```go
const HitRatioThreshold = 0.5
const (OutHit = "hit"; OutMiss = "miss"; OutError = "error"; OutObserve = "observe")
type BeatPlan struct{ Agent, SessionID, TranscriptPath string; OpenedTS, LastWrite float64; Size, BeatIndex int; BeatTS float64 }
type BeatResult struct{ Sent, OK bool; InputTokens, CacheReadTokens, OutputTokens int; CostPred, CostActual float64; Provider, Model, Err string }
type Sender interface{ Send(BeatPlan) BeatResult }
type NoopSender struct{}
func (NoopSender) Send(BeatPlan) BeatResult // {Sent:false}
func Classify(r BeatResult) string // 未真发=observe；败=error；ratio=cache/(cache+input)≥0.5 → hit 否则 miss
type Breaker struct{ MissStreak, ErrorStreak int } // MISS_LIMIT=2 ERROR_LIMIT=3
func (b *Breaker) Record(outcome string) string   // "demote"|"pause"|""（连击语义逐字：ERROR 不洗白 MISS）
type QWatchStats struct{ /* mu 内藏 */ }
func NewQWatchStats() *QWatchStats
func (s *QWatchStats) RecordHit() / RecordWindowOpened() / RecordBeat(outcome string, costActual float64)
func (s *QWatchStats) Snapshot() map[string]any // hits/windows_opened/beats_fired/beats_by_outcome/cost_actual(Round 6)
```

- HttpBeatSender 不移植（Python 版就是 NotImplementedError 占位；真实发送属 Q14 未授权——BeatSender 接口位保留）。
- [ ] **Step 1**: 移植 classify/breaker/QWatchStats 全部用例（散在 test_qwatch_scheduler.py / test_qwatch_events.py，纯逻辑部分先迁，调度部分留 T20）。
- [ ] **Step 2**: 实现 + 绿 + Commit `feat(go): T11 beat 纯逻辑移植（规格 test_qwatch_scheduler.py 纯逻辑部分）`。

---

### Task 12: internal/ledger（规格 ferryman/ledger.py + test_ledger.py）

**Files:**
- Create: `internal/ledger/ledger.go`、`ledger_test.go`

**Interfaces:**
- Produces:

```go
const SubagentEventLeakS = 3600.0
type QSnap struct{ MTime float64; Size int }
type SessionState struct {
	Agent, SessionID, TranscriptPath, Cwd, Title string
	LastWrite float64; Size, PeakCtx int
	ObservedActive bool; HandedOffAt, EnrichedWrite float64 // EnrichedWrite 初值 -1
	QWatchOpenedTS *float64 // nil=无窗
	QWatchBeatsFired int
	QWatchPlan []float64
	QWatchSnapshot *QSnap
}
type Ledger struct{ /* mu sync.Mutex; byKey map[[2]string]*SessionState; byPath map[string]*SessionState; subagents …; lastWrite float64 */ }
func New() *Ledger
func (l *Ledger) Mu() *sync.Mutex  // 跨包临界区用（C6）；持锁期间禁调本包公共方法
func (l *Ledger) Touch(agent, sid, path string, mtime float64, size int, daemonStartedAt float64) *SessionState
func (l *Ledger) TouchFull(agent, sid, path string, mtime float64, size int, cwd, title string, peakCtx int, daemonStartedAt float64) *SessionState
func (l *Ledger) Get(agent, sid string) *SessionState // nil=无
func (l *Ledger) GetByPath(path string) *SessionState
func (l *Ledger) AllSessions() []*SessionState
func (l *Ledger) LastTranscriptWrite() float64
func (l *Ledger) SubagentEvent(agent, sid, event string) int // "start"|"stop"；stop 下限 0
func (l *Ledger) SubagentActive(agent, sid string) bool      // 泄漏防护：事件超 1h 视 0 并清理
func (l *Ledger) SubagentsActiveCount() int
func (l *Ledger) MarkHandedOff(st *SessionState)
```

- Touch 语义逐字（ledger.py:103-143）：lineage 继承（同 path 换 sid → 继承 last_write/cwd/title/peak_ctx/handed_off_at）；`size or st.size`（size=0 不覆盖）；mtime>last_write 才推进并置 ObservedActive（mtime≥started_at）并**清 qwatch 窗四字段**（任何新写入关窗）。
- 返回的 *SessionState 是共享可变引用——调用方改动须持 Mu（与 Python 语义一致，注释注明）。
- [ ] **Step 1**: test_ledger.py 全量移植（lineage、泄漏、qwatch 字段清窗、Touch 各覆盖分支）。
- [ ] **Step 2**: 实现 + 绿 + Commit `feat(go): T12 ledger 移植（规格 tests/test_ledger.py）`。

---

### Task 13: internal/store（规格 ferryman/store.py + test_store.py）

**Files:**
- Create: `internal/store/store.go`、`store_test.go`

**Interfaces:**
- Consumes: internal/extract（TokenEstimate——SavePendingPrompt 的 500 token 截断）
- Produces:

```go
const FreshWindowS, CoversToleranceS = 86400.0, 60.0
type Entry struct {
	HandoffID, SessionID, Agent, Cwd, Title, CreatedAt, CoversUntil string
	CoversUntilS float64; Status, Path string; BlockedAt *string; Injected []string
}
type Store struct{ /* mu; dir; indexPath; index */ }
func New(dataDir string) (*Store, error)  // 建 handoffs/；index.json 坏/缺 → 空索引
func (s *Store) SaveHandoff(sessionID, agent, cwd, title, coversUntilISO, status, md string) Entry
	// id=YYYYmmdd_HHMMSS_hex6；同 (session,agent) 覆盖；cwd 为空则 ""，非空 filepath 大小写保留（resolve 尽力）
func (s *Store) ValidHandoff(agent, cwd string, lastWrite float64) *Entry // fresh|skeleton；covers+60s ≥ lastWrite；24h 新鲜；covers 最大者
func (s *Store) MarkBlocked(id string)
func (s *Store) SavePendingPrompt(sessionID, prompt string) // >500 token 截断+「…(超长截断)」
func (s *Store) PopPendingPrompt(sessionID, consumeFor string) string // ""=无；consumeFor!="" 标记消费
func (s *Store) RestoreCandidates(agent, cwd string) []Entry // covers 降序
func (s *Store) MarkInjected(id, sessionID string)
func (s *Store) ReadHandoff(e Entry) string // 读失败 ""
```

- 原子写：tmp 文件 + os.Rename（Windows 同卷等价 os.replace）。index JSON：indent 2、ensure_ascii=False ≡ Go 默认非 ASCII 直出、键序以 Entry 字段序（结构体序列化天然定序——Python dict 序对读方无语义）。
- 时间戳格式 `20060102_150405` / `2006-01-02 15:04:05`（本地时区）。
- [ ] **Step 1**: test_store.py 全量移植。
- [ ] **Step 2**: 实现 + 绿 + Commit `feat(go): T13 store 移植（规格 tests/test_store.py）`。

---

### Task 14: internal/harvest（规格 ferryman/harvest.py + test_harvest.py）

**Files:**
- Create: `internal/harvest/harvest.go`、`harvest_test.go`

**Interfaces:**
- Consumes: internal/accounts
- Produces:

```go
type Row struct {
	TS *float64 // nil=坏行无 timestamp（喂方取默认 0 语义由调用方处理）
	Model, MsgID string
	InputTokens, CacheReadTokens, CacheCreationTokens, OutputTokens int
	Title, Project string; Offset int64
}
func ParseUsageChunk(text, title, cwd string) (rows []Row, outTitle, outCwd string)
type HarvestState struct{ /* offsets/titles/cwds/msgSeen 按 (agent, stem) */ }
func NewHarvestState(a *accounts.Accounts) *HarvestState // 从 usage 流水恢复偏移/标题/项目
func (h *HarvestState) MaybeHarvest(path string, size int64, agent string) []Row
```

- 增量语义逐字：残行留待下轮；size<offset 从头重采；同 MsgID 只记首发；偏移先推进后返回（at-most-once）；读失败返回空不推进。
- [ ] **Step 1**: test_harvest.py 全量移植。
- [ ] **Step 2**: 实现 + 绿 + Commit `feat(go): T14 harvest 移植（规格 tests/test_harvest.py）`。

---

### Task 15: internal/notify（规格 ferryman/notify.py + test_notify.py）

**Files:**
- Create: `internal/notify/notify.go`、`notify_test.go`

**Interfaces:**
- Consumes: internal/config
- Produces:

```go
func SendPushover(title, message, token, user string) bool // 4s 超时；任何故障 false
func SendToast(title, message string) bool                  // powershell WinRT Toast；5s 超时
func NotifyAlert(title, message string, cfg *config.Config) // enabled=false 静默；Pushover 凭据回落环境变量
func NotifyBlock(handoffPath, agent, sessionID string, cfg *config.Config) // 文案逐字
```

- 测试：HTTP 用 httptest 换 PUSHOVER_URL——把 URL 提为包级 `var PushoverURL = "https://api.pushover.net/1/messages.json"`（测试替换）；toast 测试 mock 掉 powershell 调用（把命令执行提为包级 var，Python 测试同款 monkeypatch 思路）。
- [ ] **Step 1**: test_notify.py 全量移植。
- [ ] **Step 2**: 实现 + 绿 + Commit `feat(go): T15 notify 移植（规格 tests/test_notify.py）`。

---

### Task 16: internal/daemon·窗口与停车（规格 server.py:102-131,336-593 + test_subagent.py + test_merge_t51_parking_mutex.py）

**Files:**
- Create: `internal/daemon/daemon.go`（Daemon 骨架+常量）、`windows.go`、`windows_test.go` / `subagent_test.go`

**Interfaces:**
- Consumes: ledger/store/accounts/config/beat/qwatch
- Produces:

```go
const (DegradeAfterBlocks = 3; PendingTTLs = 86400.0; WarnContextCap = 2000
	HealthGraceS = 600.0; ParkExpireS = 3600.0; AckGraceS = 90.0; QWatchMissScanS = 86400.0)
type GateStats struct{ /* mu; Total int; ByAgent map[string]int; Bypass, Blocks, Warns, SubagentEvents int; LastCall float64 */ }
func (s *GateStats) Hit(agent string)
type PendingTable struct{ /* mu; t map[[2]string]pendingRec */ }
func (p *PendingTable) Get(k [2]string) bool / Set(k) / BumpBlocks(k) int / Clear(k) // Get 带 24h TTL
type winKey = [2]string // (agent, sid)
type waitWindow struct{ OpenedTS float64; StopTS *float64; SawAsync bool }
type Daemon struct {
	Cfg *config.Config; Ledger *ledger.Ledger; Store *store.Store
	EnqueueFerry func(*ledger.SessionState) bool
	Accounts *accounts.Accounts // nil=不记账
	Stats *GateStats; Pending *PendingTable
	QWatchStats *beat.QWatchStats // nil=未接线
	windowsMu sync.Mutex          // C6：外层锁
	windows map[winKey]*waitWindow
	StartedAt float64
}
func NewDaemon(cfg, lg, st, enqueue, acc *accounts.Accounts, startedAt float64, qs *beat.QWatchStats) *Daemon
func (d *Daemon) Subagent(body map[string]any) (map[string]any, error) // event∈start|stop 校验→400（error 通道）
func (d *Daemon) WindowWait(agent, sid string) bool    // 停车未过期；懒过期闭窗记 expired
func (d *Daemon) ParkingOpen(agent, sid string) bool   // 无副作用探测（活跃窗按 1h 泄漏口径、停车窗按 1h 过期口径）
func (d *Daemon) NoteUsage(agent, sid string, ts float64) // stop+90s 后恢复行 → 闭窗 main_resumed
func (d *Daemon) QWatchStop() map[string]any          // mode→off+取消全部窗/计划；锁外落账 close_reason=stop
func (d *Daemon) Acct(kind string, st *ledger.SessionState, agentOverride, sidOverride, lineageOverride string, fields accounts.Fields)
```

**锁范式（本任务立样板，T17/T20 沿用）**——Python 的"ledger.lock 临界区 + 无锁探测"翻成 Go 的**双锁同序**：

```go
// 开等答复窗（watcher 侧）：双资源临界区，两把同序全拿（C6）
func (w *Watcher) openQWatchLocked(st *ledger.SessionState) bool {
	w.daemon.windowsMu.Lock()         // 外层
	defer w.daemon.windowsMu.Unlock()
	w.ledger.Mu().Lock()              // 内层
	defer w.ledger.Mu().Unlock()
	if st.QWatchOpenedTS != nil { return false }        // 复验：并发路径已开
	if w.ledger.SubagentActiveLocked(st.Agent, st.SessionID) { return false }
	if w.daemon.parkingOpenLocked(st.Agent, st.SessionID) { return false } // 无锁内方法（已持双锁）
	// …置窗字段
	return true
}
// 停车窗落表（server 侧 subagent）：同一对锁、同一顺序
```

（ledger 需为本任务补 `SubagentActiveLocked` 等无锁内方法——公共方法自带锁的镜面。）

- 停车判定/重锚/锁存/闭窗四因（subagents_done/prompt/main_resumed/expired）语义逐字平移 server.py:365-562，注释一并搬运（这些注释就是不变量文档）。
- `_book`/`_acct` 记账封装：lineage 取 NormPath(transcript_path)、失败只打印不抛——逐字。
- [ ] **Step 1**: test_subagent.py + test_merge_t51_parking_mutex.py 全量移植（并发用例用 goroutine+WaitGroup 复刻；若本机有 gcc 加 `-race` 跑）。
- [ ] **Step 2**: 实现 daemon.go + windows.go（含 QWatchStop）。
- [ ] **Step 3**: 绿 + Commit `feat(go): T16 daemon 窗口/停车移植——双锁同序范式落地（规格 tests/test_subagent.py + test_merge_t51_parking_mutex.py）`。

---

### Task 17: internal/daemon·闸门状态机与归还（规格 server.py:135-500 + test_gate.py 43 例）

**Files:**
- Create: `internal/daemon/gate.go`、`gate_test.go`、`restore.go`、`restore_test.go`

**Interfaces:**
- Produces:

```go
func (d *Daemon) Gate(body map[string]any) map[string]any
	// 键：decision/reason/additional_context/suppressOriginalPrompt/handoff_path
func (d *Daemon) Restore(agent, cwd, sessionID string) map[string]any // 键：context（nil=无）
func (d *Daemon) Health() map[string]any                              // /stats 载荷字段名逐字（server.py:640-673）
```

- 状态机七分支顺序逐字（server.py:135-249）：①强续/!! 前缀 bypass（先闭未停车窗）；②无台账放行 no-ledger；③mode off；④machine-waiting 三道豁免（含 additional_context 文案逐字）；⑤observe 只警告（will_block=False 文案）；⑥enforce 完整分支 5/6/7（block reason 全文逐字——多行中文模板照抄；分支 6 连续 3 次降级）；pending 清除三条件（H 就绪/新闲置周期/24h）。
- `_warn_ctx`（2000 字符截断）与 `_cache_info_ctx`（cache_warn_s 纯提醒）文案逐字。
- Restore：多候选只列清单（前 5 条）、单候选注入 INJECT 层（标记缺失截 1800）、待续 prompt 拼接、`ctx[:6000]` 截断、inject 记账（lineage 按源会话转录路径解析，R9）。
- Health：qwatch 节（计数器 snapshot + miss_signals 现算 + mode 活值）、health_alert 判定（1h 写入但 gate 零调用，含 600s 启动宽限）。
- [ ] **Step 1**: test_gate.py 全量 43 例移植。gate 测试的 Python 形态是"裸 Daemon + monkeypatch 时钟"——Go 用 clock.Now 替换 + 直构 Daemon 结构体。
- [ ] **Step 2**: 实现 + 绿 + Commit `feat(go): T17 闸门状态机+归还移植（规格 tests/test_gate.py）`。

---

### Task 18: internal/daemon·httpapi（规格 server.py:676-781 + test_singleton.py）

**Files:**
- Create: `internal/daemon/httpapi.go`、`httpapi_test.go`

**Interfaces:**
- Produces:

```go
func EnsureToken(dataDir string) (string, error) // daemon.token；缺则生成 64 hex（权限 0600 尽力）
func AlreadyRunning(port int, token string) bool // GET /stats + Bearer 2s 超时
type DaemonLike interface {
	Gate(map[string]any) map[string]any
	Subagent(map[string]any) (map[string]any, error)
	QWatchStop() map[string]any
	Restore(agent, cwd, sessionID string) map[string]any
	Health() map[string]any
}
func ListenAndServe(d DaemonLike, port int, token string) (net.Listener, *http.Server, error)
	// 只绑 127.0.0.1；路由 POST /gate /subagent /qwatch_stop；GET /stats /restore
```

- 语义细节：Bearer 不符→401 `{"error":"unauthorized"}`；未知路径→404；JSON 坏/ValueError→400 `bad request: …`；/restore query 参数 agent/cwd/session_id 缺省 cc/""/""；响应 Content-Type `application/json; charset=utf-8`；访问日志静默。
- Windows 绑定：Go `net.Listen` 默认排他（无 SO_REUSEADDR）——test_singleton 的"双进程绑同口必败"天然成立；绑定失败后 AlreadyRunning 区分"唯一化跳过/端口被占"（serve 装配在 T21）。
- [ ] **Step 1**: test_singleton.py 移植（真监听临时端口、401/404/400/200 全路径）。
- [ ] **Step 2**: 实现 + 绿 + Commit `feat(go): T18 httpapi 移植（规格 tests/test_singleton.py）`。

---

### Task 19: internal/daemon·watcher（规格 daemon.py:35-121,234-490 + test_qwatch_window/scheduler/events/e2e.py）

**Files:**
- Create: `internal/daemon/watcher.go`、`watcher_test.go`

**Interfaces:**
- Consumes: T16 锁范式、qwatch.Detect、beat、harvest、extract/codextrans（懒富化）
- Produces:

```go
func CodexWatchDirs(w config.WatchCfg, home string) []string // 主目录+额外+Orca runtime 目录（daemon.py:35-50 路径逐字）
type Watcher struct {
	Cfg *config.Config; Ledger *ledger.Ledger; Store *store.Store
	Enqueue func(*ledger.SessionState) bool; StartedAt float64
	Accounts *accounts.Accounts; Daemon *Daemon // Daemon=nil 视同无接线（旧测试形态）
	BeatSender beat.Sender // nil → Noop + enforce 告警一次
	QWatchStats *beat.QWatchStats
	/* qwatchSeen/qwatchHitSeen map[winKey]float64 版本章；beatInFlight bool；breaker *beat.Breaker */
	harvest *harvest.HarvestState
}
func NewWatcher(...) *Watcher
func (w *Watcher) Run(ctx context.Context)    // 循环：pollCC + pollCodex；异常打印继续（守望永不死）
func (w *Watcher) PollOnce()                  // 测试直调（Python 测试同款）
```

- 轮询序列逐字：CC 侧 `**/*.jsonl` 跳 subagents 段 → prevOpen 快照 → Touch → 关窗事件对照（opened_ts/beats_fired 取 touch 前！）→ 用量采集 → qwatch 开窗判定 → 心跳调度 → 入队判定；Codex 侧跨目录 sid 去重（文件名末段 uuid）。
- 入队五道推迟逐字（daemon.py:186-218）：未观察活跃/未达总结阈值/交接仍覆盖/等答复窗（死线除外）/子代理在飞/悬空 tool_use（死线豁免）/停车窗（死线**不**豁免）；懒富化按版本一次；过小会话置 handed_off；入队即记 handed_off。
- qwatch 开窗：四条件全真（surge+AQD 悬空/子代理 0/停车窗无/前缀≥门槛）+ 版本章缓存 + 命中事件去重章 + 双锁临界区（T16 范式）+ 锁外落账。
- 心跳调度：计划 t0+i×interval（i≥1）；到期逐发；两道验（版本章 + 复 stat）+ 在途占用在**同一临界区**（Python 在台账锁内 stat——Go 持 ledger.Mu 内 os.Stat，注释说明为何不可移出，server 票04 评审结论）；网络锁外；结账（三态入账/熔断 demote 降级 mode/pause 清计划）。
- 用量采集喂 note_usage（ts_max 坏行滤除）。
- Python `__new__` 裸构造的旧测试 → Go 直构结构体 + nil 检查（getattr 容错全部翻成 nil-safe）。
- [ ] **Step 1**: test_qwatch_window.py(32)/test_qwatch_scheduler.py(26 调度部分)/test_qwatch_events.py/test_qwatch_e2e.py 全量移植。这是全战役最大测试面，一天消化不完就按文件拆 commit（同任务号追加）。
- [ ] **Step 2**: 实现 watcher.go（含心跳调度/富化/采集）。
- [ ] **Step 3**: 绿（有 gcc 则 `-race`）+ Commit `feat(go): T19 watcher 移植——守望/开窗/心跳调度（规格 test_qwatch_window/scheduler/events/e2e.py）`。

---

### Task 20: internal/daemon·worker 与 serve 装配（规格 daemon.py:493-663 + test_integration.py + test_big_session.py）

**Files:**
- Create: `internal/daemon/worker.go`、`serve.go`、`serve_test.go`

**Interfaces:**
- Consumes: T23 之前的依赖用接口占位：`type ferryFunc func(path string, pr ferry.Provider, timeout float64, agent string) (string, map[string]any, error)`——T23 前注入"恒失败"测试替身（骨架降级路径本身是生产行为）
- Produces:

```go
type Worker struct{ /* tasks chan map[string]any (cap 10); providers; cfg; store; accounts */ }
func NewWorker(cfg, st, acc, providers map[string]ferry.Provider) *Worker
func (w *Worker) Run(ctx context.Context) // 每任务：ferrySession(墙钟 480s ctx 超时) → 失败/超时骨架降级 + 记账 failed；成功存 fresh + 记账
func Serve(relaxMinGap bool) int // = python serve()：配置→目录→token→Ledger/Store/Accounts→队列→Daemon→监听(绑定失败分流)→pid→Watcher/Worker 起线程→横幅→ServeHTTP→优雅停
```

- 队列语义：`put_nowait`→`select { case ch <- item: default: 满/延迟 }`；worker 每秒轮询退出。
- 记账：handoff 行 price_ver 按流水时刻取版本（prices.At + PriceTag）；usage 失败记 0；**失败路径一行 failed 且在骨架保存之后**（终审#2 顺序）。
- 骨架头/尾文案逐字（daemon.py:569-571）。
- serve() 横幅逐字（daemon.py:650-653）；pid 文件 JSON {pid,port,started_at}；KeyboardInterrupt≈SIGINT 优雅停（watcher/worker stop + pid 删除）。
- [ ] **Step 1**: test_integration.py + test_big_session.py 全量移植（临时目录假会话文件 + clock 注入；ferry 用恒败替身走骨架路径）。
- [ ] **Step 2**: 实现 + 绿 + Commit `feat(go): T20 worker+serve 装配移植（规格 tests/test_integration.py + test_big_session.py）`。

---

### Task 21: hooks 端到端测试移植（规格 tests/test_hooks.py 16 例——真跑 powershell）

**Files:**
- Create: `internal/installer/hooks_e2e_test.go`

**Interfaces:**
- Consumes: T18 httpapi（测试内起真守护监听临时口）+ hooks/*.ps1 原样

- [ ] **Step 1**: 移植 16 例：每个用例 `exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", ps1)`，stdin 喂 JSON、断言 exit code 与 stdout JSON（fail-open：守护关→exit 0 放行；401→放行；block→exit 2 语义照 test_hooks.py 实测）。仅 `runtime.GOOS=="windows"` 执行，其余 `t.Skip`。
- [ ] **Step 2**: 绿 + Commit `feat(go): T21 hooks PS1 端到端测试移植（规格 tests/test_hooks.py）`。

---

### Task 22: internal/ferry 摆渡执行器（规格 ferryman/ferry.py + test_ferry_providers.py）

**Files:**
- Create: `internal/ferry/ferry.go`、`ferry_test.go`

**Interfaces:**
- Consumes: extract/codextrans
- Produces:

```go
const (InjectOpen = "<<<INJECT>>>"; InjectClose = "<<</INJECT>>>"; InjectBudget = 2200
	FullBudget = 8000; PromptReserve = 4096; WindowGuard = 8192)
const SystemPrompt = `…逐字平移 ferry.py:31-58（含防注入声明与六节结构）…`
type Provider struct{ Name, BaseURL, Model, APIKey string; Window int } // Window 缺省 131072
func LoadProviders(path string) map[string]Provider
func Chat(pr Provider, system, user string, timeoutS float64, maxTokens int) (reply string, usage map[string]any, err error)
	// OpenAI 兼容 /chat/completions；temperature 0.2；HTTPError → error 文案 "HTTP <code> from <name>: <body500>"
func TrimInjectLayer(text string) string   // >2200 token 硬截 2200 字 + "…(已截断)"
func ParseOutput(reply string) (inject, full string) // 标记缺失→全文兜底
func HandoffMarkdown(title, inject, full string, meta map[string]any) string // 头部文案逐字
func FerrySession(path string, pr Provider, timeoutS float64, agent string) (string, map[string]any, error)
	// L1: mat_tokens ≤ window−8192−4096；L2: 分块预算 max(16000, input/3)，逐段纪要(≤1200, max_tokens 2048)+reduce
	// meta 键逐字：source/title/mode/model/provider/covers_until_iso/mat_tokens_est/chunks/wall_s(Round 1)/usage/call_walls/inject_tokens_est/full_tokens_est
```

- Chat 用 `http.NewRequestWithContext`（R10：goroutine 不悬挂）；usage 三键 prompt/completion/total + wall_s。
- [ ] **Step 1**: test_ferry_providers.py 全量移植（httptest 假 OpenAI 端点：成功/HTTPError/L2 分块/max_tokens 传参/usage 汇总）。
- [ ] **Step 2**: 实现 + 绿 + 把 T20 的接口占位换成真调用（test 重跑）+ Commit `feat(go): T22 ferry 执行器移植（规格 tests/test_ferry_providers.py）`。

---

### Task 23: internal/report 报表（规格 ferryman/report.py + test_report.py）

**Files:**
- Create: `internal/report/report.go`、`report_test.go`

**Interfaces:**
- Consumes: internal/policy（StrategyCosts——第二份公式副本就此收口）、prices、accounts、config
- Produces:

```go
const (SavingsFormula = "v1"; StrategyFormula = "v1"; CompactRatio = 0.25)
var StrategyCaveats = […]string{…两段逐字 report.py:26-32…}
func HandoffCost(e map[string]any, books map[string]prices.PriceBook) *float64 // 无价/无版本 → nil
func SavingsV1(entries []map[string]any, books map[string]prices.PriceBook, econBook *prices.PriceBook) map[string]any
	// 行键逐字：lineage_id/project/blocks/bypass/injects/handoffs/windows/handoff_cost/gross/inject_cost/net(Round 4)
	// gross = Σ block S×(P_in−P_cache)/per（econ_book 按行时刻取版本）；行按 net 降序
func StrategyTable(windowEntries []map[string]any, books, ttlS float64, econKey string) map[string]any
	// rows/skipped/formula/compact_ratio；best=min 四键；跳过原因文案逐字
func RenderText(s, st map[string]any, books, econKey string, filters map[string]string) string // 全部中文模板逐字
func Run(args Args) int // --since/--until/--project/--session/--kind/--provider/--json；本地时区日期解析（until 含当日全天）
```

- [ ] **Step 1**: test_report.py 全量移植（含 --json 结构、无 p_cache 跳过、unpriced 汇总、复算口径数字）。
- [ ] **Step 2**: 实现 + 绿 + Commit `feat(go): T23 report 移植——策略公式接唯一源（规格 tests/test_report.py）`。

---

### Task 24: internal/installer——install/doctor（规格 install.py + doctor.py + test_install/test_ccswitch/test_doctor.py）

**Files:**
- Create: `internal/installer/install.go`、`ccswitch.go`、`doctor.go` + 三个测试文件
- Modify: `go.mod`（+ modernc.org/sqlite）

**Interfaces:**
- Produces:

```go
var (CCSwitchDB, CodexHooks, CodexConfig 路径函数（HOME 注入）; LauncherName = "start-daemon.cmd")
func EnsureLauncher(dataDir, exePath string) string  // cmd 内容：cd /d 数据目录 + "<exe>" serve >> serve.out/err.log（CRLF）
func FerryHookEntries(repo string) map[string]any    // 四事件条目结构逐字（PS 命令行、timeout 3/10/3/3）
func InstallCC(settingsPath, dbPath, dataDir, repo string) int
func InjectCCSwitch(dbPath string) int               // sqlite：SELECT id,name,settings_config FROM providers WHERE app_type='claude'；幂等替换；备份留 3 份
func InstallCodex(hooksPath, configPath, repo string) int // hooks.json + [features] hooks=true
func CheckCCHooks / CheckHookScripts / CheckCCSwitch / CheckCodex / CheckDaemon / CheckFerryProvider / CheckLauncher // 返回 (bool, 文案逐字)
func RunDoctor() int
```

- launch 脚本不再指向 venv python——指向 `ferryman.exe`（本战役的新形态；exe 路径参数化，测试注入临时 exe 路径）。
- sqlite 经 modernc.org/sqlite（database/sql 标准接口）；库锁失败文案与 Python 一致。
- doctor 的 BOM/控制字符检查逐字节平移（0xEF 0xBB 0xBF 前缀；0x07/08/0B/0C/1B 扫描）。
- [ ] **Step 1**: test_install/test_ccswitch/test_doctor 全量移植（临时 HOME/DB/配置文件注入；sqlite 真库临时文件）。
- [ ] **Step 2**: 实现 + 绿 + Commit `feat(go): T24 installer 移植——点火脚本改指 exe（规格 tests/test_install+ccswitch+doctor.py）`。

---

### Task 25: cmd/ferryman 合并 exe（吸收 cmd/viewer；规格=viewer 现行行为 + __main__.py 子命令面）

**Files:**
- Create: `cmd/ferryman/main.go`（子命令解析 + serve 装配 + 面板/托盘/demo/快捷方式）
- Delete: `cmd/viewer/`（main.go 并入）
- Create: `build.ps1`（`go build -ldflags "-H windowsgui" -o ferryman.exe ./cmd/ferryman`）

**Interfaces:**
- Consumes: 全部 internal/*
- Produces: 单 exe 行为面：

```
ferryman.exe                          # = serve：守护(7311) + 面板(15900) + 托盘
ferryman.exe serve                    # 同上（点火脚本用）
ferryman.exe doctor / install-cc / install-ccswitch / install-codex
ferryman.exe account report [--json …]
ferryman.exe --demo / --port N / --no-tray / --no-browser / --install-shortcuts  # 面板族 flags
```

- serve 模式内嵌面板：`internal/viewer/server.New(cfg.DataDir()/accounts)` 挂 15900（`FERRYMAN_PANEL_PORT` 可覆盖）；托盘菜单「打开面板/退出」退出=停守护；`--no-tray` 前台跑（钩子自举场景建议带 --no-tray 由 cmd 隐藏窗口承载）。快捷方式安装改指本 exe。
- [ ] **Step 1**: 手测矩阵：serve 起全链（守护口+面板口+托盘）、doctor 全绿、--demo 面板、install-shortcuts 落盘。
- [ ] **Step 2**: `go vet ./... && go test ./...` 全绿。
- [ ] **Step 3**: Commit `feat(go): T25 合并 exe——守护+面板+托盘+CLI 单进程`。

---

### Task 26: 预切换验收（全量绿 + 三场景手工冒烟）

- [ ] **Step 1**: `go test ./...` 全绿清点（数量对照：约 330 移植 + viewer 既有 + hooks 16）；有 gcc 加跑 `-race ./internal/daemon/...`。
- [ ] **Step 2**: 本机真实会话冒烟三链路（observe 配置下）：①gate 警告注入出现；②闲置达总结阈值后真 provider 摆渡落 handoff + 账本 handoff 行字段齐全；③/clear 后归还注入完整（含待续 prompt）。每条在 ~/ferryman 数据目录留证（账本行 + 交接 MD）。
- [ ] **Step 3**: Python 版与 Go 版账本行并排语义 diff 一份（人工，字段级）——这是"逐字段一致"从门禁降级为抽检后的最后一道主观确认。
- [ ] **Step 4**: Commit（如有修补）+ 记录冒烟证据到 `docs/20260918_2311_Python到Go一次性迁移_评估.md` 文末追加小节。

---

### Task 27: 切换执行（规程=评估文档 §6，逐字执行）

- [ ] **Step 1**: `git tag archive/python-final && git push --tags`。
- [ ] **Step 2**: build.ps1 出 exe → 仓库根固定路径。
- [ ] **Step 3**: 停 Python 守护；删 daemon.pid。
- [ ] **Step 4**: `ferryman.exe install-cc`（重生成 start-daemon.cmd 指向 exe）+ `install-codex` + `--install-shortcuts`。
- [ ] **Step 5**: `ferryman.exe serve` 前台首启验横幅→退出→钩子自举接管；`ferryman.exe doctor` 全绿。
- [ ] **Step 6**: 删除 ferryman/、tests/、pyproject.toml、uv.lock（.venv 本地手动删）；commit `feat(go): T27 一次性切换——Python 退役，存档 tag archive/python-final`。
- [ ] **Step 7**: 24h 观察清单（评估文档 §6）执行；发现问题当日热修（commit 前缀 `fix(go):`）。

---

## Self-Review 记录

- **规格覆盖**：21 个迁移模块 ↔ T2-T25 一一对应（policy 吸收 viewer 副本于 T4；__main__ 的 serve/install/doctor/account 子命令面落在 T20/T24/T25；e0*/eval* 冻结面在 ADR-0005 声明，无任务=有意为之）。387 个 Python 测试中冻结面（test_e0c*×3、test_eval_checks）不迁，其余全部落在对应任务的 Step 1。
- **占位扫描**：T26 Step 2/3 是人工操作步骤（非代码占位）；T22 之前 T20 用恒败替身是设计（骨架降级是真实生产行为），非 TBD。
- **类型一致性**：跨任务签名以各任务 Interfaces 块为准（ledger.SessionState 的 QWatchSnapshot=ledger.QSnap；daemon.Daemon 的 EnqueueFerry=func(*ledger.SessionState) bool 与 watcher.Enqueue 同形；ferry.FerrySession 与 T20 的 ferryFunc 同形）。执行中如需微调签名，先改本文件再改代码。
