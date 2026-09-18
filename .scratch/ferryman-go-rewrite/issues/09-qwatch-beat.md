# 票 09 · qwatch 检测器 + beat 纯逻辑

**What to build**：`internal/qwatch`（qwatch.py 1:1）：Breakdown/Verdict/Detect（尾窗 256KB + 测试注窗口；末条 assistant 按 message id 聚合；块边界=换行；悬空集合差+名字收集；空集真空真）/CorrelateMissSignals（票 06 漏检关联纯计数）。正则 **RE2+Unicode 字符类**：\s=显式 Unicode 空白集、\d=\p{Nd}、code fence `(?s)```.*?(```|\z)`；疑问词 16 词逐字。`internal/beat`（beat.py 1:1）：BeatPlan/BeatResult/Sender 接口/NoopSender/Classify/Breaker（MISS 2/ERROR 3，连击语义逐字）/QWatchStats。HttpBeatSender 不实现（接口位，Q14 未授权）。

参照：rev1 Task 10/11；spec §Implementation「文本」。

**验收标准**：
- [ ] tests/test_qwatch.py 全部用例 1:1 移植且绿
- [ ] **全角样本用例**：`**Q １**`（全角数字+可选全角空格）、全角数字编号行——期望值先以 Python `_classify` 实测钉死
- [ ] beat 纯逻辑用例（散在 test_qwatch_scheduler.py/events.py 的纯逻辑部分）移植且绿
- [ ] 包级 var 预编译正则

**Blocked by**：06
