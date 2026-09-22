package dock

// replayguard_test.go — 票03：自产重放不入快照验收钉子（追加雪球防线）。
//
// 覆盖：
//   - 带标记头的 /v1/messages 请求不入快照（主快照不被追加体顶替）、不喂
//     形态漂移；Skipped 计数不动（是标记排除，不是 session 缺失）；
//   - 真流量（无标记头）照常捕获；
//   - 重放请求照常落 dock 科目（传输视图完整：token 四列/延迟/状态）；
//   - 改写模式出站剥离标记头（不泄漏上游）。

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ferryman/internal/accounts"
)

// replayBody 比主快照大的追加形态体（雪球构造的复现形状）。
func replayBody(sessionID string) []byte {
	return []byte(`{"model":"glm-5.3","max_tokens":4096,` +
		`"messages":[{"role":"user","content":"q1"},{"role":"assistant","content":"a1"},` +
		`{"role":"user","content":"【摆渡指令】写交接"}],` +
		`"metadata":{"session_id":"` + sessionID + `"}}`)
}

func TestReplayMarkerExcludedFromCapture(t *testing.T) {
	var up upstreamEcho
	backend := httptest.NewServer(http.HandlerFunc(up.handler))
	defer backend.Close()
	dockSrv, front := newDockFront(t, backend.URL)

	real := []byte(`{"model":"glm-5.3","max_tokens":32000,` +
		`"messages":[{"role":"user","content":"q1"}],` +
		`"metadata":{"session_id":"sess-replay"}}`)
	resp, err := http.DefaultClient.Do(ccRequest(t, front.URL+"/v1/messages", real))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	main, ok := dockSrv.Snapshots().Main("sess-replay")
	if !ok {
		t.Fatal("真流量应照常捕获")
	}

	// 追加重放（体更大、带标记头）：不得顶替主快照。
	req := ccRequest(t, front.URL+"/v1/messages", replayBody("sess-replay"))
	req.Header.Set("X-Ferryman-Replay", "same_model")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	main2, ok := dockSrv.Snapshots().Main("sess-replay")
	if !ok || string(main2.Body) != string(main.Body) {
		t.Fatalf("带标记头的重放不得改动主快照（追加雪球防线）: ok=%v", ok)
	}
	if dockSrv.Snapshots().Skipped() != 0 {
		t.Fatalf("标记排除不是 session 缺失, Skipped() = %d, want 0", dockSrv.Snapshots().Skipped())
	}
	// 重放请求本身仍被转发（与真流量同路径、体保真）。
	if _, _, lastBody := up.snapshot(); string(lastBody) != string(replayBody("sess-replay")) {
		t.Fatal("重放请求未被保真转发")
	}
}

func TestReplayStillBooksDockRow(t *testing.T) {
	var up upstreamEcho
	backend := httptest.NewServer(http.HandlerFunc(up.handler))
	defer backend.Close()
	acc, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewWithOptions("127.0.0.1:15722", backend.URL,
		Options{Accounts: acc})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(srv)
	t.Cleanup(front.Close)

	req := ccRequest(t, front.URL+"/v1/messages", replayBody("sess-row"))
	req.Header.Set("X-Ferryman-Replay", "same_model")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// recordRow 在 handler 收尾落账，与客户端 Do() 返回存在毫秒级竞态——
	// 有界轮询（≤3s；长测试须有终结时间的仓库纪律）。
	deadline := time.Now().Add(3 * time.Second)
	found := false
	for time.Now().Before(deadline) {
		for _, r := range acc.Read(accounts.ReadOpts{}) {
			if r["kind"] == "dock" && r["session_id"] == "sess-row" {
				found = true
			}
		}
		if found {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !found {
		t.Fatal("重放请求应照常落 dock 科目（传输视图完整）")
	}
	if _, ok := srv.Snapshots().Main("sess-row"); ok {
		t.Fatal("重放不入快照")
	}
}

func TestOutboundStripsReplayMarker(t *testing.T) {
	h := http.Header{}
	h.Set("X-Ferryman-Replay", "same_model")
	h.Set("Authorization", "Bearer anything")
	sanitizeOutboundHeaders(h, "real-key")
	if h.Get("X-Ferryman-Replay") != "" {
		t.Fatal("改写模式出站须剥离重放标记头（不泄漏上游）")
	}
	if h.Get("Authorization") != "Bearer real-key" {
		t.Fatalf("Authorization = %q, want 真钥替换", h.Get("Authorization"))
	}
}

func TestIsReplayRequest(t *testing.T) {
	h := http.Header{}
	if isReplayRequest(h) {
		t.Fatal("无标记头 = 真流量")
	}
	h.Set("X-Ferryman-Replay", "same_model")
	if !isReplayRequest(h) {
		t.Fatal("标记头在场 = 自产重放")
	}
	// 占位令牌不构成标记（CC 真流量经 cc-switch 接管时本身带 PROXY_MANAGED，
	// 2026-09-18 调研实证——不能用它识别重放）。
	h2 := http.Header{}
	h2.Set("Authorization", "Bearer "+placeholderAuthLiteral)
	h2.Set("X-Api-Key", placeholderAuthLiteral)
	if isReplayRequest(h2) {
		t.Fatal("占位令牌不是重放标记")
	}
}

// placeholderAuthLiteral 占位令牌字面量（与 beat 发送器同值；dock 侧不引用
// beat 包——字面量在此复刻并以上方测试钉死其不可用作标记）。
const placeholderAuthLiteral = "PROXY_MANAGED"
