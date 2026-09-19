// server.go — 票01：渡口透传 handler（experiments/capture/forwarder.go 的
// 产品化：去捕获落盘，换内存快照）。
//
// 保真口径（F7 实验已证）：ReverseProxy＋Rewrite API（SetURL＋Out.Host=
// target.Host＋FlushInterval:-1）不加 XFF、Host 可控、体逐字节同、逐跳头剥。
// 入站不鉴权（绑 127.0.0.1 已足；auth 类头照抄不校验——占位令牌也是照抄）。
package dock

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// TransportTimeoutS 透传侧超时对齐 CC 的 API_TIMEOUT_MS=50min：只约束"上游
// 首字节（响应头）"的等待，不限制流式体的总时长——不设整体 Timeout 是有意
// 的（会砍 SSE 长连接）。SSE 立即冲刷由 FlushInterval:-1 保证（红线）。
const TransportTimeoutS = 50 * time.Minute

// logger 渡口日志（stderr，独立前缀；本票只记生命周期与异常，无逐请求流水——
// dock 科目流水是后续票）。
var logger = log.New(log.Writer(), "[dock] ", log.LstdFlags|log.Lmsgprefix)

// Server 渡口服务：本机透传中转（CC → 渡口 → 上游）＋内存快照捕获。
// Handler 形（ServeHTTP）可独立挂 httptest；Start/Close 是真实监听生命周期。
type Server struct {
	listen string
	target *url.URL
	store  *SnapshotStore
	proxy  *httputil.ReverseProxy

	srv *http.Server
}

// New 构造渡口。listen 为空或 upstreamBaseURL 不是带 scheme/host 的合法地址
// → error（daemon 侧只告警降级，不拖垮主服务）。
func New(listen, upstreamBaseURL string) (*Server, error) {
	if listen == "" {
		return nil, fmt.Errorf("dock: listen 为空")
	}
	target, err := url.Parse(upstreamBaseURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, fmt.Errorf("dock: 上游地址无效: %q", upstreamBaseURL)
	}
	s := &Server{
		listen: listen,
		target: target,
		store:  NewSnapshotStore(),
	}
	s.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			// SetURL 保留入站路径与 query（join 语义）；Host 显式指上游——
			// 客户端 Host 不外泄、后端看到的就是它自己（F7 断言同款）
			pr.SetURL(target)
			pr.Out.Host = target.Host
		},
		// SSE 必须立即冲刷：不设会缓冲流式响应，下游看成断流/超时 →
		// 客户端重试风暴（forwarder.go 实测教训，红线条款）
		FlushInterval: -1,
		Transport: &http.Transport{
			Proxy:                 nil, // 显式不走环境代理：上游是直连目标
			DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   16, // 默认 2 太小：CC 并发请求会频繁重建连接
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: TransportTimeoutS,
			// 不设整体 Timeout/正文超时：长 SSE 流按需无限流（见常量注释）
		},
	}
	return s, nil
}

// Snapshots 快照库句柄（daemon 侧经 Daemon.DockSnapshot() 转交给票03 心跳）。
func (s *Server) Snapshots() *SnapshotStore { return s.store }

// ServeHTTP 透传入口。仅 /v1/messages POST 读体（读体后 NopCloser 回填＝体
// 字节保真）；其余路径不动体直接进代理（流式、零行为差异）。快照失败不影响
// 转发：捕获在喂给代理之前完成，任何快照侧问题都到不了转发路径。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if ShouldCapture(r.Method, r.URL.Path) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			// 与 forwarder.go 同款：按已读部分继续（该分支只在上游断连等
			// 异常时触达，不为此给正常路径加错误分支）
			logger.Printf("读体失败（按已读部分继续转发）: %v", err)
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		// 空 session_id（缺失/非 JSON）＝Capture 内部跳过计数，不入库
		s.store.Capture(ExtractSessionID(body), body, r.Header)
	}
	s.proxy.ServeHTTP(w, r)
}
