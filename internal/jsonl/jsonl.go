// Package jsonl 行式 JSONL 共享助手：无上限行读、宽容 dict 解码、JSON 值真值
// （票 07 评审收口：cctrans/extract/codextrans 三包共用的逐字等价副本收进单源）。
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
