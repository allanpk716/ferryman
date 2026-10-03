// frames.go — 拼接 zstd 帧的结构扫描（P2-1 第一块砖）。
//
// dsh 会话文件＝checksummed zstd 帧拼接（每批事件一帧，追加式；物质化时
// 首帧仅头行）。本扫描器逐字节对 dsh 源码钉死（session-persistence-jsonl/
// src/zstd.ts scanZstdFrames 1:1）：只走帧结构不解压块——魔数、帧头描述符
// （保留位拒绝）、窗口/字典/内容尺寸变长头、块头（last/type/size 三字节，
// RLE 块载荷恒 1 字节、保留块型拒绝）、可选 4 字节内容校验和。EOF 打断尾帧
// → tornStart 报尾帧起点（写入方下次追加/修复；读取方只消费完整帧）。
//
// 解压单帧用 klauspost/compress DecodeAll（校验和验证内建）。注意：dsh 用
// ZSTD_c_checksumFlag=1 写帧；扫描器对无校验和的帧同样兼容（结构位判定），
// skippable 帧（魔数 0x184D2A5x）上游不写、本扫描器按无效魔数拒绝（同
// dsh 行为）。
package dshtrans

import (
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// ZSTDMagic zstd 帧魔数（小端读数）。
const ZSTDMagic = 0xFD2FB528

// FrameRange 一个结构完整帧的字节区间（相对传入切片）。
type FrameRange struct {
	Start int // 含
	End   int // 不含
}

// ScanZstdFrames 定位全部结构完整帧；EOF 打断尾帧时 tornStart=尾帧起点
// （无残帧为 -1）。结构性损坏（无效魔数/保留位/保留块型）返回已扫出的
// 完整帧 + 错误（dsh 上游 throw 的 Go 形——调用方按防御纪律收口）。
func ScanZstdFrames(b []byte) (frames []FrameRange, tornStart int, err error) {
	frames = nil
	tornStart = -1
	offset := 0
	torn := func(start int) ([]FrameRange, int, error) {
		return frames, start, nil
	}
	for offset < len(b) {
		start := offset
		if len(b)-offset < 4 {
			return torn(start)
		}
		if le32(b[offset:]) != ZSTDMagic {
			return frames, -1, fmt.Errorf("corrupt Zstandard session log: invalid frame magic at byte %d", offset)
		}
		offset += 4

		if offset == len(b) {
			return torn(start)
		}
		descriptor := b[offset]
		offset++
		if descriptor&0x18 != 0 {
			return frames, -1, fmt.Errorf("corrupt Zstandard session log: reserved frame-header bit at byte %d", offset-1)
		}

		contentSizeFlag := descriptor >> 6
		singleSegment := descriptor&0x20 != 0
		checksum := descriptor&0x04 != 0
		dictionaryFlag := int(descriptor & 0x03)
		dictionaryBytes := dictionaryFlag
		if dictionaryFlag == 3 {
			dictionaryBytes = 4
		}
		contentSizeBytes := 0
		if contentSizeFlag == 0 {
			if singleSegment {
				contentSizeBytes = 1
			}
		} else {
			contentSizeBytes = 1 << contentSizeFlag
		}
		remainingHeader := contentSizeBytes + dictionaryBytes
		if !singleSegment {
			remainingHeader++
		}
		if len(b)-offset < remainingHeader {
			return torn(start)
		}
		offset += remainingHeader

		for {
			if len(b)-offset < 3 {
				return torn(start)
			}
			blockHeader := le24(b[offset:])
			offset += 3
			lastBlock := blockHeader&1 != 0
			blockType := (blockHeader >> 1) & 0x03
			blockSize := blockHeader >> 3
			if blockType == 0x03 {
				return frames, -1, fmt.Errorf("corrupt Zstandard session log: reserved block type at byte %d", offset-3)
			}
			payloadBytes := blockSize
			if blockType == 0x01 {
				payloadBytes = 1 // RLE 块：单字节载荷
			}
			if len(b)-offset < int(payloadBytes) {
				return torn(start)
			}
			offset += int(payloadBytes)
			if lastBlock {
				break
			}
		}

		if checksum {
			if len(b)-offset < 4 {
				return torn(start)
			}
			offset += 4
		}
		frames = append(frames, FrameRange{Start: start, End: offset})
	}
	return frames, -1, nil
}

// le24/le32 小端读数（Buffer.readUIntLE 的 Go 形）。
func le24(b []byte) uint32 { return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 }
func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// ---- 单帧解码（klauspost DecodeAll；包级惰性单例，DecodeAll 并发安全） ----

var (
	frameDecoderOnce sync.Once
	frameDecoder     *zstd.Decoder
	frameDecoderErr  error
)

// decodeFrame 解压一个结构完整帧（校验和验证内建）；错误原样上抛由调用方
// 收口。解码器构建失败（初始化期）按致命错误缓存——后续调用恒同错。
func decodeFrame(frame []byte) ([]byte, error) {
	frameDecoderOnce.Do(func() {
		// nil reader＝仅 DecodeAll 的无状态形态（klauspost 文档口）；
		// 守望单线程消费，并发 1 档足够。
		frameDecoder, frameDecoderErr = zstd.NewReader(nil,
			zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
	})
	if frameDecoderErr != nil {
		return nil, frameDecoderErr
	}
	return frameDecoder.DecodeAll(frame, nil)
}
