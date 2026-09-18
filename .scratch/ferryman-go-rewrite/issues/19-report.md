# 票 19 · report 报表移植

**What to build**：`internal/report`（report.py 1:1）：HandoffCost（price_ver 折算、无价→nil）、SavingsV1（行键逐字、gross=Σ block S×(P_in−P_cache)/per 按行时刻取版本、net=Round 4、按 net 降序）、StrategyTable（四策略+best、跳过原因文案逐字）、RenderText（全部中文模板逐字）、Run（--since/--until/--project/--session/--kind/--provider/--json；本地时区日期解析、until 含当日全天）。策略公式**只准调 internal/policy**（第二份公式副本就此收口）。

参照：rev1 Task 23。

**验收标准**：
- [ ] tests/test_report.py → report_test.go 全部用例 1:1 移植且绿（--json 结构、无 p_cache 跳过、unpriced、复算口径数字）
- [ ] StrategyCaveats 两段逐字
- [ ] 报表数字与 Python 版同输入逐位一致（Round=FormatFloat）

**Blocked by**：03, 04, 05
