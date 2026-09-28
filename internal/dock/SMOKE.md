# 渡口错误契约 · SMOKE 13 行验收清单（发布前人工过）

> 对应 spec：`docs/superpowers/specs/20260928-dock-error-contract-spec.md` §Testing Decisions 末条（上游透传 8 行＋渡口自产 5 行，每行三查：**怎么触发｜渡口回什么｜CC 预期做什么**）。
> 阈值/状态码/字段名以实现为准（非 spec 字面）：`internal/dock/errorshape.go`（错误体形状、Retry-After=5、流内事件文案与前导空行）、`gate.go`（60s 闸门 `firstByteGateTimeout`、drainBody 注入）、`shutdown.go`（expireDrain→注入宽限→Close）、`server.go`（truncated 记账条件、`/v1/messages` 入站口）、`internal/config/config.go`（`drain_timeout_s` 缺省 180）、`internal/daemon/serve.go`（排水接线、关停来源日志）。
> 格式纪律对齐 `widget/ui/tests/SMOKE.md`：环境前置、触发步骤、预期观测点，命令可复制执行。

## 行号 ↔ spec 对应表

| spec Testing Decisions 项 | 本清单行号 |
|---|---|
| 桩回 429/401/403/500/502/503/529（状态码＋体＋retry-after 三者一致） | 行 1–7 |
| 流中段断（连接断＋账本 truncated） | 行 8 |
| 拨号失败 502＋Anthropic 错误体＋Retry-After | 行 9 |
| 首包闸门 60s → 504＋错误体 | 行 10 |
| 排水到期·流已建立（`event: error` 后断＋truncated） | 行 11 |
| 排水期在途流跑完（窗内自然结束） | 行 12 |
| 换装窗口闭环（停-拉间隙拒连→CC 退避→新守护成功）——行定义出自票面 What-to-build；spec 仅以「13 行清单」括号涵盖 | 行 13 |

## 0. 前置环境

### 0.1 安全红线（先读）

- **全程不外呼真供应商**：测试实例的上游只允许本机桩 `127.0.0.1:16100`，或故意指空的死端口 `127.0.0.1:16199`（行 9 用）。跑每行前确认测试 config 的 `[dock].active` 指向 `stub` 或 `dead`，**不得**指向真供应商条目。
- **隔离实例，不动生产**：测试守护 17311 / 测试渡口 15732 / 桩 16100 / 死端口 16199。生产（守护 7311、渡口 15722、面板 15900）与用户 `~/ferryman/config.toml` 全程不碰。唯一例外是行 13（生产形态演练，只重启守护、不改任何配置）。
- 测试 config 与数据目录放仓库外（示例用 `%USERPROFILE%\ferryman-smoke\`），勿提交进仓库。

### 0.2 构建（Git Bash，仓根）

```bash
cd /c/WorkSpace/agent/Ferryman
go build -o ferryman-smoke.exe ./cmd/ferryman
mkdir -p /c/Users/allan716/ferryman-smoke
```

### 0.3 测试 config（桩上游配置法）

另存为 `C:/Users/allan716/ferryman-smoke/config.toml`（内容如下）。**不采用"改生产 [dock].active"的方式**——独立实例不动生产配置，跑完整个删除目录即净。

```toml
# ferryman-smoke 隔离测试配置（仓库外保存）
[server]
port = 17311
data_dir = 'C:/Users/allan716/ferryman-smoke'   # 账本/令牌独立目录，与生产 ~/ferryman 无关

[watch]
poll_interval_s = 30                            # smoke 不需要快轮询

[dock]
listen = "127.0.0.1:15732"                      # 测试渡口口（生产 15722，隔离）
active = "stub"                                 # 行 9 改 "dead"
drain_timeout_s = 10                            # 排水上限：行 11 用 5、行 12 用 30，按行改后重启实例（缺省 180）

[dock.upstreams.stub]
base_url = "http://127.0.0.1:16100"             # 假上游桩
api_key = "stub-key"
model_map = { default = "stub-model" }          # 16100 非守卫透传域（15721/15722/15723 之外）→ 校验要求 default；守卫放行＝改写模式，与生产同形

[dock.upstreams.dead]
base_url = "http://127.0.0.1:16199"             # 空端口：行 9 拨号失败用
api_key = "stub-key"
model_map = { default = "stub-model" }
```

### 0.4 假上游桩（一个桩管所有行）

开一个 **PowerShell 窗口**，整块粘贴（桩按请求 query `?stub=<模式>` 分流；模式表见下）：

```powershell
# 假上游桩：Ctrl+C 停。一次只服务一个连接（冒烟够用，逐行跑）。
$l = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 16100)
$l.Start()
'stub up on 127.0.0.1:16100 （Ctrl+C 停）'
$enc = [System.Text.Encoding]::UTF8
function Wr($s, $t) { $b = $enc.GetBytes($t); $s.Write($b, 0, $b.Length) }
function SseHdr($s) { Wr $s "HTTP/1.1 200 OK`r`nContent-Type: text/event-stream`r`nConnection: close`r`n`r`n" }
while ($true) {
  $c = $l.AcceptTcpClient()
  try {
    $s = $c.GetStream()
    $b = New-Object byte[] 16384
    $n = $s.Read($b, 0, $b.Length)
    $req = $enc.GetString($b, 0, $n)
    $mode = if ($req -match 'stub=([a-z0-9]+)') { $Matches[1] } else { '429' }
    switch ($mode) {
      'silent'  { SseHdr $s; Start-Sleep -Seconds 120 }   # 200 头后一言不发
      'hangmid' {                                          # 两个事件后挂住不发不断
        SseHdr $s
        Wr $s "event: message_start`ndata: {`"type`":`"message_start`"}`n`n"
        Wr $s "event: content_block_delta`ndata: {`"type`":`"content_block_delta`"}`n`n"
        Start-Sleep -Seconds 300
      }
      'slowok'  {                                          # 事件→10s 停顿→补完 message_stop
        SseHdr $s
        Wr $s "event: message_start`ndata: {`"type`":`"message_start`"}`n`n"
        Start-Sleep -Seconds 10
        Wr $s "event: message_delta`ndata: {`"type`":`"message_delta`"}`n`n"
        Wr $s "event: message_stop`ndata: {`"type`":`"message_stop`"}`n`n"
      }
      'cut'     {                                          # 两个事件后直接断，无 message_stop
        SseHdr $s
        Wr $s "event: message_start`ndata: {`"type`":`"message_start`"}`n`n"
        Wr $s "event: content_block_delta`ndata: {`"type`":`"content_block_delta`"}`n`n"
      }
      default   {                                          # 数字模式＝回该状态码
        $code = 429; if ($mode -match '^\d{3}$') { $code = [int]$mode }
        $body = '{"type":"error","error":{"type":"stub_error","message":"stub ' + $code + '"}}'
        Wr $s ("HTTP/1.1 $code Stub`r`nContent-Type: application/json`r`nRetry-After: 17`r`nContent-Length: $($body.Length)`r`nConnection: close`r`n`r`n$body")
      }
    }
  } catch { }
  finally { $c.Close() }
}
```

| `?stub=` | 桩行为 | 服务行 |
|---|---|---|
| `429` / `401` / `403` / `500` / `502` / `503` / `529` | 回该状态码＋`Retry-After: 17`＋`stub_error` JSON 体 | 行 1–7 |
| `cut` | 200＋两个 SSE 事件后立即断（无 message_stop） | 行 8 |
| `silent` | 200 头后不发任何体字节，挂 120s | 行 10 |
| `hangmid` | 200＋两个 SSE 事件后挂住（不发也不断） | 行 11 |
| `slowok` | 200＋事件→10s 停顿→补完 message_stop | 行 12 |

### 0.5 启动隔离实例

再开一个 **Git Bash** 窗口（curl 也从这里打）：

```bash
cd /c/WorkSpace/agent/Ferryman
FERRYMAN_CONFIG='C:/Users/allan716/ferryman-smoke/config.toml' \
FERRYMAN_PANEL_PORT=17399 \
FERRYMAN_DATA='C:/Users/allan716/ferryman-smoke' \
./ferryman-smoke.exe serve --no-tray --no-browser --smoke
```

（`FERRYMAN_CONFIG` 是守护的配置；面板数据根不读它，走 `FERRYMAN_DATA`——两个都指测试目录，实例才完全隔离。）

启动成功的横幅（三行都在才算就绪）：

```
面板: http://127.0.0.1:17399 （数据目录 C:\Users\allan716\ferryman-smoke）
[ferryman] 渡口: http://127.0.0.1:15732 → http://127.0.0.1:16100（active=stub；模式由守卫裁决；快照内存态，重启即失）
[ferryman] serve: 127.0.0.1:17311 · …
```

行间切换（改 active / 改 drain_timeout_s）：实例控制台 Ctrl+C 停掉 → 改 config.toml → 重跑上面的启动命令。桩不用重启。

### 0.6 观测点速查

- **curl 输出**（`-si` 看状态码/头/错误体；`-siN` 看流内字节实时到达）。
- **账本**（Git Bash；`kind":"dock"` 行；`truncated":true` 仅截断行携带，完整流无此字段；curl 请求无 session，`session_id` 为空属正常）：

  ```bash
  tail -n 5 "/c/Users/allan716/ferryman-smoke/accounts/$(date +%Y%m).jsonl"
  ```

- **渡口日志**＝实例控制台 `[dock]` 前缀行（如 `截断流: session=… status=200（EOF 前未见 message_stop）`）。
- **daemon stdout**＝实例控制台无前缀行（`关停来源: …`、`⚠ 渡口排水到期强收: …`）。
- **CC 侧**：**只有行 13 必须人在 CC 旁观测**。行 1–12 的「CC 预期」是按契约（ADR-0017）的推演，渡口侧用 curl 即可闭环；如要亲证 CC 行为，把一个临时 CC 会话的 `ANTHROPIC_BASE_URL=http://127.0.0.1:15732` 指到测试渡口复跑对应触发（上游仍是桩，零外呼），用完改回。

### 0.7 已知非缺陷（残余边界，观测到别当 bug 报）

- 非流式（或定长体）请求排水到期时无合法错误投递通道：收尾＝裸断连＋尽力记账（spec 声明式残余）。行 11 只验流式。
- 客户端先断（非排水期）时渡口静默不写 502（对端已不在）。
- 首包闸门超时不打专门日志（只回 504＋账本行），行 10 靠 curl 与账本观测。

## 1. 上游透传组（行 1–8）

先设一次请求体（Git Bash）：

```bash
B='{"model":"claude-sonnet-5","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"ping"}]}'
```

验收口径（行 1–7 共用）：**状态码、错误体、`Retry-After` 头三者与桩所发逐字一致**（桩故意用 `Retry-After: 17` 和 `stub_error` 细类——与渡口自产的 `Retry-After: 5`/`api_error` 区分，证明是透传不是渡口造的）。

### 行 1 · 上游 429

- [ ] **触发**：`curl -si -X POST 'http://127.0.0.1:15732/v1/messages?stub=429' -H 'content-type: application/json' -d "$B"`
- [ ] **渡口回**：`HTTP/1.1 429`＋`Content-Type: application/json`＋`Retry-After: 17`＋体 `{"type":"error","error":{"type":"stub_error","message":"stub 429"}}`，逐字与桩一致；账本新增 `kind":"dock"` 行、`status":429`
- [ ] **CC 预期**：限流类错误走官方重试——按 `Retry-After` 退避后自动重试，上游恢复即成功

### 行 2 · 上游 401

- [ ] **触发**：`curl -si -X POST 'http://127.0.0.1:15732/v1/messages?stub=401' -H 'content-type: application/json' -d "$B"`
- [ ] **渡口回**：同上口径，`HTTP/1.1 401`＋`Retry-After: 17`＋`stub 401` 体逐字到达；账本行 `status":401`
- [ ] **CC 预期**：认证错误原样呈现给用户（上游原文体），靠改钥修复而非重试（不确定 CC 是否也退避重试数次，以现场为准）

### 行 3 · 上游 403

- [ ] **触发**：`curl -si -X POST 'http://127.0.0.1:15732/v1/messages?stub=403' -H 'content-type: application/json' -d "$B"`
- [ ] **渡口回**：同口径，`HTTP/1.1 403` 三者逐字；账本行 `status":403`
- [ ] **CC 预期**：权限错误原样呈现（同行 2 语义）

### 行 4 · 上游 500

- [ ] **触发**：`curl -si -X POST 'http://127.0.0.1:15732/v1/messages?stub=500' -H 'content-type: application/json' -d "$B"`
- [ ] **渡口回**：同口径，`HTTP/1.1 500` 三者逐字；账本行 `status":500`
- [ ] **CC 预期**：服务器错误走官方退避自动重试（`api_error` 类重试路径，错误体为上游原文）

### 行 5 · 上游 502

- [ ] **触发**：`curl -si -X POST 'http://127.0.0.1:15732/v1/messages?stub=502' -H 'content-type: application/json' -d "$B"`
- [ ] **渡口回**：同口径，`HTTP/1.1 502` 三者逐字；账本行 `status":502`（注意与行 9 渡口自产 502 区分：本行体是 `stub_error`、`Retry-After: 17`）
- [ ] **CC 预期**：官方退避自动重试

### 行 6 · 上游 503

- [ ] **触发**：`curl -si -X POST 'http://127.0.0.1:15732/v1/messages?stub=503' -H 'content-type: application/json' -d "$B"`
- [ ] **渡口回**：同口径，`HTTP/1.1 503` 三者逐字；账本行 `status":503`
- [ ] **CC 预期**：官方退避自动重试（过载类，`Retry-After` 照 honor）

### 行 7 · 上游 529

- [ ] **触发**：`curl -si -X POST 'http://127.0.0.1:15732/v1/messages?stub=529' -H 'content-type: application/json' -d "$B"`
- [ ] **渡口回**：同口径，非标准状态码 `HTTP/1.1 529` 原样到达（不归一、不吞）；账本行 `status":529`
- [ ] **CC 预期**：过载错误照上游语义处理（重试路径同行 6）

### 行 8 · 流中段断（上游干净 EOF 截断）

- [ ] **触发**：`curl -siN -X POST 'http://127.0.0.1:15732/v1/messages?stub=cut' -H 'content-type: application/json' -d "$B"`
- [ ] **渡口回**：`200`＋`text/event-stream`，`message_start`、`content_block_delta` 两事件实时到达后**连接即结束**——无 `message_stop`、无错误事件（契约：上游流中段断不抢救）；账本该行带 `"truncated":true`，实例控制台出现 `[dock] 截断流: session= mode=rewrite status=200（EOF 前未见 message_stop）`
- [ ] **CC 预期**：响应中途连接结束，呈现中断类错误（2026-09-28 事故的 "Connection lost mid-response" 即此形），由用户重试回合；排查时账本 `truncated` 提供证据

## 2. 渡口自产组（行 9–13）

本组错误体恒为 Anthropic 形状 `{"type":"error","error":{"type":"api_error","message":…}}`，HTTP 路径带 `Retry-After: 5`（`retryAfterShort`）。

### 行 9 · 拨号失败（上游连不通）

- [ ] **前置**：config 改 `[dock].active = "dead"`，重启实例（桩可停可不停）
- [ ] **触发**：`curl -si -X POST 'http://127.0.0.1:15732/v1/messages' -H 'content-type: application/json' -d "$B"`（回环拒连，立即返回，不等 30s 拨号超时）
- [ ] **渡口回**：`HTTP/1.1 502`＋`Content-Type: application/json`＋`Retry-After: 5`＋体 `{"type":"error","error":{"type":"api_error","message":"渡口连不上上游（dial tcp 127.0.0.1:16199: connectex: …拒连文案…）；请稍后重试"}}`（`err.Error()` 原文嵌入 message，细节以现场为准）；账本行 `status":502`；实例控制台无 `[dock]` 报错行为正常
- [ ] **CC 预期**：`api_error`＋`Retry-After: 5` → 自动退避重试；上游持续不通则按 API 错误呈现，不挂死、不裸断

### 行 10 · 首包静默 60s（上游 200 后挂死）

- [ ] **前置**：`[dock].active = "stub"`，重启实例
- [ ] **触发**：`time curl -si -X POST 'http://127.0.0.1:15732/v1/messages?stub=silent' -H 'content-type: application/json' -d "$B"`——桩发完 200 头后不发任何体字节
- [ ] **渡口回**：**约 60 秒**后（阈值＝`firstByteGateTimeout` 常量 60s）收到 `HTTP/1.1 504`＋`Retry-After: 5`＋体 `{"type":"error","error":{"type":"api_error","message":"上游已受理但迟迟未吐出任何流数据（首包静默超时）；请重试"}}`；账本行 `status":504`、无 `truncated`；无专门日志行（见 0.7）。反证口径：只闸 `stream:true` 的流式请求（非流式与上游非 200 不闸，自动测试钉住）
- [ ] **CC 预期**：60 秒内拿到可重试错误而非无限挂起，自动重试

### 行 11 · 排水到期·流已建立（SSE error 事件后断＋truncated）

- [ ] **前置**：config 改 `drain_timeout_s = 5`，重启实例；桩保持运行
- [ ] **触发**：① `curl -siN -X POST 'http://127.0.0.1:15732/v1/messages?stub=hangmid' -H 'content-type: application/json' -d "$B"`——收到 200 头和两个事件后挂住；② 在**实例控制台按 Ctrl+C**（备选：`curl -si -X POST -H "Authorization: Bearer $(cat /c/Users/allan716/ferryman-smoke/daemon.token)" http://127.0.0.1:17311/shutdown`）
- [ ] **渡口回**：Ctrl+C 后约 5 秒（排水上限），curl 收到——

  ```
  （空行）
  （空行）
  event: error
  data: {"type":"error","error":{"type":"api_error","message":"渡口正在关停（排水到期收尾），流在此时被截断；请重试以继续"}}

  ```

  随后连接关闭。事件前的**两个连续空行属预期**：第一个是桩自身事件的结束空行（SSE 事件以空行收尾），第二个是排水注入的前导空行——它终结可能挂起的半行，防止错误事件被 SSE 解析器吞进上一个事件。实例控制台依次出现：`[ferryman] 关停来源: Ctrl+C中断（…）`（走端点则为 `关停来源: shutdown端点`）→ `[ferryman] 停止中…` → `[ferryman] ⚠ 渡口排水到期强收: context deadline exceeded` → 进程退出；`[dock] 截断流: … status=200（EOF 前未见 message_stop）`；账本行 `status":200`＋`"truncated":true`
- [ ] **CC 预期**：流内 `event: error` 是 CC 认识的 Anthropic 错误事件——按可重试 API 错误退避重试，**不是无解释断连**

### 行 12 · 排水期在途流存活（窗内跑完）

- [ ] **前置**：config 改 `drain_timeout_s = 30`，重启实例（spec 行的"180s 窗"＝生产缺省值；smoke 用短数字提速，与 spec 自动测试"注入短超时"同精神）
- [ ] **触发**：① `curl -siN -X POST 'http://127.0.0.1:15732/v1/messages?stub=slowok' -H 'content-type: application/json' -d "$B"`——收到 `message_start` 后流在 10s 停顿中；② 立即在实例控制台 Ctrl+C
- [ ] **渡口回**：流**不中断**——10s 停顿后 `message_delta`、`message_stop` 照常到达，连接干净收尾；实例控制台有 `关停来源` 行但**无** `排水到期强收` 告警，进程在流跑完后退出（退出码 0）；账本行 `status":200`、**无** `truncated` 字段、无 `截断流` 日志
- [ ] **CC 预期**：本回合零感知，完整收到回复——"守护重启不掐在途回复"主诉的直接验证

### 行 13 · 换装窗口闭环（停-拉间隙拒连→CC 退避→新守护成功）【唯一必须人在 CC 侧】

- [ ] **前置**：生产形态（CC 的 base_url 指向生产渡口 `http://127.0.0.1:15722`、生产 `[dock].active` 指真供应商）。建议先 Ctrl+C 收掉测试实例，避免观察混淆。本行只重启守护、**不改任何配置**
- [ ] **触发**：① CC 正常会话发一个长回复请求；② 回复进行中按既有流程重启守护（托盘「退出」→ 常规拉起脚本；或生产控制台 Ctrl+C 后重新点火）。停-拉间隙几秒内 CC 的下一个 API 请求会 TCP 拒连（渡口监听已下）
- [ ] **渡口/守护侧观测**：生产控制台出现 `[ferryman] 关停来源: …` 行（本次重启是谁发起的）与新 serve 横幅；间隙期间若有在途流，走行 11/12 同款排水（生产 `drain_timeout_s=180`）
- [ ] **CC 预期**：连接被拒是可重试网络错误，CC 官方重试机制退避后重发；新守护起来后回合最终完成。判据＝回合不因重启永久失败、界面不出现"无解释断连"卡死
