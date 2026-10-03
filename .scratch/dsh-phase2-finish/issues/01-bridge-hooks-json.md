# 票 01 · P2-3 桥仓库侧：dsh hooks.json 单发机制＋桥行为钉测试

## What to build
Ferryman 为 dsh 单发 CC 钩子桥所需的 hooks.json 文件生成机制：并入 `ferryman provider apply` 的 dsh 配置家族（幂等、同戳成组备份、还原纪律与 CC/codex/dsh 既有件完全一致），文件落 Ferryman 自家目录（`~/ferryman/dsh-hooks/hooks.json`——**绝不写 `~/.dsh/` 任何文件**）。内容复用现有 CC 钩子脚本（UserPromptSubmit 问闸门、block→deny 语义）。附晨间安装说明：`dsh plugin --profile web add @deepseek-ai/dsh-hooks-claude-code` 且 configPath 指向该文件（说明文字进 apply 输出提示或随附文档，不执行安装）。
同时按源码钉死纪律（约束 6，源码＝调研克隆 `C:/Users/allan716/AppData/Local/Temp/dsh-research/packages/hooks/`，只读）钉四组夹具测试：①桥接的 CC 钩子事件映射与 payload 形状；②deny/block 传导语义；③configPath 生效方式；④桥键零换算——`session_id = agent?.session.header.id`（`hooks-claude-code/src/index.ts:329`），含空串回退边界（agent 不可用，:355 注记）。

## 验收标准
- [ ] `provider apply` 幂等产出 dsh-hooks/hooks.json；二跑 unchanged；还原走既有 Restore 纪律
- [ ] 生成文件内容指向现有 CC 钩子脚本路径（不复制脚本本体）
- [ ] 四组钉测试全部落测试文件并通过（夹具来自桥源码事实，标注 file:line 出处）
- [ ] 不触碰 `~/.dsh/`（测试用临时目录断言写入目标前缀）
- [ ] 安装说明文字可让晨间人工照做

## Blocked by
无，可立即开始

## 涉及路径
internal/provider/、cmd/ferryman/provider.go

## 副作用声明
测试构建禁弹黑窗；`go test ./internal/provider/ ./cmd/ferryman/` 结果落 `.scratch/dsh-phase2-finish/logs/t01-*.log`（目录共享、各票文件名带票号，不互斥）；不联网、不装依赖。

## decision_refs
D12（活实例红线）、D11（测试纪律）、spec「桥行为钉死」节

## review_blocks
F2（已证伪——钉测试吸收其事实价值）、F3
