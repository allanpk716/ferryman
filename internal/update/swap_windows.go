//go:build windows

package update

// seam A(规格 §C 第6条):单次 MoveFileEx(new→exe, MOVEFILE_REPLACE_EXISTING)
// 原子替换——NTFS 元数据日志保证要么旧要么新,无 exe 缺位窗口。前提是停旧
// 已完成(运行中的 exe 映像拒绝 DELETE 访问,替换必败——停旧先于 swap 的
// 顺序即由此钉死)。公共 syscall 包不导出 MoveFileEx,经 x/sys/windows。

import (
	"golang.org/x/sys/windows"
)

// moveFileReplace 原子替换 dst ← src(存在即覆盖;同卷)。
func moveFileReplace(src, dst string) error {
	s, err := windows.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	d, err := windows.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(s, d, windows.MOVEFILE_REPLACE_EXISTING)
}
