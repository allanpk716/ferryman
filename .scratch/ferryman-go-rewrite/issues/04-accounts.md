# 票 04 · accounts 账本移植

**What to build**：`internal/accounts`（accounts.py 1:1）：append-only 月度滚动 jsonl（YYYYMM.jsonl，本地时区）；Record 的 10 科目字段白名单 + 必填校验 + 保留字拒绝（中文报错原文）；ts=Round 3、ts_iso 本地时区 `2006-01-02T15:04:05-0700`；Read 的六维过滤；坏行跳过+stderr 告警（stdout 保持机器可解析）。

参照：rev1 Task 5。

**验收标准**：
- [ ] tests/test_accounts.py 全部 19 例 1:1 移植且绿（白名单拒绝/缺必填/保留字/月度滚动/过滤读/字段集合）
- [ ] 白名单 10 科目与字段集合逐字（handoff/beat/block/inject/bypass/window/qwatch_hit/qwatch_open/qwatch_close/usage）
- [ ] 落盘行键序：v,kind,ts,ts_iso,agent,session_id,lineage_id,project + kind 字段字母序（确定性序列化）
- [ ] Record 并发安全（mutex）

**Blocked by**：02
