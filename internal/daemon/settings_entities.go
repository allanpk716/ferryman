package daemon

// settings_entities.go — 设置视图票04：实体集合端点 + 引用完整性 + ferry
// 自键合并。
//
//   - 实体端点：PUT /settings/dock/upstreams/{名}（新增或整体替换）、
//     PUT|DELETE /settings/providers/{名}、PUT|DELETE /settings/prices/{键}。
//     全部经票03 单写者锁（settingsWriteMu）+ 写前快照（snapshotBeforeWrite）
//     + 审计行（auditSettingsWrite，section 记实体路径如 providers.glm）；
//     F3 密钥合并（settingsMergeSecrets）应用到实体 PUT 的 api_key——省略/
//     掩码占位串/空串/读面对象形＝保留盘上真钥，显式非空才覆盖。锁内全序
//     与 settingsPutSection 同款：锁→before 读（合并基线）→快照→合并/渲染
//     →落盘→审计——合并读盘必须在锁内，否则并发同实体写会以后写者的旧基
//     线覆盖前写者的换钥。
//   - 引用完整性（F5）：DELETE providers 的 chain/provider 守卫在票01 config
//     层（RemoveProviderEntry），daemon 透传错误；DELETE dock 上游的 active
//     拒删在 config 层（RemoveDockUpstream），[ferry.same_model].upstreams
//     白名单拒删在此补位（删前读盘检查，命中即拒、点名引用方）。
//   - 状态码分类（settingsEntityErrStatus）：守卫拒删=409、目标不存在=404、
//     其余（类型不对/校验失败/坏 body）=400——config 层错误文案即回话文案
//     （引用方名随文案点名）。
//   - 上游条目替换＝SetSectionTOML 的条目形（span＝条目表头到下一表头，新增
//     或整体替换）：替换不摘条目名，active/白名单引用的是名字——不悬空，
//     删除守卫语义无损；写回前置校验（validateFullLoad）经 dock 表全量把关。
//   - GET /settings/ferry：ferry 节盘上现值（含 same_model 子树）——
//     PUT /settings/ferry 自键层写后的对账读面；GET /settings 的 config 块
//     是守护内存生效值（写面只落盘、换挡在重启，needs_restart 语义），盘上
//     新值经此回话。
//
// 守门序随票03/POST 面：未知路径 404 在 auth 前；实体路由已知后 Bearer 才
// 鉴权；替身（非 *Daemon）404；坏 JSON 400 且不落审计（未成写尝试）。

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"ferryman/internal/config"

	"github.com/BurntSushi/toml"
)

// ---- 总分派（httpapi.go doPost 的 PUT|DELETE 早退落点）----

// handleSettingsWrite 设置写面总分派：实体路径（dock.upstreams/{名} 与
// providers|prices/{名}）路由到本文件端点；其余 PUT 回落票03 节级整写
// （handleSettingsPut，未知节 404 守门在彼）；DELETE 无节级面 → 404。
func handleSettingsWrite(dl DaemonLike, token string, w http.ResponseWriter, r *http.Request, bodyRaw []byte) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/settings/"), "/")

	var kind, name string
	switch {
	case len(parts) == 3 && parts[0] == "dock" && parts[1] == "upstreams" && parts[2] != "":
		kind, name = "upstream", parts[2]
	case len(parts) == 2 && parts[0] == "providers" && parts[1] != "":
		kind, name = "provider", parts[1]
	case len(parts) == 2 && parts[0] == "prices" && parts[1] != "":
		kind, name = "price", parts[1]
	}
	if kind == "" {
		if r.Method == http.MethodPut {
			handleSettingsPut(dl, token, w, r, bodyRaw)
			return
		}
		notFound(w)
		return
	}

	if !isAuthed(r, token) {
		unauthorized(w)
		return
	}
	d, ok := dl.(*Daemon)
	if !ok {
		notFound(w)
		return
	}

	var resp map[string]any
	var err error
	switch r.Method {
	case http.MethodPut:
		var body map[string]any
		if body, err = decodeJSONObject(bodyRaw); err == nil {
			switch kind {
			case "upstream":
				resp, err = d.settingsPutUpstream(name, body)
			case "provider":
				resp, err = d.settingsPutProvider(name, body)
			default:
				resp, err = d.settingsPutPrice(name, body)
			}
		}
	case http.MethodDelete:
		switch kind {
		case "upstream":
			resp, err = d.settingsDeleteUpstream(name)
		case "provider":
			resp, err = d.settingsDeleteProvider(name)
		default:
			resp, err = d.settingsDeletePrice(name)
		}
	default:
		err = errUnknownRoute
	}
	if err != nil {
		writeJSON(w, settingsEntityErrStatus(err), map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// errUnknownRoute 方法/路由组合不存在（防默认分支拿 nil 回话的护底线——
// PUT|DELETE 两形已被上游 switch 收敛，正常不可达）。
var errUnknownRoute = fmt.Errorf("路由不存在")

// settingsEntityErrStatus 实体写错误分类：守卫拒删（白名单/active/chain/
// provider 引用守卫，config 层文案统一带「拒删」）=409；目标不存在（「不
// 存在」/「不在上游表内」）=404；其余（类型不对/写回校验失败/坏 body）=400。
func settingsEntityErrStatus(err error) int {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "拒删"):
		return http.StatusConflict
	case strings.Contains(msg, "不存在") || strings.Contains(msg, "不在上游表内"):
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}

// ---- 实体写底座（锁内全序）----

// settingsPutEntityFlow 实体 PUT 锁内全序：锁→before 读（合并基线+审计
// before）→快照→prep（密钥合并+渲染→落盘动作）→落盘→审计。prep 的任何
// 错误以 attempt（body/合并面）落审计行 rejected 后上抛。
func (d *Daemon) settingsPutEntityFlow(entity string, prep func(before map[string]any) (attempt map[string]any, write func() error, err error)) (map[string]any, error) {
	settingsWriteMu.Lock()
	defer settingsWriteMu.Unlock()

	before := settingsDiskSubtree(entity)
	if err := snapshotBeforeWrite("settings-ui:" + entity); err != nil {
		auditSettingsWrite(d, entity, "rejected", before, nil, err)
		return nil, fmt.Errorf("写前快照失败: %w", err)
	}
	attempt, write, err := prep(before)
	if err != nil {
		auditSettingsWrite(d, entity, "rejected", before, attempt, err)
		return nil, err
	}
	if err := write(); err != nil {
		auditSettingsWrite(d, entity, "rejected", before, attempt, err)
		return nil, err
	}
	auditSettingsWrite(d, entity, "saved", before, attempt, nil)
	return map[string]any{"saved": true, "needs_restart": true}, nil
}

// settingsDeleteEntityFlow 实体 DELETE 锁内全序（同上，无 body；guard 供
// 删前守卫在锁内跑——白名单拒删在此挂）。
func (d *Daemon) settingsDeleteEntityFlow(entity string, guard func() error, del func() error) (map[string]any, error) {
	settingsWriteMu.Lock()
	defer settingsWriteMu.Unlock()

	before := settingsDiskSubtree(entity)
	if err := snapshotBeforeWrite("settings-ui:" + entity); err != nil {
		auditSettingsWrite(d, entity, "rejected", before, nil, err)
		return nil, fmt.Errorf("写前快照失败: %w", err)
	}
	if guard != nil {
		if err := guard(); err != nil {
			auditSettingsWrite(d, entity, "rejected", before, nil, err)
			return nil, err
		}
	}
	if err := del(); err != nil {
		auditSettingsWrite(d, entity, "rejected", before, nil, err)
		return nil, err
	}
	auditSettingsWrite(d, entity, "saved", before, nil, nil)
	return map[string]any{"deleted": true, "needs_restart": true}, nil
}

// settingsDiskSubtree 盘上实体子树（段键名="providers.glm" 等；合并基线/
// 审计 before 的事实源）。文件缺席/坏 TOML/路径不在 → nil（审计如实记
// null；后续 config 原语以同样读失败拒写，不落半形）。
func settingsDiskSubtree(entity string) map[string]any {
	segs := strings.Split(entity, ".")
	raw, err := os.ReadFile(config.ResolveConfigPath(""))
	if err != nil {
		return nil
	}
	var data map[string]any
	if _, err := toml.Decode(string(raw), &data); err != nil {
		return nil
	}
	var cur any = data
	for _, s := range segs {
		t, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = t[s]
		if !ok {
			return nil
		}
	}
	m, _ := cur.(map[string]any)
	return m
}

// ---- ① dock 上游实体 ----

// settingsPutUpstream PUT /settings/dock/upstreams/{名}：新增或整体替换。
// F3 合并 → 条目自键层渲染（字段序随 dock_edit renderUpstreamEntry）→
// SetSectionTOML 条目形落盘（写回校验经 dock 表全量把关）。
func (d *Daemon) settingsPutUpstream(name string, body map[string]any) (map[string]any, error) {
	entity := "dock.upstreams." + name
	cfgPath := config.ResolveConfigPath("")
	return d.settingsPutEntityFlow(entity, func(before map[string]any) (map[string]any, func() error, error) {
		merged, err := settingsMergeSecrets(before, body)
		if err != nil {
			return body, nil, err
		}
		tomlBody, err := settingsUpstreamEntryTOML(merged)
		if err != nil {
			return merged, nil, err
		}
		return merged, func() error { return config.SetSectionTOML(cfgPath, entity, tomlBody) }, nil
	})
}

// settingsDeleteUpstream DELETE /settings/dock/upstreams/{名}：same_model
// 白名单拒删（daemon 层补位，锁内跑）→ config 层 RemoveDockUpstream（active
// 拒删/未知条目在彼，透传）。
func (d *Daemon) settingsDeleteUpstream(name string) (map[string]any, error) {
	cfgPath := config.ResolveConfigPath("")
	return d.settingsDeleteEntityFlow("dock.upstreams."+name,
		func() error {
			if settingsSameModelRef(cfgPath, name) {
				return fmt.Errorf("条目 %q 被 [ferry].same_model.upstreams 白名单引用，拒删（先从白名单摘除再删；原文件未动）", name)
			}
			return nil
		},
		func() error { return config.RemoveDockUpstream(cfgPath, name) })
}

// settingsSameModelRef [ferry.same_model].upstreams 白名单是否引用 name。
// 读失败（文件缺席/坏 TOML）→ false：后续 RemoveDockUpstream 对同源读失败
// 同样拒写（保守侧闭合），此函数不另设报错面。
func settingsSameModelRef(cfgPath, name string) bool {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return false
	}
	var data map[string]any
	if _, err := toml.Decode(string(raw), &data); err != nil {
		return false
	}
	f, _ := data["ferry"].(map[string]any)
	sm, _ := f["same_model"].(map[string]any)
	ups, _ := sm["upstreams"].([]any)
	for _, u := range ups {
		if s, _ := u.(string); s == name {
			return true
		}
	}
	return false
}

// settingsUpstreamEntryTOML 上游条目自键层 TOML 内容行（不含表头；字段序随
// dock_edit renderUpstreamEntry：base_url/api_key 恒落、model_map/text_only/
// balance_url/codex/pi 非空才落、dialect 空=anthropic 归一恒显式）。字段类型
// 不对＝错误（拒写不落半形）。
func settingsUpstreamEntryTOML(body map[string]any) (string, error) {
	baseURL, err := settingsStrField(body, "base_url")
	if err != nil {
		return "", err
	}
	apiKey, err := settingsStrField(body, "api_key")
	if err != nil {
		return "", err
	}
	balanceURL, err := settingsStrField(body, "balance_url")
	if err != nil {
		return "", err
	}
	dialect, err := settingsStrField(body, "dialect")
	if err != nil {
		return "", err
	}
	codex, err := settingsStrField(body, "codex")
	if err != nil {
		return "", err
	}
	pi, err := settingsStrField(body, "pi")
	if err != nil {
		return "", err
	}

	var lines []string
	line := func(k, v string) { lines = append(lines, k+" = "+v) }
	line("base_url", settingsTOMLString(baseURL))
	line("api_key", settingsTOMLString(apiKey))
	if mm, ok := body["model_map"]; ok && mm != nil {
		m, ok := mm.(map[string]any)
		if !ok {
			return "", fmt.Errorf("字段 %q 应为对象", "model_map")
		}
		for k, v := range m {
			if _, ok := v.(string); !ok {
				return "", fmt.Errorf("model_map.%s 的值应为字符串", k)
			}
		}
		if len(m) > 0 {
			v, err := settingsTOMLValue(m)
			if err != nil {
				return "", err
			}
			line("model_map", v)
		}
	}
	if to, ok := body["text_only"]; ok && to != nil {
		arr, ok := to.([]any)
		if !ok {
			return "", fmt.Errorf("字段 %q 应为数组", "text_only")
		}
		for _, v := range arr {
			if _, ok := v.(string); !ok {
				return "", fmt.Errorf("text_only 元素应为字符串")
			}
		}
		if len(arr) > 0 {
			v, err := settingsTOMLValue(arr)
			if err != nil {
				return "", err
			}
			line("text_only", v)
		}
	}
	if balanceURL != "" {
		line("balance_url", settingsTOMLString(balanceURL))
	}
	if dialect == "" {
		dialect = config.DialectAnthropic
	}
	line("dialect", settingsTOMLString(dialect))
	if codex != "" {
		line("codex", settingsTOMLString(codex))
	}
	if pi != "" {
		line("pi", settingsTOMLString(pi))
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// ---- ① providers 实体 ----

// settingsPutProvider PUT /settings/providers/{名}：新增或整体替换（票01
// SetProviderEntry——整条覆盖含 extra_body；window/protocol 缺省归一在彼）。
func (d *Daemon) settingsPutProvider(name string, body map[string]any) (map[string]any, error) {
	entity := "providers." + name
	cfgPath := config.ResolveConfigPath("")
	return d.settingsPutEntityFlow(entity, func(before map[string]any) (map[string]any, func() error, error) {
		merged, err := settingsMergeSecrets(before, body)
		if err != nil {
			return body, nil, err
		}
		entry, err := settingsProviderEntry(merged)
		if err != nil {
			return merged, nil, err
		}
		return merged, func() error { return config.SetProviderEntry(cfgPath, name, entry) }, nil
	})
}

// settingsDeleteProvider DELETE /settings/providers/{名}：config 层守卫
// （[ferry].chain/.provider 引用拒删）透传，未知条目 404。
func (d *Daemon) settingsDeleteProvider(name string) (map[string]any, error) {
	return d.settingsDeleteEntityFlow("providers."+name, nil, func() error {
		return config.RemoveProviderEntry(config.ResolveConfigPath(""), name)
	})
}

// settingsProviderEntry 实体 body → 票01 条目结构（字段类型不对＝错误）。
func settingsProviderEntry(body map[string]any) (config.ProviderEntry, error) {
	var e config.ProviderEntry
	var err error
	if e.BaseURL, err = settingsStrField(body, "base_url"); err != nil {
		return e, err
	}
	if e.Model, err = settingsStrField(body, "model"); err != nil {
		return e, err
	}
	if e.APIKey, err = settingsStrField(body, "api_key"); err != nil {
		return e, err
	}
	if e.Protocol, err = settingsStrField(body, "protocol"); err != nil {
		return e, err
	}
	if w, ok, err := settingsNumField(body, "window"); err != nil {
		return e, err
	} else if ok {
		e.Window = int(w)
	}
	if eb, ok := body["extra_body"]; ok && eb != nil {
		m, ok := eb.(map[string]any)
		if !ok {
			return e, fmt.Errorf("字段 %q 应为对象", "extra_body")
		}
		e.ExtraBody = m
	}
	return e, nil
}

// ---- ① prices 实体 ----

// settingsPutPrice PUT /settings/prices/{键}：新增或整组替换（versions 子表
// 整组覆盖——票01 SetPriceEntry）。
func (d *Daemon) settingsPutPrice(key string, body map[string]any) (map[string]any, error) {
	entity := "prices." + key
	cfgPath := config.ResolveConfigPath("")
	return d.settingsPutEntityFlow(entity, func(before map[string]any) (map[string]any, func() error, error) {
		merged, err := settingsMergeSecrets(before, body)
		if err != nil {
			return body, nil, err
		}
		entry, err := settingsPriceEntry(merged)
		if err != nil {
			return merged, nil, err
		}
		return merged, func() error { return config.SetPriceEntry(cfgPath, key, entry) }, nil
	})
}

// settingsDeletePrice DELETE /settings/prices/{键}：versions 子树随条目整树
// 摘除（票01 RemovePriceEntry），未知键 404。
func (d *Daemon) settingsDeletePrice(key string) (map[string]any, error) {
	return d.settingsDeleteEntityFlow("prices."+key, nil, func() error {
		return config.RemovePriceEntry(config.ResolveConfigPath(""), key)
	})
}

// settingsPriceEntry 实体 body → 票01 价目结构（p_cache null/缺省=nil=无
// 缓存价，不落盘）。
func settingsPriceEntry(body map[string]any) (config.PriceEntry, error) {
	var e config.PriceEntry
	var err error
	if e.Unit, err = settingsStrField(body, "unit"); err != nil {
		return e, err
	}
	if per, ok, err := settingsNumField(body, "per"); err != nil {
		return e, err
	} else if ok {
		e.Per = int(per)
	}
	if raw, ok := body["versions"]; ok && raw != nil {
		arr, ok := raw.([]any)
		if !ok {
			return e, fmt.Errorf("字段 %q 应为数组", "versions")
		}
		for i, v := range arr {
			m, ok := v.(map[string]any)
			if !ok {
				return e, fmt.Errorf("versions[%d] 应为对象", i)
			}
			var ver config.PriceVersionEntry
			if ver.EffectiveFrom, err = settingsStrField(m, "effective_from"); err != nil {
				return e, fmt.Errorf("versions[%d].%w", i, err)
			}
			pin, ok, err := settingsNumField(m, "p_in")
			if err != nil {
				return e, fmt.Errorf("versions[%d].%w", i, err)
			}
			if ok {
				ver.PIn = pin
			}
			pout, ok, err := settingsNumField(m, "p_out")
			if err != nil {
				return e, fmt.Errorf("versions[%d].%w", i, err)
			}
			if ok {
				ver.POut = pout
			}
			if cv, has := m["p_cache"]; has && cv != nil {
				f, ok := settingsAsFloat(cv)
				if !ok {
					return e, fmt.Errorf("versions[%d].字段 %q 应为数值", i, "p_cache")
				}
				ver.PCache = &f
			}
			e.Versions = append(e.Versions, ver)
		}
	}
	return e, nil
}

// ---- ④ GET /settings/ferry：盘上对账读面 ----

// handleSettingsFerryGet GET /settings/ferry：ferry 节盘上现值（含 same_model
// 子树，出口统一脱敏——ferry 现无密钥键，毒名单走查兜未来）。PUT /settings/
// ferry（自键层写，settings_write.go）后的对账入口；鉴权随 doGet 全局面。
func handleSettingsFerryGet(dl DaemonLike, w http.ResponseWriter, r *http.Request) {
	if _, ok := dl.(*Daemon); !ok {
		notFound(w)
		return
	}
	sec := settingsDiskSection(config.ResolveConfigPath(""), "ferry")
	if sec == nil {
		sec = map[string]any{}
	}
	maskSecretLeaves(sec)
	writeJSON(w, http.StatusOK, sec)
}

// ---- body 字段抽取 ----

// settingsStrField 字符串字段：缺省/nil=空串；非字符串（数字/数组/对象）＝
// 错误点名键名。
func settingsStrField(body map[string]any, k string) (string, error) {
	v, ok := body[k]
	if !ok || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("字段 %q 应为字符串", k)
	}
	return s, nil
}

// settingsNumField 数值字段：缺省/nil=(0,false,nil)；非数值＝错误点名键名。
func settingsNumField(body map[string]any, k string) (float64, bool, error) {
	v, ok := body[k]
	if !ok || v == nil {
		return 0, false, nil
	}
	f, ok := settingsAsFloat(v)
	if !ok {
		return 0, false, fmt.Errorf("字段 %q 应为数值", k)
	}
	return f, true, nil
}
