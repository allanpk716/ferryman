// balance_test.go — 票07：智谱余额只读查询验收钉子。
//
// 覆盖票面验收：
//   - mock 上游 2xx JSON → 解析出余额（data 包裹形/顶层形；数值与数值串通吃，
//     字面量原样保留）；Authorization 必为 Bearer <真钥>；
//   - 401/超时/断连/坏 JSON/缺字段/非数值 → 类别错误，错误串永不携带真钥
//     （防泄漏钉子，T39）；
//   - 未配置（无 [dock]/无 api_key）→ 未配置哨兵且零 HTTP（计数器断言）；
//   - URL 覆写生效（请求落到覆写端点）；默认端点单源解析；
//   - BalanceInfo 结构即白名单（只有数值/时间两字段，响应多余内容不落）。
package dock

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ferryman/internal/config"
)

// balanceTestKey 假真钥：各类别用例断言错误串不含它（防泄漏钉子）。
const balanceTestKey = "sk-test-real-key-07"

// assertNoKey 断言错误串不携带假真钥（票据验收：无真钥泄漏）。
func assertNoKey(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), balanceTestKey) {
		t.Fatalf("错误串泄漏真钥: %q", err.Error())
	}
}

func TestBalanceParseDataWrapped(t *testing.T) {
	// bigmodel 惯例的 data 包裹形；usage/currency 等多余字段必须不落。
	var gotAuth, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		_, _ = w.Write([]byte(`{"success":true,"code":200,"msg":"ok",` +
			`"data":{"balance":"310.00","balance_int":310,"currency":"CNY",` +
			`"usage":{"pcm":1},"expire":"2026-12-31"}}`))
	}))
	defer srv.Close()

	info, err := FetchBalance(&config.DockCfg{APIKey: balanceTestKey, BalanceURL: srv.URL})
	if err != nil {
		t.Fatalf("FetchBalance: %v", err)
	}
	if info.Balance != "310.00" || info.Expire != "2026-12-31" {
		t.Fatalf("info = %+v, want balance=310.00 expire=2026-12-31", info)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("method = %s, want GET", gotMethod)
	}
	if gotAuth != "Bearer "+balanceTestKey {
		t.Fatalf("Authorization = %q, want Bearer <真钥>", gotAuth)
	}
	// 结构即白名单：只有数值/时间两字段（票07：响应只取数值/时间字段）
	if n := reflect.TypeOf(BalanceInfo{}).NumField(); n != 2 {
		t.Fatalf("BalanceInfo 字段数 = %d, want 2", n)
	}
}

func TestBalanceParseTopLevelNumber(t *testing.T) {
	// 顶层形 + JSON 数值字面量：UseNumber 保留 "123.45"/"1758000000" 原样。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"balance": 123.45, "expire": 1758000000}`))
	}))
	defer srv.Close()

	info, err := FetchBalance(&config.DockCfg{APIKey: balanceTestKey, BalanceURL: srv.URL})
	if err != nil {
		t.Fatalf("FetchBalance: %v", err)
	}
	if info.Balance != "123.45" || info.Expire != "1758000000" {
		t.Fatalf("info = %+v, want balance=123.45 expire=1758000000", info)
	}
}

func TestBalanceNotConfiguredZeroHTTP(t *testing.T) {
	// 无 [dock]（nil）/有节无钥 → 未配置哨兵，且零 HTTP（计数器钉死）。
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	for i, dk := range []*config.DockCfg{
		nil,
		{},
		{BalanceURL: srv.URL}, // 有 URL 无钥：仍不得发请求
	} {
		info, err := FetchBalance(dk)
		if !errors.Is(err, ErrBalanceNotConfigured) {
			t.Fatalf("用例 %d: err = %v, want 未配置哨兵", i, err)
		}
		if info != (BalanceInfo{}) {
			t.Fatalf("用例 %d: info = %+v, want 零值", i, info)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("未配置路径发出 %d 次 HTTP, want 0", hits.Load())
	}
}

func TestBalanceEndpointDefaultAndOverride(t *testing.T) {
	// 默认内置（单源 config.DefaultDockBalanceURL）；覆写原样保留。
	if got := balanceEndpoint(&config.DockCfg{APIKey: "k"}); got != config.DefaultDockBalanceURL {
		t.Fatalf("endpoint = %q, want 内置默认", got)
	}
	const ov = "http://127.0.0.1:19999/api/user/balance"
	if got := balanceEndpoint(&config.DockCfg{APIKey: "k", BalanceURL: ov}); got != ov {
		t.Fatalf("endpoint = %q, want 覆写值", got)
	}
	// 空白串视同未配置端点 → 回落默认（宽松容错）。
	if got := balanceEndpoint(&config.DockCfg{APIKey: "k", BalanceURL: "   "}); got != config.DefaultDockBalanceURL {
		t.Fatalf("endpoint = %q, want 空白回落默认", got)
	}
}

func TestBalanceErrorCategoriesNoKeyLeak(t *testing.T) {
	t.Run("上游401", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
		}))
		defer srv.Close()

		_, err := FetchBalance(&config.DockCfg{APIKey: balanceTestKey, BalanceURL: srv.URL})
		if err == nil || err.Error() != "上游状态 401" {
			t.Fatalf("err = %v, want 上游状态 401", err)
		}
		assertNoKey(t, err)
	})

	t.Run("超时", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(400 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		c := &http.Client{Timeout: 80 * time.Millisecond}
		_, err := FetchBalanceWithClient(c, &config.DockCfg{APIKey: balanceTestKey, BalanceURL: srv.URL})
		if err == nil || err.Error() != "网络错误（超时）" {
			t.Fatalf("err = %v, want 网络错误（超时）", err)
		}
		assertNoKey(t, err)
	})

	t.Run("断连", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		u, err := url.Parse(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		srv.Close() // 先拆监听 → 连接拒绝

		_, err = FetchBalance(&config.DockCfg{APIKey: balanceTestKey, BalanceURL: "http://" + u.Host + "/x"})
		if err == nil || err.Error() != "网络错误" {
			t.Fatalf("err = %v, want 网络错误", err)
		}
		assertNoKey(t, err)
	})

	t.Run("坏JSON", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`not-json`))
		}))
		defer srv.Close()

		_, err := FetchBalance(&config.DockCfg{APIKey: balanceTestKey, BalanceURL: srv.URL})
		if err == nil || err.Error() != "响应解析失败" {
			t.Fatalf("err = %v, want 响应解析失败", err)
		}
		assertNoKey(t, err)
	})

	t.Run("缺balance字段", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"data":{}}`))
		}))
		defer srv.Close()

		_, err := FetchBalance(&config.DockCfg{APIKey: balanceTestKey, BalanceURL: srv.URL})
		if err == nil || err.Error() != "响应解析失败" {
			t.Fatalf("err = %v, want 响应解析失败", err)
		}
	})

	t.Run("balance非数值", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"balance":{"x":1}}`))
		}))
		defer srv.Close()

		_, err := FetchBalance(&config.DockCfg{APIKey: balanceTestKey, BalanceURL: srv.URL})
		if err == nil || err.Error() != "响应解析失败" {
			t.Fatalf("err = %v, want 响应解析失败", err)
		}
		assertNoKey(t, err)
	})
}
