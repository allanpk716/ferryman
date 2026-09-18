// ferryman cmd/viewer——账本时间线查看器（T43；T47 托盘+配置页）。
// 解析 flags → 解析数据目录（数据根自动探 accounts/）→ 组装路由（JSON API + favicon +
// 嵌入的静态页）→ 127.0.0.1 监听 → 打印 URL → 自动开浏览器（可 --no-browser 关）→
// 常驻系统托盘（帆船图标：菜单「打开面板/退出」；--no-tray 关）。
// 端口被占且探到 /api/sessions 活着 = 面板已在跑 → 直接开浏览器退出
//（桌面/任务栏快捷方式因此"点一下必达面板"，无论跑没跑）。
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

	"github.com/getlantern/systray"

	"ferryman/internal/viewer/demo"
	"ferryman/internal/viewer/server"
)

//go:embed web
var webFS embed.FS

//go:embed icon.ico
var iconICO []byte

// faviconSVG 内联图标（暗底小帆船，呼应"摆渡人"）：Go 端伺服 /favicon.svg 免 404。
const faviconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">` +
	`<rect width="32" height="32" rx="6" fill="#111111"/>` +
	`<line x1="16" y1="6" x2="16" y2="21" stroke="#dddddd" stroke-width="1.6"/>` +
	`<path d="M17 8 L24 19 L17 19 Z" fill="#64b5f6"/>` +
	`<path d="M15 10 L10 19 L15 19 Z" fill="#4caf50"/>` +
	`<path d="M5 22 H27 L23 27 H9 Z" fill="#ffb74d"/>` +
	`</svg>`

// defaultShortcutPort 快捷方式固定端口：默认 15900（port=0 随机口会让"第二实例
// 探测转开浏览器"的短路逻辑失灵——快捷方式必须钉死端口）。
const defaultShortcutPort = 15900

func main() {
	data := flag.String("data", "", "账本目录或数据根（默认 $FERRYMAN_DATA 或 ~/ferryman；根下无 *.jsonl 而有 accounts/ 时自动下钻）")
	port := flag.Int("port", 0, "监听端口（0=随机）")
	noBrowser := flag.Bool("no-browser", false, "启动后不自动打开浏览器")
	isDemo := flag.Bool("demo", false, "演示模式：加载确定性合成账本（非真实数据），忽略 --data")
	noTray := flag.Bool("no-tray", false, "不建系统托盘图标（无界面环境/服务化运行）")
	install := flag.Bool("install-shortcuts", false, "创建桌面+开始菜单快捷方式后退出（Windows；快捷方式钉 15900 口）")
	flag.Parse()

	if *install {
		pin := *port
		if pin == 0 {
			pin = defaultShortcutPort
		}
		exe, err := os.Executable()
		if err != nil {
			log.Fatal(err)
		}
		if err := installShortcuts(exe, pin); err != nil {
			log.Fatal(err)
		}
		return
	}

	// --demo 独占数据源：忽略 --data/$FERRYMAN_DATA，合成账本写进一次性临时目录，
	// 绝不碰真实账本；目录留给系统临时区清理（查看器只读，进程期间无人回收它）。
	var dir string
	if *isDemo {
		d, err := os.MkdirTemp("", "ferryman-demo-")
		if err != nil {
			log.Fatal(err)
		}
		if _, err := demo.Write(d, demoBase(time.Now())); err != nil {
			log.Fatal(err)
		}
		dir = d
	} else {
		dir = resolveDataDir(*data)
	}

	srv := server.New(dir)
	if *isDemo {
		srv.SetNote("演示模式：当前数据为合成账本（含未来心跳事件的预演），非真实流水")
	}
	mux := srv.Routes()
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
		// 固定口被占：探得到 /api/sessions = 面板已在跑 → 开浏览器完事（快捷方式语义）
		url := fmt.Sprintf("http://127.0.0.1:%d", *port)
		if *port != 0 && probeViewer(url) {
			fmt.Println("面板已在运行，直接打开：", url)
			openBrowser(url)
			return
		}
		log.Fatal(err)
	}
	url := fmt.Sprintf("http://%s", ln.Addr())
	if *isDemo {
		fmt.Printf("时间线查看器(演示模式): %s （合成数据目录 %s）\n", url, dir)
	} else {
		fmt.Printf("时间线查看器: %s （数据目录 %s）\n", url, dir)
	}
	if *noTray {
		fmt.Println("（--no-tray：无托盘，Ctrl+C 退出）")
	} else {
		fmt.Println("（托盘图标常驻：菜单可打开面板/退出）")
	}
	if !*noBrowser {
		openBrowser(url)
	}
	// ReadHeaderTimeout 防 slowloris 式慢握手占死连接（本服务只听本机回环，纵深防御）。
	httpsrv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if *noTray {
		log.Fatal(httpsrv.Serve(ln)) // 前台阻塞，Ctrl+C 即退
	}
	go func() { log.Fatal(httpsrv.Serve(ln)) }()
	systray.Run(func() { trayReady(url) }, func() {})
}

// trayReady 托盘就绪：帆船图标 + 面板地址 tooltip + 菜单（打开面板/退出）。
func trayReady(url string) {
	systray.SetIcon(iconICO)
	systray.SetTooltip("Ferryman 时间线 · " + url)
	open := systray.AddMenuItem("打开面板", url)
	systray.AddSeparator()
	quit := systray.AddMenuItem("退出", "关闭查看器（面板随之不可访问）")
	go func() {
		for {
			select {
			case <-open.ClickedCh:
				openBrowser(url)
			case <-quit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

// probeViewer 探测 url 上是否活着一个本查看器（800ms 超时）。判定 /api/sessions
// 回 200 即认——够区分"自己在跑"与"端口被无关程序占用"。
func probeViewer(url string) bool {
	c := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := c.Get(url + "/api/sessions")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// openBrowser 按 GOOS 起系统默认浏览器；起不起来都不影响服务，错误忽略。
// Windows 用 rundll32 而非 `cmd /c start`：GUI 子系统（-H windowsgui 构建）下
// cmd 会闪黑窗，rundll32 不会。
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// installShortcuts 用 PowerShell 的 WScript.Shell COM 建 .lnk：桌面 + 开始菜单，
// 名「Ferryman 面板」，目标 exe --port <pin>（点击 = 未跑则起面板+开浏览器，
// 已跑则探测转开浏览器后退出），图标用 exe 同目录 icon.ico（缺则 exe 默认图标）。
func installShortcuts(exe string, pin int) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("--install-shortcuts 仅支持 Windows")
	}
	exeDir := filepath.Dir(exe)
	icon := filepath.Join(exeDir, "icon.ico")
	if _, err := os.Stat(icon); err != nil {
		icon = exe // 没带 icon.ico 就退 exe 默认图标（难看但能跑）
	}
	args := fmt.Sprintf("--port %d", pin)
	ps := fmt.Sprintf(`$ws = New-Object -ComObject WScript.Shell
$name = 'Ferryman 面板.lnk'
foreach ($dir in @([Environment]::GetFolderPath('Desktop'), [Environment]::GetFolderPath('Programs'))) {
  $lnk = $ws.CreateShortcut((Join-Path $dir $name))
  $lnk.TargetPath = '%s'
  $lnk.Arguments = '%s'
  $lnk.WorkingDirectory = '%s'
  $lnk.IconLocation = '%s'
  $lnk.Description = 'Ferryman 时间线查看器：点击打开数据面板（未运行则自动启动）'
  $lnk.Save()
  Write-Output (Join-Path $dir $name)
}`, filepath.ToSlash(exe), args, filepath.ToSlash(exeDir), filepath.ToSlash(icon))
	out, err := exec.Command("powershell", "-NoProfile", "-Command", ps).CombinedOutput()
	if err != nil {
		return fmt.Errorf("创建快捷方式失败: %v\n%s", err, strings.TrimSpace(string(out)))
	}
	fmt.Println("已创建快捷方式：")
	fmt.Print(string(out))
	fmt.Printf("（目标 %s %s；可右键 .lnk 选「固定到任务栏」）\n", exe, args)
	return nil
}

// demoBase 取演示时间锚：今天本地 09:00，未到 09:00 则取昨日——剧本最晚事件在
// base+101min，锚定 09:00 既让整条时间线落在白天，也保证所有时间戳都已过去
//（演示里冒出"未来"的时间戳会露馅）。
func demoBase(now time.Time) time.Time {
	b := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, now.Location())
	if b.After(now) {
		b = b.AddDate(0, 0, -1)
	}
	return b
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
