# 票02 · 同会话续用的交接注入面：默认零注入 + 用户开关 dsh_handoff_on_continue

## What to build

同一个 DSH 会话续用（强续首条/压缩后回来首条）时默认不再注入任何交接材料（上下文本就在会话内）；交接材料（锚定文档+候选清单）只给真新会话开场。提供用户开关 `[gate] dsh_handoff_on_continue`（默认 false=助手推荐、用户未拍板）：开启时续用时刻在横幅之外附最新己线交接文档。开关只控制己线交接文档、不控制候选清单（清单任何情况不附续用会话=设计判断）。restore 候选排序补全序第三键；插件注入去重改按首句模板。

背景机制（夜链预查证已钉死）：
- 15:41:18 同轮三注入=锚定文档（插件 onCreated 播种路径）+两份不同清单（MarkInjected 消费后候选集合法变化+用户步补注路径；dsh_dedup 全文比对拦不住内容不同的重复）
- 续用判定主依据=**转录可解析出事件**（size>0 仅快路径——防宿主新会话 created 即落头部行误判；F8）；peak_ctx 仅辅助（票03 已证闸门时刻读 0，不得作主判据或捷径；F3）
- 现行行为矛盾实证：15:41（无压缩）注入三份；16:20（压缩后）零注入——两时刻应统一为默认零注入

## 验收标准

- [ ] daemon：`/dsh/handoff` 对"转录可解析出事件的会话"返回 null（续用零注入）；真新会话（转录无事件）锚定/清单行为逐字不变
- [ ] 交叉面（必测）：PeakCtx=0 但转录非空的续用会话（重启后场景）默认零注入
- [ ] 开关两档：`dsh_handoff_on_continue=false` 续用只有横幅；`=true` 续用在横幅外恰附一份最新己线交接文档
- [ ] 开关任何档位下，续用会话不收到候选清单
- [ ] RestoreCandidates 排序全序（covers_until desc → created_at desc → handoff_id/路径字典序）；同状态连续 10 次查询应答逐字节一致
- [ ] 插件：同轮两份不同候选清单注入被模板去重拦下（构造 A/B 清单复现 15:41 形态）
- [ ] config.example.toml 补键+注释（注明"默认值=助手推荐待用户确认；开关只控制己线交接文档，不控制候选清单"）
- [ ] 新会话开场回归：锚定文档/候选清单既有用例零回归

## Blocked by

无，可立即开始

## 涉及路径

- internal/daemon/restore.go
- internal/daemon/dsh_receive.go
- internal/config/config.go
- internal/daemon/restore_test.go
- internal/daemon/dsh_receive_test.go
- plugin/ferryman-dsh/src/events.ts
- plugin/ferryman-dsh/src/daemon.ts（如回话形状需带开关语义）
- plugin/ferryman-dsh/test/（events/dedup 相关测试文件）
- config.example.toml

## 副作用声明

- daemon 侧：`go test ./internal/daemon/ -run 'Restore|DshReceive|Gate'`
- config：`go test ./internal/config/`
- 插件侧与票01共用 `npm test`——**两票并行时插件验证先由票01独占，本票插件改动先跑 `node --test test/<相关文件>` scoped，全量留给串行收尾**（避免验证命令互踩）
- 不安装依赖、不联网、不动 .xcheck/

## decision_refs

D1（范围）、D2（开关存在=用户确认）、D3（默认值=助手推荐未拍板，晨报置顶待确认——不得写成用户已确认）、D4（清单不附=设计判断非用户确认）

## review_blocks

无（F1/F2/F3 已解除；F8 已并入本票判据）
