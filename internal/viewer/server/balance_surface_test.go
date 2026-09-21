// balance_surface_test.go — 票03：查看器无余额面的钉子。
//
// 盘点结论（票03）：余额展示行只存在于守护 GET /stats 的 glm_balance（票01 起
// 按 active 条目取 balance_url）；查看器（账本只读 API：sessions/timeline/config/
// backtest）与面板前端（cmd/ferryman/web）均无任何余额取数路径——/api/config
// 只是配置文本只读展示（密钥脱敏），不发起余额查询。本钉子防回归：查看器
// API 面不得长出余额端点，余额类路径一律 404。
package server

import (
	"net/http"
	"testing"
)

func TestViewerSurfaceHasNoBalanceEndpoint(t *testing.T) {
	ts := serve(t, t.TempDir())
	for _, p := range []string{"/api/balance", "/balance", "/api/stats", "/stats", "/api/glm_balance"} {
		code, body := call(t, ts, http.MethodGet, p, "")
		if code != http.StatusNotFound {
			t.Fatalf("GET %s = %d %s, want 404（查看器无余额端点）", p, code, body)
		}
	}
}
