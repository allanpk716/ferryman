# record-codex-traffic —— 真流量重录工具（票04 · 白天用）

把本机 codex→渡口 的真实流量录成原始捕获，再按脱敏门禁转成候选夹具，人工复核后并入
`internal/dock/fixtures/`（四形夹具的同源补充形，spec F3/W4）。

**红线（先读）**

1. 原始捕获含真实密钥：只落本机 git 忽略目录（默认 = 仓库根 `/.scratch/recorded/`，已入 .gitignore），**转完即删**；
   绝不 commit、绝不发网络。`--keep-raw` 仅排查用，用完必须手删。
2. 本工具不进 CI、不自动跑、夜链不执行——只有白天 W4 runbook 里人守着用。
3. 工具只自动清**鉴权类**敏感物（鉴权头整条剥、真形密钥串换 `sk-test-redacted`）；
   payload 里的 session_id、邮箱、文件路径、代码片段等**仍须人工复核**。

## 录制步骤

前置：渡口在跑（默认 `127.0.0.1:15722`），本机有 python 3.10+。

```cmd
:: 1. 起捕获口（本机回环，两个端口都不出网）
python tools\record-codex-traffic\record_codex_traffic.py record --listen 127.0.0.1:15799 --upstream http://127.0.0.1:15722

:: 2. 另开终端：把 codex 的 base_url 临时指向捕获口
::    （改 ~/.codex/config.toml 的 base_url = http://127.0.0.1:15799/v1，wire_api 保持 "responses"；
::    orca 那份 CODEX_HOME 配置同理。录制完记得还原，或跑 ferryman provider apply 重写。）

:: 3. 正常使用 codex 跑几轮对话/工具调用（想录哪形就造哪形：普通问答 / 让它调工具 /
::    长输出 / 故意触发一次错误如停上游）。

:: 4. 回到捕获口终端 Ctrl-C。捕获在 ..\..\..\.scratch\recorded\capture-<时间戳>\exchange-*.json
```

捕获文件格式：一次 HTTP 交换一个 JSON（方法/路径/原始头/请求体 b64/状态/响应分块 b64，
SSE 分块边界原样保留）。

## 脱敏门禁（sanitize）

```cmd
python tools\record-codex-traffic\record_codex_traffic.py sanitize --capture-dir ..\..\..\.scratch\recorded\capture-<时间戳> --out candidates
```

sanitize 依次做：

1. **剥鉴权头**：`authorization` / `x-api-key` / `x-goog-api-key` / `cookie` / `set-cookie`
   整条剥除（候选夹具不携带任何头）；
2. **密钥串扫描替换**：请求体、响应全文过真形密钥规则（sk-/GitHub/AWS/Google/Slack token、
   Bearer 长令牌——与 `internal/dock/fixtures` 的零真实密钥扫描互为镜像），命中串换成
   `sk-test-redacted`；已是 `sk-test-*`/显式占位的串放行不动；
3. **转候选夹具**：按 `internal/dock/fixtures` 的 schema 生成候选（codex_request +
   anthropic_upstream / responses_upstream 回放数据，SSE 帧按事件名归边），每个候选标注
   `redaction_hits` 命中数；
4. **删原始捕获**：默认 `rmtree` 掉 capture-* 目录（转完即删），终端回显删除路径。

## 人工复核与并入

1. 打开 `candidates/candidate-*.json`，肉眼过一遍：session_id、邮箱、机器路径、代码内容等
   payload 敏感物，该删的删、该改的改成合成文本；
2. 挑形状完整的候选改名（如 `tool_call_namespace.json`），放入 `internal/dock/fixtures/`；
3. 跑 `go test ./internal/dock/fixtures/`——加载器的零真实密钥扫描会对新文件兜底复扫，
   任何真形密钥串会拒绝加载（这是门禁最后一道闸，不是替代人工复核）。

## 排障

- 命中数是 0 但确认流量里有钥：看捕获文件 `req_headers` 是否确有头（codex 可能只用 env 占位，
  真钥由渡口注入——那正说明录到的入站就是占位，无需担心）。
- `--keep-raw` 保留原始捕获排查差异后，**务必手删** capture-* 目录。
