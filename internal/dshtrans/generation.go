// generation.go — 不可变代文件名解析与代选择。
//
// dsh 源码（session-format/src/filename.ts + persistence-jsonl/src/format.ts）：
// 正则 /^session(?:\.v([1-9][0-9]*))?\.jsonl$/u——v0 无名（session.jsonl）、
// v1 起 session.vN.jsonl；压缩后缀 .zstd 追加在 .jsonl 之后。非规范名
// （大写 V、前导零、.v0、临时名 session.v4.jsonl.<hex>.tmp）不识别为已提交
// 代。读取方取数值最高代（resolveGenerationInDirectory 同规则）——天然向前
// 兼容：未来 v5 出现即被选中，旧代原地保留作迁移源。
package dshtrans

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Generation 会话目录内选中的一个代文件。
type Generation struct {
	Path    string
	Version int
	Zstd    bool
}

// ParseGenerationFilename 文件名 → (版本, 是否 zstd, 是否规范代名)。
// 两种物理编码都认（一个 root 不混编码，但读取侧宽容——按代数值选）。
func ParseGenerationFilename(name string) (version int, zstd bool, canonical bool) {
	zstd = false
	base := name
	if strings.HasSuffix(base, ".zstd") {
		base = strings.TrimSuffix(base, ".zstd")
		zstd = true
	}
	// /^session(\.v([1-9][0-9]*))?\.jsonl$/
	if base == "session.jsonl" {
		return 0, zstd, true
	}
	const p = "session.v"
	if !strings.HasPrefix(base, p) || !strings.HasSuffix(base, ".jsonl") {
		return 0, zstd, false
	}
	digits := base[len(p) : len(base)-len(".jsonl")]
	if digits == "" || digits[0] == '0' {
		return 0, zstd, false // 前导零/.v0 不规范
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, zstd, false
		}
	}
	v, err := strconv.Atoi(digits)
	if err != nil || v < 1 {
		return 0, zstd, false
	}
	return v, zstd, true
}

// LatestGeneration 会话目录内取数值最高的规范代文件；无规范代（空目录/
// 仅临时文件）→ false。同版本双编码并存（上游 root 不混编码，实际不可达）
// 取先遇者——代数值才是选择判据。
func LatestGeneration(dir string) (Generation, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Generation{}, false
	}
	var best Generation
	found := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		v, zstd, canonical := ParseGenerationFilename(e.Name())
		if !canonical {
			continue
		}
		if !found || v > best.Version {
			best = Generation{Path: filepath.Join(dir, e.Name()), Version: v, Zstd: zstd}
			found = true
		}
	}
	return best, found
}

// ReadHeaderLine 读一个代文件的首条完整行（头行）。zstd＝只解首帧（物质化
// 首帧恰一行，头行恒小——按 dsh readFirstZstdLine 同款 8KB 起步倍增有界
// 读取，避免大文件整读）；明文＝读到首个 \n。行含行尾 \n；文件不足一条
// 完整行 → false。
func ReadHeaderLine(path string, isZstd bool) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	if !isZstd {
		// 明文：bufio 读首行即可。
		line, err := bufio.NewReaderSize(f, 64*1024).ReadBytes('\n')
		if err != nil || len(line) == 0 {
			return "", false
		}
		return string(line), true
	}
	// zstd：分块读直到首帧结构完整，解之取首行。
	chunk := make([]byte, 8192)
	have := make([]byte, 0, 8192)
	for {
		n, rerr := f.Read(chunk)
		if n > 0 {
			have = append(have, chunk[:n]...)
		}
		if len(have) > 0 {
			frames, torn, serr := ScanZstdFrames(have)
			if serr == nil && len(frames) > 0 {
				plain, derr := decodeFrame(have[frames[0].Start:frames[0].End])
				if derr != nil {
					return "", false
				}
				nl := strings.IndexByte(string(plain), '\n')
				if nl < 0 {
					return "", false // 首帧无完整行（dsh 断言「首帧恰一行」的坏形）
				}
				return string(plain[:nl+1]), true
			}
			if serr == nil && torn >= 0 && len(have) >= 1<<20 {
				return "", false // 首帧超 1MB 仍未结构完整——非常形，放弃
			}
		}
		if rerr != nil {
			return "", false
		}
	}
}
