# Spec · T48 异步等待信号修复（async wait signal）

- **日期**：2026-09-18（夜链固化；上游=rev1 计划 + 两轮异构评审附录，`docs/superpowers/plans/2026-09-18-t48-async-wait-signal.rev1.md` 文末）
- **词汇表**：CONTEXT.md（等待窗口/停车/缺口A 条目随票 05 更新）

## Problem Statement

主会话以异步方式派发子代理（工具秒回、真身跑 25–35 分钟）期间，Ferryman 三处信号失真：等待窗口台账把真实等待记成 ~0.5 分钟（Stop 事件早到）；"子代理在飞"计数归零使缺口 A 闸门豁免失明（enforce 模式下用户中途输入可能被当凉会话拦截）；摆渡把机器等待误判为"人不在"闲置（等待中途白做交接总结）。后果：用户在编排型会话里被错误对待、心跳复盘（T50）拿不到真实窗口数据。

## Solution（用户视角）

主会话等异步子代理期间：闸门放行（带豁免提示）、不生成摆渡、等待窗口按真实时长入账（结束时记 `main_resumed`）；等待超过 1 小时才放弃跟踪（记 `expired`，豁免与摆渡随之恢复）。同步子代理行为完全不变。

## User Stories

1. 作为编排会话的用户，我想要主会话等异步子代理期间向其输入消息被放行（豁免提示、不拦、不弹交接），以便中途补充的输入排队生效而不是被打断。
2. 作为成本管理者，我想要每个等待窗口按真实起止与时长入账（含前缀规模），以便 T50 心跳复盘与策略对比有可信数据。
3. 作为成本管理者，我想要机器等待期间不误触发生成摆渡总结，以免无意义的交接文件与噪音。
4. 作为守护进程运维者，我想要窗口记账/台账路径的任何异常都被吞掉且不影响闸门与守望主路径，以便服务持续可用。
5. 作为 CC 用户，我想要同步子代理（非后台派发）的窗口与豁免行为与现状完全一致，以便零回归风险。

## Implementation Decisions

（决策定案，符号与接口形状给实现；完整代码级设计见上游 rev1 计划与其评审附录——**附录 13 条清单是实施必改项**）

1. **判定函数** `has_async_launch(path) -> bool`（transcripts 层）：读转录尾部 256KB，取最后一个 Task/Agent tool_use；其 input 的 `run_in_background`/`background` 为真，**或**其 tool_result 首个文本块以 `"Async agent launched"` **为前缀**（严格前缀，防同步 result 复读文案误判——附录#6）即 True；其余一律 False（含坏行/文件缺失；`message` 非 dict 行安全跳过——附录#11）。
2. **窗口结构**（内存，`(agent, sid) → {opened_ts, stop_ts, saw_async}`，跨 HTTP/守望线程加 RLock）：
   - 开窗：首个 start（沿用 R10 重锚，重锚旧停车窗如实闭账——按 `now - stop_ts > PARK_EXPIRE_S` 判过期而非 opened_ts，`closed_ts = min(stop_ts+PARK_EXPIRE_S, now)`——附录#3/#13）。
   - 停车（count 归零时）：`saw_async` 已锁存或尾判 async → `stop_ts=now, saw_async=True`；否则即闭 `subagents_done`（旧语义）。
   - **latch 置位时机**：start 事件处理时若尾判 async 亦置 `saw_async`（覆盖"sync start 先于 async stop"的重叠序——附录#7）。
   - 续窗：停车窗遇新 start 清 `stop_ts`（窗口连续）。
3. **闭窗三道**：① 用户 prompt（仅未停车窗，`prompt`）；② 主会话恢复调用：新 usage 行 `ts > stop_ts + ACK_GRACE_S(90s)` → `main_resumed`（宽限防 ack 确认回合误闭——当天实测 ack 均 fallback 在 stop 前，宽限是时序反转保险）；③ 停车超 `PARK_EXPIRE_S(=3600，独立命名)` → `expired`（后果如实声明：豁免失效+摆渡可恢复入队，与计数道同界）。
4. **豁免/推迟**：`window_wait(agent, sid) -> bool`（停车未过期）作为缺口 A 第三道（`_machine_waiting`）与摆渡推迟（`_maybe_enqueue`）的共同信号；谓词与 `note_usage` 均保证不抛（记账异常吞掉；`note_usage` 不得"先丢窗后记账失败"——附录#10）。
5. **接线**：`Watcher` 增可选 `ferry_daemon` 引用（默认 None，旧测试零改动）；usage 采集逐轮喂 `note_usage(st.agent, sid, max_row_ts)`；serve/Harness 传参。
6. **常量**：`PARK_EXPIRE_S`、`ACK_GRACE_S` 定义在 server 模块常量区（注释含运营后果）；测试 import 真常量不复制字面量（附录#12）。
7. **隐私不变量**：判定只返回 bool，账本永不落消息内容。

## Testing Decisions

- 单测（pytest）：transcripts 判定（含同步复读文案负例）；server 状态机（两阶段写文件的交错用例——附录#1、重叠事件序用例——附录#7、宽限内 ack 不闭、停车豁免、过期（回拨 stop_ts）、`window_wait`/`note_usage` 异常路径（patch 面收窄——附录#5）、prompt 只闭未停车窗）；daemon 摆渡推迟与恢复后放行。
- 集成（Harness）：写文件后**自然闲置**触发（summarize_s=1s，勿手改 last_write——附录#4）；对照会话（应摆渡）证明守望在跑后，断言停车会话未摆渡；ack 行在 stop 事件后落盘；恢复行时间戳 now+120s（附录#2/#8）。
- 全量测试绿是每个 commit 的前置条件（当前 204 用例基线）。
- 人工验收：A6 演练单 async 用例（票 05 落文档，用户择时执行）。

## Out of Scope

- 心跳执行器（用户铁律：未授权不实施）。
- viewer 改动（已核实 close_reason 自由文本直通，零改动兼容）。
- codex 轨停车（仅 cc 轨；codex 维持现状）。
- T49 压缩试点与 T50 心跳复盘的实施（协议待用户另批）。

## Further Notes

- **开发时要盯**（撞上即停下反馈，别默默绕过）：① CC 改版改掉 "Async agent launched" 文案或 hook 时序 → 停车退化为旧语义/窗口数据变回 0.5 分钟级；② >1h 的 async 等待 → 过期后豁免失效+摆渡入队属设计内，但若频繁误摆渡要反馈；③ latch 使"async 后再派 sync"整窗 dur 合并（按窗记账粒度取舍）。
- **T49 数字口径**（协议修正，附录#9）：前缀 26 万→15 万约减 42% 通行费（"70%"是通行费占账单比，非减幅）；压到 10 万约减 60%。
- 词汇表需补："ack 落宽限内且会话随后静默 → 该窗记 expired@1h，dur 虚高约 1h"变体声明（附录#12）。
