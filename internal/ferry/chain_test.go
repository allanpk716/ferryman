package ferry

// chain_test.go — 票03：摆渡多级顺位链执行器 + Anthropic 协议适配器的行为钉。
//
// 覆盖票面验收：
//   - 杀掉第 N 级（httptest 假上游按协议回放）→ 第 N+1 级产出交接，顺序断言；
//   - 五类失败（连接/HTTP≥400/解析失败/空输出/结构不合）各一测：判失败→滑落；
//   - 全链死 → 聚合 error（worker 侧骨架与告警在 internal/daemon 的接线测）；
//   - 每级单次尝试零重试（假上游计数 = 1）；
//   - 本地级拨号超时 5s 的配置语义 + 拨号限时包装的真行为（伪底层拨号器，零网络）；
//   - Anthropic 适配器：正常回包 / thinking 块混排 / usage 映射 / extra_body 并入
//     / 端点拼接三形 / HTTP≥400 文案。
//
// 全部用 httptest 假上游，不出网；结构校验与同模型档同款纪律
// （ParseSameModelOutput：两标记齐、顺序正、两层非空）。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- 测试小材料 ----

// chainOKReply 严格合格的最小模型回复（两层标记齐、两层皆非空——同模型档
// 同款纪律；链执行器按此判定结构校验过）。
const chainOKReply = "<<<INJECT>>>\n注入层：干完了 fb.py\n<<</INJECT>>>\n\n# 全文\n干完了：fb.py"

// writeSmallCCSession 两行最小 CC 会话（user+assistant usage）——小材料必走
// L1 单发（writeBigCCSession 的对偶；大材料 L2 已由 FerrySession 既有测钉住）。
func writeSmallCCSession(t *testing.T, dir string) string {
	t.Helper()
	mk := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	lines := []string{
		mk(map[string]any{"type": "user", "timestamp": "2026-09-18T10:00:00.000Z",
			"cwd": "C:/proj", "message": map[string]any{"role": "user", "content": "做点活"}}),
		mk(map[string]any{"type": "assistant", "timestamp": "2026-09-18T10:01:00.000Z",
			"message": map[string]any{"role": "assistant",
				"content": []any{map[string]any{"type": "text", "text": "干完了"}},
				"usage":   map[string]any{"input_tokens": 200}}}),
	}
	f := filepath.Join(dir, "small.jsonl")
	if err := os.WriteFile(f, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// callOrder 跨上游的请求到达顺序（杀 N 级 → N+1 级的顺序断言面）。
type callOrder struct {
	mu   sync.Mutex
	tags []string
}

func (o *callOrder) add(tag string) {
	o.mu.Lock()
	o.tags = append(o.tags, tag)
	o.mu.Unlock()
}

func (o *callOrder) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string{}, o.tags...)
}

// chainUpstream 可编排的记录型 OpenAI 形假上游：behaviors 按序回放（耗尽
// 重复末项）；order 记跨上游到达序。
type chainUpstream struct {
	mu        sync.Mutex
	srv       *httptest.Server
	calls     int
	payloads  []map[string]any
	behaviors []chainBeh
	order     *callOrder
	tag       string
}

type chainBeh struct {
	status int
	body   string
}

// chatBodyFor 包一层 OpenAI choices 封包（content=给定文本、usage 10/5/15；
// content 空串 = 空输出类）。
func chatBodyFor(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
		"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	})
	return string(b)
}

func newChainUpstream(t *testing.T, tag string, order *callOrder, behs ...chainBeh) *chainUpstream {
	t.Helper()
	u := &chainUpstream{behaviors: behs, order: order, tag: tag}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(raw, &payload)
		u.mu.Lock()
		u.calls++
		n := u.calls
		u.payloads = append(u.payloads, payload)
		u.mu.Unlock()
		if u.order != nil {
			u.order.add(tag)
		}
		beh := u.behaviors[min(n, len(u.behaviors))-1]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(beh.status)
		_, _ = w.Write([]byte(beh.body))
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *chainUpstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

// closedUpstreamURL 杀掉的上级：起监听即关 → 拨号连接拒绝（连接失败类）。
func closedUpstreamURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()
	return srv.URL
}

// anthSrv Anthropic 形记录型假上游（/v1/messages；记录路径/头/载荷）。
type anthSrv struct {
	mu       sync.Mutex
	srv      *httptest.Server
	calls    int
	paths    []string
	apiKeys  []string
	versions []string
	payloads []map[string]any
	body     string
}

func newAnthSrv(t *testing.T, body string) *anthSrv {
	t.Helper()
	a := &anthSrv{body: body}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(raw, &payload)
		a.mu.Lock()
		a.calls++
		a.paths = append(a.paths, r.URL.Path)
		a.apiKeys = append(a.apiKeys, r.Header.Get("x-api-key"))
		a.versions = append(a.versions, r.Header.Get("anthropic-version"))
		a.payloads = append(a.payloads, payload)
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(a.body))
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *anthSrv) snap() (paths, apiKeys, versions []string, payloads []map[string]any, calls int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string{}, a.paths...), append([]string{}, a.apiKeys...),
		append([]string{}, a.versions...), append([]map[string]any{}, a.payloads...), a.calls
}

// anthBodyFor Anthropic Messages 回包封包：content 块序列 + usage input/output。
func anthBodyFor(blocks []map[string]any, in, out int) string {
	b, _ := json.Marshal(map[string]any{
		"content": blocks,
		"usage":   map[string]any{"input_tokens": in, "output_tokens": out},
	})
	return string(b)
}

func textBlock(s string) map[string]any { return map[string]any{"type": "text", "text": s} }
func thinkingBlock(s string) map[string]any {
	return map[string]any{"type": "thinking", "thinking": s}
}

// ---- Anthropic 适配器 ----

// 正常回包 + thinking 块混排 + usage 映射：text 块按序拼接（thinking 忽略）、
// input→prompt / output→completion / total=input+output；路径 /v1/messages、
// 头 x-api-key + anthropic-version: 2023-06-01、载荷形状（model/system/
// messages[user]/temperature 0.2/max_tokens）。
func TestAnthropicChatTextBlocksUsageHeaders(t *testing.T) {
	srv := newAnthSrv(t, anthBodyFor([]map[string]any{
		thinkingBlock("先想想结构"), textBlock("第一段"), thinkingBlock("再想想"), textBlock("第二段"),
	}, 100, 50))
	pr := Provider{Name: "kimi", BaseURL: srv.srv.URL, Model: "kimi-for-coding", APIKey: "sk-ant"}
	reply, usage, err := AnthropicChat(pr, "sys-prompt", "usr-material", 10, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "第一段第二段" {
		t.Fatalf("reply = %q, want text 块拼接（thinking 忽略）", reply)
	}
	paths, apiKeys, versions, payloads, _ := srv.snap()
	if len(paths) != 1 || paths[0] != "/v1/messages" {
		t.Fatalf("请求路径 = %v, want /v1/messages", paths)
	}
	if apiKeys[0] != "sk-ant" {
		t.Fatalf("x-api-key = %q", apiKeys[0])
	}
	if versions[0] != "2023-06-01" {
		t.Fatalf("anthropic-version = %q, want 2023-06-01", versions[0])
	}
	p := payloads[0]
	if p["model"] != "kimi-for-coding" {
		t.Fatalf("model = %v", p["model"])
	}
	if p["system"] != "sys-prompt" {
		t.Fatalf("system = %v", p["system"])
	}
	msgs, ok := p["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("messages 应为单条 user: %v", p["messages"])
	}
	if m := msgs[0].(map[string]any); m["role"] != "user" || m["content"] != "usr-material" {
		t.Fatalf("messages[0] = %v, want user/usr-material", m)
	}
	if p["temperature"] != 0.2 || p["max_tokens"] != float64(4096) {
		t.Fatalf("temperature/max_tokens = %v/%v", p["temperature"], p["max_tokens"])
	}
	if usage["prompt_tokens"] != float64(100) || usage["completion_tokens"] != float64(50) ||
		usage["total_tokens"] != float64(150) {
		t.Fatalf("usage 映射 = %v, want 100/50/150", usage)
	}
	if wall, ok := usage["wall_s"].(float64); !ok || wall < 0 {
		t.Fatalf("wall_s 缺失或非法: %v", usage["wall_s"])
	}
}

// usage 块缺失 → 三键补 0（与 Chat 同款宽松语义）。

func TestAnthropicChatUsageDefaultsZero(t *testing.T) {
	srv := newAnthSrv(t, `{"content":[{"type":"text","text":"x"}]}`)
	_, usage, err := AnthropicChat(Provider{Name: "a", BaseURL: srv.srv.URL, Model: "m"}, "s", "u", 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
		if usage[k] != float64(0) {
			t.Fatalf("usage[%s] = %v, want 0", k, usage[k])
		}
	}
}

// extra_body 逐键并入：透传键进载荷；与默认键冲突以透传为准（max_tokens/
// temperature 均被覆盖）。

func TestAnthropicChatExtraBodyMergedOverride(t *testing.T) {
	srv := newAnthSrv(t, anthBodyFor([]map[string]any{textBlock("ok")}, 1, 1))
	pr := Provider{Name: "a", BaseURL: srv.srv.URL, Model: "m", Protocol: ProtocolAnthropic,
		ExtraBody: map[string]any{"thinking": map[string]any{"type": "disabled"},
			"max_tokens": int64(128), "temperature": 0.7}}
	if _, _, err := AnthropicChat(pr, "s", "u", 10, 4096); err != nil {
		t.Fatal(err)
	}
	_, _, _, payloads, _ := srv.snap()
	p := payloads[0]
	if p["max_tokens"] != float64(128) {
		t.Fatalf("extra_body.max_tokens 覆盖 = %v, want 128", p["max_tokens"])
	}
	if p["temperature"] != 0.7 {
		t.Fatalf("extra_body.temperature 覆盖 = %v, want 0.7", p["temperature"])
	}
	th, ok := p["thinking"].(map[string]any)
	if !ok || th["type"] != "disabled" {
		t.Fatalf("extra_body.thinking = %v, want {type: disabled}", p["thinking"])
	}
}

// 端点拼接三形：裸 host → /v1/messages；尾 /v1 → /messages（不双 /v1）；
// 已指到 /v1/messages → 原样（cc-switch forwarder 同款防双后缀）。

func TestAnthropicChatEndpointForms(t *testing.T) {
	srv := newAnthSrv(t, anthBodyFor([]map[string]any{textBlock("x")}, 1, 1))
	for _, base := range []string{srv.srv.URL, srv.srv.URL + "/v1", srv.srv.URL + "/v1/messages"} {
		pr := Provider{Name: "a", BaseURL: base, Model: "m"}
		if _, _, err := AnthropicChat(pr, "s", "u", 10, 1); err != nil {
			t.Fatalf("base %q: %v", base, err)
		}
	}
	_, _, _, _, _ = srv.snap()
	paths, _, _, _, calls := srv.snap()
	if calls != 3 {
		t.Fatalf("应 3 次请求, got %d", calls)
	}
	for _, p := range paths {
		if p != "/v1/messages" {
			t.Fatalf("端点 = %q, want /v1/messages（三形归一）", p)
		}
	}
}

// HTTP≥400 → 与 Chat 同款文案 `HTTP <code> from <name>: <body>`。

func TestAnthropicChatHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream boom"))
	}))
	defer srv.Close()
	_, _, err := AnthropicChat(Provider{Name: "anth", BaseURL: srv.URL, Model: "m"}, "s", "u", 10, 1)
	if err == nil || !strings.Contains(err.Error(), "HTTP 502 from anth: upstream boom") {
		t.Fatalf("HTTPError 文案不符: %v", err)
	}
}

// ---- 链执行器：滑落 / 顺序 / 零重试 ----

// 杀掉第 1 级（监听即关）+ 第 2 级回 500 → 第 3 级产出交接；到达序断言
// （被杀级连 HTTP 都不到，500 级先于存活级）；meta 带胜出级身份与顺位号。

func TestChainSessionKilledLevelSlidesInOrder(t *testing.T) {
	order := &callOrder{}
	dead := closedUpstreamURL(t) // 第 1 级：杀掉（拨号连接拒绝）
	bad := newChainUpstream(t, "lvl2", order, chainBeh{status: 500, body: `{"error":"quota"}`})
	ok := newChainUpstream(t, "lvl3", order, chainBeh{status: 200, body: chatBodyFor(chainOKReply)})
	chain := []Provider{
		{Name: "lvl1", BaseURL: dead, Model: "m1"},
		{Name: "lvl2", BaseURL: bad.srv.URL, Model: "m2"},
		{Name: "lvl3", BaseURL: ok.srv.URL, Model: "m3"},
	}
	f := writeSmallCCSession(t, t.TempDir())
	var got []ChainAttempt
	md, meta, err := ChainSession(f, chain, 10, "cc", func(a ChainAttempt) { got = append(got, a) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "<<<INJECT>>>\n注入层：干完了 fb.py\n<<</INJECT>>>") ||
		!strings.Contains(md, "[Ferryman 交接 · 会话 ") {
		t.Fatalf("交接 MD 两层结构缺失:\n%s", md)
	}
	if meta["provider"] != "lvl3" || meta["chain_pos"] != 2 || meta["mode"] != "L1" {
		t.Fatalf("meta = %v/%v/%v, want lvl3/2/L1", meta["provider"], meta["chain_pos"], meta["mode"])
	}
	if len(got) != 3 {
		t.Fatalf("attempts = %d, want 3（逐级一次）", len(got))
	}
	if got[0].Outcome != ChainOutcomeFailed || got[0].Provider != "lvl1" || got[0].Pos != 0 {
		t.Fatalf("attempt[0] = %+v, want lvl1 failed", got[0])
	}
	if got[1].Outcome != ChainOutcomeFailed || got[1].Provider != "lvl2" {
		t.Fatalf("attempt[1] = %+v, want lvl2 failed", got[1])
	}
	if !strings.Contains(got[1].Err, "HTTP 500") {
		t.Fatalf("lvl2 失败原因缺 HTTP 500: %q", got[1].Err)
	}
	if got[2].Outcome != ChainOutcomeFresh || got[2].Provider != "lvl3" || got[2].Pos != 2 {
		t.Fatalf("attempt[2] = %+v, want lvl3 fresh", got[2])
	}
	if got[2].Usage["prompt_tokens"] != float64(10) || got[2].Usage["completion_tokens"] != float64(5) {
		t.Fatalf("fresh attempt usage = %v, want 10/5", got[2].Usage)
	}
	if seq := order.snapshot(); len(seq) != 2 || seq[0] != "lvl2" || seq[1] != "lvl3" {
		t.Fatalf("到达序 = %v, want [lvl2 lvl3]（lvl1 拨号即拒）", seq)
	}
	// 零重试：每级假上游恰被请求一次（滑落即重试）
	if bad.count() != 1 || ok.count() != 1 {
		t.Fatalf("每级应单次尝试: lvl2=%d lvl3=%d", bad.count(), ok.count())
	}
}

// 五类失败各一测：判失败 → 滑落到下一级（下一级合格即产出交接）。

func TestChainSessionFiveFailureClassesSlide(t *testing.T) {
	f := writeSmallCCSession(t, t.TempDir())
	cases := []struct {
		name    string
		bad     func(t *testing.T) string // 返回坏上游 baseURL
		wantSub string                    // 失败原因子串（空=仅判失败）
	}{
		{"连接失败", func(t *testing.T) string {
			return closedUpstreamURL(t)
		}, ""},
		{"HTTP>=400", func(t *testing.T) string {
			srv := newChainUpstream(t, "bad", nil, chainBeh{status: 429, body: `{"error":"rate"}`})
			return srv.srv.URL
		}, "HTTP 429"},
		{"解析失败", func(t *testing.T) string {
			srv := newChainUpstream(t, "bad", nil, chainBeh{status: 200, body: "not-json{{"})
			return srv.srv.URL
		}, ""},
		{"空输出", func(t *testing.T) string {
			srv := newChainUpstream(t, "bad", nil, chainBeh{status: 200, body: chatBodyFor("")})
			return srv.srv.URL
		}, "输出为空"},
		{"结构不合", func(t *testing.T) string {
			srv := newChainUpstream(t, "bad", nil, chainBeh{status: 200, body: chatBodyFor("没有标记层的裸文本")})
			return srv.srv.URL
		}, "结构"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			badURL := tc.bad(t)
			ok := newChainUpstream(t, "ok", nil, chainBeh{status: 200, body: chatBodyFor(chainOKReply)})
			chain := []Provider{
				{Name: "bad", BaseURL: badURL, Model: "mb"},
				{Name: "ok", BaseURL: ok.srv.URL, Model: "mo"},
			}
			var got []ChainAttempt
			md, _, err := ChainSession(f, chain, 10, "cc", func(a ChainAttempt) { got = append(got, a) })
			if err != nil {
				t.Fatalf("%s: 应滑落到 ok 级成功: %v", tc.name, err)
			}
			if !strings.Contains(md, "注入层：干完了 fb.py") {
				t.Fatalf("%s: 交接 MD 缺失", tc.name)
			}
			if len(got) != 2 || got[0].Outcome != ChainOutcomeFailed || got[1].Outcome != ChainOutcomeFresh {
				t.Fatalf("%s: attempts = %+v, want [failed, fresh]", tc.name, got)
			}
			if tc.wantSub != "" && !strings.Contains(got[0].Err, tc.wantSub) {
				t.Fatalf("%s: 失败原因缺 %q: %q", tc.name, tc.wantSub, got[0].Err)
			}
		})
	}
}

// 全链死 → 聚合 error（点名各级）、无产物；attempts 逐级 failed。

func TestChainSessionAllDeadErrors(t *testing.T) {
	f := writeSmallCCSession(t, t.TempDir())
	bad1 := newChainUpstream(t, "b1", nil, chainBeh{status: 500, body: "{}"})
	chain := []Provider{
		{Name: "p1", BaseURL: closedUpstreamURL(t), Model: "m1"},
		{Name: "p2", BaseURL: bad1.srv.URL, Model: "m2"},
	}
	md, meta, err := ChainSession(f, chain, 10, "cc", nil) // onAttempt nil 亦不炸
	if err == nil {
		t.Fatal("全链死应上抛聚合 error")
	}
	for _, want := range []string{"p1", "p2", "皆败"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("聚合 error 缺 %q: %v", want, err)
		}
	}
	if md != "" || meta != nil {
		t.Fatalf("全链死不应有产物: %v", md)
	}
}

// 空链（防御形：装配处保证非空才开链）→ error，不炸。

func TestChainSessionEmptyChainErrors(t *testing.T) {
	f := writeSmallCCSession(t, t.TempDir())
	if _, _, err := ChainSession(f, nil, 10, "cc", nil); err == nil || !strings.Contains(err.Error(), "空链") {
		t.Fatalf("空链应 error: %v", err)
	}
}

// Anthropic 档经链执行器分派：anthropic provider 走 /v1/messages 且产出交接。

func TestChainSessionAnthropicLevelDispatch(t *testing.T) {
	srv := newAnthSrv(t, anthBodyFor([]map[string]any{
		thinkingBlock("想一想"), textBlock(chainOKReply)}, 30, 20))
	chain := []Provider{{Name: "kimi", BaseURL: srv.srv.URL, Model: "k3",
		Protocol: ProtocolAnthropic}}
	f := writeSmallCCSession(t, t.TempDir())
	var got []ChainAttempt
	md, meta, err := ChainSession(f, chain, 10, "cc", func(a ChainAttempt) { got = append(got, a) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "注入层：干完了 fb.py") {
		t.Fatalf("anthropic 级应产出交接:\n%s", md)
	}
	if meta["provider"] != "kimi" || meta["chain_pos"] != 0 {
		t.Fatalf("meta = %v/%v", meta["provider"], meta["chain_pos"])
	}
	paths, _, _, _, calls := srv.snap()
	if calls != 1 || paths[0] != "/v1/messages" {
		t.Fatalf("anthropic 档应走 /v1/messages 单次: %v", paths)
	}
	if len(got) != 1 || got[0].Outcome != ChainOutcomeFresh || got[0].Protocol != ProtocolAnthropic {
		t.Fatalf("attempt = %+v, want fresh anthropic", got)
	}
	if got[0].Usage["prompt_tokens"] != float64(30) || got[0].Usage["completion_tokens"] != float64(20) {
		t.Fatalf("anthropic usage 映射 = %v", got[0].Usage)
	}
}

// ---- 本地级拨号超时 5s 的配置语义 ----

// 默认 5 秒钉死 + 派生规则：仅链首（顺位 0）且 BaseURL 指内网/回环才限时；
// 云端级/非链首一律 0（不限，沿用既有 context 总时限语义）。

func TestChainLocalDialTimeoutSemantics(t *testing.T) {
	if LocalDialTimeoutS != 5.0 {
		t.Fatalf("LocalDialTimeoutS 缺省 = %v, want 5.0", LocalDialTimeoutS)
	}
	local := []string{
		"http://127.0.0.1:8000/v1",
		"http://localhost:8000/v1",
		"http://192.168.1.10:8000/v1",
		"http://10.1.2.3:8000/v1",
		"http://172.16.0.5:8000/v1",
	}
	for _, base := range local {
		if got := ChainDialTimeoutS(0, Provider{BaseURL: base}); got != 5.0 {
			t.Fatalf("本地级（0 顺位 %q）拨号限时 = %v, want 5.0", base, got)
		}
	}
	if got := ChainDialTimeoutS(0, Provider{BaseURL: "https://api.example.com/v1"}); got != 0 {
		t.Fatalf("云端级拨号限时 = %v, want 0（不限）", got)
	}
	if got := ChainDialTimeoutS(0, Provider{BaseURL: "https://bigmodel.cn/v1"}); got != 0 {
		t.Fatalf("公网域名 = %v, want 0", got)
	}
	for _, idx := range []int{1, 2} {
		if got := ChainDialTimeoutS(idx, Provider{BaseURL: "http://192.168.1.10:8000/v1"}); got != 0 {
			t.Fatalf("非链首（%d）不得限时 = %v, want 0", idx, got)
		}
	}
}

// 拨号限时包装的真行为（零网络）：底层拨号阻塞到 ctx 收口 → 按限时提前
// 返回错误；cap<=0 直通底层。

func TestCappedDialContextBoundsDial(t *testing.T) {
	blocked := func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done() // 底层拨号永不完成（挂死上游的确定性替身）
		return nil, ctx.Err()
	}
	dial := cappedDialContext(0.1, blocked)
	start := time.Now()
	_, err := dial(context.Background(), "tcp", "10.255.255.1:81")
	if err == nil {
		t.Fatal("限时到点拨号应报错")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("拨号未按 0.1s 限时收口: %v", el)
	}
	sentinel := fmt.Errorf("底层直通")
	passthrough := cappedDialContext(0, func(context.Context, string, string) (net.Conn, error) {
		return nil, sentinel
	})
	if _, err := passthrough(context.Background(), "tcp", "x"); err != sentinel {
		t.Fatalf("cap<=0 应直通底层: %v", err)
	}
}
