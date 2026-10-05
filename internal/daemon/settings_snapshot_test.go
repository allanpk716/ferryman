package daemon

// settings_snapshot_test.go — 设置视图票05：配置快照与还原验收钉子。
//
// 验收对照（票面）：
//   - 写路径（PUT 节）触发自动快照：文件存在且与写前磁盘逐字节一致；
//   - GET /settings/snapshots 响应 JSON 序列化后不含任何密钥明文（每份只含
//     id/时间/原因/字节数四字段）；无内容下载端点（红线反向断言）；
//   - restore 后 config.toml 与目标快照逐字节一致；还原前当前态已另存
//     pre-restore 快照；响应 needs_restart=true；
//   - 生成 25 份后只剩 20 份、最旧先删（mtime 逐份钉距，序断言确定性）；
//   - 守门：无/错 Bearer 401；未知 id/穿越 id/异形子路径 404；替身 404。
//
// 全部经 HTTP 面（httptest 真端口）+ 沙箱文件系统断言；不直呼实现内部符号，
// 红灯=行为性失败（端点 404/文件缺席）。环境复用 newSettingsEnv（同包编译面：
// FERRYMAN_CONFIG 指临时配置、data_dir 钉沙箱 → 快照目录也落沙箱）。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// ---- 小帮手 ----

// ssSnapDir 沙箱快照目录（= <data_dir>/backups/config，与实现解析同源——
// 夹具 [server].data_dir 钉死，两路必同值）。
func ssSnapDir(e *queryEnv) string {
	return filepath.Join(e.d.Cfg.DataDir(), "backups", "config")
}

// ssPost POST /settings/snapshots[/{id}/restore] 助手。
func ssPost(t *testing.T, e *queryEnv, path string) (int, []byte) {
	t.Helper()
	return swReqNF(http.MethodPost, e.port, e.token, path, nil)
}

// ssList GET /settings/snapshots 助手：200 时返回（原始字节, 解码 JSON）。
func ssList(t *testing.T, e *queryEnv) ([]byte, map[string]any) {
	t.Helper()
	code, raw := getRaw(t, e.port, "/settings/snapshots", e.token)
	if code != 200 {
		t.Fatalf("GET /settings/snapshots = %d %q", code, raw)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("响应非 JSON: %v (%q)", err, raw)
	}
	return raw, resp
}

// ssCreateManual 手动快照一枚，返回其 id。
func ssCreateManual(t *testing.T, e *queryEnv) string {
	t.Helper()
	code, raw := ssPost(t, e, "/settings/snapshots")
	if code != http.StatusOK {
		t.Fatalf("POST /settings/snapshots = %d %q, want 200", code, raw)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("响应非 JSON: %v (%q)", err, raw)
	}
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatalf("手动快照响应缺 id: %v", resp)
	}
	return id
}

// ssFiles 目录内现存快照文件名集合（解析不过的异形文件不计）。
func ssFiles(t *testing.T, dir string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out
		}
		t.Fatalf("读快照目录: %v", err)
	}
	for _, en := range entries {
		name := en.Name()
		if strings.HasPrefix(name, "config-") && strings.HasSuffix(name, ".toml") {
			out[name] = true
		}
	}
	return out
}

// ssStageMtime 把快照文件 mtime 钉到 base+i 秒（滚动序断言确定性——
// NTFS 裸 mtime 粒度不稳，逐份钉距）。base 取真实当前时刻减一小时，
// 保证在飞新文件的裸 mtime 恒新于已钉文件（滚动删除目标=最旧钉值）。
func ssStageMtime(t *testing.T, dir, id string, base time.Time, i int) {
	t.Helper()
	ts := base.Add(time.Duration(i) * time.Second)
	if err := os.Chtimes(filepath.Join(dir, id), ts, ts); err != nil {
		t.Fatalf("钉 mtime: %v", err)
	}
}

// ---- 验收①：写路径（PUT 节）触发自动快照 ----

// TestSnapshotAutoOnSettingsPut 真实现接线后（不换桩），PUT /settings/{节}
// 每次写前在 <data_dir>/backups/config/ 落一份 auto 快照，字节=写前磁盘态。
func TestSnapshotAutoOnSettingsPut(t *testing.T) {
	e, cfgPath := newSettingsEnv(t)
	dir := ssSnapDir(e)

	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if code, raw := swPut(t, e, "gate", map[string]any{"cc_mode": "enforce"}); code != http.StatusOK {
		t.Fatalf("PUT /settings/gate = %d %q, want 200", code, raw)
	}

	files := ssFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("首写后快照数 = %d, want 1（auto）: %v", len(files), files)
	}
	name := ""
	for n := range files {
		name = n
	}
	// 类=auto（文件名第三段）；字节=写前磁盘态逐字节一致（含密钥的整文件快照）。
	if !strings.HasPrefix(name, "config-") || !strings.HasSuffix(name, "-auto.toml") {
		t.Fatalf("快照名 = %q, want config-<YYYYMMDD-HHMMSS>-auto.toml", name)
	}
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(before) {
		t.Fatal("auto 快照与写前磁盘态不逐字节一致")
	}

	// 第二写再落一份（同秒撞名靠序号消歧，两份俱在）。
	if code, raw := swPut(t, e, "gate", map[string]any{"cc_mode": "observe"}); code != http.StatusOK {
		t.Fatalf("二写 PUT = %d %q, want 200", code, raw)
	}
	files = ssFiles(t, dir)
	if len(files) != 2 {
		t.Fatalf("两写后快照数 = %d, want 2: %v", len(files), files)
	}
}

// ---- 验收②：GET 仅元数据、无密钥明文、无内容下载端点 ----

// TestSnapshotListMetadataOnly GET /settings/snapshots 每份只含
// id/ts/reason/bytes 四字段；整响应字节不含夹具任一真钥；无内容下载端点
// （GET 快照内容一律 404）。
func TestSnapshotListMetadataOnly(t *testing.T) {
	e, cfgPath := newSettingsEnv(t)
	id := ssCreateManual(t, e)

	raw, resp := ssList(t, e)

	// 密钥明文红线：夹具四处真钥一个都不许出现在响应字节里。
	for _, secret := range []string{
		"sk-ferry-provider-key-8888", "sk-dock-legacy-key-7777",
		"sk-dock-upstream-key-6666", "pushover-token-abcdefgh",
	} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("GET 响应泄漏密钥明文 %q", secret)
		}
	}

	// 顶层恰 snapshots 一键；每份恰 id/ts/reason/bytes 四字段。
	if len(resp) != 1 {
		t.Fatalf("GET 顶层键 = %v, want 仅 snapshots", resp)
	}
	items, ok := resp["snapshots"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("snapshots = %v, want 恰 1 份", resp["snapshots"])
	}
	item, _ := items[0].(map[string]any)
	if item == nil {
		t.Fatalf("快照项非对象: %v", items[0])
	}
	if len(item) != 4 {
		t.Fatalf("快照项字段 = %v, want 恰 id/ts/reason/bytes 四键", item)
	}
	for _, k := range []string{"id", "ts", "reason", "bytes"} {
		if _, has := item[k]; !has {
			t.Fatalf("快照项缺 %q: %v", k, item)
		}
	}
	if item["id"] != id {
		t.Fatalf("id = %v, want %v", item["id"], id)
	}
	if item["reason"] != "manual" {
		t.Fatalf("reason = %v, want manual", item["reason"])
	}
	wantBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if item["bytes"] != float64(len(wantBytes)) {
		t.Fatalf("bytes = %v, want %d", item["bytes"], len(wantBytes))
	}
	if _, ok := item["ts"].(float64); !ok {
		t.Fatalf("ts 非数值: %v", item["ts"])
	}

	// 无内容下载端点红线：任何快照内容路径一律 404。
	for _, p := range []string{
		"/settings/snapshots/" + id,
		"/settings/snapshots/" + id + "/content",
		"/settings/snapshots/" + id + "/download",
	} {
		if code, _ := getRaw(t, e.port, p, e.token); code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404（无内容下载端点）", p, code)
		}
	}
}

// ---- 验收③：restore 逐字节还原 + 还原前强制 pre-restore 快照 ----

// TestSnapshotRestoreByteForByte 手动快照 → 改两写 → 还原该快照：
// config.toml 逐字节回到目标态；还原前的当前态已另存 pre-restore 快照；
// 响应 needs_restart=true。
func TestSnapshotRestoreByteForByte(t *testing.T) {
	e, cfgPath := newSettingsEnv(t)

	if code, raw := swPut(t, e, "gate", map[string]any{"cc_mode": "enforce"}); code != http.StatusOK {
		t.Fatalf("PUT #1 = %d %q, want 200", code, raw)
	}
	b1, err := os.ReadFile(cfgPath) // 目标还原态
	if err != nil {
		t.Fatal(err)
	}
	targetID := ssCreateManual(t, e)

	if code, raw := swPut(t, e, "gate", map[string]any{"cc_mode": "observe"}); code != http.StatusOK {
		t.Fatalf("PUT #2 = %d %q, want 200", code, raw)
	}
	b2, err := os.ReadFile(cfgPath) // 还原前当前态
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) == string(b2) {
		t.Fatal("夹具自蚀：两次写后 config 字节相同，还原断言失真")
	}

	code, raw := ssPost(t, e, "/settings/snapshots/"+targetID+"/restore")
	if code != http.StatusOK {
		t.Fatalf("restore = %d %q, want 200", code, raw)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("restore 响应非 JSON: %v (%q)", err, raw)
	}
	if resp["restored"] != true || resp["needs_restart"] != true {
		t.Fatalf("restore 响应 = %v, want restored=true needs_restart=true", resp)
	}

	// config.toml 与目标快照逐字节一致。
	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(b1) {
		t.Fatal("restore 后 config.toml 与目标快照不逐字节一致")
	}

	// 还原前当前态已另存 pre-restore 快照（恰一份，字节=b2）。
	files := ssFiles(t, ssSnapDir(e))
	var pre []string
	for n := range files {
		if strings.HasSuffix(n, "-pre-restore.toml") {
			pre = append(pre, n)
		}
	}
	if len(pre) != 1 {
		t.Fatalf("pre-restore 快照数 = %d, want 1: %v", len(pre), files)
	}
	preRaw, err := os.ReadFile(filepath.Join(ssSnapDir(e), pre[0]))
	if err != nil {
		t.Fatal(err)
	}
	if string(preRaw) != string(b2) {
		t.Fatal("pre-restore 快照与还原前当前态不逐字节一致")
	}
}

// ---- 验收④：滚动保留 20 份、最旧先删 ----

// TestSnapshotRetention25to20 连打 25 份手动快照（逐份钉距 mtime）：只剩
// 20 份，最旧 5 份（第 1~5 份）先删，其余俱在。
func TestSnapshotRetention25to20(t *testing.T) {
	e, _ := newSettingsEnv(t)
	dir := ssSnapDir(e)
	base := time.Now().Add(-time.Hour)

	var ids []string
	for i := 0; i < 25; i++ {
		id := ssCreateManual(t, e)
		ssStageMtime(t, dir, id, base, i) // 第 i 份钉到 base+i 秒（越后越新）
		ids = append(ids, id)
	}

	files := ssFiles(t, dir)
	if len(files) != 20 {
		t.Fatalf("25 份后剩余 = %d, want 20", len(files))
	}
	for i, id := range ids {
		_, present := files[id]
		if i < 5 && present {
			t.Fatalf("最旧第 %d 份 %q 应已删除（现存: %s）", i+1, id, ssDumpDir(t, dir))
		}
		if i >= 5 && !present {
			t.Fatalf("第 %d 份 %q 不应被删（只剩 20 份应保新）", i+1, id)
		}
	}
}

// ssDumpDir 诊断用：目录内快照文件名+mtime 升序一行一个。
func ssDumpDir(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err.Error()
	}
	type ni struct {
		name string
		mt   time.Time
	}
	var items []ni
	for _, en := range entries {
		info, err := en.Info()
		if err != nil {
			continue
		}
		items = append(items, ni{en.Name(), info.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mt.Before(items[j].mt) })
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "\n  %s %s", it.mt.Format("15:04:05.000000000"), it.name)
	}
	return b.String()
}

// ---- 滚动保留：类别优先（返工：先删最旧 auto，auto 删尽才删最旧 manual）----

// TestSnapshotPruneClassPriority 混合夹具：6 份旧 manual + 19 份新 auto
// （共 25）→ prune 后剩 20：6 份 manual 全在，删掉的 5 份全是最旧 auto；
// 全 manual（无 auto 让位）时删最旧 manual。
func TestSnapshotPruneClassPriority(t *testing.T) {
	// 混合面：先 6 份 manual（mtime 钉最旧），再 19 份 auto（经写路径 PUT
	// 产生，mtime 钉较新）。
	e, _ := newSettingsEnv(t)
	dir := ssSnapDir(e)
	base := time.Now().Add(-time.Hour)

	var manualIDs, autoIDs []string
	for i := 0; i < 6; i++ {
		id := ssCreateManual(t, e)
		ssStageMtime(t, dir, id, base, i)
		manualIDs = append(manualIDs, id)
	}
	for i := 0; i < 19; i++ {
		mode := "observe"
		if i%2 == 1 {
			mode = "enforce"
		}
		if code, raw := swPut(t, e, "gate", map[string]any{"cc_mode": mode}); code != http.StatusOK {
			t.Fatalf("PUT #%d = %d %q, want 200（产生 auto 快照）", i+1, code, raw)
		}
		// 最新 auto = 目录里 mtime 最新且未认领者（创建序=钉距序，逐一认领）。
		id := ssNewestAuto(t, dir, append(append([]string(nil), manualIDs...), autoIDs...))
		ssStageMtime(t, dir, id, base, 10+i)
		autoIDs = append(autoIDs, id)
	}

	files := ssFiles(t, dir)
	if len(files) != 20 {
		t.Fatalf("25 份后剩余 = %d, want 20", len(files))
	}
	for i, id := range manualIDs {
		if _, present := files[id]; !present {
			t.Fatalf("manual 第 %d 份 %q 不应被删（类别优先：auto 先让位）", i+1, id)
		}
	}
	for i, id := range autoIDs {
		_, present := files[id]
		if i < 5 && present {
			t.Fatalf("最旧 auto 第 %d 份 %q 应已删除（现存: %s）", i+1, id, ssDumpDir(t, dir))
		}
		if i >= 5 && !present {
			t.Fatalf("auto 第 %d 份 %q 不应被删（只删最旧 5 份 auto）", i+1, id)
		}
	}

	// 全 manual 面（独立环境，无 auto 让位）：23 份 → 剩 20，最旧 3 份先删。
	e2, _ := newSettingsEnv(t)
	dir2 := ssSnapDir(e2)
	var m2 []string
	for i := 0; i < 23; i++ {
		id := ssCreateManual(t, e2)
		ssStageMtime(t, dir2, id, base, i)
		m2 = append(m2, id)
	}
	files2 := ssFiles(t, dir2)
	if len(files2) != 20 {
		t.Fatalf("全 manual 23 份后剩余 = %d, want 20", len(files2))
	}
	for i, id := range m2 {
		_, present := files2[id]
		if i < 3 && present {
			t.Fatalf("全 manual：最旧第 %d 份 %q 应已删除", i+1, id)
		}
		if i >= 3 && !present {
			t.Fatalf("全 manual：第 %d 份 %q 不应被删", i+1, id)
		}
	}
}

// ssNewestAuto 目录内尚不存在于 claimed 的最新 auto 快照名（mtime 最新；混合
// 夹具逐 PUT 认领刚落的那份 auto）。
func ssNewestAuto(t *testing.T, dir string, claimed []string) string {
	t.Helper()
	seen := map[string]bool{}
	for _, c := range claimed {
		seen[c] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读快照目录: %v", err)
	}
	best, bestMt := "", time.Time{}
	for _, en := range entries {
		name := en.Name()
		if seen[name] || !strings.HasSuffix(name, "-auto.toml") {
			continue
		}
		info, err := en.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(bestMt) {
			best, bestMt = name, info.ModTime()
		}
	}
	if best == "" {
		t.Fatalf("目录中找不到新 auto 快照: %v", claimed)
	}
	return best
}

// TestSnapshotGuards 无/错 Bearer 401（POST known-path 后 auth、GET 先 auth）；
// 未知 id/穿越 id/异形子路径 404；替身（非 *Daemon）404。
func TestSnapshotGuards(t *testing.T) {
	e, _ := newSettingsEnv(t)
	id := ssCreateManual(t, e)

	// 无/错 Bearer。
	if c, _ := swReqNF(http.MethodPost, e.port, "", "/settings/snapshots", nil); c != http.StatusUnauthorized {
		t.Fatalf("无 Bearer 手动快照 = %d, want 401", c)
	}
	if c, _ := swReqNF(http.MethodPost, e.port, "wrong", "/settings/snapshots/"+id+"/restore", nil); c != http.StatusUnauthorized {
		t.Fatalf("错 Bearer restore = %d, want 401", c)
	}
	if c, _ := getRaw(t, e.port, "/settings/snapshots", ""); c != http.StatusUnauthorized {
		t.Fatalf("无 Bearer GET 列表 = %d, want 401", c)
	}

	// 未知 id / 穿越 id / 异形子路径 → 404。
	for _, p := range []string{
		"/settings/snapshots/config-19990101-000000-manual.toml/restore", // 不存在
		"/settings/snapshots/..%2F..%2Fconfig.toml/restore",              // 穿越解码后 ../..
		"/settings/snapshots/config-19990101-000000-manual.toml",         // 非 restore 子路径
		"/settings/snapshots/garbage/restore",                            // 非快照名
	} {
		if c, _ := swReqNF(http.MethodPost, e.port, e.token, p, nil); c != http.StatusNotFound {
			t.Fatalf("POST %s = %d, want 404", p, c)
		}
	}

	// restore 成功路径后 id 失效重放仍 404（快照还在，还原幂等面不受此钉约束
	// ——本钉只证未知 id 面；上面第一条已覆盖）。此处补穿越字节面。
	if c, _ := swReqNF(http.MethodPost, e.port, e.token,
		"/settings/snapshots/..%5C..%5Cferryman%5Cconfig.toml/restore", nil); c != http.StatusNotFound {
		t.Fatalf("反斜杠穿越 = %d, want 404", c)
	}

	// 替身（非 *Daemon）：手动快照/restore 404。
	h := makeHandler(&stopDaemon{}, "tok-ss", nil, nil)
	for _, p := range []string{"/settings/snapshots", "/settings/snapshots/x.toml/restore"} {
		// httptest.NewRequest 保证 Body 非 nil（doPost 先读光 body 的 RST 纪律
		// 在直连 ServeHTTP 面遇 nil Body 会炸）。
		req := httptest.NewRequest(http.MethodPost, p, nil)
		req.Header.Set("Authorization", "Bearer tok-ss")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("替身 POST %s = %d %q, want 404", p, rec.Code, rec.Body.String())
		}
	}
}
