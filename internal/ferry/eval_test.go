// eval_test.go — 票04:盲评生成工具的行为例。
//
// 覆盖面(对票面验收标准):
//   - 样本选取=最近 N 份交接按时间倒序;不足 N 如实全取(total 如实上抛);
//   - 假上游(httptest)下一样本一文件对:骨架/叙事并排,文件名含时间戳与
//     会话短 ID,内容含素材与叙事两段,产物注明「全文模式」与样本不足注;
//   - 无可用样本/供应商缺名/供应商表缺名 → 报错清晰不空跑;
//   - 单样本生成失败不炸整批:失败记账、跳过文件对,成功样本照常产出。
package ferry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeHandoff 样本目录里落一份交接 MD(生产命名契约 YYYYMMDD_HHMMSS_<短ID>.md)。
func writeHandoff(t *testing.T, dir, stamp, sid, content string) string {
	t.Helper()
	p := filepath.Join(dir, stamp+"_"+sid+".md")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// evalProviders 指向 httptest 假上游的供应商表。
func evalProviders(url string) map[string]Provider {
	return map[string]Provider{
		"fake": {Name: "fake", BaseURL: url, Model: "glm-5.3-flash", Protocol: ProtocolOpenAI},
	}
}

func sampleNames(ss []EvalSample) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.SessionID
	}
	return out
}

// ---- 样本选取 ----

func TestPickRecentHandoffsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	writeHandoff(t, dir, "20260929_080000", "ccc333", "旧")
	writeHandoff(t, dir, "20261001_074613", "aaa111", "新")
	writeHandoff(t, dir, "20260930_120000", "bbb222", "中")
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err) // 非 .md 不进样本
	}
	got, total, err := PickRecentHandoffs(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	names := sampleNames(got)
	if len(names) != 2 || names[0] != "aaa111" || names[1] != "bbb222" {
		t.Fatalf("样本序 = %v, want [aaa111 bbb222]（时间倒序取 2）", names)
	}
	if got[0].Stamp != "20261001_074613" || got[0].FileName != "20261001_074613_aaa111.md" {
		t.Fatalf("样本元数据不符: %+v", got[0])
	}
}

func TestPickRecentHandoffsFewerThanNTakesAll(t *testing.T) {
	dir := t.TempDir()
	writeHandoff(t, dir, "20261001_074613", "aaa111", "甲")
	got, total, err := PickRecentHandoffs(dir, 5)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(got) != 1 {
		t.Fatalf("total=%d len=%d, want 各 1（不足 N 如实全取）", total, len(got))
	}
}

func TestPickRecentHandoffsEmptyDirErrors(t *testing.T) {
	dir := t.TempDir()
	_, _, err := PickRecentHandoffs(dir, 3)
	if err == nil || !strings.Contains(err.Error(), "无可用交接") {
		t.Fatalf("want 无可用交接报错, got %v", err)
	}
}

func TestPickRecentHandoffsBadN(t *testing.T) {
	dir := t.TempDir()
	writeHandoff(t, dir, "20261001_074613", "aaa111", "甲")
	if _, _, err := PickRecentHandoffs(dir, 0); err == nil {
		t.Fatal("n=0 应报错")
	}
}

// ---- 生成主流程 ----

func TestRunEvalFerryGeneratesPairs(t *testing.T) {
	c := newChatSrv(t, []string{
		chatBody(t, "<<<INJECT>>>\n叙事甲\n<<</INJECT>>>\n\n# 目标\n甲全文"),
		chatBody(t, "<<<INJECT>>>\n叙事乙\n<<</INJECT>>>\n\n# 目标\n乙全文"),
	})
	handoffs := t.TempDir()
	out := t.TempDir()
	writeHandoff(t, handoffs, "20261001_074613", "aaa111", "这是骨架素材甲")
	writeHandoff(t, handoffs, "20260930_120000", "bbb222", "这是骨架素材乙")

	res, err := RunEvalFerry(EvalOptions{
		ProviderName: "fake",
		Providers:    evalProviders(c.srv.URL),
		N:            5, // > 可用 2 份 → 截断注明
		HandoffDir:   handoffs,
		OutDir:       out,
		TimeoutS:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Requested != 5 || res.Total != 2 || res.Taken != 2 || !res.Truncated {
		t.Fatalf("计数不符: %+v", res)
	}
	if len(res.Pairs) != 2 || len(res.Failures) != 0 {
		t.Fatalf("pairs=%d failures=%d, want 2/0", len(res.Pairs), len(res.Failures))
	}

	// 文件对:文件名含时间戳与会话短 ID,并排落盘,存在可读。
	for i, p := range res.Pairs {
		wantSID := []string{"aaa111", "bbb222"}[i]
		wantStamp := []string{"20261001_074613", "20260930_120000"}[i]
		for _, f := range []string{p.SkeletonPath, p.NarrativePath} {
			if !strings.Contains(filepath.Base(f), wantStamp+"_"+wantSID) {
				t.Fatalf("文件名缺时间戳+短 ID: %s", f)
			}
			if _, err := os.Stat(f); err != nil {
				t.Fatalf("产物缺失: %v", err)
			}
		}
		if !strings.HasSuffix(p.SkeletonPath, "_骨架.md") || !strings.HasSuffix(p.NarrativePath, "_叙事.md") {
			t.Fatalf("文件对后缀不符: %s / %s", p.SkeletonPath, p.NarrativePath)
		}
	}

	// 骨架件:含素材全文 + 全文模式注明 + 样本不足注明。
	sk, err := os.ReadFile(res.Pairs[0].SkeletonPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"这是骨架素材甲", "全文模式", "样本不足", "请求 5 份", "实取 2 份"} {
		if !strings.Contains(string(sk), want) {
			t.Fatalf("骨架件缺 %q:\n%s", want, sk)
		}
	}
	// 叙事件:含模型叙事 + 供应商/模型溯源 + 全文模式注明。
	nr, err := os.ReadFile(res.Pairs[0].NarrativePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"叙事甲", "全文模式", "供应商: fake", "glm-5.3-flash"} {
		if !strings.Contains(string(nr), want) {
			t.Fatalf("叙事件缺 %q:\n%s", want, nr)
		}
	}

	// 上游调用:逐样本一次,system=摆渡 SystemPrompt,user=交接 MD 全文素材。
	paths, _, payloads := c.snap()
	if len(paths) != 2 {
		t.Fatalf("上游请求数 = %d, want 2", len(paths))
	}
	for i, wantMaterial := range []string{"这是骨架素材甲", "这是骨架素材乙"} { // 时间倒序处理
		if got := userContent(t, payloads[i]); got != wantMaterial {
			t.Fatalf("样本 %d 的 user 素材 = %q, want %q", i, got, wantMaterial)
		}
		msgs := payloads[i]["messages"].([]any)
		sys, _ := msgs[0].(map[string]any)
		if sys["content"] != SystemPrompt {
			t.Fatalf("system 应为摆渡 SystemPrompt 原文")
		}
		if payloads[i]["max_tokens"] != float64(PromptReserve) {
			t.Fatalf("max_tokens = %v, want %d", payloads[i]["max_tokens"], PromptReserve)
		}
	}
}

func TestRunEvalFerryMissingProviderName(t *testing.T) {
	res, err := RunEvalFerry(EvalOptions{Providers: evalProviders("http://127.0.0.1:1"), N: 1,
		HandoffDir: t.TempDir(), OutDir: t.TempDir()})
	if err == nil || res != nil {
		t.Fatalf("缺 --provider 应报错, got res=%v err=%v", res, err)
	}
	if !strings.Contains(err.Error(), "供应商名") {
		t.Fatalf("报错应点名供应商名: %v", err)
	}
}

func TestRunEvalFerryUnknownProvider(t *testing.T) {
	_, err := RunEvalFerry(EvalOptions{ProviderName: "nope",
		Providers:  evalProviders("http://127.0.0.1:1"),
		N:          1,
		HandoffDir: t.TempDir(), OutDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "nope") || !strings.Contains(err.Error(), "fake") {
		t.Fatalf("报错应含缺失名与可用名: %v", err)
	}
}

func TestRunEvalFerryAnthropicDispatches(t *testing.T) {
	// 终局修复2：anthropic 档经 chainCall 分派走票03 适配器，盲评门可用。
	srv := newAnthSrv(t, anthBodyFor([]map[string]any{
		textBlock("k3 的叙事"), thinkingBlock("内部思考不进产物"),
	}, 120, 80))
	handoffs := t.TempDir()
	writeHandoff(t, handoffs, "20261001_074613", "aaa111", "素材甲")
	res, err := RunEvalFerry(EvalOptions{ProviderName: "kimi-k3",
		Providers: map[string]Provider{"kimi-k3": {Name: "kimi-k3",
			BaseURL: srv.srv.URL, Model: "k3", APIKey: "sk-k",
			Protocol: ProtocolAnthropic}},
		N: 1, HandoffDir: handoffs, OutDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pairs) != 1 {
		t.Fatalf("anthropic 档应产出文件对, got %d 对, failures=%v", len(res.Pairs), res.Failures)
	}
	nr, rerr := os.ReadFile(res.Pairs[0].NarrativePath)
	if rerr != nil || !strings.Contains(string(nr), "k3 的叙事") {
		t.Fatalf("叙事件应含适配器 text 块输出: err=%v", rerr)
	}
	if strings.Contains(string(nr), "内部思考不进产物") {
		t.Fatal("thinking 块不得混入叙事产物")
	}
	paths, apiKeys, _, _, _ := srv.snap()
	if len(paths) != 1 || paths[0] != "/v1/messages" {
		t.Fatalf("anthropic 请求路径 = %v, want [/v1/messages]", paths)
	}
	if len(apiKeys) != 1 || apiKeys[0] != "sk-k" {
		t.Fatalf("x-api-key = %v", apiKeys)
	}
}

func TestRunEvalFerryNoSamplesNoCalls(t *testing.T) {
	c := newChatSrv(t, nil)
	_, err := RunEvalFerry(EvalOptions{ProviderName: "fake", Providers: evalProviders(c.srv.URL),
		N: 3, HandoffDir: t.TempDir(), OutDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "无可用交接") {
		t.Fatalf("无样本应报错不空跑: %v", err)
	}
	if paths, _, _ := c.snap(); len(paths) != 0 {
		t.Fatalf("无样本不得发起上游调用, 实发 %d", len(paths))
	}
}

// ---- 单样本失败:不炸整批,失败记账,不落该样本的文件对 ----

func TestRunEvalFerryPerSampleFailureContinues(t *testing.T) {
	// 样本乙的应答为空体 → Chat 解析失败;样本甲正常。
	c := newChatSrv(t, []string{chatBody(t, "叙事甲"), ""})
	handoffs := t.TempDir()
	out := t.TempDir()
	writeHandoff(t, handoffs, "20261001_074613", "aaa111", "素材甲")
	writeHandoff(t, handoffs, "20260930_120000", "bbb222", "素材乙")

	res, err := RunEvalFerry(EvalOptions{ProviderName: "fake", Providers: evalProviders(c.srv.URL),
		N: 2, HandoffDir: handoffs, OutDir: out, TimeoutS: 10})
	if err != nil {
		t.Fatalf("单样本失败不该炸整批: %v", err)
	}
	if len(res.Pairs) != 1 || len(res.Failures) != 1 {
		t.Fatalf("pairs=%d failures=%d, want 1/1", len(res.Pairs), len(res.Failures))
	}
	if res.Pairs[0].Sample.SessionID != "aaa111" {
		t.Fatalf("成功对应是新样本: %+v", res.Pairs[0].Sample)
	}
	if res.Failures[0].Sample.SessionID != "bbb222" || res.Failures[0].Err == "" {
		t.Fatalf("失败记账缺样本或原因: %+v", res.Failures[0])
	}
	// 失败样本不落任何文件。
	ents, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, de := range ents {
		if strings.Contains(de.Name(), "bbb222") {
			t.Fatalf("失败样本不应落盘: %s", de.Name())
		}
	}
}

func TestRunEvalFerryEmptyNarrativeFails(t *testing.T) {
	// 200 但正文为空 → 空叙事按失败记账,不落空文件。
	c := newChatSrv(t, []string{`{"choices":[{"message":{"content":""}}],"usage":{}}`})
	handoffs := t.TempDir()
	writeHandoff(t, handoffs, "20261001_074613", "aaa111", "素材甲")
	res, err := RunEvalFerry(EvalOptions{ProviderName: "fake", Providers: evalProviders(c.srv.URL),
		N: 1, HandoffDir: handoffs, OutDir: t.TempDir(), TimeoutS: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pairs) != 0 || len(res.Failures) != 1 {
		t.Fatalf("空叙事应记账失败: pairs=%d failures=%d", len(res.Pairs), len(res.Failures))
	}
	if !strings.Contains(res.Failures[0].Err, "空叙事") {
		t.Fatalf("失败原因应点名空叙事: %q", res.Failures[0].Err)
	}
}

// JSON 序列化护底线:EvalResult 供 CLI 摘要消费,字段须可序列化可往返。
func TestEvalResultSerializable(t *testing.T) {
	raw, err := json.Marshal(EvalResult{Requested: 5, Total: 2, Taken: 2, Truncated: true})
	if err != nil {
		t.Fatal(err)
	}
	var back EvalResult
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Requested != 5 || back.Total != 2 || back.Taken != 2 || !back.Truncated {
		t.Fatalf("往返失真: %+v", back)
	}
}
