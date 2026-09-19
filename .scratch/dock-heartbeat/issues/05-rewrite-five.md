# 05 · 改写五件：纯逻辑改写器

## What to build

`internal/dock/rewrite.go`（纯函数包内模块，无 I/O）——`rewrite_enabled=true` 时对透传请求的全部生效改写，出现第六件要么缺陷要么过决策（红线）：

1. **模型映射六键**：入参 model 字符串→出参。规则顺序：
   - 先剥 `[1M]` 后缀（大小写变体兼容：`[1m]`/`[1M]`）得裸名；
   - 裸名精确命中映射表**已知真名**（GLM 模型名，如配置映射表的值域）→**原样透传**（子代理真名精确保留）；
   - 裸名命中六个别名键（claude-opus-5/claude-fable-5-1/claude-sonnet-5/claude-haiku-4-5——映射表键，可配）→换映射值；
   - 未知名→default 兜底键的映射值。
   映射表来自 `[dock].model_map`（六键：四个别名＋default＋text_only 判定用的真名列表另配）。剥下的 [1M] 信息丢弃（1M 由映射后的模型自身承载）。
2. **真钥替换**（改写器只做标记，实际头操作在票 06 的头卫生；本票交付 body 侧无真钥——**真钥永不进 body 改写器**，只进出站头）。本件在纯逻辑层的体现＝无（说明注释写清楚职责边界）。
3. **图片降级**：目标模型（映射后）在 `[dock].text_only` 列表→遍历 messages 的 content blocks，`type=="image"` 的块替换为 `{"type":"text","text":"[image omitted: text-only model]"}`（占位文本固定字面量）；无 image 块不动。
4. **顶层形状**：只动 `model`、`messages`（图片降级时）两个键；其余顶层键（system/tools/thinking/max_tokens/metadata/stream 等）逐字节不动（测试钉死）。

改写器 API 形状自定（如 `Rewrite(body []byte, cfg RewriteConfig) ([]byte, ModelNames, error)`，ModelNames 含改写前后模型名供 dock 科目记账）——票 06 依赖此 API，实现后把签名写进票 06 派单包。

## 验收标准

- [ ] 六键映射逐键测试：四别名→映射值；子代理真名（映射值域内的名字，带/不带 [1M]）→原样透传；未知名→default；`[1m]` 小写变体同样剥。
- [ ] 图片降级：text_only 模型＋含 image 块→替换为固定占位；非 text_only 模型＋image 块→不动；无 image→不动。
- [ ] 保真钉死：除 model/图片块外，其余键与字节完全不变（含中文/emoji 内容）。
- [ ] 非法 body（非 JSON）→ 返回错误（透传层按不改写放行，票 06 接线时定义——本票只测错误返回）。
- [ ] `go test ./internal/dock/...` 全绿。

## Blocked by

01（internal/dock 包基座与配置类型）。

## 涉及路径

- internal/dock/rewrite.go、internal/dock/rewrite_test.go（新文件）

## 副作用声明

- 默认只跑类型检查/单文件测试：`go test ./internal/dock/ -run Rewrite`。

decision_refs: D4
review_blocks: 无
