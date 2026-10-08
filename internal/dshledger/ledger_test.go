// ledger_test.go — 票03 验收钉子（spec「契约锚与已知良好档案」节）。
//
// 覆盖面：
//   - 判定流水：Append/List/RecentKnownGood 往返、无 green 返回空、未知判定
//     响亮拒绝、重开（重新 New）持久、落盘 JSONL 形状（snake_case 键）；
//   - 契约锚：Write/Latest 往返、滚动保留（默认 N=10 第 11 份淘汰最旧＋可配
//     N）、四面结构校验（缺面/多面拒绝）、无锚时 Latest 空；
//   - DiffFaces：改一个 manifest 字段返回对应面、无漂移返回空、缺面计漂移。
//
// 时钟：clock.Now 包级可注入（仓库惯例），stepClock 冻结并逐次步进——滚动
// 测试的先后次序由此确定性成立。
package dshledger

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"ferryman/internal/clock"
)

// stepClock 冻结时钟并逐次步进（每次调用 +step 秒）；测试毕恢复原值。
func stepClock(t *testing.T, start, step float64) {
	t.Helper()
	orig := clock.Now
	cur := start
	clock.Now = func() float64 { c := cur; cur += step; return c }
	t.Cleanup(func() { clock.Now = orig })
}

// fourFaces 四面夹具：每面一个最小形状代表（票05 采集前的占位形——发现面给
// manifest 字段集，事件面给事件位清单，鸭子面给服务接口名，浏览器半面给槽位
// 字段集）。main 参数用于构造漂移（改一个 manifest 字段）。
func fourFaces(main string) map[string]any {
	return map[string]any{
		FaceDiscovery: map[string]any{"main": main, "exports": map[string]any{".": "./index.js"}},
		FaceEvents:    []any{"turn/start", "turn/complete"},
		FaceDuck:      map[string]any{"services": []any{"dock", "gate"}},
		FaceBrowser:   map[string]any{"dock_slot": float64(1)},
	}
}

// anchorFileCount 统计锚目录内按命名规约落下的锚文件数（*.json）。
func anchorFileCount(t *testing.T, s *AnchorStore) int {
	t.Helper()
	des, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, de := range des {
		if !de.IsDir() && strings.HasSuffix(de.Name(), ".json") {
			n++
		}
	}
	return n
}

// dirContainsHash 锚目录内是否存在含该哈希的锚文件。
func dirContainsHash(t *testing.T, s *AnchorStore, hash string) bool {
	t.Helper()
	des, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, de := range des {
		if strings.Contains(de.Name(), hash) {
			return true
		}
	}
	return false
}

func TestAppendListRoundtrip(t *testing.T) {
	stepClock(t, 1.7e9, 1)
	dir := t.TempDir()
	v, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	e1, err := v.Append("dsh-1", "plugin-1", "daemon-1", VerdictGreen, "anchorhash1", `C:\备份\dsh-1.exe`)
	if err != nil {
		t.Fatal(err)
	}
	if e1.TS != 1.7e9 {
		t.Fatalf("e1.TS = %v, want 1.7e9（clock 盖章）", e1.TS)
	}
	if e1.TSISO == "" {
		t.Fatal("e1.TSISO 不应为空")
	}
	e2, err := v.Append("dsh-2", "plugin-2", "daemon-2", VerdictYellow, "", "")
	if err != nil {
		t.Fatal(err)
	}
	e3, err := v.Append("dsh-3", "plugin-3", "daemon-3", VerdictRed, "", "")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := v.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("List len = %d, want 3", len(rows))
	}
	if rows[0] != e1 || rows[1] != e2 || rows[2] != e3 {
		t.Fatalf("往返不一致: %+v vs [%+v %+v %+v]", rows, e1, e2, e3)
	}
	// 落盘形状：JSONL 三行、snake_case 键、可选 installer_path 只在 green 行
	data, err := os.ReadFile(filepath.Join(dir, "dshledger", "verdicts.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if n := strings.Count(strings.TrimRight(text, "\n"), "\n") + 1; n != 3 {
		t.Fatalf("落盘行数 = %d, want 3", n)
	}
	for _, key := range []string{`"dsh_version":"dsh-1"`, `"plugin_version":"plugin-1"`,
		`"daemon_version":"daemon-1"`, `"verdict":"green"`, `"anchor_hash":"anchorhash1"`,
		`"installer_path":"C:\\备份\\dsh-1.exe"`} {
		if !strings.Contains(text, key) {
			t.Fatalf("落盘缺键 %s\n全文:\n%s", key, text)
		}
	}
	if strings.Contains(text, "installer_path") && strings.Count(text, "installer_path") != 1 {
		t.Fatalf("installer_path 应只在 green 行出现一次:\n%s", text)
	}
	// 重开（重新 New）持久
	v2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	rows2, err := v2.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows2) != 3 || rows2[0] != e1 {
		t.Fatalf("重开后 List = %d 行, 首 %+v", len(rows2), rows2[0])
	}
}

func TestRecentKnownGood(t *testing.T) {
	stepClock(t, 1.7e9, 1)
	v, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// 无流水 → 空
	got, err := v.RecentKnownGood()
	if err != nil || got != nil {
		t.Fatalf("无流水 RecentKnownGood = %+v, %v; want nil, nil", got, err)
	}
	// 有黄有红、无 green → 空（验收钉子）
	if _, err := v.Append("dsh-0", "p", "d", VerdictYellow, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Append("dsh-0", "p", "d", VerdictRed, "", ""); err != nil {
		t.Fatal(err)
	}
	got, err = v.RecentKnownGood()
	if err != nil || got != nil {
		t.Fatalf("无 green RecentKnownGood = %+v, %v; want nil, nil", got, err)
	}
	// 首个 green → 指针立起（三元组＋锚哈希齐）
	if _, err := v.Append("dsh-1", "p1", "d1", VerdictGreen, "hash1", ""); err != nil {
		t.Fatal(err)
	}
	got, err = v.RecentKnownGood()
	if err != nil || got == nil {
		t.Fatalf("有 green 后 RecentKnownGood = %+v, %v; want 非 nil", got, err)
	}
	if got.DSHVersion != "dsh-1" || got.PluginVersion != "p1" || got.DaemonVersion != "d1" ||
		got.AnchorHash != "hash1" || got.Verdict != VerdictGreen {
		t.Fatalf("已知良好三元组不符: %+v", got)
	}
	// 后续黄不抬指针
	if _, err := v.Append("dsh-2", "p2", "d2", VerdictYellow, "", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ = v.RecentKnownGood(); got == nil || got.DSHVersion != "dsh-1" {
		t.Fatalf("黄不应抬指针: %+v", got)
	}
	// 更新 green → 指针移到最近全绿
	if _, err := v.Append("dsh-3", "p3", "d3", VerdictGreen, "hash3", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ = v.RecentKnownGood(); got == nil || got.DSHVersion != "dsh-3" {
		t.Fatalf("指针应移到最近全绿 dsh-3: %+v", got)
	}
}

func TestAppendRejectsUnknownVerdict(t *testing.T) {
	v, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Append("dsh-1", "p", "d", Verdict("blue"), "", ""); err == nil {
		t.Fatal("未知判定应响亮拒绝")
	}
	rows, err := v.List()
	if err != nil || len(rows) != 0 {
		t.Fatalf("被拒行不应落盘: %d 行, %v", len(rows), err)
	}
}

func TestAnchorWriteLatestRoundtrip(t *testing.T) {
	stepClock(t, 1.7e9, 1)
	s, err := NewAnchorStore(t.TempDir(), 0) // 0 → 默认 N=10
	if err != nil {
		t.Fatal(err)
	}
	hash, err := s.Write("dsh-9", fourFaces("y.js"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hash) != 64 {
		t.Fatalf("锚哈希应为 sha256 hex 64 位, got %d", len(hash))
	}
	a, gotHash, err := s.Latest()
	if err != nil || a == nil {
		t.Fatalf("Latest = %+v, %v; want 非 nil", a, err)
	}
	if gotHash != hash {
		t.Fatalf("Latest 哈希 %s != Write 返回 %s", gotHash, hash)
	}
	if a.DSHVersion != "dsh-9" {
		t.Fatalf("锚应绑 DSH 版本: %+v", a.DSHVersion)
	}
	// 往返等价：DiffFaces 对同形状应零漂移（归一比较即等价判定）＋抽查一值
	if drift := DiffFaces(a, fourFaces("y.js")); len(drift) != 0 {
		t.Fatalf("同形状往返应零漂移: %+v", drift)
	}
	if !reflect.DeepEqual(a.Faces[FaceBrowser], map[string]any{"dock_slot": float64(1)}) {
		t.Fatalf("浏览器半面往返不符: %+v", a.Faces[FaceBrowser])
	}
	// 无锚目录 → Latest 空
	s2, err := NewAnchorStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	a2, h2, err := s2.Latest()
	if err != nil || a2 != nil || h2 != "" {
		t.Fatalf("无锚 Latest = %+v, %q, %v; want nil, \"\", nil", a2, h2, err)
	}
}

func TestAnchorRollingDefaultKeepsTen(t *testing.T) {
	stepClock(t, 1.7e9, 1) // 每次 Write 步进 1s：文件名毫秒戳与内容 ts 同序
	s, err := NewAnchorStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	hashes := make([]string, 11)
	for i := range hashes {
		h, err := s.Write(fmt.Sprintf("dsh-%02d", i), fourFaces(fmt.Sprintf("v%02d.js", i)))
		if err != nil {
			t.Fatal(err)
		}
		hashes[i] = h
	}
	// 第 11 份写入后：最旧（第 1 份）被淘汰，余 10
	if n := anchorFileCount(t, s); n != 10 {
		t.Fatalf("滚动后锚文件数 = %d, want 10", n)
	}
	if dirContainsHash(t, s, hashes[0]) {
		t.Fatal("最旧锚（第 1 份）应被淘汰")
	}
	if !dirContainsHash(t, s, hashes[10]) {
		t.Fatal("最新锚（第 11 份）应在盘")
	}
	a, gotHash, err := s.Latest()
	if err != nil || a == nil || gotHash != hashes[10] || a.DSHVersion != "dsh-10" {
		t.Fatalf("Latest 应为第 11 份: %+v, %q, %v", a, gotHash, err)
	}
}

func TestAnchorRollingConfigurableN(t *testing.T) {
	stepClock(t, 1.7e9, 1)
	s, err := NewAnchorStore(t.TempDir(), 3)
	if err != nil {
		t.Fatal(err)
	}
	hashes := make([]string, 4)
	for i := range hashes {
		h, err := s.Write(fmt.Sprintf("dsh-%d", i), fourFaces(fmt.Sprintf("v%d.js", i)))
		if err != nil {
			t.Fatal(err)
		}
		hashes[i] = h
	}
	if n := anchorFileCount(t, s); n != 3 {
		t.Fatalf("keepN=3 滚动后 = %d, want 3", n)
	}
	if dirContainsHash(t, s, hashes[0]) {
		t.Fatal("keepN=3: 第 1 份应被淘汰")
	}
	if a, gotHash, _ := s.Latest(); a == nil || gotHash != hashes[3] {
		t.Fatalf("keepN=3: Latest 应为第 4 份: %+v, %q", a, gotHash)
	}
}

func TestAnchorWriteRejectsBadFaceSet(t *testing.T) {
	s, err := NewAnchorStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	// 缺面
	short := fourFaces("x.js")
	delete(short, FaceDuck)
	if _, err := s.Write("dsh-1", short); err == nil {
		t.Fatal("缺面应响亮拒绝")
	}
	// 多面（面名拼错）
	bad := fourFaces("x.js")
	bad["discover"] = map[string]any{} // 拼错的面名
	if _, err := s.Write("dsh-1", bad); err == nil {
		t.Fatal("多面应响亮拒绝")
	}
	if n := anchorFileCount(t, s); n != 0 {
		t.Fatalf("被拒锚不应落盘: %d", n)
	}
}

func TestDiffFaces(t *testing.T) {
	old := &Anchor{DSHVersion: "dsh-1", Faces: fourFaces("y.js")}
	// 无漂移 → 空（验收钉子）
	if got := DiffFaces(old, fourFaces("y.js")); len(got) != 0 {
		t.Fatalf("无漂移应为空: %+v", got)
	}
	// 改一个 manifest 字段 → 返回对应面（发现面）
	got := DiffFaces(old, fourFaces("z.js"))
	if len(got) != 1 {
		t.Fatalf("单字段漂移应只报一个面: %+v", got)
	}
	if got[0].Face != FaceDiscovery {
		t.Fatalf("漂移面 = %s, want %s", got[0].Face, FaceDiscovery)
	}
	if got[0].Old == nil || got[0].New == nil {
		t.Fatalf("漂移面应带旧/新形状供诊断: %+v", got[0])
	}
	// 新采集缺面 → 该面漂移
	missing := fourFaces("y.js")
	delete(missing, FaceEvents)
	got = DiffFaces(old, missing)
	if len(got) != 1 || got[0].Face != FaceEvents {
		t.Fatalf("缺面应计该面漂移: %+v", got)
	}
	// 漂移面定序 = 四面点名序（多面漂移可预期）
	changed := fourFaces("z.js")
	delete(changed, FaceBrowser)
	got = DiffFaces(old, changed)
	if len(got) != 2 || got[0].Face != FaceDiscovery || got[1].Face != FaceBrowser {
		t.Fatalf("多面漂移应按面定序: %+v", got)
	}
	// 旧锚为 nil（无历史锚）→ 空，不伪造漂移
	if got := DiffFaces(nil, fourFaces("y.js")); len(got) != 0 {
		t.Fatalf("nil 旧锚应返回空: %+v", got)
	}
}

// TestAnchorFilenameStampUsesClock 文件名毫秒戳取 clock 口径（与内容 ts 同源，
// 测试冻结钟即得确定名）；顺带钉住命名规约 <stamp>_<哈希>.json。
func TestAnchorFilenameStampUsesClock(t *testing.T) {
	stepClock(t, 1.7e9, 0.001) // 步进 1ms
	s, err := NewAnchorStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	h1, err := s.Write("dsh-1", fourFaces("a.js"))
	if err != nil {
		t.Fatal(err)
	}
	h2, err := s.Write("dsh-2", fourFaces("b.js"))
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatal("两份不同形状的锚哈希不应相同")
	}
	want := time.UnixMilli(int64((1.7e9 + 0.001) * 1000)).Local().Format("20060102_150405.000")
	des, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, de := range des {
		if strings.HasPrefix(de.Name(), want+"_") && strings.Contains(de.Name(), h2) {
			found = true
		}
	}
	if !found {
		t.Fatalf("未找到命名规约文件 %s_<%s>.json", want, h2)
	}
}
