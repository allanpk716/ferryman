// Package jsonl 行式 JSONL 共享助手：无上限行读、宽容 dict 解码、JSON 值真值
// （票 07 评审收口：cctrans/extract/codextrans 三包共用的逐字等价副本收进单源）、
// 尾窗读（票 10 骑手收口：qwatch/cctrans 两份私有 tailWindow 归一）。
//
// 行读纪律：一律 bufio.Reader.ReadBytes('\n') 无上限——Scanner 有内部行上限
// （ErrTooLong 断流，超长行后的内容全部丢失），禁用。
// 防御纪律：DecodeDict 对坏 JSON/顶层非 dict 一律 false 静默，绝不向调用方抛错。
package jsonl

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
)

// ReadLines 以 ReadBytes('\n') 无上限行读逐行交付（Python for line in f 的
// Go 形）；fn 返回 false 提前止步。行含行尾 '\n'。打开文件/读中断流（非 EOF）
// → 上抛，由调用方按 OSError 语义收场。
func ReadLines(path string, fn func(line string) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		raw, rerr := r.ReadBytes('\n')
		if len(raw) > 0 && !fn(string(raw)) {
			return nil
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return nil
			}
			return rerr
		}
	}
}

// DecodeDict 行 → 顶层 dict；坏 JSON 或顶层非 dict（评审#11）一律 false 静默。
func DecodeDict(line string) (map[string]any, bool) {
	var d any
	if json.Unmarshal([]byte(line), &d) != nil {
		return nil, false
	}
	m, ok := d.(map[string]any)
	return m, ok
}

// Truthy JSON 解码值的 Python bool() 真值语义（nil/0/""/空容器 falsy；
// json.Unmarshal 只产出这几种类型）。
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	default:
		return true
	}
}

// TailWindow 只读文件尾部 tailBytes 字节并按 '\n' 切行（Python 尾窗读法 1:1：
// Stat 取文件长 → 尾段 ReadAt → end > tail 时丢弃首行半行）。
// 打不开/读不了 → ok=false（一切 OSError 语义 → false，由调用方兜底）。
// 解码 errors="replace" 语义：非法 UTF-8 字节串以 U+FFFD 替换后切行
// （票 10 骑手收口：统一 qwatch 版 ToValidUTF8 语义，修复 cctrans 版
// 坏 UTF-8 保真缺口）。
func TailWindow(path string, tailBytes int64) ([]string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, false
	}
	end := st.Size()
	off := end - tailBytes
	if off < 0 {
		off = 0
	}
	if off > end {
		off = end // tailBytes 为负等异常入参：等效空窗（Python seek 越界读空）
	}
	data := make([]byte, end-off)
	if len(data) > 0 {
		n, rerr := f.ReadAt(data, off)
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return nil, false
		}
		data = data[:n]
	}
	lines := strings.Split(strings.ToValidUTF8(string(data), "�"), "\n")
	if end > tailBytes && len(lines) > 0 {
		lines = lines[1:] // 窗口首行可能是半行，丢弃
	}
	return lines, true
}
