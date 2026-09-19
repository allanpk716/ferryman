// Package dock 渡口：Claude Code API 流量的本机透传中转（票01 竖切）。
//
// CC → 渡口（127.0.0.1:15722）→ 上游（默认仍是 cc-switch 15721，行为不变）。
// 三件套：opt-in 生命周期（dock.go）＋内存快照（snapshot.go）＋纯转发保真
// （server.go）。配置无 [dock] 节时 daemon 完全不构造本包的 Server——不绑
// 端口、零行为变化（评审 F11 裁定语义）。
//
// 包边界：透传与改写双模式（rewrite_enabled＋守卫单源裁决，票05/06）＋快照＋
// dock 科目流水＋形态漂移告警。改写集以差分验证为准——出现第六件要么缺陷
// 要么过决策。
package dock

import (
	"net"
	"net/http"
	"time"
)

// Start 绑定监听并在独立 goroutine 起服务（不阻塞调用方）。绑定失败返回
// error——调用方（daemon）只告警降级、不拖垮主服务：渡口挂＝CC 直连上游的
// 旧行为，人工把 base_url 指回即回退。
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.listen)
	if err != nil {
		return err
	}
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logger.Printf("serve 退出: %v", err)
		}
	}()
	return nil
}

// Close 立即关停（断监听＋断在途连接）。重复调用安全。
func (s *Server) Close() error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Close()
}
