# 票10 · pi 写入器:provider apply 第四目标

## What to build
internal/provider 写入器增 pi 目标(providerTargetsFromHome 增第四份):①~/.pi/agent/models.json:供应商条目改/增——api="anthropic-messages"、baseUrl=渡口地址(DockURLFromListen 同源)、apiKey=占位值(FERRYMAN_MANAGED 同款语义)、models 列表至少含 pi 主模型 id(自 active 上游条目 model_map 的 pi 键派生;列表空=异形拒绝);条目形态对照既有 anthropic-messages 旧条目(不带 compat);清理指向 15721 的死条目;②~/.pi/agent/settings.json:defaultProvider=新条目名、defaultModel=该条目 models 内的 pi 主模型——**两文件成对同批落盘**,任一异形(结构不合/模型名无法对齐/~/.pi 不存在)→整体拒绝该目标转人工,绝不半写;③纪律照搬 writer.go 既有:F7 式前置校验、幂等短路(已是目标形态零写零备份)、同戳备份(四份同组)、解析树自证(拔可变键 DeepEqual)、~/.pi 目录缺失→该目标 not_checked 式跳过并如实回显(不算失败);④部分失败恢复语义(F10):第二文件写败→按同戳备份恢复两份或提示幂等重跑收敛,单测钉"第二文件写败→恢复后与基线一致"。

## 验收标准
- [ ] 异形拒绝:models 空/defaultModel 无法对齐/结构不合→零写入零备份、报因转人工(单测)
- [ ] 幂等:已是目标形态→零写零备份
- [ ] 成对性:两文件同批生效;单测模拟第二文件写败→恢复一致
- [ ] 同戳备份四份一组;--restore 覆盖 pi 目标
- [ ] ~/.pi 缺失→跳过+回显,不失败
- [ ] 真机零触碰(路径全参数化,既有测试形态)
- [ ] go test ./internal/provider/ ./cmd/ferryman/ 绿

## Blocked by
票09(models 列表派生自 model_map pi 键,结构先定)

## 涉及路径
internal/provider/writer.go
internal/provider/writer_test.go
internal/provider/backup.go
cmd/ferryman/provider.go(apply 目标派生)
cmd/ferryman/provider_test.go

## 副作用声明
无独占验证命令;go test ./internal/provider/ ./cmd/ferryman/

## decision_refs
D4、D16;F1(写入侧)、F10

## review_blocks
无
