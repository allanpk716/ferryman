// arm_verdict.go — 票04:上游启用门状态机(ADR-0015 决定一【启用硬门槛】)。
//
// 「白名单预置 ≠ 启用」的真身:每上游维护追加重放实跳臂结论
// (待实跳 pending / 通过 passed / 未过 failed),持久化在状态目录
// (默认 ~/ferryman/arm_verdict.jsonl,与账本同风格:一行一条 JSON、
// append-only、人可读)。读取语义 = 每上游取最后一行(last-wins):
// 同一结论重复回写只是追加新行,天然幂等;复跑翻案(未过→通过、
// 通过→未过)同样靠追加末行表达,永不改写历史行。
//
// 状态机(转移表):
//
//	预置(白名单登记,无记录)  --实跳通过-->  启用(passed)
//	预置                      --实跳未过-->  保持未启用(failed)
//	启用(passed)              --复跑未过-->  吊销启用(failed)
//	未过(failed)              --复跑通过-->  重新启用(passed)
//
// 无结论 = 未启用:票01 的 ArmVerdictResolver 缝(watcher not_enabled 判据与
// doctor 同一查法)的真源即 ArmVerdictResolverFor——文件缺失/无记录一律
// (false,false),启用硬门槛由缺省形态兜底。接线(把缝装配进 watcher/
// doctor)在收口票:internal/daemon、cmd 不在本票路径内。
package ferry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"ferryman/internal/clock"
)

// ArmRecordKind 状态文件里本类记录的 kind 字段值(与账本 kind 同风格)。
const ArmRecordKind = "arm_verdict"

// 实跳臂结论状态三态(状态文件 verdict 字段只落 passed/failed;
// pending 是「无记录」的推导态,不落文件——无结论不落账)。
const (
	ArmStatePending = "pending" // 待实跳(白名单预置、尚无结论)
	ArmStatePassed  = "passed"  // 实跳通过 → 启用
	ArmStateFailed  = "failed"  // 实跳未过 → 保持未启用
)

// ArmRecord 一条实跳臂结论记录(状态文件一行)。Criteria 为四条标准
// (ArmCrit* 名 → ArmStatus* 值)的证据快照;占比双值随行留档。
type ArmRecord struct {
	V             int               `json:"v"`
	Kind          string            `json:"kind"`
	TS            float64           `json:"ts"`
	TSISO         string            `json:"ts_iso"`
	Upstream      string            `json:"upstream"`
	Verdict       string            `json:"verdict"` // passed | failed
	Criteria      map[string]string `json:"criteria,omitempty"`
	AppendRatio   float64           `json:"append_ratio,omitempty"`
	BaselineRatio float64           `json:"baseline_ratio,omitempty"`
	Model         string            `json:"model,omitempty"`
	Snapshot      string            `json:"snapshot,omitempty"` // 实跳所用快照文件(证据链)
	Note          string            `json:"note,omitempty"`
}

// RecordArmVerdict 追加一条结论到状态文件(append-only;同结论重复回写=
// 追加新行,幂等语义靠 last-wins 读取承载)。非 passed/failed 的结论拒绝
// 落账——inconclusive(不可判定)不是状态,状态保持待实跳。
func RecordArmVerdict(path string, rec ArmRecord) error {
	if rec.Verdict != ArmStatePassed && rec.Verdict != ArmStateFailed {
		return fmt.Errorf("arm_verdict: 结论 %q 非法(只落 passed/failed;不可判定不落状态)", rec.Verdict)
	}
	if rec.Upstream == "" {
		return fmt.Errorf("arm_verdict: 缺 upstream")
	}
	if rec.V == 0 {
		rec.V = 1
	}
	rec.Kind = ArmRecordKind
	if rec.TS == 0 {
		rec.TS = clock.Now()
	}
	if rec.TSISO == "" {
		rec.TSISO = time.Unix(int64(rec.TS), 0).Format("2006-01-02T15:04:05-0700")
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("arm_verdict: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("arm_verdict: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("arm_verdict: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("arm_verdict: %w", err)
	}
	return nil
}

// LoadArmVerdicts 读状态文件 → 每上游最后一行(last-wins)。文件不存在 =
// 零记录(nil error);坏行(手编痕迹)跳过不炸——append-only 文件读侧从宽、
// 写侧从严。
func LoadArmVerdicts(path string) (map[string]ArmRecord, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]ArmRecord{}, nil
		}
		return nil, fmt.Errorf("arm_verdict: %w", err)
	}
	out := map[string]ArmRecord{}
	for _, ln := range splitLines(raw) {
		var rec ArmRecord
		if json.Unmarshal([]byte(ln), &rec) != nil {
			continue
		}
		if rec.Kind != ArmRecordKind || rec.Upstream == "" {
			continue
		}
		if rec.Verdict != ArmStatePassed && rec.Verdict != ArmStateFailed {
			continue
		}
		out[rec.Upstream] = rec
	}
	return out, nil
}

// splitLines 按行切(容忍 \r\n 与尾空行)。
func splitLines(raw []byte) []string {
	var out []string
	start := 0
	for i, b := range raw {
		if b == '\n' {
			line := string(raw[start:i])
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			if line != "" {
				out = append(out, line)
			}
			start = i + 1
		}
	}
	if start < len(raw) {
		if line := string(raw[start:]); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// ArmState 该上游当前状态:无记录 = 待实跳;有记录 = 末行结论。
func ArmState(recs map[string]ArmRecord, upstream string) string {
	rec, ok := recs[upstream]
	if !ok {
		return ArmStatePending
	}
	return rec.Verdict
}

// ArmVerdictResolverFor 状态文件 → 实跳臂结论查询缝。返回值与票01
// config.ArmVerdictResolver 同形(可直接装配给 watcher.ArmVerdict 与
// doctor);每次调用现读状态文件——文件小(每上游一行×复跑次数)且
// 实跳回写是人工低频动作,现读保证 daemon 侧零陈旧。
func ArmVerdictResolverFor(path string) func(upstream string) (hasVerdict, enabled bool) {
	return func(upstream string) (hasVerdict, enabled bool) {
		recs, err := LoadArmVerdicts(path)
		if err != nil {
			return false, false // 读失败按无结论对待(启用硬门槛缺省兜底)
		}
		rec, ok := recs[upstream]
		if !ok {
			return false, false
		}
		return true, rec.Verdict == ArmStatePassed
	}
}

// DefaultArmVerdictPath 默认状态文件落点:~/ferryman/arm_verdict.jsonl
// (config.DataDir 同根;不 import config,保持 ferry 叶子依赖)。
func DefaultArmVerdictPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "arm_verdict.jsonl" // 无 home 的非常规环境:退工作目录
	}
	return filepath.Join(home, "ferryman", "arm_verdict.jsonl")
}
