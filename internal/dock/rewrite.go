// rewrite.go — 票05：改写五件之纯逻辑件（模型映射六键＋剥 [1M]＋text-only
// 图片降级）。
//
// 职责边界——为什么本文件没有「换真钥」：五件中的换真钥与出站头卫生是传输层
// 职责（票06）。真钥只进出站头（Authorization: Bearer <key>），**永不进入
// body 改写器**：本文件不接触、不感知任何认证信息，请求体内不存在也不应存
// 在真钥字段（T39：真钥只活本机 config.toml 永不入库）。纯逻辑层的第二件
// ＝无，仅留本注释钉住边界，防止后续把钥匙逻辑误塞进 body 改写。
//
// 纯函数约束：无 I/O、无包级可变状态，同入参同出参。只动顶层 model 与
// messages（且 messages 仅在图片降级命中 image 块时语义改变）两个键；其余
// 顶层键（system/tools/thinking/max_tokens/metadata/stream/output_config/
// context_management 等）原值保留（解析后逐键 DeepEqual 级别）。
//
// 保真实现口径：UseNumber 解析（数字按字面量保留，>2^53 整数不丢精度）＋
// SetEscapeHTML(false) 重编码（不对 <>& 做多余转义，CC 请求体常含代码片段）。
// 重编码会规范化空白与键序，票面接受「解析后等价」而非逐字节相同。
//
// 接线现状（票01 收敛）：RewriteConfig 的 model_map/default/text_only 三字段
// 由装配点从渡口上游条目（config.DockUpstream，经 ActiveUpstream 解析）注入；
// 改写恒被请求（隐含开启），守卫拒绝即整体退透传——本纯函数不感知准入，
// 被调用即执行全部生效改写，不存在「第六件」。
package dock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// RewriteConfig 改写配置三要素。字段由票06 从 [dock] 节接入；本文件只定义
// 形状，不读配置文件。
type RewriteConfig struct {
	// ModelMap 别名键→GLM 档名（claude-opus-5/claude-fable-5-1/
	// claude-sonnet-5/claude-haiku-4-5 等，可配）。值域（全部 value ＋
	// Default）即「已知真名」集合：剥 [1M] 后精确命中值域的名字原样透传
	// （子代理真名精确保留）。
	ModelMap map[string]string
	// Default 未知名兜底档名（配置里的 default 键）。
	Default string
	// TextOnly text-only 模型名单——按**映射后**的目标模型名匹配，命中则把
	// messages 里的 image 块降级为固定文本占位（GLM-5.3 在名单）。
	TextOnly []string
}

// Rewritten 改写结果。票06 的 dock 科目记账依赖 ModelIn/ModelOut 一对模型名
// （透传时同值；改写模式下二者可不同）。
type Rewritten struct {
	Body     []byte // 改写后请求体（重编码，解析后等价口径）
	ModelIn  string // 改写前模型名（入参 model 原值，含未剥的 [1M]）
	ModelOut string // 改写后模型名（[1M] 已剥、映射已换的最终值）
}

// imagePlaceholderText 图片降级固定占位文本（票面钉死字面量，不得改动）。
const imagePlaceholderText = "[image omitted: text-only model]"

var (
	errBodyNotObject = errors.New("dock/rewrite: 请求体不是 JSON object")
	errModelMissing  = errors.New("dock/rewrite: 请求体缺少 model 字符串字段")
)

// Rewrite 对 /v1/messages 请求体执行全部生效改写，返回改写后 body 与改写前
// 后模型名。body 非法（非 JSON／非 object／缺 model）返回错误，如何应答归
// 接线方（票06）处理；本函数只负责报错。
func Rewrite(body []byte, cfg RewriteConfig) (Rewritten, error) {
	var root any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber() // 数字保真：按字面量保留，大整数重编码不丢精度
	if err := dec.Decode(&root); err != nil {
		return Rewritten{}, fmt.Errorf("dock/rewrite: 请求体非法 JSON: %w", err)
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return Rewritten{}, errBodyNotObject
	}
	modelIn, ok := obj["model"].(string)
	if !ok {
		return Rewritten{}, errModelMissing
	}

	out := Rewritten{ModelIn: modelIn, ModelOut: mapModel(modelIn, cfg)}
	obj["model"] = out.ModelOut

	if containsStr(cfg.TextOnly, out.ModelOut) {
		degradeImages(obj["messages"])
	}

	// SetEscapeHTML(false)：默认会把 <>& 转义成 < 等，解析虽等价但徒增
	// 上游 diff 噪声；CC 请求体常含代码/HTML 片段，关掉换字节级更干净。
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		return Rewritten{}, fmt.Errorf("dock/rewrite: 重编码失败: %w", err)
	}
	out.Body = bytes.TrimRight(buf.Bytes(), "\n") // Encoder 尾部补的 \n 不入请求体
	return out, nil
}

// mapModel 模型映射核心，判定顺序即票面语义：
//  1. 剥 [1M] 后缀（[1m]/[1M] 大小写变体兼容，只剥后缀、中段不剥）；
//  2. 裸名精确命中值域（已知真名）→ 原样透传（[1M] 信息丢弃）；
//  3. 裸名命中别名键 → 换映射值；
//  4. 未知名 → default 兜底。
//
// 真名判定先于别名：若某名字同时是值域成员与别名键（配置病态），按真名
// 透传处理——子代理真名精确保留的优先级最高。
func mapModel(raw string, cfg RewriteConfig) string {
	const suffix = "[1m]"
	bare := raw
	if len(bare) >= len(suffix) && strings.EqualFold(bare[len(bare)-len(suffix):], suffix) {
		bare = bare[:len(bare)-len(suffix)]
	}
	if isKnownTrueName(bare, cfg) {
		return bare
	}
	if v, ok := cfg.ModelMap[bare]; ok && v != "" {
		return v
	}
	if cfg.Default == "" {
		// 防御：default 未配置（票06 校验前的退化输入）时宁可不映射，
		// 绝不凭空造出 model="" 打向上游。
		return bare
	}
	return cfg.Default
}

// isKnownTrueName 已知真名集合 = ModelMap 全部 value ∪ {Default}。
func isKnownTrueName(name string, cfg RewriteConfig) bool {
	for _, v := range cfg.ModelMap {
		if v == name {
			return true
		}
	}
	return name == cfg.Default
}

// degradeImages 就地遍历 messages 的 content blocks，把 type=="image" 的块
// 整块替换为固定文本占位（source 等字段随整块消失）。content 为字符串的
// 消息不动；无 image 块的消息语义不动。messages 缺失或形状异常不报错——
// 不在改写器里替上游做请求校验。
func degradeImages(messages any) {
	arr, ok := messages.([]any)
	if !ok {
		return
	}
	for _, mv := range arr {
		m, ok := mv.(map[string]any)
		if !ok {
			continue
		}
		blocks, ok := m["content"].([]any)
		if !ok {
			continue // content 为纯字符串（或异常形状）：不动
		}
		for i, b := range blocks {
			bm, ok := b.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := bm["type"].(string); t == "image" {
				blocks[i] = map[string]any{"type": "text", "text": imagePlaceholderText}
			}
		}
	}
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
