// gate.go — 票02：响应体流上的两件机构（错误契约热修 3＋排水的流内收尾）。
//
// 首包闸门：仅流式记账请求（/v1/messages POST 且体 stream:true——请求体在
// ServeHTTP 已读，判定从记账元数据来；纯透传不读体不闸，daemon 生产接线恒
// 记账）。上游 200 响应头到达后扣住不还给反向代理，等首个流字节到达才放行
// （首字节随体续传，不额外缓冲）：上游"收了理但一言不发"的挂死在 60 秒内以
// 可重试错误（504＋错误体＋Retry-After）了结，而不是让 CC 挂到天荒地老。
// 非流式不闸（慢生成是合法行为，避免误杀）；上游非 200 不闸（响应保真）。
//
// drainBody：已放行的流在排水到期时的收尾通道——把合成的 Anthropic 流内
// 错误事件（sseErrorEvent）注入转发体，注入完毕转 EOF，让反向代理的拷贝
// 循环把错误事件写给 CC 后正常收尾。对比从 ResponseWriter 外侧硬写：经体
// 内注入与拷贝循环天然串行（同一 goroutine 顺次 Read/Write），无并发写竞争。
package dock

import (
	"io"
	"net/http"
	"time"
)

// firstByteGateTimeout 首包静默判失败的阈值（spec：60 秒）。包级 var 供测试
// 注入短值——别在测试里真等 60 秒。
var firstByteGateTimeout = 60 * time.Second

// gateFirstByte 闸门主体（metaTransport.RoundTrip 在 200 且流式时调用，
// 阻塞至首字节/超时/排水三者其一）。body 为已包好 usageBody 的响应体——
// 预读的首字节在读取时已喂过 usage 扫描器，续传不重喂。
//
// 放行＝返回响应（Body 换成 drainBody，首字节挂前缀）；超时/排水＝返回
// 哨兵错误（ErrorHandler 回 504＋错误体），并清 meta.status 使记账行以
// 客户端实收为准。
func (s *Server) gateFirstByte(meta *reqMeta, resp *http.Response, body io.ReadCloser) (*http.Response, error) {
	resp.Body = body
	first := make([]byte, 1)
	type readRes struct {
		n   int
		err error
	}
	ch := make(chan readRes, 1)
	go func() {
		n, err := body.Read(first)
		ch <- readRes{n, err}
	}()
	timer := time.NewTimer(firstByteGateTimeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		// 排水先于结果采纳的确定性重查：expireDrain 先关 drainCh 再取消出站
		// （顺序见 shutdown.go），取消引发的读错误到达时 drainCh 必已关闭。
		select {
		case <-s.drainCh:
			_ = body.Close()
			meta.status = 0 // 客户端实收 504，行状态以 statusWriter 为准
			return nil, errDrainExpired
		default:
		}
		if r.n > 0 {
			s.markResponded(meta)
			// 注入收尾只对无声明长度的流式体（SSE 恒 chunked）：定长体（如
			// 上游以 JSON 应答 stream 请求）注入额外字节会超出 Content-Length
			// 造成协议违例——定长体到期收尾＝EOF 截断（非流式残余边界同款）。
			resp.Body = &drainBody{inner: body, s: s, first: first[:r.n],
				fixedLen: resp.ContentLength >= 0}
			return resp, nil
		}
		// 首读即 EOF/错误：不闸不换形状（保真透传；截断与否由记账观察）
		s.markResponded(meta)
		return resp, nil
	case <-timer.C:
		_ = body.Close()
		meta.status = 0
		return nil, errFirstByteSilent
	case <-s.drainCh:
		_ = body.Close()
		meta.status = 0
		return nil, errDrainExpired
	}
}

// drainBody 闸门放行后的响应体：正常读穿透 inner（usageBody，喂扫描器），
// 排水广播后转为交付合成错误事件、交完转 EOF（fixedLen 体除外——见
// gateFirstByte 的定长注记）。Read/Close 都由反向代理的拷贝 goroutine 顺次
// 调用，无并发访问。
type drainBody struct {
	inner    io.ReadCloser
	s        *Server
	first    []byte // 闸门预读的首字节（预读时已喂扫描器，续传不重喂）
	pend     []byte // 注入错误事件的未交付残余
	doneEOF  bool   // 注入已交付完毕（其后恒 EOF）
	fixedLen bool   // 定长体：不注入（到期＝EOF 截断，残余边界）
	closed   bool
}

func (b *drainBody) Read(p []byte) (int, error) {
	if len(b.first) > 0 {
		n := copy(p, b.first)
		b.first = b.first[n:]
		return n, nil
	}
	if b.doneEOF {
		return 0, io.EOF
	}
	// 排水已广播：即刻转收尾（不再发起新的上游读）
	select {
	case <-b.s.drainCh:
		return b.expire(p)
	default:
	}
	// 弃读安全：上游读入独立缓冲 own，不共享 p——kick 分支弃置该读时，
	// 拷贝循环复用 p 不会与弃置中的读竞争同一缓冲。
	own := make([]byte, len(p))
	type readRes struct {
		n   int
		err error
	}
	ch := make(chan readRes, 1)
	go func() {
		n, err := b.inner.Read(own)
		ch <- readRes{n, err}
	}()
	select {
	case r := <-ch:
		if r.n > 0 {
			copy(p, own[:r.n])
			return r.n, nil // 真实字节优先交付；收尾（若有）留给下一次 Read
		}
		if r.err != nil {
			select {
			case <-b.s.drainCh:
				return b.expire(p)
			default:
			}
		}
		return r.n, r.err // 上游 EOF/断流：原样了结（不抢救，契约边界）
	case <-b.s.drainCh:
		return b.expire(p)
	}
}

// expire 排水收尾一次交付：流式体交付合成错误事件（必要时分多次 Read 交付
// 完，交完转 EOF）；定长体直接 EOF 截断（无合法错误投递通道，残余边界）。
func (b *drainBody) expire(p []byte) (int, error) {
	if b.fixedLen {
		b.doneEOF = true
		return 0, io.EOF
	}
	if len(b.pend) == 0 {
		b.pend = sseErrorEvent("渡口正在关停（排水到期收尾），流在此时被截断；请重试以继续")
	}
	n := copy(p, b.pend)
	b.pend = b.pend[n:]
	if len(b.pend) == 0 {
		b.doneEOF = true
	}
	return n, nil
}

func (b *drainBody) Close() error {
	if b.closed {
		return nil
	}
	b.closed = true
	return b.inner.Close()
}
