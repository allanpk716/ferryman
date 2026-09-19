# router-fidelity 验证套件（tap · diff · replay · scenarios）

把「自建路由与 cc-switch 等价」从推断变成测量的工具集。方案原文：
`docs/research/20260919_CC中转替换_研究与验证方案.md`（L1/L2/L3 与决策门的定义出处）。
实验代码不进 daemon 二进制；**L1/L2/L3 真流量验证是人工步骤**——本套件只提供工具与操作单，夜间不烧积分。

## 隐私纪律（先读这个，再动手）

**tap 捕获不脱敏——真钥会原样流经落盘文件。** 这是本套件的设计前提，不是事故：

- 捕获目录**不进 git**（tap 默认落 `~/ferryman/router-fidelity`，仓库外）；
- 权限收紧：目录 0o700 / 文件 0o600（Windows 为 best-effort）；
- **验完即删**：结论归档后立刻删除整个捕获目录，不留过夜。

replay.py 是唯一例外路径：它发出的请求只带占位令牌（`Authorization: Bearer PROXY_MANAGED` /
`X-Api-Key: PROXY_MANAGED`），真钥只活在被测路径内部——这正是差分重放的观测前提。

## 组成与观测拓扑

```
CC ──▶ 被测路径 X（cc-switch 或自建 router）──▶ tap(127.0.0.1:15723) ──▶ 上游（GLM anthropic 兼容口）
```

| 文件 | 作用 |
|---|---|
| `tap.go` | 上游捕获器：请求侧全量（headers+body，不脱敏）；响应侧摘要（状态码＋usage 四列，正文零落盘） |
| `diff.py` | 两份捕获逐字段差分（白名单忽略不可控字段），L1 golden diff 工具 |
| `replay.py` | 差分重放器：同一入站捕获喂两条路径（-a/-b），各臂落 tap 形状出站捕获 |
| `scenarios.py` | 场景矩阵清单（9 类 13 条）；只列不跑，人工验证的操作单 |

捕获文件形状（tap.go 同款，diff/replay 按此读写）：

- `*_reqNNN.json`：`seq` / `ts` / `method` / `path` / `query` / `headers`（多值 join 成单值）/ `body`（解析后 JSON，解析失败存 `body_raw`）
- `*_respNNN.json`：`seq` / `ts` / `status` / `content_type` / `content_encoding` / `usage_source`（`message_delta` 为权威）/ `usage` 四列

## 三层验证流程

### L1 形状等价（拍基线 → golden diff → 差分重放）

1. **tap 拍基线**：把被测路径 X 的上游指到 tap（X＝cc-switch：DB 里把 GLM 供应商 base_url 临时改 `http://127.0.0.1:15723`；X＝自建 router：upstream 配 15723），起 tap；
2. **驱使真 CC 产全矩阵流量**：`scenarios.py --list` 领操作单，逐场景在 scratch 目录跑 claude，收捕获；
3. **golden diff**：固化 cc-switch 出站捕获为 golden，之后自建 router 每前进一步 `diff.py` 对比一次；
4. **差分重放**：同一份入站捕获喂两条路径（`replay.py`），两臂出站捕获直接 `diff.py <out>/a <out>/b`。

判据：全场景矩阵 diff＝0；非 0 的差异逐条归属（"有意为之"写明理由）。
白名单默认忽略 `ts` / `seq` / `session_id` / `user_id`（含 `X-Claude-Code-Session-Id` 头与
`body.metadata.user_id`，键名不分大小写，头名按包含匹配）；跨路径出站对比时 Authorization
必然不同，按需 `--ignore authorization,x-api-key` 追加。

### L2 行为等价（真打上游对照）

- **cacheRead 当最灵敏探针**：同前缀请求两条路径的 `cache_read_input_tokens` 必须一致——上游对请求内容（含改写后缓存键）的"看法"直接反映在命中上（Q14 方法复用，主场景＝s07 长前缀）；
- **套餐 4 格实验**：有/无 `claude-code-20250219` beta 标记 × 两条路径，共 4 格——结论决定头卫生的实现严格度（丢标记被拒/错价＝硬要求；无差别＝无害冗余）；
- 同时比对：HTTP 状态、SSE usage 四列、stop_reason、错误语义（429/4xx 文案）。

### L3 功能等价（CC 端到端全功能清单）

base_url 切自建 router，走一遍：主对话各档模型 / 子代理派发回收 / 后台小请求（ai-title 用 haiku 档）/
读图（media 降级）/ 1M 长上下文 / 工具调用往返 / 网络错误重试（`API_TIMEOUT_MS=3000000`＝50min，
router 代理超时必须 ≥ 此值且 SSE 不断流）。

## 四条决策门（全过才进迁移）

1. 出站 golden diff 全场景＝0（或差异全部解释并记录归属）；
2. 套餐对照 4 格结论明确（标记是硬要求／无害，二选一有实证）；
3. CC 全功能清单通过、账本 usage 正常；
4. CC 版本漂移有监控手段：入站捕获 diff 告警（新 beta / 新顶层参数出现即知，避免静默 miss）。

四条满足 → 迁移三步（透传版 → 改写版 → 退役）。激活顺序不可跳步：**基线 → 透传 → L1-L3 → 改写**。

## ai-title 归属实测（评审 F3 的 L1 验证项）

即 `scenarios.py --list` 中 `s08-ai-title-attribution`：

1. 正常会话跑起来，等 CC 自动生成会话标题（或主动换话题触发一次）；
2. 在 tap 捕获目录筛小请求：`max_tokens` 明显小（几十至数百）、model 为 haiku 档、messages 仅 1 条；
3. 对比该请求 `X-Claude-Code-Session-Id` 头与 `body.metadata.user_id` 内 `session_id` 是否与主会话相同；
4. 把结论记入验证报告：**相同**（largest-wins 主快照天然排除小请求——小请求体远小于主对话）或**不同**（快照按会话分桶天然隔离）。

主快照策略不依赖此结论，但必须实测归档。

## 工具一行示例

```bash
# 拍基线（tap 架在被测路径与上游之间）
go run tap.go -listen 127.0.0.1:15723 -upstream http://127.0.0.1:15721 -out ~/ferryman/router-fidelity

# golden diff（目录对目录按 seq 配对；文件对文件直接比）
python -X utf8 diff.py golden/ outbound/
python -X utf8 diff.py golden/req_base.json outbound/req001.json --ignore authorization,x-api-key

# 差分重放（两臂出站捕获落 <out>/{a,b}，可直接再 diff）
python -X utf8 replay.py inbound_req001.json -a http://127.0.0.1:15721 -b http://127.0.0.1:9000 --out ~/ferryman/router-fidelity-replay

# 场景矩阵（只列不跑）
python -X utf8 scenarios.py --list

# 全套自测（零网络外呼；replay 自测走本地回环）
python -X utf8 diff.py --selftest
python -X utf8 replay.py --selftest
python -X utf8 scenarios.py --selftest
```

## 纪律

- `scenarios.py` 只列矩阵，**不自动跑真 CC**；真实流量验证是人工阶段。
- replay 只重建占位令牌，真钥只活在被测路径内部；tap 捕获含真钥——目录不进 git、验完即删。
- 全部 Python 件只用标准库，`python -X utf8` 可跑；自测零网络外呼（本地回环除外）。
