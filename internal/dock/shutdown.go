// shutdown.go — 票02：优雅排水（错误契约热修 1）。
//
// Shutdown(ctx) 语义：停收新连接 → 等在途请求自然结束；ctx 到期仍未完 →
// 合成收尾（流已建立的在途请求经 drainBody 逐个注入 Anthropic 流内错误
// 事件；首字节未写出的请求取消出站，由 ErrorHandler 回 504＋错误体），
// 给注入交付宽限后 Close 硬收，返回 ctx.Err()。自然结束返回 nil。排水上限
// 由调用方经 ctx 给（daemon 侧用 [dock].drain_timeout_s 造 ctx——票03 接线，
// 本票不改 internal/daemon）。
//
// 在途注册表只收记账路径请求（record 恒真于 daemon 生产接线；纯透传模式
// 不读体无法判流式，硬收断连属声明式残余边界）。非流式 200 已写出的请求
// 到期时无合法错误投递通道：断连＋尽力记账（spec 声明式残余，不过度设计）。
package dock

import (
	"context"
	"net/http"
	"time"
)

// drainInjectGrace 注入交付宽限：排水到期注入错误事件后，给拷贝循环这点
// 时间把事件冲刷给客户端再 Close 硬收（本地毫秒级即完，1.5s 是慢链余量）。
// 包级 var 供测试调节。
var drainInjectGrace = 1500 * time.Millisecond

// inflight 单个在途记账请求的排水句柄。
type inflight struct {
	cancel    context.CancelFunc // 出站请求取消句柄（响应未确立时用它换 504）
	responded bool               // 出站响应已确立（闸门放行即置位；此后不再取消，走注入路径）
}

// trackInflight / untrackInflight 注册表进出。空↔非空切换 idleCh（空载时
// 关闭）——waitIdle 据此免轮询等待。
func (s *Server) trackInflight(meta *reqMeta, cancel context.CancelFunc) {
	e := &inflight{cancel: cancel}
	meta.entry = e
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.inflights) == 0 {
		s.idleCh = make(chan struct{})
	}
	s.inflights[e] = struct{}{}
}

func (s *Server) untrackInflight(meta *reqMeta) {
	if meta.entry == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inflights, meta.entry)
	if len(s.inflights) == 0 {
		close(s.idleCh)
	}
}

// markResponded 出站响应确立（transport 层回填；此后该请求不再被排水取消）。
func (s *Server) markResponded(meta *reqMeta) {
	if meta.entry == nil {
		return
	}
	s.mu.Lock()
	meta.entry.responded = true
	s.mu.Unlock()
}

// isDrained 排水是否已到期（ErrorHandler 的 context.Canceled 分流依据）。
func (s *Server) isDrained() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drained
}

// waitIdle 等注册表清空；ctx 先到返回 false。
func (s *Server) waitIdle(ctx context.Context) bool {
	for {
		s.mu.Lock()
		n, ch := len(s.inflights), s.idleCh
		s.mu.Unlock()
		if n == 0 {
			return true
		}
		select {
		case <-ch: // 注册表刚清空（或换了新通道）——复查
		case <-ctx.Done():
			return false
		}
	}
}

// Shutdown 优雅排水关停（热修 1）。真实监听（Start 过）时停收新连接
// （http.Server.Shutdown 语义）等在途自然结束；handler 级挂载（httptest，
// s.srv 为 nil）时以在途注册表空为准。ctx 到期未完 → expireDrain 合成收尾
// → 注入交付宽限 → Close 硬收 → 返回 ctx.Err()；自然结束返回 nil。
// 重复调用安全（expireDrain 幂等；Close 亦幂等）。
func (s *Server) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var srvDone chan error
	if s.srv != nil {
		srvDone = make(chan error, 1)
		// 排水上限只约束等待：srv.Shutdown 自身用 Background 等在途，由本
		// 函数的 select 兑现到期强收——不会出现"内部先超时放弃"的缝隙。
		go func() { srvDone <- s.srv.Shutdown(context.Background()) }()
	}
	if s.srv != nil {
		select {
		case <-srvDone: // 在途全部自然结束（handler 返回前必已出注册表）
			return nil
		case <-ctx.Done():
		}
	} else if s.waitIdle(ctx) {
		return nil
	}
	// 排水到期：合成收尾 → 注入交付宽限 → 硬收。
	s.expireDrain()
	grace, cancel := context.WithTimeout(context.Background(), drainInjectGrace)
	defer cancel()
	s.waitIdle(grace)
	if s.srv != nil {
		err := s.srv.Close()
		<-srvDone
		if err != nil && err != http.ErrServerClosed {
			return err
		}
	}
	return ctx.Err()
}

// expireDrain 排水收尾广播（幂等）。顺序是确定性的来源：先关 drainCh
// （闸门扣住中的请求转 504、drainBody 转注入），再取消响应未确立的出站
// （其 RoundTrip 以 context.Canceled 失败，ErrorHandler 按 isDrained 回
// 504；闸门/体内对到达的取消读错误重查 drainCh 即得收尾形状而非裸透传）。
func (s *Server) expireDrain() {
	s.mu.Lock()
	if s.drained {
		s.mu.Unlock()
		return
	}
	s.drained = true
	close(s.drainCh)
	var pending []context.CancelFunc
	for e := range s.inflights {
		if !e.responded {
			pending = append(pending, e.cancel)
		}
	}
	s.mu.Unlock()
	for _, c := range pending {
		c()
	}
}
