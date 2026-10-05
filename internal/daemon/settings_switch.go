package daemon

// settings_switch.go — 设置视图票06：POST /settings/dock/switch（spec 写面
// 「POST /settings/dock/switch 复用既有 provider_switch」）。
//
//   - 复用既有热换绑核心：分发行把既有 ProviderSwitchFunc 钩子（serveConfig
//     装配的 dockState.switchTo——存在性校验→SetActiveUpstream 落盘→COW 内存
//     换绑，provider_switch.go）原样递进本端点，只调用不复制、不改其行为；
//     「只能切启动时已加载条目」由此天然满足（switchTo 查内存快照
//     cur.Upstreams，启动装载后只读）；钩子未装（渡口未启用/替身）＝未知
//     路径 404（/provider_switch 同语义）。
//   - 票03 单写者锁：换绑是设置写路径，settingsWriteMu 串行（与节级 PUT/
//     实体写/快照还原同锁；锁内调 switchTo 只嵌套其自有 h.mu，无反向依赖，
//     无死锁环）。
//   - 票05 写前自动快照：snapshotBeforeWrite("switch")（seam 真实现=整文件
//     字节快照滚动 20 份；文件类恒 auto——snapshotClasses 白名单只有 auto/
//     manual/pre-restore，异类名不可清单、滚动不收；switch 溯源由审计行
//     section=dock.switch 与 seam reason 承担，settingsPutSection 同口径）。
//   - 审计行：auditSettingsWrite(section="dock.switch"，saved/rejected+error，
//     before/after=盘上 active 现值→目标名——settingsPutSection 的「before=
//     盘上现值」同口径）。
//   - 「本会话经设置面新增、未重启」条目：盘上有、启动内存表没有——钩子以
//     「不在上游表内」报错，本端点据盘上存在性把这一形态翻译成 409 +
//     needs_restart:true + 含「重启」文案（票面二选一已定 409 形态，测试
//     钉死）。落盘失败经错误前缀「落盘失败」先行甄别（switchTo 唯一与存在
//     性无关的失败族，前缀写死于 provider_switch.go），如实 400 不误判 409。
//   - codex 不可用按既有规则拒绝：目标条目 CodexAvailability=unsupported →
//     400 报因（对齐 CLI provider switch 默认拒语义，cmd/ferryman/provider.go；
//     /provider_switch 端点族本身不做此拒，设置面入口对齐 CLI 默认）。可用性
//     取盘面条目（启动表加载后只读，无本会话编辑时盘=内存；本会话编辑过的
//     条目按重启语义本就不热切，拒绝面从严）。
//
// 守门序随 /shutdown、/provider_switch 管理端点族：loopback → 方法 POST →
// 先读光 body → Bearer → 业务；200 响应 {switched,needs_restart,active}。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"ferryman/internal/config"

	"github.com/BurntSushi/toml"
)

// settingsSwitchSection 审计行 section 口径（票面：section 记 "dock.switch"）。
const settingsSwitchSection = "dock.switch"

// doSettingsDockSwitch POST /settings/dock/switch（httpapi.go makeHandler 分派
// 行落点；onSwitch＝既有 ProviderSwitchFunc 钩子，与 /provider_switch 同源）。
func doSettingsDockSwitch(dl DaemonLike, onSwitch ProviderSwitchFunc, token string, w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden: loopback only"})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	bodyRaw, _ := io.ReadAll(r.Body) // 先读光 body 再回话（管理端点族 RST 纪律）
	if !isAuthed(r, token) {
		unauthorized(w)
		return
	}
	d, ok := dl.(*Daemon)
	if !ok {
		notFound(w) // 替身无设置写面（settings_write.go 同语义）
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(bodyRaw, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		badRequest(w, errors.New(`body 须为 {"name":"<条目名>"} 的 JSON 对象（name 非空）`))
		return
	}
	code, resp := settingsDockSwitch(d, onSwitch, req.Name)
	writeJSON(w, code, resp)
}

// settingsDockSwitch 换绑业务（锁内全序：盘上读数→写前快照→codex 既有规则
// 校验→既有钩子换绑落盘→审计）。状态码/响应体定形：
//
//	200 {switched:true, needs_restart:false, active}
//	409 {error(含「重启」), needs_restart:true} —— 盘上有、启动表没有（本会话新增）
//	400 {error}                                —— 未知条目/codex 不可用/落盘失败
//	500 {error}                                —— 写前快照失败
func settingsDockSwitch(d *Daemon, onSwitch ProviderSwitchFunc, name string) (int, map[string]any) {
	settingsWriteMu.Lock()
	defer settingsWriteMu.Unlock()

	cfgPath := config.ResolveConfigPath("")
	curActive := ""
	if sec := settingsDiskSection(cfgPath, "dock"); sec != nil {
		if s, ok := sec["active"].(string); ok {
			curActive = s
		}
	}
	before := map[string]any{"active": curActive} // 审计 before=盘上现值（settingsPutSection 同口径）
	after := map[string]any{"active": name}

	// 写前自动快照（票05；seam reason="switch"，文件类恒 auto——可清单可滚动）。
	if err := snapshotBeforeWrite("switch"); err != nil {
		err = fmt.Errorf("写前快照失败: %w", err)
		auditSettingsWrite(d, settingsSwitchSection, "rejected", before, after, err)
		return http.StatusInternalServerError, map[string]any{"error": err.Error()}
	}

	// codex 不可用既有规则（CLI provider switch 默认拒语义）：盘面条目
	// codex=unsupported（显式否决）→ 拒绝并报因，内存/盘均不动。
	if up, hit := settingsDockDiskUpstream(cfgPath, name); hit &&
		up.CodexAvailability() == config.CodexUnsupported {
		err := fmt.Errorf("条目 %q 对 codex 不可用（codex=%q）；如仅需 cc 车道，"+
			"请用 CLI provider switch --cc-only 显式放行", name, config.CodexUnsupported)
		auditSettingsWrite(d, settingsSwitchSection, "rejected", before, after, err)
		return http.StatusBadRequest, map[string]any{"error": err.Error()}
	}

	// 既有热换绑核心（provider_switch.go 只调用不改）：校验→落盘→COW 换绑。
	// 成功回显体（ok/active/base_url/dialect/codex）不进本端点响应——票面定形
	// {switched,needs_restart,active}。
	if _, err := onSwitch(name); err != nil {
		if strings.Contains(err.Error(), "落盘失败") { // 持久化失败：内存未换绑，如实 400
			auditSettingsWrite(d, settingsSwitchSection, "rejected", before, after, err)
			return http.StatusBadRequest, map[string]any{"error": err.Error()}
		}
		if _, hit := settingsDockDiskUpstream(cfgPath, name); hit {
			// 盘上有、启动加载表没有＝本会话经设置面新增、未重启（票面 409 形态）。
			msg := fmt.Errorf("条目 %q 不在守护启动时加载的上游表内（本会话新增，尚未重启）；"+
				"重启守护后即可切换", name)
			auditSettingsWrite(d, settingsSwitchSection, "rejected", before, after, msg)
			return http.StatusConflict, map[string]any{
				"error":         msg.Error(),
				"needs_restart": true,
			}
		}
		auditSettingsWrite(d, settingsSwitchSection, "rejected", before, after, err)
		return http.StatusBadRequest, map[string]any{"error": err.Error()}
	}
	auditSettingsWrite(d, settingsSwitchSection, "saved", before, after, nil)
	return http.StatusOK, map[string]any{
		"switched":      true,
		"needs_restart": false, // 换绑即时生效（spec：provider switch 即时生效）
		"active":        name,
	}
}

// settingsDockDiskUpstream 盘上某上游条目（部分解码，不整载不校验——存在性
// 与 codex 可用性裁决用；读不得/无该节/无名＝(零值,false)）。
func settingsDockDiskUpstream(cfgPath, name string) (config.DockUpstream, bool) {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return config.DockUpstream{}, false
	}
	var partial struct {
		Dock struct {
			Upstreams map[string]config.DockUpstream `toml:"upstreams"`
		} `toml:"dock"`
	}
	if err := toml.Unmarshal(raw, &partial); err != nil {
		return config.DockUpstream{}, false
	}
	up, ok := partial.Dock.Upstreams[name]
	return up, ok
}
