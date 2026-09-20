# 票 02 · /gate_check 双模式（单会话判定＋汇总）

## What to build

替换票 01 留下的 /gate_check stub，实现闸门判定预告查询（用户视角：agent 问"此刻提交拦不拦我、还剩几分钟"）：

1. **单会话模式**（带 session_id）：此刻判定（allow/block/warn——按台账闲置时长、总结阈值、拦截阈值与既有闸门判定语义**只读推演**，绝不写任何状态或触发真闸门）、离拦截阈值剩余分钟、判定依据（有效交接路径或缺失原因）。
2. **汇总模式**（无 session_id）：逐会话一行（session_id、判定、剩余分钟），limit 同构默认 50；**排序键＝预计拦截时刻升序（等价闲置时长降序）；并列按 session_id 字典序稳定排序；无有效交接者排在有交接者之前**（spec F7 裁定）。
3. 判定推演只依据台账与交接库索引（CONTEXT.md：闸门判定"只依据台账"），不解析 jsonl 正文。

红线同票 01：只读、无消息内容、无凭据；/stats 契约不动。

## 验收标准

- [ ] 单会话：未凉/已凉有交接/已凉无交接三态判定正确（构造三种台账夹具）；剩余分钟计算与拦截阈值一致
- [ ] 汇总：排序键三项规则（临近度升序、并列 session_id 字典序、无交接者在前）各有夹具断言；limit 默认 50 生效
- [ ] session_id 不存在 → 404 JSON；无鉴权 → 401
- [ ] 全程零状态写入（调用前后台账与闸门状态字节不变）
- [ ] 响应无消息内容、无凭据字段（反向断言）
- [ ] go test ./internal/daemon/ -count=1 全绿；gofmt 干净

## Blocked by
票 01

## 涉及路径
- internal/daemon/query_gate_check.go（新建，替换 stub）
- internal/daemon/query_gate_check_test.go（新建）

## 副作用声明
- 独占验证命令：go test ./internal/daemon/ -count=1

## decision_refs: D2、D5、D6、D13
## review_blocks: 无
