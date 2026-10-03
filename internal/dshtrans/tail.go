// tail.go — 会话文件的增量尾读（守望轮询的消费面）。
//
// 语义（harvest.readNew 的 dsh 形）：传入已消费字节偏移，读出此后新增的
// **完整**内容并返回新偏移——偏移只推进到已消费边界，残尾留待下轮：
//   - zstd：新增字节里逐帧结构扫描，只解完整帧（写入方一批一帧、帧尾即
//     行尾；残帧/坏帧之后的字节不消费）；结构性损坏按「无新增+错误」收口
//     （不推进偏移——下一轮重试，dsh 上游可能已修复截断）；
//   - 明文（compression:'none'）：读到文件尾，截到最后一个 \n（残行留待
//     下轮）。
//
// 文件收缩（写方 rollbackAppend/truncateTornTail 修复残尾）→ 偏移清零从头
// 重采（CC harvest 同款；seq 去重防重账）。
package dshtrans

import (
	"bytes"
	"errors"
	"io"
	"os"
)

// TailResult 一轮尾读的产出。
type TailResult struct {
	Text      string // 新增完整行的明文（每行含行尾 \n；可能为空）
	NewOffset int64  // 消费后的新偏移（== 调用方应记住的偏移）
	Err       error  // 结构性损坏（zstd 坏帧/魔数）；Text/NewOffset 仍有效（坏点前）
}

// TailText 增量尾读一个代文件。打不开文件 → Err（os 错误原样）；无新增
// （size==offset）→ 零值结果不报错。
func TailText(path string, isZstd bool, offset int64) TailResult {
	info, err := os.Stat(path)
	if err != nil {
		return TailResult{Err: err}
	}
	size := info.Size()
	if size < offset {
		offset = 0 // 收缩：从头重采
	}
	if size == offset {
		return TailResult{NewOffset: offset}
	}
	f, err := os.Open(path)
	if err != nil {
		return TailResult{Err: err}
	}
	defer f.Close()
	raw := make([]byte, size-offset)
	n, rerr := f.ReadAt(raw, offset)
	if rerr != nil && !errors.Is(rerr, io.EOF) {
		return TailResult{Err: rerr}
	}
	raw = raw[:n]
	if len(raw) == 0 {
		return TailResult{NewOffset: offset}
	}
	if !isZstd {
		end := len(raw)
		if raw[len(raw)-1] != '\n' { // 残行扣留
			nl := bytes.LastIndexByte(raw, '\n')
			if nl < 0 {
				return TailResult{NewOffset: offset}
			}
			end = nl + 1
		}
		return TailResult{Text: string(raw[:end]), NewOffset: offset + int64(end)}
	}

	// zstd：只消费完整帧。扫描器坏在中途时返回「已扫出的完整帧+错误」——
	// 帧照常消费，错误随行上抛（调用方记账后下轮从坏点重试）。
	frames, _, serr := ScanZstdFrames(raw)
	var plain []byte
	consumed := 0
	var derr error
	for _, fr := range frames {
		out, err := decodeFrame(raw[fr.Start:fr.End])
		if err != nil {
			derr = err // 校验和/解码坏：停在该帧边界，前序帧照常交付
			break
		}
		plain = append(plain, out...)
		consumed = fr.End
	}
	retErr := derr
	if serr != nil {
		retErr = serr
	}
	if retErr != nil && consumed == 0 {
		// 首帧即坏（魔数/保留位/解码坏）：不推进偏移，错误上抛
		return TailResult{Err: retErr}
	}
	return TailResult{Text: string(plain), NewOffset: offset + int64(consumed), Err: retErr}
}
