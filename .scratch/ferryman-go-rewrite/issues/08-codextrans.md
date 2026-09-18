# 票 08 · codextrans（Codex rollout 防御读取器）

**What to build**：`internal/codextrans`（codex_transcripts.py 1:1）：XCTurn/TokenCountTurns（last_token_usage 口径，input 已含 cached）、SessionCwd（头 10 行）、ExtractCodex（message 正文 input_text/output_text；五个环境注入 user 前缀逐字丢弃；apply_patch 三前缀抽文件；cmd/command 抽命令；标题=首条 user 首行截 60）。行读无上限。

参照：rev1 Task 9。

**验收标准**：
- [ ] tests/test_codex_transcripts.py → codextrans_transcripts_test.go、tests/test_codex_extract.py → codextrans_extract_test.go（**两源各自成文件**，同包共存）
- [ ] 全部用例 1:1 移植且绿
- [ ] developer/system 角色、function_call_output、reasoning 均不进材料

**Blocked by**：07
