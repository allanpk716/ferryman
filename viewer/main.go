// ferryman/viewer——账本时间线查看器（T43）。
// 解析 flags → 组装路由（JSON API + 嵌入的静态页）→ 127.0.0.1 监听 → 打印 URL →
// 自动开浏览器（可 --no-browser 关）→ Serve。
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
	"runtime"

	"ferryman/viewer/internal/server"
)

//go:embed web
var webFS embed.FS

func main() {
	data := flag.String("data", "", "账本数据目录（默认 $FERRYMAN_DATA 或 ~/ferryman）")
	port := flag.Int("port", 0, "监听端口（0=随机）")
	noBrowser := flag.Bool("no-browser", false, "启动后不自动打开浏览器")
	flag.Parse()
	dir := resolveDataDir(*data)

	mux := server.New(dir).Routes()
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
	log.Fatal(http.Serve(ln, mux))
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

func resolveDataDir(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if env := os.Getenv("FERRYMAN_DATA"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "ferryman" // 拿不到家目录时退相对路径，仅提示性
	}
	return home + "/ferryman"
}
