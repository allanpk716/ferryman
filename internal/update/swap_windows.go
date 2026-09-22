//go:build windows

package update

// seam A 原语:MoveFileEx(src→dst, MOVEFILE_REPLACE_EXISTING)——同卷移动,
// dst 存在即覆盖(可覆盖陈旧备份;对运行中映像会 Access denied)。两步换装
// 编排(改名让位)在 supervisor.go swapFiles;运行映像「可改名、不可覆盖」
// 的 Windows 语义是该编排的根基(ADR-0015)。

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
