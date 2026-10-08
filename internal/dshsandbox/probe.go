// probe.go — L2 探针本体（实现 internal/dshverify.ProbeRunner 接缝，票04 消费）。
//
// 流程：起栈（失败＝基础设施故障回 error——含「环境缺 DSH」跳过哨兵）→ web
// 网关登录（就绪行 token→Set-Cookie，dsh web-auth e2e 实锚）→ 四痕采集 →
// 压缩链两道（各自派发·各自见上报·归因独立）→ 契约锚采集 → Evaluate →
// ProbeResult。收尾恒走（defer Stop）；红/错时保留沙箱现场（路径进摘要）。
//
// 通道事实（dsh 调研克隆只读钉点，全部有真机/e2e 实锚）：
//   - RPC：POST /api/<service>/<method>，client-request 信封
//     {type,rpcId,method,payload:{args}}；args 键＝方法参数名（session/create
//     取 request:{cwd,agentPreset?}——sessionId 省略＝宿主 `session-${randomUUID()}`
//     签发（会话键断言的受验值非探针自造）；session/prompt 取 request:
//     {requestId,sessionId,mode:'queue',content}——e2e driver T8 同款）。
//   - 会话转录：<DSH_HOME>/sessions/<ProjectKey(cwd)>/<EncodeSegment(sid)>/
//     session*.jsonl（明文——profile patch 已覆写 compression:none）。
//   - 压缩链执行痕归因：命令道＝转录含 /compact 的 command/run 生命周期
//     （commands executor 落事件于转录）；服务面＝压缩发生而无命令生命周期。
package dshsandbox

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ferryman/internal/dshtrans"
	"ferryman/internal/dshverify"
)

// 探针消息模板（流水行探针标记：消息文本带 marker＋会话 cwd 钉独立 workspace
// ——usage 行的 project 列与转录均可辨识探针流量）。
const (
	probeMsgTemplate = "[ferryman verify-dsh 探针 %s] 请只回复一句话：收到。不要使用任何工具。"
	// svcPreset 服务面腿会话的宿主 preset（minimal 无 /compact 命令注册——命令道
	// execute 解析 undefined，插件落服务面；见包注「合成组成」）。
	svcPreset = "minimal"
	// handoffProbeSID /dsh/handoff 零副作用探针的 sid（空 cwd——selfcheck 先例）。
	handoffProbeSID = "ferryman-verify-dsh-l2probe"
)

// svcLaneMsgText 服务面腿消息（垫料）。宿主压缩服务有硬不变量「摘要 framed
// tokens 必须严格小于被遮蔽内容」，2026-10-08 v0.9.10 三轮实锚钉死其行为：
// ①短消息（影子 95）摘要地板 ~300 必拒；②异质十段/重复 50 行（影子 364/
// 447）摘要逐项枚举到 497-627——比值恒 ≥1 仍拒；③E2E 散文灌到 ~13k token
// 两次压缩全绿（唯一实证过线量级，摘要输出在量级处被封顶）。据此垫料走
// 量级路线：~1500 行 ≈3.3 万字 ≈12k token 影子，进入实证过线区。marker
// 「服务面腿」保位（transcriptTurnEnd 锚）。var 而非 const＝strings.Repeat。
var svcLaneMsgText = "[ferryman verify-dsh 探针·服务面腿] 以下是垫料（同一句例行记录重复约一千五百行，无实际含义，唯一目的是把会话内容堆到压缩摘要必然更小的量级），请通读后按末句作答。\n\n" +
	strings.Repeat("例行巡检无异常，检查项通过，无需记录细节。\n", 1500) +
	"\n请只回复两个字：就绪。不要使用任何工具。"

// Probe L2 沙箱探针（dshverify.ProbeRunner 的实现）。
type Probe struct {
	Opt Options
	// nowTight 轮询节拍（var 形＝测试注入；缺省 1s）。
	pollTick time.Duration
}

// NewProbe 装配一个探针（o.DockUpstreams/o.DockActive 由装配层从生产 config
// 抄入——「沙箱 daemon 配置的最便宜上游」的单源）。
func NewProbe(o Options) *Probe {
	return &Probe{Opt: o, pollTick: time.Second}
}

// Run 起沙箱跑探针断言（票04 ProbeRunner 契约：error＝执行出错（起栈失败等
// 基础设施故障/环境缺 DSH——未完成验证非断言失败）；断言失败 Green=false）。
func (p *Probe) Run() (*dshverify.ProbeResult, error) {
	s, err := Start(p.Opt)
	if err != nil {
		return nil, err
	}
	defer s.Stop()

	gw, err := newGateway(s)
	if err != nil {
		s.Opt.KeepRoot = true // 排障现场
		return nil, fmt.Errorf("web 网关登录失败: %w", err)
	}

	// ---- 四痕 ----
	tf, obs, err := p.traceFacts(s, gw)
	if err != nil {
		s.Opt.KeepRoot = true
		return nil, err
	}

	// ---- 压缩链两道（各自派发·各自见上报；互不抵消判定在 lanes） ----
	cmdLane, svcLane := p.compactLanes(s, gw, obs)

	items := Evaluate(tf, cmdLane, svcLane)
	green := AllGreen(items)
	if !green {
		s.Opt.KeepRoot = true // 红＝保留现场（路径进摘要）
	}
	faces := collectFaces(s, tf, cmdLane, svcLane, obs)
	sum := Summarize(items)
	if !green {
		sum = fmt.Sprintf("%s（沙箱现场: %s）", sum, s.Logs)
	}
	logf(s, "L2 判定: green=%v %s", green, sum)
	return &dshverify.ProbeResult{Green: green, Summary: sum, Faces: faces}, nil
}

// probeObs 探针过程观察（锚采集与道归因共用）。
type probeObs struct {
	sidA       string // 探针主会话（四痕＋命令道腿）
	sidB       string // 服务面腿会话（合成组成）
	bCreated   bool   // 服务面腿会话建成（agentPreset=minimal 接受）
	turnADone  bool
	turnBDone  bool
	gateBefore int
	gateAfter  int
	usageA     int
	usageB     int
}

// traceFacts 四痕采集：注入路径探针→会话 A 创建→真消息→回合收口→闸门/账本/
// 转录取证。error＝基础设施故障（会话建不成/回合超时——未完成验证）。
func (p *Probe) traceFacts(s *Stack, gw *gateway) (TraceFacts, *probeObs, error) {
	obs := &probeObs{}
	tf := TraceFacts{}
	// ④ 注入路径：/dsh/handoff 零副作用探针（空 cwd——selfcheck 同款先例）。
	if code, body, err := postDaemonJSON(s, "/dsh/handoff",
		map[string]any{"cwd": "", "session_id": handoffProbeSID}); err != nil {
		tf.HandoffDetail = fmt.Sprintf("/dsh/handoff 探针失败: %v", err)
	} else if code != http.StatusOK {
		tf.HandoffDetail = fmt.Sprintf("/dsh/handoff 探针 HTTP %d（body %s）", code, clipStr(body, 120))
	} else {
		tf.HandoffOK = true
		tf.HandoffDetail = "/dsh/handoff 通道可达（零副作用探针 200）"
	}

	// ① 基线快照（gate_calls_by_agent["dsh"]）。
	obs.gateBefore = daemonGateCalls(s)

	// 会话 A：宿主签发 sid（session/create 不带 sessionId——会话键断言防自证）。
	sidA, err := gw.createSession(s.Workspace, "")
	if err != nil {
		return tf, obs, fmt.Errorf("探针会话创建失败: %w", err)
	}
	obs.sidA = sidA
	tf.SID = sidA
	tf.SIDSource = "宿主 session/create 签发（未传 sessionId）"

	// 真消息（一条；真模型经沙箱渡口 active 上游）。
	marker := randToken(6)
	if err := gw.promptSession(sidA, fmt.Sprintf(probeMsgTemplate, marker)); err != nil {
		return tf, obs, fmt.Errorf("探针消息发送失败: %w", err)
	}
	logf(s, "探针会话 %s 已发消息（marker=%s）", sidA, marker)

	// 回合收口：usage 行落账（＝事件上报痕）＋转录 turn/end/尺寸稳定兜底。
	turnDeadline := time.Now().Add(p.turnTimeout())
	turnDone := false
	for time.Now().Before(turnDeadline) {
		if n := countUsageRows(s, sidA); n > 0 && transcriptTurnEnd(s, s.Workspace, sidA, marker) {
			turnDone = true
			break
		}
		time.Sleep(p.pollTick)
	}
	obs.usageA = countUsageRows(s, sidA)
	obs.turnADone = turnDone
	if !turnDone {
		return tf, obs, fmt.Errorf("探针回合 %.0fs 未收口（usage 行 %d；沙箱日志 %s）——"+
			"模型路由/插件链路故障，未完成验证",
			p.turnTimeout().Seconds(), obs.usageA, s.Logs)
	}
	obs.gateAfter = daemonGateCalls(s)
	tf.GateDelta = obs.gateAfter - obs.gateBefore
	tf.GateDetail = fmt.Sprintf("/dsh/gate 计数 %d→%d（dsh 档）", obs.gateBefore, obs.gateAfter)
	tf.UsageRows = obs.usageA
	if rows := readAccountRows(s, "usage", sidA); len(rows) > 0 {
		tf.UsageDetail = fmt.Sprintf("usage 科目 %d 行（末行 model=%s）", len(rows),
			rowStr(rows[len(rows)-1], "model"))
	}
	return tf, obs, nil
}

// compactLanes 压缩链两道：
//   - 命令道（A 会话，生产组成）：daemon 自然触发→插件命令臂→上报→转录命令
//     生命周期归因；
//   - 服务面（B 会话，合成组成 minimal preset）：同链路落服务臂→上报→无命令
//     生命周期归因。合成组成任何一环起不来（preset 拒绝/会话建不成/回合不
//     收口）→ ContractMissing 带观察事实——红灯不是跳过（票面钉死）。
func (p *Probe) compactLanes(s *Stack, gw *gateway, obs *probeObs) (cmd, svc LaneFact) {
	// ---- 命令道腿（A 已在 traceFacts 建好并完成回合） ----
	cmd = p.waitLane(s, obs.sidA, LaneCommandName)
	// ---- 服务面腿（B：合成组成会话） ----
	svc = LaneFact{Name: LaneServiceName}
	sidB, err := gw.createSession(s.Workspace, svcPreset)
	if err != nil {
		svc.ContractMissing = true
		svc.Detail = fmt.Sprintf("合成组成会话创建失败（agentPreset=%s）: %v", svcPreset, err)
		return cmd, svc
	}
	obs.sidB = sidB
	obs.bCreated = true
	svc.SessionID = sidB
	if err := gw.promptSession(sidB, svcLaneMsgText); err != nil {
		svc.ContractMissing = true
		svc.Detail = fmt.Sprintf("服务面腿消息发送失败: %v", err)
		return cmd, svc
	}
	turnDeadline := time.Now().Add(p.turnTimeout())
	for time.Now().Before(turnDeadline) {
		if countUsageRows(s, sidB) > 0 && transcriptTurnEnd(s, s.Workspace, sidB, "服务面腿") {
			obs.turnBDone = true
			break
		}
		time.Sleep(p.pollTick)
	}
	obs.usageB = countUsageRows(s, sidB)
	if !obs.turnBDone {
		svc.ContractMissing = true
		svc.Detail = fmt.Sprintf("服务面腿回合未收口（usage 行 %d）——合成组成不可用", obs.usageB)
		return cmd, svc
	}
	return cmd, p.waitLane(s, sidB, LaneServiceName)
}

// waitLane 等一道压缩链收口：daemon 触发痕→compacted 上报→转录归因。
// 超时/无上报按观察事实落 fail/契约缺失——绝不放宽。
func (p *Probe) waitLane(s *Stack, sid, laneName string) LaneFact {
	l := LaneFact{Name: laneName, SessionID: sid}
	deadline := time.Now().Add(p.compactTimeout())
	var reportRows []map[string]any
	for time.Now().Before(deadline) {
		reportRows = readAccountRows(s, "compacted", sid)
		if len(reportRows) > 0 {
			last := reportRows[len(reportRows)-1]
			if rowBool(last, "ok") {
				l.ReportOK = true
				if n := rowNum(last, "prefix_tokens"); n > 0 {
					l.Detail = fmt.Sprintf("上报 ok（prefix_tokens=%d, source=%s）",
						int(n), rowStr(last, "source"))
				} else {
					l.Detail = fmt.Sprintf("上报 ok（无 prefix_tokens, source=%s）",
						rowStr(last, "source"))
				}
				break
			}
			// ok=false：连续失败上报攒 reason（no-compaction-channel＝两道皆缺的
			// 插件自报——服务面腿据此记契约缺失；其余失败继续等到窗口收口）。
			l.ReportReason = joinReasons(reportRows)
			if rowStr(last, "reason") == "no-compaction-channel" {
				break
			}
		}
		time.Sleep(p.pollTick)
	}
	// 派发痕：daemon.log 触发行（[compact] dsh 触发…<sid 前 16 位>）。
	l.Dispatched = daemonLogHasTrigger(s, sid)
	// 归因：转录命令生命周期（命令道执行痕）vs 压缩事件无命令生命周期（服务面）。
	hasCmdLifecycle, hasCompaction := transcriptCompactEvidence(s, s.Workspace, sid)
	l.ExecViaCommand = hasCmdLifecycle
	l.ExecViaService = hasCompaction && !hasCmdLifecycle
	if !l.ReportOK && strings.Contains(l.ReportReason, "no-compaction-channel") {
		// 插件自报「宿主无压缩通道」（两道皆缺）——本道契约缺失（红灯不是跳过）。
		l.ContractMissing = true
		l.Detail = fmt.Sprintf("插件自报 no-compaction-channel（宿主无压缩通道）——%s", l.Detail)
	}
	if !l.Dispatched && !l.ReportOK {
		l.Detail = fmt.Sprintf("压缩指令未派发（daemon 未触发；日志 %s）——%s",
			filepath.Join(s.Logs, "daemon.log"), l.Detail)
	}
	return l
}

// ---- 超时缺省（回合收口/压缩链窗口——真机量级；压缩窗口盖过触发线 30s＋
// 轮询领取 10s＋执行＋落盘稳定窗＋余量） ----

func (p *Probe) turnTimeout() time.Duration    { return 240 * time.Second }
func (p *Probe) compactTimeout() time.Duration { return 420 * time.Second }

// ---- web 网关客户端（cookie 登录＋client-request 信封） ----

// gateway 沙箱 web 宿主网关客户端。
type gateway struct {
	base  string
	hc    *http.Client
	token string
	seq   int
}

// newGateway 登录：GET /?token=<launch>（303＋Set-Cookie——web-auth e2e 实锚；
// cookiejar 收 HttpOnly 会话 cookie），随后 RPC 全带 cookie。
func newGateway(s *Stack) (*gateway, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	g := &gateway{
		base:  fmt.Sprintf("http://127.0.0.1:%d", s.Ports.Web),
		hc:    &http.Client{Timeout: s.Opt.HTTPTimeout, Jar: jar},
		token: s.WebToken,
	}
	resp, err := g.hc.Get(g.base + "/?token=" + g.token)
	if err != nil {
		return nil, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	u, _ := url.Parse(g.base)
	if u == nil || len(jar.Cookies(u)) == 0 {
		return nil, fmt.Errorf("token 交换未下发会话 cookie（HTTP %d）", resp.StatusCode)
	}
	return g, nil
}

// rpc POST /api/<method>，client-request 信封；应答 result.ok=false 时回错误
// （code+message 指路），成功回 result.value 的原始 JSON。
func (g *gateway) rpc(method string, args map[string]any) (json.RawMessage, error) {
	g.seq++
	envelope := map[string]any{
		"type":    "client-request",
		"rpcId":   fmt.Sprintf("verify-dsh-%d-%s", g.seq, randToken(4)),
		"method":  method,
		"payload": map[string]any{"args": args},
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, g.base+"/api/"+method, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d（%s）: %s", resp.StatusCode, method, clipStr(string(raw), 160))
	}
	var env struct {
		Type   string `json:"type"`
		RpcID  string `json:"rpcId"`
		Result *struct {
			OK    *bool           `json:"ok"`
			Value json.RawMessage `json:"value"`
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("%s 应答解析失败: %v: %s", method, err, clipStr(string(raw), 160))
	}
	if env.Result == nil {
		return nil, fmt.Errorf("%s 应答缺 result: %s", method, clipStr(string(raw), 160))
	}
	if env.Result.Error != nil {
		return nil, fmt.Errorf("%s 错误 %s: %s", method,
			env.Result.Error.Code, env.Result.Error.Message)
	}
	if env.Result.OK != nil && !*env.Result.OK {
		return nil, fmt.Errorf("%s result.ok=false: %s", method, clipStr(string(raw), 160))
	}
	return env.Result.Value, nil
}

// createSession session/create（agentPreset 空＝缺省 preset——宿主签发 sid）。
func (g *gateway) createSession(cwd, agentPreset string) (string, error) {
	req := map[string]any{"cwd": cwd}
	if agentPreset != "" {
		req["agentPreset"] = agentPreset
	}
	val, err := g.rpc("session/create", map[string]any{"request": req})
	if err != nil {
		return "", err
	}
	var out struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(val, &out); err != nil || out.SessionID == "" {
		return "", fmt.Errorf("session/create 应答缺 sessionId: %s", clipStr(string(val), 160))
	}
	return out.SessionID, nil
}

// promptSession session/prompt（queue 模式一条文本消息——e2e driver T8 同形）。
func (g *gateway) promptSession(sid, text string) error {
	args := map[string]any{"request": map[string]any{
		"requestId": "verify-dsh-" + randToken(8),
		"sessionId": sid,
		"mode":      "queue",
		"content":   []any{map[string]any{"type": "text", "text": text}},
	}}
	if _, err := g.rpc("session/prompt", args); err != nil {
		return err
	}
	return nil
}

// ---- 沙箱 daemon / 账本 / 转录取证 ----

// postDaemonJSON 沙箱 daemon 管理口 POST（Bearer；loopback）。
func postDaemonJSON(s *Stack, path string, body map[string]any) (int, string, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, "", err
	}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d%s", s.Ports.Daemon, path), bytes.NewReader(raw))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.DaemonToken())
	resp, err := daemonHTTPClient(s.Opt.HTTPTimeout).Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, string(out), nil
}

// daemonGateCalls /stats gate_calls_by_agent["dsh"]（读失败＝-1：探针窗内
// 无从计数，GateDelta 负值自然 fail——fail 方向安全）。
func daemonGateCalls(s *Stack) int {
	req, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d/stats", s.Ports.Daemon), nil)
	if err != nil {
		return -1
	}
	req.Header.Set("Authorization", "Bearer "+s.DaemonToken())
	resp, err := daemonHTTPClient(s.Opt.HTTPTimeout).Do(req)
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return -1
	}
	var out struct {
		GateByAgent map[string]int `json:"gate_calls_by_agent"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return -1
	}
	return out.GateByAgent["dsh"]
}

// readAccountRows 账本科目行（<data>/accounts/*.jsonl；kind+session_id 过滤）。
func readAccountRows(s *Stack, kind, sid string) []map[string]any {
	var out []map[string]any
	dir := filepath.Join(s.Data, "accounts")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		for _, ln := range strings.Split(string(raw), "\n") {
			ln = strings.TrimSpace(ln)
			if ln == "" {
				continue
			}
			var row map[string]any
			if json.Unmarshal([]byte(ln), &row) != nil {
				continue // 坏行跳过（append-only 读侧宽容，accounts 同款）
			}
			if rowStr(row, "kind") != kind || rowStr(row, "session_id") != sid {
				continue
			}
			out = append(out, row)
		}
	}
	return out
}

// countUsageRows usage 行数（事件上报断言的事实源）。
func countUsageRows(s *Stack, sid string) int {
	return len(readAccountRows(s, "usage", sid))
}

// daemonLogHasTrigger daemon.log 有该会话的压缩触发行（[compact] dsh 触发…＋
// sid 前 16 位——compact_trigger.go 打印形状；触发痕＝「派发发生」的证据面）。
func daemonLogHasTrigger(s *Stack, sid string) bool {
	raw, err := os.ReadFile(filepath.Join(s.Logs, "daemon.log"))
	if err != nil {
		return false
	}
	key := sid
	if len(key) > 16 {
		key = key[:16]
	}
	for _, ln := range strings.Split(string(raw), "\n") {
		if strings.Contains(ln, "[compact] dsh 触发") && strings.Contains(ln, key) {
			return true
		}
	}
	return false
}

// transcriptDir 会话转录目录（dshtrans 布局：<sessions>/<ProjectKey(cwd)>/
// <EncodeSegment(sid)>）。
func transcriptDir(s *Stack, cwd, sid string) string {
	proj, err := dshtrans.ProjectKey(cwd)
	if err != nil {
		return ""
	}
	seg, err := dshtrans.EncodeSegment(sid)
	if err != nil {
		return ""
	}
	return filepath.Join(s.Home, "sessions", proj, seg)
}

// transcriptText 会话最新代转录全文（明文/zstd 两形态——TailText 同源；缺
// 代文件回空串）。
func transcriptText(s *Stack, cwd, sid string) string {
	dir := transcriptDir(s, cwd, sid)
	if dir == "" {
		return ""
	}
	gen, ok := dshtrans.LatestGeneration(dir)
	if !ok {
		return ""
	}
	res := dshtrans.TailText(gen.Path, gen.Zstd, 0)
	if res.Err != nil {
		return res.Text // 部分文本也收（尾读失败不抹掉已读证据）
	}
	return res.Text
}

// transcriptTurnEnd 转录含该回合收口（turn/end 在 marker 之后出现；兜底＝
// assistant 文本已含「收到」类回复——以 turn/end 为主判据，marker 保位防旧回合）。
func transcriptTurnEnd(s *Stack, cwd, sid, marker string) bool {
	text := transcriptText(s, cwd, sid)
	if text == "" {
		return false
	}
	mi := strings.Index(text, marker)
	if mi < 0 {
		return false // 探针首条尚未落转录
	}
	return strings.Contains(text[mi:], `"type":"turn/end"`) ||
		strings.Contains(text[mi:], `"type": "turn/end"`)
}

// transcriptCompactEvidence 压缩链执行痕归因（转录全文扫描）：
// hasCmdLifecycle＝/compact 的命令生命周期（command/run 或 command/done 行内
// 含 compact）；hasCompaction＝压缩事件（"compaction/" 行）。
func transcriptCompactEvidence(s *Stack, cwd, sid string) (hasCmdLifecycle, hasCompaction bool) {
	text := transcriptText(s, cwd, sid)
	if text == "" {
		return false, false
	}
	for _, ln := range strings.Split(text, "\n") {
		if !hasCmdLifecycle && strings.Contains(ln, "command/") && strings.Contains(ln, "compact") {
			hasCmdLifecycle = true
		}
		if !hasCompaction && strings.Contains(ln, "compaction/") {
			hasCompaction = true
		}
	}
	return hasCmdLifecycle, hasCompaction
}

// ---- 行取值小件（宽松收形：JSON 数值 float64/字符串两态） ----

func rowStr(row map[string]any, k string) string {
	v, _ := row[k].(string)
	return v
}

func rowBool(row map[string]any, k string) bool {
	v, _ := row[k].(bool)
	return v
}

func rowNum(row map[string]any, k string) float64 {
	switch x := row[k].(type) {
	case float64:
		return x
	case int:
		return float64(x)
	}
	return 0
}

// joinReasons 失败上报 reason 去重连接（诊断可读）。
func joinReasons(rows []map[string]any) string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		reason := rowStr(r, "reason")
		if reason == "" || seen[reason] {
			continue
		}
		seen[reason] = true
		out = append(out, reason)
	}
	return strings.Join(out, ";")
}

// clipStr 超长字符串截断（错误面防刷屏）。
func clipStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// randToken 随机短 token（hex；requestId/marker 用）。
func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strings.Repeat("0", n*2)
	}
	return hex.EncodeToString(b)[:n*2]
}
