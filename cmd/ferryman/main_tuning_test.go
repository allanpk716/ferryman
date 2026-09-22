package main

// main_tuning_test.go — 终局修复(票07 回执与 status 如实):tuning status 每
// 上游增一行生效值(经 Store.EffectiveThreshold 现算,或拒算原因+守望回落
// 种子);apply 回执打印现算生效值——不再留"status 可查"而 status 不显示的
// 空承诺。配置/数据目录/价格表全指临时目录,不读真实 ~/ferryman。

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ferryman/internal/tuning"
)

// tuningTestEnv 写最小 config.toml(recommend 档+同模型白名单 glm+glm 价格
// 表,总结阈值 25 分钟、上限 25),返回 (--config 路径, 数据根)。
func tuningTestEnv(t *testing.T, mode string) (string, string) {
	t.Helper()
	tmp := t.TempDir()
	data := filepath.Join(tmp, "data")
	cfgPath := filepath.Join(tmp, "config.toml")
	toml := "[server]\ndata_dir = \"" + filepath.ToSlash(data) + "\"\n" +
		"\n[thresholds]\nsummarize_s = 1500\nblock_s = 2100\n" +
		"\n[tuning]\nmode = \"" + mode + "\"\n" +
		"\n[ferry.same_model]\nenabled = true\nthreshold_min = 25\nupstreams = [\"glm\"]\n" +
		"\n[prices.glm]\nunit = \"u\"\nper = 10000\n" +
		"\n[[prices.glm.versions]]\neffective_from = \"2026-01-01\"\n" +
		"p_in = 6.9\np_cache = 1.7\np_out = 24\n"
	if err := os.WriteFile(cfgPath, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfgPath, data
}

// tuningIdle20 校准闲置样本(n=20,F(25)=0.95;TTL [20] → 安全点 16、经济点 15,
// 生效值 16)。
func tuningIdle20() []float64 {
	return []float64{40, 20, 15, 15, 15, 10, 10, 10, 10, 10,
		5, 5, 5, 5, 5, 5, 5, 5, 5, 5}
}

// tuningSeedSug 落一条建议并接受(校准=建议观测投影)。
func tuningSeedSug(t *testing.T, data string, id string, idle []float64) {
	t.Helper()
	st := tuning.NewStore(data)
	now := float64(time.Now().Unix())
	sug := &tuning.Suggestion{ID: id, Upstream: "glm", SuggestMin: 16, CurrentMin: 25,
		HasCurrent: true, BestMin: 16, NetSavings: 1, FerryEvents: 42, MinEvents: 30,
		TTLObsMin: []float64{20}, IdleObsMin: idle, CreatedAt: now}
	if err := st.RecordSuggestion(sug, now); err != nil {
		t.Fatal(err)
	}
	if err := st.Accept(id, "recommend", 25, now+1); err != nil {
		t.Fatal(err)
	}
}

// TestCmdTuningStatusShowsEffective status 每上游一行生效值:校准齐全 →
// 现算 16.0 分钟(TTL 中位 20×0.8),不再只有校准摘要。
func TestCmdTuningStatusShowsEffective(t *testing.T) {
	cfgPath, data := tuningTestEnv(t, "recommend")
	tuningSeedSug(t, data, "s1-glm", tuningIdle20())
	var out, errb bytes.Buffer
	if code := cmdTuningStatus([]string{"--config", cfgPath}, &out, &errb); code != 0 {
		t.Fatalf("status 退出码 %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "生效值") {
		t.Fatalf("status 应含生效值段:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "16.0 分钟") {
		t.Fatalf("status 应显示现算生效值 16.0 分钟:\n%s", out.String())
	}
}

// TestCmdTuningStatusShowsRefusal 拒算如实:缺闲置的旧档位校准 → 生效值行
// 显示拒算原因与守望回落种子,不编造数字。
func TestCmdTuningStatusShowsRefusal(t *testing.T) {
	cfgPath, data := tuningTestEnv(t, "recommend")
	tuningSeedSug(t, data, "s1-glm", nil) // 旧档位形态:只有 TTL,无闲置
	var out, errb bytes.Buffer
	if code := cmdTuningStatus([]string{"--config", cfgPath}, &out, &errb); code != 0 {
		t.Fatalf("status 退出码 %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "拒算") || !strings.Contains(out.String(), "冷启动种子") {
		t.Fatalf("status 拒算行应带原因与回落种子:\n%s", out.String())
	}
}

// TestCmdTuningApplyReceiptShowsEffective apply 回执:打印现算生效值
// (16.0 分钟)与回滚指引——兑现"可查"的承诺。
func TestCmdTuningApplyReceiptShowsEffective(t *testing.T) {
	cfgPath, data := tuningTestEnv(t, "recommend")
	st := tuning.NewStore(data)
	now := float64(time.Now().Unix())
	sug := &tuning.Suggestion{ID: "s1-glm", Upstream: "glm", SuggestMin: 16, CurrentMin: 25,
		HasCurrent: true, BestMin: 16, NetSavings: 1, FerryEvents: 42, MinEvents: 30,
		TTLObsMin: []float64{20}, IdleObsMin: tuningIdle20(), CreatedAt: now}
	if err := st.RecordSuggestion(sug, now); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := cmdTuningApply([]string{"--config", cfgPath, "s1-glm"}, &out, &errb); code != 0 {
		t.Fatalf("apply 退出码 %d: %s\n%s", code, errb.String(), out.String())
	}
	if !strings.Contains(out.String(), "生效值 16.0 分钟") {
		t.Fatalf("apply 回执应打印现算生效值 16.0 分钟:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "回滚") {
		t.Fatalf("apply 回执应保留回滚指引:\n%s", out.String())
	}
}
