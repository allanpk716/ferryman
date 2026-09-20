package update

// 最小语义化版本：tag 比较只需 vX.Y.Z[-<pre>] 的排序（构建元数据 +… 忽略）。
// 标准库无 semver，不引第三方——手写足够。

import (
	"errors"
	"strconv"
	"strings"
)

// semver 已解析的语义化版本。pre 空 = 正式版（正式 > 同号预发布）。
type semver struct {
	major, minor, patch int
	pre                 []string
}

// ParseSemver 解析 vX.Y.Z[-pre]；不合式报错（dev、commit 描述都在此失败）。
func ParseSemver(s string) (*semver, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	core, preStr := s, ""
	if i := strings.IndexByte(s, '-'); i >= 0 {
		core, preStr = s[:i], s[i+1:]
	}
	nums := strings.Split(core, ".")
	if len(nums) != 3 {
		return nil, errors.New("非 vX.Y.Z 形式: " + s)
	}
	v := &semver{}
	for i, p := range nums {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, errors.New("数字段不合法: " + s)
		}
		switch i {
		case 0:
			v.major = n
		case 1:
			v.minor = n
		case 2:
			v.patch = n
		}
	}
	if preStr != "" {
		v.pre = strings.Split(preStr, ".")
	}
	return v, nil
}

// Compare 按语义化版本优先级比较：a<b → -1，a>b → +1。
func (a *semver) Compare(b *semver) int {
	if c := cmpInt(a.major, b.major); c != 0 {
		return c
	}
	if c := cmpInt(a.minor, b.minor); c != 0 {
		return c
	}
	if c := cmpInt(a.patch, b.patch); c != 0 {
		return c
	}
	// 数字段比完：无预发布 > 有预发布
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		if c := comparePre(a.pre[i], b.pre[i]); c != 0 {
			return c
		}
	}
	return cmpInt(len(a.pre), len(b.pre))
}

// comparePre 预发布单段：两侧皆数字按数值；数字段 < 字母段；否则字典序。
func comparePre(x, y string) int {
	xn, xerr := strconv.Atoi(x)
	yn, yerr := strconv.Atoi(y)
	switch {
	case xerr == nil && yerr == nil:
		return cmpInt(xn, yn)
	case xerr == nil:
		return -1
	case yerr == nil:
		return 1
	default:
		return strings.Compare(x, y)
	}
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
