# 票06 · 守护重启贫血修复(账本回放重建峰值)

## What to build
守护重启后,纯闲置的 dsh 会话不再"峰值归零":重建台账时对 peak==0 的 dsh 会话,从最近两个账本月文件的 usage/dock 条目回放,重算 peak(各请求计费输入三列之和的最大值);标题从最近交接文档头行取,没有就留空。boot 补记/去重 handed_off 的判定只认"未消耗交接真覆盖 last_write"(消耗感知来自票01 的 ValidHandoff 口径)。效果:重启后被拦的会话能真入队铸新交接、守望开窗条件不再被挡。

## 验收标准
- [ ] boot 重建台账时,dsh 会话 peak==0 → 回放最近两个账本月文件(按条目 session_id/lineage_id 匹配该会话)重建 peak
- [ ] 跨月边界用例:条目落上月文件也能回放(今晨在 9 月文件、重启在 10 月的场景)
- [ ] title 从最近交接文档头行取,无则留空(不报错)
- [ ] boot 补记/去重只认未消耗交接(与票01 口径一致,构造已消耗场景不再挡重摆)
- [ ] cc/codex 的 boot/enrich 路径零变化(dsh 跳过 enrich 的既有规则不动)
- [ ] 单测覆盖以上;watcher_boot/dsh_wiring 既有测试不破

## Blocked by
票01(ValidHandoff 消耗口径先行)。

## 涉及路径
internal/daemon/watcher_dsh.go
internal/daemon/watcher_boot_test.go
(如需新文件:internal/daemon/dsh_boot_replay.go;accounts 读取如需辅助函数,可加 internal/accounts/ 下新文件)

## 副作用声明
只跑 internal/daemon 与 internal/accounts 相关单测(不跑全仓)。

## decision_refs: D1、F1、F6、F7
## review_blocks: 无
