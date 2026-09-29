package main

// util.go — 彩排台通用助手：端口分配（生产口黑名单护栏）、仓库根定位、
// 文件哈希、隐藏拉起、原子写。平台进程面（隐藏拉起/杀进程/PID 活性）在
// util_windows.go / util_other.go（对齐 internal/update 的 proc_* 惯例）。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ferryman/internal/update"
)

// forbiddenPorts 生产与固定端口黑名单：彩排的一切端口从高位随机分配，代码
// 层面禁止出现对这些端口的任何拨号/监听。这是票05 验收「零生产端口访问，
// 装配注入可证」的机械护栏——分配器在这里把事故拦死，不靠人眼审配置。
var forbiddenPorts = map[int]bool{
	15700: true, // 生产守护管理口（update.DefaultDaemonPort 同值，不 import 以免拉依赖面）
	15721: true, // 生产上游中转口（cc-switch）
	15722: true, // 生产渡口代理面
	15723: true, // 本地中转守卫域（config.IsLocalRelayAddr 的已知口）
	15900: true, // 生产面板口
}

// isForbiddenPort 彩排禁用端口判定（测试钉死口径）。
func isForbiddenPort(p int) bool { return forbiddenPorts[p] }

// allocPort 从环回临时监听取一个空闲高位端口（先占后放，让渡给影子装配）。
// 临时分配不会落在低位固定口，命中黑名单只是防御性重试。
func allocPort() (int, error) {
	for i := 0; i < 50; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0, err
		}
		p := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		if p > 1024 && !isForbiddenPort(p) {
			return p, nil
		}
	}
	return 0, errors.New("彩排端口分配重试耗尽")
}

// findRepoRoot 向上找 module ferryman 的 go.mod。彩排要 go build 两版 exe，
// 仓库根是构建的工作目录；go run（cwd=仓库根）与 go test（cwd=tools/rehearsal）
// 两种入口都能走到。
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		b, rerr := os.ReadFile(filepath.Join(dir, "go.mod"))
		if rerr == nil && strings.Contains(string(b), "module ferryman") {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", errors.New("找不到仓库根（向上未命中 module ferryman 的 go.mod）")
}

// fileExists 盘上存在且非目录。
func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// sha256File 文件摘要（构建指纹与「盘上已恢复旧版」断言用）。
func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyFileSteno 复制文件（构建产物 → 影子 ferryman.exe；内容保真即可，
// 权限位沿用默认）。
func copyFileSteno(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// writeAtomic 临时文件+改名落位：结果 JSON 由监督者副本进程写、编排进程
// 200ms 轮询读——直接写目标路径会被读到半截 JSON。
func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// filterEnv 复制环境变量并剥掉监督者自拉起标记：蜂群注入的守护必须不带
// FERRYMAN_LAUNCHED_BY_SUPERVISOR（带标记=升级正主，让路判定会豁免——
// 那正是要注入的故障面，不能被测试进程自身的环境污染）。
func filterEnv() []string {
	out := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, update.SupervisorLaunchEnv+"=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// readDaemonPIDFile 读 <dataDir>/daemon.pid（daemon serve 写的 JSON）。
func readDaemonPIDFile(dataDir string) int {
	b, err := os.ReadFile(filepath.Join(dataDir, "daemon.pid"))
	if err != nil {
		return 0
	}
	var pj struct {
		PID int `json:"pid"`
	}
	if json.Unmarshal(b, &pj) != nil || pj.PID <= 0 {
		return 0
	}
	return pj.PID
}

// readUpdateLockPID 读 <dataDir>/update.lock 的持有者 PID（彩排兜底清场：
// 事务卡死时按锁杀监督者副本；读不到 = 0 = 无从杀）。
func readUpdateLockPID(dataDir string) int {
	b, err := os.ReadFile(filepath.Join(dataDir, "update.lock"))
	if err != nil {
		return 0
	}
	var lj struct {
		PID int `json:"pid"`
	}
	if json.Unmarshal(b, &lj) != nil || lj.PID <= 0 {
		return 0
	}
	return lj.PID
}

// isASCII 全 ASCII 判定：点火脚本铁律（非 UTF-8 代码页控制台下中文会被
// 重解码破坏 rem/行结构——installer 同款）。
func isASCII(s string) bool {
	for _, c := range s {
		if c > 0x7F {
			return false
		}
	}
	return true
}

// fmtDur 时长的结果文件渲染（秒级足够；排版统一）。
func fmtDur(d time.Duration) string { return d.Round(time.Millisecond).String() }

// jsonDecodeBody 读光并解码 JSON 响应体（RST 纪律：先读光再关）。
func jsonDecodeBody(r io.Reader, v any) error {
	b, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
