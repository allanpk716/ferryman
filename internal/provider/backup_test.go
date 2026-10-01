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

// installBackupAt 写一份 install 链族备份（下划线戳形：<base>.bak-ferryman-
// YYYYMMDD_HHMMSS，installer.InstallCC/InstallCodex 同款命名）。
func installBackupAt(t *testing.T, cfgPath, stamp, content string) {
	t.Helper()
	writeFixture(t, cfgPath+backupMarker+stamp, content)
}

// 场景 A（票05 R1）：同日 install 族下划线备份（墙上时间更晚）与 provider 族
// 连字符组共存 → Restore 必选 provider 组。跨族字典序不可比：同日 '_'(0x5F)
// 大于 '-'(0x2D)，更早写的 install 备份也会排到 provider 组之后冒充"最新"。
func TestRestoreIgnoresInstallFamilyStamps(t *testing.T) {
	defer withStampSeq(t, "20260930-120000")()
	fp := interimFixture(t)
	orig := snapshot(t, fp.cc, fp.codexCfg, fp.orcaCfg)
	if _, err := Apply(targetsOf(fp)); err != nil { // provider 组：20260930-120000
		t.Fatal(err)
	}
	// install 链随后跑过（InstallCC 形）：CC 目录多出下划线戳备份，墙上时间更晚
	installBackupAt(t, fp.cc, "20260930_120005", `{"install":"here"}`)

	rep, err := Restore(targetsOf(fp))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Targets) != 3 {
		t.Fatalf("应选中 provider 组并逐三份还原: %+v", rep.Targets)
	}
	for _, r := range rep.Targets {
		if r.Action != ActionRestored {
			t.Fatalf("provider 组成员齐全应全部 restored（而非被 install 组击穿成 skipped）: %+v", r)
		}
	}
	if strings.Contains(mustReadStr(t, fp.cc), `"install":"here"`) {
		t.Fatal("restore 选中了 install 族备份——选组只应认本族连字符戳")
	}
	for p, b := range orig {
		if got := mustReadStr(t, p); got != string(b) {
			t.Fatalf("%s 还原后与接管前不一致", p)
		}
	}
}

// 场景 B：install 族是"最新"文件（同日下划线戳文件名必然大于连字符戳）但
// provider 组在位 → 同 A，Restore 必选 provider 组、逐三份还原。
func TestRestorePicksProviderOverLexicallyLargerInstallFile(t *testing.T) {
	defer withStampSeq(t, "20260930-120000")()
	fp := interimFixture(t)
	orig := snapshot(t, fp.cc, fp.codexCfg, fp.orcaCfg)
	if _, err := Apply(targetsOf(fp)); err != nil {
		t.Fatal(err)
	}
	// install 戳取当日最大形（23:59:59），文件名字典序必然压过 provider 组
	installBackupAt(t, fp.codexCfg, "20260930_235959", "install-side codex content")

	rep, err := Restore(targetsOf(fp))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Targets {
		if r.Action != ActionRestored {
			t.Fatalf("provider 组在位应逐三份 restored（install 文件不参与选组）: %+v", rep.Targets)
		}
	}
	if strings.Contains(mustReadStr(t, fp.codexCfg), "install-side codex content") {
		t.Fatal("restore 选中了 install 族备份（该组在 codex 目录缺 CC/orca 成员，本应整体出局）")
	}
	for p, b := range orig {
		if got := mustReadStr(t, p); got != string(b) {
			t.Fatalf("%s 还原后与接管前不一致", p)
		}
	}
}

// 场景 C（全新机器边角）：install 备份内容是 InstallCC 刚建的空 {} ——
// Restore 绝不能把 {} 当"接管前形态"还原回去。
func TestRestoreNeverPicksEmptyInstallBackup(t *testing.T) {
	defer withStampSeq(t, "20260930-120000")()
	fp := interimFixture(t)
	orig := snapshot(t, fp.cc, fp.codexCfg, fp.orcaCfg)
	if _, err := Apply(targetsOf(fp)); err != nil {
		t.Fatal(err)
	}
	installBackupAt(t, fp.cc, "20260930_120010", "{}")

	rep, err := Restore(targetsOf(fp))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Targets) != 3 {
		t.Fatalf("应选中 provider 组并逐三份还原: %+v", rep.Targets)
	}
	if got := mustReadStr(t, fp.cc); got == "{}" {
		t.Fatal("restore 把空 {} install 备份当接管前形态还原了")
	}
	for p, b := range orig {
		if got := mustReadStr(t, p); got != string(b) {
			t.Fatalf("%s 还原后与接管前不一致", p)
		}
	}
}

// 场景 D：只有 install 族备份、无 provider 组 → Restore 如实报"无备份"
// （不拿 install 组充数、不还原）。
func TestRestoreWithOnlyInstallFamilyErrors(t *testing.T) {
	fp := interimFixture(t)
	installBackupAt(t, fp.cc, "20260930_120005", `{"install":"only"}`)
	installBackupAt(t, fp.codexCfg, "20260930_120005", "install-side codex")
	if _, err := Restore(targetsOf(fp)); err == nil {
		t.Fatal("只有 install 族备份应报无备份（不还原）")
	}
}
