// Package cutover 切换工具（票23，spec §Further Notes 切换时序 + 评审附录
// #4/#5/#6/#9/#10/#11）：数据备份 / 回退工件生成与演练 / 沙箱冒烟。
//
// 票面铁律：本包只做切换的**工具面**，不执行任何生产切换——不删 Python 源、
// 不停真实守护、不写真实数据目录（BackupData 只读复制；DrillRollback 只在
// 调用方给定的临时目录里活动；SmokeAll 全程独立端口+独立数据目录）。
package cutover

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// BackupData 数据全量备份（评审附录#6：Go 写坏账本时 git 恢复不了数据，
// 备份才能）。复制内容（存在才复制）：
//
//	accounts/ 全部 *.jsonl、handoffs/ 全部 *.md、index.json、
//	config.toml、daemon.token；daemon.pid 不备（易失运行态）。
//
// 目标 = <destDir>/ferryman-backup-<YYYYmmdd-HHMMSS>/（同秒冲突加序号）。
// 只读复制：源文件零改动；逐文件 SHA-256 复读回比对后打印清单位点清单。
// 返回备份目录路径。
func BackupData(dataDir, destDir string) (string, error) {
	// 源目录存在性：数据目录必须在（备份一个不存在的目录毫无意义）
	if st, err := os.Stat(dataDir); err != nil {
		return "", fmt.Errorf("数据目录不可读: %w", err)
	} else if !st.IsDir() {
		return "", fmt.Errorf("数据目录不是目录: %s", dataDir)
	}
	if destDir == "" {
		destDir = filepath.Dir(dataDir) // 缺省 = 数据目录同卷上级
	}

	dest, err := backupDestDir(destDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}

	// 备份清单：(源, 目标)。目录类（accounts/handoffs）在 collect 阶段展开为
	// 逐文件；单文件类存在才备。daemon.pid 有意不在清单——易失运行态，回放无意义。
	type item struct{ src, dst string }
	var items []item
	add := func(src, dst string) {
		if _, err := os.Stat(src); err == nil {
			items = append(items, item{src, dst})
		}
	}
	for _, pat := range []struct{ sub, ext string }{
		{"accounts", ".jsonl"},
		{"handoffs", ".md"},
	} {
		dir := filepath.Join(dataDir, pat.sub)
		des, err := os.ReadDir(dir)
		if err != nil {
			continue // 子目录不存在 = 无可备（新装环境），不报错
		}
		dstSub := filepath.Join(dest, pat.sub)
		needSub := false
		for _, de := range des {
			if de.IsDir() || filepath.Ext(de.Name()) != pat.ext {
				continue
			}
			if !needSub {
				if err := os.MkdirAll(dstSub, 0o755); err != nil {
					return "", err
				}
				needSub = true
			}
			add(filepath.Join(dir, de.Name()), filepath.Join(dstSub, de.Name()))
		}
	}
	for _, name := range []string{"index.json", "config.toml", "daemon.token"} {
		add(filepath.Join(dataDir, name), filepath.Join(dest, name))
	}

	// 复制 + SHA-256 复读回比对（副本完整性自证）
	fmt.Printf("[backup] %s -> %s\n", dataDir, dest)
	if len(items) == 0 {
		fmt.Println("[backup] （空清单：数据目录无可备文件）")
	}
	for _, it := range items {
		sumHex, err := copyVerified(it.src, it.dst)
		if err != nil {
			return "", fmt.Errorf("复制 %s: %w", it.src, err)
		}
		info, err := os.Stat(it.src)
		size := int64(-1)
		if err == nil {
			size = info.Size()
		}
		fmt.Printf("  %-40s %8d B  sha256:%s\n", relPath(dataDir, dest, it), size, sumHex[:12])
	}
	fmt.Printf("[backup] 完成：%d 个文件；恢复 = 整目录拷回数据目录（先停守护）\n", len(items))
	return dest, nil
}

// backupDestDir 带时间戳备份目录；同秒重跑加序号 -2、-3…（幂等不覆盖旧备份）。
func backupDestDir(destDir string) (string, error) {
	base := filepath.Join(destDir, "ferryman-backup-"+time.Now().Format("20060102-150405"))
	dest := base
	for n := 2; ; n++ {
		if _, err := os.Stat(dest); os.IsNotExist(err) {
			return dest, nil
		} else if err != nil {
			return "", err
		}
		dest = fmt.Sprintf("%s-%d", base, n)
	}
}

// copyVerified 复制单文件并复读回做 SHA-256 比对（源与副本哈希一致才返回）。
func copyVerified(src, dst string) (string, error) {
	raw, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	srcSum := sha256.Sum256(raw)
	info, err := os.Stat(src)
	perm := os.FileMode(0o644)
	if err == nil {
		perm = info.Mode().Perm()
	}
	if err := os.WriteFile(dst, raw, perm); err != nil {
		return "", err
	}
	back, err := os.ReadFile(dst)
	if err != nil {
		return "", err
	}
	dstSum := sha256.Sum256(back)
	if srcSum != dstSum {
		return "", fmt.Errorf("副本哈希不一致 src=%s dst=%s",
			shortHash(srcSum[:]), shortHash(dstSum[:]))
	}
	return hex.EncodeToString(srcSum[:]), nil
}

// relPath 清单显示用的相对路径（数据目录内显示相对形；备份目录名显示尾段）。
func relPath(dataDir, destDir string, it struct{ src, dst string }) string {
	if rel, err := filepath.Rel(dataDir, it.src); err == nil && len(rel) > 0 && rel != "." {
		return filepath.ToSlash(rel)
	}
	base := filepath.Base(it.src)
	if d, err := filepath.Rel(destDir, filepath.Dir(it.dst)); err == nil && d != "." {
		return filepath.ToSlash(filepath.Join(d, base))
	}
	return base
}

func shortHash(sum []byte) string { return hex.EncodeToString(sum)[:12] }
