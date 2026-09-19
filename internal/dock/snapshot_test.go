// snapshot_test.go — 票01：内存快照库验收钉子。
// 覆盖票面四条：大会话后小请求主快照仍是大体（F3 largest-wins）；Pin 住塞满
// 16+ 非 pinned 不被淘汰；Unpin 后可被 LRU 淘汰；头集白名单（auth 类不入）。
package dock

import (
	"bytes"
	"fmt"
	"net/http"
	"reflect"
	"testing"
)

// sizedBody 生成 n 字节、每字节 = seed 的互不相同的请求体。
func sizedBody(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = seed
	}
	return b
}

// hdr 便捷构造（键按 http 规范大小写传入）。
func hdr(pairs ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		h.Set(pairs[i], pairs[i+1])
	}
	return h
}

// fullHdr 模拟 CC 真实发送的全套头（白名单 6 项 + auth 类 + 杂项）。
func fullHdr() http.Header {
	return hdr(
		"Content-Type", "application/json",
		"Anthropic-Version", "2023-06-01",
		"Anthropic-Beta", "claude-code-20250219,oauth-2025-04-20",
		"User-Agent", "claude-cli/2.0.0 (external)",
		"Accept", "application/json",
		"Accept-Encoding", "gzip, deflate",
		"Authorization", "Bearer sk-ant-secret-placeholder",
		"X-Api-Key", "sk-ant-api03-placeholder",
		"X-Stainless-Retry-Count", "0",
	)
}

func TestCaptureMainLargestWinsLastLatest(t *testing.T) {
	store := NewSnapshotStore()
	store.Capture("s", sizedBody(100, 1), fullHdr())
	store.Capture("s", sizedBody(50, 2), hdr("Content-Type", "text/plain"))
	store.Capture("s", sizedBody(80, 3), hdr("Content-Type", "application/xml"))

	main, ok := store.Main("s")
	if !ok {
		t.Fatal("Main(s) 不存在, want 存在")
	}
	if len(main.Body) != 100 || main.Body[0] != 1 {
		t.Fatalf("主快照体 = %d 字节 seed=%d, want 100 字节 seed=1（最大体）", len(main.Body), main.Body[0])
	}
	// 主快照的头集 = 最大那次请求的头集（大体与小请求的头一并保留）
	if main.Headers["content-type"] != "application/json" {
		t.Fatalf("主快照 content-type = %q, want application/json（跟随最大体那次请求）",
			main.Headers["content-type"])
	}

	last, ok := store.Last("s")
	if !ok {
		t.Fatal("Last(s) 不存在, want 存在")
	}
	if len(last.Body) != 80 || last.Body[0] != 3 {
		t.Fatalf("最后一份体 = %d 字节 seed=%d, want 80 字节 seed=3（最新）", len(last.Body), last.Body[0])
	}
}

func TestCaptureEqualSizeKeepsFirstMax(t *testing.T) {
	// 同尺寸不打擂：首见最大体保留（心跳重放体≈主快照体长，严格大于避免来回覆盖）
	store := NewSnapshotStore()
	store.Capture("s", sizedBody(100, 1), hdr("Content-Type", "application/json"))
	store.Capture("s", sizedBody(100, 2), hdr("Content-Type", "application/xml"))
	main, _ := store.Main("s")
	if main.Body[0] != 1 || main.Headers["content-type"] != "application/json" {
		t.Fatalf("同尺寸第二份覆盖了主快照: seed=%d ct=%q, want 首见保留", main.Body[0], main.Headers["content-type"])
	}
}

func TestCaptureHeadersWhitelistNoAuth(t *testing.T) {
	store := NewSnapshotStore()
	store.Capture("s", sizedBody(10, 1), fullHdr())
	main, _ := store.Main("s")
	want := map[string]string{
		"content-type":      "application/json",
		"anthropic-version": "2023-06-01",
		"anthropic-beta":    "claude-code-20250219,oauth-2025-04-20",
		"user-agent":        "claude-cli/2.0.0 (external)",
		"accept":            "application/json",
		"accept-encoding":   "gzip, deflate",
	}
	if !reflect.DeepEqual(main.Headers, want) {
		t.Fatalf("快照头集 = %v\nwant = %v\n（键须小写、白名单外（authorization/x-api-key/追踪类）一律不入）",
			main.Headers, want)
	}
}

func TestCaptureMissingHeaderOmitted(t *testing.T) {
	store := NewSnapshotStore()
	store.Capture("s", sizedBody(10, 1), hdr("Content-Type", "application/json"))
	main, _ := store.Main("s")
	if len(main.Headers) != 1 || main.Headers["content-type"] != "application/json" {
		t.Fatalf("快照头集 = %v, want 仅含 content-type（缺失头不入集）", main.Headers)
	}
}

func TestCaptureOwnsBodyBytes(t *testing.T) {
	// 快照必须拥有体字节：调用方事后改写原切片不得影响快照
	store := NewSnapshotStore()
	b := []byte(`{"metadata":{"session_id":"s"}}`)
	store.Capture("s", b, hdr("Content-Type", "application/json"))
	b[0] = 'X'
	main, _ := store.Main("s")
	if main.Body[0] != '{' {
		t.Fatalf("快照体被调用方改写污染: %q", main.Body[:8])
	}
}

func TestCaptureEmptySessionIDCountsSkipped(t *testing.T) {
	// session_id 缺失/提取失败：不入快照只记计数（透传不受影响由 server 测试覆盖）
	store := NewSnapshotStore()
	store.Capture("", sizedBody(10, 1), fullHdr())
	store.Capture("", sizedBody(10, 2), fullHdr())
	if got := store.Skipped(); got != 2 {
		t.Fatalf("Skipped() = %d, want 2", got)
	}
	if _, ok := store.Main(""); ok {
		t.Fatal("空 session_id 不得入库")
	}
}

func TestLRUOver16NonPinnedEvictsOldest(t *testing.T) {
	store := NewSnapshotStore()
	for i := 0; i < 16; i++ {
		store.Capture(fmt.Sprintf("s%02d", i), sizedBody(10, byte(i)), fullHdr())
	}
	// s00 再捕获（recency 刷新）→ 淘汰对象变为最旧的 s01
	store.Capture("s00", sizedBody(10, 0), fullHdr())
	store.Capture("s16", sizedBody(10, 16), fullHdr()) // 第 17 个非 pinned → 挤掉 LRU
	if _, ok := store.Main("s01"); ok {
		t.Fatal("s01 应被 LRU 淘汰")
	}
	if _, ok := store.Main("s00"); !ok {
		t.Fatal("s00 刚捕获过（recency 新）不应被淘汰")
	}
	if _, ok := store.Main("s16"); !ok {
		t.Fatal("s16 不应被淘汰")
	}
}

func TestPinProtectsFromEvictionAndUnpinEvictable(t *testing.T) {
	store := NewSnapshotStore()
	store.Capture("S", sizedBody(500, 9), fullHdr()) // 大会话
	store.Pin("S")
	// 塞满 16+ 非 pinned：17 个 A 会话 → LRU 挤掉 A00，pinned 的 S 必须幸存
	for i := 0; i < 17; i++ {
		store.Capture(fmt.Sprintf("A%02d", i), sizedBody(10, byte(i)), fullHdr())
	}
	if _, ok := store.Main("A00"); ok {
		t.Fatal("A00 应被 LRU 淘汰（超 16 非 pinned 上限）")
	}
	if _, ok := store.Main("S"); !ok {
		t.Fatal("pinned 会话 S 被淘汰——违反 Pinned 不变式")
	}
	// Unpin 后（对应窗口收尾＋最后一跳结算）才可清理：先确认 unpin 即刻还在，
	// 再老化出局
	store.Unpin("S")
	if _, ok := store.Main("S"); !ok {
		t.Fatal("Unpin 后立即消失：unpin 时刻 recency 应为最新，不该当场被挤")
	}
	for i := 0; i < 16; i++ { // 16 个新会话把 S 挤成 LRU
		store.Capture(fmt.Sprintf("B%02d", i), sizedBody(10, byte(i+32)), fullHdr())
	}
	if _, ok := store.Main("S"); ok {
		t.Fatal("Unpin 且老化后 S 应可被 LRU 淘汰")
	}
	if _, ok := store.Main("B00"); !ok {
		t.Fatal("B00 不应被淘汰（S 比 B 系更旧）")
	}
}

func TestPinUnknownSessionPlaceholderSurvives(t *testing.T) {
	// 票03 场景：窗先开（Pin）请求后到——占位 pinned 记录不得被淘汰挤掉
	store := NewSnapshotStore()
	store.Pin("ghost")
	if _, ok := store.Main("ghost"); ok {
		t.Fatal("占位无数据：Main 应返回 false")
	}
	for i := 0; i < 20; i++ { // 远超 16 上限的淘汰压力
		store.Capture(fmt.Sprintf("s%02d", i), sizedBody(10, byte(i)), fullHdr())
	}
	store.Capture("ghost", sizedBody(200, 7), fullHdr())
	main, ok := store.Main("ghost")
	if !ok || len(main.Body) != 200 {
		t.Fatalf("占位 pinned 会话在淘汰压力后捕获失败: ok=%v len=%d", ok, len(main.Body))
	}
}

func TestUnpinUnknownSessionNoop(t *testing.T) {
	store := NewSnapshotStore()
	store.Unpin("nope") // 不得 panic / 建占位
	if _, ok := store.Main("nope"); ok {
		t.Fatal("Unpin 未知会话不得建占位记录")
	}
}

func TestShouldCapturePathFilter(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodPost, "/v1/messages", true},
		{http.MethodPost, "/v1/messages/count_tokens", false}, // 计数探针不入快照
		{http.MethodPost, "/api/v1/messages", true},           // 带前缀挂载兼容
		{http.MethodPost, "/api/v1/messages/count_tokens", false},
		{http.MethodGet, "/v1/messages", false}, // 仅 POST
		{http.MethodPost, "/v1/chat/completions", false},
		{http.MethodPost, "/v1/messages/", false}, // 尾斜杠非 messages 精确路径
		{http.MethodPost, "/user/balance", false},
	}
	for _, c := range cases {
		if got := ShouldCapture(c.method, c.path); got != c.want {
			t.Errorf("ShouldCapture(%s, %s) = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

func TestExtractSessionID(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"正常提取", `{"model":"m","metadata":{"session_id":"abc-123"}}`, "abc-123"},
		{"无 metadata", `{"model":"m"}`, ""},
		{"metadata 空", `{"metadata":{}}`, ""},
		{"非 JSON", `not-json`, ""},
		{"空体", ``, ""},
	}
	for _, c := range cases {
		if got := ExtractSessionID([]byte(c.body)); got != c.want {
			t.Errorf("%s: ExtractSessionID = %q, want %q", c.name, got, c.want)
		}
	}
	if got := ExtractSessionID(bytes.Repeat([]byte("x"), 1<<20)); got != "" {
		t.Errorf("大非 JSON 体应返回空, got %q", got)
	}
}
