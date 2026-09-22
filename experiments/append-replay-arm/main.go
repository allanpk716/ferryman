// append-replay-arm —— 票04:同模型摆渡「追加重放实跳臂」验证工具
// (ADR-0015 决定一【启用硬门槛】;experiments/ 惯例:独立可跑、结论人判)。
//
// 给定一份会话捕获快照,走四步:
//
//	construct  构造追加重放体(前缀字节一个不动,末尾追加摆渡指令,max_tokens
//	           放开至封顶),独立校验「字节差异仅末尾追加段+max_tokens 数值
//	           子区间」,证据落盘;
//	send       两跳真实发送到渡口入站口(基线=同前缀心跳式原样重放
//	           max_tokens=1,先走;追加=追加重放,后走)——复用票03
//	           HttpBeatSender,与生产同一改写路径;--dry-run 只跳过发送,
//	           其余全走;
//	evaluate   四条成功标准评估(①追加跳缓存读占比≥基线跳;②字节差异仅
//	           末尾;③无 tool_use;④输出可解析为交接 MD),证据落盘;
//	writeback  结论回写启用门状态文件(passed/failed;inconclusive 拒写)。
//
// 另有 status(看各上游当前状态)与 run(串前四步,writeback 默认不自动,
// 需 --writeback)。实跳由人手动执行:真实上游调用花钱,证据目录含会话
// 内容,绝不入 git(隐私铁律,同 cache-ttl 套件纪律)。
//
// 用法示例:
//
//	go run main.go construct -snapshot ~/ferryman/captures/arm_snap.json -workdir ~/armrun -upstream zhipu
//	go run main.go send      -snapshot ... -workdir ~/armrun -dock-url http://127.0.0.1:15722
//	go run main.go evaluate  -workdir ~/armrun
//	go run main.go writeback -workdir ~/armrun
//	go run main.go status
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"ferryman/internal/beat"
	"ferryman/internal/dock"
	"ferryman/internal/ferry"
)

// 快照文件形状(工具自定义;capture 转发器的 body_raw 形态可直接转过来):
//
//	{
//	  "session_id": "…",   // 必填
//	  "body": "…",         // 请求体原字节(UTF-8 文本);与 body_file 二选一
//	  "body_file": "…",    // 相对快照文件目录;字节保真优先用文件形态
//	  "headers": {…},      // 可选:白名单内容头(小写键)
//	  "note": "…"          // 可选
//	}
type armSnapshot struct {
	SessionID string            `json:"session_id"`
	Body      string            `json:"body"`
	BodyFile  string            `json:"body_file"`
	Headers   map[string]string `json:"headers"`
	Note      string            `json:"note"`
}

// 工作目录内的证据文件名(每步一文件,人可直接打开看)。
const (
	snapOutName   = "append_body.json"      // construct:追加重放体原字节
	constructName = "construct_report.json" // construct:构造与②预检报告
	sendName      = "send_result.json"      // send:两跳事实(usage+stop_reason+全文)
	evaluateName  = "evaluate_result.json"  // evaluate:四标准判定
)

// armSendResult send 步的证据:两跳事实 + 场景元数据。
type armSendResult struct {
	TS       float64            `json:"ts"`
	TSISO    string             `json:"ts_iso"`
	DryRun   bool               `json:"dry_run"`
	DockURL  string             `json:"dock_url"`
	MaxTok   int                `json:"max_tokens"`
	Snapshot string             `json:"snapshot"`
	Baseline ferry.ArmHopResult `json:"baseline"`
	Append   ferry.ArmHopResult `json:"append"`
}

// armEvaluateResult evaluate 步的证据:四标准判定 + 场景元数据(writeback
// 从这里取结论,不看内存)。
type armEvaluateResult struct {
	TS         float64             `json:"ts"`
	TSISO      string              `json:"ts_iso"`
	Upstream   string              `json:"upstream"`
	Snapshot   string              `json:"snapshot"`
	MaxTok     int                 `json:"max_tokens"`
	DryRun     bool                `json:"dry_run"`
	Evaluation ferry.ArmEvaluation `json:"evaluation"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run 子命令分发(可测形态:io 注入,返回退出码)。
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprint(stderr, usage)
		return 1
	}
	switch args[0] {
	case "construct":
		return cmdConstruct(args[1:], stdout, stderr)
	case "send":
		return cmdSend(args[1:], stdout, stderr)
	case "evaluate":
		return cmdEvaluate(args[1:], stdout, stderr)
	case "writeback":
		return cmdWriteback(args[1:], stdout, stderr)
	case "status":
		return cmdStatus(args[1:], stdout, stderr)
	case "run":
		return cmdRun(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "未知子命令 %q\n%s", args[0], usage)
		return 1
	}
}

const usage = `追加重放实跳臂工具(票04;ADR-0015 启用硬门槛)

子命令:
  construct  构造追加重放体 + ②字节面预检(证据: append_body.json / construct_report.json)
  send       两跳真实发送(基线心跳式 → 追加重放);--dry-run 只跳过发送
  evaluate   四条成功标准评估(证据: evaluate_result.json)
  writeback  结论回写启用门状态文件(inconclusive 拒写)
  status     看各上游当前启用门状态
  run        construct→send→evaluate 串跑(writeback 需显式 --writeback)

公共旗标: -snapshot 快照文件 -workdir 证据目录(默认 ./armrun)
  -upstream 上游名 -dock-url 渡口入站(默认 ` + beat.DefaultDockURL + `)
  -max-tokens 封顶(默认 4096) -state 状态文件(默认 ~/ferryman/arm_verdict.jsonl)
`

// ---- 公共旗标组 ----

type commonFlags struct {
	snapshot string
	workdir  string
	upstream string
	dockURL  string
	maxTok   int
	state    string
	dryRun   bool
}

func bindCommon(fs *flag.FlagSet, c *commonFlags) {
	fs.StringVar(&c.snapshot, "snapshot", "", "会话捕获快照 JSON 文件(必填,除 status/writeback)")
	fs.StringVar(&c.workdir, "workdir", "./armrun", "证据目录(逐步落盘)")
	fs.StringVar(&c.upstream, "upstream", "", "上游名(= [dock.upstreams] 条目键)")
	fs.StringVar(&c.dockURL, "dock-url", beat.DefaultDockURL, "渡口入站地址(与真流量同路径)")
	fs.IntVar(&c.maxTok, "max-tokens", ferry.SameModelMaxTokens, "追加跳 max_tokens 封顶")
	fs.StringVar(&c.state, "state", ferry.DefaultArmVerdictPath(), "启用门状态文件")
	fs.BoolVar(&c.dryRun, "dry-run", false, "只跳过真实发送,其余全走")
}

func die(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "[拒绝] %v\n", err)
	return 1
}

// loadSnapshot 读快照文件;body_file 相对快照文件目录解析(字节保真优先)。
func loadSnapshot(path string) (*armSnapshot, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("读快照文件: %w", err)
	}
	var s armSnapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, nil, fmt.Errorf("快照文件非约定 JSON 形状(session_id/body/body_file/headers): %w", err)
	}
	if s.SessionID == "" {
		return nil, nil, fmt.Errorf("快照缺 session_id")
	}
	var body []byte
	switch {
	case s.BodyFile != "":
		p := s.BodyFile
		if !filepath.IsAbs(p) {
			p = filepath.Join(filepath.Dir(path), p)
		}
		if body, err = os.ReadFile(p); err != nil {
			return nil, nil, fmt.Errorf("读 body_file: %w", err)
		}
	case s.Body != "":
		body = []byte(s.Body)
	default:
		return nil, nil, fmt.Errorf("快照缺 body 与 body_file(请求体原字节)")
	}
	return &s, body, nil
}

// writeEvidence 证据文件统一落盘(缩进 JSON,人可直接打开看)。
func writeEvidence(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func tsISO(ts float64) string {
	return time.Unix(int64(ts), 0).Format("2006-01-02T15:04:05-0700")
}

// ---- construct ----

func cmdConstruct(args []string, stdout, stderr io.Writer) int {
	var c commonFlags
	fs := flag.NewFlagSet("construct", flag.ContinueOnError)
	bindCommon(fs, &c)
	if fs.Parse(args) != nil {
		return 1
	}
	if c.upstream == "" {
		return die(stderr, fmt.Errorf("construct 需要 -upstream(结论按上游分列)"))
	}
	snap, body, err := loadSnapshot(c.snapshot)
	if err != nil {
		return die(stderr, err)
	}
	out, model, err := beat.AppendReplayBody(body, c.maxTok, ferry.SameModelInstruction)
	if err != nil {
		return die(stderr, fmt.Errorf("构造追加重放体: %w", err))
	}
	bodyPath := filepath.Join(c.workdir, snapOutName)
	if err := os.MkdirAll(c.workdir, 0o755); err != nil {
		return die(stderr, err)
	}
	if err := os.WriteFile(bodyPath, out, 0o600); err != nil {
		return die(stderr, err)
	}
	diffErr := ferry.VerifyAppendOnlyDiff(body, out, c.maxTok)
	report := map[string]any{
		"ts": clockNow(), "upstream": c.upstream,
		"snapshot": c.snapshot, "session_id": snap.SessionID, "model": model,
		"src_len": len(body), "append_len": len(out),
		"max_tokens":  c.maxTok,
		"diff_ok":     diffErr == nil,
		"diff_detail": detailOrOK(diffErr),
	}
	if err := writeEvidence(filepath.Join(c.workdir, constructName), report); err != nil {
		return die(stderr, err)
	}
	fmt.Fprintf(stdout, "构造完成: 追加体 %d 字节(快照 %d),model=%s\n", len(out), len(body), model)
	if diffErr != nil {
		fmt.Fprintf(stdout, "②字节面预检未过: %v(证据已留档,勿发送)\n", diffErr)
		return 1
	}
	fmt.Fprintf(stdout, "②字节面预检过: 差异仅末尾追加段+max_tokens 数值子区间\n")
	return 0
}

func detailOrOK(err error) string {
	if err != nil {
		return err.Error()
	}
	return "ok"
}

// ---- send ----

func cmdSend(args []string, stdout, stderr io.Writer) int {
	var c commonFlags
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	bindCommon(fs, &c)
	if fs.Parse(args) != nil {
		return 1
	}
	snap, body, err := loadSnapshot(c.snapshot)
	if err != nil {
		return die(stderr, err)
	}
	res := armSendResult{
		TS: clockNow(), TSISO: "", DryRun: c.dryRun, DockURL: c.dockURL,
		MaxTok: c.maxTok, Snapshot: c.snapshot,
	}
	if c.dryRun {
		// dry-run:只跳过发送,其余全走——两跳按未真发留档(评估据此判
		// not_evaluatable),构造产物照常在证据目录可查。
		res.Baseline = ferry.ArmHopResult{Hop: "baseline"}
		res.Append = ferry.ArmHopResult{Hop: "append"}
		fmt.Fprintln(stdout, "dry-run: 跳过真实发送(两跳按未真发留档)")
	} else {
		// 与生产同一条发送通道:快照入库 → HttpBeatSender → 渡口入站口。
		store := dock.NewSnapshotStore()
		h := http.Header{}
		for k, v := range snap.Headers {
			h.Set(k, v)
		}
		store.Capture(snap.SessionID, body, h)
		sender := beat.NewHttpBeatSender(c.dockURL, store)
		// 基线跳先走(心跳式原样重放,max_tokens=1):标准①的基线占比。
		br := sender.Send(beat.BeatPlan{Agent: "cc", SessionID: snap.SessionID})
		res.Baseline = ferry.ArmHopResult{
			Hop: "baseline", Sent: br.Sent, OK: br.OK, Err: br.Err,
			Input: br.InputTokens, CacheRead: br.CacheReadTokens, Output: br.OutputTokens,
			Model: br.Model,
		}
		// 追加跳后走(前缀原样+末尾摆渡指令,max_tokens=封顶)。
		ar := sender.SendAppendReplay(beat.AppendReplayPlan{
			SessionID: snap.SessionID, Instruction: ferry.SameModelInstruction, MaxTokens: c.maxTok,
		})
		res.Append = ferry.ArmHopResult{
			Hop: "append", Sent: ar.Sent, OK: ar.OK,
			StopReason: ar.StopReason, Text: ar.Text, Err: ar.Err,
			Input: ar.InputTokens, CacheRead: ar.CacheReadTokens, Output: ar.OutputTokens,
			Model: ar.Model,
		}
	}
	res.TSISO = tsISO(res.TS)
	if err := writeEvidence(filepath.Join(c.workdir, sendName), res); err != nil {
		return die(stderr, err)
	}
	fmt.Fprintf(stdout, "发送完成: 基线跳 ok=%v err=%q | 追加跳 ok=%v stop=%q err=%q\n"+
		"证据已落盘: %s\n",
		res.Baseline.OK, res.Baseline.Err, res.Append.OK, res.Append.StopReason, res.Append.Err,
		filepath.Join(c.workdir, sendName))
	return 0
}

// ---- evaluate ----

func cmdEvaluate(args []string, stdout, stderr io.Writer) int {
	var c commonFlags
	fs := flag.NewFlagSet("evaluate", flag.ContinueOnError)
	bindCommon(fs, &c)
	if fs.Parse(args) != nil {
		return 1
	}
	if c.upstream == "" {
		return die(stderr, fmt.Errorf("evaluate 需要 -upstream"))
	}
	_, snapBody, err := loadSnapshot(c.snapshot)
	if err != nil {
		return die(stderr, err)
	}
	appendBody, err := os.ReadFile(filepath.Join(c.workdir, snapOutName))
	if err != nil {
		return die(stderr, fmt.Errorf("读构造产物(先跑 construct): %w", err))
	}
	sendRaw, err := os.ReadFile(filepath.Join(c.workdir, sendName))
	if err != nil {
		return die(stderr, fmt.Errorf("读发送结果(先跑 send): %w", err))
	}
	var sr armSendResult
	if err := json.Unmarshal(sendRaw, &sr); err != nil {
		return die(stderr, fmt.Errorf("发送结果非约定形状: %w", err))
	}
	ev := ferry.EvaluateArm(snapBody, appendBody, sr.MaxTok, sr.Baseline, sr.Append)
	out := armEvaluateResult{
		TS: clockNow(), TSISO: tsISO(clockNow()),
		Upstream: c.upstream, Snapshot: sr.Snapshot, MaxTok: sr.MaxTok,
		DryRun: sr.DryRun, Evaluation: ev,
	}
	if err := writeEvidence(filepath.Join(c.workdir, evaluateName), out); err != nil {
		return die(stderr, err)
	}
	for _, cr := range ev.Criteria {
		mark := map[string]string{ferry.ArmStatusPass: "[过]", ferry.ArmStatusFail: "[败]", ferry.ArmStatusNA: "[悬]"}[cr.Status]
		fmt.Fprintf(stdout, "%s %s %s\n", mark, cr.Name, cr.Detail)
	}
	fmt.Fprintf(stdout, "总判定: %s(证据: %s)\n", ev.Verdict, filepath.Join(c.workdir, evaluateName))
	if ev.Verdict == ferry.ArmVerdictInconclusive {
		fmt.Fprintln(stdout, "不可判定不回写——状态保持待实跳;排查后复跑 send/evaluate。")
	}
	return 0
}

// ---- writeback ----

func cmdWriteback(args []string, stdout, stderr io.Writer) int {
	var c commonFlags
	var note string
	fs := flag.NewFlagSet("writeback", flag.ContinueOnError)
	bindCommon(fs, &c)
	fs.StringVar(&note, "note", "", "备注(随结论入状态文件)")
	if fs.Parse(args) != nil {
		return 1
	}
	evRaw, err := os.ReadFile(filepath.Join(c.workdir, evaluateName))
	if err != nil {
		return die(stderr, fmt.Errorf("读评估结论(先跑 evaluate): %w", err))
	}
	var er armEvaluateResult
	if err := json.Unmarshal(evRaw, &er); err != nil {
		return die(stderr, fmt.Errorf("评估结论非约定形状: %w", err))
	}
	if er.Evaluation.Verdict != ferry.ArmVerdictPassed && er.Evaluation.Verdict != ferry.ArmVerdictFailed {
		return die(stderr, fmt.Errorf("结论 %q 不可回写(只收 passed/failed;不可判定不落状态,状态保持待实跳)", er.Evaluation.Verdict))
	}
	rec := ferry.ArmRecord{
		Upstream:      er.Upstream,
		Verdict:       er.Evaluation.Verdict,
		Criteria:      map[string]string{},
		AppendRatio:   er.Evaluation.AppendRatio,
		BaselineRatio: er.Evaluation.BaselineRatio,
		Snapshot:      er.Snapshot,
		Note:          note,
	}
	for _, cr := range er.Evaluation.Criteria {
		rec.Criteria[cr.Name] = cr.Status
	}
	// 追加跳的 model 随行留档(记账元数据同源)。
	if er.Evaluation.Verdict == ferry.ArmVerdictPassed || er.Evaluation.Verdict == ferry.ArmVerdictFailed {
		var sr armSendResult
		if raw, err := os.ReadFile(filepath.Join(c.workdir, sendName)); err == nil {
			if json.Unmarshal(raw, &sr) == nil {
				rec.Model = sr.Append.Model
			}
		}
	}
	if err := ferry.RecordArmVerdict(c.state, rec); err != nil {
		return die(stderr, err)
	}
	fmt.Fprintf(stdout, "结论已回写: %s → %s(状态文件: %s)\n", er.Upstream, er.Evaluation.Verdict, c.state)
	return cmdStatusArgs([]string{"-state", c.state, er.Upstream}, stdout, stderr)
}

// ---- status ----

func cmdStatus(args []string, stdout, stderr io.Writer) int {
	return cmdStatusArgs(args, stdout, stderr)
}

func cmdStatusArgs(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	state := fs.String("state", ferry.DefaultArmVerdictPath(), "启用门状态文件")
	if fs.Parse(args) != nil {
		return 1
	}
	recs, err := ferry.LoadArmVerdicts(*state)
	if err != nil {
		return die(stderr, err)
	}
	names := fs.Args()
	if len(names) == 0 {
		for u := range recs {
			names = append(names, u)
		}
		sortStrings(names)
	}
	if len(names) == 0 {
		fmt.Fprintln(stdout, "状态文件暂无任何结论(全部待实跳):", *state)
		return 0
	}
	for _, u := range names {
		rec, ok := recs[u]
		if !ok {
			fmt.Fprintf(stdout, "%-12s 待实跳(白名单预置≠启用)\n", u)
			continue
		}
		state := "已启用"
		if rec.Verdict == ferry.ArmStateFailed {
			state = "未过(保持未启用)"
		}
		fmt.Fprintf(stdout, "%-12s %s\t%s\t追加跳占比 %.4f / 基线 %.4f\t%s\n",
			u, state, rec.TSISO, rec.AppendRatio, rec.BaselineRatio, rec.Note)
	}
	return 0
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// clockNow 时刻源(clock.Now 同语义;独立别名避免测试整体换时钟的耦合)。
func clockNow() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

// ---- run(串前四步;writeback 需显式 --writeback) ----

func cmdRun(args []string, stdout, stderr io.Writer) int {
	var c commonFlags
	var doWriteback bool
	var note string
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	bindCommon(fs, &c)
	fs.BoolVar(&doWriteback, "writeback", false, "评估结论可落账时自动回写(默认不,人看完证据再 writeback)")
	fs.StringVar(&note, "note", "", "writeback 备注")
	if fs.Parse(args) != nil {
		return 1
	}
	if rc := cmdConstruct(fs.Args(), stdout, stderr); rc != 0 {
		return rc
	}
	if rc := cmdSend(fs.Args(), stdout, stderr); rc != 0 {
		return rc
	}
	if rc := cmdEvaluate(fs.Args(), stdout, stderr); rc != 0 {
		return rc
	}
	if !doWriteback {
		fmt.Fprintln(stdout, "run 完: writeback 未自动执行——人看完证据再跑 writeback(或 --writeback)。")
		return 0
	}
	// 结论不可判定时 writeback 自行拒绝(退出码 1)。
	return cmdWriteback(append(fs.Args(), "-note", note), stdout, stderr)
}
