// dock_upstream_e2e_test.go — 票05：端到端集成验收补钉（只补票面与既有钉子的
// 差集，不改不动既有测试文件）。
//
//   - 双假上游切换（跨 config 重载链路）：票 01 的钉子只看单场装配（active 对
//     旧单值的优先，daemon/dock_active_test.go）；这里补「两条真上游条目 +
//     SetActiveUpstream 写回 active → 重载配置 → 重启渡口实例 → 请求到达新目
//     的地、带新钥、按新条目 model_map 改写，旧目的地零再命中」——即
//     `upstream use` 落盘后守护重启生效的完整链路（CLI 注入面已由
//     cmd/ferryman/upstream_test.go 钉死，此处不重复）。
//   - 档位映射矩阵的接线面：纯函数矩阵在 rewrite_test.go
//     TestRewriteModelMappingSixKeys；这里钉同矩阵经渡口 server 打到真上游的
//     出站形态（opus/sonnet/haiku→各自映射名、[1M] 大小写剥离、真名透传、
//     未知名 default 兜底；每发出站钥均随条目）。
//   - 改写模式 SSE 即时冲刷：透传侧口径钉在 server_test.go
//     TestPassthroughSSEStream；改写侧此前只有事件回流＋记账断言
//     （TestRewriteRecordsUsageRow 读到流尾才算账），本文件用「先发一帧、按
//     住、再发第二帧」的节奏把 FlushInterval:-1 的即时性钉进时间断言——回归
//     成缓冲转发（首帧压到流尾）即败。
//
// 纪律：全部 httptest 假上游（零真实外呼）；一切等待带超时上限。
package dock

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ferryman/internal/config"
)

// e2eBackend 捕获型假上游：累计命中数，锁存最近一次的出站鉴权/Host/体。
// 命中数是「目的地确实换了」的证据（旧目的地零再命中）。
type e2eBackend struct {
	name string
	mu   sync.Mutex
	hits int
	auth string
	host string
	body []byte
}

func (b *e2eBackend) handler(w http.ResponseWriter, r *http.Request) {
	reqBody, _ := io.ReadAll(r.Body)
	b.mu.Lock()
	b.hits++
	b.auth, b.host, b.body = r.Header.Get("Authorization"), r.Host, reqBody
	b.mu.Unlock()
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok-from-" + b.name))
}

func (b *e2eBackend) snapshot() (hits int, auth, host string, body []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.hits, b.auth, b.host, append([]byte(nil), b.body...)
}

// outboundModel 提取上游收到的体里的 model 字段（体非法即 Fatal）。
func outboundModel(t *testing.T, body []byte) string {
	t.Helper()
	var probe struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		t.Fatalf("上游收到的体非法 JSON: %s", body)
	}
	return probe.Model
}

func TestUpstreamSwitchAcrossConfigReload(t *testing.T) {
	// 两条假上游：不同 base_url/密钥/模型名——任何字段串门都可辨。
	alpha, beta := &e2eBackend{name: "alpha"}, &e2eBackend{name: "beta"}
	alphaSrv := httptest.NewServer(http.HandlerFunc(alpha.handler))
	defer alphaSrv.Close()
	betaSrv := httptest.NewServer(http.HandlerFunc(beta.handler))
	defer betaSrv.Close()

	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	src := fmt.Sprintf(`
[dock]
listen = "127.0.0.1:15722"
active = "alpha"

[dock.upstreams.alpha]
base_url = "%s"
api_key = "sk-alpha-key-1111"

[dock.upstreams.alpha.model_map]
claude-opus-5 = "alpha-opus-model"
default = "alpha-default-model"

[dock.upstreams.beta]
base_url = "%s"
api_key = "sk-beta-key-2222"

[dock.upstreams.beta.model_map]
claude-opus-5 = "beta-opus-model"
default = "beta-default-model"
`, alphaSrv.URL, betaSrv.URL)
	if err := os.WriteFile(cfgPath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	// 第一场：active=alpha。Load → ActiveUpstream 解析单源 → NewWithOptions，
	// 与守护 serve 装配同链路。
	cfg, err := config.Load(cfgPath, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	name, up := cfg.Dock.ActiveUpstream()
	if name != "alpha" || up == nil || up.BaseURL != alphaSrv.URL {
		t.Fatalf("active 解析 = %q/%+v, want alpha@%s", name, up, alphaSrv.URL)
	}
	srvA, err := NewWithOptions("127.0.0.1:15722", up.BaseURL, Options{Upstream: up})
	if err != nil {
		t.Fatal(err)
	}
	frontA := frontOf(t, srvA)

	body := []byte(`{"model":"claude-opus-5","max_tokens":8,"metadata":{"session_id":"switch-e2e"},"messages":[]}`)
	resp, err := http.DefaultClient.Do(ccRequest(t, frontA+"/v1/messages", body))
	if err != nil {
		t.Fatal(err)
	}
	respA, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(respA) != "ok-from-alpha" {
		t.Fatalf("第一场响应 = %q, want ok-from-alpha（须达 alpha）", respA)
	}
	aHits, aAuth, aHost, aBody := alpha.snapshot()
	if aHits != 1 {
		t.Fatalf("alpha 命中 = %d, want 1", aHits)
	}
	if aAuth != "Bearer sk-alpha-key-1111" {
		t.Fatalf("第一场出站 Authorization = %q, want alpha 条目真钥", aAuth)
	}
	if m := outboundModel(t, aBody); m != "alpha-opus-model" {
		t.Fatalf("第一场出站 model = %q, want alpha-opus-model（按 active 条目映射）", m)
	}
	if target, _ := url.Parse(alphaSrv.URL); aHost != target.Host {
		t.Fatalf("第一场出站 Host = %q, want %q", aHost, target.Host)
	}
	if bHits, _, _, _ := beta.snapshot(); bHits != 0 {
		t.Fatalf("未切换时 beta 被命中 %d 次, want 0", bHits)
	}

	// 切换：`upstream use` 的落盘单源 SetActiveUpstream（原子写 active，票02）。
	if err := config.SetActiveUpstream(cfgPath, "beta"); err != nil {
		t.Fatalf("SetActiveUpstream: %v", err)
	}

	// 第二场：重启渡口实例模拟守护重启（无热加载）——重载配置重新装配。
	cfg2, err := config.Load(cfgPath, false)
	if err != nil {
		t.Fatalf("切换后 Load: %v", err)
	}
	name2, up2 := cfg2.Dock.ActiveUpstream()
	if name2 != "beta" || up2 == nil || up2.BaseURL != betaSrv.URL {
		t.Fatalf("切换后解析 = %q/%+v, want beta@%s", name2, up2, betaSrv.URL)
	}
	srvB, err := NewWithOptions("127.0.0.1:15722", up2.BaseURL, Options{Upstream: up2})
	if err != nil {
		t.Fatal(err)
	}
	frontB := frontOf(t, srvB)

	resp, err = http.DefaultClient.Do(ccRequest(t, frontB+"/v1/messages", body))
	if err != nil {
		t.Fatal(err)
	}
	respB, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(respB) != "ok-from-beta" {
		t.Fatalf("第二场响应 = %q, want ok-from-beta（须达新目的地 beta）", respB)
	}
	bHits, bAuth, _, bBody := beta.snapshot()
	if bHits != 1 {
		t.Fatalf("切换后 beta 命中 = %d, want 1", bHits)
	}
	if bAuth != "Bearer sk-beta-key-2222" {
		t.Fatalf("切换后出站 Authorization = %q, want beta 条目真钥（换出站密钥）", bAuth)
	}
	if m := outboundModel(t, bBody); m != "beta-opus-model" {
		t.Fatalf("切换后出站 model = %q, want beta-opus-model（新条目映射值）", m)
	}
	// 旧目的地零再命中：整场只许第一场那一次。
	if aHits2, _, _, _ := alpha.snapshot(); aHits2 != 1 {
		t.Fatalf("切换后 alpha 被再命中: hits=%d, want 仍 1", aHits2)
	}
}

func TestUpstreamRewriteTierMappingAtWire(t *testing.T) {
	// 纯函数矩阵（rewrite_test.go TestRewriteModelMappingSixKeys）的接线面：
	// 同一矩阵经渡口 server + httptest 上游验证出站体与出站钥。
	up := &e2eBackend{name: "tiers"}
	backend := httptest.NewServer(http.HandlerFunc(up.handler))
	defer backend.Close()

	entry := &config.DockUpstream{
		BaseURL: backend.URL,
		APIKey:  "sk-tier-entry-key",
		ModelMap: map[string]string{
			"claude-opus-5":    "tier-opus-model",
			"claude-sonnet-5":  "tier-sonnet-model",
			"claude-haiku-4-5": "tier-haiku-model",
			"default":          "tier-default-model",
		},
	}
	srv, err := NewWithOptions("127.0.0.1:15722", backend.URL, Options{Upstream: entry})
	if err != nil {
		t.Fatal(err)
	}
	front := frontOf(t, srv)

	cases := []struct {
		name  string
		model string
		want  string
	}{
		{"opus档位别名_映射值", "claude-opus-5", "tier-opus-model"},
		{"sonnet档位别名_映射值", "claude-sonnet-5", "tier-sonnet-model"},
		{"haiku档位别名_映射值", "claude-haiku-4-5", "tier-haiku-model"},
		{"opus带[1M]大写_剥后映射", "claude-opus-5[1M]", "tier-opus-model"},
		{"haiku带[1m]小写_剥后映射", "claude-haiku-4-5[1m]", "tier-haiku-model"},
		{"真名_值域内透传不映射", "tier-sonnet-model", "tier-sonnet-model"},
		{"未知名_default兜底", "whatever-v9", "tier-default-model"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"model":%q,"max_tokens":8,"metadata":{"session_id":"tier-%d"},"messages":[]}`,
				tc.model, i)
			resp, err := http.DefaultClient.Do(ccRequest(t, front+"/v1/messages", []byte(body)))
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			_, auth, _, gotBody := up.snapshot()
			if auth != "Bearer sk-tier-entry-key" {
				t.Fatalf("出站 Authorization = %q, want 条目真钥（每发都换钥）", auth)
			}
			if m := outboundModel(t, gotBody); m != tc.want {
				t.Fatalf("入站 %q → 出站 model = %q, want %q", tc.model, m, tc.want)
			}
		})
	}
}

func TestRewriteModeSSEFlushesImmediately(t *testing.T) {
	// 改写模式的 SSE 即时冲刷钉子（红线：FlushInterval:-1 不许回归成缓冲）。
	// 节奏：首帧发完即 Flush，按住 hold 不发第二帧。即时冲刷下首帧毫秒级到
	// 达；一旦回归缓冲转发，首帧要压到流尾（≥hold）才见——用 <hold/2 的上限
	// 把两种世界分开（本机回环正常毫秒级，余量 2 倍）。
	const hold = 800 * time.Millisecond
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: message_start\n"+
			`data: {"type":"message_start","frame":1}`+"\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(hold)
		_, _ = io.WriteString(w, "event: message_delta\n"+
			`data: {"type":"message_delta","frame":2}`+"\n\n")
		w.(http.Flusher).Flush()
	}))
	defer backend.Close()

	srv, err := NewWithOptions("127.0.0.1:15722", backend.URL,
		Options{Upstream: rewriteDockUpstream(backend.URL)})
	if err != nil {
		t.Fatal(err)
	}
	front := frontOf(t, srv)

	req := ccRequest(t, front+"/v1/messages",
		[]byte(`{"model":"claude-opus-5","stream":true,"max_tokens":16,"metadata":{"session_id":"sse-flush"},"messages":[]}`))
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// 首 goroutine 只为「首行到达」打点；流尾另行收割（即时 ≠ 截断）。
	type firstRead struct {
		line string
		err  error
	}
	first := make(chan firstRead, 1)
	restDone := make(chan []byte, 1)
	go func() {
		rd := bufio.NewReader(resp.Body)
		ln, err := rd.ReadString('\n')
		first <- firstRead{ln, err}
		rest, _ := io.ReadAll(rd)
		restDone <- rest
	}()
	var got firstRead
	select {
	case got = <-first:
	case <-time.After(3 * time.Second):
		t.Fatal("3s 内未见 SSE 首行（流被缓冲或断流）")
	}
	elapsed := time.Since(start)
	if got.err != nil {
		t.Fatalf("读 SSE 首行失败: %v", got.err)
	}
	if !strings.Contains(got.line, "message_start") {
		t.Fatalf("SSE 首行 = %q, want message_start", got.line)
	}
	if elapsed >= hold/2 {
		t.Fatalf("SSE 首帧 %v 才到达（≥ hold/2=%v）——即时冲刷疑似回归成缓冲转发",
			elapsed, hold/2)
	}
	select {
	case rest := <-restDone:
		if !bytes.Contains(rest, []byte(`"frame":2`)) {
			t.Fatalf("第二帧丢失（即时≠截断）: %q", rest)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("3s 内流未收尾（第二帧丢失或连接悬挂）")
	}
}
