// Package dshledger verify-dsh 的判定流水与契约锚存储（票03，ADR-0026，
// decision_refs D5/D7/D11，spec「契约锚与已知良好档案」节）。
//
// 两个持久面都在 daemon 数据目录的 dshledger/ 子目录（与账本 accounts/ 同区，
// 不入 config.toml）：
//   - verdicts.jsonl 判定流水：append-only JSONL，每行＝DSH/插件/daemon 版本
//     三元组＋判定（green/yellow/red）＋日期时间＋契约锚哈希＋可选 installer
//     路径；最近全绿行＝已知良好指针。D7 纪律：只背书真跑过验证的版本，
//     不声称支持范围——本包不提供 min/max 版本 API。
//   - anchors/ 契约锚目录（anchor.go）：四类契约面形状签名快照，绑 DSH 版本，
//     全绿写入、滚动保留最近 N 份。
//
// 并发模型：单一 daemon/CLI 进程串行调用是调用方契约（票面钉死）；包内互斥
// 只防进程内并发（accounts 同款）。daemon 版本字段由调用方传入（票04 取生产
// daemon 自报版本，本包不猜）；存储根由调用方传入，本包不猜数据目录位置。
package dshledger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ferryman/internal/clock"
	"ferryman/internal/jsonl"
	"ferryman/internal/mathx"
)

// Verdict 判定灯色（spec「灯色、输出与告警」三档；存英文字面量，展示层翻译）。
type Verdict string

const (
	VerdictGreen  Verdict = "green"  // 全绿：三 profile 静态/挂载＋web 沙箱功能探针全过
	VerdictYellow Verdict = "yellow" // 版本未验证/超龄无宿主——只挂输出不推送
	VerdictRed    Verdict = "red"    // 断言失败/超龄有宿主——推送告警
)

// Entry 判定流水一行。json tag 显式 snake_case（store 同款纪律：直接序列化
// 不得输出 CamelCase）；字段序＝落盘行键序（结构体定序，确定性序列化）。
type Entry struct {
	TS            float64 `json:"ts"`     // UTC epoch 秒（clock 口径，毫秒精度盖章）
	TSISO         string  `json:"ts_iso"` // 本地时区（accounts ts_iso 同款格式）
	DSHVersion    string  `json:"dsh_version"`
	PluginVersion string  `json:"plugin_version"`
	DaemonVersion string  `json:"daemon_version"`
	Verdict       Verdict `json:"verdict"`
	AnchorHash    string  `json:"anchor_hash"`              // 全绿行=契约锚哈希（AnchorStore.Write 同源值）；黄/红行空串
	InstallerPath string  `json:"installer_path,omitempty"` // 可选（D11）：降级安装包路径；不填不报错
}

// VerdictLog 判定流水（append-only JSONL）。
type VerdictLog struct {
	mu   sync.Mutex // 进程内互斥；跨进程串行性由「单进程调用」调用方契约保证
	path string
}

// New 打开判定流水：<dataDir>/dshledger/verdicts.jsonl（目录不存在则建）。
func New(dataDir string) (*VerdictLog, error) {
	dir := filepath.Join(dataDir, "dshledger")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &VerdictLog{path: filepath.Join(dir, "verdicts.jsonl")}, nil
}

// Append 追加一行并落盘（O_APPEND；单行 = 结构体定序 compact JSON + '\n'）。
// 时间由本包盖章（clock.Now）——日期口径单源，调用方不传；daemon 版本照传
// 值落盘（票04 负责取生产 daemon 自报，本包不猜不补）。verdict 非三档响亮
// 拒绝（错灯色入流水比缺行更毒——已知良好指针只认 green）。
func (v *VerdictLog) Append(dshVersion, pluginVersion, daemonVersion string,
	verdict Verdict, anchorHash, installerPath string) (Entry, error) {
	switch verdict {
	case VerdictGreen, VerdictYellow, VerdictRed:
	default:
		return Entry{}, fmt.Errorf("未知判定: %q（可选 green|yellow|red）", verdict)
	}
	ts := clock.Now()
	e := Entry{
		TS:            mathx.Round(ts, 3),
		TSISO:         time.Unix(int64(ts), 0).Format("2006-01-02T15:04:05-0700"),
		DSHVersion:    dshVersion,
		PluginVersion: pluginVersion,
		DaemonVersion: daemonVersion,
		Verdict:       verdict,
		AnchorHash:    anchorHash,
		InstallerPath: installerPath,
	}
	line, err := marshalLine(e)
	if err != nil {
		return Entry{}, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	fh, err := os.OpenFile(v.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return Entry{}, err
	}
	defer fh.Close()
	if _, err := fh.Write(line); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// List 全部流水行（落盘序＝时间序）。文件缺失（首跑）→ 空表非错；坏行跳过
// （进程中断留下的半行容忍——append-only 流水的读侧宽容，accounts 同款）。
func (v *VerdictLog) List() ([]Entry, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := []Entry{}
	err := jsonl.ReadLines(v.path, func(line string) bool {
		line = strings.TrimSpace(line)
		if line == "" {
			return true
		}
		var e Entry
		if json.Unmarshal([]byte(line), &e) != nil {
			return true // 坏行跳过（宁缺勿炸：指针侧不因半行失明）
		}
		out = append(out, e)
		return true
	})
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	return out, nil
}

// RecentKnownGood 已知良好指针＝流水最近一条全绿行（三元组＋锚哈希＋时刻
// 在其中，降级回退目标由此取）。无 green（首跑/从未全绿）→ nil——不声称
// 任何支持范围（D7）。黄/红行不抬指针。
func (v *VerdictLog) RecentKnownGood() (*Entry, error) {
	rows, err := v.List()
	if err != nil {
		return nil, err
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Verdict == VerdictGreen {
			e := rows[i] // 副本出锁（store ValidHandoff 同款纪律）
			return &e, nil
		}
	}
	return nil, nil
}

// marshalLine 单行 compact JSON + 尾随 '\n'（json.Encoder 自带换行）；
// SetEscapeHTML(false)＝非 ASCII 直出（store/accounts 同款：installer 路径
// 常含中文，转义会毁人读性）。
func marshalLine(v any) ([]byte, error) {
	b, err := encodeCompact(v)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// encodeCompact 紧凑 JSON（无尾随换行）；SetEscapeHTML(false) 同 marshalLine。
func encodeCompact(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
