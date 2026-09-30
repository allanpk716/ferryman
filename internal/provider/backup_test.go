// backup_test.go — 票05（F5）：接管前备份与 --restore 还原验收。全部临时目录，
// 绝不写真机配置。
package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 往返：interim → apply（写入器形态）→ restore → 三份逐字节回到接管前。
func TestBackupRestoreRoundtrip(t *testing.T) {
	fp := interimFixture(t)
	orig := snapshot(t, fp.cc, fp.codexCfg, fp.orcaCfg)
	if _, err := Apply(targetsOf(fp)); err != nil {
		t.Fatal(err)
	}
	// 已到接管形态：codex 不再指 15721
	if strings.Contains(mustReadStr(t, fp.codexCfg), "15721") {
		t.Fatal("apply 后 codex 仍指 15721——夹具失效")
	}
	rep, err := Restore(targetsOf(fp))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Targets) != 3 {
		t.Fatalf("还原应逐三份: %+v", rep.Targets)
	}
	for _, r := range rep.Targets {
		if r.Action != ActionRestored {
			t.Fatalf("应 restored: %+v", r)
		}
		if !strings.Contains(r.Backup, backupMarker) {
			t.Fatalf("还原报告应带备份路径: %+v", r)
		}
	}
	for p, b := range orig {
		if got := mustReadStr(t, p); got != string(b) {
			t.Fatalf("%s 还原后与接管前不一致（回到 interim 拓扑失败）", p)
		}
	}
}

// 最近一组语义：两次 apply 产生两组备份，restore 取最新组（第二次接管前的
// 漂移态），而不是第一组。
func TestRestorePicksLatestGroup(t *testing.T) {
	defer withStampSeq(t, "20260930-120001", "20260930-120002")()
	fp := interimFixture(t)
	if _, err := Apply(targetsOf(fp)); err != nil { // 第一组
		t.Fatal(err)
	}
	// 模拟接管后 codex 被外部改写（漂移到 15731），再次 apply → 第二组
	writeFixture(t, fp.codexCfg,
		strings.Replace(codexInterimForm, "15721", "15731", 1))
	if _, err := Apply(targetsOf(fp)); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(targetsOf(fp)); err != nil {
		t.Fatal(err)
	}
	// 还原的是第二组（= 15731 漂移态），不是第一组 interim
	got := mustReadStr(t, fp.codexCfg)
	if !strings.Contains(got, "15731") {
		t.Fatal("restore 应回到最近一次接管前（第二组=15731 漂移态）")
	}
	if !strings.Contains(got, "PROXY_MANAGED") {
		t.Fatal("第二组备份时 bearer 仍是 PROXY_MANAGED（漂移夹具带回的旧值）——还原应原样")
	}
}

// 无任何备份 → 报错（不是静默成功）。
func TestRestoreWithoutBackupsErrors(t *testing.T) {
	fp := interimFixture(t)
	if _, err := Restore(targetsOf(fp)); err == nil {
		t.Fatal("无备份应报错")
	}
}

// 最近组缺成员（orca 备份被清）→ 其余还原、orca 如实 skipped。
func TestRestorePartialGroupSkipsMissing(t *testing.T) {
	fp := interimFixture(t)
	orig := snapshot(t, fp.cc, fp.codexCfg, fp.orcaCfg)
	if _, err := Apply(targetsOf(fp)); err != nil {
		t.Fatal(err)
	}
	// 删掉 orca 目录里那组备份，模拟组缺成员
	orcaDir := filepath.Dir(fp.orcaCfg)
	for _, p := range bakFiles(t, fp) {
		if filepath.Dir(p) == orcaDir {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
		}
	}
	rep, err := Restore(targetsOf(fp))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]TargetReport{}
	for _, r := range rep.Targets {
		byName[r.Name] = r
	}
	if byName[targetCC].Action != ActionRestored || byName[targetCodex].Action != ActionRestored {
		t.Fatalf("在组成员应 restored: %+v", rep.Targets)
	}
	if byName[targetOrca].Action != ActionSkipped {
		t.Fatalf("缺成员应 skipped: %+v", rep.Targets)
	}
	for p, b := range orig {
		if p == fp.orcaCfg {
			continue // 缺成员,不在还原域
		}
		if got := mustReadStr(t, p); got != string(b) {
			t.Fatalf("%s 还原后与接管前不一致", p)
		}
	}
}

// withStampSeq 依次注入备份时间戳（同秒双组会互相覆盖——成组断言需要可分辨
// 的戳）；测试结束恢复。
func withStampSeq(t *testing.T, seq ...string) func() {
	t.Helper()
	orig := stampNow
	i := 0
	stampNow = func() string {
		if i < len(seq) {
			s := seq[i]
			i++
			return s
		}
		return orig()
	}
	return func() { stampNow = orig }
}
