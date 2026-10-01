# 票 07 · 词汇表、收官骨架与白天 runbook

## What to build
1. **CONTEXT.md 增补词条**(术语表纪律:只收域词不收实现细节):「供应商」「dialect/方言(anthropic|openai_responses)」「翻译车道」「原生透传」「接管(apply)」「codex 可用性(原生/需翻译/不支持)」。
2. **战役收官记录骨架** docs/20260930_服务商接管战役收官记录.md:按票 01-06 的实际交付填写(实施范围、验收证据指针、未尽事项),预留 W4 白天段落。
3. **白天 runbook(W4 顺序,与战役计划 rev1 一致)**,写进收官记录:
   - apply→观察一天(CC 与 codex 含 orca 各跑一天真实工作);
   - 切换验收:建议各切一次"需翻译"与"原生 responses"分支(F12);
   - 托盘退出+自启关→重启→三件全绿终验(CC 走渡口/codex 走渡口/hooks=true);**任一步失败或任一件红=`ferryman provider apply --restore` 回 interim+手动拉起 cc-switch,修复后重来**;
   - 全绿后用户手工卸载(App 卸载器)→三件复检(三项配置仍指渡口/无残留自启/doctor 新增三项全绿);
   - 首周观察:doctor 报"CC 指向"=cc-switch 会话自动同步复开(F9 归因);orca 家是否再被重写;
   - 可选:跨供应商混合历史第五夹具形,白天用票 04 的重录工具低成本顺手录(F8,未采纳不强制)。
4. cmd/ferryman/README.md 命令清单补 provider 族一行式说明。

## 验收标准
- [ ] CONTEXT.md 词条齐且只收域词(零实现细节)
- [ ] 收官记录骨架含实施范围/验收证据指针/未尽事项/白天预留段
- [ ] runbook 与战役计划 rev1 的 W4 四步顺序逐字一致(含回退条款)
- [ ] README 命令清单更新
- [ ] 纯文档票:内容核对通过(与票 03/06 实际交付对齐)

## Blocked by
票 03(车道行为定型), 票 06(CLI 命令面定型)

## 涉及路径
- CONTEXT.md
- docs/20260930_服务商接管战役收官记录.md(新建)
- cmd/ferryman/README.md

## 副作用声明
无测试/构建副作用(纯文档票)

decision_refs: D10
review_blocks: 无
