package daemon

// settings_restart.go — 设置视图票07：POST /settings/restart 安全重启端点。
//
// 语义总述（票面拍板，F2/F7 已解除）：
//   - 回滚源=F2 定案 A「上次健康运行配置」：守护每次成功启动把当次已 Load
//     校验的 config.toml 原字节盖戳到 <data_dir>/backups/config/last-healthy.toml
//     （stampLastHealthyConfig；serve.go 在 pid 落盘后、控制面开跑前调用）。
//     重启健康失败时 restart 帮手按它还原后再拉起一次（update/restart.go
//     RunRestart 的 Restore 缺省）。命名无 config- 前缀——不进快照滚动/清单
//     （pruneSnapshots/listSnapshots 按前缀匹配天然排除，测试已钉）。
//   - F7 全程持锁：restart 编排从预检到终局全程持 settingsWriteMu——取锁即
//     排干在飞写（写完才轮到 restart 的预检）；准入后新到的写排队、在进程
//     退场前不获锁（生产形态 hold 到帮手停掉本进程，锁永不释放；测试经
//     settingsRestartHoldUntilExit 缝返回，defer Unlock 正常收尾）。
//   - 帮手进程分工：端点进程内只做「预检 → 静默门 → 拉帮手 → 回话 → hold」，
//     绝不在自己进程里停旧/拉新——旧进程不退场端口不释放，停旧拉新在帮手
//     进程 `ferryman restart --from N --to M`（cmd/ferryman/restart.go →
//     update.RunRestart）里做，旧进程由帮手经 /shutdown+兜底 kill 收。
//     缺省拉起缝带递归熔断：测试二进制（*.test/.test.exe）内禁真拉帮手
//     （2026-10-06 闪窗事故防线，见缝注释）。
//
// 预检四连（顺序钉死）：
//   ① 升级事务进行中（update.lock 被活监督者持有——serve.go 让路判定同源）
//     → 拒：restart 与换装互斥，升级窗口内不重启；
//   ② 盘上 config 干跑 Load（重启后守护按它启动——现在就拒好过起来再死）；
//   ④ 数据目录身份比对（Load 成功后即可比）：盘上 [server].data_dir 与运行
//     中守护不一致 → 拒——真迁移要搬 token/pid/handoffs/台账一整套，超出
//     本票（评审高·返工裁定，详见预检处注释）；
//   ③ 端口改动须可绑：盘上 [server].port ≠ 守护内存口时 net.Listen 探测新口，
//     绑不上 → 拒（文案报端口号与原因；TOCTOU：探测与帮手真绑之间的竞窗由
//     F9 裁定兜底——帮手 Launch 后 Probe 轮询抓得住，按健康失败走回滚）。
//
// 静默门：d.dockProxyStats() 判静默（在途 0 且距最后请求 ≥10s 或从未有请求）
// 才动手；不满足轮询等待 settingsRestartQuietWait（缺省 60s，间隔
// settingsRestartQuietPoll 缺省 1s）；预算尽 → 硬切放行（用户主动点重启）+
// 一行日志。与 update 监督者 quietGate 同判据（defaultQuietRequired 同值），
// 但无交互三选——端点语境无人在终端前。
//
// 审计行：auditSettingsWrite(section="restart"，outcome=restarting|rejected，
// before/after={port:旧口→新口}，拒绝路径带 error)。守门序照 settings_switch.go
// 管理端点族：loopback 403 → 非 POST 405+Allow → 读光 body → Bearer 401 →
// 非 *Daemon 404；空 body 或 {} 皆收（无必填字段）。

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"ferryman/internal/config"
	"ferryman/internal/update"
)

// ---- 盖章（F2 回滚源 A）----

// stampLastHealthyConfig 启动成功点盖戳「上次健康运行配置」：cfgPath 原字节
// → <dataDir>/backups/config/last-healthy.toml。只在启动成功点调用（写面尚未
// 服务，无需取锁）；外部手改漂移由回滚前 Load 校验兜底（cmd 侧 Load 不通过
// 即判无有效回滚源）。0600/0755 尽力——Windows 依赖用户目录 ACL（快照同
// 纪律）。
func stampLastHealthyConfig(cfgPath, dataDir string) error {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	dir := filepath.Join(dataDir, "backups", "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "last-healthy.toml"), raw, 0o600)
}

// ---- 缝（var 形=测试注入，snapshotBeforeWrite 同惯例）----

// settingsRestartUpdateLockHeld 升级锁让路判定单源（serve.go lockYieldProbe
// 同款）：update 包导出的只读助手，daemon 只调用不复制。
var settingsRestartUpdateLockHeld = update.LockHeldByLiveSupervisor

// settingsRestartQuietWait / settingsRestartQuietPoll 静默门等待预算/轮询间隔
// （缺省 60s/1s）。
var (
	settingsRestartQuietWait = 60 * time.Second
	settingsRestartQuietPoll = 1 * time.Second
)

// settingsRestartSpawnHelper 帮手拉起缝：缺省 detached 隐藏拉起本 exe 的
// `restart --from <旧口> --to <新口>`（update.SpawnDetachedHidden——零闪窗
// 铁律+帮手长命于端点进程）。
//
// 递归熔断（2026-10-06 闪窗事故主防线，永久）：go test 进程里 os.Executable()
// 是 *.test(.exe)——真拉起会以「restart --from N --to M」位置参数把测试二进制
// 再执行一遍＝整套 internal/daemon 套件递归重跑（套件内真跑 powershell 钩子/
// netstat 的测试每个 console 子进程各开可见 conhost → 用户桌面闪窗风暴；且
// 若递归链里再进 restart 端点，真 hold 会泄漏永久持 settingsWriteMu 的
// goroutine 绞死整包测试）。故测试二进制内一律拒绝真拉起、直接返回错误——
// spawn 必败则 hold 不可达，绞死类问题同时消灭。
var settingsRestartSpawnHelper = func(fromPort, toPort int) error {
	if base := filepath.Base(os.Args[0]); strings.HasSuffix(base, ".test") || strings.HasSuffix(base, ".test.exe") {
		return fmt.Errorf("测试二进制内禁真拉帮手（防整套测试递归重跑；from=%d to=%d）", fromPort, toPort)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return update.SpawnDetachedHidden(exe, "restart",
		"--from", strconv.Itoa(fromPort), "--to", strconv.Itoa(toPort))
}

// settingsRestartHoldUntilExit 终局持锁缝：缺省 select{} 永不返回——生产形态
// 本进程将由帮手停掉（进程退场=锁随进程消失）；测试注入返回形，defer Unlock
// 正常收尾。
var settingsRestartHoldUntilExit = func() { select {} }

// ---- POST /settings/restart ----

// doSettingsRestart POST /settings/restart（httpapi.go makeHandler 无条件拦截
// ——管理端点族，无钩子面）。守门序见文件头注释；业务编排 settingsRestart。
func doSettingsRestart(dl DaemonLike, token string, w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden: loopback only"})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	bodyRaw, _ := io.ReadAll(r.Body) // 先读光 body 再回话（管理端点族 RST 纪律）
	if !isAuthed(r, token) {
		unauthorized(w)
		return
	}
	d, ok := dl.(*Daemon)
	if !ok {
		notFound(w) // 替身无设置面（settings_write.go 同语义）
		return
	}
	// 空 body 或 {} 皆收（无必填字段）；非空非对象 JSON 明确拒。
	if len(bodyRaw) > 0 {
		var probe map[string]any
		if err := json.Unmarshal(bodyRaw, &probe); err != nil {
			badRequest(w, fmt.Errorf("body 须为空或 JSON 对象（本端点无必填字段）"))
			return
		}
	}

	// F7 全程持锁：从预检到终局（含回话与 hold）都在 settingsWriteMu 临界
	// 区内——取锁=排干在飞写；准入后新到的写排队、在进程退场前不获锁。
	// 生产形态 hold 永不返回，defer Unlock 不可达；测试 hold 缝返回后正常收尾。
	settingsWriteMu.Lock()
	defer settingsWriteMu.Unlock()

	code, resp := settingsRestartLocked(d)
	writeJSON(w, code, resp)
	// hold 后 handler 永不返回——不冲刷，响应永远出不了网（doShutdown 同坑
	// 同修：writeJSON 只写缓冲，Flush 才上 TCP）。
	_ = http.NewResponseController(w).Flush()
	if code == http.StatusOK {
		// F7 终局：hold 在锁内——生产 select{} 永不返回，defer Unlock 不可达
		//（进程退场=锁随进程消失，排队写在帮手停掉本进程前不获锁）；测试经缝
		// 返回后 defer Unlock 正常收尾。
		settingsRestartHoldUntilExit()
	}
}

// settingsRestartLocked 重启编排主体（F7：调用方已持 settingsWriteMu，本函数
// 不取锁——预检→静默门→审计→拉帮手）。所有 400/500 路径：审计 rejected
// （带 error）后回话；票01 原语保证拒绝路径 config 字节不动（本函数全程不写盘）。
func settingsRestartLocked(d *Daemon) (int, map[string]any) {
	oldPort := d.Cfg.Server.Port
	dataDir := d.Cfg.DataDir()
	before := map[string]any{"port": oldPort}

	reject := func(after map[string]any, err error) (int, map[string]any) {
		auditSettingsWrite(d, "restart", "rejected", before, after, err)
		return http.StatusBadRequest, map[string]any{"error": err.Error()}
	}

	// 预检①：升级事务进行中 → 拒（restart 与换装互斥）。
	selfExe, _ := os.Executable()
	if _, held := settingsRestartUpdateLockHeld(dataDir, selfExe, nil, nil); held {
		return reject(nil, fmt.Errorf("升级事务进行中，稍后再试"))
	}
	// 预检②：盘上配置干跑 Load。
	diskCfg, err := config.Load(config.ResolveConfigPath(""), false)
	if err != nil {
		return reject(nil, fmt.Errorf("盘上配置重启后将无法启动，拒重启: %w", err))
	}
	// 预检④（评审高·返工裁定）：数据目录身份比对——盘上 data_dir 与运行中
	// 守护不一致 → 拒。真迁移 data_dir 要搬 token/pid/handoffs/台账一整套，
	// 超出本票；只修帮手停旧（用旧身份）会让新守护带着空数据目录「健康」
	// 起来——更糟。两侧都走 DataDir() 解析（空=~/ferryman 缺省）再比。
	if !settingsRestartSameDataDir(diskCfg.DataDir(), d.Cfg.DataDir()) {
		return reject(nil, fmt.Errorf("盘上数据目录将变为 %s（当前运行中为 %s）——数据目录改动暂不支持随安全重启生效，本次不重启；如确要迁移，请手动改回或手动迁移后再试",
			diskCfg.DataDir(), d.Cfg.DataDir()))
	}
	// 预检③：端口改动须可绑。
	newPort := diskCfg.Server.Port
	after := map[string]any{"port": newPort}
	if newPort != oldPort {
		ln, lerr := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(newPort)))
		if lerr != nil {
			return reject(after, fmt.Errorf("新端口 %d 目前绑不上（被占用或不可用），请先换端口: %w", newPort, lerr))
		}
		_ = ln.Close()
	}

	// 静默门：在途 0 且距最后请求 ≥10s（或从未有请求）才动手；预算尽硬切
	// 放行——用户主动点重启，静默门是礼貌不是关卡。
	settingsRestartWaitQuiet(d)

	auditSettingsWrite(d, "restart", "restarting", before, after, nil)

	// 拉起帮手（detached 隐藏）：停旧/拉新/健康失败回滚全在帮手进程里。
	if err := settingsRestartSpawnHelper(oldPort, newPort); err != nil {
		err = fmt.Errorf("重启帮手拉起失败: %w", err)
		auditSettingsWrite(d, "restart", "rejected", before, after, err)
		return http.StatusInternalServerError, map[string]any{"error": err.Error()}
	}

	resp := map[string]any{"restarting": true}
	if newPort != oldPort {
		resp["new_port"] = newPort // 端口变了才附（UI 明示「重启后自动改连新端口」）
	}
	return http.StatusOK, resp
}

// settingsRestartSameDataDir 数据目录身份比对（预检④）：Clean 后比较；
// Windows 文件系统大小写不敏感 → EqualFold，且正斜杠归一为反斜杠
// （update.samePath 同款归一——盘上写 / 与运行态 \ 混用不误判）；非 Windows
// 精确比。
func settingsRestartSameDataDir(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(strings.ReplaceAll(a, "/", `\`)),
			filepath.Clean(strings.ReplaceAll(b, "/", `\`)))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// settingsRestartWaitQuiet 静默门（update 监督者 quietGate 同判据、无交互面）：
// dockProxyStats 抄表在途/最后请求时戳；不满足则按预算轮询，预算尽硬切放行
// （一行留证）。DockSnap 为 nil（渡口未启用）恒 0/0=从未有请求=立即静默。
func settingsRestartWaitQuiet(d *Daemon) {
	deadline := time.Now().Add(settingsRestartQuietWait)
	for {
		inflight, lastTS := d.dockProxyStats()
		quiet := inflight == 0 &&
			(lastTS == 0 || time.Since(time.Unix(lastTS, 0)) >= 10*time.Second)
		if quiet {
			return
		}
		if !time.Now().Before(deadline) {
			fmt.Printf("[ferryman] restart 静默门预算尽（在途 %d，距最后请求 %ds）——硬切放行（用户主动点重启）\n",
				inflight, int(time.Since(time.Unix(lastTS, 0)).Seconds()))
			return
		}
		time.Sleep(settingsRestartQuietPoll)
	}
}
