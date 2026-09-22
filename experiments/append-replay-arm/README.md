# 追加重放实跳臂工具(append-replay-arm)

同模型摆渡的**启用硬门槛**验证工具(ADR-0015 决定一;票04)。白名单预置 ≠ 启用:
任一上游要真正启用同模型档,必须先过一发"追加重放实跳臂"的四条成功标准,
结论回写启用门状态文件。**工具就绪即验收;真实实跳由人手动执行**(要花真实
上游的钱)。

## 四条成功标准(评审口径,硬约束)

| # | 标准 | 数据源 |
|---|------|--------|
| ① | 追加后仍命中:该跳缓存读占比 ≥ 同前缀心跳基线 | 两跳响应 usage;占比 = cache_read/(cache_read+input),与 beat.Classify 单源 |
| ② | 与捕获快照字节差异仅末尾追加段 + max_tokens 数值子区间 | 独立 span 校验器,不信任构造方自证(快照 32000→封顶 4096 属允许的数值子区间,非缓存键、无碍命中) |
| ③ | stop_reason 非 tool_use | 追加跳响应 |
| ④ | 输出可解析为交接 MD 结构 | 票03 ParseSameModelOutput 严格解析,单源复用 |

判定三态:任一 **fail → failed**(回写,保持未启用);无 fail 但有悬而未决
(发送失败/dry-run)→ **inconclusive**(不回写,状态保持待实跳);全过 →
**passed**(回写,启用)。

## 两跳

1. **基线跳**:同前缀心跳式原样重放(max_tokens→1),先走——标准①的基线占比;
2. **追加跳**:前缀字节一个不动 + 末尾一条摆渡指令 user 消息 + max_tokens 放开
   至封顶(默认 4096),后走。

两跳都发到**渡口入站口**(`-dock-url`,默认 `http://127.0.0.1:15722`),
与真实流量同路径同改写,复用票03 `HttpBeatSender`。发送零重试(F1)。

## 快照文件(输入)

```json
{
  "session_id": "…",                    // 必填
  "body_file": "raw_body.json",         // 请求体原字节文件(相对本文件);字节保真首选
  "body": "…",                          // 或内联原字节文本;与 body_file 二选一
  "headers": {"content-type": "application/json", "anthropic-version": "…"},
  "note": "从哪个会话/哪次抓包来"
}
```

**字节保真是命根**:追加跳要命中的是真实会话留在上游的缓存,快照体必须与
CC 实际发出的请求体逐字节一致(含键序/空白/转义形态)。抓包用
`experiments/capture/forwarder.go` 时认 `body_raw` 形态;凡重新序列化过的
body(pretty-print 过的 `body`)会静默 miss,别用。

## 五步用法

```bash
cd experiments/append-replay-arm
S=~/ferryman/captures/arm_snap.json     # 快照文件
W=~/ferryman/armrun-zhipu               # 证据目录(含会话内容,绝不入 git)

go run main.go construct -snapshot $S -workdir $W -upstream zhipu   # 1. 构造+②预检
go run main.go send      -snapshot $S -workdir $W                   # 2. 两跳真实发送
#   先演练一遍不花钱: send --dry-run(只跳过发送,其余全走)
go run main.go evaluate  -snapshot $S -workdir $W -upstream zhipu   # 3. 四标准评估
go run main.go writeback -workdir $W -note "2026-09-22 实跳"        # 4. 结论回写(人拍板后)
go run main.go status                                               # 5. 看各上游状态
```

`run` 子命令串 1→3(writeback 默认不自动,要 `--writeback` 显式给)。
writeback 只收 passed/failed;inconclusive 拒写——状态保持待实跳,排查后复跑。

## 证据留盘(每步一文件,人可直接打开)

| 文件 | 内容 |
|------|------|
| `append_body.json` | 实际发送的追加重放体原字节 |
| `construct_report.json` | 构造摘要 + 标准②预检结论 |
| `send_result.json` | 两跳事实:usage 四列 + stop_reason + **输出全文**原样留档 |
| `evaluate_result.json` | 四标准逐条判定 + 总判定 |

缓存写列(`cache_write_tokens`)留档为 `null`:beat 发送器的 usage 解析不含
该列,如实标注不伪造;占比口径三列自足,不受影响。

## 启用门状态文件

默认 `~/ferryman/arm_verdict.jsonl`(与账本同风格:一行一条 JSON、append-only、
人可读)。读取 = 每上游最后一行(last-wins):同结论重复回写只是追加,复跑翻案
(未过→通过、通过→未过)同样靠追加末行,永不改历史行。无记录 = 待实跳 =
未启用(启用硬门槛的缺省形态)。`ferry.ArmVerdictResolverFor(state)` 与票01
`config.ArmVerdictResolver` 缝同形,daemon/doctor 接线即装配此函数。

## 隐私与安全纪律

- 证据目录含会话内容与模型输出全文,**绝不入 git**;
- 快照文件同样含会话内容,放 `~/ferryman/` 下,不入 git;
- 工具只发到渡口入站口,不直接触外网;真实上游调用经渡口出站,花钱的是
  send 这一步(基线跳极便宜:max_tokens=1;追加跳≈一次交接生成的输出价);
- 发送零重试:传输错/429/5xx 一律按事实留档,不自动补发。
