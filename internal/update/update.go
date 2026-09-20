// Package update 自升级只读核心（发布链票03）：目标版本解析、--check 报告、
// 资产下载与 SHA256 校验（下载见 download.go，版本比较见 semver.go）。执行路径
// （锁/journal/换装/停旧/拉起）属监督者，票05 只复用本包库函数。repo slug
// 硬编码，配置化后补。
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	// repoSlug 发布仓（D2：GitHub Release 唯一正式分发途径）。
	repoSlug = "allanpk716/ferryman"
	// assetName 固定产物名（D11：裸 exe 不含版本号，供 latest/download 直链）。
	assetName = "ferryman_windows_amd64.exe"
)

// Endpoints 网络端点与 HTTP 客户端。零值 = 真实 GitHub + 走环境代理的内建
// 客户端（D8：HTTP(S)_PROXY）；测试注入 httptest 回环基址，零外呼。
type Endpoints struct {
	APIBase string       // Release API 基址；空 = https://api.github.com
	DLBase  string       // 下载直链基址；空 = https://github.com
	HTTP    *http.Client // 空 = 内建 ProxyFromEnvironment 客户端
}

func (e Endpoints) apiBase() string {
	if e.APIBase == "" {
		return "https://api.github.com"
	}
	return strings.TrimSuffix(e.APIBase, "/")
}

func (e Endpoints) dlBase() string {
	if e.DLBase == "" {
		return "https://github.com"
	}
	return strings.TrimSuffix(e.DLBase, "/")
}

func (e Endpoints) client() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return defaultHTTPClient
}

// getJSON GET 并解码 JSON 响应。
func (e Endpoints) getJSON(apiURL string, v any) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, apiURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := e.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("未找到（404）: %s", apiURL)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GitHub API %s: HTTP %d %s", apiURL, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// ghRelease GitHub release API 的最小字段集。
type ghRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// Target 目标版本解析结果：tag 与同 release 的 exe/校验资产下载直链。
type Target struct {
	Tag    string
	ExeURL string
	ShaURL string
}

// ResolveTarget 解析升级目标：
//
//	spec 空、非 prerelease → releases/latest（稳定版；GitHub 侧已排除 prerelease/draft）
//	spec 空、  prerelease → 列表接口取最新一条非 draft（纳入 rc/beta；列表按创建时间倒序）
//	spec 非空            → 直取该 tag 的 release（更低版本照取，支持降级；404 报未找到）
//
// latest 走 releases/latest/download 直链，指定版本走 releases/download/<tag> 直链。
func ResolveTarget(e Endpoints, spec string, prerelease bool) (*Target, error) {
	switch {
	case spec == "" && !prerelease:
		var rel ghRelease
		if err := e.getJSON(e.apiBase()+"/repos/"+repoSlug+"/releases/latest", &rel); err != nil {
			return nil, fmt.Errorf("查询 latest 失败: %w", err)
		}
		asset := e.dlBase() + "/" + repoSlug + "/releases/latest/download/" + assetName
		return &Target{Tag: rel.TagName, ExeURL: asset, ShaURL: asset + ".sha256"}, nil
	case spec == "":
		var rels []ghRelease
		if err := e.getJSON(e.apiBase()+"/repos/"+repoSlug+"/releases?per_page=30", &rels); err != nil {
			return nil, fmt.Errorf("查询 release 列表失败: %w", err)
		}
		for _, rel := range rels {
			if !rel.Draft {
				asset := e.dlBase() + "/" + repoSlug + "/releases/download/" + rel.TagName + "/" + assetName
				return &Target{Tag: rel.TagName, ExeURL: asset, ShaURL: asset + ".sha256"}, nil
			}
		}
		return nil, fmt.Errorf("无可用 release（含预发布）")
	default:
		tag := normalizeTag(spec)
		var rel ghRelease
		if err := e.getJSON(e.apiBase()+"/repos/"+repoSlug+"/releases/tags/"+url.PathEscape(tag), &rel); err != nil {
			return nil, fmt.Errorf("目标 %s: %w", tag, err)
		}
		asset := e.dlBase() + "/" + repoSlug + "/releases/download/" + rel.TagName + "/" + assetName
		return &Target{Tag: rel.TagName, ExeURL: asset, ShaURL: asset + ".sha256"}, nil
	}
}

// CheckStatus --check 结论三态。
type CheckStatus int

const (
	CheckUpdateAvailable CheckStatus = iota // 发现新版（含显式指定他版/降级）
	CheckUpToDate                           // 已是最新
	CheckDevBuild                           // 当前非 semver 版本（dev/commit 描述），无从比较
)

// CheckOutcome --check 结论；String() 为人话报告（只报告不动手）。
type CheckOutcome struct {
	Status    CheckStatus
	Current   string
	Target    string
	Downgrade bool // 目标低于当前（显式降级场景）
}

func (o *CheckOutcome) String() string {
	switch o.Status {
	case CheckDevBuild:
		return fmt.Sprintf("当前 %s 无从比较（目标 %s；dev 构建没有可比版本号）", o.Current, o.Target)
	case CheckUpToDate:
		return fmt.Sprintf("已是最新 %s", o.Target)
	default:
		if o.Downgrade {
			return fmt.Sprintf("发现 %s,当前 %s（降级）", o.Target, o.Current)
		}
		return fmt.Sprintf("发现 %s,当前 %s", o.Target, o.Current)
	}
}

// Check --check 全流程：解析目标 + 与当前版本比较，产出结论。纯只读，不下载。
// 当前版本非 semver（dev / 无 tag 的 commit 描述）→ CheckDevBuild，如实提示
// 无从比较，目标版本照报。
func Check(e Endpoints, current, spec string, prerelease bool) (*CheckOutcome, error) {
	t, err := ResolveTarget(e, spec, prerelease)
	if err != nil {
		return nil, err
	}
	cv, cerr := ParseSemver(normalizeTag(current))
	tv, terr := ParseSemver(t.Tag)
	if normalizeTag(current) == "dev" || cerr != nil || terr != nil {
		return &CheckOutcome{Status: CheckDevBuild, Current: current, Target: t.Tag}, nil
	}
	o := &CheckOutcome{Current: current, Target: t.Tag}
	switch c := tv.Compare(cv); {
	case c == 0:
		o.Status = CheckUpToDate
	case c < 0:
		o.Status = CheckUpdateAvailable
		o.Downgrade = true
	default:
		o.Status = CheckUpdateAvailable
	}
	return o, nil
}

// normalizeTag 版本参数规整：纯数字开头补 v 前缀（0.1.0 → v0.1.0）；其余
// （v0.1.0、dev、commit 描述）原样。
func normalizeTag(s string) string {
	s = strings.TrimSpace(s)
	if s != "" && s[0] >= '0' && s[0] <= '9' {
		return "v" + s
	}
	return s
}
