// health_client.go — verify-dsh 票04：daemon 管理口只读 HTTP 客户端。
//
// 两个端点（均在既有管理口鉴权面内，与 /stats 同域，票01 落地）：
//   - GET /dsh/health —— L1 挂载记账＋宿主旁证（键名＝daemon DshHealth() 的
//     API 契约，票01 钉死；此处只镜像不重造语义）；
//   - GET /stats —— daemon 版本自报（判定流水三元组的 daemon 份单源）。
//
// 纪律：纯读、Bearer 鉴权、2s 超时（cmd status.daemonGet 同位——本机环回拖长
// 无意义）；BaseURL 由装配层派生（CLI 自 config.Server.Port），测试以
// httptest 指假端点——绝不打真端口。错误分两类哨兵（errDaemonOffline/
// errAuth 同款语义，供编排层区分「守护不在线」与「在线但 token 错位」文案）。
package dshverify

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// 哨兵错误（status.go errDaemonOffline/errAuth 同语义；编排层按类出文案）。
var (
	// ErrDaemonUnreachable 管理口拨不通（连接拒绝/超时）——「守护不在线」判据。
	ErrDaemonUnreachable = errors.New("守护不在线（管理口无应答）")
	// ErrAuth 端点应答但鉴权失败（401/403）——在线但本机 token 与守护错位。
	ErrAuth = errors.New("鉴权失败（401/403）")
)

// Health GET /dsh/health 应答形状（键＝票01 DshHealth 的 API 契约；daemon 侧
// 改键名时此处解析静默失真——票01/票04 双侧测试各自钉住）。
type Health struct {
	// LastPollAgeS 全局最近 poll 年龄秒；null＝本进程 lifecycle 内未见过 poll
	// （守护重启冷启动窗）——形态 A 判定原料（票01 文件头「合成语义」节）。
	LastPollAgeS *float64 `json:"last_poll_age_s"`
	// Sessions sid → 最近被 poll 见到年龄秒（宿主近似粒度）。
	Sessions map[string]float64 `json:"sessions"`
	// PollHintS config poll_hint_s（daemon 经 poll 应答下发的建议间隔）。
	PollHintS float64 `json:"poll_hint_s"`
	// PollRhythmS 实测节律中位数；null＝不足两轮。
	PollRhythmS *float64 `json:"poll_rhythm_s"`
	// IntervalSource 生效间隔依据（hint|rhythm|none）。
	IntervalSource string `json:"interval_source"`
	// IntervalAssumed 无任何已知间隔走 90s 兜底时为 true（输出注明假设）。
	IntervalAssumed bool `json:"interval_assumed"`
	// EffectiveIntervalS 生效间隔秒。
	EffectiveIntervalS float64 `json:"effective_interval_s"`
	// OverdueThresholdS 超龄阈值秒（3×生效间隔，D13）。
	OverdueThresholdS float64 `json:"overdue_threshold_s"`
	// PollOverdue 全局最近 poll 年龄 > 阈值（从未 poll 恒 false——票01 不越权）。
	PollOverdue bool `json:"poll_overdue"`
	// HostProcessesPresent 宿主进程旁证布尔（harness 进程腿∨3080 端口腿）。
	HostProcessesPresent bool   `json:"host_processes_present"`
	HostEvidence         string `json:"host_evidence"`
}

// HealthClient 管理口客户端。Client nil＝缺省（2s 超时）。
type HealthClient struct {
	BaseURL string // 形如 http://127.0.0.1:15700（无尾斜杠）
	Token   string // Bearer（绝不打印）
	Client  *http.Client
}

// get 共用 GET：Bearer＋2s 超时＋1MiB 读上界。网络错→ErrDaemonUnreachable；
// 401/403→ErrAuth；其余非 200→带状态码错误（status.daemonGet 同款分类）。
func (c *HealthClient) get(path string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	hc := c.Client
	if hc == nil {
		hc = &http.Client{Timeout: 2 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDaemonUnreachable, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 先排水后解码
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, ErrAuth
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("HTTP %d（%s）", resp.StatusCode, path)
	}
	return body, nil
}

// Health GET /dsh/health → 应答形状。
func (c *HealthClient) Health() (*Health, error) {
	body, err := c.get("/dsh/health")
	if err != nil {
		return nil, err
	}
	var h Health
	if err := json.Unmarshal(body, &h); err != nil {
		return nil, fmt.Errorf("/dsh/health 解析失败: %w", err)
	}
	return &h, nil
}

// StatsVersion GET /stats 的 version 字段（daemon 版本自报——判定流水三元组
// 的 daemon 份单源；缺失＝空串由调用方注明）。
func (c *HealthClient) StatsVersion() (string, error) {
	body, err := c.get("/stats")
	if err != nil {
		return "", err
	}
	var out struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("/stats 解析失败: %w", err)
	}
	return out.Version, nil
}
