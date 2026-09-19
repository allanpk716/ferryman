// router-fidelity/tap —— 渡口改写保真度验证·上游捕获器（capture/forwarder.go 同型变体）。
// 架在 CC 与真实上游之间：CC → 本 tap(15723) → 上游（默认 GLM anthropic 兼容口）。
// 与 forwarder 的差别：
//  1. 不脱敏——请求头原样捕获，供与 forwarder 捕获件逐字段差分（diff.py）；
//  2. 新增响应侧摘要（状态码＋SSE usage 四列），响应正文零落盘零留存。
//
// ⚠️ 真钥会流经落盘文件（本工具设计前提）：捕获目录不进 git、验完即删、权限收紧
// （目录 0o700 / 文件 0o600，Windows 为 best-effort）。此警告须同步写入套件
// README（该文件归 scenarios/README 票，不在本票路径内）。
//
// 用法：go run tap.go [-listen 127.0.0.1:15723] [-upstream https://open.bigmodel.cn/api/anthropic] [-out ~/ferryman/router-fidelity]
package main

import (
	"bytes"
	"context"
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

// 权限位：目录 0o700 / 文件 0o600。Windows 下权限位不生效（best-effort），
// 验收条款即"测试断言 os.FileMode 传参"，故落盘单点走可注入变量。
const (
	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600
)

// osMkdirAll / osWriteFile 落盘单点：stdlib 函数无法拦截，经变量注入让单测
// 断言权限传参。生产路径原样直通 stdlib。
var (
	osMkdirAll  = func(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }
	osWriteFile = func(path string, data []byte, perm os.FileMode) error { return os.WriteFile(path, data, perm) }
)

// tap 一个捕获器实例：输出目录＋序号发号器。
type tap struct {
	outDir string
	mu     sync.Mutex // 保护 seq 与捕获文件写入
	seq    int
}

// newTap 建 tap：构造即确保捕获目录存在（0o700）。
func newTap(outDir string) (*tap, error) {
	if err := osMkdirAll(outDir, dirPerm); err != nil {
		return nil, err
	}
	return &tap{outDir: outDir}, nil
}

func (tp *tap) nextSeq() int {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.seq++
	return tp.seq
}

// defaultOutDir 默认捕获目录 ~/ferryman/router-fidelity（仓库外，天然不进 git）。
func defaultOutDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "ferryman", "router-fidelity"), nil
}

// seqCtxKey 请求序号经 context 传给 ModifyResponse（响应摘要与请求捕获同 seq 关联）。
type seqCtxKey struct{}

// handler 返回 tap 的转发处理器。
func (tp *tap) handler(target *url.URL) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
			// 不调 SetXForwarded：Rewrite 模式下代理零附加头，出站保真
		},
		// SSE 必须立即冲刷：不设会缓冲流式响应（Q14 实测：缓冲＝客户端重试风暴）
		FlushInterval: -1,
		// Transport 用默认：上游是真实 HTTPS 端点，证书校验不开旁路，不改传输行为
		ModifyResponse: func(resp *http.Response) error {
			seq, _ := resp.Request.Context().Value(seqCtxKey{}).(int)
			log.Printf("[tap] ← 上游 %d %s（%d 字节）", resp.StatusCode, resp.Status, resp.ContentLength)
			resp.Body = newRespTee(tp, seq, resp)
			return nil
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__capture/ping" { // 自检端点（看门/探活用），零捕获
			fmt.Fprintln(w, "ok")
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("[tap] 读体失败: %v", err)
		}
		r.Body = io.NopCloser(bytes.NewReader(body)) // 读体回填，转发不缺斤短两
		seq := tp.nextSeq()
		tp.captureRequest(seq, r, body)
		ctx := context.WithValue(r.Context(), seqCtxKey{}, seq)
		proxy.ServeHTTP(w, r.WithContext(ctx))
	})
}

// captureHeaders 头原样捕获——本 tap 与 forwarder 的关键差别：不脱敏。真钥
// 完整落盘是设计前提，目录管控与即删纪律见文件头警告。
func captureHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, vs := range h {
		out[k] = strings.Join(vs, ",")
	}
	return out
}

// captureRequest 请求侧捕获：seq/ts/method/path/query/headers/body。
// body 存解析后 JSON（便于 diff.py 逐字段对比），解析失败存原文。
func (tp *tap) captureRequest(seq int, r *http.Request, body []byte) {
	entry := map[string]any{
		"seq":     seq,
		"ts":      time.Now().Format(time.RFC3339Nano),
		"method":  r.Method,
		"path":    r.URL.Path,
		"query":   r.URL.RawQuery,
		"headers": captureHeaders(r.Header),
	}
	var parsed any
	if json.Unmarshal(body, &parsed) == nil {
		entry["body"] = parsed
	} else {
		entry["body_raw"] = string(body)
	}
	tp.writeFile("req", seq, entry)

	// 摘要一行：路径＋顶层键＋消息数（日志快速判读）
	keys := ""
	if m, ok := parsed.(map[string]any); ok {
		for k := range m {
			keys += k + ","
		}
		if msgs, ok := m["messages"].([]any); ok {
			keys += fmt.Sprintf(" messages=%d", len(msgs))
		}
	}
	log.Printf("[tap] #%d %s %s keys={%s}", seq, r.Method, r.URL.Path, keys)
}

// writeFile 捕获文件落盘单点（0o600＋互斥）；kind ∈ {req, resp}，同 seq 两件配对。
func (tp *tap) writeFile(kind string, seq int, entry map[string]any) {
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		log.Printf("[tap] 捕获序列化失败 #%d %s: %v", seq, kind, err)
		return
	}
	name := fmt.Sprintf("%s_%s%03d.json", time.Now().Format("20060102_150405"), kind, seq)
	path := filepath.Join(tp.outDir, name)
	tp.mu.Lock()
	err = osWriteFile(path, data, filePerm)
	tp.mu.Unlock()
	if err != nil {
		log.Printf("[tap] 捕获写入失败 %s: %v", path, err)
	}
}

// ---- 响应侧：usage 四列提取（边流边解析，正文零留存） ----

// usageFields 四列 token（dock 科目同口径）。指针字段＝字段级覆盖合并：
// message_delta 带的列一律覆盖 message_start（Q14：GLM 真 usage 在 delta，
// start 恒 0），delta 未带的列保留 start 值。
type usageFields struct {
	InputTokens              *int `json:"input_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
}

// sseEventJSON 只解析用得到的三处；其余字段（delta/content_block 等）跳过。
type sseEventJSON struct {
	Type    string `json:"type"`
	Message *struct {
		Usage *usageFields `json:"usage"`
	} `json:"message"`
	Usage *usageFields `json:"usage"`
}

// sseParser 增量 SSE 解析器：只提取 usage 元数据，正文增量消化不留存。
// 与 internal/beat/httpsender.go 的 parseSSEUsage 同语义（Q14 雷同一颗：
// usage 以 message_delta 为准），但适配 respTee 逐块喂入而非一次性 Reader。
type sseParser struct {
	pending  []byte   // 不足一行的尾巴
	data     []string // 当前事件已累积的 data 行
	usage    usageFields
	sawStart bool
	sawDelta bool
}

// feed 喂一块响应字节（仅 SSE 明文有效；压缩流由 respTee 跳过不喂）。
func (p *sseParser) feed(chunk []byte) {
	p.pending = append(p.pending, chunk...)
	for {
		i := bytes.IndexByte(p.pending, '\n')
		if i < 0 {
			return
		}
		line := strings.TrimSuffix(string(p.pending[:i]), "\r")
		p.pending = p.pending[i+1:]
		p.line(line)
	}
}

// flushTail 流末尾兜底：最后事件可能没有空行收尾。
func (p *sseParser) flushTail() {
	if len(p.pending) > 0 {
		line := strings.TrimSuffix(string(p.pending), "\r")
		p.pending = nil
		p.line(line)
	}
	p.flushEvent()
}

func (p *sseParser) line(l string) {
	switch {
	case l == "": // 空行＝事件边界
		p.flushEvent()
	case strings.HasPrefix(l, "data:"):
		v := strings.TrimPrefix(l, "data:")
		v = strings.TrimPrefix(v, " ") // 单个前导空格是字段分隔符，不是内容
		p.data = append(p.data, v)
	}
	// 其余行（event:/注释/keep-alive）不参与解析——事件类型以 data JSON 的 type 为准
}

func (p *sseParser) flushEvent() {
	if len(p.data) == 0 {
		return
	}
	payload := strings.Join(p.data, "\n") // SSE 多行 data 并接（规范行为）
	p.data = p.data[:0]
	var ev sseEventJSON
	if json.Unmarshal([]byte(payload), &ev) != nil {
		return // 非 JSON 行忽略
	}
	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			p.apply(ev.Message.Usage)
			p.sawStart = true
		}
	case "message_delta":
		if ev.Usage != nil {
			p.apply(ev.Usage)
			p.sawDelta = true
		}
	}
}

func (p *sseParser) apply(u *usageFields) {
	if u == nil {
		return
	}
	if u.InputTokens != nil {
		p.usage.InputTokens = u.InputTokens
	}
	if u.CacheReadInputTokens != nil {
		p.usage.CacheReadInputTokens = u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens != nil {
		p.usage.CacheCreationInputTokens = u.CacheCreationInputTokens
	}
	if u.OutputTokens != nil {
		p.usage.OutputTokens = u.OutputTokens
	}
}

// source usage 来源："message_delta"（权威）/ "message_start" / "none"。
func (p *sseParser) source() string {
	switch {
	case p.sawDelta:
		return "message_delta"
	case p.sawStart:
		return "message_start"
	default:
		return "none"
	}
}

func deref(i *int) int {
	if i == nil {
		return 0
	}
	return *i
}

// usageValue 仅在真解析到 usage 时返回四列。"没拿到"绝不给全 0 假值——
// Q14 雷：全 0 与"没拿到"必须可区分（真值判定全靠 message_delta）。
func (p *sseParser) usageValue() map[string]int {
	if !p.sawStart && !p.sawDelta {
		return nil
	}
	return map[string]int{
		"input_tokens":                deref(p.usage.InputTokens),
		"cache_read_input_tokens":     deref(p.usage.CacheReadInputTokens),
		"cache_creation_input_tokens": deref(p.usage.CacheCreationInputTokens),
		"output_tokens":               deref(p.usage.OutputTokens),
	}
}

// respTee 响应侧旁路：字节原样透传给代理拷贝循环，同时喂给 SSE 解析器提取
// usage。正文只在解析器里增量消化，不缓冲不落盘（验收：响应正文零落盘）。
type respTee struct {
	tp              *tap
	seq             int
	rc              io.ReadCloser
	p               sseParser
	parse           bool // content-type 为 SSE 且未压缩才解析 usage
	compressed      bool // 压缩流：保真透传不解压，usage 如实标 skipped
	done            bool // finish 幂等（EOF 与 Close 双路径收口）
	status          int
	contentType     string
	contentEncoding string
}

func newRespTee(tp *tap, seq int, resp *http.Response) *respTee {
	ct := resp.Header.Get("Content-Type")
	ce := resp.Header.Get("Content-Encoding")
	compressed := ce != "" && !strings.EqualFold(ce, "identity")
	return &respTee{
		tp: tp, seq: seq, rc: resp.Body,
		parse:           strings.Contains(ct, "text/event-stream") && !compressed,
		compressed:      compressed,
		status:          resp.StatusCode,
		contentType:     ct,
		contentEncoding: ce,
	}
}

func (t *respTee) Read(b []byte) (int, error) {
	n, err := t.rc.Read(b)
	if n > 0 && t.parse {
		t.p.feed(b[:n])
	}
	if err != nil {
		t.finish() // EOF 或读错误都收口（幂等）
	}
	return n, err
}

func (t *respTee) Close() error {
	t.finish() // 客户端提前断开时由此收口
	return t.rc.Close()
}

// finish 落盘响应摘要：状态码＋元数据＋usage 四列（如有）。正文一字节不落。
func (t *respTee) finish() {
	if t.done {
		return
	}
	t.done = true
	if t.parse {
		t.p.flushTail()
	}
	entry := map[string]any{
		"seq":              t.seq,
		"ts":               time.Now().Format(time.RFC3339Nano),
		"status":           t.status,
		"content_type":     t.contentType,
		"content_encoding": t.contentEncoding,
	}
	switch {
	case t.compressed:
		entry["usage_source"] = "skipped_compressed"
	default:
		entry["usage_source"] = "none"
		if t.parse {
			entry["usage_source"] = t.p.source()
		}
	}
	if t.parse {
		if u := t.p.usageValue(); u != nil {
			entry["usage"] = u
		}
	}
	t.tp.writeFile("resp", t.seq, entry)
}

func main() {
	listen := flag.String("listen", "127.0.0.1:15723", "监听地址")
	upstream := flag.String("upstream", "https://open.bigmodel.cn/api/anthropic", "上游地址（默认 GLM anthropic 兼容口）")
	out := flag.String("out", "", "捕获输出目录（默认 ~/ferryman/router-fidelity）")
	flag.Parse()

	outDir := *out
	if outDir == "" {
		d, err := defaultOutDir()
		if err != nil {
			log.Fatal(err)
		}
		outDir = d
	}
	tp, err := newTap(outDir)
	if err != nil {
		log.Fatal(err)
	}

	target, err := url.Parse(*upstream)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("router-fidelity tap: http://%s → %s（捕获目录 %s，Ctrl+C 退出）", *listen, *upstream, outDir)
	log.Printf("⚠️ 真钥将原样落盘于捕获目录：目录不进 git、验完即删、权限收紧（0o700/0o600）")
	srv := &http.Server{Addr: *listen, Handler: tp.handler(target), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
