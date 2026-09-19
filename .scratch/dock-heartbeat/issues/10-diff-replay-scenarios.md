# 10 · 验证套件：diff.py＋replay.py＋scenarios 骨架＋README

## What to build

`experiments/router-fidelity/` 的 Python 件（对标 q14s3.py 的工程经验——UTF-8 落盘、GBK 控制台防御）：

1. **diff.py**：两份 tap 捕获目录（或两份单文件 JSON）逐字段 diff——headers（多值 join）逐键、body 顶层键集、model/messages 结构（逐 message role/blocks type 计数）、全 body 规范化 JSON 深比较；**白名单忽略**：ts、seq、session_id、user_id 类不可控字段（可配）；输出：差异清单＋`DIFF=0`/`DIFF=N` 退出码（供门禁脚本用）。
2. **replay.py**：差分重放器——取一份入站捕获（CC 原始请求），原样（仅按需重建占位 auth）分别发往两条被测路径（`-a <url> -b <url>`），各自落出站捕获；SSE 解析取 message_delta usage（复用 q14s3 的解析与头部跳过集合：`accept-encoding/content-length/host/connection/authorization/x-api-key`＋非 ASCII 值防御）。
3. **scenarios.py 骨架**：场景矩阵清单化（四档别名/子代理真名/[1M] 变体/多轮/工具往返/图片/41k+ 长前缀/ai-title 小请求）——每场景一条：id、驱使真 CC 的操作说明（人工步骤）、期望入站形态要点。**不自动跑真 CC**（真实流量是人工验证阶段）；骨架＋清单即交付。
4. **README.md**：L1/L2/L3 三层验证流程（tap 拍基线→golden diff→差分重放→L2 真打上游 cacheRead 探针＋套餐 4 格→L3 CC 全功能清单）、四条决策门、隐私纪律（捕获本地保留永不入库、真钥流经文件收紧权限验完即删）、ai-title session_id 归属实测步骤（F3 的 L1 项）。

## 验收标准

- [ ] diff.py 自测（pytest 或内置 `--selftest`）：构造两份夹具（一份全等→DIFF=0；一份改 model/加头/改 message→逐项报差异且 session_id 差异被白名单忽略）。
- [ ] replay.py 自测：httptest 双上游本地回环——同一请求喂两路、两份出站捕获落盘、usage 提取正确（mock SSE）。
- [ ] scenarios.py 骨架运行 `--list` 输出全矩阵清单；README 存在且含三层流程＋四门＋隐私纪律。
- [ ] 全部 Python 件 `python -X utf8` 可运行；无网络依赖的自测路径。

## Blocked by

无，可立即开始。

## 涉及路径

- experiments/router-fidelity/（diff.py、replay.py、scenarios.py、README.md、fixtures/ 自测夹具）

## 副作用声明

- 默认只跑：`python -X utf8 diff.py --selftest && python -X utf8 replay.py --selftest`。自测全走本地夹具/httptest，不外呼。

decision_refs: D7
review_blocks: 无
