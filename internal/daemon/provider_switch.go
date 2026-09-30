package daemon

// provider_switch.go — 票02（供应商接管，D5/F4）：活跃供应商热切换——守护
// 管理口 POST /provider_switch ＋ 内存态原子换绑 ＋ 落盘持久化。
//
// 语义（spec Implementation Decisions 4）：
//   - 渡口对每个新请求经 dock.UpstreamResolver（本文件的 dockUpstreamState）
//     读内存态活跃条目——switch 触发内存原子换绑后新请求即刻新上游；
//   - 在途请求/SSE 流持有既有上游连接自然跑完（渡口侧视图机制，server.go）；
//   - switch 一体动作＝存在性校验 → 落盘（config.SetActiveUpstream 原子写
//     单源）→ 内存 COW 换绑；落盘失败内存不动（盘/内存不失配）；守护与渡口
//     进程不动、端口无空窗；
//   - switch 到 codex="unsupported" 条目的拒绝逻辑在 CLI 层（票06）——端点只
//     做存在性/合法性校验。
//
// 端点与 /shutdown 同族管理端点：守门序 loopback → 方法 POST → Bearer →
// 业务；body {"name":"<条目名>"}；200 回 {ok,active,base_url,dialect,codex}
// （真钥永不入回显——T39）。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"ferryman/internal/config"
)

// ProviderSwitchFunc 热切换端点钩子（装配处=serveConfig，渡口启用才有）；
// nil＝端点未装，/provider_switch 落未知路径 404。回 map 为 200 回显体；
// 错误→400 带 error（校验/落盘失败，如实报因不静默）。
type ProviderSwitchFunc func(name string) (map[string]any, error)

// doProviderSwitch POST /provider_switch（票02）。守门序与 doShutdown 同款
// （先读光 body 再回话的 RST 纪律同守）。
func doProviderSwitch(onSwitch ProviderSwitchFunc, token string, w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden: loopback only"})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	bodyRaw, _ := io.ReadAll(r.Body) // 先读光 body 再回话（同 doPost/doShutdown 的 RST 纪律）
	if !isAuthed(r, token) {
		unauthorized(w)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(bodyRaw, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		badRequest(w, errors.New("body 须为 {\"name\":\"<条目名>\"} 的 JSON 对象（name 非空）"))
		return
	}
	resp, err := onSwitch(req.Name)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// dockUpstreamState 活跃上游内存态持有者（票02）：COW 快照经原子指针换绑，
// 读侧（渡口逐请求/端点校验）无锁。Upstreams 表加载后只读——切换只改
// active 单选键，快照间共享同一 map 是安全的（无人改它）。
type dockUpstreamState struct {
	cur  atomic.Pointer[config.DockCfg]
	mu   sync.Mutex // switch 串行化：校验→落盘→换绑一体，并发 switch 不交错
	path string     // 落盘路径（与守护 Load 同源的 config.toml）
}

// newDockUpstreamState 构造（dock nil＝持有者空载：渡口未启用形态，仅防御）。
func newDockUpstreamState(dock *config.DockCfg, path string) *dockUpstreamState {
	h := &dockUpstreamState{path: path}
	if dock != nil {
		h.cur.Store(dock)
	}
	return h
}

// ActiveUpstream 实现 dock.UpstreamResolver（渡口逐请求读，无锁）。
func (h *dockUpstreamState) ActiveUpstream() (string, *config.DockUpstream) {
	if d := h.cur.Load(); d != nil {
		return d.ActiveUpstream()
	}
	return "", nil
}

// switchTo 热切换本体：存在性校验 → 落盘（SetActiveUpstream 原子写单源，
// 自校验不过即拒写）→ 内存 COW 换绑。落盘失败＝内存不动；成功后新请求
// 即刻新上游，守护/渡口不重启。
func (h *dockUpstreamState) switchTo(name string) (map[string]any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cur := h.cur.Load()
	if cur == nil || len(cur.Upstreams) == 0 {
		return nil, errors.New("无 [dock.upstreams] 上游表（旧单值形态），无可切条目")
	}
	up, ok := cur.Upstreams[name]
	if !ok {
		names := make([]string, 0, len(cur.Upstreams))
		for k := range cur.Upstreams {
			names = append(names, k)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("条目 %q 不在上游表内（可用: %s）", name, strings.Join(names, ", "))
	}
	if err := config.SetActiveUpstream(h.path, name); err != nil {
		return nil, fmt.Errorf("落盘失败（内存未换绑）: %w", err)
	}
	next := *cur // 浅拷贝 COW：Upstreams map 只读共享，仅换 active 单选键
	next.Active = name
	h.cur.Store(&next)
	fmt.Printf("[ferryman] 供应商热切换: %s → %s（渡口不重启，新请求即刻生效）\n",
		name, up.BaseURL)
	return map[string]any{
		"ok":       true,
		"active":   name,
		"base_url": up.BaseURL,
		"dialect":  up.Dialect,
		"codex":    up.CodexAvailability(), // CLI（票06）回显 codex 车道模式用
	}, nil
}
