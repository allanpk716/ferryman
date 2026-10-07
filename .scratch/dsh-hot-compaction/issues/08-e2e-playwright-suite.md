# 票08 · E2E Playwright 全链剧本（全自动验收）

## What to build
tools/e2e_dsh/suite：Playwright 驱动票07 沙箱栈的全链验收剧本+断言库：token 登录→建会话→多轮对话灌上下文至超阈值→闲置等待触发→断言账本 kind=compacted、poll/compacted HTTP 往返、横幅 DOM→继续发消息不被拦（无选择卡、账本 allow reason=compacted-short-prefix）→交接文件落盘存在→降级链回归（停沙箱 daemon 插件通道后，闲置过线再发消息→选择卡出现）。剧本可重复跑（幂等建会话）。

## 验收标准
- [ ] 全链剧本一键跑绿（对沙箱栈），断言含上列每一项
- [ ] 断言失败时输出可定位诊断（截断的账本行+DOM 状态）
- [ ] 降级链回归断言含在内
- [ ] 若票01 结论为状态错乱：对应竞态断言按票01 清单跳过并标注 waiting，其余照跑
- [ ] 剧本全程零人工介入（无人值守验收，D7）

## Blocked by
票07（栈）、票02/03/04（daemon 全链）、票05/06（插件全链）

## 涉及路径
- tools/e2e_dsh/suite/（新建）

## 副作用声明
对沙箱栈端口 25xxx 起 Playwright；禁碰生产端口；账本断言只读沙箱数据目录

decision_refs: D5 D7
review_blocks: F3 F5(部分)
