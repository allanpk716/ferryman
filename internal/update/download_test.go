package update

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestDownloadVerifyOK 正常链:拉 .sha256 → 下载 → SHA256 相符 → 落位,
// 旁路 .part 不残留。
func TestDownloadVerifyOK(t *testing.T) {
	body := []byte("fake-exe-bytes-123")
	sum := sha256.Sum256(body)
	srv := startFakeGH(t, []fakeRel{{tag: "v0.1.0", bytes: body}})
	eps := fakeEps(srv)

	sha, err := eps.FetchSHA256(assetURL(srv.URL, "v0.1.0", assetName+".sha256"))
	if err != nil {
		t.Fatal(err)
	}
	if sha != hex.EncodeToString(sum[:]) {
		t.Fatalf("FetchSHA256 = %s, want %s", sha, hex.EncodeToString(sum[:]))
	}

	dest := filepath.Join(t.TempDir(), "ferryman.exe.new")
	if err := DownloadToFile(eps.HTTP, assetURL(srv.URL, "v0.1.0", assetName), sha, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatalf("落位内容不符: got %q", got)
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Fatalf("旁路 .part 应已改名落位, stat err = %v", err)
	}
}

// TestDownloadSHAMismatchLeavesNothing 校验不过:明确报错、不落位、半成品删除。
func TestDownloadSHAMismatchLeavesNothing(t *testing.T) {
	srv := startFakeGH(t, []fakeRel{{tag: "v0.1.0", bytes: []byte("real-bytes")}})
	eps := fakeEps(srv)
	dest := filepath.Join(t.TempDir(), "ferryman.exe.new")

	bad := strings.Repeat("0", 64)
	err := DownloadToFile(eps.HTTP, assetURL(srv.URL, "v0.1.0", assetName), bad, dest)
	if err == nil || !strings.Contains(err.Error(), "SHA256") {
		t.Fatalf("应报 SHA256 校验不符, got %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("校验不过不得落位, stat err = %v", err)
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Fatalf("半成品 .part 必须删除, stat err = %v", err)
	}
}

// TestDownloadViaHTTPSProxy 下载走 HTTPS_PROXY（D8）：httptest 同时演源站（TLS）
// 与代理桩；桩收到 CONNECT 且打通隧道才算过——若客户端直连会绕开桩，断言即失败。
// 注意：net/http 的代理解析对回环/localhost 目标一律直连（硬编码），所以源站
// 对外用假域名（客户端只把它放进 CONNECT，解析在桩里落到真实回环源站）。
func TestDownloadViaHTTPSProxy(t *testing.T) {
	body := []byte("proxied-exe")
	origin := httptest.NewTLSServer(fakeGHMux([]fakeRel{{tag: "v0.1.0", bytes: body}}))
	t.Cleanup(origin.Close)
	_, port, _ := net.SplitHostPort(origin.Listener.Addr().String())
	const fakeHost = "ferryman-release.test" // 非回环假名，否则代理解析跳过
	base := "https://" + net.JoinHostPort(fakeHost, port)

	var mu sync.Mutex
	var seen []string // 代理桩收到的 CONNECT 目标
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if r.Method == http.MethodConnect {
			seen = append(seen, "CONNECT "+r.Host)
		} else {
			seen = append(seen, "GET "+r.Host+r.URL.Path)
		}
		mu.Unlock()
		if r.Method != http.MethodConnect {
			http.Error(w, "代理桩只伺服 CONNECT", http.StatusMethodNotAllowed)
			return
		}
		// 打通隧道：200 后双向拼接；假域名解析成真实回环源站
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
			return
		}
		up, err := net.DialTimeout("tcp", origin.Listener.Addr().String(), 5*time.Second)
		if err != nil {
			return
		}
		defer up.Close()
		go func() { _, _ = io.Copy(up, buf); _ = up.Close() }()
		_, _ = io.Copy(conn, up)
	}))
	t.Cleanup(proxy.Close)

	// 代理只认环境变量（D8）：HTTPS_PROXY 指向桩；清掉 NO_PROXY 免得本机
	// 环境把目标排除在代理外。
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("https_proxy", proxy.URL)
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	// 传输层代理解析走 ProxyFromEnvironment（被测机制）；InsecureSkipVerify
	// 只为容忍假域名与自签证书——本测试验证代理路由，不验证证书链。
	hc := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	eps := Endpoints{APIBase: base, DLBase: base, HTTP: hc}

	sha, err := eps.FetchSHA256(assetURL(base, "v0.1.0", assetName+".sha256"))
	if err != nil {
		t.Fatalf("经代理拉校验文件失败: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "out.exe")
	if err := DownloadToFile(hc, assetURL(base, "v0.1.0", assetName), sha, dest); err != nil {
		t.Fatalf("经代理下载失败: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("代理桩没收到任何请求——下载没走 HTTPS_PROXY")
	}
	wantHost := net.JoinHostPort(fakeHost, port)
	for _, s := range seen {
		if strings.Contains(s, wantHost) {
			// 内容经隧道无损
			got, err := os.ReadFile(dest)
			if err != nil || string(got) != string(body) {
				t.Fatalf("隧道内容不符: %q err=%v", got, err)
			}
			return
		}
	}
	t.Fatalf("代理桩收到 %v, want 含目标 %s 的 CONNECT", seen, wantHost)
}
