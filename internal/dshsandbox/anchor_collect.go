// anchor_collect.go — 契约锚采集（票面第 4 条）：从沙箱实际形状采集四类契约
// 面签名，交 dshledger（票03 Write/DiffFaces；键＝Face* 四常量，锚结构校验
// 要求恰含四面——多一面少一面 Write 响亮拒绝）。
//
// 采集面＝静态形状（备料快照与生成面——确定性、对插件侧漂移敏感）＋动态
// 观察（探针过程的 daemon/账本事实——对宿主侧漂移敏感；「形状不变行为变的
// 漂移只能靠运行时验证抓」，spec §3.4）。浏览器半面在无头探针下无挂载可观察
// 面（client.js 只在浏览器加载）——采静态形状并明示动态面缺席，不伪造。
package dshsandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"ferryman/internal/dshledger"
)

// collectFaces 四面采集（ProbeResult.Faces 的原料；值全 JSON 安全）。
func collectFaces(s *Stack, tf TraceFacts, cmd, svc LaneFact, obs *probeObs) map[string]any {
	return map[string]any{
		dshledger.FaceDiscovery: discoveryFace(s),
		dshledger.FaceEvents:    eventsFace(s, tf, obs),
		dshledger.FaceDuck:      duckFace(s, cmd, svc, obs),
		dshledger.FaceBrowser:   browserFace(s),
	}
}

// ---- 发现面：package.json dsh manifest 形状＋patch insert 行＋junction 布局 ----

func discoveryFace(s *Stack) map[string]any {
	face := map[string]any{}
	// manifest 形状（备料快照的 package.json——真相源＝plugin/ferryman-dsh）。
	pkgPath := filepath.Join(s.PluginSnap, "package.json")
	if raw, err := os.ReadFile(pkgPath); err == nil {
		var doc map[string]any
		if json.Unmarshal(raw, &doc) == nil {
			face["plugin_version"] = mapStr(doc, "version")
			face["main"] = mapStr(doc, "main")
			if dsh, ok := doc["dsh"].(map[string]any); ok {
				keys := make([]string, 0, len(dsh))
				for k := range dsh {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				face["dsh_manifest_keys"] = keys
			} else {
				face["dsh_manifest_keys"] = nil
			}
			if exports, ok := doc["exports"].(map[string]any); ok {
				keys := make([]string, 0, len(exports))
				for k := range exports {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				face["exports_keys"] = keys
			}
		}
	}
	// patch insert 行（生成面形状——含合成组成两行，漂移＝生成面改版）。
	patch, _ := os.ReadFile(filepath.Join(s.Home, "profiles", "web", "cordis.patch.yml"))
	pt := string(patch)
	face["patch_inserts"] = map[string]bool{
		"ferryman-dsh":              strings.Contains(pt, "id: ferryman-dsh"),
		"compaction-profile":        strings.Contains(pt, "id: compaction-profile"),
		"session-persistence-jsonl": strings.Contains(pt, "id: session-persistence-jsonl"),
		"compression_none":          strings.Contains(pt, "compression: none"),
	}
	// junction 布局（镜像生产安装形态的核心钉点）。
	link := filepath.Join(s.Home, "profiles", "web", "ferryman-dsh")
	if target, err := os.Readlink(link); err == nil {
		resolved := resolveLinkTarget(filepath.Dir(link), target)
		face["junction_resolves_snapshot"] = strings.EqualFold(
			filepath.Clean(resolved), filepath.Clean(s.PluginSnap))
	}
	nmExists := dirExists(filepath.Join(s.Home, "profiles", "web", "node_modules", "ferryman-dsh"))
	face["node_modules_copy"] = nmExists
	// 快照内容指纹（整树文件相对路径＋大小＋内容哈希——对插件源任何改动敏感）。
	face["snapshot_shape_sha256"] = treeSHA256(s.PluginSnap)
	return face
}

// ---- 事件面：五事件位（静态＝插件注册面；动态＝探针观察面） ----

// eventRegRe 插件源的五事件位注册行（ctx.on("<event>", …)——index.ts
// registerHooks 形状）。
var eventRegRe = regexp.MustCompile(`ctx\.on\("([a-z/-]+)"`)

func eventsFace(s *Stack, tf TraceFacts, obs *probeObs) map[string]any {
	face := map[string]any{}
	// 静态：插件注册的事件位清单。
	if raw, err := os.ReadFile(filepath.Join(s.PluginSnap, "src", "index.ts")); err == nil {
		seen := map[string]bool{}
		for _, m := range eventRegRe.FindAllStringSubmatch(string(raw), -1) {
			seen[m[1]] = true
		}
		keys := make([]string, 0, len(seen))
		for k := range seen {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		face["plugin_registered_events"] = keys
	}
	// 动态：探针窗内的到达观察（agent/disposed 无终局动作——如实记 false）。
	sessions := daemonHealthSessions(s)
	face["observed"] = map[string]bool{
		"agent/pre-step":   tf.GateDelta > 0,
		"agent/created":    sessionsContain(sessions, obs.sidA) || sessionsContain(sessions, obs.sidB),
		"session/event":    tf.UsageRows > 0,
		"agent/status":     sessionsContain(sessions, obs.sidA) || sessionsContain(sessions, obs.sidB),
		"agent/disposed":   false,
		"poll_cycle_alive": len(sessions) > 0,
	}
	return face
}

// ---- 鸭子面：PluginContext 容错注入的服务接口群（行为面可达性） ----

func duckFace(s *Stack, cmd, svc LaneFact, obs *probeObs) map[string]any {
	// sessionProjections 读面：compacted 上报带 prefix_tokens>0（投影读成功）。
	prefixRead := false
	for _, sid := range []string{obs.sidA, obs.sidB} {
		if sid == "" {
			continue
		}
		for _, row := range readAccountRows(s, "compacted", sid) {
			if rowBool(row, "ok") && rowNum(row, "prefix_tokens") > 0 {
				prefixRead = true
			}
		}
	}
	return map[string]any{
		// commands 服务面 execute(agent,'/compact') 解析并执行（命令道执行痕）。
		"commands_execute_resolved": cmd.ExecViaCommand,
		// compaction 服务面 compactNow 对插件域可见（服务面执行痕；真机 web
		// 宿主组内隔离下恒 false——合成组成腿给出真实可达性）。
		"compaction_compactnow_reachable": svc.ExecViaService,
		"session_projections_prefix_read": prefixRead,
		// agents 服务面（session/create 经 ensureSession 收养）。
		"agents_ensure_session": obs.sidA != "",
		// sessions 注册表（插件播种/事件喂养——poll 面可见 sid）。
		"sessions_registry_listed": len(daemonHealthSessions(s)) > 0,
	}
}

// ---- 浏览器半面：client.js 模块加载、dock 槽位与 RPC 形状（静态形状；无头
// 探针无浏览器挂载面——动态缺席如实记，不伪造） ----

func browserFace(s *Stack) map[string]any {
	face := map[string]any{
		"dynamic_observable_headless": false, // 无头探针：client.js 只在浏览器加载
	}
	raw, err := os.ReadFile(filepath.Join(s.PluginSnap, "client.js"))
	if err != nil {
		face["client_js_readable"] = false
		return face
	}
	text := string(raw)
	sum := sha256.Sum256(raw)
	face["client_js_readable"] = true
	face["client_js_sha256"] = hex.EncodeToString(sum[:])
	face["dock_slot"] = firstQuotedAfter(text, "var DOCK_SLOT = ")
	face["rpc_namespace"] = firstQuotedAfter(text, "var NAMESPACE = ")
	face["module_loader_form"] = strings.Contains(text, "__ModuleLoader__.load")
	face["rpc_methods"] = map[string]bool{
		"list":       strings.Contains(text, "/list"),
		"resend":     strings.Contains(text, "/resend"),
		"newSession": strings.Contains(text, "/newSession"),
	}
	return face
}

// ---- 小件 ----

// daemonHealthSessions /dsh/health 的 sessions 键集（插件 poll 上报面——
// 注册表（五事件位/播种）与轮询臂共同的可达证据）。
func daemonHealthSessions(s *Stack) []string {
	req, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d/dsh/health", s.Ports.Daemon), nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+s.DaemonToken())
	resp, err := daemonHTTPClient(s.Opt.HTTPTimeout).Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	var out struct {
		Sessions map[string]float64 `json:"sessions"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	keys := make([]string, 0, len(out.Sessions))
	for k := range out.Sessions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sessionsContain(sids []string, sid string) bool {
	if sid == "" {
		return false
	}
	for _, s := range sids {
		if s == sid {
			return true
		}
	}
	return false
}

// mapStr map 取字符串小件。
func mapStr(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}

// firstQuotedAfter 取 `var X = '<value>'` 形态的值（client.js 常量形状采集）。
func firstQuotedAfter(text, marker string) string {
	i := strings.Index(text, marker)
	if i < 0 {
		return ""
	}
	rest := text[i+len(marker):]
	q1 := strings.IndexAny(rest, "'\"")
	if q1 < 0 {
		return ""
	}
	quote := rest[q1]
	q2 := strings.IndexByte(rest[q1+1:], quote)
	if q2 < 0 {
		return ""
	}
	return rest[q1+1 : q1+1+q2]
}

// treeSHA256 目录内容指纹：文件相对路径（正斜杠）＋每文件 sha256 的拼接再
// sha256（不 含大小/时戳——内容寻址；跳过 .playwright-cli/.git 与生成物）。
func treeSHA256(root string) string {
	var b strings.Builder
	var walk func(dir, rel string)
	walk = func(dir, rel string) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		names := make([]string, 0, len(ents))
		for _, e := range ents {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, name := range names {
			r := name
			if rel != "" {
				r = rel + "/" + name
			}
			if r == ".playwright-cli" || r == ".git" {
				continue
			}
			p := filepath.Join(dir, name)
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				walk(p, r)
				continue
			}
			raw, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			sum := sha256.Sum256(raw)
			fmt.Fprintf(&b, "%s=%s\n", r, hex.EncodeToString(sum[:]))
		}
	}
	walk(root, "")
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
