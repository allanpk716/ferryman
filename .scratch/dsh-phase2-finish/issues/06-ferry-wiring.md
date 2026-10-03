# 票 06 · 摆渡闭环接线（条件式规则，验收首项＝材料可得判定）

## What to build
**首步（验收首项，判定留痕）**：判定"事件上报口可提供会话键＋快照所需材料"——判据（spec 定案）：票 05 的 session/event 上报携带会话键（头行 id），且材料足以按捕获-重放前缀语义（ADR-0007 同源）重建快照输入。判定过程与结论写入本票产出的判定记录文件（`.scratch/dsh-phase2-finish/judge-materials.md`）。
**材料可得 ⇒** 接线 dsh 摆渡：`maybeEnqueue` 对 dsh 会话生效（事件口活动作为触发信号）＋同步补 enrich 的 dsh 分支（P2-1 遗留注记③——title/峰值等富集走事件/文件面，与 CC enrich 同构）；表驱动测试＋真机夹具（env 门控）双证。
**材料不可得 ⇒** 本票转为产出降级说明（判定记录文件内写明缺口与依赖项），不接线，验收勾"降级"项——「精华被渡」依赖晨间接法乙/直报决策。
二选一显式定案，不允许静默缺位（F1 定案规则）。

## 验收标准
- [ ] 判定记录文件落盘：判据逐条核对＋结论（可得/不可得）＋留痕
- [ ] 若可得：maybeEnqueue dsh 面接线＋enrich dsh 分支完成，表驱动测试＋真机夹具（env 门控）跑绿（落日志）
- [ ] 若不可得：降级说明完整（缺口/依赖项/晨间决策项），且不接线（代码面零改动除判定记录）
- [ ] 台账/守望键维持头行 id 定案（不另造键，测试断言）

## Blocked by
票 05

## 涉及路径
internal/daemon/、internal/enrich/

## 副作用声明
测试构建禁弹黑窗；`go test ./internal/daemon/ ./internal/enrich/`（enrich 若在别的包以实际为准）落 `.scratch/dsh-phase2-finish/logs/t06-*.log`；真机夹具 env 门控保 hermetic；不联网。

## decision_refs
F1（摆渡闭环规则）、F6（判定时点＝本票验收首项）、P2-1 遗留注记③

## review_blocks
F1
