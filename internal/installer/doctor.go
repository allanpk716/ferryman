// doctor.go — ferryman doctor：一键体检（规格 ferryman/doctor.py 1:1，票20）。
//
// 2026-09-17 两类事故的直接解药——都是"静默失效"，出问题时表面毫无异常：
//   - CC Switch 切换/重写抹掉 settings.json 里的钩子（T21）；
//   - 生成文件时反斜杠转义被写成控制字符（\a→BEL、\f→FF），钩子指向不存在路径。
//
// 检查项全部纯函数化（路径/探针可注入）；runDoctor 聚合并打印 ✓/✗。
//
// Go 新形态：
//   - CheckCCHooks 闸门事件（UserPromptSubmit）缺位 = 提示不失败（C12 用户
//     策略：正文注明"闸门钩子按用户指令未安装"）；其余三事件缺失/路径不
//     存在/SessionStart 超时 <10s 照旧失败；
//   - CheckLauncher 查点火脚本 start 行的 exe 路径有效性（Python 时代查
//     venv python.exe，Go 新形态查合并 exe）；
//   - HttpBeatSender 功能退化声明（评审附录#14）：信息行输出，不判 FAIL。
package installer

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"ferryman/internal/config"
	"ferryman/internal/ferry"
)

// Check 单检查项结论。
type Check struct {
	OK  bool
	Msg string
}

// HttpBeatNotice HttpBeatSender 功能退化声明（评审附录#14）：心跳真实发送
// 未实装（Q14 未授权），enforce 模式自动回落 observe 演练。信息行，不计入
// 检查项、不判 FAIL。
const HttpBeatNotice = "[提示] 心跳真实发送未实装（Q14 未授权），enforce 模式自动回落 observe 演练"

// 条目 JSON 里 -File "<path>" 的两种形（json 转义串 / 原文；doctor.py 正则逐字）。
var (
	escPathRe   = regexp.MustCompile(`-File \\"(.*?)\\"`)
	plainPathRe = regexp.MustCompile(`-File "(.*?)"`)
	// startExeRe 点火脚本 start 行的 exe 路径（Go 新形态 EnsureLauncher 产物）。
	startExeRe = regexp.MustCompile(`start "" /min "([^"]+)"`)
)

// CheckCCHooks settings.json 四事件齐全、路径存在、restore 超时够自举
// （doctor.py check_cc_hooks 逐字 + C12 闸门缺位提示不失败）。Python 形参
// repo 从未参与检查（路径取 settings.json 内嵌绝对路径），Go 面去除。
func CheckCCHooks(settingsPath string) Check {
	if _, err := os.Stat(settingsPath); err != nil {
		return Check{false, fmt.Sprintf("%s 不存在", settingsPath)}
	}
	rawData, err := os.ReadFile(settingsPath)
	if err != nil {
		return Check{false, fmt.Sprintf("%s 不存在", settingsPath)}
	}
	var root map[string]any
	if err := json.Unmarshal(rawData, &root); err != nil {
		return Check{false, fmt.Sprintf("settings.json 解析失败: %v", err)}
	}
	hooks, _ := root["hooks"].(map[string]any)
	var missing []string
	for _, evt := range FerryEvents {
		if !eventHasFerryman(hooks, evt) {
			missing = append(missing, evt)
		}
	}
	gateMissing := false
	var others []string
	for _, m := range missing {
		if m == GateEvent {
			gateMissing = true
		} else {
			others = append(others, m)
		}
	}
	if len(others) > 0 {
		// 文案逐字（missing 列全量——含闸门；闸门缺位只在"仅缺闸门"时放行）
		return Check{false, fmt.Sprintf("settings.json 缺 ferryman 钩子事件: %s"+
			"（CC Switch 切换会抹钩子——跑 install-cc / install-ccswitch）",
			strings.Join(missing, ", "))}
	}
	// 路径存在性 + restore 超时（自举等待预算）
	for _, evt := range FerryEvents {
		for _, entry := range asList(hooks[evt]) {
			entryObj, isObj := entry.(map[string]any)
			if !isObj {
				continue
			}
			blob := marshalCompact(entry)
			if !strings.Contains(blob, "ferryman") {
				continue
			}
			m := escPathRe.FindStringSubmatch(blob)
			if m == nil {
				m = plainPathRe.FindStringSubmatch(blob)
			}
			if m == nil || !pathExists(m[1]) {
				detail := ""
				if m != nil {
					detail = m[1]
				} else {
					detail = truncateRunes(blob, 60)
				}
				return Check{false, fmt.Sprintf("%s: 钩子脚本路径不存在（%s）", evt, detail)}
			}
			if evt == "SessionStart" && firstTimeout(entryObj) < 10 {
				return Check{false, "SessionStart 超时 <10s（含 daemon 自举等待会不够——重跑 install-cc）"}
			}
		}
	}
	if gateMissing {
		// C12 用户策略：闸门钩子按用户指令未安装——提示不失败
		return Check{true, "settings.json 缺闸门钩子 UserPromptSubmit——" +
			"闸门钩子按用户指令未安装（C12：防子代理久跑场景主会话输入被吞），提示不判失败；其余三事件在位"}
	}
	return Check{true, "settings.json 四钩子在位"}
}

// CheckHookScripts BOM 在位（PS5.1 中文注释地雷）+ 无控制字符（\a→BEL / \f→FF
// 事故）（doctor.py check_hook_scripts 逐字节）。
func CheckHookScripts(paths []string) []Check {
	out := []Check{}
	for _, p := range paths {
		name := filepath.Base(p)
		rawData, err := os.ReadFile(p)
		if err != nil {
			out = append(out, Check{false, name + ": 不存在"})
			continue
		}
		if !bytes.HasPrefix(rawData, []byte{0xEF, 0xBB, 0xBF}) {
			out = append(out, Check{false, name + ": 缺 UTF-8 BOM（PS5.1 按 ANSI 解析中文注释会炸）"})
			continue
		}
		var bad []string
		for _, b := range []byte{0x07, 0x08, 0x0B, 0x0C, 0x1B} {
			if bytes.IndexByte(rawData, b) >= 0 {
				bad = append(bad, fmt.Sprintf("'0x%x'", b))
			}
		}
		if len(bad) > 0 {
			out = append(out, Check{false, fmt.Sprintf("%s: 含控制字符 [%s]"+
				"（路径转义事故——重跑 install 修复）", name, strings.Join(bad, ", "))})
			continue
		}
		out = append(out, Check{true, name + ": BOM/无控制字符"})
	}
	return out
}

// CheckCCSwitch 全部 claude 供应商快照都带 ferryman 钩子（切换=逐字写入，
// 缺了就会被抹）（doctor.py check_ccswitch 逐字）。
func CheckCCSwitch(dbPath string) Check {
	if _, err := os.Stat(dbPath); err != nil {
		return Check{true, "未装 CC Switch（跳过）"}
	}
	rows, err := readClaudeProviders(dbPath, 5000)
	if err != nil {
		return Check{false, fmt.Sprintf("cc-switch.db 读取失败: %v", err)}
	}
	var lacking []string
	for _, r := range rows {
		if !snapshotHasAllEvents(r.Raw) {
			lacking = append(lacking, r.Name)
		}
	}
	if len(lacking) > 0 {
		return Check{false, fmt.Sprintf("供应商快照缺钩子: %s（重跑 install-ccswitch）",
			strings.Join(lacking, ", "))}
	}
	return Check{true, fmt.Sprintf("CC Switch %d 个 claude 快照全带钩子", len(rows))}
}

// snapshotHasAllEvents 单快照四事件全覆盖判定（NULL/坏 JSON/非 dict 记缺——
// 该快照需要的正是重注入）。
func snapshotHasAllEvents(raw sql.NullString) bool {
	var root map[string]any
	if !raw.Valid || json.Unmarshal([]byte(raw.String), &root) != nil {
		return false
	}
	hooks, _ := root["hooks"].(map[string]any)
	for _, evt := range FerryEvents {
		if !eventHasFerryman(hooks, evt) {
			return false
		}
	}
	return true
}

// CheckCodex Codex：钩子条目在位 + [features] hooks = true（默认关，不开则
// 整包静默失效）（doctor.py check_codex 逐字）。
func CheckCodex(hooksPath, configPath string) Check {
	var problems []string
	if _, err := os.Stat(hooksPath); err != nil {
		problems = append(problems, "hooks.json 不存在（跑 install-codex）")
	} else {
		rawData, err := os.ReadFile(hooksPath)
		var root map[string]any
		if err != nil || json.Unmarshal(rawData, &root) != nil {
			detail := err
			if rawData != nil {
				if uerr := json.Unmarshal(rawData, &root); uerr != nil {
					detail = uerr
				}
			}
			problems = append(problems, fmt.Sprintf("hooks.json 解析失败: %v", detail))
		} else {
			hooks, _ := root["hooks"].(map[string]any)
			var missing []string
			for _, evt := range FerryEvents {
				if !eventHasFerryman(hooks, evt) {
					missing = append(missing, evt)
				}
			}
			if len(missing) > 0 {
				problems = append(problems, fmt.Sprintf("hooks.json 缺: %s（跑 install-codex）",
					strings.Join(missing, ", ")))
			}
		}
	}
	tomlRaw, _ := os.ReadFile(configPath) // 不存在 = 空（Python 同位）
	if !tomlHasHooksFlag(string(tomlRaw)) {
		problems = append(problems, "[features] hooks = true 未开（Codex 钩子默认关闭）")
	}
	if len(problems) > 0 {
		return Check{false, strings.Join(problems, "；")}
	}
	return Check{true, "Codex 钩子+旗标在位"}
}

// CheckDaemon probe() 返回 /stats dict（活）或 nil（死）（doctor.py check_daemon 逐字）。
func CheckDaemon(probe func() map[string]any, pidFile string) Check {
	st := probe()
	if st == nil {
		return Check{false, "daemon 未运行（钩子自举会拉起，或手动 start-daemon.cmd）"}
	}
	extra := ""
	if rawData, err := os.ReadFile(pidFile); err == nil {
		var pid struct {
			PID int `json:"pid"`
		}
		if json.Unmarshal(rawData, &pid) == nil {
			extra = fmt.Sprintf("（pid %d）", pid.PID)
		}
	}
	alert := " · ok"
	if v, ok := st["health_alert"].(bool); ok && v {
		alert = " · ⚠ 健康告警: 疑似钩子失效"
	}
	return Check{true, "daemon 活着" + extra + alert}
}

// CheckFerryProvider 摆渡 provider 已配置且在 [providers.*] 有定义（T39 去内置
// 默认后的新静默失效点）（doctor.py check_ferry_provider 逐字）。
func CheckFerryProvider(name string, providers map[string]ferry.Provider) Check {
	if name == "" {
		return Check{false, "[ferry] provider 未配置——摆渡永远降级骨架" +
			"（复制 config.example.toml 到 ~/ferryman/config.toml）"}
	}
	if _, ok := providers[name]; !ok {
		return Check{false, fmt.Sprintf("[ferry] provider '%s' 未在 [providers.*] 定义", name)}
	}
	return Check{true, fmt.Sprintf("摆渡 provider '%s' 在位", name)}
}

// CheckLauncher 点火脚本在位且其 exe 路径有效（钩子自举的地基；doctor.py
// check_launcher 的 Go 新形态：脚本内 start 行的 exe 路径存在）。
func CheckLauncher(path string) Check {
	if _, err := os.Stat(path); err != nil {
		return Check{false, fmt.Sprintf("%s 不存在（重跑 install-cc 生成点火脚本）", path)}
	}
	rawData, err := os.ReadFile(path)
	if err != nil {
		rawData = nil
	}
	if m := startExeRe.FindStringSubmatch(string(rawData)); m != nil {
		if _, err := os.Stat(m[1]); err != nil {
			return Check{false, fmt.Sprintf("点火脚本 exe 路径失效: %s", m[1])}
		}
	}
	return Check{true, LauncherName + " 在位"}
}

// doctorDeps runDoctor 的可注入面（测试密闭；真实入口 RunDoctor 装配真值）。
type doctorDeps struct {
	Home, Repo              string
	CCSwitchDB              string
	CodexHooks, CodexConfig string
	LoadCfg                 func() (*config.Config, error)
	LoadProviders           func() (map[string]ferry.Provider, error)
	Probe                   func() map[string]any
	Out                     io.Writer
}

// RunDoctor 一键体检真实入口（HOME/exe 面）；返回进程退出码（有 FAIL → 1）。
func RunDoctor() int {
	home := homeDir()
	return runDoctor(doctorDeps{
		Home:        home,
		Repo:        repoRoot(),
		CCSwitchDB:  CCSwitchDBPath(home),
		CodexHooks:  CodexHooksPath(home),
		CodexConfig: CodexConfigPath(home),
		LoadCfg:     func() (*config.Config, error) { return config.Load("", false) },
		LoadProviders: func() (map[string]ferry.Provider, error) {
			return ferry.LoadProviders("")
		},
		Probe: realStatsProbe(filepath.Join(home, "ferryman")),
		Out:   os.Stdout,
	})
}

// doctorScriptNames 体检的钩子脚本清单（doctor.py run_doctor scripts 逐字）。
func doctorScriptNames() []string {
	return []string{
		"ferryman-gate.ps1", "ferryman-restore.ps1", "ferryman-subagent.ps1",
		"ferryman-ensure.ps1", "ferryman-gate-codex.ps1",
		"ferryman-restore-codex.ps1", "ferryman-subagent-codex.ps1"}
}

// runDoctor 聚合检查并打印（doctor.py run_doctor 逐字 + 附录#14 声明行）。
func runDoctor(d doctorDeps) int {
	dataDir := filepath.Join(d.Home, "ferryman")
	results := []Check{}
	results = append(results, CheckCCHooks(filepath.Join(d.Home, ".claude", "settings.json")))
	results = append(results, CheckLauncher(filepath.Join(dataDir, LauncherName)))
	results = append(results, CheckCCSwitch(d.CCSwitchDB))
	// 配置坏要让 doctor 报出来而非崩（Python try/except 同形）
	cfg, err := d.LoadCfg()
	if err != nil {
		results = append(results, Check{false, fmt.Sprintf("摆渡配置加载失败: %v", err)})
	} else if providers, err := d.LoadProviders(); err != nil {
		results = append(results, Check{false, fmt.Sprintf("摆渡配置加载失败: %v", err)})
	} else {
		results = append(results, CheckFerryProvider(cfg.FerryProvider, providers))
	}

	scripts := []string{}
	for _, n := range doctorScriptNames() {
		scripts = append(scripts, filepath.Join(d.Repo, "hooks", n))
	}
	results = append(results, CheckHookScripts(scripts)...)
	results = append(results, CheckCodex(d.CodexHooks, d.CodexConfig))
	results = append(results, CheckDaemon(d.Probe, filepath.Join(dataDir, "daemon.pid")))

	fails := 0
	for _, r := range results {
		tag := "[FAIL] "
		if r.OK {
			tag = "[OK]   "
		}
		fmt.Fprintln(d.Out, tag+r.Msg)
		if !r.OK {
			fails++
		}
	}
	fmt.Fprintln(d.Out)
	fmt.Fprintln(d.Out, HttpBeatNotice) // 附录#14：功能退化声明——信息行不判 FAIL
	conclusion := fmt.Sprintf("体检结论: %d/%d 通过", len(results)-fails, len(results))
	if fails > 0 {
		conclusion += "——有问题见上"
	}
	fmt.Fprintln(d.Out, conclusion)
	if fails > 0 {
		return 1
	}
	return 0
}

// realStatsProbe /stats 探针：Bearer token 读 data_dir，2s 超时；任何失败 → nil
// （doctor.py real_probe 逐字；端口 7311 硬编码同 Python）。
func realStatsProbe(dataDir string) func() map[string]any {
	return func() map[string]any {
		tokenRaw, err := os.ReadFile(filepath.Join(dataDir, "daemon.token"))
		if err != nil {
			return nil
		}
		req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:7311/stats", nil)
		if err != nil {
			return nil
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tokenRaw)))
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return nil
		}
		var st map[string]any
		if json.Unmarshal(data, &st) != nil {
			return nil
		}
		return st
	}
}

// ---- 检查器共享小件 ----

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// eventHasFerryman 事件条目数组里任一 JSON 含 "ferryman"（Python any(...) 逐字）。
func eventHasFerryman(hooks map[string]any, evt string) bool {
	for _, e := range asList(hooks[evt]) {
		if strings.Contains(marshalCompact(e), "ferryman") {
			return true
		}
	}
	return false
}

func asList(v any) []any {
	lst, _ := v.([]any)
	return lst
}

// firstTimeout entry["hooks"][0]["timeout"]（Python entry.get("hooks",[{}])[0]
// .get("timeout",0) 同位；形状不齐 → 0）。
func firstTimeout(entry map[string]any) int {
	inner := asList(entry["hooks"])
	if len(inner) == 0 {
		return 0
	}
	hook, _ := inner[0].(map[string]any)
	if v, ok := hook["timeout"].(float64); ok {
		return int(v)
	}
	return 0
}

// truncateRunes 前 n 个码点（Python blob[:60] 同为码点切）。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
