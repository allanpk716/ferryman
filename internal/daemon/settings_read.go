package daemon

// settings_read.go — 设置视图票02：GET /settings 只读端点（设置视图渲染用，
// spec .scratch/settings-view-impl/spec.md「读面」条）。
//
// 响应三块 + 顶层 effects：
//   - config：守护内存生效配置（d.Cfg——Load 装载、已填默认、已过校验，即
//     「现在跑着的是什么」）全部 11 节，键名对齐 config.toml 蛇形；[dock] 节
//     缺失如实缺席（F11：nil=渡口不启动）。注意：设置写面落盘后、重启前，
//     本块与盘上有差——差况由 effects 徽章解释（操作级语义）。
//   - 三类实体集合（spec 写面实体路径的读侧镜像）：
//       dock.upstreams → config.dock.upstreams（[dock.upstreams.<名>]）；
//       providers      → 顶层 providers（[providers.*]，ferry.LoadProviders，
//                        路径与 serve 装配同源 ResolveConfigPath）；
//       prices         → 顶层 prices（[prices.*]，prices.LoadPrices）。
//     前两者不在 Config 结构体里（config 包只拿 [providers] 做 chain 校验），
//     故从盘上按生产同源路径现读——与 /config_tuning 读盘先例一致。
//   - effects：生效元数据，操作级（spec 定案）：provider_switch 与 tuning
//     即时生效（needs_restart=false）；其余一切写操作 needs_restart=true；
//     新增上游条目后对其首次 switch 亦需重启——独立字段
//     new_upstream_first_switch 标注（switchTo 只认启动时已加载条目）。
//     UI 数字对账以本端点为唯一事实源。
//
// 脱敏红线（queryapi.go 顶部块全文适用，此处按票面加严执行）：凡命中毒名单
// 正则的键，值一律替换为 {masked:"••••"+尾四位, has_key:bool}，永不回明文。
// 掩码走通用递归（出口统一过一遍毒名单走查），不是逐字段点名——新增密钥
// 字段自动被覆盖；毒名单正则与 viewer/server/config.go、query_config_tuning.go
// 同款（两处先例，本文件独立第三份——包间不互依，改动须三处同步）。
//
// 纯只读：无任何状态写入；配置缺席/解析失败如实空集合，不编造、不回 5xx。

import (
	"net/http"
	"regexp"

	"ferryman/internal/config"
	"ferryman/internal/ferry"
	"ferryman/internal/prices"
)

// 密钥键名毒名单（viewer/server/config.go 同款，三处同步）：broad 抓复合词，
// tail 抓"末段就是 key/token/auth"的键——末段锚定避免误伤数量词
// （min_ctx_tokens 是阈值不是密钥）。
var (
	setSecretBroad = regexp.MustCompile(`(?i)(api[_-]?key|apikey|access[_-]?key|secret|passw(or)?d|credential)`)
	setSecretTail  = regexp.MustCompile(`(?i)(^|[._-])(key|token|auth)$`)
)

// setIsSecretKey 键名是否命中毒名单。
func setIsSecretKey(k string) bool {
	return setSecretBroad.MatchString(k) || setSecretTail.MatchString(k)
}

// setMaskSecret 掩码口径（本票钉死，settings 读面统一新口径，取代
// /config_tuning 的前3后4旧口径——旧文件不动）：••••+尾四位；rune 数 ≤4 时
// 尾四位即全钥——整段全掩（8 颗点），绝不出现明文子串；空值 masked=""、
// has_key=false（UI 据此显"未设置"）。
func setMaskSecret(v string) (string, bool) {
	r := []rune(v)
	if len(r) == 0 {
		return "", false
	}
	if len(r) <= 4 {
		return "••••••••", true
	}
	return "••••" + string(r[len(r)-4:]), true
}

// maskSecretLeaves 出口统一脱敏走查：递归整棵响应树，凡毒名单键下的字符串
// 叶一律替换为 {masked, has_key} 对象。表/数组照常下钻（实体集合逐条目过
// 刷）；非串标量不动（密钥只可能是串；条目名等表键不在脱敏域）。
func maskSecretLeaves(m map[string]any) {
	for k, v := range m {
		switch t := v.(type) {
		case map[string]any:
			maskSecretLeaves(t)
		case []any:
			maskSecretSlice(t)
		case string:
			if setIsSecretKey(k) {
				masked, has := setMaskSecret(t)
				m[k] = map[string]any{"masked": masked, "has_key": has}
			}
		}
	}
}

func maskSecretSlice(arr []any) {
	for _, e := range arr {
		if t, ok := e.(map[string]any); ok {
			maskSecretLeaves(t)
		}
	}
}

// ---- GET /settings ----

// handleSettingsRead 入口：全量配置 JSON + 三实体集合 + effects，一次回话。
// DaemonLike 断言（dispatchQuery 同语义）：替身无设置读面，落 notFound。
func handleSettingsRead(dl DaemonLike, w http.ResponseWriter, r *http.Request) {
	d, ok := dl.(*Daemon)
	if !ok {
		notFound(w)
		return
	}
	cfgPath := config.ResolveConfigPath("")
	resp := map[string]any{
		"config":    settingsConfigSections(d.Cfg),
		"providers": settingsProviders(cfgPath),
		"prices":    settingsPrices(cfgPath),
		"effects":   settingsEffects(),
	}
	maskSecretLeaves(resp) // 出口统一脱敏（含 providers/extra_body 等任意深处）
	writeJSON(w, http.StatusOK, resp)
}

// settingsConfigSections 内存生效配置全节（键名对齐 config.toml 蛇形）。
// 密钥值先按明文放入，出口 maskSecretLeaves 统一掩。
func settingsConfigSections(cfg *config.Config) map[string]any {
	sections := map[string]any{
		"gate": map[string]any{
			"cc_mode":    cfg.GateCC,
			"codex_mode": cfg.GateCodex,
			"dsh_mode":   cfg.GateDsh, // 空=未设置（闸门处决点回落 codex_mode）
		},
		"thresholds": map[string]any{
			"summarize_s":    cfg.Thresholds.SummarizeS,
			"block_s":        cfg.Thresholds.BlockS,
			"min_ctx_tokens": cfg.Thresholds.MinCtxTokens,
			"cache_warn_s":   cfg.Thresholds.CacheWarnS,
		},
		"watch": map[string]any{
			"poll_interval_s":    cfg.Watch.PollIntervalS,
			"cc_projects_dir":    cfg.Watch.CCProjectsDir,
			"codex_sessions_dir": cfg.Watch.CodexSessionsDir,
			"codex_extra_dirs":   cfg.Watch.CodexExtraDirs,
			"dsh_sessions_dir":   cfg.Watch.DshSessionsDir,
			"harvest_usage":      cfg.Watch.HarvestUsage,
		},
		"server": map[string]any{
			"port":     cfg.Server.Port,
			"data_dir": cfg.Server.DataDir,
		},
		"notify": map[string]any{
			"enabled":        cfg.Notify.Enabled,
			"pushover":       cfg.Notify.Pushover,
			"pushover_token": cfg.Notify.PushoverToken, // 出口统一掩
			"pushover_user":  cfg.Notify.PushoverUser,  // 毒名单不命中：用户标识非密钥
			"toast":          cfg.Notify.Toast,
		},
		"heartbeat": map[string]any{
			"enabled":         cfg.Heartbeat.Enabled,
			"ttl_s":           cfg.Heartbeat.TTLS,
			"ttl_measured_at": cfg.Heartbeat.TTLMeasuredAt,
			"ttl_source":      cfg.Heartbeat.TTLSource,
		},
		"question_watch": map[string]any{
			"mode":                  cfg.QuestionWatch.Mode,
			"min_questions":         cfg.QuestionWatch.MinQuestions,
			"beat_interval_s":       cfg.QuestionWatch.BeatIntervalS,
			"max_beats":             cfg.QuestionWatch.MaxBeats,
			"ferry_deadline_lead_s": cfg.QuestionWatch.FerryDeadlineLeadS,
			"dsh_mode":              cfg.QuestionWatch.DshMode,
		},
		"wait_window": map[string]any{
			"mode":              cfg.WaitWindow.Mode,
			"manual_wait_cap_s": cfg.WaitWindow.ManualWaitCapS,
		},
		"ferry": map[string]any{
			"provider": cfg.FerryProvider,
			"chain":    cfg.FerryChain,
			"same_model": map[string]any{
				"enabled":       cfg.SameModel.Enabled,
				"upstreams":     cfg.SameModel.Upstreams,
				"threshold_min": cfg.SameModel.ThresholdMin,
				"ceiling_min":   cfg.SameModel.CeilingMin,
			},
		},
		"tuning": map[string]any{
			"mode":        cfg.Tuning.Mode,
			"window_days": cfg.Tuning.WindowDays,
			"min_events":  cfg.Tuning.MinEvents,
		},
	}
	if cfg.Dock != nil { // nil=[dock] 节缺失＝渡口不启动（F11）——如实缺席
		sections["dock"] = settingsDockSection(cfg.Dock)
	}
	return sections
}

// settingsDockSection [dock] 节（含实体集合一：upstreams 条目表）。
func settingsDockSection(dk *config.DockCfg) map[string]any {
	ups := map[string]any{}
	for name, up := range dk.Upstreams {
		ups[name] = map[string]any{
			"base_url":    up.BaseURL,
			"api_key":     up.APIKey, // 出口统一掩
			"model_map":   up.ModelMap,
			"text_only":   up.TextOnly,
			"balance_url": up.BalanceURL,
			"dialect":     up.Dialect,
			"codex":       up.Codex,
			"pi":          up.Pi,
		}
	}
	return map[string]any{
		"listen":            dk.Listen,
		"active":            dk.Active,
		"upstream_base_url": dk.UpstreamBaseURL, // 旧单值兜底字段（装配不读，展示保留）
		"api_key":           dk.APIKey,          // 旧单值：出口统一掩
		"model_map":         dk.ModelMap,
		"text_only":         dk.TextOnly,
		"balance_url":       dk.BalanceURL,
		"drain_timeout_s":   dk.DrainTimeoutS,
		"bind_retry_s":      dk.BindRetryS,
		"upstreams":         ups, // 实体集合一：dock.upstreams
	}
}

// settingsProviders 实体集合二：摆渡供应商（[providers.*]，盘上现读，路径与
// serve 装配同源）。坏 TOML 如实空表（serve.go 装配同款兜底——面板不炸）。
func settingsProviders(cfgPath string) map[string]any {
	ps, err := ferry.LoadProviders(cfgPath)
	if err != nil {
		ps = map[string]ferry.Provider{}
	}
	out := map[string]any{}
	for name, p := range ps {
		out[name] = map[string]any{
			"name":       p.Name,
			"base_url":   p.BaseURL,
			"model":      p.Model,
			"api_key":    p.APIKey, // 出口统一掩
			"window":     p.Window,
			"protocol":   p.Protocol,
			"extra_body": p.ExtraBody, // 透传字典；nil=无（深处密钥由出口走查兜住）
		}
	}
	return out
}

// settingsPrices 实体集合三：价格表（[prices.*]，盘上现读）。p_cache nil=
// 无缓存价（如实透传 null——策略计算器据此拒算，面板不替它编数）。
func settingsPrices(cfgPath string) map[string]any {
	books := prices.LoadPrices(cfgPath)
	out := map[string]any{}
	for key, b := range books {
		versions := make([]map[string]any, 0, len(b.Versions))
		for _, v := range b.Versions {
			versions = append(versions, map[string]any{
				"effective_from": v.EffectiveFrom,
				"p_in":           v.PIn,
				"p_cache":        v.PCache,
				"p_out":          v.POut,
			})
		}
		out[key] = map[string]any{
			// 标识字段名用 "name" 不用 "key"：裸 "key" 撞毒名单尾段正则，会被
			// 出口走查连带掩掉（键名即标识，本字段只是镜像，随 providers 条目同款）。
			"name":     b.Key,
			"unit":     b.Unit,
			"per":      b.Per,
			"versions": versions,
		}
	}
	return out
}

// settingsEffects 生效元数据（spec「读面」操作级语义，UI 生效徽章唯一事实源）：
//   - provider_switch / tuning 即时生效（needs_restart=false）；
//   - 其余一切写操作 needs_restart=true；
//   - new_upstream_first_switch 独立字段：新增上游条目后对其首次热切换仍需
//     先重启（switchTo 只认启动时已加载条目，spec Further Notes 已含 UI 文案）。
func settingsEffects() map[string]any {
	eff := func(restart bool) map[string]any {
		return map[string]any{"needs_restart": restart}
	}
	return map[string]any{
		"provider_switch": eff(false),
		"tuning":          eff(false),
		// 独立标注：热切换本身即时，但新条目首次切换例外（启动快照里没有它）。
		"new_upstream_first_switch": eff(true),
		// 以下全部需重启（九节 + 三实体 + 还原）。
		"gate":           eff(true),
		"thresholds":     eff(true),
		"watch":          eff(true),
		"notify":         eff(true),
		"heartbeat":      eff(true),
		"question_watch": eff(true),
		"wait_window":    eff(true),
		"server":         eff(true),
		"dock_upstreams": eff(true),
		"providers":      eff(true),
		"prices":         eff(true),
		"restore":        eff(true),
	}
}
