// daemon_test.go — 票01 W1：/stats 渡口统计两字段（dock_inflight /
// last_request_ts）的验收钉子：字段在位（字段名逐字即 API 契约）、渡口未启用
// 0/0 且不报错、管理面流量（/stats 自身与闸门问询）不动字段、真代理流量经
// DockSnap 只读链如实上报（装配同形：serveConfig 的 d.DockSnap = ds.Snapshots()）。
package daemon

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/dock"
)

// TestHealthDockStatsFieldsAndZeroWhenDisabled /stats 含两字段；DockSnap 为 nil
// （无 [dock] 配置 / 渡口构造或绑定失败降级）＝0/0 且不报错；管理面流量
// （连续 /stats 拉取、闸门问询）不得改变两字段。
func TestHealthDockStatsFieldsAndZeroWhenDisabled(t *testing.T) {
	e := newGateEnv(t)
	h := e.d.Health()
	for _, k := range []string{"dock_inflight", "last_request_ts"} {
		if _, ok := h[k]; !ok {
			t.Fatalf("/stats 缺字段 %s: %v", k, h)
		}
	}
	if h["dock_inflight"] != 0 || h["last_request_ts"] != int64(0) {
		t.Fatalf("渡口未启用应 0/0: dock_inflight=%v last_request_ts=%v",
			h["dock_inflight"], h["last_request_ts"])
	}
	e.d.Gate(gateBody("hs-dock0", "C:/no-such-dock0.jsonl", "C:/proj")) // 闸门问询（管理面）
	h = e.d.Health()                                                   // /stats 自身（管理面）
	if h["dock_inflight"] != 0 || h["last_request_ts"] != int64(0) {
		t.Fatalf("管理面流量不得改变两字段: dock_inflight=%v last_request_ts=%v",
			h["dock_inflight"], h["last_request_ts"])
	}
}

// TestHealthDockStatsViaWiredDock 真请求穿过渡口 handler 后两字段如实上报：
// 完成后在途回落 0，last_request_ts 更新为完成时刻（秒级）。
func TestHealthDockStatsViaWiredDock(t *testing.T) {
	e := newGateEnv(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()
	acc, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// listen 只存不用（不 Start，挂 httptest 前端——仓库既有惯例，不占真实端口）。
	srv, err := dock.NewWithOptions("127.0.0.1:15722", backend.URL, dock.Options{Accounts: acc})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(srv)
	defer front.Close()
	e.d.DockSnap = srv.Snapshots() // serve.go 装配同形（DockSnap 只读转交）

	if h := e.d.Health(); h["dock_inflight"] != 0 || h["last_request_ts"] != int64(0) {
		t.Fatalf("无代理流量应 0/0: dock_inflight=%v last_request_ts=%v",
			h["dock_inflight"], h["last_request_ts"])
	}

	body := []byte(`{"model":"m","metadata":{"session_id":"wire"},"messages":[]}`)
	req, err := http.NewRequest(http.MethodPost, front.URL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	resp.Body.Close()

	// 完成收尾序：recordRow 落行 → handler 返回 → 出册记账——行到即轮询等回落。
	deadline := time.Now().Add(2 * time.Second)
	for {
		if rows := acc.Read(accounts.ReadOpts{Kind: "dock"}); len(rows) >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("dock 行未落盘")
		}
		time.Sleep(5 * time.Millisecond)
	}
	h := e.d.Health()
	if h["dock_inflight"] != 0 {
		t.Fatalf("完成后在途应回落 0: %v", h["dock_inflight"])
	}
	ts, ok := h["last_request_ts"].(int64)
	if !ok || ts <= 0 || ts > time.Now().Unix() {
		t.Fatalf("last_request_ts = %v, want 完成时刻（秒级，正数且不超前）", h["last_request_ts"])
	}
}
