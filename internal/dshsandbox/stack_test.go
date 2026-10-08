// stack_test.go — 沙箱起栈/收尾单测（票面验收标准①：注入假 exe/假 DSH_CLI，
// 断言 env、参数、junction 备料、端口锁、进程全停）。
//
// 假件形态：spawnProc 替换为进程记录器——「假 daemon」在 FERRYMAN_DATA 落
// daemon.token 并起内存 HTTP 服务（/stats 200＋/shutdown 收口），「假 web」把
// 就绪行写进 StdoutFile——Start 全链（锁→备料→生成面→扫描→拉起→就绪）走真
// 逻辑、零真进程零真 DSH。junction 备料 Windows 用真 mklink /J（验收标准钉死
// mklink 形态；mklink 子进程 HideWindow）；非 Windows 注入符号链接假件
// （junction 备料是 Windows 安装形态，非 Windows 由 GOOS 守卫跳过真 mklink）。
package dshsandbox

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"ferryman/internal/config"
)

// ---- 假件 ----

// fakeProc 假进程（kill 记录＋可选收口钩）。
type fakeProc struct {
	pid    int
	killed bool
	onKill func()
}

func (f *fakeProc) KillTree() error {
	if f.killed {
		return nil
	}
	f.killed = true
	if f.onKill != nil {
		f.onKill()
	}
	return nil
}

// fakeSpawner spawnProc 假件：记录 spec；对 daemon 形（env 带 FERRYMAN_CONFIG）
// 起 /stats 假服务＋落 token；对 web 形写就绪行。
type fakeSpawner struct {
	mu        sync.Mutex
	specs     []procSpec
	procs     []*fakeProc
	webPort   int // 0＝按 args 解析
	daemonSrv *fakeDaemonSrv
}

func (fs *fakeSpawner) spawn(spec procSpec) (*managedProc, error) {
	fs.mu.Lock()
	fs.specs = append(fs.specs, spec)
	fs.mu.Unlock()
	env := envMap(spec.Env)
	fp := &fakeProc{pid: 1000 + len(fs.specs)}
	if v, ok := env["FERRYMAN_CONFIG"]; ok && v != "" {
		// 假 daemon：token 落盘＋/stats 服务（/shutdown 即收口）。
		srv := newFakeDaemonSrv(env["FERRYMAN_DATA"], env["FERRYMAN_PORT"])
		fs.mu.Lock()
		fs.daemonSrv = srv
		fs.mu.Unlock()
		fp.onKill = srv.close
		if err := srv.start(); err != nil {
			return nil, err
		}
	} else if strings.Contains(strings.Join(spec.Args, " "), "web") {
		// 假 web：就绪行写日志（含真 token）。
		port := fs.webPort
		if port == 0 {
			for i, a := range spec.Args {
				if a == "--port" && i+1 < len(spec.Args) {
					fmt.Sscanf(spec.Args[i+1], "%d", &port)
				}
			}
		}
		line := fmt.Sprintf("dsh web: http://127.0.0.1:%d/?token=fakeWebToken1234567890123456789\n", port)
		_ = os.WriteFile(spec.StdoutFile, []byte(line), 0o644)
		fp.onKill = func() {}
	}
	fs.mu.Lock()
	fs.procs = append(fs.procs, fp)
	fs.mu.Unlock()
	return &managedProc{PID: fp.pid, killFn: fp.KillTree}, nil
}

// fakeDaemonSrv 假 daemon 控制口（/stats 200＋/shutdown 收口）。
type fakeDaemonSrv struct {
	dataDir string
	port    string
	ln      net.Listener
	done    chan struct{}
	once    sync.Once
}

func newFakeDaemonSrv(dataDir, port string) *fakeDaemonSrv {
	return &fakeDaemonSrv{dataDir: dataDir, port: port, done: make(chan struct{})}
}

func (s *fakeDaemonSrv) start() error {
	if err := os.MkdirAll(s.dataDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s.dataDir, "daemon.token"),
		[]byte("fake-daemon-token"), 0o644); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+s.port)
	if err != nil {
		return err
	}
	s.ln = ln
	mux := http.NewServeMux()
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"version":"fake","gate_calls_by_agent":{"dsh":0}}`)
	})
	mux.HandleFunc("/dsh/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"last_poll_age_s":null,"sessions":{}}`)
	})
	mux.HandleFunc("/shutdown", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		s.close()
	})
	go func() { _ = http.Serve(ln, mux) }()
	return nil
}

func (s *fakeDaemonSrv) close() {
	s.once.Do(func() {
		close(s.done)
		if s.ln != nil {
			_ = s.ln.Close()
		}
	})
}

// envMap env 切片 → 键值表。
func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	return m
}

// ---- 夹具 ----

// writeFixture 沙箱测试夹具树：home 源＋插件源（含 junction 与 bak 残留的
// 剔除面；Windows 侧造生产同形插件 junction——staging 须换装备料快照）。
// 返回 (homeSrc, pluginSrc, appPkgDir, stalePluginDir)。
func writeFixture(t *testing.T) (homeSrc, pluginSrc, appPkgDir, stalePluginDir string) {
	t.Helper()
	base := t.TempDir()
	pluginSrc = filepath.Join(base, "plugin", "ferryman-dsh")
	homeSrc = filepath.Join(base, "dsh-home")
	appPkgDir = filepath.Join(base, "app-res", "dsh-hypatia")
	stalePluginDir = filepath.Join(base, "stale-plugin", "ferryman-dsh")
	for _, d := range []string{
		filepath.Join(pluginSrc, "src"),
		filepath.Join(homeSrc, "profiles", "web", "node_modules"),
		filepath.Join(homeSrc, "sessions", "old"),
		filepath.Join(homeSrc, "remote-link"),
		appPkgDir,
		stalePluginDir,
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// 插件源（manifest/入口/五事件位注册/client.js 常量——锚采集面用同树）。
	writeFile(t, filepath.Join(pluginSrc, "package.json"), `{
  "name": "ferryman-dsh", "version": "9.9.9-test", "main": "src/index.ts",
  "exports": {".": "./src/index.ts", "./package.json": "./package.json", "./client": "./client.js"},
  "dsh": {"manifestVersion": 1, "bundle": {"patch": "cordis.patch.yml"},
          "client": {"platform": "web", "export": "./client.js"}}
}`)
	writeFile(t, filepath.Join(pluginSrc, "src", "index.ts"),
		"export function registerHooks(ctx, deps) {\n"+
			"  ctx.on(\"agent/pre-step\", h1); ctx.on(\"agent/created\", h2);\n"+
			"  ctx.on(\"session/event\", h3); ctx.on(\"agent/disposed\", h4);\n"+
			"  ctx.on(\"agent/status\", h5);\n}\n")
	writeFile(t, filepath.Join(pluginSrc, "src", "compact.ts"), "export const DEFAULT_POLL_INTERVAL_MS = 30000;\n")
	writeFile(t, filepath.Join(pluginSrc, "client.js"),
		"var NAMESPACE = 'ferrymanBlocked';\nvar DOCK_SLOT = 'conversation.composer.dock';\n"+
			"window.__ModuleLoader__.load({id:'ferryman-dsh',factory:f});\n")
	writeFile(t, filepath.Join(pluginSrc, "cordis.patch.yml"),
		"- insert:\n    - id: ferryman-dsh\n      name: ./ferryman-dsh/index.mjs\n")
	// home 源：patch（15722 渡口行——待重写）＋env＋profile 面＋junction 群。
	writeFile(t, filepath.Join(homeSrc, "cordis.patch.yml"),
		"# ferryman-takeover\n- id: llm-deepseek\n  name: '@deepseek-ai/dsh-llm-deepseek-api-key'\n"+
			"  config:\n    baseURL: http://127.0.0.1:15722\n    apiKeyEnv: FERRYMAN_DOCK_TOKEN\n"+
			"- id: agent-default-model\n  name: '@deepseek-ai/dsh-agent-default-model'\n"+
			"  config:\n    provider: deepseek-official\n    model: claude-opus-5\n")
	writeFile(t, filepath.Join(homeSrc, ".env"), "FERRYMAN_DOCK_TOKEN=FERRYMAN_MANAGED\n")
	writeFile(t, filepath.Join(homeSrc, "sessions", "old", "leftover.jsonl"), "{}\n")
	writeFile(t, filepath.Join(homeSrc, "remote-link", "x.txt"), "x\n")
	writeFile(t, filepath.Join(homeSrc, "cordis.patch.yml.bak-ferryman-x"), "- old\n")
	writeFile(t, filepath.Join(homeSrc, "settings.yaml.imported"), "provider: zai-coding-cn\n")
	writeFile(t, filepath.Join(homeSrc, "profiles", "web", "cordis.patch.yml"),
		"- id: ios-control\n  name: dsh-ios-control\n  config:\n    port: 3081\n"+
			"- insert:\n    - id: ferryman-dsh\n      name: ./ferryman-dsh/index.mjs\n")
	writeFile(t, filepath.Join(homeSrc, "profiles", "web", "package.json"), `{
  "name": "dsh-profile-web", "private": true,
  "dependencies": {"dsh-hypatia": "^0.2.3", "dsh-ios-control": "github:x/y",
                   "ferryman-dsh": "file:C:/nope/plugin/ferryman-dsh"},
  "dsh": {"profile": {"bundles": ["@deepseek-ai/dsh-base", "@deepseek-ai/dsh-web-app",
                                   "dsh-ios-control", "ferryman-dsh", "dsh-hypatia"]}}
}`)
	writeFile(t, filepath.Join(appPkgDir, "package.json"), `{"name":"dsh-hypatia"}`)
	// 生产同形旧插件（stale）：junction 位（Windows）＋node_modules 实目录拷——
	// staging 须整体换装备料快照，不渗入旧内容。
	writeFile(t, filepath.Join(stalePluginDir, "package.json"), `{"name":"ferryman-dsh","version":"0.0.1-stale"}`)
	writeFile(t, filepath.Join(homeSrc, "profiles", "web", "node_modules", "ferryman-dsh", "package.json"),
		`{"name":"ferryman-dsh","version":"0.0.1-stale"}`)
	return homeSrc, pluginSrc, appPkgDir, stalePluginDir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mkJunctionHidden 测试夹具造 junction（mklink /J；console 子进程 HideWindow——
// l0_test 同款纪律）。
func mkJunctionHidden(t *testing.T, link, target string) {
	t.Helper()
	cmd := exec.Command("cmd", "/c", "mklink", "/J", link, target)
	a := new(syscall.SysProcAttr)
	if f := reflect.ValueOf(a).Elem().FieldByName("HideWindow"); f.IsValid() {
		f.SetBool(true)
	}
	cmd.SysProcAttr = a
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mklink /J %s → %s: %v: %s", link, target, err, out)
	}
}

// fixtureOptions 假件装配（假 spawnProc＋夹具路径＋短超时）。
func fixtureOptions(t *testing.T, fs *fakeSpawner, homeSrc, pluginSrc string) Options {
	t.Helper()
	fakeEXE := filepath.Join(t.TempDir(), "ferryman-fake.exe")
	writeFile(t, fakeEXE, "MZ") // 假 exe（存在即可——不真执行）
	fakeDSH := filepath.Join(t.TempDir(), "dsh.cmd")
	writeFile(t, fakeDSH, "@echo off\n")
	return Options{
		ExePath:            fakeEXE,
		DSHCLI:             fakeDSH,
		DSHHomeSrc:         homeSrc,
		PluginSrc:          pluginSrc,
		TempRoot:           t.TempDir(),
		DaemonReadyTimeout: 5 * time.Second,
		WebReadyTimeout:    5 * time.Second,
		HTTPTimeout:        2 * time.Second,
		DockUpstreams: map[string]DockUpstream{
			"cheap": {BaseURL: "https://api.example.test/v1", APIKey: "sk-test",
				ModelMap: map[string]string{"default": "model-x", "opus": "model-x"}},
		},
		DockActive: "cheap",
	}
}

// ---- 起栈/收尾主测 ----

// TestStartStopFakeStack 全链：锁→备料→生成面→拉起假 daemon/web→就绪→Stop
// （进程全停＋目录清理＋锁释放）。断言 env/参数/junction 备料（验收标准①）。
func TestStartStopFakeStack(t *testing.T) {
	homeSrc, pluginSrc, appPkgDir, stalePluginDir := writeFixture(t)
	// home 源放两个真 junction（生产同形插件位＋node_modules 包位——staging
	// 须前者换装备料快照、后者按原目标重链不 deref）。
	hypLink := filepath.Join(homeSrc, "profiles", "web", "node_modules", "dsh-hypatia")
	linkFake := false
	if runtime.GOOS == "windows" {
		mkJunctionHidden(t, hypLink, appPkgDir)
		mkJunctionHidden(t, filepath.Join(homeSrc, "profiles", "web", "ferryman-dsh"), stalePluginDir)
	} else {
		linkFake = true // 非 Windows：makeJunctions 假件（os.Symlink）
	}
	fs := &fakeSpawner{}
	if linkFake {
		orig := makeJunctions
		makeJunctions = func(pairs [][2]string) error {
			for _, p := range pairs {
				if err := os.Symlink(p[1], p[0]); err != nil && !os.IsExist(err) {
					return err
				}
			}
			return nil
		}
		t.Cleanup(func() { makeJunctions = orig })
	}
	origSpawn := spawnProc
	spawnProc = fs.spawn
	t.Cleanup(func() { spawnProc = origSpawn })

	s, err := Start(fixtureOptions(t, fs, homeSrc, pluginSrc))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	root := s.Root
	t.Cleanup(func() { s.Stop() })

	// ---- 进程 spec 断言（env/参数——e2e start.sh 形状） ----
	fs.mu.Lock()
	specs := append([]procSpec(nil), fs.specs...)
	fs.mu.Unlock()
	if len(specs) != 2 {
		t.Fatalf("拉起进程数=%d want 2（daemon＋web）", len(specs))
	}
	daemon, web := specs[0], specs[1]
	if !strings.Contains(strings.Join(daemon.Args, " "), "serve") ||
		!containsArg(daemon.Args, "--no-tray") || !containsArg(daemon.Args, "--no-browser") ||
		!containsArg(daemon.Args, "--smoke") || !containsArg(daemon.Args, "--port") {
		t.Errorf("daemon 参数形状异常: %v", daemon.Args)
	}
	dEnv := envMap(daemon.Env)
	if dEnv["USERPROFILE"] != filepath.ToSlash(s.Root) {
		t.Errorf("daemon USERPROFILE 未钉沙箱: %q", dEnv["USERPROFILE"])
	}
	if dEnv["FERRYMAN_CONFIG"] != filepath.ToSlash(filepath.Join(s.Root, "config.toml")) {
		t.Errorf("daemon FERRYMAN_CONFIG 异常: %q", dEnv["FERRYMAN_CONFIG"])
	}
	if dEnv["FERRYMAN_DATA"] != filepath.ToSlash(s.Data) {
		t.Errorf("daemon FERRYMAN_DATA 异常: %q", dEnv["FERRYMAN_DATA"])
	}
	if dEnv["FERRYMAN_PORT"] != fmt.Sprint(s.Ports.Daemon) {
		t.Errorf("daemon FERRYMAN_PORT 异常: %q", dEnv["FERRYMAN_PORT"])
	}
	for _, banned := range []string{"DSH_HOME", "CODEX_HOME"} {
		if _, ok := dEnv[banned]; ok {
			t.Errorf("daemon env 应 -u %s", banned)
		}
	}
	if !containsArg(web.Args, "web") || !containsArg(web.Args, "--no-open") ||
		!containsArgPrefix(web.Args, "--port", fmt.Sprint(s.Ports.Web)) {
		t.Errorf("web 参数形状异常: %v", web.Args)
	}
	wEnv := envMap(web.Env)
	if wEnv["DSH_HOME"] != filepath.ToSlash(s.Home) {
		t.Errorf("web DSH_HOME 未钉沙箱: %q", wEnv["DSH_HOME"])
	}
	if wEnv["FERRYMAN_PORT"] != fmt.Sprint(s.Ports.Daemon) {
		t.Errorf("web FERRYMAN_PORT 异常: %q", wEnv["FERRYMAN_PORT"])
	}
	if _, ok := wEnv["FERRYMAN_DISABLE"]; ok {
		t.Errorf("web env 应 -u FERRYMAN_DISABLE")
	}
	if !strings.HasSuffix(filepath.ToSlash(wEnv["FERRYMAN_TOKEN_FILE"]), "ferryman-data/daemon.token") {
		t.Errorf("web FERRYMAN_TOKEN_FILE 异常: %q", wEnv["FERRYMAN_TOKEN_FILE"])
	}

	// ---- junction 备料断言（镜像生产安装形态） ----
	link := filepath.Join(s.Home, "profiles", "web", "ferryman-dsh")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("插件位应为 junction: %v", err)
	}
	resolved := resolveLinkTarget(filepath.Dir(link), target)
	if !strings.EqualFold(filepath.Clean(resolved), filepath.Clean(s.PluginSnap)) {
		t.Errorf("插件 junction 应指备料快照: %s → %s（want %s）", link, resolved, s.PluginSnap)
	}
	nm := filepath.Join(s.Home, "profiles", "web", "node_modules", "ferryman-dsh")
	if raw, err := os.ReadFile(filepath.Join(nm, "package.json")); err != nil ||
		!strings.Contains(string(raw), "9.9.9-test") {
		t.Errorf("node_modules 插件拷贝缺失/版本不符: %v", err)
	}
	// 旧插件零渗入：生产同形 stale（junction 位换装＋node_modules 覆写）后，
	// 沙箱 home 内不得再出现 stale 版本串。
	if walkContains(s.Home, "0.0.1-stale") {
		t.Errorf("旧插件内容渗入沙箱（0.0.1-stale 残留）")
	}
	// junction 群重链不 deref（仅 Windows——夹具 junction 只在 Windows 造）：
	// 沙箱侧 dsh-hypatia 应是链接、目标仍指 app 资源夹具（无实体拷贝渗入）。
	if runtime.GOOS == "windows" {
		stagedHyp := filepath.Join(s.Home, "profiles", "web", "node_modules", "dsh-hypatia")
		st, err := os.Readlink(stagedHyp)
		if err != nil {
			t.Fatalf("junction 群应重链（dsh-hypatia）: %v", err)
		}
		if got := resolveLinkTarget(filepath.Dir(stagedHyp), st); !strings.EqualFold(
			filepath.Clean(got), filepath.Clean(appPkgDir)) {
			t.Errorf("junction 群重链目标漂移: %s", got)
		}
	}
	// 收尾卫生面：sessions 空壳（不带生产残留）、bak/remote-link 不带。
	if raw, err := os.ReadFile(filepath.Join(s.Home, "sessions", "old", "leftover.jsonl")); err == nil {
		t.Errorf("生产 sessions 残留渗入沙箱: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(s.Home, "remote-link")); !os.IsNotExist(err) {
		t.Errorf("remote-link 应剔除")
	}
	if _, err := os.Stat(filepath.Join(s.Home, "cordis.patch.yml.bak-ferryman-x")); !os.IsNotExist(err) {
		t.Errorf("bak 残留应剔除")
	}

	// ---- 生成面断言 ----
	homePatch, _ := os.ReadFile(filepath.Join(s.Home, "cordis.patch.yml"))
	if strings.Contains(string(homePatch), "15722") {
		t.Errorf("home patch 仍含生产渡口口 15722")
	}
	if !strings.Contains(string(homePatch), fmt.Sprintf("127.0.0.1:%d", s.Ports.Dock)) {
		t.Errorf("home patch 未改指沙箱渡口 %d", s.Ports.Dock)
	}
	profPatch, _ := os.ReadFile(filepath.Join(s.Home, "profiles", "web", "cordis.patch.yml"))
	for _, mark := range []string{"id: ferryman-dsh", "id: compaction-profile",
		"compression: none", "dsh-compaction-basic"} {
		if !strings.Contains(string(profPatch), mark) {
			t.Errorf("沙箱 profile patch 缺 %s", mark)
		}
	}
	if strings.Contains(string(profPatch), "3081") {
		t.Errorf("profile patch 仍含 ios-control 生产口")
	}
	pkg, _ := os.ReadFile(filepath.Join(s.Home, "profiles", "web", "package.json"))
	if strings.Contains(string(pkg), "dsh-ios-control") {
		t.Errorf("package.json 应摘 dsh-ios-control")
	}
	if !strings.Contains(string(pkg), "ferryman-dsh") {
		t.Errorf("package.json bundles 应保留 ferryman-dsh")
	}
	cfg, _ := os.ReadFile(filepath.Join(s.Root, "config.toml"))
	for _, mark := range []string{
		fmt.Sprintf("port = %d", s.Ports.Daemon),
		fmt.Sprintf("\"127.0.0.1:%d\"", s.Ports.Dock),
		"active = \"cheap\"", "[dsh_compact]", "[heartbeat]", "ttl_s = 60",
	} {
		if !strings.Contains(string(cfg), mark) {
			t.Errorf("config 缺 %q", mark)
		}
	}
	if strings.Contains(string(cfg), "sk-test-") {
		t.Errorf("api_key 序列化异常")
	}
	// 生成 config 必须可被真解析器吃下（真机实锚回归：上游名「智谱」曾以裸键
	// 落盘直接炸 TOML——本断言钉住生成面与解析器的一致性）。
	parsed, err := config.Load(filepath.Join(s.Root, "config.toml"), true)
	if err != nil {
		t.Fatalf("生成的 config 不可解析: %v", err)
	}
	if name, up := parsed.Dock.ActiveUpstream(); up == nil || name != "cheap" ||
		up.BaseURL != "https://api.example.test/v1" {
		t.Errorf("渡口 active 上游回读异常: %s %+v", name, up)
	}
	if parsed.DshCompact.MinPeakTokens != 0 || parsed.Heartbeat.TTLS != 60 {
		t.Errorf("压缩链参数回读异常: %+v", parsed.DshCompact)
	}
	// 四口全 25xxx。
	for _, p := range []int{s.Ports.Daemon, s.Ports.Panel, s.Ports.Web, s.Ports.Dock} {
		if p < 25000 || p > 25999 {
			t.Errorf("端口 %d 越出 25xxx 段", p)
		}
	}

	// ---- web 就绪 token 捕获 ----
	if s.WebToken == "" {
		t.Errorf("web 登录 token 未捕获")
	}

	// ---- 收尾：进程全停＋目录清理＋锁释放 ----
	fs.mu.Lock()
	procs := append([]*fakeProc(nil), fs.procs...)
	fs.mu.Unlock()
	s.Stop()
	for i, fp := range procs {
		if !fp.killed {
			t.Errorf("进程 %d 未被杀树（进程全停断言）", i)
		}
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("沙箱根应清理: %s（err=%v）", root, err)
	}
	if _, err := os.Stat(filepath.Join(s.Opt.TempRoot,
		fmt.Sprintf("ferryman-verify-dsh-%d.lock", s.Ports.Daemon))); !os.IsNotExist(err) {
		t.Errorf("锁文件应释放")
	}
	// 幂等。
	s.Stop()
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func containsArgPrefix(args []string, flag, val string) bool {
	for i, a := range args {
		if a == flag && i+1 < len(args) && args[i+1] == val {
			return true
		}
	}
	return false
}

// TestStartKeepRoot 红错现场保留（KeepRoot＝收尾不清目录）。
func TestStartKeepRoot(t *testing.T) {
	homeSrc, pluginSrc, _, _ := writeFixture(t)
	fs := &fakeSpawner{}
	origSpawn := spawnProc
	spawnProc = fs.spawn
	t.Cleanup(func() { spawnProc = origSpawn })
	o := fixtureOptions(t, fs, homeSrc, pluginSrc)
	o.KeepRoot = true
	s, err := Start(o)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	root := s.Root
	s.Stop()
	if _, err := os.Stat(root); err != nil {
		t.Errorf("KeepRoot 下沙箱根应保留: %v", err)
	}
	_ = os.RemoveAll(root)
}

// TestStartErrNoDSH DSH 缺位＝ErrNoDSH 哨兵（「环境缺 DSH」跳过不算绿的面）。
func TestStartErrNoDSH(t *testing.T) {
	homeSrc, pluginSrc, _, _ := writeFixture(t)
	o := fixtureOptions(t, &fakeSpawner{}, homeSrc, pluginSrc)
	o.DSHCLI = filepath.Join(t.TempDir(), "nope-dsh.cmd")
	if _, err := Start(o); err == nil || !strings.Contains(err.Error(), "环境缺 DSH") {
		t.Errorf("DSH 缺位应回 ErrNoDSH 哨兵: %v", err)
	}
}

// TestStartFailTeardownsLock 起栈失败（假 daemon 起不来——端口被占）→ 锁
// 释放＋目录清理（不留半状态）。
func TestStartFailTeardownsLock(t *testing.T) {
	homeSrc, pluginSrc, _, _ := writeFixture(t)
	// 预占 25900-25909 全段（真监听）——portFree 全 false → acquirePortLock 失败。
	var listeners []net.Listener
	for p := 25900; p <= 25909; p++ {
		if ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)); err == nil {
			listeners = append(listeners, ln)
		}
	}
	t.Cleanup(func() {
		for _, ln := range listeners {
			_ = ln.Close()
		}
	})
	o := fixtureOptions(t, &fakeSpawner{}, homeSrc, pluginSrc)
	_, err := Start(o)
	if err == nil || !strings.Contains(err.Error(), "端口段全被占") {
		t.Fatalf("全段被占应拒跑: %v", err)
	}
	// 候选耗尽时无锁可泄；单段占用的换段行为在 TestPortLockShift 钉。
}

// ---- 端口锁单测 ----

func TestPortLockShift(t *testing.T) {
	tmp := t.TempDir()
	// 预占 25900（模拟 e2e 在跑）——探测面失败即换段。
	ln, err := net.Listen("tcp", "127.0.0.1:25900")
	if err != nil {
		t.Skip("25900 不可占用（环境受限）——跳过换段断言")
	}
	defer ln.Close()
	l1, err := acquirePortLock(tmp, portCandidates)
	if err != nil {
		t.Fatalf("应换到 25904 段: %v", err)
	}
	if l1.base != 25904 {
		t.Errorf("换段基址=%d want 25904", l1.base)
	}
	// 同段锁被持（不占端口、只占锁）→ 再换 25905。
	if f, err := os.OpenFile(filepath.Join(tmp, "ferryman-verify-dsh-25905.lock"),
		os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644); err == nil {
		_ = f.Close()
		l2, err := acquirePortLock(tmp, portCandidates)
		if err != nil {
			t.Fatalf("应换到 25906 段: %v", err)
		}
		if l2.base != 25906 {
			t.Errorf("锁被持时段基址=%d want 25906", l2.base)
		}
		l2.release()
	}
	l1.release()
	// 释放后可复得。
	l3, err := acquirePortLock(tmp, portCandidates)
	if err != nil {
		t.Fatalf("释放后应复得: %v", err)
	}
	l3.release()
}

// TestPortLockExclusive 并发独占：同基址二次创建必败（O_EXCL 语义）。
func TestPortLockExclusive(t *testing.T) {
	tmp := t.TempDir()
	l1, err := acquirePortLock(tmp, []int{25900})
	if err != nil {
		t.Skipf("25900 不可得: %v", err)
	}
	defer l1.release()
	if _, err := acquirePortLock(tmp, []int{25900}); err == nil {
		t.Errorf("同基址应互斥")
	}
}

// ---- 隔离扫描单测 ----

// TestIsolationScanRejects 判生成面残迹即拒（生产端口五行任一）。
func TestIsolationScanRejects(t *testing.T) {
	for _, mark := range prodPortMarks {
		s := &Stack{Root: t.TempDir(), Home: t.TempDir()}
		writeFile(t, filepath.Join(s.Home, "cordis.patch.yml"),
			"- id: llm-deepseek\n  config:\n    baseURL: http://127.0.0.1:"+mark+"\n")
		writeFile(t, filepath.Join(s.Root, "config.toml"), "[server]\nport = 25900\n")
		writeFile(t, filepath.Join(s.Home, "profiles", "web", "cordis.patch.yml"), "- insert: []\n")
		writeFile(t, filepath.Join(s.Home, "profiles", "web", "package.json"), "{}\n")
		if err := isolationScan(s); err == nil || !strings.Contains(err.Error(), mark) {
			t.Errorf("残迹 %s 应拒跑: %v", mark, err)
		}
	}
}

// ---- 小面单测 ----

// TestParseWebToken 就绪行 token 捕获（端口对上才认——防串口）。
func TestParseWebToken(t *testing.T) {
	if tok, err := parseWebToken("25902/?token=abc123", 25902); err != nil || tok != "abc123" {
		t.Errorf("就绪行解析: %q %v", tok, err)
	}
	if _, err := parseWebToken("25903/?token=abc", 25902); err == nil {
		t.Errorf("端口不符应拒")
	}
	if _, err := parseWebToken("25902/nolayout", 25902); err == nil {
		t.Errorf("无 token 应拒")
	}
}

// TestSandboxEnv env 钉值/剥离（FERRYMAN_* 全清＋DSH_HOME 清＋unset 清单）。
func TestSandboxEnv(t *testing.T) {
	t.Setenv("FERRYMAN_PORT", "15700")      // 生产渗入面
	t.Setenv("DSH_HOME", "C:/prod/.dsh")    //
	t.Setenv("CODEX_HOME", "C:/prod/codex") // daemon -u 面
	t.Setenv("SOME_AMBIENT", "keepme")
	env := sandboxEnv(map[string]string{"FERRYMAN_PORT": "25900",
		"FERRYMAN_CONFIG": `C:\sandbox\config.toml`}, "CODEX_HOME", "DSH_HOME")
	m := envMap(env)
	if m["FERRYMAN_PORT"] != "25900" {
		t.Errorf("FERRYMAN_PORT 钉值异常: %q", m["FERRYMAN_PORT"])
	}
	if m["FERRYMAN_CONFIG"] != "C:/sandbox/config.toml" {
		t.Errorf("路径应转正斜杠: %q", m["FERRYMAN_CONFIG"])
	}
	for _, banned := range []string{"DSH_HOME", "CODEX_HOME"} {
		if _, ok := m[banned]; ok {
			t.Errorf("应剥离 %s", banned)
		}
	}
	if m["SOME_AMBIENT"] != "keepme" {
		t.Errorf("无关环境应保留")
	}
}

// TestHiddenCmdHideWindow 进程创建单点的 HideWindow 在位（Windows 分支——
// 零闪窗铁律的机器可验面；非 Windows 无该字段跳过）。
func TestHiddenCmdHideWindow(t *testing.T) {
	cmd := hiddenCmd(procSpec{Path: "cmd", Args: []string{"/c", "exit"}})
	f := reflect.ValueOf(cmd.SysProcAttr).Elem().FieldByName("HideWindow")
	if !f.IsValid() {
		if runtime.GOOS == "windows" {
			t.Fatalf("Windows 侧 SysProcAttr 应有 HideWindow 字段")
		}
		return
	}
	if !f.Bool() {
		t.Errorf("HideWindow 未置位（零闪窗铁律）")
	}
}

// walkContains 目录树内是否有文件包含目标串（链接不跟——防越出沙箱根）。
func walkContains(root, needle string) bool {
	found := false
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || found || d.IsDir() || d.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return nil
		}
		if raw, rerr := os.ReadFile(p); rerr == nil && strings.Contains(string(raw), needle) {
			found = true
		}
		return nil
	})
	return found
}
