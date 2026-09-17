// ferryman/viewer——账本时间线查看器（T43）。
// 解析 flags → 解析数据目录（数据根自动探 accounts/）→ 组装路由（JSON API + favicon +
// 嵌入的静态页）→ 127.0.0.1 监听 → 打印 URL → 自动开浏览器（可 --no-browser 关）→ Serve。
package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"ferryman/viewer/internal/server"
)

//go:embed web
var webFS embed.FS

// faviconSVG 内联图标（暗底小帆船，呼应"摆渡人"）：Go 端伺服 /favicon.svg 免 404。
const faviconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">` +
	`<rect width="32" height="32" rx="6" fill="#111111"/>` +
	`<line x1="16" y1="6" x2="16" y2="21" stroke="#dddddd" stroke-width="1.6"/>` +
	`<path d="M17 8 L24 19 L17 19 Z" fill="#64b5f6"/>` +
	`<path d="M15 10 L10 19 L15 19 Z" fill="#4caf50"/>` +
	`<path d="M5 22 H27 L23 27 H9 Z" fill="#ffb74d"/>` +
	`</svg>`

func main() {
	data := flag.String("data", "", "账本目录或数据根（默认 $FERRYMAN_DATA 或 ~/ferryman；根下无 *.jsonl 而有 accounts/ 时自动下钻）")
	port := flag.Int("port", 0, "监听端口（0=随机）")
	noBrowser := flag.Bool("no-browser", false, "启动后不自动打开浏览器")
	flag.Parse()
	dir := resolveDataDir(*data)

	mux := server.New(dir).Routes()
	mux.HandleFunc("GET /favicon.svg", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "max-age=86400")
		_, _ = w.Write([]byte(faviconSVG))
	})
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		log.Fatal(err)
	}
	url := fmt.Sprintf("http://%s", ln.Addr())
	fmt.Printf("时间线查看器: %s （数据目录 %s，Ctrl+C 退出）\n", url, dir)
	if !*noBrowser {
		openBrowser(url)
	}
	// ReadHeaderTimeout 防 slowloris 式慢握手占死连接（本服务只听本机回环，纵深防御）。
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.Serve(ln))
}

// openBrowser 按 GOOS 起系统默认浏览器；起不起来都不影响服务，错误忽略。
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// resolveDataDir 解析账本目录。三种来源（--data / $FERRYMAN_DATA / ~/ferryman）都按
// "数据根"语义对待：目录本身没有 *.jsonl 而其下有 accounts/ 子目录时自动下钻一层；
// 直接传 accounts 目录（或任何已含 *.jsonl 的目录）原样使用。其余情况原样返回，
// 交由服务端按「数据目录不存在」提示。
func resolveDataDir(flagVal string) string {
	dir := flagVal
	if dir == "" {
		dir = os.Getenv("FERRYMAN_DATA")
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "ferryman" // 拿不到家目录时退相对路径，仅提示性
		}
		dir = home + "/ferryman"
	}
	if hasJSONL(dir) {
		return dir
	}
	if st, err := os.Stat(filepath.Join(dir, "accounts")); err == nil && st.IsDir() {
		return filepath.Join(dir, "accounts")
	}
	return dir
}

// hasJSONL 目录下（不含子目录）是否存在至少一个 *.jsonl。
func hasJSONL(dir string) bool {
	des, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, de := range des {
		if !de.IsDir() && strings.HasSuffix(de.Name(), ".jsonl") {
			return true
		}
	}
	return false
}
