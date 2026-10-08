// anchor_collect_test.go — 契约锚采集单测：四面恰含 dshledger.Face* 键（Write
// 的结构校验面）、静态形状对源漂移敏感、动态观察面如实回填。
package dshsandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ferryman/internal/dshledger"
)

// stagedStackForFaces 造一个已备料的 Stack（不拉进程——锚采集只读沙箱形状）。
func stagedStackForFaces(t *testing.T) (*Stack, func()) {
	t.Helper()
	homeSrc, pluginSrc, _, _ := writeFixture(t)
	o := fixtureOptions(t, &fakeSpawner{}, homeSrc, pluginSrc)
	o.TempRoot = t.TempDir()
	// 直接走备料（不经 Start——不起进程；端口锁手工持有形态简化为 25900 段）。
	s := &Stack{Opt: o, Ports: portSet{Daemon: 25900, Panel: 25901, Web: 25902, Dock: 25903},
		Root: filepath.Join(o.TempRoot, "sbx"), lock: &portLock{base: 25900,
			path: filepath.Join(o.TempRoot, "ferryman-verify-dsh-25900.lock")}}
	s.Home = filepath.Join(s.Root, "dsh-home")
	s.Data = filepath.Join(s.Root, "ferryman-data")
	s.Logs = filepath.Join(s.Root, "logs")
	s.Workspace = filepath.Join(s.Root, "probe-workspace")
	s.PluginSnap = filepath.Join(s.Root, "plugin", "ferryman-dsh")
	if err := stageHome(s, homeSrc, pluginSrc); err != nil {
		t.Fatalf("备料: %v", err)
	}
	if err := writeSandboxConfig(s); err != nil {
		t.Fatalf("config: %v", err)
	}
	_ = os.WriteFile(s.lock.path, []byte("0\n0\n"), 0o644)
	return s, func() { _ = os.RemoveAll(s.Root); _ = os.Remove(s.lock.path) }
}

// TestCollectFacesShape 四面恰含 Face* 键、各面静态形状在位。
func TestCollectFacesShape(t *testing.T) {
	s, cleanup := stagedStackForFaces(t)
	defer cleanup()
	tf := TraceFacts{GateDelta: 1, UsageRows: 1, SID: "session-01234567-89ab-cdef-0123-456789abcdef"}
	obs := &probeObs{sidA: "session-01234567-89ab-cdef-0123-456789abcdef"}
	faces := collectFaces(s, tf, greenCmdLane(), greenSvcLane(), obs)
	for _, key := range []string{dshledger.FaceDiscovery, dshledger.FaceEvents,
		dshledger.FaceDuck, dshledger.FaceBrowser} {
		if _, ok := faces[key]; !ok {
			t.Errorf("缺契约面 %s", key)
		}
	}
	if len(faces) != 4 {
		t.Errorf("面数=%d want 4（Write 结构校验要求恰含四面）", len(faces))
	}
	// 发现面：manifest/patch/junction/node_modules 四路形状。
	disc, _ := faces[dshledger.FaceDiscovery].(map[string]any)
	if disc["plugin_version"] != "9.9.9-test" {
		t.Errorf("plugin_version=%v", disc["plugin_version"])
	}
	if disc["main"] != "src/index.ts" {
		t.Errorf("main=%v", disc["main"])
	}
	if ins, _ := disc["patch_inserts"].(map[string]bool); ins == nil ||
		!ins["ferryman-dsh"] || !ins["compaction-profile"] || !ins["session-persistence-jsonl"] {
		t.Errorf("patch_inserts 异常: %v", disc["patch_inserts"])
	}
	if v, _ := disc["junction_resolves_snapshot"].(bool); !v {
		t.Errorf("junction 应解析到备料快照")
	}
	if v, _ := disc["node_modules_copy"].(bool); !v {
		t.Errorf("node_modules 拷贝应标记在位")
	}
	if sha, _ := disc["snapshot_shape_sha256"].(string); len(sha) != 64 {
		t.Errorf("快照指纹形状异常: %q", sha)
	}
	// 事件面：五事件位静态清单＋动态观察。
	ev, _ := faces[dshledger.FaceEvents].(map[string]any)
	reg, _ := ev["plugin_registered_events"].([]string)
	want := []string{"agent/created", "agent/disposed", "agent/pre-step", "agent/status", "session/event"}
	if len(reg) != len(want) {
		t.Fatalf("注册事件位=%v", reg)
	}
	for i := range want {
		if reg[i] != want[i] {
			t.Errorf("注册事件位[%d]=%s want %s", i, reg[i], want[i])
		}
	}
	// 鸭子面：两道执行痕回填。
	duck, _ := faces[dshledger.FaceDuck].(map[string]any)
	if v, _ := duck["commands_execute_resolved"].(bool); !v {
		t.Errorf("命令道执行痕应回填")
	}
	if v, _ := duck["compaction_compactnow_reachable"].(bool); !v {
		t.Errorf("服务面执行痕应回填")
	}
	// 浏览器半面：静态形状（dock 槽/RPC 名空间/模块加载形态）。
	br, _ := faces[dshledger.FaceBrowser].(map[string]any)
	if br["dock_slot"] != "conversation.composer.dock" {
		t.Errorf("dock_slot=%v", br["dock_slot"])
	}
	if br["rpc_namespace"] != "ferrymanBlocked" {
		t.Errorf("rpc_namespace=%v", br["rpc_namespace"])
	}
	if v, _ := br["module_loader_form"].(bool); !v {
		t.Errorf("模块加载形态应命中")
	}
	if v, _ := br["dynamic_observable_headless"].(bool); v {
		t.Errorf("无头探针应如实记动态面缺席")
	}
}

// TestCollectFacesDriftSensitive 静态形状对插件源漂移敏感（改一个字节→指纹变）。
func TestCollectFacesDriftSensitive(t *testing.T) {
	s, cleanup := stagedStackForFaces(t)
	defer cleanup()
	tf := TraceFacts{}
	f1 := collectFaces(s, tf, LaneFact{}, LaneFact{}, &probeObs{})
	// 漂移：快照里 compact.ts 改内容。
	if err := os.WriteFile(filepath.Join(s.PluginSnap, "src", "compact.ts"),
		[]byte("export const DEFAULT_POLL_INTERVAL_MS = 10000;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f2 := collectFaces(s, tf, LaneFact{}, LaneFact{}, &probeObs{})
	h1 := f1[dshledger.FaceDiscovery].(map[string]any)["snapshot_shape_sha256"]
	h2 := f2[dshledger.FaceDiscovery].(map[string]any)["snapshot_shape_sha256"]
	if h1 == h2 {
		t.Errorf("插件源漂移后快照指纹应变化（%v）", h1)
	}
	// 浏览器半面同理：client.js 改动→sha 变。
	b1 := f1[dshledger.FaceBrowser].(map[string]any)["client_js_sha256"]
	if err := os.WriteFile(filepath.Join(s.PluginSnap, "client.js"),
		[]byte("var NAMESPACE = 'ferrymanBlocked';\n// drifted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f3 := collectFaces(s, tf, LaneFact{}, LaneFact{}, &probeObs{})
	b3 := f3[dshledger.FaceBrowser].(map[string]any)["client_js_sha256"]
	if b1 == b3 {
		t.Errorf("client.js 漂移后 sha 应变化（%v）", b1)
	}
}

// TestTreeSHA256Stable 同内容两次指纹一致（内容寻址、与遍历随机序无关）。
func TestTreeSHA256Stable(t *testing.T) {
	s, cleanup := stagedStackForFaces(t)
	defer cleanup()
	a, b := treeSHA256(s.PluginSnap), treeSHA256(s.PluginSnap)
	if a != b {
		t.Errorf("同内容指纹不稳: %s vs %s", a, b)
	}
	if len(a) != 64 || strings.Count(a, "") < 64 {
		t.Errorf("指纹形态异常")
	}
}
