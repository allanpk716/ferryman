# 票 06 · `ferryman provider` 命令族(CLI 操作面)

## What to build
cmd/ferryman 下新增 provider 命令族(D3:CLI 先行),接线票 02 的热切换端点与票 05 的写入器:

1. **`provider list`**:全部条目+active 标注+dialect/codex 可用性(原生/需翻译/不支持)+模型位概要+密钥脱敏(沿用既有只露尾 4 位纪律)。
2. **`provider switch <名>`**:条目不存在→拒绝并列可用;codex="unsupported"→**默认拒绝并报因**;`--cc-only` 显式放行并明示"codex 暂断供,仅 CC 走该供应商";合法→调守护管理口热切换(不重启),回显新 active+codex 车道模式(翻译/透传)。守护不在线→如实报错给拉起指引,不静默失败。
3. **`provider add <名>` / `provider remove <名>`**:编辑本机 config 供应商表(密钥经参数或环境变量传入,输出永不回显全钥);remove 拒绝删 active 条目。
4. **`provider import-ccswitch`**:读 ~/.cc-switch/cc-switch.db(sqlite,modernc.org/sqlite 已在依赖):providers 表 claude/codex 两类逐条映射入供应商表——claude 类→dialect=anthropic;codex 类按端点线协议推 dialect;密钥只落本机 config(T39);重名冲突=跳过并报告,不覆盖。测试用 testdata 假库(假密钥),**不读真 ~/.cc-switch**。
5. **`provider apply`**:跑票 05 写入器(前置校验+三份备份+外科写入+回显逐份结果);`provider apply --restore`:按接管前备份还原并回显。
6. 单测:各子命令含拒绝路径(refuse unsupported/缺 key/重名/守护不在线)、脱敏输出、import 映射与冲突跳过、apply 参数透传。

## 验收标准
- [ ] list 输出含可用性/模型位/脱敏密钥
- [ ] switch 拒绝路径(不存在/不支持)与 --cc-only 放行告知齐,有断言
- [ ] switch 走热切换端点不重启;守护不在线如实报错
- [ ] add/remove 含 active 保护;全钥零回显
- [ ] import 用假库测试,映射/dialect 推断/冲突跳过有断言
- [ ] apply/--restore 参数与回显齐
- [ ] go test ./cmd/... ./internal/provider/... 全绿;go vet 净

## Blocked by
票 02(热切换端点), 票 05(写入器)

## 涉及路径
- cmd/ferryman/(provider.go 新文件 + provider_test.go)
- internal/provider/(CLI 辅助函数与扩展测试;不改动 writer 核心行为)
- internal/config/(仅当 CLI 需要表读写辅助时最小增)

## 副作用声明
- 独占验证命令:go test ./cmd/... ./internal/provider/...
- import 测试只用 testdata 假库;不读/写真 ~/.cc-switch;不碰生产进程

decision_refs: D3, D9
review_blocks: F2
