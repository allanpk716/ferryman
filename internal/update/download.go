package update

// 资产下载与 SHA256 校验（D9：校验不过不落位、不留半文件）。下载函数供监督者
// 票05 复用：目标 URL + 期望摘要 + 落点。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// defaultHTTPClient 内建客户端：D8——代理只认环境变量（HTTP_PROXY/HTTPS_PROXY/
// NO_PROXY 标准语义），不读任何配置。net/http 进程内只解析一次代理环境变量。
var defaultHTTPClient = &http.Client{
	Transport: &http.Transport{Proxy: http.ProxyFromEnvironment},
	Timeout:   10 * time.Minute, // 覆盖慢代理拉整个 exe；检查/下载共用
}

// FetchSHA256 拉取 .sha256 资产，返回十六进制摘要（sha256sum 产出格式
// 「<hash>␠␠<文件名>」，取首字段）。
func (e Endpoints) FetchSHA256(shaURL string) (string, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, shaURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := e.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("拉取校验文件 %s: HTTP %d", shaURL, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return "", fmt.Errorf("校验文件为空: %s", shaURL)
	}
	sum := strings.ToLower(fields[0])
	if len(sum) != 64 || !isHex(sum) {
		return "", fmt.Errorf("校验文件格式不对（首字段应为 64 位十六进制）: %s", shaURL)
	}
	return sum, nil
}

// DownloadToFile 下载 exeURL 到 destPath：先写旁路 destPath+".part" 边下边算
// SHA256，与 wantSHA256 一致才改名落位；不符或中途失败都删旁路文件——不留
// 半成品。hc 空 = 内建环境代理客户端（D8）。
func DownloadToFile(hc *http.Client, exeURL, wantSHA256, destPath string) error {
	c := hc
	if c == nil {
		c = defaultHTTPClient
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, exeURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 %s: HTTP %d", exeURL, resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	part := destPath + ".part"
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(part)
		return fmt.Errorf("下载中断: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(part)
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, wantSHA256) {
		_ = os.Remove(part)
		return fmt.Errorf("SHA256 校验不符: 期望 %s 实际 %s（半成品已删: %s）", wantSHA256, got, part)
	}
	if err := os.Rename(part, destPath); err != nil {
		_ = os.Remove(part)
		return fmt.Errorf("落位失败: %w", err)
	}
	return nil
}

// isHex 全串小写十六进制判定（FetchSHA256 已 ToLower）。
func isHex(s string) bool {
	for _, c := range s {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}
