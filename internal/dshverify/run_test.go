// run_test.go — 票04 验收：编排＋灯色判定＋输出分层＋--status＋红灯告警。
//
// 断言面（票面验收标准逐条）：
//   - 灯色矩阵表驱动（超龄×宿主在/不在×流水有无×L0 成败×L2 未装配各态，
//     外加 L2 装配三态：全绿/未过/执行错——绿优先级钉死「跑完即消除未验证黄」）；
//   - L2 未装配 → 黄（未完成验证）、不落锚、不滚已知良好指针、零告警；
//   - L2 装配全绿（假探针注入）→ 绿：落锚＋流水行带锚哈希＋指针滚动＋绿灯总义行；
//   - 红灯两分类告警文本（插件失联/验证失败）＋降级目标（已知良好三元组＋
//     installer 路径若登记）；黄路径零调用；
//   - --status 三要素（当前/已知良好/最近流水）＋未验证提醒＋只读（行数不变）；
//   - daemon 不可达 → 流水行 daemon 版本记 CLI 自身版本（cli: 前缀注明）。
//
// 绝不打真端口不真推送：L1 走 httptest 假端点，告警走注入的假 sender，
// 流水/锚全落 t.TempDir。L0 全过夹具复用 l0_test.go 的 newFullFixture
// （junction 夹具仅 Windows——非 Windows 跳过，与票02 同纪律）。
package dshverify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ferryman/internal/dshledger"
)

// ---- 测试助手 ----

// healthJSON 造 /dsh/health 应答体（键名＝票01 daemon DshHealth 的 API 契约）。
// lastAge 传 nil＝从未 poll（last_poll_age_s null）。
func healthJSON(lastAge any, overdue, host bool) string {
	b, err := json.Marshal(map[string]any{
		"last_poll_age_s":        lastAge,
		"sessions":               map[string]any{"sess-1": 4.2},
		"poll_hint_s":            30.0,
		"poll_rhythm_s":          nil,
		"interval_source":        "hint",
		"interval_assumed":       false,
		"effective_interval_s":   30.0,
		"overdue_threshold_s":    90.0,
		"poll_overdue":           overdue,
		"host_processes_present": host,
		"host_evidence":          "harness=2; port3080=listening",
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// fakeHealthSrv 假 daemon 管理口：/dsh/health 与 /stats 两端点；Token 非空时
// 校验 Bearer（钉鉴权形态）。
func fakeHealthSrv(t *testing.T, healthBody, statsVersion, token string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/dsh/health":
			_, _ = w.Write([]byte(healthBody))
		case "/stats":
			_, _ = fmt.Fprintf(w, `{"version":%q}`, statsVersion)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// alertRec 假告警 sender（验收：红灯恰一条且文本含降级目标；黄零调用）。
type alertRec struct {
	mu    sync.Mutex
	calls []string // "title|body" 全文，断言用
}

func (a *alertRec) send(title, body string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, title+"|"+body)
}

func (a *alertRec) n() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

func (a *alertRec) all() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return strings.Join(a.calls, "\n")
}

// fakeProbe 假 L2 探针（票05 填真实现前的接缝测试替身）。
type fakeProbe struct {
	res *ProbeResult
	err error
}

func (f *fakeProbe) Run() (*ProbeResult, error) { return f.res, f.err }

// fourFaces 四面形状（落锚原料；键＝dshledger 四面契约）。
func fourFaces() map[string]any {
	return map[string]any{
		dshledger.FaceDiscovery: "shape:discovery:v1",
		dshledger.FaceEvents:    "shape:events:v1",
		dshledger.FaceDuck:      "shape:duck:v1",
		dshledger.FaceBrowser:   "shape:browser:v1",
	}
}

// ---- L1Facts 构造 ----

func l1Fresh(host bool) *L1Facts {
	return &L1Facts{LastPollAgeS: 5, OverdueThresholdS: 90, HostPresent: host,
		HostEvidence: "harness=2; port3080=listening", IntervalSource: "hint", SessionCount: 1}
}

func l1Overdue(host bool) *L1Facts {
	f := l1Fresh(host)
	f.LastPollAgeS = 300
	f.PollOverdue = true
	return f
}

func l1Never(host bool) *L1Facts {
	f := l1Fresh(host)
	f.NeverPolled = true
	return f
}

func passL0() []CheckResult {
	return []CheckResult{
		{Name: ChkProfileJunction, Profile: "web", OK: true, Detail: "ok"},
		{Name: ChkHomePatchRoute, OK: true, Detail: "ok"},
	}
}

func failL0() []CheckResult {
	return append(passL0(), CheckResult{Name: ChkProfilePatchInsert, Profile: "web",
		Detail: "cordis.patch.yml 未找到含 id: ferryman-dsh 的 insert 块"})
}

// ---- 灯色矩阵（表驱动；票面验收第 1 条） ----

func TestJudgeMatrix(t *testing.T) {
	greenProbe := &ProbeResult{Green: true, Summary: "四痕全落", Faces: fourFaces()}
	redProbe := &ProbeResult{Green: false, Summary: "压缩链命令道未过"}
	cases := []struct {
		name       string
		l1         *L1Facts
		inLedger   bool
		l0         []CheckResult
		probe      *ProbeResult // nil = 未装配
		probeErr   error
		want       dshledger.Verdict
		wantRed    []string // 红理由须含的子串（每子串至少命中一条红理由）
		wantYellow []string // 黄理由须含的子串
	}{
		{
			name: "超龄且宿主在=红插件失联", l1: l1Overdue(true), l0: passL0(),
			want: dshledger.VerdictRed, wantRed: []string{"插件失联（有宿主无 poll）"},
		},
		{
			name: "从未poll且宿主在=红形态A", l1: l1Never(true), l0: passL0(),
			want: dshledger.VerdictRed, wantRed: []string{"插件失联"},
		},
		{
			name: "L0挂=红验证失败", l1: l1Fresh(true), l0: failL0(),
			want: dshledger.VerdictRed, wantRed: []string{"验证失败", "项未过", "profile_patch_insert"},
		},
		{
			name: "超龄+L0挂=双红并列", l1: l1Overdue(true), l0: failL0(),
			want:    dshledger.VerdictRed,
			wantRed: []string{"插件失联（有宿主无 poll）", "验证失败"},
		},
		{
			name: "超龄+L0挂+不在流水=红压倒黄", l1: l1Overdue(false), inLedger: false, l0: failL0(),
			want: dshledger.VerdictRed, wantRed: []string{"验证失败"},
		},
		{
			name: "超龄但宿主不在=黄宿主未运行", l1: l1Overdue(false), l0: passL0(),
			want: dshledger.VerdictYellow, wantYellow: []string{"宿主未运行"},
		},
		{
			name: "从未poll且宿主不在=黄宿主未运行", l1: l1Never(false), l0: passL0(),
			want: dshledger.VerdictYellow, wantYellow: []string{"宿主未运行"},
		},
		{
			name: "全新鲜+L2未装配+在流水=黄未完成", l1: l1Fresh(true), inLedger: true, l0: passL0(),
			want: dshledger.VerdictYellow, wantYellow: []string{"L2 探针未装配", "未完成验证"},
		},
		{
			name: "全新鲜+L2未装配+不在流水=黄双理由", l1: l1Fresh(false), inLedger: false, l0: passL0(),
			want: dshledger.VerdictYellow, wantYellow: []string{"版本未验证", "L2 探针未装配"},
		},
		{
			name: "L1无从求值=黄未完成", l1: nil, l0: passL0(),
			want: dshledger.VerdictYellow, wantYellow: []string{"L1 无从求值"},
		},
		{
			name: "L2装配未过=红", l1: l1Fresh(true), inLedger: true, l0: passL0(), probe: redProbe,
			want: dshledger.VerdictRed, wantRed: []string{"验证失败", "L2"},
		},
		{
			name: "L2装配执行错=黄未完成（非断言失败）", l1: l1Fresh(true), inLedger: true, l0: passL0(),
			probeErr: errors.New("沙箱起栈失败"),
			want:     dshledger.VerdictYellow, wantYellow: []string{"L2 探针执行出错"},
		},
		{
			name: "L2装配全绿+不在流水=绿（跑完即消除未验证）", l1: l1Fresh(true), inLedger: false,
			l0: passL0(), probe: greenProbe,
			want: dshledger.VerdictGreen,
		},
		{
			name: "L2装配全绿+宿主无旁证=绿（poll 新鲜是主证据）", l1: l1Fresh(false), inLedger: true,
			l0: passL0(), probe: greenProbe,
			want: dshledger.VerdictGreen,
		},
		{
			name: "L2装配全绿但poll超龄无宿主=黄非绿", l1: l1Overdue(false), inLedger: true,
			l0: passL0(), probe: greenProbe,
			want: dshledger.VerdictYellow, wantYellow: []string{"宿主未运行"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := JudgeInput{L0: tc.l0, L1: tc.l1, VersionInLedger: tc.inLedger,
				Probe: tc.probe, ProbeErr: tc.probeErr}
			j := Judge(in)
			if j.Verdict != tc.want {
				t.Fatalf("判定=%s want %s（reds=%v yellows=%v）", j.Verdict, tc.want, j.Reds, j.Yellows)
			}
			join := func(ss []string) string { return strings.Join(ss, "\n") }
			for _, sub := range tc.wantRed {
				if !strings.Contains(join(j.Reds), sub) {
					t.Fatalf("红理由缺 %q: %v", sub, j.Reds)
				}
			}
			for _, sub := range tc.wantYellow {
				if !strings.Contains(join(j.Yellows), sub) {
					t.Fatalf("黄理由缺 %q: %v", sub, j.Yellows)
				}
			}
			switch tc.want {
			case dshledger.VerdictRed:
				if len(j.Reds) == 0 {
					t.Fatalf("红判定须有红理由")
				}
				if len(j.Yellows) != 0 {
					t.Fatalf("红判定不填黄理由（红压倒黄）: %v", j.Yellows)
				}
			case dshledger.VerdictGreen:
				if len(j.Reds) != 0 || len(j.Yellows) != 0 {
					t.Fatalf("绿判定不应带红/黄理由: %v / %v", j.Reds, j.Yellows)
				}
			}
		})
	}
}

// ---- Run 编排 ----

// TestRunAllPassL2Unassembled L0/L1 全过＋L2 未装配 → 黄（未完成验证）；
// 不落锚不滚已知良好指针（票面专测）；零告警；流水落黄行（锚哈希空）。
func TestRunAllPassL2Unassembled(t *testing.T) {
	dshRoot, installDir, _ := newFullFixture(t)
	srv := fakeHealthSrv(t, healthJSON(5.0, false, true), "v0.9.8", "")
	dataDir := t.TempDir()
	rec := &alertRec{}
	buf := &bytes.Buffer{}
	code := Run(Options{DSHRoot: dshRoot, DSHInstall: installDir, DockBaseURL: fixtureDockURL,
		Health: &HealthClient{BaseURL: srv.URL}, DataDir: dataDir,
		CLIVersion: "test-cli", Alert: rec.send, Out: buf})
	if code != 0 {
		t.Fatalf("exit=%d want 0（黄不是失败）:\n%s", code, buf.String())
	}
	log, err := dshledger.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := log.List()
	if err != nil || len(rows) != 1 {
		t.Fatalf("流水行数=%d err=%v want 1", len(rows), err)
	}
	if rows[0].Verdict != dshledger.VerdictYellow {
		t.Fatalf("流水判定=%s want yellow", rows[0].Verdict)
	}
	if rows[0].AnchorHash != "" {
		t.Fatalf("黄行不应带锚哈希: %+v", rows[0])
	}
	if rows[0].DSHVersion != fixtureDSHVersion || rows[0].PluginVersion != "0.2.0" ||
		rows[0].DaemonVersion != "v0.9.8" {
		t.Fatalf("流水三元组不符: %+v", rows[0])
	}
	// 不落锚：anchors 目录无锚文件
	anchors, err := filepath.Glob(filepath.Join(dataDir, "dshledger", "anchors", "*.json"))
	if err != nil || len(anchors) != 0 {
		t.Fatalf("L2 未装配不应落锚: %v err=%v", anchors, err)
	}
	// 不滚指针：无已知良好
	if kg, err := log.RecentKnownGood(); err != nil || kg != nil {
		t.Fatalf("已知良好指针=%v err=%v want nil（黄行不抬指针）", kg, err)
	}
	// 零告警
	if rec.n() != 0 {
		t.Fatalf("黄不推——告警调用 %d 次: %s", rec.n(), rec.all())
	}
	out := buf.String()
	for _, sub := range []string{"L2 探针", "未装配", "仅代表 web 宿主形态（沙箱）",
		"判定: 黄", "未完成验证"} {
		if !strings.Contains(out, sub) {
			t.Fatalf("输出缺 %q:\n%s", sub, out)
		}
	}
	if strings.Contains(out, "绿灯总义") {
		t.Fatalf("非绿不应出现绿灯总义:\n%s", out)
	}
}

// TestRunGreenWithFakeProbe 假探针全绿 → 绿：落锚＋流水行锚哈希同源＋指针滚动＋
// 绿灯总义行＋零告警。
func TestRunGreenWithFakeProbe(t *testing.T) {
	dshRoot, installDir, _ := newFullFixture(t)
	srv := fakeHealthSrv(t, healthJSON(5.0, false, true), "v0.9.8", "")
	dataDir := t.TempDir()
	rec := &alertRec{}
	buf := &bytes.Buffer{}
	code := Run(Options{DSHRoot: dshRoot, DSHInstall: installDir, DockBaseURL: fixtureDockURL,
		Health: &HealthClient{BaseURL: srv.URL}, DataDir: dataDir,
		Probe:      &fakeProbe{res: &ProbeResult{Green: true, Summary: "四痕全落", Faces: fourFaces()}},
		CLIVersion: "test-cli", Alert: rec.send, Out: buf})
	if code != 0 {
		t.Fatalf("exit=%d want 0:\n%s", code, buf.String())
	}
	log, _ := dshledger.New(dataDir)
	rows, _ := log.List()
	if len(rows) != 1 || rows[0].Verdict != dshledger.VerdictGreen {
		t.Fatalf("流水行不符: %+v", rows)
	}
	store, err := dshledger.NewAnchorStore(dataDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	a, hash, err := store.Latest()
	if err != nil || a == nil {
		t.Fatalf("应落锚: a=%v err=%v", a, err)
	}
	if a.DSHVersion != fixtureDSHVersion {
		t.Fatalf("锚绑 DSH 版本不符: %+v", a)
	}
	if rows[0].AnchorHash != hash {
		t.Fatalf("流水行锚哈希 %q 与锚存储 %q 不同源", rows[0].AnchorHash, hash)
	}
	if kg, err := log.RecentKnownGood(); err != nil || kg == nil || kg.DSHVersion != fixtureDSHVersion {
		t.Fatalf("已知良好指针未滚动: %+v err=%v", kg, err)
	}
	if rec.n() != 0 {
		t.Fatalf("绿不推——告警 %d 次", rec.n())
	}
	out := buf.String()
	for _, sub := range []string{"判定: 绿", "绿灯总义",
		"web 沙箱功能全验证＋生产三 profile 静态/挂载验证"} {
		if !strings.Contains(out, sub) {
			t.Fatalf("输出缺 %q:\n%s", sub, out)
		}
	}
}

// TestRunRedLostPluginAlert 红·插件失联：超龄+宿主在 → 恰一条告警，文本含
// 「插件失联（有宿主无 poll）」与降级目标（无已知良好时如实说）；exit 1。
func TestRunRedLostPluginAlert(t *testing.T) {
	dshRoot, installDir, _ := newFullFixture(t)
	srv := fakeHealthSrv(t, healthJSON(300.0, true, true), "v0.9.8", "")
	dataDir := t.TempDir()
	rec := &alertRec{}
	buf := &bytes.Buffer{}
	code := Run(Options{DSHRoot: dshRoot, DSHInstall: installDir, DockBaseURL: fixtureDockURL,
		Health: &HealthClient{BaseURL: srv.URL}, DataDir: dataDir,
		CLIVersion: "test-cli", Alert: rec.send, Out: buf})
	if code != 1 {
		t.Fatalf("红灯 exit=%d want 1:\n%s", code, buf.String())
	}
	if rec.n() != 1 {
		t.Fatalf("告警调用 %d 次 want 1", rec.n())
	}
	all := rec.all()
	for _, sub := range []string{"插件失联（有宿主无 poll）", "降级目标", "尚无已知良好"} {
		if !strings.Contains(all, sub) {
			t.Fatalf("告警文本缺 %q:\n%s", sub, all)
		}
	}
	log, _ := dshledger.New(dataDir)
	rows, _ := log.List()
	if len(rows) != 1 || rows[0].Verdict != dshledger.VerdictRed {
		t.Fatalf("红灯应落红行: %+v", rows)
	}
}

// TestRunRedL0FailAlertWithKnownGood 红·验证失败＋已登记的已知良好：空 DSH 根
// （全平台可跑，L0 逐项 fail）→ 告警文本含「验证失败（N 项未过）」＋已知良好
// 三元组＋installer 路径。
func TestRunRedL0FailAlertWithKnownGood(t *testing.T) {
	srv := fakeHealthSrv(t, healthJSON(5.0, false, true), "v0.9.8", "")
	dataDir := t.TempDir()
	log, err := dshledger.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.Append("43.0.0", "0.1.0", "v0.9.7", dshledger.VerdictGreen,
		"deadbeef", `C:\inst\dsh-setup-43.0.0.exe`); err != nil {
		t.Fatal(err)
	}
	rec := &alertRec{}
	buf := &bytes.Buffer{}
	code := Run(Options{DSHRoot: t.TempDir(), DockBaseURL: fixtureDockURL, // 空根→L0 全红
		Health: &HealthClient{BaseURL: srv.URL}, DataDir: dataDir,
		CLIVersion: "test-cli", Alert: rec.send, Out: buf})
	if code != 1 {
		t.Fatalf("红灯 exit=%d want 1:\n%s", code, buf.String())
	}
	if rec.n() != 1 {
		t.Fatalf("告警调用 %d 次 want 1", rec.n())
	}
	all := rec.all()
	for _, sub := range []string{"验证失败", "项未过", "降级目标（已知良好）",
		"43.0.0", "0.1.0", "v0.9.7", `C:\inst\dsh-setup-43.0.0.exe`} {
		if !strings.Contains(all, sub) {
			t.Fatalf("告警文本缺 %q:\n%s", sub, all)
		}
	}
}

// TestRunYellowHostDownNoAlert 黄·宿主未运行：超龄+宿主不在 → 零告警、exit 0。
func TestRunYellowHostDownNoAlert(t *testing.T) {
	dshRoot, installDir, _ := newFullFixture(t)
	srv := fakeHealthSrv(t, healthJSON(300.0, true, false), "v0.9.8", "")
	dataDir := t.TempDir()
	rec := &alertRec{}
	buf := &bytes.Buffer{}
	code := Run(Options{DSHRoot: dshRoot, DSHInstall: installDir, DockBaseURL: fixtureDockURL,
		Health: &HealthClient{BaseURL: srv.URL}, DataDir: dataDir,
		CLIVersion: "test-cli", Alert: rec.send, Out: buf})
	if code != 0 {
		t.Fatalf("黄 exit=%d want 0:\n%s", code, buf.String())
	}
	if rec.n() != 0 {
		t.Fatalf("黄不推——告警 %d 次: %s", rec.n(), rec.all())
	}
	if !strings.Contains(buf.String(), "宿主未运行") {
		t.Fatalf("输出缺宿主未运行:\n%s", buf.String())
	}
}

// TestRunDaemonOffline 守护不在线 → 黄（L1 无从求值）；流水行 daemon 版本记
// CLI 自身版本（cli: 前缀注明）；输出注明代记。
func TestRunDaemonOffline(t *testing.T) {
	dshRoot, installDir, _ := newFullFixture(t)
	srv := fakeHealthSrv(t, "{}", "", "") // 立即关掉＝端口死
	srv.Close()
	dataDir := t.TempDir()
	rec := &alertRec{}
	buf := &bytes.Buffer{}
	code := Run(Options{DSHRoot: dshRoot, DSHInstall: installDir, DockBaseURL: fixtureDockURL,
		Health: &HealthClient{BaseURL: srv.URL}, DataDir: dataDir,
		CLIVersion: "test-cli", Alert: rec.send, Out: buf})
	if code != 0 {
		t.Fatalf("黄 exit=%d want 0:\n%s", code, buf.String())
	}
	if rec.n() != 0 {
		t.Fatalf("无红灯依据不推——告警 %d 次", rec.n())
	}
	log, _ := dshledger.New(dataDir)
	rows, _ := log.List()
	if len(rows) != 1 || rows[0].DaemonVersion != "cli:test-cli" {
		t.Fatalf("daemon 版本应记 CLI 自身版本（cli: 前缀）: %+v", rows)
	}
	out := buf.String()
	for _, sub := range []string{"L1 无从求值", "守护不在线", "CLI 自身版本"} {
		if !strings.Contains(out, sub) {
			t.Fatalf("输出缺 %q:\n%s", sub, out)
		}
	}
}

// ---- --status ----

// TestStatusThreeElements --status 三要素：当前版本（含插件版本）/已知良好/
// 最近流水行；当前版本不在流水 → 未验证提醒；只读（行数不变）。
func TestStatusThreeElements(t *testing.T) {
	dshRoot, installDir, _ := newFullFixture(t)
	dataDir := t.TempDir()
	log, err := dshledger.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.Append("43.0.0", "0.1.0", "v0.9.7", dshledger.VerdictGreen,
		"deadbeef", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := log.Append("43.5.0", "0.1.5", "v0.9.8", dshledger.VerdictYellow,
		"", ""); err != nil {
		t.Fatal(err)
	}
	buf := &bytes.Buffer{}
	code := Status(StatusOptions{DSHRoot: dshRoot, DSHInstall: installDir,
		DataDir: dataDir, Out: buf})
	if code != 0 {
		t.Fatalf("exit=%d want 0:\n%s", code, buf.String())
	}
	out := buf.String()
	for _, sub := range []string{"当前 DSH 版本", fixtureDSHVersion, "0.2.0",
		"已知良好", "43.0.0", "最近流水", "43.5.0", "黄",
		"不在流水"} {
		if !strings.Contains(out, sub) {
			t.Fatalf("输出缺 %q:\n%s", sub, out)
		}
	}
	// 只读：流水行数不变
	rows, _ := log.List()
	if len(rows) != 2 {
		t.Fatalf("--status 落了盘（只读契约）: 行数=%d", len(rows))
	}
}

// TestStatusCurrentVerified 当前版本已在流水 → 无未验证提醒。
func TestStatusCurrentVerified(t *testing.T) {
	dshRoot, installDir, _ := newFullFixture(t)
	dataDir := t.TempDir()
	log, _ := dshledger.New(dataDir)
	if _, err := log.Append(fixtureDSHVersion, "0.2.0", "v0.9.8",
		dshledger.VerdictYellow, "", ""); err != nil {
		t.Fatal(err)
	}
	buf := &bytes.Buffer{}
	if code := Status(StatusOptions{DSHRoot: dshRoot, DSHInstall: installDir,
		DataDir: dataDir, Out: buf}); code != 0 {
		t.Fatalf("exit=%d:\n%s", code, buf.String())
	}
	if strings.Contains(buf.String(), "不在流水") {
		t.Fatalf("当前版本已在流水不应提醒未验证:\n%s", buf.String())
	}
}

// ---- health_client ----

// TestHealthClientParse 假端点解析：null 年龄→从未 poll；Bearer 鉴权形态；
// /stats 版本自报。
func TestHealthClientParse(t *testing.T) {
	srv := fakeHealthSrv(t, healthJSON(nil, false, true), "v0.9.8", "tok")
	c := &HealthClient{BaseURL: srv.URL, Token: "tok"}
	h, err := c.Health()
	if err != nil {
		t.Fatal(err)
	}
	if h.LastPollAgeS != nil {
		t.Fatalf("null 年龄应解析为 nil: %+v", h.LastPollAgeS)
	}
	if h.PollOverdue || !h.HostProcessesPresent {
		t.Fatalf("解析不符: %+v", h)
	}
	if !strings.Contains(h.HostEvidence, "port3080") {
		t.Fatalf("宿主旁证证据缺: %+v", h)
	}
	if h.OverdueThresholdS != 90 {
		t.Fatalf("阈值解析不符: %+v", h.OverdueThresholdS)
	}
	v, err := c.StatsVersion()
	if err != nil || v != "v0.9.8" {
		t.Fatalf("daemon 版本=%q err=%v", v, err)
	}
	// 鉴权形态：错 token → errAuth
	bad := &HealthClient{BaseURL: srv.URL, Token: "wrong"}
	if _, err := bad.Health(); !errors.Is(err, ErrAuth) {
		t.Fatalf("错 token 应报鉴权失败: %v", err)
	}
}

// TestHealthClientUnreachable 死端口 → errDaemonUnreachable（哨兵可判）。
func TestHealthClientUnreachable(t *testing.T) {
	srv := fakeHealthSrv(t, "{}", "", "")
	srv.Close()
	c := &HealthClient{BaseURL: srv.URL}
	if _, err := c.Health(); !errors.Is(err, ErrDaemonUnreachable) {
		t.Fatalf("死端口应报守护不在线: %v", err)
	}
	if _, err := c.StatsVersion(); !errors.Is(err, ErrDaemonUnreachable) {
		t.Fatalf("死端口 /stats 应报守护不在线: %v", err)
	}
}

// TestFactsFromHealth Health → L1Facts 提取：null=从未 poll；布尔直通。
func TestFactsFromHealth(t *testing.T) {
	age := 300.0
	h := &Health{LastPollAgeS: &age, PollOverdue: true, HostProcessesPresent: true,
		HostEvidence: "ev", OverdueThresholdS: 90, IntervalSource: "hint"}
	f := FactsFromHealth(h)
	if f.NeverPolled || !f.PollOverdue || !f.HostPresent || f.LastPollAgeS != 300 {
		t.Fatalf("提取不符: %+v", f)
	}
	nilAge := &Health{LastPollAgeS: nil}
	if f := FactsFromHealth(nilAge); !f.NeverPolled {
		t.Fatalf("null 年龄应提取为 NeverPolled: %+v", f)
	}
}

// ---- 版本读取 ----

// TestReadVersions 安装树版本与插件版本读取：夹具 → "44.0.0"/"0.2.0"；
// 缺席 → 空串（展示层译「未知」）。
func TestReadVersions(t *testing.T) {
	dshRoot, installDir, _ := newFullFixture(t)
	if v := readDSHVersion(installDir); v != fixtureDSHVersion {
		t.Fatalf("DSH 版本=%q want %q", v, fixtureDSHVersion)
	}
	if v := readPluginVersion(dshRoot, nil); v != "0.2.0" {
		t.Fatalf("插件版本=%q want 0.2.0", v)
	}
	if v := readDSHVersion(t.TempDir()); v != "" {
		t.Fatalf("缺失应空串: %q", v)
	}
	if v := readPluginVersion(t.TempDir(), nil); v != "" {
		t.Fatalf("缺失应空串: %q", v)
	}
}
