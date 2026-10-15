package daemon

// 票39（spec#34，ADR-0025 信号②「用户显式命令」）：显式恢复入口的端点缝
// 钉子——显式请求走 restoreNewest 的“单候选注入最新／多候选列清单／无候选
// 明说”语义，照常记 inject 账并登记注入会话（上一代交接判定依赖它）；
// 非显式行为由 #38 定义（零注入终态），本票零改动（既有 restore_test.go
// 票38 用例继续钉住）。接缝 = Daemon.Restore（HTTP 面是 query 薄包装，
// singleton_test.go 钉参数透传）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ferryman/internal/accounts"
	"ferryman/internal/pathsx"
)

func TestRestoreExplicitSingleInjectsAndBooks(t *testing.T) {
	// 显式、单份交接（无锚——显式路径不依赖被拦锚）：注入该交接 + inject
	// 记账（lineage 按源会话转录路径）+ mark_injected 登记注入会话。
	w := newWenv(t)
	srcPath := filepath.Join(w.tmp, "src.jsonl")
	if err := os.WriteFile(srcPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.led.TouchFull("cc", "src", srcPath, w.t0, 10, "C:/srccwd", "", 100, 0)
	en := w.store.SaveHandoff("src", "cc", "C:/proj", "标题显式",
		isoUTC(w.t0-100), "fresh", "<<<INJECT>>>\n显式取回正文\n<<</INJECT>>>")
	r := w.d.RestoreExplicit("cc", "C:/proj", "explicit-sid")
	ctx, _ := r["context"].(string)
	if !strings.Contains(ctx, "会话 标题显式") || !strings.Contains(ctx, "显式取回正文") {
		t.Fatalf("显式单份应注入该交接: %q", ctx)
	}
	if !strings.Contains(ctx, "完整交接文档: "+en.Path) {
		t.Fatalf("应带完整文档行: %q", ctx)
	}
	// inject 账：session_id = 显式请求会话、handoff_id 对应、lineage 按源会话
	injects := w.acc.Read(accounts.ReadOpts{Kind: "inject"})
	if len(injects) != 1 {
		t.Fatalf("inject 行数 = %d, want 1: %v", len(injects), injects)
	}
	inv := injects[0]
	if inv["session_id"] != "explicit-sid" {
		t.Fatalf("session_id = %v, want explicit-sid", inv["session_id"])
	}
	if inv["handoff_id"] != en.HandoffID {
		t.Fatalf("handoff_id = %v, want %v", inv["handoff_id"], en.HandoffID)
	}
	if inv["lineage_id"] != pathsx.NormPath(srcPath) {
		t.Fatalf("lineage_id = %v, want %v", inv["lineage_id"], pathsx.NormPath(srcPath))
	}
	// mark_injected：index 注入去重追加 explicit-sid（上一代判定依赖它）
	data, err := os.ReadFile(filepath.Join(w.tmp, "data", "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Handoffs []struct {
			Injected []string `json:"injected"`
		} `json:"handoffs"`
	}
	if err := json.Unmarshal(data, &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Handoffs) != 1 || len(idx.Handoffs[0].Injected) != 1 ||
		idx.Handoffs[0].Injected[0] != "explicit-sid" {
		t.Fatalf("mark_injected 未登记注入会话: %+v", idx.Handoffs)
	}
}

func TestRestoreExplicitMultiLists(t *testing.T) {
	// 显式、多份：列清单（前 5、covers 降序、必问不猜）——语义钉子已由
	// TestRestoreNewestMultiListsTopFiveOnly / TwoCandidates… 移回端点缝
	// （restore_test.go），此处只钉显式多份不出 inject 账（清单不是注入）。
	e := newGateEnv(t)
	saveCand(t, e, "s1", "h1", 100, "md")
	saveCand(t, e, "s2", "h2", 200, "md")
	r := e.d.RestoreExplicit("cc", "C:/proj", "explicit-sid")
	ctx, _ := r["context"].(string)
	if !strings.HasPrefix(ctx, "[Ferryman] 本项目有 2 份可用交接") {
		t.Fatalf("显式多份应列清单: %q", ctx)
	}
	if _, has := r["continuation"]; has {
		t.Fatalf("显式响应不回 continuation（dsh 插件契约键）: %v", r)
	}
}

func TestRestoreExplicitNoCandidatesSaysPlainly(t *testing.T) {
	// 显式、无候选：明说无可用交接（CLI/斜杠命令把 context 打给人看——
	// 沉默的 nil 会变成“命令没反应”）。
	e := newGateEnv(t)
	r := e.d.RestoreExplicit("cc", "C:/proj", "explicit-sid")
	ctx, _ := r["context"].(string)
	if ctx == "" || !strings.Contains(ctx, "没有可用交接") {
		t.Fatalf("显式无候选应明说: %v", r)
	}
	if _, has := r["continuation"]; has {
		t.Fatalf("显式响应不回 continuation: %v", r)
	}
}

func TestRestoreExplicitContrastNonExplicitZeroInject(t *testing.T) {
	// 同数据对照：非显式（无锚）零注入终态（#38 逐字不变）；显式注入。
	e := newGateEnv(t)
	saveCand(t, e, "src", "单线", 100, "md")
	assertZeroInjectTerminal(t, e.d.Restore("cc", "C:/proj", "newsid"))
	ctx, _ := e.d.RestoreExplicit("cc", "C:/proj", "newsid2")["context"].(string)
	if !strings.Contains(ctx, "会话 单线") {
		t.Fatalf("显式应注入单份交接: %q", ctx)
	}
}

func TestRestoreExplicitEmptySessionIDSynthesizesCLIRestoreID(t *testing.T) {
	// 显式请求 session_id 缺省（CLI 形态）：守护合成 cli-restore-* 记账
	// （inject 账的 session_id 非空、mark_injected 登记合成 id）。
	w := newWenv(t)
	w.store.SaveHandoff("src", "cc", "C:/proj", "t", isoUTC(w.t0-100), "fresh",
		"<<<INJECT>>>\n正文\n<<</INJECT>>>")
	r := w.d.RestoreExplicit("cc", "C:/proj", "")
	if ctx, _ := r["context"].(string); !strings.Contains(ctx, "正文") {
		t.Fatalf("应照常注入: %v", r)
	}
	injects := w.acc.Read(accounts.ReadOpts{Kind: "inject"})
	if len(injects) != 1 {
		t.Fatalf("inject 行数 = %d, want 1", len(injects))
	}
	sid, _ := injects[0]["session_id"].(string)
	if !strings.HasPrefix(sid, "cli-restore-") || len(sid) <= len("cli-restore-") {
		t.Fatalf("session_id 应合成 cli-restore-<ts>: %q", sid)
	}
	data, err := os.ReadFile(filepath.Join(w.tmp, "data", "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Handoffs []struct {
			Injected []string `json:"injected"`
		} `json:"handoffs"`
	}
	if err := json.Unmarshal(data, &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Handoffs) != 1 || len(idx.Handoffs[0].Injected) != 1 ||
		idx.Handoffs[0].Injected[0] != sid {
		t.Fatalf("mark_injected 应登记合成 id: %+v", idx.Handoffs)
	}
}

func TestRestoreExplicitConsumesPendingOfNewest(t *testing.T) {
	// 显式取回最新候选时 consume_for 语义保留：待续原话随注入带上一次
	// （单候选注入路径 PopPendingPrompt 既有行为，显式照旧）。
	e := newGateEnv(t)
	saveCand(t, e, "src", "t", 100, "md")
	e.store.SavePendingPromptFor("cc", "C:/proj", "src", "被拦原话E")
	ctx, _ := e.d.RestoreExplicit("cc", "C:/proj", "explicit-sid")["context"].(string)
	if !strings.Contains(ctx, "被拦原话E") {
		t.Fatalf("显式注入应带最新候选的待续原话: %q", ctx)
	}
}
