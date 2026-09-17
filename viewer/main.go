// ferryman/viewer——账本时间线查看器（T43）。
// 本文件是最小骨架：仅解析 flags、监听并占住端口；真路由在 Task 3 接入。
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
)

func main() {
	data := flag.String("data", "", "账本数据目录（默认 $FERRYMAN_DATA 或 ~/ferryman）")
	port := flag.Int("port", 0, "监听端口（0=随机）")
	flag.Parse()
	dir := resolveDataDir(*data)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("时间线查看器: http://%s （数据目录 %s，Ctrl+C 退出）\n", ln.Addr(), dir)
	log.Fatal(http.Serve(ln, http.NotFoundHandler())) // Task 3 换真路由
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
