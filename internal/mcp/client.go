package mcp

// client.go — 票04：daemon HTTP 转发客户端（五件工具的唯一数据通道）。
//
// 铁律（规格 docs/superpowers/specs/20260920-agent-surface-mcp-readonly-spec.md
// 「架构与读取路径」「错误面」节）：
//   - 只发只读 GET，Bearer＋127.0.0.1 原样复用既有面（不新开监听、不复制鉴权）；
//   - token 经既有配置解析取得（config.Load 优先级：显式参数 > FERRYMAN_CONFIG
//     > 默认路径 → cfg.DataDir()/daemon.token），只活进程内存，任何错误文案
//     不回显 token（token 只进 Authorization 头，永不进 URL/日志）；
//   - daemon 不可达/超时 → 明确报错，文案含拉起途径提示；不缓存（每次真连）、
//     不伪造、不顺势自举 daemon（本客户端除这次 GET 外零副作用）。
//
// token 每次调用现读文件：daemon 首启/重生成 token 后 MCP server 无需重启，
// 也天然满足「不缓存」——64 字节本地文件，成本可忽略。

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ferryman/internal/config"
)

// daemonRequestTimeout 单次 daemon 请求超时（不可达/挂死都要及时回错，绝不
// 顺势自举）。
const daemonRequestTimeout = 5 * time.Second

// daemonTokenFile token 文件名（daemon.EnsureToken 同名同位）。
const daemonTokenFile = "daemon.token"

// pullUpHint 错误文案必含的拉起途径提示（规格「错误面」节逐字）。
const pullUpHint = "任意钩子触发或 ferryman serve 会拉起"

// maxDaemonBody 单次响应体上限（报表面量级远不及此，防御性封顶）。
const maxDaemonBody = 8 << 20

// DaemonClient daemon 只读端点转发客户端。
type DaemonClient struct {
	port    int
	dataDir string
	httpc   *http.Client
}

// NewDaemonClient cfg 来自 config.Load（优先级即鉴权解析优先级）。
func NewDaemonClient(cfg *config.Config) *DaemonClient {
	return &DaemonClient{
		port:    cfg.Server.Port,
		dataDir: cfg.DataDir(),
		httpc:   &http.Client{Timeout: daemonRequestTimeout},
	}
}

// readToken 只读读取 daemon.token（缺失/空白 → 明确错误；绝不生成——生成是
// daemon 启动路径 EnsureToken 的职责，MCP 面不自举）。
func readToken(dataDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, daemonTokenFile))
	if err != nil {
		return "", fmt.Errorf("daemon token 文件不可读（%s 下无 %s，daemon 尚未在此"+
			"数据目录启动过）: %v", dataDir, daemonTokenFile, err)
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		return "", fmt.Errorf("daemon token 文件为空（%s）", filepath.Join(dataDir, daemonTokenFile))
	}
	return tok, nil
}

// Get 转发一次只读 GET：query 参数透传（可空）；2xx 回 daemon 响应体原样字节
// （JSON 透传，不解析不重排）；其余一律 error——错误文案含 daemon 侧信息但不
// 含 token。
func (c *DaemonClient) Get(path string, query url.Values) ([]byte, error) {
	token, err := readToken(c.dataDir)
	if err != nil {
		return nil, fmt.Errorf("%v——%s", err, pullUpHint)
	}
	u := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", c.port), Path: path}
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.httpc.Do(req)
	if err != nil {
		// url.Error 只含 URL（token 在头、不在 URL），可安全上抛。
		return nil, fmt.Errorf("daemon 不可达（127.0.0.1:%d）: %v——%s；本面不缓存、"+
			"不伪造、不自举，拉起后重试即可", c.port, err, pullUpHint)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDaemonBody))
	if err != nil {
		return nil, fmt.Errorf("读 daemon 响应失败（HTTP %d）: %v", resp.StatusCode, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("daemon 返回 HTTP %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}
