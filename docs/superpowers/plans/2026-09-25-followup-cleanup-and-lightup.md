# 收尾与点亮役（widget follow-up: cleanup & upstream light-up）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 清掉悬浮窗战役与启用役的全部尾巴——glm_balance 死面下线、评审 M2/M3 两 Minor、/report JSON 合法性回归钉、点亮 Kimi/DeepSeek 两家圆控件、widget 自升级发布侧落地。

**Architecture:** 全部为存量面上的增量收口：Go daemon 侧三处小改（删死码 / Kimi 解析补数字串 / 报表测试加固），widget 侧一处（轮询框架改自适应 + 恢复即拉），配置侧一处（config.toml 加两条上游条目点亮查询器），CI 侧一处（widget-latest 常驻 tag）。无新包、无新端点、无契约破坏。

**Tech Stack:** Go（daemon/quota/report 既有包）、vanilla JS + Tauri 2（widget）、GitHub Actions（widget.yml）。

**Spec:** `.scratch/usage-widget/MORNING-20260925.md`（待办 #0–#8）、`widget/docs/release.md`（发布侧人工四件套）、评审 Minor 清单（M2 恢复即拉 / M3 数字串 resetTime / M5 已记录在案不修）、`.scratch/usage-widget/issues/09-daemon-wiring.md`（M2 的票面原文）。

## Global Constraints

- **Windows 零闪窗铁律**：会话内只跑**限定包**的 `go test ./internal/<pkg>/...`；不裸跑全仓 `go test ./...`（要跑走 VBS 隐身壳落日志）。
- **凭据红线（T39）**：api_key 只进本机 `~/ferryman/config.toml`，绝不进 git、绝不出现在命令输出/日志/晨报里（报告时掩码 `<len N>`）。
- **错误串纪律**：quota/dock 错误只出类别文案，永不携带 key 与 URL 原文。
- **契约只增不改义**：`/widget/summary` 契约 v0 键集不动（本役零契约变更）。
- **公式单源**：不出现第二份节省/策略公式（本役不碰公式面）。
- **observe 周纪律**（至 09-27）：不触闸门/摆渡热路径；daemon 重启走既有 graceful 流程并记录次数；判据数据源在台账（持久）不受影响。
- **VBS 纯 ASCII**（若涉及）；提交信息中文；提交尾行 `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`。
- 长任务/构建不阻塞会话，`run_in_background` 落日志。

## 审核决策点（拍板处，默认按推荐执行）

| # | 决策 | 推荐 | 备选 |
|---|---|---|---|
| D1 | `/stats` 的 `glm_balance` 行 | **删**（编码套餐 key 不适用 user/balance，永远报"余额查询失败"；悬浮窗已由 quota/limit 端点承担，此字段是重复且坏的死面） | 保留并修（需先实证智谱编码套餐有没有可用余额端点——大概率没有） |
| D2 | widget updater endpoint 三选一（release.md「开始前」） | **选项 2 固定 tag 指针**：CI 发版后自动 `git tag -f widget-latest`，endpoint 指向 `releases/download/widget-latest/latest.json`——零新仓库、CI 一步自动化 | 选项 1 独立仓库（最干净但要管两仓）；选项 3 gh-pages（多养一个分支） |
| D3 | Kimi/DeepSeek 两把 key 何时给 | **本役内、你在场时给**（cc-switch DB 读取或直接粘贴），Task 5 当场点亮 | 只备好 runbook，key 以后再配（Task 5 挂起） |

**事实核查结论（2026-09-25 本会话实测，影响任务范围）**：
1. `/report` JSON 非法 `\` 转义（启用役遗留②）**在现行构建上不复发**——CLI `ferryman account report --json`（289KB）与 HTTP `/report`（114KB）双路径 `json.loads` 均 VALID。该缺陷应出自换装前的来路存疑构建 `v0.1.3-2-g5af93ea`。→ Task 3 从"修复"降级为"回归钉 + 销账"。
2. daemon `/widget/summary` 已是多上游就绪（并行补拉 + 缓存 + 名字排序），widget UI 渲染层对 `upstreams[]` 完全泛化（demo 本来就三家）——点亮 Kimi/DeepSeek **零代码改动，纯配置**。
3. M5（硬编码 7311）票 09 钦定记录在案不修；设置窗自启开关双写 Run 键为外观问题——均不在本役。
4. 本机 config.toml `[dock]` 只有手写的智谱条目（09-23 直连役所写），首启迁移的三条预置从未落地——Task 5 按预置同形手写两条。

**依赖关系**：Task 1–4 相互独立可并行。Task 5 独立（仅需 D3 的 key）。**Task 6 的密钥生成（人工）解锁 Task 4 的部署**——本地 `npm run tauri build` 现在硬要求签名环境（release.md「附」），所以 Task 4 代码先行、部署并入 Task 6 发版流程。

---

### Task 1: `/stats` glm_balance 行下线（死码清理）

**Files:**
- Modify: `internal/daemon/health.go`（删第 7-8 行注释里的 glm_balance 描述、第 89 行字段、第 93-111 行 `glmBalance()`；按编译器提示清理 `errors`/`config`/`dock` 失效 import）
- Delete: `internal/daemon/health_balance_test.go`（整文件，3 个测试全是 glm_balance）
- Delete: `internal/daemon/balance_active_wire_test.go`（整文件）
- Modify: `internal/daemon/dock_active_test.go:197-228`（删 `TestHealthGLMBalanceFollowsActiveUpstream` 整函数——删后该文件对 `balancePanelKey`(:212) 的唯一引用消失，`httptest`/`atomic` import 若无他测引用一并清理）
- Delete: `internal/dock/balance.go` + `internal/dock/balance_test.go`（`FetchBalance` 唯一消费方是 health.go，删后死码；删除前 grep 确认无其他引用）

**Interfaces:**
- Produces: `/stats` 响应从此**不含** `glm_balance` 键（该键是票 07 Go 侧增量，非 Python 契约字段；仓内 WebUI 面板 JS 未消费——`cmd/ferryman/web/` grep 零命中；widget 走 `/widget/summary` 不受影响）。
- 保留不动：`config.DockUpstream.BalanceURL` 字段与 `DefaultDockBalanceURL`（inert——用户 config.toml 里的 `balance_url` 与首启迁移回写都还指着它，动它要连坐 dock_migrate 往返测试，留后续清理役；在本任务提交信息里注记）。

- [ ] **Step 1: 基线绿**

Run: `go test ./internal/daemon/ ./internal/dock/`
Expected: PASS（改动前先确认两包全绿）

- [ ] **Step 2: 删除测试面**

删 `health_balance_test.go`、`balance_active_wire_test.go` 两文件；删 `dock_active_test.go` 里 `TestHealthGLMBalanceFollowsActiveUpstream` 函数（197-228 行）。

- [ ] **Step 3: 删除生产面**

`health.go`：删 `"glm_balance": d.glmBalance(),` 行、`glmBalance()` 函数、头部注释中 glm_balance 描述句；清 import。
确认无残留引用后删 `internal/dock/balance.go`、`internal/dock/balance_test.go`：

Run: `grep -rn "glm_balance\|glmBalance\|FetchBalance\|BalanceInfo" internal/ cmd/ --include='*.go' | grep -v _test`
Expected: 仅剩 config 侧 `BalanceURL`/`DefaultDockBalanceURL`（保留项），无 daemon/dock 命中

- [ ] **Step 4: 包测试过**

Run: `go test ./internal/daemon/ ./internal/dock/`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add -A internal/daemon internal/dock
git commit -m "refactor(daemon): /stats glm_balance 行下线——编码套餐 key 不适用 user/balance 永远报失败，悬浮窗已由 quota 端点承担；死码 dock/balance 一并退役（config.BalanceURL 字段 inert 保留，后续清理役处理）

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Kimi resetTime 数字串兼容（评审 M3）

**Files:**
- Modify: `internal/quota/kimi.go`（`resetTimeOf` 字符串支补数字判位；新增 `strconv` import）
- Test: `internal/quota/quota_test.go`（追加两个测试）

**Interfaces:**
- Consumes: 既有 `resetTimeOf(v any) (time.Time, bool)`（包内私有）、`time.UnixMilli`、阈值 `1_000_000_000_000`（与数字支同款：秒级 < 1e12，毫秒 ≥ 1e12）。
- Produces: `resetTimeOf("1761412800")` → `time.Unix(1761412800, 0), true`；`"1790280000000"` → `time.UnixMilli(...), true`；非数字非 ISO / 空 / ≤0 → `false`（现状语义不变）。

背景：Kimi `resetTime` 若以数字**字符串**到达（JSON `"resetTime": "1761412800"`），现行字符串支只试 ISO 布局、全败即判无重置——窗口静默丢倒计时。cc-switch 的 `extract_reset_time` 字符串支直传不解析（同款缺口，本仓补上不照抄）。

- [ ] **Step 1: 写失败测试**

追加到 `internal/quota/quota_test.go`：

```go
// TestResetTimeNumericString M3（2026-09-25 评审遗留）：resetTime 为数字串时
// ISO 布局全败须按秒/毫秒判位解析——此前静默丢重置时刻（HasReset=false 无倒计时）。
// cc-switch extract_reset_time 字符串支直传不解析（自身同缺口），本仓补上。
func TestResetTimeNumericString(t *testing.T) {
	sec, ok := resetTimeOf("1761412800")
	if !ok || !sec.Equal(time.Unix(1761412800, 0)) {
		t.Fatalf("秒级数字串 = %v %v, want %v", sec, ok, time.Unix(1761412800, 0))
	}
	ms, ok := resetTimeOf("1790280000000")
	if !ok || !ms.Equal(time.UnixMilli(1790280000000)) {
		t.Fatalf("毫秒级数字串 = %v %v", ms, ok)
	}
	for _, bad := range []string{"", "abc", "0", "-5"} {
		if _, ok := resetTimeOf(bad); ok {
			t.Fatalf("%q 应判无重置", bad)
		}
	}
	// ISO 形态不回归
	iso, ok := resetTimeOf("2026-09-28T00:00:00+08:00")
	if !ok || iso.Format(time.RFC3339) != "2026-09-28T00:00:00+08:00" {
		t.Fatalf("ISO 字符串回归 = %v %v", iso, ok)
	}
}

// TestParseKimiNumericStringReset parseKimi 全链路：数字串 resetTime 进窗。
func TestParseKimiNumericStringReset(t *testing.T) {
	body := []byte(`{"usage":{"limit":5000,"remaining":850,"resetTime":"1790280000"},
		"limits":[{"detail":{"limit":1500,"remaining":1200,"resetTime":"1790280000000"}}]}`)
	q, err := parseKimi(body)
	if err != nil {
		t.Fatal(err)
	}
	if !q.Week.HasReset || !q.Week.ResetsAt.Equal(time.Unix(1790280000, 0)) {
		t.Fatalf("周窗数字串 resetTime = %+v", q.Week)
	}
	if !q.FiveHour.HasReset || !q.FiveHour.ResetsAt.Equal(time.UnixMilli(1790280000000)) {
		t.Fatalf("5h 窗毫秒数字串 resetTime = %+v", q.FiveHour)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/quota/ -run 'TestResetTimeNumericString|TestParseKimiNumericStringReset' -v`
Expected: FAIL（`ok=false` / HasReset=false）

- [ ] **Step 3: 最小实现**

`internal/quota/kimi.go` 的 `resetTimeOf` 字符串支，ISO 循环失败后、`return time.Time{}, false` 之前插入：

```go
		// M3（2026-09-25）：数字串（"1761412800" 秒 / 毫秒）——ISO 全败后按
		// 数字判位（阈值与数字支同款；cc-switch 字符串支直传不解析，此处补上）。
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
			if n < 1_000_000_000_000 {
				n *= 1000
			}
			return time.UnixMilli(n), true
		}
```

注意：字符串支入口已 `s := x`——先把 `s` 统一 `strings.TrimSpace`（现状未剪空白；`" 1761412800 "` 也该认）。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/quota/ -v`
Expected: PASS（整包，含既有 GLM/Kimi/DS 用例无回归）

- [ ] **Step 5: 提交**

```bash
git add internal/quota/
git commit -m "fix(quota): Kimi resetTime 数字串兼容（评审 M3）——ISO 布局全败后按秒/毫秒判位解析，不再静默丢倒计时；cc-switch 字符串支同缺口本仓补上

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: /report JSON 合法性回归钉 + 遗留②销账

**Files:**
- Modify: `internal/report/report_test.go`（强化 `TestRunJSONOutput`，复用既有 `captureStdout`/`testEnv`/`stubRun` 接缝）
- Modify: `C:\Users\allan716\.claude\projects\C--WorkSpace-agent-Ferryman\memory\ferryman-dock-cutover-campaign-20260923.md`（遗留②销账，非 git 面）

**Interfaces:**
- Consumes: `captureStdout(t, fn)`（report_test.go:122）、`accounts.Fields`、`Run(Args{JSON: true})`。
- Produces: 无生产码改动（纯测试加固）。

- [ ] **Step 1: 强化测试**

把 `TestRunJSONOutput`（report_test.go:235-241）替换为：

```go
func TestRunJSONOutput(t *testing.T) {
	env := testEnv(t)
	// 遗留②回归钉（2026-09-25）：project 含 Windows 反斜杠路径的行进 --json，
	// 输出必须是合法 JSON（历史缺陷形态：裸 \ 转义，json.loads 炸；实测出自
	// v0.1.3-2-g5af93ea 来路存疑构建，现行 json.Encoder 路径不复发——本钉防
	// 未来任何手拼 JSON 发射器回归）。
	if _, err := env.acc.Record("bypass", accounts.Fields{
		"agent": "cc", "session_id": "s9", "lineage_id": "L9",
		"project": `C:\Users\allan716\proj`, "prefix_tokens": 1,
	}); err != nil {
		t.Fatal(err)
	}
	stubRun(t, env)
	out := captureStdout(t, func() {
		if Run(Args{JSON: true}) != 0 {
			t.Fatal("run != 0")
		}
	})
	if !json.Valid([]byte(out)) {
		t.Fatalf("--json 输出不是合法 JSON（裸反斜杠转义？）: %.120s", out)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if _, ok := parsed["savings"]; !ok {
		t.Fatal("输出缺 savings 键")
	}
}
```

- [ ] **Step 2: 跑测试确认通过**

Run: `go test ./internal/report/ -run TestRunJSONOutput -v` 然后 `go test ./internal/report/`
Expected: PASS（现行实现本就合法——这是钉不是修）

- [ ] **Step 3: 遗留②销账（memory 面）**

编辑 `ferryman-dock-cutover-campaign-20260923.md` 遗留段：把「②account report 的 JSON 含非法 \ 转义……report 侧待修」改为已销账结论（2026-09-25 双路径实测 VALID；缺陷出自 v0.1.3-2-g5af93ea 存疑构建；回归钉在 report_test.go TestRunJSONOutput）。

- [ ] **Step 4: 提交（repo 面）**

```bash
git add internal/report/report_test.go
git commit -m "test(report): --json 合法性回归钉（遗留②销账）——反斜杠路径行进输出必须可解析；实测现行 CLI/HTTP 双路径均合法，缺陷出自换装前存疑构建 v0.1.3-2-g5af93ea

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: widget 评审 M2——网络失败退避 + 恢复即拉

**Files:**
- Modify: `widget/ui/data.js`（`startPolling` 改自适应 setTimeout 链 + 暴露 `poke()`；返回值从 `stop()` 函数改为 `{stop, poke}`）
- Modify: `widget/ui/app.js`（:468 调用点接新返回值；新增 `widget-restored` 事件监听调 `poke()`）
- Modify: `widget/src-tauri/src/lib.rs`（托盘 "show" 处理器 :268-273 与单实例回调 :189-194 各加一行 `emit`）
- Modify: `widget/ui/tests/assert-static.mjs`（追加 M2 断言）
- Modify: `widget/ui/tests/SMOKE.md`（追加人工冒烟项）

**Interfaces:**
- Produces（data.js 导出面变化）: `startPolling(onUpdate)` 返回 `{ stop: () => void, poke: () => void }`（**破坏性变更**——唯一调用方 app.js:468 同 commit 适配）；新增导出常量 `RETRY_MS = 10000`、`RETRY_MAX_MS = 60000`。
- Produces（Rust→前端事件）: `widget-restored`（无载荷）——托盘「显示悬浮窗」与单实例拉起时 emit；app.js 经 `t.event.listen('widget-restored', ...)` 消费。
- 语义（票 09 票面原文「30s 轮询 + 从收起恢复即拉 + 网络错误退避」）：成功 → 30s 后下一轮；失败 → 10s 起指数退避（10s→20s→40s→60s 封顶），成功即复位；`poke()` → 清计时器立刻走一轮（恢复即拉）。

- [ ] **Step 1: data.js 重写 startPolling**

替换 `startPolling` 整函数（data.js:181-203），并在 `POLL_MS` 旁新增两常量：

```js
/** 失败快重试基值（daemon 灰化期间 10s 起探——30s 全速轮询钉着坏上游无意义）。 */
export const RETRY_MS = 10000;
/** 连败退避上限（10s→20s→40s→60s 封顶；成功即复位 30s 常规周期）。 */
export const RETRY_MAX_MS = 60000;
```

```js
/**
 * 30s 轮询框架 + 网络错误退避 + 恢复即拉（票 09 票面原文；评审 M2 补齐）。
 * demo 模式对内嵌副本空转（可选注入灰化）；live 模式经 resolveDaemonTarget
 * 拿端点+Bearer token（壳外无注入=不可达，如实灰化）。启动立即回调一次。
 *
 * @param {(summary:Summary|null, reachable:boolean)=>void} onUpdate
 * @returns {{stop:()=>void, poke:()=>void}} stop=停轮询；poke=恢复即拉
 *   （托盘显示/单实例拉起事件触发——清计时器立刻走一轮，不等下个周期）
 */
export function startPolling(onUpdate) {
  let timer = null, stopped = false, fails = 0;
  async function tick() {
    if (isDev()) {
      onUpdate(maybeSuperset(DEMO_SUMMARY), !forceGray());
      return !forceGray();
    }
    const target = await resolveDaemonTarget();
    if (!target) { onUpdate(null, false); return false; }
    try {
      const r = await fetch(target.url, {
        headers: target.token ? { Authorization: `Bearer ${target.token}` } : {},
        signal: AbortSignal.timeout(8000), // daemon 冷缓存最长 5s 外呼+装配，留 8s
      });
      if (!r.ok) throw new Error(String(r.status));
      onUpdate(/** @type {Summary} */ (await r.json()), true);
      return true;
    } catch {
      onUpdate(null, false); // daemon 不可达=整体灰化+⚠（不假造数据）
      return false;
    }
  }
  async function loop() {
    const ok = await tick();
    if (stopped) return;
    let delay = POLL_MS;
    if (!ok) {
      fails = Math.min(fails + 1, 4);
      delay = Math.min(RETRY_MS * 2 ** (fails - 1), RETRY_MAX_MS);
    } else {
      fails = 0;
    }
    timer = setTimeout(loop, delay);
  }
  function poke() {
    if (stopped) return;
    if (timer) clearTimeout(timer);
    loop();
  }
  loop(); // 立即一次（同步落定首屏）
  return { stop: () => { stopped = true; if (timer) clearTimeout(timer); }, poke };
}
```

- [ ] **Step 2: app.js 接新返回值 + 事件监听**

app.js:468 调用点改：

```js
const poller = data.startPolling((summary, reachable) => {
```

（回调体不动、原来对返回值的丢弃改为持有 `poller`。）在同文件既有 `t.event.listen('profile-changed', ...)`(:429) 的同一 `t` 守护块内追加：

```js
  // M2 恢复即拉：托盘「显示悬浮窗」/单实例拉起 → 立即重探一轮（不等下个周期）
  t.event.listen('widget-restored', () => poller.poke());
```

注意：`poller` 声明须在 listen 注册可达作用域内（listen 回调是异步触发，闭包引用即可）。

- [ ] **Step 3: lib.rs 两处 emit**

单实例回调（:189-194）与托盘 "show" 分支（:268-273）各在 `w.show()` 后加：

```rust
                        let _ = app.emit("widget-restored", ());
```

（`Emitter` 已在 import 列表 :8。两处上下文都有 `app` 句柄。）

- [ ] **Step 4: 静态断言追加**

`widget/ui/tests/assert-static.mjs` 追加（跟随该文件既有 `check(...)` 风格与变量名）：

```js
  // M2（2026-09-25）：失败退避 + 恢复即拉
  check('m2.退避常量与自适应轮询在场', /RETRY_MS/.test(datajs) && /RETRY_MAX_MS/.test(datajs) && /setTimeout\(loop/.test(datajs), '');
  check('m2.startPolling 暴露 poke', /poke\(\)/.test(datajs) && /return \{ stop:/.test(datajs), '');
  check('m2.app 监听恢复事件并即拉', /widget-restored/.test(appjs) && /poller\.poke\(\)/.test(appjs), '');
```

（按文件顶部既有的 `datajs`/`appjs` 源文本变量实际名对齐；若变量名不同以文件为准。）

- [ ] **Step 5: 跑静态断言**

Run: `node widget/ui/tests/assert-static.mjs`
Expected: 全过（计数比之前 +3）

- [ ] **Step 6: Rust 编译检查**

Run: `cargo check --manifest-path widget/src-tauri/Cargo.toml`
Expected: 编译通过零告警新增

- [ ] **Step 7: SMOKE.md 追加人工项**

`widget/ui/tests/SMOKE.md` 托盘节后追加：

```markdown
## 6. 评审 M2 · 恢复即拉与退避（接真数据后人工过）

- [ ] 杀 daemon → 小窗 30s 内灰化+⚠ → 观察重试间隔 ~10s→20s→40s（退避）而非固定 30s
- [ ] 重启 daemon → 小窗 ≤10s 内自愈（不等 30s 全周期）
- [ ] 托盘「收起到托盘」→「显示悬浮窗」→ 数据立即刷新一轮（恢复即拉）
```

- [ ] **Step 8: 提交**

```bash
git add widget/ui widget/src-tauri/src/lib.rs
git commit -m "feat(widget): 评审 M2——网络失败指数退避（10s→60s 封顶，成功复位 30s）+ 托盘恢复/单实例拉起即拉；startPolling 返回 {stop,poke}

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

**部署注记**：本任务的产物**不单独发版**——本地 `npm run tauri build` 现在硬要求签名环境（release.md「附」），随 Task 6 密钥配好后一并构建部署。

---

### Task 5: 点亮 Kimi/DeepSeek（配置部署，需你在场给 key——决策 D3）

**Files:**
- Modify: `C:\Users\allan716\ferryman\config.toml`（**本机配置，非 git 面**——追加两个上游条目）
- 无代码改动（daemon 多上游就绪、widget 渲染泛化均已核实）

**Interfaces:**
- Consumes: 既有 `[dock.upstreams]` 表结构、`ferryman upstream list`、`POST /shutdown` + `wscript ~/ferryman/start-daemon-hidden.vbs` 重启流程、`/widget/summary` 契约（条目名=契约 id=widget profile 键）。
- Produces: `/widget/summary` 的 `upstreams[]` 从 1 条变 3 条；widget 出现 Kimi（双环+倒计时）与 DeepSeek（¥ 余额圆控件）圆控件。

**已知口径变化（预期内，非 bug）**：`widgetLedgerAggregate` 的单上游兜底（claude-* 行全归智谱）随多上游消失——**月 token 估算会下降**，降幅≈9月 09-23 直连切换前的 claude-* 行用量（宁缺勿猜口径，代码头注已披露）。09-23 后的 glm-* 行按 model_map 命中不受影响。

- [ ] **Step 1: 换装前快照（对账基准）**

```bash
TOKEN=$(cat ~/ferryman/daemon.token)
curl -s -H "Authorization: Bearer $TOKEN" http://127.0.0.1:7311/widget/summary > .scratch/pre-lightup-summary.json
```

记下 GLM 条目 `month_tokens` 现值。

- [ ] **Step 2: 配置追加两上游（key 由你提供，当场填入）**

`config.toml` 的 `[dock.upstreams."智谱"]` 块之后追加（形状=dock_migrate.go:41-48 预置同款；`active` 不动仍指智谱）：

```toml
[dock.upstreams.kimi]
base_url = "https://api.kimi.com/coding/"
api_key = "<你的 Kimi key>"
model_map = { default = "kimi-for-coding", sonnet = "kimi-for-coding", opus = "k3", haiku = "k3-256k" }

[dock.upstreams.deepseek]
base_url = "https://api.deepseek.com/anthropic"
api_key = "<你的 DeepSeek key>"
model_map = { default = "deepseek-flash", sonnet = "deepseek-flash", haiku = "deepseek-flash", opus = "deepseek-v4-pro" }
```

key 来源二选一（你在场授权）：从 cc-switch DB 对应条目读取，或你直接粘贴。**key 不出现在任何输出里。**

- [ ] **Step 3: 优雅重启 daemon（observe 周纪律：记一次重启）**

```bash
TOKEN=$(cat ~/ferryman/daemon.token)
curl -s -X POST -H "Authorization: Bearer $TOKEN" http://127.0.0.1:7311/shutdown
sleep 3
wscript C:\\Users\\allan716\\ferryman\\start-daemon-hidden.vbs
```

注意：daemon 重启**可能换 token**（EnsureToken 重落盘）——后续 curl 前重读 `~/ferryman/daemon.token`。

- [ ] **Step 4: 验证三面**

```bash
./ferryman.exe upstream list
TOKEN=$(cat ~/ferryman/daemon.token)
curl -s -H "Authorization: Bearer $TOKEN" http://127.0.0.1:7311/widget/summary | python -m json.tool
curl -s -H "Authorization: Bearer $TOKEN" http://127.0.0.1:7311/health
```

Expected：
- `upstream list` 三条目、kimi/deepseek key 状态掩码非空、active 仍=智谱；
- `/widget/summary` upstreams 三条：kimi 有 `window_5h`/`week` 环（或如实错误类别——key 坏则出「上游状态 401」类文案，也是点亮成功的证明：查询器在打真端点）；
- GLM 条目 `month_tokens` ≈ Step 1 快照值减去 09-23 前用量（口径变化见上）；/widget 30s 内自动出新圆控件（无需重启 widget——轮询自动吃新契约）。

- [ ] **Step 5: 回滚路径（如需）**

删两个条目块 → 重复 Step 3 重启。widget 侧无状态可回（渲染泛化，条目消失圆控件即消失）。

---

### Task 6: widget 自升级发布侧（决策 D2 + CI 落地 + 人工四件套）

**Files:**
- Modify: `.github/workflows/widget.yml`（release 作业末尾追加 tag 前移步骤——**若 D2 选选项 2**）
- Modify: `widget/src-tauri/tauri.conf.json`（updater endpoint 改指 widget-latest——同上）
- Modify: `widget/docs/release.md`（「开始前」三选项落定注记）

**Interfaces:**
- Consumes: 既有 release 作业（`permissions: contents: write` 已配，actions/checkout 凭据可推 tag）。
- Produces: updater endpoint `https://github.com/allanpk716/ferryman/releases/download/widget-latest/latest.json`（常驻 tag 永不被主程序发版顶掉）；首个 `widget-vX.Y.Z` 发版起通道生效。

- [ ] **Step 1: CI 追加 tag 前移（选项 2）**

widget.yml release 作业资产上传步骤之后追加：

```yaml
      - name: 前移 widget-latest 常驻 tag（updater endpoint 指向它，防主程序发版顶掉 latest 通道）
        run: |
          git config user.name "github-actions[bot]"
          git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
          git tag -f widget-latest "$GITHUB_SHA"
          git push -f origin widget-latest
```

（作业 `defaults.run.working-directory: widget` 对 git 命令无碍——子目录里 tag/push 照常生效。）

- [ ] **Step 2: tauri.conf.json endpoint 改写**

`plugins.updater.endpoints[0]`：

```json
"endpoints": [
  "https://github.com/allanpk716/ferryman/releases/download/widget-latest/latest.json"
]
```

（tag 未建前该 URL 404——与现状 latest/download 404 同为诚实报错路径，行为不劣化。）

- [ ] **Step 3: release.md 落定注记**

「开始前」节追加一行：2026-09-25 拍板选项 2（固定 tag 指针），CI 已自动化前移步骤。

- [ ] **Step 4: 提交**

```bash
git add .github/workflows/widget.yml widget/src-tauri/tauri.conf.json widget/docs/release.md
git commit -m "ci(widget): widget-latest 常驻 tag 自动前移 + updater endpoint 指向——防主程序发版顶掉 latest 通道（release.md 三选项落定选项 2）

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 5: 人工四件套（你操作，runbook=release.md 第 1-4 步 + 验收单）**

1. `cd widget && npx tauri signer generate -w ~/.tauri/ferryman-widget.key`（设密码并灾备）
2. pubkey 整行替换 tauri.conf.json 占位串 → 提交
3. GitHub Secrets 配 `TAURI_WIDGET_SIGNING_PRIVATE_KEY` / `..._PASSWORD`
4. 三处版本号 bump → `git tag -a widget-vX.Y.Z` → push → 验 Releases 三件套资产 + `widget-latest` tag 指向它

- [ ] **Step 6: 双版本升级实测 + 部署 Task 4 产物**

本机装当前版（v0.2.x 手动换 exe 或走安装包）→ 托盘「检查更新」→ 升级到新 tag 版；顺手验证 M2 冒烟项（SMOKE.md §6）。篡改包验签失败路径按 release.md 验收单第 4 项抽测。

---

## 明确不做（防翻案 / 已挂起）

- **DS 花费计价**（票 07 尾巴）：无 DS 流水无价格表，等 DS 真承载流量再议。
- **M5 硬编码 7311**：票 09 钦定记录在案。
- **config.BalanceURL 惰性字段清理**：连坐 dock_migrate 往返测试，后续清理役。
- **设置窗自启开关双写 Run 键**：外观问题单实例去重，无功能影响。
- **压缩赛道 / 同模型摆渡启用**：等 10-07 评估日数据说话。

## 战役收尾

全任务完成后：晨报更新（.scratch/usage-widget/ 新一篇或追加节：五件事落定 + 口径变化披露）、memory 更新（悬浮窗战役文件补 Kimi/DeepSeek 点亮与 M2/M3 收口）、`git push`（推不推你定，main 当前与 origin 平齐）。
