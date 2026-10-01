// codex_tool_media.go — 票03：工具输出内嵌媒体的剥离与钳制（对照表 §1.2
// 「嵌了图片的非原生形态」行）。
//
// 参考实现 tool_media.rs 的 ImagesOnly 子集（Anthropic 桥只用该档）：识别
// input_image / image_url / Anthropic image / 无 type 的裸 data URL 四形，
// 递归剥离（含 JSON 字符串内嵌形态，最深 32 层），整串 data URL ≥8KB 才认
// （小图标留在文本里）；命中后残余的 base64ish 大标量（≥16KB）以
// "[cc-switch: omitted N bytes]" 标记钳制。标记字面量逐字节沿用参考实现
// （codex 会话历史里已存在这些标记，换字面量＝存量对账断裂）。
package dock

import (
	"strings"
)

const (
	// wholeDataURLMinBytes 整串 data URL 的最小字节阈（小于此留在文本里）。
	wholeDataURLMinBytes = 8 * 1024
	// base64ishMinBytes 残余 base64ish 标量的最小字节阈。
	base64ishMinBytes = 16 * 1024
	// mediaTraversalMaxDepth 递归剥离最大深度（防御环/超深嵌套）。
	mediaTraversalMaxDepth = 32
	// toolResultMediaAttachedMarker 媒体被剥出为原生块的替换标记（逐字节沿用）。
	toolResultMediaAttachedMarker = "[cc-switch: tool result media attached as native media]"
)

// stripAndClampMediaFromToolValue 递归剥离媒体块并钳制残余大标量，返回
// （可能改写后的值, 命中数, 剥出的媒体块列表）。字符串容器（slice 元素 /
// map 值）由本函数各臂原地回写；顶层字符串调用方以返回值替换。
//   - 结构化媒体块 → 整块替换为 replacementBlock；
//   - 整串 data URL 字符串 → 替换为 replacementText（原是纯字符串的保持
//     纯字符串，不加 JSON 引号层）；
//   - JSON 字符串内嵌命中 → 解析树内剥离+钳制后规范化回写成字符串。
func stripAndClampMediaFromToolValue(v any, replacementBlock map[string]any, replacementText string, depth int) (any, int, []map[string]any) {
	if depth > mediaTraversalMaxDepth {
		return v, 0, nil
	}
	var media []map[string]any
	switch cur := v.(type) {
	case string:
		if mp, ok := wholeStringImageDataURL(cur); ok {
			return replacementText, 1, append(media, mp)
		}
		t := strings.TrimSpace(cur)
		if t == "" {
			return v, 0, nil
		}
		parsed, ok := decodeJSONValue([]byte(t))
		if !ok {
			return v, 0, nil
		}
		np, n, m := stripAndClampMediaFromToolValue(parsed, replacementBlock, replacementText, depth+1)
		if n == 0 {
			return v, 0, nil
		}
		clampBase64ishStrings(np)
		return canonicalJSONString(np), n, append(media, m...)
	case []any:
		total := 0
		for i, item := range cur {
			nv, n, m := stripAndClampMediaFromToolValue(item, replacementBlock, replacementText, depth+1)
			if n > 0 {
				cur[i] = nv
				total += n
				media = append(media, m...)
			}
		}
		return cur, total, media
	case map[string]any:
		if mp, ok := chatMediaPart(cur); ok {
			return replacementBlock, 1, append(media, mp)
		}
		if content, present := cur["content"]; present {
			nc, n, m := stripAndClampMediaFromToolValue(content, replacementBlock, replacementText, depth+1)
			if n > 0 {
				cur["content"] = nc
				return cur, n, append(media, m...)
			}
		}
		return cur, 0, nil
	}
	return v, 0, nil
}

// clampBase64ishStrings 命中媒体后的残余钳制（就地）：data: URL（≥8KB）或
// 纯 base64 字符集长标量（≥16KB）→ "[cc-switch: omitted N bytes]"。
func clampBase64ishStrings(v any) {
	switch cur := v.(type) {
	case []any:
		for i, item := range cur {
			if s, ok := item.(string); ok {
				cur[i] = clampOneString(s)
			} else {
				clampBase64ishStrings(item)
			}
		}
	case map[string]any:
		for k, val := range cur {
			if s, ok := val.(string); ok {
				cur[k] = clampOneString(s)
			} else {
				clampBase64ishStrings(val)
			}
		}
	}
}

// clampOneString 单个字符串的钳制判定与替换。
func clampOneString(s string) string {
	t := strings.TrimSpace(s)
	if (len(t) >= wholeDataURLMinBytes && strings.HasPrefix(strings.ToLower(t), "data:")) ||
		looksLikeBase64Payload(t) {
		return "[cc-switch: omitted " + strconvItoa(len(s)) + " bytes]"
	}
	return s
}

// chatMediaPart 识别一个可剥离的媒体块并归一为 {type:image_url,image_url:{url}}
// （ImagesOnly：只认图片）。ok=false＝不是媒体块（或非图片类）。
func chatMediaPart(part map[string]any) (map[string]any, bool) {
	t, hasType := part["type"].(string)
	switch {
	case hasType && (t == "input_image" || t == "image_url"):
		iu, ok := normalizedImageURL(part)
		if !ok {
			return nil, false
		}
		return imageURLContentPart(iu), true
	case hasType && t == "image":
		if !typedImageHasPayload(part) {
			return nil, false
		}
		iu, ok := typedImageURL(part)
		if !ok {
			return nil, false
		}
		return imageURLContentPart(iu), true
	case !hasType:
		iu, ok := looseDataURL(part)
		if !ok {
			return nil, false
		}
		return imageURLContentPart(iu), true
	}
	return nil, false
}

// normalizedImageURL input_image/image_url 的 image_url 字段归一（字符串或
// {url} 对象皆可；顶层 detail 合并，"original" 降 "auto"——严格网关拒收）。
func normalizedImageURL(part map[string]any) (map[string]any, bool) {
	raw, present := part["image_url"]
	if !present {
		return nil, false
	}
	var obj map[string]any
	switch iu := raw.(type) {
	case string:
		if strings.TrimSpace(iu) == "" {
			return nil, false
		}
		obj = map[string]any{"url": iu}
	case map[string]any:
		u, ok := iu["url"].(string)
		if !ok || strings.TrimSpace(u) == "" {
			return nil, false
		}
		obj = make(map[string]any, len(iu))
		for k, v := range iu {
			obj[k] = v
		}
	default:
		return nil, false
	}
	mergeTopLevelDetail(part, obj)
	return obj, true
}

// looseDataURL 无 type 字段、image_url 是 data: URL 的裸形态。
func looseDataURL(part map[string]any) (map[string]any, bool) {
	if _, hasType := part["type"]; hasType {
		return nil, false
	}
	obj, ok := normalizedImageURL(part)
	if !ok {
		return nil, false
	}
	u, _ := obj["url"].(string)
	if !strings.HasPrefix(strings.ToLower(u), "data:") {
		return nil, false
	}
	return obj, true
}

// typedImageHasPayload Anthropic image 块（source 形或 data+mimeType 形）有
// 实际载荷判定。
func typedImageHasPayload(part map[string]any) bool {
	if src, ok := asMap(part["source"]); ok && sourceMediaTypeIsImage(src) {
		if u, ok := src["url"].(string); ok && strings.TrimSpace(u) != "" {
			return true
		}
		if d, ok := src["data"].(string); ok && d != "" {
			return true
		}
	}
	d, hasData := part["data"].(string)
	if !hasData || d == "" {
		return false
	}
	mt := mapStr(part, "mimeType")
	if mt == "" {
		mt = mapStr(part, "mime_type")
	}
	return isImageMIME(mt)
}

// typedImageURL Anthropic image 块 → data:/http: URL 归一。
func typedImageURL(part map[string]any) (map[string]any, bool) {
	if src, ok := asMap(part["source"]); ok {
		if !sourceMediaTypeIsImage(src) {
			return nil, false
		}
		if u, ok := src["url"].(string); ok && strings.TrimSpace(u) != "" {
			obj := map[string]any{"url": u}
			mergeTopLevelDetail(part, obj)
			return obj, true
		}
		if d, ok := src["data"].(string); ok && d != "" {
			mediaType := firstNonEmpty(mapStr(src, "media_type"), mapStr(src, "mime_type"),
				mapStr(src, "mimeType"), "image/png")
			var u string
			if len(d) >= 11 && strings.EqualFold(d[:11], "data:image/") {
				u = d
			} else {
				u = "data:" + mediaType + ";base64," + d
			}
			obj := map[string]any{"url": u}
			mergeTopLevelDetail(part, obj)
			return obj, true
		}
		return nil, false
	}
	d, ok := part["data"].(string)
	if !ok || d == "" {
		return nil, false
	}
	mt := firstNonEmpty(mapStr(part, "mimeType"), mapStr(part, "mime_type"))
	if !isImageMIME(mt) {
		return nil, false
	}
	obj := map[string]any{"url": "data:" + mt + ";base64," + d}
	mergeTopLevelDetail(part, obj)
	return obj, true
}

// imageURLContentPart 归一 URL → {type:image_url,image_url:{...}}。
func imageURLContentPart(iu map[string]any) map[string]any {
	return map[string]any{"type": "image_url", "image_url": iu}
}

// mergeTopLevelDetail 顶层 detail 并入（缺失才并；"original" 降 "auto"）。
func mergeTopLevelDetail(part, iu map[string]any) {
	if _, has := iu["detail"]; !has {
		if d, present := part["detail"]; present {
			iu["detail"] = d
		}
	}
	if s, ok := iu["detail"].(string); ok && s == "original" {
		iu["detail"] = "auto"
	}
}

func sourceMediaTypeIsImage(src map[string]any) bool {
	return isImageMIME(firstNonEmpty(mapStr(src, "media_type"), mapStr(src, "mime_type"),
		mapStr(src, "mimeType")))
}

func isImageMIME(v string) bool {
	return len(v) >= 6 && strings.EqualFold(v[:6], "image/")
}

// isImageBase64DataURL "data:image/…;base64," 形判定（大小写不敏感）。
func isImageBase64DataURL(v string) bool {
	i := strings.IndexByte(v, ',')
	if i < 0 {
		return false
	}
	h := strings.ToLower(v[:i])
	return strings.HasPrefix(h, "data:image/") && strings.HasSuffix(h, ";base64")
}

// looksLikeBase64Payload 纯 base64 字符集的长标量判定（残余大载荷钳制用）。
func looksLikeBase64Payload(v string) bool {
	if len(v) < base64ishMinBytes {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '+', c == '/', c == '=':
		default:
			return false
		}
	}
	return true
}

// wholeStringImageDataURL 整串即完整图片 data URL（≥8KB）→ 归一 image_url part。
func wholeStringImageDataURL(s string) (map[string]any, bool) {
	t := strings.TrimSpace(s)
	if len(t) < wholeDataURLMinBytes || !isImageBase64DataURL(t) {
		return nil, false
	}
	return map[string]any{
		"type":      "image_url",
		"image_url": map[string]any{"url": t},
	}, true
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func strconvItoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
