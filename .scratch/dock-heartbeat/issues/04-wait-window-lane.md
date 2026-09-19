# 04 · 等待窗泳道心跳排程：watcher 双泳道＋[wait_window] 配置

## What to build

1. **配置节**（internal/config）：`[wait_window]` 三态 `mode = "off" | "observe" | "enforce"`（默认 off，缺节即 off）、`manual_wait_cap_s`（可选，只能往下夹紧策略计算器算出的值）、泳道标记字段供记账。校验：`mode=enforce` 且配置无 `[dock]`（渡口关）→ 启动时告警并把等待窗侧按 observe 对待（问询守望不受此校验影响——它有 NoopSender 演练路径；enforce 且渡口关对问询守望也维持既有"nil sender→observe 演练"行为，不新增拒绝）。
2. **排程**（internal/daemon/watcher.go，风格对齐既有 `maybeFireBeats`/`fireOneBeat`）：新增等待窗泳道——
   - 判据（F8）：排跳 ⇔ **等待窗开着**（windows.go 现有窗口状态；含停车未过期窗＝异步子代理仍在跑；停车满 1h 懒过期窗口已闭→不排）。纯工具等待（无子代理→无窗）自然不覆盖。主会话恢复写入→窗口已闭→停。
   - 起跳：主会话闲置满 τ（policy 计算器现算，公式单源 internal/policy——**不得在 watcher 自带公式**）且前缀 ≥ 最小阈值（policy 输出）。
   - 节律：间隔与等待上限全部从 policy 计算器取（价格表＋实测 TTL＋该会话前缀大小闭式现算）；manual_wait_cap_s 只向下夹紧。
   - **复用全局单在途**：与问询守望共用同一在途占用/串行节奏（多窗同开排队，绝不并行）。
3. **窗口级熔断（F9，按窗计）**：1 次 MISS→停本窗剩余跳＋告警（建议复测 TTL，不自改配置）；连续 3 次 transport-ERROR→停本窗。问询守望现有全局 Breaker 语义一行不动。
4. **记账**：逐跳入既有 beat 科目并带泳道标记（wait/qwatch 区分字段或既有字段扩展，读 ledger/accounts 现有 Row 形状后选最小改动）；窗口收尾后主会话未回归→记无效保温一行（沿既有科目惯例）。
5. observe 态：走 NoopSender 演练（零网络）逐跳入账标 observe——与问询守望同型。

## 验收标准

- [ ] 排跳判据测试：窗开（含停车未过期）→到点排跳；停车满 1h 过期→不排；主会话恢复写入→不排；无窗（纯工具等待）→不排。
- [ ] policy 接线测试：间隔/上限来自计算器输出（fake policy 断言取值调用）；manual_wait_cap_s 只向下夹紧（配置更大值不生效）。
- [ ] 1 MISS 停窗＋告警；3 连 ERROR 停窗；问询守望 Breaker 既有测试零回归。
- [ ] enforce＋无 [dock] → 等待窗侧按 observe（告警一次），无真发送。
- [ ] 单在途：等待窗与问询窗同时到期→串行（一前一后），无并行发送。
- [ ] 记账行形状：beat 科目带泳道标记；无效保温行存在。
- [ ] mode 缺省 off：无配置时 daemon 行为与本版之前完全一致（既有测试零回归）。
- [ ] `go test ./...` 全绿。

## Blocked by

03（HttpBeatSender＋serve 注入）。

## 涉及路径

- internal/daemon/watcher.go（等待窗泳道）
- internal/daemon/windows.go（只读消费窗口状态；如需最小读接口，改动面最小化）
- internal/config/（[wait_window] 节＋测试）
- internal/ledger/ 或 internal/accounts/（泳道标记字段——读现有形状后最小改动＋测试）

## 副作用声明

- 独占验证命令：`go test ./internal/daemon/... ./internal/config/... ./internal/ledger/... ./internal/accounts/...`。

decision_refs: D2、D3、D9（不动项：停车窗 1h 语义只消费不改变）
review_blocks: 无（F8/F9 语义已在 spec 钉死）
