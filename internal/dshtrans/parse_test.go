package dshtrans

// 代文件名解析与代选择（dsh filename.ts CANONICAL_LOG_FILENAME 钉死）＋
// 头行/事件行解析（真机 v0/v4 形状钉死）。

import (
	"path/filepath"
	"testing"
)

func TestParseGenerationFilename(t *testing.T) {
	cases := []struct {
		name          string
		wantV         int
		wantZstd      bool
		wantCanonical bool
	}{
		{"session.jsonl", 0, false, true},
		{"session.jsonl.zstd", 0, true, true},
		{"session.v4.jsonl", 4, false, true},
		{"session.v4.jsonl.zstd", 4, true, true},
		{"session.v12.jsonl.zstd", 12, true, true},
		// 非规范：大写、前导零、.v0、纯后缀错位、临时名。
		{"session.V4.jsonl", 0, false, false},
		{"session.v04.jsonl", 0, false, false},
		{"session.v0.jsonl", 0, false, false},
		{"session.jsonlz", 0, false, false},
		{"session.v4.jsonl.zstd.9f2a71.tmp", 0, false, false},
		{"rollout-2026.jsonl", 0, false, false},
		{"sessions.jsonl", 0, false, false},
		{"session.v4x.jsonl", 0, false, false},
	}
	for _, c := range cases {
		v, z, canon := ParseGenerationFilename(c.name)
		if v != c.wantV || z != c.wantZstd || canon != c.wantCanonical {
			t.Errorf("ParseGenerationFilename(%q) = (%d,%v,%v), want (%d,%v,%v)",
				c.name, v, z, canon, c.wantV, c.wantZstd, c.wantCanonical)
		}
	}
}

func TestLatestGenerationPicksHighest(t *testing.T) {
	dir := t.TempDir()
	// 迁移现场：v0 无名代 + v4 现行代并存 → 取 v4。
	writeGen(t, dir, "session.jsonl.zstd", zstdFrames(t, realHeaderV0))
	want := writeGen(t, dir, "session.v4.jsonl.zstd", zstdFrames(t, realHeaderV4))
	writeGen(t, dir, "session.v4.jsonl.zstd.9f2a71c0b3d4.tmp", []byte("garbage")) // 临时名不识别
	gen, ok := LatestGeneration(dir)
	if !ok || gen.Path != want || gen.Version != 4 || !gen.Zstd {
		t.Fatalf("LatestGeneration = %+v ok=%v, want v4 %s", gen, ok, want)
	}
	// 仅旧代：取 v0。
	dir2 := t.TempDir()
	p0 := writeGen(t, dir2, "session.jsonl.zstd", zstdFrames(t, realHeaderV0))
	gen2, ok := LatestGeneration(dir2)
	if !ok || gen2.Path != p0 || gen2.Version != 0 {
		t.Fatalf("仅 v0: %+v ok=%v", gen2, ok)
	}
	// 空目录/不存在 → false。
	if _, ok := LatestGeneration(t.TempDir()); ok {
		t.Error("空目录应 false")
	}
	if _, ok := LatestGeneration(filepath.Join(t.TempDir(), "nope")); ok {
		t.Error("不存在目录应 false")
	}
	// 未来代 v5 出现 → 直接选中（向前兼容）。
	dir3 := t.TempDir()
	p5 := writeGen(t, dir3, "session.v5.jsonl.zstd", zstdFrames(t, realHeaderV4))
	writeGen(t, dir3, "session.v4.jsonl.zstd", zstdFrames(t, realHeaderV4))
	gen3, ok := LatestGeneration(dir3)
	if !ok || gen3.Path != p5 || gen3.Version != 5 {
		t.Fatalf("未来代: %+v ok=%v", gen3, ok)
	}
}

func TestReadHeaderLineBothEncodings(t *testing.T) {
	dir := t.TempDir()
	pz := writeGen(t, dir, "session.v4.jsonl.zstd", zstdFrames(t, realHeaderV4))
	if line, ok := ReadHeaderLine(pz, true); !ok || line != realHeaderV4 {
		t.Errorf("zstd 头行: ok=%v len=%d", ok, len(line))
	}
	pp := writeGen(t, dir, "session.jsonl", []byte(realHeaderV0))
	if line, ok := ReadHeaderLine(pp, false); !ok || line != realHeaderV0 {
		t.Errorf("明文头行: ok=%v len=%d", ok, len(line))
	}
	// 坏形：无完整行。
	pb := writeGen(t, dir, "broken.jsonl", []byte("no newline"))
	if _, ok := ReadHeaderLine(pb, false); ok {
		t.Error("无换行头行应 false")
	}
	if _, ok := ReadHeaderLine(filepath.Join(dir, "absent"), false); ok {
		t.Error("缺文件应 false")
	}
}

func TestParseHeaderLine(t *testing.T) {
	h4, ok := ParseHeaderLine(realHeaderV4)
	if !ok {
		t.Fatal("v4 头行应可解析")
	}
	want := Header{Version: 4, ID: "session-6e360520-09b8-46b6-9d82-cc9446ec8bd9",
		CreatedAt:       1790905220031,
		Cwd:             "C:\\Users\\allan716\\orca\\workspaces\\Ferryman\\支持-deepseek-harness-服务商配置",
		DelegationDepth: 0}
	if h4 != want {
		t.Errorf("v4 头 = %+v, want %+v", h4, want)
	}
	h0, ok := ParseHeaderLine(realHeaderV0)
	if !ok || h0.Version != 0 || h0.CreatedAt != 1788397646083 ||
		h0.Cwd != "C:\\WorkSpace\\ca_things" || h0.IsSeeded {
		t.Errorf("v0 头 = %+v ok=%v（isSeeded 缺省 false）", h0, ok)
	}
	// 子代理头（origin/parentSession/delegationDepth）。
	childLine := `{"type":"session","version":4,"id":"session-child","createdAt":1790905300000,"cwd":"C:\\p","parentSession":"session-parent","isSeeded":true,"origin":"subagent","delegationDepth":1}` + "\n"
	hc, ok := ParseHeaderLine(childLine)
	if !ok || hc.ParentSession != "session-parent" || hc.Origin != "subagent" ||
		hc.DelegationDepth != 1 || !hc.IsSeeded {
		t.Errorf("子头 = %+v ok=%v", hc, ok)
	}
	// 坏形拒绝。
	for _, bad := range []string{
		"", "not json", `[]`, `{"type":"user/message"}`,
		`{"type":"session"}`, // 缺 id
	} {
		if _, ok := ParseHeaderLine(bad); ok {
			t.Errorf("坏头行 %q 应拒绝", bad)
		}
	}
}

func TestParseChunkUsageAndTitle(t *testing.T) {
	text := realUsageLine +
		// 带缓存读写两列的 usage（Kimi/GLM 缓存形态）。
		`{"type":"assistant/message","seq":20,"time":1790905240000,"data":{"turn":2,"step":1,"message":{"role":"assistant","content":[],"source":{"kind":"model","provider":"zai-coding-cn","model":"glm-5.3"}},"usage":{"inputTokens":120,"outputTokens":80,"cacheReadTokens":30000,"cacheWriteTokens":5000}}}` + "\n" +
		// 标题事件。
		`{"type":"session/title","seq":21,"time":1790905250000,"data":{"title":"调研会话","messageSeqs":[8],"source":"user"}}` + "\n" +
		// 无 usage 的 assistant/message：跳过。
		`{"type":"assistant/message","seq":22,"time":1790905260000,"data":{"turn":2,"step":2,"message":{"role":"assistant","content":[],"source":{"kind":"model","provider":"x","model":"y"}}}}` + "\n" +
		// 无关事件与坏行：跳过不抛。
		`{"type":"turn/start","seq":23,"time":1790905270000,"data":{"turn":3}}` + "\n" +
		`{"type":"todo/write","ignorable":true,"seq":24,"time":1,"data":{"x":1}}` + "\n" +
		"{broken json\n\n"
	rows, title, children := ParseChunk(text, "旧标题")
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if title != "调研会话" {
		t.Errorf("title = %q", title)
	}
	if len(children) != 0 {
		t.Errorf("不应有子女: %v", children)
	}
	r1 := rows[0]
	if r1.Seq != 16 || r1.InputTokens != 9169 || r1.OutputTokens != 21 ||
		r1.CacheReadTokens != 0 || r1.CacheWriteTokens != 0 {
		t.Errorf("真机行四列: %+v", r1)
	}
	if r1.BilledInput() != 9169 {
		t.Errorf("计费输入 = %d", r1.BilledInput())
	}
	if !r1.HasTS || r1.TS != 1790905227.524 { // 毫秒 → 秒
		t.Errorf("TS = %v has=%v", r1.TS, r1.HasTS)
	}
	if r1.Model != "GLM-5.3" { // responseModel 优先（真实上游）
		t.Errorf("Model = %q", r1.Model)
	}
	r2 := rows[1]
	if r2.Model != "glm-5.3" || r2.CacheReadTokens != 30000 || r2.CacheWriteTokens != 5000 ||
		r2.BilledInput() != 35120 {
		t.Errorf("缓存行: %+v", r2)
	}
	// 标题携带：段内无标题事件时保留传入值。
	_, t2, _ := ParseChunk(`{"type":"turn/end","seq":30,"time":1,"data":{}}`+"\n", "调研会话")
	if t2 != "调研会话" {
		t.Errorf("标题携带失效: %q", t2)
	}
}

func TestParseChunkCatalog(t *testing.T) {
	text := `{"type":"subagent/catalog","seq":40,"time":1790905300000,"data":{"version":0,"childId":"session-child-1","childCreatedAt":1790905290000,"mode":"one-shot","label":"调研助手"}}` + "\n" +
		`{"type":"subagent/catalog","seq":41,"time":1790905310000,"data":{"version":1,"childId":"session-child-2","childCreatedAt":1790905309000,"mode":"continuable","label":"长跑"}}` + "\n" +
		`{"type":"subagent/catalog","seq":42,"time":1790905320000,"data":{"version":1,"childId":"session-child-3","childCreatedAt":1790905319000,"mode":"unknown"}}` + "\n"
	_, _, children := ParseChunk(text, "")
	if len(children) != 3 {
		t.Fatalf("want 3 children, got %d", len(children))
	}
	want := []Child{
		{ChildID: "session-child-1", ChildCreatedAt: 1790905290000, Mode: "one-shot", Label: "调研助手"},
		{ChildID: "session-child-2", ChildCreatedAt: 1790905309000, Mode: "continuable", Label: "长跑"},
		{ChildID: "session-child-3", ChildCreatedAt: 1790905319000, Mode: "unknown"},
	}
	for i, c := range children {
		if c != want[i] {
			t.Errorf("child[%d] = %+v, want %+v", i, c, want[i])
		}
	}
}

func TestCatalogChildrenEndToEnd(t *testing.T) {
	p := writeGen(t, t.TempDir(), "session.v4.jsonl.zstd", zstdFrames(t,
		realHeaderV4,
		`{"type":"subagent/catalog","seq":40,"time":1790905300000,"data":{"version":0,"childId":"session-child-1","childCreatedAt":1790905290000,"mode":"one-shot"}}`+"\n",
		`{"type":"assistant/message","seq":41,"time":1790905310000,"data":{"message":{"source":{}},"usage":{"inputTokens":1,"outputTokens":1}}}`+"\n"+
			`{"type":"subagent/catalog","seq":42,"time":1790905320000,"data":{"version":0,"childId":"session-child-2","childCreatedAt":1790905319000,"mode":"one-shot"}}`+"\n"))
	kids := CatalogChildren(p, true)
	if len(kids) != 2 || kids[0].ChildID != "session-child-1" || kids[1].ChildID != "session-child-2" {
		t.Fatalf("CatalogChildren = %+v", kids)
	}
	// 明文路径同样可用。
	pp := writeGen(t, t.TempDir(), "session.jsonl", []byte(
		realHeaderV0+`{"type":"subagent/catalog","seq":9,"time":1,"data":{"version":0,"childId":"c9","childCreatedAt":2,"mode":"continuable","label":"L"}}`+"\n"))
	kids2 := CatalogChildren(pp, false)
	if len(kids2) != 1 || kids2[0].ChildID != "c9" || kids2[0].Label != "L" {
		t.Fatalf("明文 CatalogChildren = %+v", kids2)
	}
	// 无子女文件。
	pn := writeGen(t, t.TempDir(), "session.v4.jsonl.zstd", zstdFrames(t, realHeaderV4))
	if kids := CatalogChildren(pn, true); kids != nil {
		t.Errorf("无子女应 nil: %v", kids)
	}
}
