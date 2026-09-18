// capture/forwarder —— Q14 心跳保真度实验·段一 + billion-context 兼容性验证的抓包转发器。
// 架在 CC 与 cc-switch 之间：CC → 本转发器(15722) → cc-switch(15721) → GLM。
// 每个请求落一个 JSON 捕获文件（~/ferryman/captures/，绝不进 git）；响应原样回流（ReverseProxy 流式透传）。
// 用法：go run forwarder.go [-listen 127.0.0.1:15722] [-upstream http://127.0.0.1:15721] [-out ~/ferryman/captures]
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	outDir string
	mu     sync.Mutex // 捕获文件写锁
	reqSeq int
)

// redactHeaders 敏感头只留前缀（本地文件也不存完整凭据）。
func redactHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for k, vs := range h {
		v := strings.Join(vs, ",")
		lk := strings.ToLower(k)
		if strings.Contains(lk, "authorization") || strings.Contains(lk, "api-key") || strings.Contains(lk, "token") {
			if len(v) > 8 {
				v = v[:8] + "…(redacted)"
			}
		}
		out[k] = v
	}
	return out
}

func captureReq(r *http.Request, body []byte) {
	mu.Lock()
	reqSeq++
	seq := reqSeq
	mu.Unlock()

	entry := map[string]any{
		"seq":    seq,
		"ts":     time.Now().Format(time.RFC3339Nano),
		"method": r.Method,
		"path":   r.URL.Path,
		"query":  r.URL.RawQuery,
		"headers": redactHeaders(r.Header),
	}
	// body 尽量存解析后的 JSON（便于 jq），失败存原文
	var parsed any
	if json.Unmarshal(body, &parsed) == nil {
		entry["body"] = parsed
	} else {
		entry["body_raw"] = string(body)
	}
	data, _ := json.MarshalIndent(entry, "", "  ")

	name := fmt.Sprintf("%s_req%03d.json", time.Now().Format("20060102_150405"), seq)
	path := filepath.Join(outDir, name)
	mu.Lock()
	err := os.WriteFile(path, data, 0o600)
	mu.Unlock()
	if err != nil {
		log.Printf("[capture] 写入失败 %s: %v", path, err)
		return
	}
	// 摘要一行：路径 + 顶层键 + 消息数（够日志快速判读）
	keys := ""
	if m, ok := parsed.(map[string]any); ok {
		for k := range m {
			keys += k + ","
		}
		if msgs, ok := m["messages"].([]any); ok {
			keys += fmt.Sprintf(" messages=%d", len(msgs))
		}
	}
	log.Printf("[capture] #%d %s %s keys={%s} → %s", seq, r.Method, r.URL.Path, keys, name)
}

func main() {
	listen := flag.String("listen", "127.0.0.1:15722", "监听地址")
	upstream := flag.String("upstream", "http://127.0.0.1:15721", "上游（cc-switch）地址")
	out := flag.String("out", "", "捕获输出目录（默认 ~/ferryman/captures）")
	flag.Parse()

	outDir = *out
	if outDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatal(err)
		}
		outDir = filepath.Join(home, "ferryman", "captures")
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		log.Fatal(err)
	}

	target, err := url.Parse(*upstream)
	if err != nil {
		log.Fatal(err)
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
		},
		// SSE 必须立即冲刷：不设会缓冲流式响应，下游看成断流/超时 → 客户端重试风暴（实测教训）
		FlushInterval: -1,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // 上游是本地 cc-switch，证书无所谓
		},
		ModifyResponse: func(resp *http.Response) error {
			log.Printf("[capture] ← 上游 %d %s（%d 字节）", resp.StatusCode, resp.Status, resp.ContentLength)
			return nil
		},
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__capture/ping" { // 自检端点
			fmt.Fprintln(w, "ok")
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("[capture] 读体失败: %v", err)
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		captureReq(r, body)
		proxy.ServeHTTP(w, r)
	})

	log.Printf("capture forwarder: http://%s → %s （捕获目录 %s，Ctrl+C 退出）", *listen, *upstream, outDir)
	srv := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
