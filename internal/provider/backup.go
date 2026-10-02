// backup.go — 票05（F5）：接管前备份与 --restore 还原。
//
// 命名沿用本机既有 bak-ferryman 惯例（与 installer.InstallCC/InstallCodex 的
// 备份同族）：<base>.bak-ferryman-<YYYYMMDD-HHMMSS>，戳格式按票05 背景材料
// （YYYYMMDD-HHMMSS 连字符形）。一次 Apply 的多份备份共用同一时间戳——成组
// （同时间戳前缀；票10 起含 pi 两文件）；Restore 取各目录里最新的那组逐一
// 还原，组内缺成员（如 orca 份 apply 时不在位、pi 未装时无备份）如实跳过
// 该份。
//
// 已知边界（票05 R1 修订）：~/.claude 与 ~/.codex 下另有 install 链备份
// （installer 的戳是 YYYYMMDD_HHMMSS 下划线形）与本族共享 *.bak-ferryman-*
// glob——Restore 选组只认本族连字符戳（backupStampRe），install 备份不参与
// 选组。此前跨族混排：同日 '_'(0x5F) 字典序大于 '-'(0x2D)，更早的 install
// 备份会冒充"最新"组击穿还原。
package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// backupMarker 备份名中缀（<base> + marker + <stamp>）。
const backupMarker = ".bak-ferryman-"

// backupStampLayout 戳格式（票05 背景材料：*.bak-ferryman-YYYYMMDD-HHMMSS）。
const backupStampLayout = "20060102-150405"

// backupStampRe 本族戳形（YYYYMMDD-HHMMSS 连字符形）。install 链族戳是
// YYYYMMDD_HHMMSS 下划线形（installer.stamp），两族共享同一 glob；跨族字典序
// 不可比（同日 '_' > '-'，戳序≠时间序）——选组只收本族戳，"字典序最大戳=最新
// 接管前组"的不变式才成立。
var backupStampRe = regexp.MustCompile(`^\d{8}-\d{6}$`)

// stampNow 备份时间戳（包级变量——测试可注入序列，同秒双组需要可分辨的戳）。
var stampNow = func() string { return time.Now().Format(backupStampLayout) }

// copyFile 内容拷贝 + 尽力保留权限位（installer.backupFile 同语义；不 import
// installer——依赖方向只能 installer → provider）。
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if info, err := os.Stat(src); err == nil {
		perm = info.Mode().Perm()
	}
	return os.WriteFile(dst, data, perm)
}

// Restore --restore 语义（票05 F5；票10 覆盖 pi 两目标）：按最近一次接管前
// 备份组还原，回 interim 拓扑。组 = 各目标目录里同时间戳后缀、戳形为本族
// 连字符形（backupStampRe）的 *.bak-ferryman-*——install 族下划线戳不入选组；
// 取字典序最大的戳（本族内同格式戳即时间序）。组内缺哪个成员就如实跳过哪
// 份（pi 未装的机器上从无 pi 备份，跳过不失败）；所有目录皆无本族备份 →
// 报错（不是静默成功）。
func Restore(t Targets) (ApplyReport, error) {
	var rep ApplyReport
	type rt struct {
		name, path string
	}
	rts := []rt{
		{targetCC, t.CCSettings},
		{targetCodex, t.CodexConfig},
		{targetOrca, t.OrcaCodexConfig},
	}
	// pi 两目标（票10）：路径成对派生才入还原清单——未派生（旧调用形态）时
	// 不扫 pi 目录，与 Apply 的入案判定同口径。
	if t.PiModels != "" && t.PiSettings != "" {
		rts = append(rts, rt{targetPiModels, t.PiModels}, rt{targetPiSettings, t.PiSettings})
	}
	perTarget := make([]map[string]string, len(rts))
	stamps := map[string]bool{}
	for i, r := range rts {
		perTarget[i] = map[string]string{}
		dir, base := filepath.Dir(r.path), filepath.Base(r.path)
		matches, err := filepath.Glob(filepath.Join(dir, base+backupMarker+"*"))
		if err != nil {
			return rep, fmt.Errorf("provider: 备份扫描失败(%s): %w", r.path, err)
		}
		for _, m := range matches {
			s := strings.TrimPrefix(filepath.Base(m), base+backupMarker)
			if !backupStampRe.MatchString(s) {
				continue // 非本族戳（install 族下划线形）不入选组——见 backupStampRe
			}
			perTarget[i][s] = m
			stamps[s] = true
		}
	}
	if len(stamps) == 0 {
		return rep, fmt.Errorf("provider: 无接管前备份（*%s*）——apply 从未写过，无法还原",
			backupMarker)
	}
	sorted := make([]string, 0, len(stamps))
	for s := range stamps {
		sorted = append(sorted, s)
	}
	sort.Strings(sorted)
	latest := sorted[len(sorted)-1]
	for i, r := range rts {
		b, ok := perTarget[i][latest]
		if !ok {
			rep.Targets = append(rep.Targets, TargetReport{Name: r.name, Path: r.path,
				Action: ActionSkipped,
				Detail: "最近一组备份(" + latest + ")缺该目标——未还原"})
			continue
		}
		if err := copyFile(b, r.path); err != nil {
			return rep, fmt.Errorf("provider: 还原 %s 失败: %w", r.path, err)
		}
		rep.Targets = append(rep.Targets, TargetReport{Name: r.name, Path: r.path,
			Action: ActionRestored, Backup: b,
			Detail: "已还原到接管前备份(" + latest + ")"})
	}
	return rep, nil
}
