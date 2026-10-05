package daemon

// settings_snapshot.go — 设置视图票05：配置快照与还原（spec .scratch/
// settings-view-impl/spec.md「快照」条）。
//
//   - 目录 <data_dir>/backups/config/，文件名
//     config-<YYYYMMDD-HHMMSS>-<auto|manual|pre-restore>.toml（同秒撞名时
//     插序号 config-<ts>-<N>-<class>.toml 消歧——冻结钟/高频写下唯一性）。
//     整文件字节快照（含密钥——快照是「原样回到写前」的救援面，不做脱敏
//     变体；内容绝不进任何响应）。
//   - 权限尽力收紧 0600：os.OpenFile/WriteFile 的 0600 是 POSIX 语义，
//     Windows/部分文件系统 chmod 不生效（EnsureToken 同先例，httpapi.go）——
//     敏感能级在 Windows 依赖用户目录 ACL（默认仅当前用户可读）。
//   - 滚动保留 20 份：超出按 mtime 删最旧；同刻并列按文件名序（时戳→序号）
//     兜确定性，同类最旧先删。
//   - 端点三口：POST /settings/snapshots（手动）、GET /settings/snapshots
//     （仅元数据 id/ts/reason/bytes——绝不回文件内容，无内容下载端点）、
//     POST /settings/snapshots/{id}/restore（还原前强制先快照当前态
//     reason=pre-restore → 整文件字节原子还原 → 响应 needs_restart=true）。
//   - 锁纪律（票03 单写者）：restore 与手动快照是写路径，经 settingsWriteMu
//     串行；GET 列表读不取锁。写路径自动快照的调用点（settingsPutSection）
//     已在锁内，故 snapshotBeforeWrite 的真实现自身不再取锁（sync.Mutex
//     不可重入，重入即死锁）。
//   - data_dir 解析：读盘上 config.toml 的 [server].data_dir（空=~/ferryman，
//     config.DataDir 同语义）。快照是盘面救援机制，以盘为准、不依赖守护内存
//     cfg（可 nil/滞后）；坏 TOML 照常可快照（部分解码失败回落默认目录）。

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ferryman/internal/clock"
	"ferryman/internal/config"

	"github.com/BurntSushi/toml"
)

// snapshotKeepN 滚动保留份数（spec「快照」条）。
const snapshotKeepN = 20

// snapshotClasses 文件名类 token（解析序：长者先判，pre-restore 含连字符）。
var snapshotClasses = []string{"pre-restore", "manual", "auto"}

// errSnapshotNotFound 未知/非法快照 id（还原面 404）。
var errSnapshotNotFound = errors.New("快照不存在")

// ---- data_dir / 目录 ----

// settingsSnapshotDataDir 盘上 config 的 data_dir：部分解码 [server] 节
// （整文件 Load 会因校验失败拒读——救援面不挑食）。空缺/坏 TOML 回落
// ~/ferryman（config.DataDir 同语义）。
func settingsSnapshotDataDir(cfgPath string) string {
	if raw, err := os.ReadFile(cfgPath); err == nil {
		var partial struct {
			Server struct {
				DataDir string `toml:"data_dir"`
			} `toml:"server"`
		}
		if err := toml.Unmarshal(raw, &partial); err == nil && partial.Server.DataDir != "" {
			return partial.Server.DataDir
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return filepath.Join(home, "ferryman")
}

// settingsSnapshotDir 快照目录（确保存在）。
func settingsSnapshotDir(cfgPath string) (string, error) {
	dir := filepath.Join(settingsSnapshotDataDir(cfgPath), "backups", "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// ---- 快照落盘 ----

// takeSnapshot 当前 config.toml 的整文件字节快照。调用方须已持
// settingsWriteMu（或在单写者临界区内）——本函数不取锁。返回元数据四字段。
func takeSnapshot(class string) (map[string]any, error) {
	cfgPath := config.ResolveConfigPath("")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("config.toml 读不出，无从快照: %w", err)
	}
	dir, err := settingsSnapshotDir(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("快照目录建不出: %w", err)
	}
	name, err := uniqueSnapshotName(dir, class)
	if err != nil {
		return nil, err
	}
	// 0600 尽力：POSIX 语义，Windows 下 chmod 不生效（文件头注释——快照含
	// 明文密钥，敏感能级在 Windows 依赖用户目录 ACL）。
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
		return nil, fmt.Errorf("快照落盘失败: %w", err)
	}
	pruneSnapshots(dir)
	return snapshotMeta(dir, name, class), nil
}

// uniqueSnapshotName 取下一个未用名：同秒无任何快照用基础名；已有则取同秒
// 最大序号+1。存在性探测不可用——滚动删除释放旧名后，新快照若复用会让已删
// id 重生（id 必须是稳定的创建身份）。序号按同秒全体快照共享（跨类单调）。
// 锁内调用，无创建竞态；时间取 clock.Now（冻结钟可测）UTC 定形。
func uniqueSnapshotName(dir, class string) (string, error) {
	ts := time.Unix(int64(clock.Now()), 0).UTC().Format("20060102-150405")
	prefix := "config-" + ts + "-"
	base := prefix + class + ".toml"
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	maxSeq := 0
	if _, err := os.Stat(filepath.Join(dir, base)); err == nil {
		maxSeq = 1
	}
	for _, en := range entries {
		name := en.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".toml") {
			continue
		}
		// rest = "<class>"（基础名，无序号）或 "<N>-<class>"；pre-restore 类
		// 自含连字符，Atoi("pre") 必败即自然跳过。
		rest := name[len(prefix) : len(name)-len(".toml")]
		if i := strings.IndexByte(rest, '-'); i > 0 {
			if n, err := strconv.Atoi(rest[:i]); err == nil && n > maxSeq {
				maxSeq = n
			}
		}
	}
	if maxSeq == 0 {
		return base, nil
	}
	return fmt.Sprintf("%s%d-%s.toml", prefix, maxSeq+1, class), nil
}

// ---- 文件名解析 ----

// snapshotKey 文件名排序键：ts 定宽字典序=时序；seq 同秒序号（无=1）。
type snapshotKey struct {
	ts  string
	seq int
}

// parseSnapshotName 快照文件名解析 → (类, 排序键, ok)。异形名一律不认——
// 列表/还原/滚动都只认解析通过的文件名（穿越与任意文件写入的一道闸）。
func parseSnapshotName(name string) (string, snapshotKey, bool) {
	if !strings.HasPrefix(name, "config-") || !strings.HasSuffix(name, ".toml") {
		return "", snapshotKey{}, false
	}
	rest := name[len("config-") : len(name)-len(".toml")]
	for _, class := range snapshotClasses {
		suf := "-" + class
		if !strings.HasSuffix(rest, suf) {
			continue
		}
		timePart := rest[:len(rest)-len(suf)]
		if len(timePart) < 15 {
			return "", snapshotKey{}, false
		}
		if _, err := time.Parse("20060102-150405", timePart[:15]); err != nil {
			return "", snapshotKey{}, false
		}
		key := snapshotKey{ts: timePart[:15], seq: 1}
		if tail := timePart[15:]; tail != "" {
			if len(tail) < 2 || tail[0] != '-' {
				return "", snapshotKey{}, false
			}
			n, err := strconv.Atoi(tail[1:])
			if err != nil || n < 2 {
				return "", snapshotKey{}, false
			}
			key.seq = n
		}
		return class, key, true
	}
	return "", snapshotKey{}, false
}

// ---- 元数据 / 清单 ----

// snapshotMeta 单份元数据（GET 面唯一口径：id/ts/reason/bytes 四字段，
// 绝不含文件内容；ts=mtime epoch 秒浮点，与账本 mtime 口径同源）。
func snapshotMeta(dir, id, class string) map[string]any {
	st, err := os.Stat(filepath.Join(dir, id))
	if err != nil {
		return map[string]any{"id": id, "reason": class} // 读不得=如实残缺（编不出字节数）
	}
	return map[string]any{
		"id":     id,
		"ts":     float64(st.ModTime().UnixNano()) / 1e9,
		"reason": class,
		"bytes":  st.Size(),
	}
}

// listSnapshots 目录清单（新→旧）。异形文件跳过；目录缺席=空清单。
func listSnapshots(dir string) []map[string]any {
	type item struct {
		name string
		mt   time.Time
		key  snapshotKey
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var items []item
	for _, en := range entries {
		if en.IsDir() {
			continue
		}
		_, key, ok := parseSnapshotName(en.Name())
		if !ok {
			continue
		}
		info, err := en.Info()
		if err != nil {
			continue
		}
		items = append(items, item{en.Name(), info.ModTime(), key})
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].mt.Equal(items[j].mt) {
			return items[i].mt.After(items[j].mt)
		}
		return snapshotKeyLess(items[i].key, items[j].key)
	})
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, snapshotMeta(dir, it.name, snapshotClassOf(it.name)))
	}
	return out
}

// snapshotClassOf 文件名 → 快照类（parseSnapshotName 的薄壳；清单已预筛
// 必然 ok）。
func snapshotClassOf(name string) string {
	c, _, _ := parseSnapshotName(name)
	return c
}

// snapshotKeyLess 同刻并列的文件名序：时戳先、序号次（基础名=1 最旧先删，
// 同类最旧先删在此收口）。
func snapshotKeyLess(a, b snapshotKey) bool {
	if a.ts != b.ts {
		return a.ts < b.ts
	}
	return a.seq < b.seq
}

// ---- 滚动保留 ----

// pruneSnapshots 超 snapshotKeepN 份删最旧：mtime 主序（跨秒真实时序），
// 同刻并列按文件名序兜确定性。删除尽力而为：失败留待下次滚动再收。
func pruneSnapshots(dir string) {
	type item struct {
		name string
		mt   time.Time
		key  snapshotKey
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var items []item
	for _, en := range entries {
		if en.IsDir() {
			continue
		}
		key, ok := snapshotKeyOf(en.Name())
		if !ok {
			continue
		}
		info, err := en.Info()
		if err != nil {
			continue
		}
		items = append(items, item{en.Name(), info.ModTime(), key})
	}
	if len(items) <= snapshotKeepN {
		return
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].mt.Equal(items[j].mt) {
			return items[i].mt.Before(items[j].mt) // 最旧在前
		}
		return snapshotKeyLess(items[i].key, items[j].key)
	})
	for i := 0; i < len(items)-snapshotKeepN; i++ {
		_ = os.Remove(filepath.Join(dir, items[i].name))
	}
}

// snapshotKeyOf 文件名 → 排序键（不要类的 prune/list 共用预筛）。
func snapshotKeyOf(name string) (snapshotKey, bool) {
	_, key, ok := parseSnapshotName(name)
	return key, ok
}

// ---- 还原 ----

// validSnapshotID 还原目标 id 白名单：纯文件名（无路径分隔、无 ".."、非
// 特殊名）且解析为合法快照名。
func validSnapshotID(id string) bool {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id ||
		strings.ContainsAny(id, `/\`) {
		return false
	}
	_, _, ok := parseSnapshotName(id)
	return ok
}

// settingsSnapshotRestore 还原（调用方持 settingsWriteMu）：先快照当前态
// （reason=pre-restore——还原覆写不可逆，回退点先行落袋，失败即不还原）→
// 整文件字节原子还原。响应 needs_restart=true（改的是 config.toml，重启生效）。
func settingsSnapshotRestore(id string) (map[string]any, error) {
	if !validSnapshotID(id) {
		return nil, errSnapshotNotFound
	}
	cfgPath := config.ResolveConfigPath("")
	snapPath := filepath.Join(settingsSnapshotDataDir(cfgPath), "backups", "config", id)
	raw, err := os.ReadFile(snapPath)
	if err != nil {
		return nil, errSnapshotNotFound
	}
	if _, err := takeSnapshot("pre-restore"); err != nil {
		return nil, fmt.Errorf("还原前快照失败（不还原）: %w", err)
	}
	if err := snapshotAtomicRestore(cfgPath, raw); err != nil {
		return nil, fmt.Errorf("还原落盘失败: %w", err)
	}
	return map[string]any{"restored": true, "needs_restart": true}, nil
}

// snapshotAtomicRestore 整文件字节还原（config.settingsAtomicWrite 同纪律：
// 权限沿用原文件缺省 0600、同目录临时文件、rename 原子替换、写后复读逐字节
// 自校验）。
func snapshotAtomicRestore(p string, raw []byte) error {
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(p); err == nil {
		mode = fi.Mode().Perm() // 保留原文件权限（含真钥的敏感能级不放宽）
	}
	tmp := p + ".restore-tmp"
	if err := os.WriteFile(tmp, raw, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p); err != nil { // 原子替换（Windows MoveFileEx 语义）
		_ = os.Remove(tmp)
		return err
	}
	back, err := os.ReadFile(p) // 写后复读自校验
	if err != nil {
		return err
	}
	if !bytes.Equal(back, raw) {
		return errors.New("还原写后复读与快照不符（config.toml 可能已损坏，请人工核查）")
	}
	return nil
}

// ---- HTTP 端点 ----

// handleSettingsSnapshotCreate POST /settings/snapshots（手动快照）：写路径
// 经票03 单写者锁。响应 created=true + 元数据四字段。
func handleSettingsSnapshotCreate(dl DaemonLike, w http.ResponseWriter, _ *http.Request) {
	if _, ok := dl.(*Daemon); !ok {
		notFound(w) // 替身无快照面（settings_write.go 同语义）
		return
	}
	settingsWriteMu.Lock()
	defer settingsWriteMu.Unlock()
	meta, err := takeSnapshot("manual")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	resp := map[string]any{"created": true}
	for k, v := range meta {
		resp[k] = v
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSettingsSnapshotList GET /settings/snapshots：仅元数据清单（新→旧）。
// 读面不取写锁（票03 锁纪律）；绝不回文件内容，亦无内容下载端点。
func handleSettingsSnapshotList(dl DaemonLike, w http.ResponseWriter) {
	if _, ok := dl.(*Daemon); !ok {
		notFound(w)
		return
	}
	dir := filepath.Join(settingsSnapshotDataDir(config.ResolveConfigPath("")), "backups", "config")
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": listSnapshots(dir)})
}

// handleSettingsSnapshotRestore POST /settings/snapshots/{id}/restore：写路径
// 经锁；未知/非法 id 404。
func handleSettingsSnapshotRestore(dl DaemonLike, w http.ResponseWriter, r *http.Request) {
	if _, ok := dl.(*Daemon); !ok {
		notFound(w)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/settings/snapshots/")
	if !strings.HasSuffix(rest, "/restore") {
		notFound(w) // 非 restore 子路径不认（无其他动作面）
		return
	}
	id := strings.TrimSuffix(rest, "/restore")
	settingsWriteMu.Lock()
	defer settingsWriteMu.Unlock()
	resp, err := settingsSnapshotRestore(id)
	if err != nil {
		if errors.Is(err, errSnapshotNotFound) {
			notFound(w)
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
