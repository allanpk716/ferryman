# 票 11 · store 交接库移植

**What to build**：`internal/store`（store.py 1:1）：Entry **显式 snake_case json tag**（handoff_id/session_id/…/covers_until_s/blocked_at *string/injected []string——直接序列化不得输出 CamelCase）；SaveHandoff（id=YYYYmmdd_HHMMSS_hex6、同 session+agent 覆盖、原子写 tmp+rename）；ValidHandoff（fresh|skeleton、covers+60s≥lastWrite、24h 新鲜、取 covers 最大）；SavePendingPrompt（>500 token 截断+「…(超长截断)」）/PopPendingPrompt（消费标记）；RestoreCandidates（covers 降序）；MarkBlocked/MarkInjected/ReadHandoff。

参照：rev1 Task 13。

**验收标准**：
- [ ] tests/test_store.py 全部用例 1:1 移植且绿
- [ ] **键名断言**：落盘 index.json 直接反序列化为 map 断言 snake_case 键存在、blocked_at=null 语义
- [ ] 时间格式 `20060102_150405` / `2006-01-02 15:04:05`（本地时区）

**Blocked by**：07
