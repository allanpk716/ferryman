package update

// 替身守护(票05 验收:「测试里的 daemon 一律用测试内编译的替身 exe,随机口、
// TEMP 数据目录;不碰生产 7311 与仓库根 exe」)。
//
// TestDaemonStandin 以 `exe -test.run=TestDaemonStandin` 形态被拉起(exe =
// 测试二进制的副本,盘上文件尾部追加版本标记);env 开关门控,正常 go test
// 跑到它只是 Skip。协议对齐 daemon 面:GET /stats(Bearer)报 version;
// POST /shutdown 优雅退(NO_SHUTDOWN=1 时 404——模拟端点不可达)。
// daemon.pid 写入 env 指定数据目录(与生产 daemon 同位)。

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// standin 环境变量名(经 start-daemon.cmd 的 set 行传入)。
const (
	standinEnv         = "FERRYMAN_UPDATE_STANDIN"
	standinEnvPort     = "FERRYMAN_UPDATE_STANDIN_PORT"
	standinEnvToken    = "FERRYMAN_UPDATE_STANDIN_TOKEN"
	standinEnvDataDir  = "FERRYMAN_UPDATE_STANDIN_DATADIR"
	standinEnvNoShut   = "FERRYMAN_UPDATE_STANDIN_NO_SHUTDOWN"
	standinEnvVersion  = "FERRYMAN_UPDATE_STANDIN_VERSION"
	standinVerMagic    = "\n@@FERRYMAN-FAKEVER:"
	standinTestPattern = "TestDaemonStandin"
)

// TestDaemonStandin 替身本体:env 未开门 → Skip(正常测试跑无害)。
func TestDaemonStandin(t *testing.T) {
	if os.Getenv(standinEnv) != "1" {
		t.Skip("替身模式未开门")
	}
	port := os.Getenv(standinEnvPort)
	token := os.Getenv(standinEnvToken)
	dataDir := os.Getenv(standinEnvDataDir)
	noShutdown := os.Getenv(standinEnvNoShut) == "1"

	// daemon.pid(生产同位:<dataDir>/daemon.pid,pid+port)
	if dataDir != "" {
		_ = os.MkdirAll(dataDir, 0o755)
		pidb, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "port": port})
		_ = os.WriteFile(filepath.Join(dataDir, "daemon.pid"), pidb, 0o644)
	}

	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return false
		}
		return true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"version": standinSelfVersion()})
	})
	mux.HandleFunc("POST /shutdown", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		if noShutdown { // 模拟端点不可达(老版本/非 Ferryman 占口者)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
		_ = http.NewResponseController(w).Flush()
		time.Sleep(150 * time.Millisecond) // 响应先出网再退(RST 纪律)
		os.Exit(0)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		os.Exit(1) // 端口被占:模拟 daemon 唯一化跳过(退出 0 语义),非 0 便于排障
	}
	_ = http.Serve(ln, mux)
	os.Exit(0)
}

// standinSelfVersion 版本读自身 exe 尾部标记(buildStandinExe 追加);
// 无标记回落 env(直启场景),再回落占位。
func standinSelfVersion() string {
	if exe, err := os.Executable(); err == nil {
		if data, err := os.ReadFile(exe); err == nil {
			if i := bytes.LastIndex(data, []byte(standinVerMagic)); i >= 0 {
				rest := data[i+len(standinVerMagic):]
				if j := bytes.IndexByte(rest, '\n'); j >= 0 {
					rest = rest[:j]
				}
				if v := strings.TrimSpace(string(rest)); v != "" {
					return v
				}
			}
		}
	}
	if v := os.Getenv(standinEnvVersion); v != "" {
		return v
	}
	return "v0.0.0-standin"
}

// ---- 测试侧替身构造与驱动 ----

// markedTestBinary 测试二进制副本 + 尾部版本标记(PE overlay 安全;被换装的
// 「新/旧 exe 内容」即此字节)。
func markedTestBinary(t *testing.T, version string) []byte {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	return append(append([]byte{}, data...), []byte(standinVerMagic+version+"\n")...)
}

// buildStandinExe 把测试二进制带版本标记写到 dst(测试世界的「当前 exe」)。
func buildStandinExe(t *testing.T, dst, version string) {
	t.Helper()
	if err := os.WriteFile(dst, markedTestBinary(t, version), 0o755); err != nil {
		t.Fatal(err)
	}
}

// writeStandinCmd 造测试世界的点火脚本(seam E 解析 + 拉起两用)。set 行的
// 引号值不以 .exe 结尾,不干扰解析器。注意:cmd 路径引号手工拼(%q 会把
// 反斜杠转义成 \\,cmd 不认)。
func writeStandinCmd(t *testing.T, dataDir, exePath string, port int, token string) string {
	t.Helper()
	body := "@echo off\r\n" +
		"rem test standin launcher\r\n" +
		"set \"" + standinEnv + "=1\"\r\n" +
		"set \"" + standinEnvPort + "=" + strconv.Itoa(port) + "\"\r\n" +
		"set \"" + standinEnvToken + "=" + token + "\"\r\n" +
		"set \"" + standinEnvDataDir + "=" + dataDir + "\"\r\n" +
		"\"" + exePath + "\" -test.run=" + standinTestPattern +
		" >> \"" + filepath.Join(dataDir, "standin.out.log") +
		"\" 2>> \"" + filepath.Join(dataDir, "standin.err.log") + "\"\r\n"
	p := filepath.Join(dataDir, "start-daemon.cmd")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// startStandinProcess 直启替身进程(不经 cmd 文件;构造看门抢跑/拒杀现场用)。
// 返回 PID;t.Cleanup 兜底杀(Windows 不随父进程收尸,纪律:不留孤儿)。
func startStandinProcess(t *testing.T, exePath string, port int, token, dataDir string, noShutdown bool) int {
	t.Helper()
	cmd := exec.Command(exePath, "-test.run="+standinTestPattern)
	cmd.Env = append(os.Environ(),
		standinEnv+"=1",
		fmt.Sprintf("%s=%d", standinEnvPort, port),
		standinEnvToken+"="+token,
		standinEnvDataDir+"="+dataDir,
	)
	if noShutdown {
		cmd.Env = append(cmd.Env, standinEnvNoShut+"=1")
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} // 测试进程不闪窗
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		if procAliveImpl(pid) {
			_ = killImpl(pid)
		}
	})
	return pid
}

// freePort 随机空闲口(测试绝不碰生产 7311/15900)。
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// randToken 测试令牌。
func randToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", b)
}

// syncBuf 并发安全日志收集(logf 注入)。
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// httpStatsVersion 直打 /stats 取 version(空 = 无应答/未起)。
func httpStatsVersion(port int, token string) (string, bool) {
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/stats", port), nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var out struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", false
	}
	return out.Version, true
}

// waitServe 等 /stats 报出 want 版本(超时 fail)。
func waitServe(t *testing.T, port int, token, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if v, ok := httpStatsVersion(port, token); ok && v == want {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("替身未在限时报出 %s", want)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
