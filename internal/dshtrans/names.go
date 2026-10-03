// names.go — 会话目录名规范化规则（P2-1 第二块砖）。
//
// 逐条对 dsh 源码钉死（packages/session/session-persistence-jsonl/src/format.ts，
// master@639ed015；rc 期格式 v3→v4 迁移频繁，规则以源码为准、夹具按真实
// 磁盘目录回钉）：
//
//   - EncodeSegment（转义 id → 目录名）：按 UTF-16 码元逐个处理；安全字符
//     [A-Za-z0-9._-] 且非 `~` 原样，其余（含 `~` 自身）→ `~XXXX`（4 位
//     大写十六进制、零填）；`.` → `~002E`、`..` → `~002E~002E`（防穿越）。
//     对全部 JS 字符串单射（含孤立代理对）——Go 侧按 rune 折 UTF-16 码元
//     同域处理（非 BMP 字符拆高低代理对两个 ~XXXX）。
//   - ProjectKey（cwd → 项目目录名）：分隔符 `/` `\` `:` → `-` 且**连续
//     分隔符合并成一个 `-`**（separatorRun）；不安全码元同 ~XXXX 转义；
//     去首部 `-`；空则 `root`；外包 `--`…`--`；slug 截 251 个码元（输出恒
//     ASCII，码元数=字节数）。cwd 缺失的会话落 `_no-cwd` 目录（projectDir
//     分支，非 ProjectKey 产物）。
package dshtrans

import (
	"fmt"
	"strings"
	"unicode/utf16"
)

// safeSegmentChar EncodeSegment 的安全字符集（format.ts 正则 /^[A-Za-z0-9._-]$
// 逐字；`~` 不在其中——自身也要转义）。
func safeSegmentChar(ch rune) bool {
	switch {
	case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9':
		return true
	case ch == '.' || ch == '_' || ch == '-':
		return true
	}
	return false
}

// EncodeSegment 任意串 → 单个安全路径段（dsh encodeSegment 1:1）。空串报错
// （上游同款 throw）。对 UTF-16 码元单射；Go string 的非法 UTF-8 字节按
// U+FFFD 折算（JS 侧不可达形态，防御收口）。
func EncodeSegment(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("cannot encode an empty path segment")
	}
	if raw == "." {
		return "~002E", nil
	}
	if raw == ".." {
		return "~002E~002E", nil
	}
	var b strings.Builder
	appendUnit := func(unit uint16) {
		ch := rune(unit)
		if ch != '~' && safeSegmentChar(ch) {
			b.WriteRune(ch)
		} else {
			fmt.Fprintf(&b, "~%04X", unit)
		}
	}
	for _, r := range raw { // 非法 UTF-8 字节已被 range 折为 U+FFFD（与 JS 无对应形态，防御收口）
		if r <= 0xFFFF {
			appendUnit(uint16(r))
		} else {
			hi, lo := utf16.EncodeRune(r) // 非 BMP：高低代理对各占一码元
			appendUnit(uint16(hi))
			appendUnit(uint16(lo))
		}
	}
	return b.String(), nil
}

// DecodeSegment EncodeSegment 的逆（目录名 → 原 id）。非 `~XXXX` 形态的
// 转义（长度≠4、非十六进制）→ false（非本编码产物，拒绝复原）。
// 孤立代理对原样还原（单射域内合法）。
func DecodeSegment(seg string) (string, bool) {
	var units []uint16
	for i := 0; i < len(seg); {
		if seg[i] != '~' {
			if seg[i] >= 0x80 {
				return "", false // 非 ASCII 字面量：非本编码产物（EncodeSegment 只产 ASCII）
			}
			units = append(units, uint16(seg[i]))
			i++
			continue
		}
		if i+5 > len(seg) {
			return "", false
		}
		var unit uint32
		for _, h := range seg[i+1 : i+5] {
			unit <<= 4
			switch {
			case h >= '0' && h <= '9':
				unit |= uint32(h - '0')
			case h >= 'a' && h <= 'f':
				unit |= uint32(h-'a') + 10
			case h >= 'A' && h <= 'F':
				unit |= uint32(h-'A') + 10
			default:
				return "", false
			}
		}
		if unit > 0xFFFF {
			return "", false // 4 位十六进制不可达，防御
		}
		units = append(units, uint16(unit))
		i += 5
	}
	return string(utf16.Decode(units)), true
}

// ProjectKey cwd → 项目目录名（dsh projectKey 1:1：分隔符合并、~XXXX 转义、
// 去首部 `-`、空落 `root`、`--`+slug(≤251 码元)+`--`）。空串报错（上游同款）。
// 注意：分隔符合并与截断是有损压缩（人读导航用）；cwd 的精确值以会话头
// 行为准，本函数只用于布局推定与夹具对账。
func ProjectKey(cwd string) (string, error) {
	if cwd == "" {
		return "", fmt.Errorf("cannot encode an empty project path")
	}
	var b strings.Builder
	separatorRun := false
	flush := func() {
		b.WriteByte('-')
		separatorRun = true
	}
	appendUnit := func(unit uint16) {
		ch := rune(unit)
		if ch != '~' && safeSegmentChar(ch) {
			b.WriteRune(ch)
		} else {
			fmt.Fprintf(&b, "~%04X", unit)
		}
		separatorRun = false
	}
	for _, r := range cwd { // 非法 UTF-8 字节已被 range 折为 U+FFFD（防御收口）
		if r == '/' || r == '\\' || r == ':' {
			if !separatorRun {
				flush()
			}
			continue
		}
		if r <= 0xFFFF {
			appendUnit(uint16(r))
		} else {
			hi, lo := utf16.EncodeRune(r)
			appendUnit(uint16(hi))
			appendUnit(uint16(lo))
		}
	}
	slug := strings.TrimLeft(b.String(), "-") // replace(/^-+/, '')
	if slug == "" {
		slug = "root"
	}
	// slice(0, 251)：输出恒 ASCII（安全字符/转义均为 ASCII），码元数=字节数。
	if len(slug) > 251 {
		slug = slug[:251]
	}
	return "--" + slug + "--", nil
}
