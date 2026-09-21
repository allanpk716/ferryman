// balance_active_wire_test.go — 票03：/stats 余额行随 active 条目的 HTTP 线面钉子。
//
// 票01 已把余额行接成 ActiveUpstream 单源（health.go glmBalance；d.Health() 层
// 钉子见 dock_active_test.go），票03 盘点确认查看器（internal/viewer + 面板前端
// cmd/ferryman/web）无任何独立余额取数路径。本文件把同一语义钉到 GET /stats
// 线面（makeHandler → writeJSON 序列化后），对齐验收三条：
//   - active=未配 balance_url 的条目（kimi/deepseek 预置同形）→ glm_balance=
//     「未配置」且零外呼（/stats 契约键恒在——query_sessions_test 契约快照钉死；
//     值「未配置」即「无余额行」的线面形态，D11 不配不显示）；
//   - active=智谱（预置含 balance_url 同形——测试把条目 balance_url 指到
//     httptest 假端点）→ 余额行显示，端点与出站钥都取该条目配置值；
//   - 即配即显：改 active 立即生效，无需重启。
package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ferryman/internal/config"
)

func TestStatsWireBalanceFollowsActiveUpstream(t *testing.T) {
	e := newGateEnv(t)
	var hits atomic.Int64
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":{"balance":"3.14"}}`))
	}))
	defer srv.Close()

	e.d.Cfg.Dock = &config.DockCfg{
		Active: "kimi", // 预置 kimi/deepseek 同形：不配 balance_url
		Upstreams: map[string]config.DockUpstream{
			"kimi": {BaseURL: "https://api.kimi.com/coding/", APIKey: "sk-kimi"},
			"智谱":   {BaseURL: "https://open.bigmodel.cn/api/anthropic", APIKey: balancePanelKey, BalanceURL: srv.URL},
		},
	}
	ts := httptest.NewServer(makeHandler(e.d, "tok-wire", nil))
	defer ts.Close()

	get := func() (map[string]any, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/stats", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer tok-wire")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /stats = %d: %s", resp.StatusCode, raw)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("/stats 非合法 JSON: %v", err)
		}
		return m, string(raw)
	}

	// 未配 balance_url 的 active：「未配置」+ 零外呼（无对任何余额端点、
	// 尤其智谱端点的请求），线面亦无真钥泄漏。
	m, raw := get()
	if got := m["glm_balance"]; got != "未配置" {
		t.Fatalf("wire glm_balance = %v, want 未配置", got)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("未配 balance_url 却发出 %d 次 HTTP", n)
	}
	if strings.Contains(raw, balancePanelKey) {
		t.Fatalf("/stats 线面泄漏真钥: %s", raw)
	}

	// 切到配了 balance_url 的条目（智谱预置同形）：余额行显示，端点与出站钥
	// 都是该条目配置值——hits 落在条目 balance_url 指向的假端点上即证。
	e.d.Cfg.Dock.Active = "智谱"
	m, _ = get()
	if got := m["glm_balance"]; got != "3.14" {
		t.Fatalf("wire glm_balance = %v, want 3.14", got)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("hits = %d, want 1（恰好查该条目配置端点一次）", n)
	}
	if gotAuth != "Bearer "+balancePanelKey {
		t.Fatalf("余额查询出站 Authorization = %q, want 该条目真钥", gotAuth)
	}
}
