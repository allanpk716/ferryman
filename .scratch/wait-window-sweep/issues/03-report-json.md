# 票03 · 报告生成（markdown + --json）

## What to build

把扫参结果渲染成两种投影（端到端行为：同一结果结构体 → docs 惯例 markdown 实验报告 + `--json` 结构化输出，数字逐字段一致）。

markdown 报告必含的节（顺序即规格）：
1. 头部：装载时点戳、数据集双口径计数（全部窗/真实项目过滤后，含 close_reason 分布）、未还原/多值/unknown/不可算桶计数
2. **差距表**：当前闭式配置 vs 主网格最优（同 TTL 档内对比；推荐 = ttl_s 校准建议）
3. TTL 三档场景轴结果（档名：**config TTL（无实测，采集日期 N/A）/ −1/3 / −1/2**）与结论翻转点
4. 诊断网格（单列，标注"诊断用，非可部署"）
5. 无效保温单列（历史空表照登 + expired 占比 0/10%/30% 三档场景）
6. 双计检查节（心跳花费与摆渡成本各列各的，不互相抵扣）
7. 留出集两栏（前半选参/后半验证；后半栏参数集与前半一致）
8. 盲区节（固定文案：DefaultBeatOutTokens=300 假设与线性放大+第二遍对账、渡口限流不可见、GLM TTL 漂移、单机样本外推）
9. 证据等级标注原文："**单位成本输入（价格表、prefix_tokens）为事实；跳数、时点、熔断触发与总成本均为反事实推断**"

## 验收标准

- [ ] markdown 含上述 9 项且顺序正确（夹具结果断言关键行存在）
- [ ] 证据等级标注与 TTL 档名逐字符合规格
- [ ] --json 与 markdown 数字一致（同一结构体两投影，测试断言互相印证）
- [ ] 报告只含元数据（时间/token 数/金额/项目路径/计数）；结构上不存在消息内容输入
- [ ] 确定性：同输入两次渲染逐字节一致
- [ ] `go test ./internal/backtest/ -count=1` 全绿；`go vet` 净

## Blocked by

01

## 涉及路径

- internal/backtest/report.go（新建：markdown 渲染 + JSON 投影）
- internal/backtest/report_test.go（新建）

## 副作用声明

只跑 `go test ./internal/backtest/`；无端口无联网；测试写临时目录。

## decision_refs

D2（无效保温单列）、D7（docs 实验报告惯例）、D9（空表照登+三档场景）、D15（--json 结构化输出）

## review_blocks

无
