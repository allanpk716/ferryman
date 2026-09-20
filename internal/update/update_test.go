package update

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---- 伪 GitHub(httptest 回环,零外呼) ----

// fakeRel 一条 release 的造数(tag、预发布标记、资产字节)。
type fakeRel struct {
	tag   string
	pre   bool
	bytes []byte
}

// fakeGHMux 伪 GitHub 路由:latest 端点实现 GitHub 语义——排除 prerelease
// (draft 不造);列表按 rels 传入顺序(镜像 GitHub 创建时间倒序,新→旧);
// 资产直链 latest/download 与 download/<tag> 两种形态都伺服。
func fakeGHMux(rels []fakeRel) *http.ServeMux {
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /repos/"+repoSlug+"/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		for _, r := range rels {
			if !r.pre {
				writeJSON(w, ghRelease{TagName: r.tag})
				return
			}
		}
		http.NotFound(w, nil)
	})
	mux.HandleFunc("GET /repos/"+repoSlug+"/releases", func(w http.ResponseWriter, _ *http.Request) {
		out := make([]ghRelease, 0, len(rels))
		for _, r := range rels {
			out = append(out, ghRelease{TagName: r.tag, Prerelease: r.pre})
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("GET /repos/"+repoSlug+"/releases/tags/", func(w http.ResponseWriter, req *http.Request) {
		tag := strings.TrimPrefix(req.URL.Path, "/repos/"+repoSlug+"/releases/tags/")
		for _, r := range rels {
			if r.tag == tag {
				writeJSON(w, ghRelease{TagName: r.tag, Prerelease: r.pre})
				return
			}
		}
		http.NotFound(w, nil)
	})
	serveAsset := func(w http.ResponseWriter, r *fakeRel, name string) {
		switch name {
		case assetName:
			_, _ = w.Write(r.bytes)
		case assetName + ".sha256":
			fmt.Fprintf(w, "%x  %s\n", sha256.Sum256(r.bytes), assetName)
		default:
			http.NotFound(w, nil)
		}
	}
	mux.HandleFunc("GET /"+repoSlug+"/releases/latest/download/", func(w http.ResponseWriter, req *http.Request) {
		name := strings.TrimPrefix(req.URL.Path, "/"+repoSlug+"/releases/latest/download/")
		for _, r := range rels {
			if !r.pre {
				serveAsset(w, &r, name)
				return
			}
		}
		http.NotFound(w, nil)
	})
	mux.HandleFunc("GET /"+repoSlug+"/releases/download/", func(w http.ResponseWriter, req *http.Request) {
		rest := strings.TrimPrefix(req.URL.Path, "/"+repoSlug+"/releases/download/")
		tag, name, _ := strings.Cut(rest, "/")
		for _, r := range rels {
			if r.tag == tag {
				serveAsset(w, &r, name)
				return
			}
		}
		http.NotFound(w, nil)
	})
	return mux
}

// startFakeGH 明文伪 GitHub(常规用)。
func startFakeGH(t *testing.T, rels []fakeRel) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(fakeGHMux(rels))
	t.Cleanup(srv.Close)
	return srv
}

// fakeEps 指向伪 GitHub 的端点;HTTP 客户端明确不走环境代理——开发机可能设了
// 真实代理变量,除代理专项测试外一律直连回环。
func fakeEps(srv *httptest.Server) Endpoints {
	return Endpoints{APIBase: srv.URL, DLBase: srv.URL,
		HTTP: &http.Client{Transport: &http.Transport{}}}
}

// assetURL 指定 tag 的 exe/校验文件直链(测试内拼 URL 用)。
func assetURL(base, tag, name string) string {
	return base + "/" + repoSlug + "/releases/download/" + tag + "/" + name
}

// ---- 目标版本解析 ----

// TestResolveLatestExcludesPrerelease latest 走稳定版(排除 prerelease);
// --prerelease 走列表接口,rc 可见且直链换成 download/<tag>。
func TestResolveLatestExcludesPrerelease(t *testing.T) {
	rc := fakeRel{tag: "v0.2.0-rc.1", pre: true, bytes: []byte("rc")}
	stable := fakeRel{tag: "v0.1.0", bytes: []byte("stable")}
	srv := startFakeGH(t, []fakeRel{rc, stable}) // 新→旧
	eps := fakeEps(srv)

	got, err := ResolveTarget(eps, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tag != "v0.1.0" {
		t.Fatalf("latest 应排除 prerelease 取 v0.1.0, got %s", got.Tag)
	}
	wantExe := srv.URL + "/" + repoSlug + "/releases/latest/download/" + assetName
	if got.ExeURL != wantExe {
		t.Fatalf("latest 直链 = %s, want %s", got.ExeURL, wantExe)
	}
	if got.ShaURL != wantExe+".sha256" {
		t.Fatalf("校验文件直链 = %s, want %s", got.ShaURL, wantExe+".sha256")
	}

	pre, err := ResolveTarget(eps, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if pre.Tag != "v0.2.0-rc.1" {
		t.Fatalf("--prerelease 应见到 rc, got %s", pre.Tag)
	}
	if want := assetURL(srv.URL, "v0.2.0-rc.1", assetName); pre.ExeURL != want {
		t.Fatalf("prerelease 直链 = %s, want %s", pre.ExeURL, want)
	}
}

// TestResolveExplicitIncludingLower 显式版本直取该 release,更低版本(降级)
// 照样可解析;未知 tag 报「未找到」。
func TestResolveExplicitIncludingLower(t *testing.T) {
	newer := fakeRel{tag: "v0.2.0", bytes: []byte("new")}
	older := fakeRel{tag: "v0.1.0", bytes: []byte("old")}
	srv := startFakeGH(t, []fakeRel{newer, older})
	eps := fakeEps(srv)

	got, err := ResolveTarget(eps, "v0.1.0", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tag != "v0.1.0" {
		t.Fatalf("显式更低版本应照取 v0.1.0, got %s", got.Tag)
	}
	if want := assetURL(srv.URL, "v0.1.0", assetName); got.ExeURL != want {
		t.Fatalf("显式版本直链 = %s, want %s", got.ExeURL, want)
	}

	noV, err := ResolveTarget(eps, "0.1.0", false)
	if err != nil || noV.Tag != "v0.1.0" {
		t.Fatalf("裸 0.1.0 应规整为 v0.1.0: %v %+v", err, noV)
	}

	if _, err := ResolveTarget(eps, "v9.9.9", false); err == nil || !strings.Contains(err.Error(), "未找到") {
		t.Fatalf("未知 tag 应报未找到, got %v", err)
	}
}

// ---- --check 报告(人话,只报告不动手) ----

func TestCheckReporting(t *testing.T) {
	rc := fakeRel{tag: "v0.3.0-rc.1", pre: true, bytes: []byte("rc")}
	stable := fakeRel{tag: "v0.2.0", bytes: []byte("stable")}
	older := fakeRel{tag: "v0.1.0", bytes: []byte("old")}
	srv := startFakeGH(t, []fakeRel{rc, stable, older})
	eps := fakeEps(srv)

	// 当前 dev:无从比较,但目标照报
	o, err := Check(eps, "dev", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != CheckDevBuild {
		t.Fatalf("dev 应判无从比较, got %v", o.Status)
	}
	if s := o.String(); !strings.Contains(s, "当前 dev 无从比较") || !strings.Contains(s, "v0.2.0") {
		t.Fatalf("dev 报告 = %q, want 含「当前 dev 无从比较」与目标版本", s)
	}

	// 已是最新
	o, err = Check(eps, "v0.2.0", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != CheckUpToDate {
		t.Fatalf("同版应判已是最新, got %v", o.Status)
	}
	if s := o.String(); s != "已是最新 v0.2.0" {
		t.Fatalf("同版报告 = %q, want「已是最新 v0.2.0」", s)
	}

	// 发现新版
	o, err = Check(eps, "v0.1.0", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != CheckUpdateAvailable || o.Downgrade {
		t.Fatalf("应判发现新版, got %v downgrade=%v", o.Status, o.Downgrade)
	}
	if s := o.String(); s != "发现 v0.2.0,当前 v0.1.0" {
		t.Fatalf("新版报告 = %q, want「发现 v0.2.0,当前 v0.1.0」", s)
	}

	// 显式降级:注明降级
	o, err = Check(eps, "v0.2.0", "v0.1.0", false)
	if err != nil {
		t.Fatal(err)
	}
	if !o.Downgrade {
		t.Fatalf("目标更低应注明降级: %+v", o)
	}
	if s := o.String(); !strings.Contains(s, "发现 v0.1.0,当前 v0.2.0") || !strings.Contains(s, "降级") {
		t.Fatalf("降级报告 = %q", s)
	}

	// --prerelease 检查:rc 可见
	o, err = Check(eps, "v0.2.0", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if o.Target != "v0.3.0-rc.1" || o.Status != CheckUpdateAvailable {
		t.Fatalf("--prerelease 应发现 v0.3.0-rc.1: %+v", o)
	}
}

// ---- 最小 semver 比较 ----

func TestParseSemverCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int // Compare(a,b) 的符号
	}{
		{"v1.2.3", "v1.2.3", 0},
		{"v1.2.3", "v1.2.3-rc.1", 1},  // 正式 > 预发布
		{"v1.2.3-rc.2", "v1.2.3-rc.1", 1},
		{"v1.2.3-1", "v1.2.3-rc", -1}, // 数字段 < 字母段
		{"v0.2.0", "v0.1.9", 1},
		{"v1.0.0-alpha.beta", "v1.0.0-alpha.1", 1},
		{"v1.0.0-rc.1", "v1.0.0-rc.1.x", -1}, // 少段 < 多段
		{"0.2.0", "v0.1.9", 1},
	}
	for _, c := range cases {
		a, err := ParseSemver(c.a)
		if err != nil {
			t.Fatalf("ParseSemver(%q): %v", c.a, err)
		}
		b, err := ParseSemver(c.b)
		if err != nil {
			t.Fatalf("ParseSemver(%q): %v", c.b, err)
		}
		got := sign(a.Compare(b))
		if got != c.want {
			t.Fatalf("Compare(%s,%s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	for _, bad := range []string{"dev", "v1.2", "e8aea1f", "vx.y.z"} {
		if _, err := ParseSemver(bad); err == nil {
			t.Fatalf("ParseSemver(%q) 应报错", bad)
		}
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
