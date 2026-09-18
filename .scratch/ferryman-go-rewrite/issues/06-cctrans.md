# 票 06 · cctrans（CC 转录防御读取器）

**What to build**：`internal/cctrans`（transcripts.py 1:1）：Turn/AssistantTurns（usage 轮次，无时区按 UTC）、HasDanglingToolUse（尾窗 256KB 集合差 + 测试注窗口变体）、AITitle、FirstUserMessageHash（sha256）、HasAsyncLaunch（尾窗最后 Task/Agent 派发；run_in_background/background 或 "Async agent launched" 严格前缀）。**行读一律 bufio.Reader.ReadBytes('\n') 无上限**（Scanner ErrTooLong 断流，禁用）；子串预筛保留；一切 OSError/坏行/缺字段静默零抛。

参照：rev1 Task 7；spec §Implementation「I/O」。

**验收标准**：
- [ ] tests/test_transcripts.py 全部用例 1:1 移植且绿（含 t48 async 系列、坏行/空文件/缺字段防御用例）
- [ ] 尾窗读法：Stat→ReadAt 尾段→首行半行丢弃
- [ ] >10MB 单行不丢后续行（构造超长行测试用例）

**Blocked by**：02
