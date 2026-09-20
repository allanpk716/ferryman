//go:build !windows

package update

// seam A 非 Windows 存根:POSIX rename(2) 自带同目录原子替换语义。

import "os"

// moveFileReplace rename 原子替换(dst 存在即覆盖;同文件系统)。
func moveFileReplace(src, dst string) error {
	return os.Rename(src, dst)
}
