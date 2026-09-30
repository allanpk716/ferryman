// server_switch_test.go — 票02（供应商接管，F4）：渡口逐请求活跃上游 seam
// （Options.Resolver）的验收钉子。
//
// 验收口径（票 02）：
//   - 切换时在途请求持有既有上游连接自然跑完（目标/真钥/模型映射=旧条目整套视图）；
//   - 下一请求即刻新上游（新条目整套视图：目标+真钥+model 改写同步换）；
//   - resolver nil＝构造期固化既有行为零变化；resolver 悬空＝回落构造期快照；
//   - 并发切换下无撕裂（每个请求的 目标/钥/模型映射 来自同一条目）。
package dock

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ferryman/internal/config"
)

// stubResolver UpstreamResolver 测试替身：原子换当前表视图（daemon 生产
// 持有者的同款并发形状）。
type stubResolver struct {
	cur atomic.Pointer[config.DockCfg]
}

func (s *stubResolver) ActiveUpstream() (string, *config.DockUpstream) {
	if d := s.cur.Load(); d != nil {
		return d.ActiveUpstream()
	}
	return "", nil
}

func (s *stubResolver) store(active string, ups map[string]config.DockUpstream) {
	s.cur.Store(&config.DockCfg{Active: active, Upstreams: ups})
}

// switchUpstream 假上游：记录观测（Authorization+model），A 可阻塞（在途
// 边界用），按身份回显 "origin|auth|model"（撕裂检测：三者必须同源）。
type switchUpstream struct {
	srv   *httptest.Server
	obs   chan string // 每请求一条 "auth|model"
	block chan struct{} // 非 nil＝收到请求后阻塞至放行（在途边界用）
	label string
}

func newSwitchUpstream(t *testing.T, label string, blocking bool) *switchUpstream {
	t.Helper()
	u := &switchUpstream{obs: make(chan string, 64), label: label}
	if blocking {
		u.block = make(chan struct{})
	}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		model := ""
		if i := strings.Index(string(b), `"model":"`); i >= 0 {
			rest := string(b[i+len(`"model":"`):])
			model = rest[:strings.Index(rest, `"`)]
		}
		auth := r.Header.Get("Authorization")
		select {
		case u.obs <- auth + "|" + model:
		default: // 并发压测下观测不落盘也可：回显体已带同源三要素
		}
		if u.block != nil {
			<-u.block // 在途边界：压住响应直到测试放行
		}
		_, _ = w.Write([]byte(label + "|" + auth + "|" + model))
	}))
	t.Cleanup(u.srv.Close)
	return u
}

// switchEntry 造上游表条目（非本地 httptest 地址→改写模式开）。
func switchEntry(baseURL, apiKey, defModel string) config.DockUpstream {
	return config.DockUpstream{
		BaseURL:  baseURL,
		APIKey:   apiKey,
		ModelMap: map[string]string{"default": defModel},
	}
}

func switchPost(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json",
		strings.NewReader(`{"model":"claude-x","metadata":{"session_id":"sw"},"messages":[]}`))
	if err != nil {
		return 0, "ERR:" + err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestResolverSwitchInflightHoldsOldUpstream F4 边界主线：在途请求旧上游跑完
// （旧目标+旧真钥+旧模型映射），下一请求即刻新上游（新条目整套视图）。
func TestResolverSwitchInflightHoldsOldUpstream(t *testing.T) {
	upA := newSwitchUpstream(t, "A", true) // A 阻塞：制造在途窗口
	upB := newSwitchUpstream(t, "B", false)

	res := &stubResolver{}
	res.store("a", map[string]config.DockUpstream{
		"a": switchEntry(upA.srv.URL, "kA", "glm-a"),
		"b": switchEntry(upB.srv.URL, "kB", "glm-b"),
	})
	srv, err := NewWithOptions("127.0.0.1:15722", upA.srv.URL,
		Options{Upstream: &config.DockUpstream{
			BaseURL: upA.srv.URL, APIKey: "kA",
			ModelMap: map[string]string{"default": "glm-a"},
		}, Resolver: res})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// 请求 1 → A（阻塞中＝在途）
	type res1 struct {
		code int
		body string
	}
	got1 := make(chan res1, 1)
	go func() { c, b := switchPost(t, ts.URL+"/v1/messages"); got1 <- res1{c, b} }()

	// 等 A 真收到请求（在途确立）
	var obsA string
	select {
	case obsA = <-upA.obs:
	case <-time.After(5 * time.Second):
		t.Fatal("请求 1 未到达上游 A")
	}
	if obsA != "Bearer kA|glm-a" {
		t.Fatalf("A 收到 auth|model = %q, want Bearer kA|glm-a", obsA)
	}

	// 切换（此刻请求 1 仍在途）
	res.store("b", res.cur.Load().Upstreams)

	// 请求 2 必须即刻走 B（不等 A 放行）
	done2 := make(chan string, 1)
	go func() { _, b := switchPost(t, ts.URL+"/v1/messages"); done2 <- b }()
	var body2 string
	select {
	case body2 = <-done2:
	case <-time.After(5 * time.Second):
		t.Fatal("切换后新请求未在时限内经新上游返回（A 仍阻塞，B 应即刻可用）")
	}
	if body2 != "B|Bearer kB|glm-b" {
		t.Fatalf("请求 2 回显 = %q, want B|Bearer kB|glm-b（新条目整套视图）", body2)
	}

	// 放行 A：请求 1 以旧上游整套视图跑完
	close(upA.block)
	select {
	case r1 := <-got1:
		if r1.code != http.StatusOK || r1.body != "A|Bearer kA|glm-a" {
			t.Fatalf("在途请求收尾 = %d %q, want 200 A|Bearer kA|glm-a（旧上游跑完）", r1.code, r1.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("在途请求未在放行后跑完")
	}
}

// TestResolverNilKeepsConstructionUpstream seam 关闭态：resolver nil＝构造期
// 固化（票01 既有行为），切表动作不存在——两次请求同一构造上游。
func TestResolverNilKeepsConstructionUpstream(t *testing.T) {
	upA := newSwitchUpstream(t, "A", false)
	upB := newSwitchUpstream(t, "B", false)
	_ = upB // 本用例不切：仅证明 nil resolver 不引入任何逐请求解析路径

	srv, err := NewWithOptions("127.0.0.1:15722", upA.srv.URL, Options{Upstream: &config.DockUpstream{
		BaseURL: upA.srv.URL, APIKey: "kA",
		ModelMap: map[string]string{"default": "glm-a"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	for i := 0; i < 2; i++ {
		code, body := switchPost(t, ts.URL+"/v1/messages")
		if code != 200 || body != "A|Bearer kA|glm-a" {
			t.Fatalf("第 %d 请求 = %d %q, want 200 A|Bearer kA|glm-a（构造期固化）", i+1, code, body)
		}
	}
}

// TestResolverDanglingFallsBackToConstruction resolver 悬空（表在但 active
// 悬空→nil）：回落构造期快照，绝不半改写、绝不 5xx。
func TestResolverDanglingFallsBackToConstruction(t *testing.T) {
	upA := newSwitchUpstream(t, "A", false)

	res := &stubResolver{}
	res.store("a", map[string]config.DockUpstream{
		"a": switchEntry(upA.srv.URL, "kA", "glm-a"),
	})
	srv, err := NewWithOptions("127.0.0.1:15722", upA.srv.URL, Options{Upstream: &config.DockUpstream{
		BaseURL: upA.srv.URL, APIKey: "kA",
		ModelMap: map[string]string{"default": "glm-a"},
	}, Resolver: res})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// 悬空：active 指向不存在条目（Validate 拒启的防御路径）
	res.store("missing", res.cur.Load().Upstreams)
	code, body := switchPost(t, ts.URL+"/v1/messages")
	if code != 200 || body != "A|Bearer kA|glm-a" {
		t.Fatalf("悬空解析 = %d %q, want 回落构造期上游 200 A|Bearer kA|glm-a", code, body)
	}
}

// TestResolverConcurrentSwitchNoTornView 并发切换压测：每请求的
// 目标/真钥/模型映射必须同条目（回显三要素同源），无撕裂无失败。
func TestResolverConcurrentSwitchNoTornView(t *testing.T) {
	upA := newSwitchUpstream(t, "A", false)
	upB := newSwitchUpstream(t, "B", false)

	res := &stubResolver{}
	table := map[string]config.DockUpstream{
		"a": switchEntry(upA.srv.URL, "kA", "glm-a"),
		"b": switchEntry(upB.srv.URL, "kB", "glm-b"),
	}
	res.store("a", table)
	entryA := table["a"]
	srv, err := NewWithOptions("127.0.0.1:15722", upA.srv.URL, Options{Upstream: &entryA, Resolver: res})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// 切换者：毫秒级往返切换
	stop := make(chan struct{})
	var sw sync.WaitGroup
	sw.Add(1)
	go func() {
		defer sw.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				res.store("b", table)
			} else {
				res.store("a", table)
			}
			time.Sleep(time.Millisecond)
		}
	}()

	// 请求侧：4 路 × 25 请求
	var wg sync.WaitGroup
	var bad atomic.Int64
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				code, body := switchPost(t, ts.URL+"/v1/messages")
				if code != 200 {
					bad.Add(1)
					continue
				}
				// 三要素同源断言：A 路＝kA+glm-a；B 路＝kB+glm-b
				if body != "A|Bearer kA|glm-a" && body != "B|Bearer kB|glm-b" {
					bad.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	sw.Wait()
	if n := bad.Load(); n != 0 {
		t.Fatalf("%d 个请求撕裂/失败（视图必须整条目原子换）", n)
	}
}
