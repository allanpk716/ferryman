// health_balance_test.go — 票07：/stats 面板 glm_balance 展示行三态钉子
// （未配置零 HTTP / 成功报数值 / 失败报类别且不泄漏真钥）。查询按需触发：
// 每次 Health()（面板拉取）至多一次，断言不做后台轮询由实现无定时器保证。
package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ferryman/internal/config"
)

// balancePanelKey 假真钥：断言展示行/错误串不含它（票据验收：无真钥泄漏）。
const balancePanelKey = "sk-panel-real-07"

func TestHealthGLMBalanceNotConfigured(t *testing.T) {
	// config.Default()：Dock=nil → "未配置"（零 HTTP 无从发生——连 URL 都没有）。
	e := newGateEnv(t)
	if got := e.d.Health()["glm_balance"]; got != "未配置" {
		t.Fatalf("glm_balance = %v, want 未配置", got)
	}
}

func TestHealthGLMBalanceZeroHTTPWithoutKey(t *testing.T) {
	// 有 [dock].balance_url 但无 api_key → "未配置"，且计数器证明零 HTTP。
	e := newGateEnv(t)
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()
	e.d.Cfg.Dock = &config.DockCfg{BalanceURL: srv.URL}
	if got := e.d.Health()["glm_balance"]; got != "未配置" {
		t.Fatalf("glm_balance = %v, want 未配置", got)
	}
	if hits.Load() != 0 {
		t.Fatalf("无 api_key 发出 %d 次 HTTP, want 0", hits.Load())
	}
}

func TestHealthGLMBalanceFetchSuccessThenUnauthorized(t *testing.T) {
	// 首拉成功报数值；再拉上游 401 → 类别行；展示行永不携带真钥。
	e := newGateEnv(t)
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer "+balancePanelKey {
			t.Errorf("Authorization = %q, want Bearer <真钥>", got)
		}
		if n == 1 {
			_, _ = w.Write([]byte(`{"data":{"balance":"88.5","usage":{"pcm":1}}}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	e.d.Cfg.Dock = &config.DockCfg{APIKey: balancePanelKey, BalanceURL: srv.URL}

	if got := e.d.Health()["glm_balance"]; got != "88.5" {
		t.Fatalf("glm_balance = %v, want 88.5", got)
	}
	got, ok := e.d.Health()["glm_balance"].(string)
	if !ok {
		t.Fatalf("glm_balance 应为字符串: %v", e.d.Health()["glm_balance"])
	}
	if got != "余额查询失败：上游状态 401" {
		t.Fatalf("glm_balance = %q, want 类别行", got)
	}
	if strings.Contains(got, balancePanelKey) {
		t.Fatalf("展示行泄漏真钥: %q", got)
	}
	if hits.Load() != 2 {
		t.Fatalf("两次拉取应恰好两次查询, got %d", hits.Load())
	}
}
