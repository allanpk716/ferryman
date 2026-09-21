// dock_upstream_wire_test.go — 票01：渡口按渡口上游条目（Options.Upstream）
// 装配的验收钉子。D13/D15：rewrite 隐含开启——装配不再有开关；本地中转地址
// 由守卫强制退透传（不改写、不换钥、头零处理），非本地上游放行改写＋真钥替换。
package dock

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"ferryman/internal/config"
)

// upstreamEntry 放行形态的渡口上游条目（base_url 由调用方给）。
func upstreamEntry(baseURL string) *config.DockUpstream {
	return &config.DockUpstream{
		BaseURL: baseURL,
		APIKey:  "sk-real-key",
		ModelMap: map[string]string{
			"claude-opus-5": "glm-5.5",
			"default":       "glm-4.7-flash",
		},
		TextOnly: []string{"glm-5.3-air"},
	}
}

func TestUpstreamImplicitRewriteLocalRelayForcesPassthrough(t *testing.T) {
	// 上游＝本地中转（cc-switch 回退条目形态）：守卫强制透传——构造期即判
	// rewriteOn=false（不改写、不换钥、头零处理；透传语义由
	// TestRewriteGuardBlocksLocalRelayAtServer 与 daemon 端到端钉子共同看住）。
	// 旧 rewrite_enabled 开关不复存在：隐含请求改写也被守卫拦下。
	srv, err := NewWithOptions("127.0.0.1:15722", "http://127.0.0.1:15721",
		Options{Upstream: upstreamEntry("http://127.0.0.1:15721")})
	if err != nil {
		t.Fatal(err)
	}
	if srv.rewriteOn {
		t.Fatal("上游为本地中转地址：隐含改写须被守卫退回透传")
	}
	if srv.apiKey != "" {
		t.Fatal("透传域不得注入条目真钥（入站认证照抄）")
	}
}

func TestUpstreamImplicitRewriteNonLocalRewritesAndRekeys(t *testing.T) {
	// 上游＝真供应商（非本地）：隐含开启改写——模型映射生效、出站真钥替换、
	// 出站头卫生生效。Options.Upstream 是改写值与真钥的唯一来源。
	var up upstreamEcho
	backend := httptest.NewServer(http.HandlerFunc(up.handler))
	defer backend.Close()

	srv, err := NewWithOptions("127.0.0.1:15722", backend.URL,
		Options{Upstream: upstreamEntry(backend.URL)})
	if err != nil {
		t.Fatal(err)
	}
	if !srv.rewriteOn {
		t.Fatal("非本地上游：隐含改写应放行")
	}
	front := frontOf(t, srv)

	body := []byte(`{"model":"claude-opus-5","metadata":{"session_id":"s-up"},"messages":[]}`)
	resp, err := http.DefaultClient.Do(ccRequest(t, front+"/v1/messages", body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	gotHdr, _, gotBody := up.snapshot()
	if gotHdr.Get("Authorization") != "Bearer sk-real-key" {
		t.Fatalf("出站 Authorization = %q, want 条目真钥", gotHdr.Get("Authorization"))
	}
	if !bytes.Contains(gotBody, []byte(`"model":"glm-5.5"`)) {
		t.Fatalf("模型未按条目 model_map 改写: %s", gotBody)
	}
}

func TestUpstreamMissingDefaultNonLocalStaysPassthrough(t *testing.T) {
	// 防御一致性：非本地上游但条目缺 default（配置层已拒；构造期兜底）→ 透传。
	var up upstreamEcho
	backend := httptest.NewServer(http.HandlerFunc(up.handler))
	defer backend.Close()

	entry := upstreamEntry(backend.URL)
	entry.ModelMap = map[string]string{"claude-opus-5": "glm-5.5"}
	srv, err := NewWithOptions("127.0.0.1:15722", backend.URL, Options{Upstream: entry})
	if err != nil {
		t.Fatal(err)
	}
	if srv.rewriteOn {
		t.Fatal("缺 default 须退回透传（守卫单源）")
	}
}
