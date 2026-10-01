package main

// stub.go — 彩排台两个本地桩服务（全部环回、端口从装配注入，零外网）：
//
//   - releaseStub：假 GitHub Release。API 面（releases/latest、releases/tags/
//     <tag>、列表）回最小 ghRelease JSON；下载面回 exe 字节与 .sha256 文本。
//     监督者的 Endpoints（APIBase/DLBase）注入指向这里——升级事务的「新版
//     校验」want 就是这里的 tag_name。
//   - upstreamStub：影子渡口的 active 上游。非流式请求秒回 JSON（间歇流量/
//     1rps 探针）；流式请求按 chunkEvery 吐 Anthropic 形慢速 SSE、全程
//     streamDur、末尾 message_stop（零截断判据的分母）。killStreams() 是
//     「排水中强断」注入面：广播后所有在途流 handler 立即返回，上游连接
//     即刻关闭。

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// releaseStub 假发布端点。
type releaseStub struct {
	srv  *http.Server
	addr string

	mu      sync.Mutex
	tag     string
	exePath string
	sha     string
}

// newReleaseStub 在给定端口起假发布服务（127.0.0.1）。
func newReleaseStub(port int) (*releaseStub, error) {
	s := &releaseStub{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		s.mu.Lock()
		tag, exePath, sha := s.tag, s.exePath, s.sha
		s.mu.Unlock()
		switch {
		case len(p) > 7 && p[len(p)-7:] == ".sha256":
			// sha256sum 产出格式「<hash>␠␠<文件名>」，FetchSHA256 取首字段。
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprintf(w, "%s  ferryman_windows_amd64.exe\n", sha)
		case strings.Contains(p, "/releases/download/"):
			http.ServeFile(w, r, exePath)
		default:
			// releases/latest / releases/tags/<tag> / releases 列表同应答
			//（彩排一律显式 spec=tag，latest/列表只是兜底可达）。
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, "{\"tag_name\":%q,\"draft\":false,\"prerelease\":false}\n", tag)
		}
	})
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	s.addr = ln.Addr().String()
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// setRelease 切换桩发布内容：彩排的 N 次事务在两版之间往复，桩按事务翻
// tag 与资产（tag 必须等于资产 exe 烤进的 main.version——版本校验的谓词）。
func (s *releaseStub) setRelease(tag, exePath, sha string) {
	s.mu.Lock()
	s.tag, s.exePath, s.sha = tag, exePath, sha
	s.mu.Unlock()
}

func (s *releaseStub) close() { _ = s.srv.Close() }

// upstreamStub 桩上游（影子渡口 active 指向它）。
type upstreamStub struct {
	srv        *http.Server
	addr       string
	streamDur  time.Duration // 单条 SSE 流全程（长流场景 >60s）
	chunkEvery time.Duration // 吐 chunk 节奏

	mu     sync.Mutex
	kills  map[int]chan struct{}
	nextID int
}

// newUpstreamStub 在给定端口起桩上游。
func newUpstreamStub(port int, streamDur, chunkEvery time.Duration) (*upstreamStub, error) {
	u := &upstreamStub{streamDur: streamDur, chunkEvery: chunkEvery, kills: map[int]chan struct{}{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", u.handleMessages)
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	u.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	u.addr = ln.Addr().String()
	go func() { _ = u.srv.Serve(ln) }()
	return u, nil
}

func (u *upstreamStub) close() { _ = u.srv.Close() }

// handleMessages /v1/messages：非流式秒回；流式慢速 SSE 至 streamDur。
func (u *upstreamStub) handleMessages(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var probe struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &probe)
	if !probe.Stream {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_rehearsal","type":"message","role":"assistant",` +
			`"content":[{"type":"text","text":"rehearsal-ok"}]}`))
		return
	}
	// 慢速 SSE：无 Content-Length（chunked）——与真上游流式形态一致，
	// 渡口 drainBody 的注入路径只对无声明长度的流生效。
	fl := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_ = fl.Flush()
	kill := u.register()
	defer u.unregister(kill)
	write := func(ev, data string) bool {
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev, data); err != nil {
			return false
		}
		_ = fl.Flush() // SSE 红线：立即冲刷
		return true
	}
	if !write("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":1}}}`) {
		return
	}
	tk := time.NewTicker(u.chunkEvery)
	defer tk.Stop()
	end := time.Now().Add(u.streamDur)
	for i := 0; ; i++ {
		select {
		case <-r.Context().Done(): // 客户端（渡口）先断：正常收尾
			return
		case <-kill: // 强断注入：handler 返回 → 上游连接关闭 → 渡口读断流
			return
		case <-tk.C:
			if !write("content_block_delta",
				fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"chunk-%d"}}`, i)) {
				return
			}
			if !time.Now().Before(end) {
				write("message_stop", `{"type":"message_stop"}`) // 完整流的标志
				return
			}
		}
	}
}

func (u *upstreamStub) register() chan struct{} {
	ch := make(chan struct{})
	u.mu.Lock()
	u.kills[u.nextID] = ch
	u.nextID++
	u.mu.Unlock()
	return ch
}

func (u *upstreamStub) unregister(ch chan struct{}) {
	u.mu.Lock()
	for k, v := range u.kills {
		if v == ch {
			delete(u.kills, k)
		}
	}
	u.mu.Unlock()
}

// killStreams 强断注入：广播全部在途流。排水契约的收尾路径（截断记录/
// 流内错误）由此触发。
func (u *upstreamStub) killStreams() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	for _, ch := range u.kills {
		close(ch)
		n++
	}
	u.kills = map[int]chan struct{}{}
	return n
}
