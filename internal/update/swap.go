package update

// 换装文件助手(票05,规格 §C 第6条):备份命名/旧备份只留 2 份/一切退出
// 路径清 .new 与 .swap-tmp 残留/copy 语义。单次原子替换本体(moveFileReplace)
// 在 swap_windows.go / swap_other.go。

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// exeBaseName 换装目标的旁路与备份基名(与固定产物名对应,exe 本名由
// 换装目标路径决定,不在此假设)。
const (
	newExeSuffix  = ".new"              // 旁路:ferryman.exe.new
	oldPrefixBase = "ferryman.exe.old-" // 备份:ferryman.exe.old-<版本>
)

// newExePath 旁路 .new 落点(与换装目标同卷——MoveFileEx 原子替换的前提)。
func newExePath(targetExe string) string {
	return targetExe + newExeSuffix
}

// backupPath 备份落点:<exe 目录>/ferryman.exe.old-<白名单化旧版本>。
func backupPath(exeDir, version string) string {
	return filepath.Join(exeDir, oldPrefixBase+sanitizeFileToken(version))
}

// staleAsidePath 占据者挪窝名:备份位被先前换装的存活让位者(运行映像,
// 可改名、不可被 REPLACE)占据时,先把它挪到本名再空出备份位。名字留在
// old-* 域内(pruneBackups 收账域)——占据者多为运行映像,prune 删不动时
// 静默跳过,待其进程退出后自动收走。日期+PID 防同秒撞名。
func staleAsidePath(backup string) string {
	return fmt.Sprintf("%s.stale-%s-%d", backup, time.Now().Format("20060102-150405"), os.Getpid())
}

// sanitizeFileToken 版本串进文件名:白名单 [A-Za-z0-9._-] 外一律 _;
// 空串(不可能的版本)落 unknown。
func sanitizeFileToken(s string) string {
	var b strings.Builder
	for _, r := range s {
		if 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' ||
			'0' <= r && r <= '9' || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// pruneBackups 旧备份自动只留 keep 份(按 ModTime 新→旧);不足/相等不动。
func pruneBackups(exeDir string, keep int) {
	matches, err := filepath.Glob(filepath.Join(exeDir, oldPrefixBase+"*"))
	if err != nil || len(matches) <= keep {
		return
	}
	type ent struct {
		path string
		mod  int64
	}
	ents := make([]ent, 0, len(matches))
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil {
			ents = append(ents, ent{m, st.ModTime().UnixNano()})
		}
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].mod > ents[j].mod })
	for _, e := range ents[keep:] {
		_ = os.Remove(e.path)
	}
}

// cleanSwapResidues 一切退出路径清理换装残留:.new/.new.part/.swap-tmp*/
// .supervisor-copy*(自中继副本无法删除自身运行镜像,留待此处收走)。
// .old-* 备份与正式 exe 不在清理域。目录缺失静默。
func cleanSwapResidues(exeDir string) {
	for _, pat := range []string{"ferryman.exe.new", "ferryman.exe.new.part", "ferryman.exe.swap-tmp*", "ferryman.exe.supervisor-copy*"} {
		matches, err := filepath.Glob(filepath.Join(exeDir, pat))
		if err != nil {
			continue
		}
		for _, m := range matches {
			_ = os.Remove(m)
		}
	}
}

// copyFile copy 语义(备份/回滚恢复都用 copy,不用 move——源保留:
// 备份保留份是回滚的再次保障)。目标已存在则覆盖。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
