package daemon

// dsh_liveness_test.go — verify-dsh 票01：L1 挂载记账＋宿主进程旁证＋
// /dsh/health 只读面的钉子（spec「三层检测与进程模型」L1 节＋「灯色」节；
// 决策 D1 只记账不判定、D13 阈值＝3×生效间隔/90s 兜底注明假设）。
//
// 覆盖矩阵（票面验收逐条）：
//   - /dsh/poll 序列（走 HTTP 真路径——记账钩子挂在 doDshReceive 拦截器，
//     直调 DshPoll 不记账）→ /dsh/health 全局年龄＋sid last-seen 正确；
//   - 阈值推导表驱动：有 hint=3×hint；无 hint 有节律=3×节律（D13）；全无=
//     90s 兜底且 assumed=true；超龄判定与「未 poll 过不判超龄」（无证据不
//     判定，D1）；
//   - 宿主旁证两态（枚举器假实现注入）＋默认探针组合逻辑（harness 进程腿
//     ×3080 端口腿四象限）；tasklist 输出解析；HideWindow 在位（Windows
//     分支运行时断言，零闪窗铁律）。

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// newDshLivenessEnv L1 测试环境：newDshRcvEnv 全套＋包级记账态复位＋探针假
// 实现注入（缺省"宿主在"——不关心旁证的用例不被真 tasklist/3080 拨号噪声
// 沾染，钟冻结在 t0 同 gateEnv）。
func newDshLivenessEnv(t *testing.T) *gateEnv {
	t.Helper()
	e := newDshRcvEnv(t, "enforce")
	resetDshLiveness(t)
	setDshHostProbe(t, true, "harness=1; port3080=listening")
	return e
}

// resetDshLiveness L1 包级态复位（compact.go resetCompactMiss 同纪律：单守护
// 进程多用例防跨用例渗漏）＋探针缝与目标口还原（Cleanup 兜底）。
func resetDshLiveness(t *testing.T) {
	t.Helper()
	dshL1Mu.Lock()
	dshLiveLastPoll, dshLiveSidSeen, dshLiveGaps = 0, nil, nil
	dshL1Mu.Unlock()
	origProbe, origPort := dshHostProbe, dshHostProbePort
	t.Cleanup(func() { dshHostProbe, dshHostProbePort = origProbe, origPort })
}

// setDshHostProbe 注入宿主旁证假实现（票面验收：测试可注入枚举器假实现）。
func setDshHostProbe(t *testing.T, present bool, evidence string) {
	t.Helper()
	dshHostProbe = func() (bool, string) { return present, evidence }
}

// dshGet 构造 loopback Bearer GET（/dsh/health 查询面与守门共用）。
func dshGet(h http.Handler, token, uri string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, uri, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// ---- ① poll 序列记账：全局年龄＋sid last-seen（HTTP 真路径驱动钩子） ----

func TestDshLivenessPollSeriesAgesAndLastSeen(t *testing.T) {
	e := newDshLivenessEnv(t)
	h := makeHandler(e.d, "tok-lv", nil, nil)
	poll := func(body string) {
		t.Helper()
		if rec := dshPost(h, "tok-lv", "/dsh/poll", body); rec.Code != http.StatusOK {
			t.Fatalf("/dsh/poll = %d %q", rec.Code, rec.Body.String())
		}
	}
	poll(`{"agent":"dsh","sessions":[{"sid":"sidA"},{"sid":"sidB"}]}`) // t0
	e.advance(40)
	poll(`{"agent":"dsh","sessions":[{"sid":"sidB"},{"sid":"sidC"}]}`) // t0+40
	e.advance(10)                                                      // t0+50

	r := e.d.DshHealth()
	if got := r["last_poll_age_s"]; got != 10.0 {
		t.Fatalf("last_poll_age_s = %v, want 10（最近 poll 在 t0+40）", got)
	}
	sess, ok := r["sessions"].(map[string]float64)
	if !ok {
		t.Fatalf("sessions 缺或形不对: %v", r["sessions"])
	}
	if got := sess["sidA"]; got != 50.0 {
		t.Fatalf("sidA last-seen = %v, want 50（首轮后未再见）", got)
	}
	if got := sess["sidB"]; got != 10.0 {
		t.Fatalf("sidB last-seen = %v, want 10（两轮都被见到）", got)
	}
	if _, in := sess["sidC"]; !in {
		t.Fatalf("sidC 应在表（第二轮见到）: %v", sess)
	}

	// 坏形收窄：sessions 非数组/元素缺 sid——poll 到达照常盖章全局时刻，sid
	// 表不进脏账（DshPoll 同纪律）。
	e.advance(1)
	poll(`{"agent":"dsh","sessions":["oops",{"sid":""},{"noSidKey":1}]}`)
	r = e.d.DshHealth()
	if got := r["last_poll_age_s"]; got != 0.0 {
		t.Fatalf("坏形 poll 后 last_poll_age_s = %v, want 0（到达即盖章）", got)
	}
	sess = r["sessions"].(map[string]float64)
	if len(sess) != 3 || sess["sidA"] != 51.0 || sess["sidB"] != 11.0 {
		t.Fatalf("坏形 poll 不得动 sid 表: %v", sess)
	}

	// 端点面：GET /dsh/health 200＋关键字段在回包（此时最近 poll＝刚发生的
	// 坏形 poll，年龄 0）；错 token 401（/stats 同域鉴权惯例——查询面只挂
	// doGet 的 auth 之后）。
	rec := dshGet(h, "tok-lv", "/dsh/health")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"last_poll_age_s":0`) ||
		!strings.Contains(rec.Body.String(), `"host_processes_present":true`) {
		t.Fatalf("GET /dsh/health = %d %q", rec.Code, rec.Body.String())
	}
	if rec := dshGet(h, "WRONG", "/dsh/health"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("错 token = %d %q, want 401", rec.Code, rec.Body.String())
	}
}

// ---- ② 阈值推导矩阵：3×hint / 3×节律 / 90s 兜底注明假设（D13） ----

func TestDshLivenessOverdueThreshold(t *testing.T) {
	cases := []struct {
		name         string
		hint         float64
		pollSteps    []float64 // 相邻两轮 poll 的推进间隔（首轮在 t0）
		advanceAfter float64   // 末轮 poll 到查询时刻的推进
		noPoll       bool      // 全程不 poll（重启冷启动形态）
		wantSrc      string
		wantAssumed  bool
		wantEff      float64
		wantThr      float64
		wantOverdue  bool
		wantAge      any // nil=last_poll_age_s 应为 null
		wantRhythm   any // nil=poll_rhythm_s 应为 null
	}{
		{"有hint按3×hint未超龄", 45, []float64{40}, 50, false,
			"hint", false, 45, 135, false, 50.0, nil},
		{"有hint超龄", 45, nil, 136, false,
			"hint", false, 45, 135, true, 136.0, nil},
		{"无hint单轮无节律兜90s超龄", 0, nil, 271, false,
			"none", true, 90, 270, true, 271.0, nil},
		{"无hint有节律按3×节律", 0, []float64{10, 30}, 61, false,
			"rhythm", false, 20, 60, true, 61.0, 20.0},
		{"无hint无poll兜90s标注假设", 0, nil, 5000, true,
			"none", true, 90, 270, false, nil, nil},
		{"未poll过不判超龄(无证据不判定)", 30, nil, 99999, true,
			"hint", false, 30, 90, false, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newDshLivenessEnv(t)
			e.d.Cfg.DshCompact.PollHintS = tc.hint
			if !tc.noPoll {
				body := map[string]any{"agent": "dsh",
					"sessions": []any{map[string]any{"sid": "sidT"}}}
				noteDshLivenessPoll(body) // 首轮 t0
				for _, step := range tc.pollSteps {
					e.advance(step)
					noteDshLivenessPoll(body)
				}
			}
			e.advance(tc.advanceAfter)
			r := e.d.DshHealth()
			if r["interval_source"] != tc.wantSrc {
				t.Fatalf("interval_source = %v, want %s", r["interval_source"], tc.wantSrc)
			}
			if r["interval_assumed"] != tc.wantAssumed {
				t.Fatalf("interval_assumed = %v, want %v", r["interval_assumed"], tc.wantAssumed)
			}
			if r["effective_interval_s"] != tc.wantEff {
				t.Fatalf("effective_interval_s = %v, want %v", r["effective_interval_s"], tc.wantEff)
			}
			if r["overdue_threshold_s"] != tc.wantThr {
				t.Fatalf("overdue_threshold_s = %v, want %v", r["overdue_threshold_s"], tc.wantThr)
			}
			if r["poll_overdue"] != tc.wantOverdue {
				t.Fatalf("poll_overdue = %v, want %v", r["poll_overdue"], tc.wantOverdue)
			}
			if r["last_poll_age_s"] != tc.wantAge {
				t.Fatalf("last_poll_age_s = %v, want %v", r["last_poll_age_s"], tc.wantAge)
			}
			if r["poll_rhythm_s"] != tc.wantRhythm {
				t.Fatalf("poll_rhythm_s = %v, want %v", r["poll_rhythm_s"], tc.wantRhythm)
			}
		})
	}
}

// ---- ③ 宿主旁证两态（枚举器假实现注入）＋默认探针组合逻辑 ----

func TestDshLivenessHostPresence(t *testing.T) {
	e := newDshLivenessEnv(t)
	setDshHostProbe(t, true, "harness=1; port3080=listening")
	if r := e.d.DshHealth(); r["host_processes_present"] != true ||
		r["host_evidence"] != "harness=1; port3080=listening" {
		t.Fatalf("宿主在态 = %v/%v", r["host_processes_present"], r["host_evidence"])
	}
	setDshHostProbe(t, false, "harness=0; port3080=quiet")
	if r := e.d.DshHealth(); r["host_processes_present"] != false ||
		r["host_evidence"] != "harness=0; port3080=quiet" {
		t.Fatalf("宿主不在态 = %v/%v", r["host_processes_present"], r["host_evidence"])
	}
}

// TestDshLivenessHostProbeCombines 默认探针组合逻辑四象限：旁证布尔＝harness
// 进程腿 ∨ 3080 端口腿（任一即"宿主在跑"）；枚举不可知（known=false）不冒充
// "不在"，由端口腿独立裁决。两腿都是真环回探测（拨号随身监听器/已关端口，
// 零 console 零网络）。
func TestDshLivenessHostProbeCombines(t *testing.T) {
	resetDshLiveness(t) // 探针缝与目标口 Cleanup 还原（本用例不建 Daemon）
	origHarness := dshHarnessCount
	t.Cleanup(func() { dshHarnessCount = origHarness })

	listen := func(t *testing.T) int {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		return ln.Addr().(*net.TCPAddr).Port
	}
	quiet := func(t *testing.T) int {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close() // 关闭即空闲（竞窗由"断言只看腿明细"兜住）
		return port
	}
	cases := []struct {
		name         string
		harnessN     int
		harnessKnown bool
		portListening bool
		wantPresent  bool
	}{
		{"harness在即旁证(端口quiet)", 2, true, false, true},
		{"仅端口在也旁证(harness零)", 0, true, true, true},
		{"两腿皆无=不在", 0, true, false, false},
		{"枚举不可知端口在=在", 0, false, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, detail := tc.harnessN, "harness=0"
			if !tc.harnessKnown {
				detail = "harness=unknown(x)"
			}
			dshHarnessCount = func() (int, bool, string) { return n, tc.harnessKnown, detail }
			var port int
			if tc.portListening {
				port = listen(t)
			} else {
				port = quiet(t)
			}
			dshHostProbePort = port
			present, evidence := dshHostProbe()
			if present != tc.wantPresent {
				t.Fatalf("present = %v, want %v（evidence %q）", present, tc.wantPresent, evidence)
			}
			portWord := "quiet"
			if tc.portListening {
				portWord = "listening"
			}
			if !strings.Contains(evidence, "port"+strconv.Itoa(port)+"="+portWord) ||
				!strings.Contains(evidence, "harness=") {
				t.Fatalf("证据摘要缺腿明细: %q", evidence)
			}
		})
	}
}

// ---- ④ tasklist 输出解析（纯函数，跨平台表驱动）＋零闪窗在位 ----

func TestDshLivenessTasklistParse(t *testing.T) {
	out := strings.Join([]string{
		`"DeepSeek Harness.exe","17616","Console","1","123,456 K"`,
		`"DeepSeek Harness.exe","25676","Console","1","98,765 K"`,
		"",
		"信息: 没有运行的任务匹配指定标准。", // 无匹配时的本地化 INFO 行（不以引号起头）
	}, "\r\n")
	if got := dshParseTasklistOutput(out); got != 2 {
		t.Fatalf("两行映像＋INFO 行应记 2, got %d", got)
	}
	for _, tc := range []struct {
		name string
		out  string
		want int
	}{
		{"仅INFO行(中文)", "信息: 没有运行的任务匹配指定标准。\r\n", 0},
		{"仅INFO行(英文)", "INFO: No tasks are running which match the specified criteria.\r\n", 0},
		{"空输出", "", 0},
		{"别家进程不算", "\"node.exe\",\"42\",\"Console\",\"1\",\"9 K\"\r\n", 0},
	} {
		if got := dshParseTasklistOutput(tc.out); got != tc.want {
			t.Fatalf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestDshLivenessProbeCmdHideWindow 零闪窗铁律机器可验面：宿主枚举命令在
// Windows 分支必须带 SysProcAttr.HideWindow（daemon 生产形态无控制台，console
// 子进程不显式隐藏必闪窗）。
func TestDshLivenessProbeCmdHideWindow(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("HideWindow 是 Windows 专属 SysProcAttr 字段")
	}
	c := dshNewCmd(context.Background(), "tasklist")
	if c.SysProcAttr == nil || !c.SysProcAttr.HideWindow {
		t.Fatal("零闪窗铁律：宿主枚举命令缺 SysProcAttr.HideWindow")
	}
}
