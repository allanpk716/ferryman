// 契约锚存储（票03，D5）：验证全绿时对四类契约面落形状签名快照，绑 DSH
// 版本；全绿自动滚动、保留最近 N 份历史。本包只存结构与 diff——形状采集在
// 票05（探针），DiffFaces 只出"哪些面漂了"的清单供诊断聚焦，不影响执行
// 范围（D5 补注：L0/L1/L2 执行范围不缩减、L2 含压缩链两道每次全跑）。
package dshledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"ferryman/internal/clock"
)

// 四类契约面（CONTEXT.md「契约面」词条：锚 JSON 按面分节的节名）。中文术语
// 为准，英文键为落盘形状。
const (
	FaceDiscovery = "discovery" // 发现面：package.json dsh manifest 形状＋insert 行＋junction 指向
	FaceEvents    = "events"    // 事件面：宿主五事件位
	FaceDuck      = "duck"      // 鸭子面：PluginContext 上容错注入的服务接口群
	FaceBrowser   = "browser"   // 浏览器半面：client.js 模块加载、dock 槽位与 RPC
)

// faceOrder 面定序＝CONTEXT.md 四面点名序（结构校验报错与 DiffFaces 输出
// 均按此序，诊断报告可预期）。
var faceOrder = []string{FaceDiscovery, FaceEvents, FaceDuck, FaceBrowser}

// DefaultKeepAnchors 锚滚动保留份数（spec Further Notes：N 取 10，首版可调）。
const DefaultKeepAnchors = 10

// Anchor 契约锚本体：四面形状签名（每面＝形状哈希串或字段集，票05 采集定形，
// 本包不校验值形状）＋DSH 版本。哈希与身份只含这两个字段——时间不进哈希，
// 同形状重复验证不换锚身份。
type Anchor struct {
	DSHVersion string         `json:"dsh_version"`
	Faces      map[string]any `json:"faces"`
}

// FaceDiff 一个漂移面（仅供诊断报告：面名＋旧/新形状；不进执行决策）。
// 任一侧缺面时对应值为 nil。
type FaceDiff struct {
	Face string `json:"face"`
	Old  any    `json:"old"`
	New  any    `json:"new"`
}

// anchorFile 落盘包装：ts 只用于 Latest 排序与诊断展示，不参与哈希；匿名
// 内嵌使落盘 JSON 平铺为 {"ts":…,"dsh_version":…,"faces":…}。
type anchorFile struct {
	TS float64 `json:"ts"`
	Anchor
}

// AnchorStore 契约锚目录（<dataDir>/dshledger/anchors/）。
type AnchorStore struct {
	mu    sync.Mutex
	dir   string
	keepN int
}

// NewAnchorStore 打开契约锚存储；keepN<=0 → DefaultKeepAnchors（N 可配）。
func NewAnchorStore(dataDir string, keepN int) (*AnchorStore, error) {
	dir := filepath.Join(dataDir, "dshledger", "anchors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if keepN <= 0 {
		keepN = DefaultKeepAnchors
	}
	return &AnchorStore{dir: dir, keepN: keepN}, nil
}

// Write 全绿落锚：四面结构校验（缺面/多面响亮拒绝——四面钉死在 CONTEXT.md，
// 采集面拼错静默成第五节比拒绝更毒）→ 内容哈希（sha256 hex，语义载荷＝DSH
// 版本＋四面）→ 落盘 <本地时刻毫秒戳>_<全哈希>.json（原子写）→ 滚动淘汰
// 最旧。返回锚哈希（判定流水 green 行 anchor_hash 的同源值）。
func (s *AnchorStore) Write(dshVersion string, faces map[string]any) (string, error) {
	if err := checkFaces(faces); err != nil {
		return "", err
	}
	a := Anchor{DSHVersion: dshVersion, Faces: faces}
	hash, err := anchorHash(&a)
	if err != nil {
		return "", err
	}
	ts := clock.Now()
	payload, err := encodeIndent(anchorFile{TS: ts, Anchor: a})
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := atomicWrite(filepath.Join(s.dir, stampName(ts, hash)), payload); err != nil {
		return "", err
	}
	s.pruneLocked()
	return hash, nil
}

// Latest 最近一份锚（无锚 → nil,"",nil：首跑/从未全绿）。哈希取文件名尾段
// （Write 落名即真值，免重算），与判定流水 green 行 anchor_hash 同源可比。
func (s *AnchorStore) Latest() (*Anchor, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ents := s.list()
	if len(ents) == 0 {
		return nil, "", nil
	}
	data, err := os.ReadFile(ents[0].path)
	if err != nil {
		return nil, "", err
	}
	var f anchorFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, "", fmt.Errorf("契约锚解析失败 %s: %w", ents[0].name, err)
	}
	a := f.Anchor // 副本出锁
	return &a, anchorHashFromName(ents[0].name), nil
}

// DiffFaces 旧锚 vs 新采集形状的漂移面清单（面定序＝faceOrder）。面在任一
// 侧缺失、或两侧形状归一 JSON 不等 → 该面漂移。仅诊断输出——调用方不得据
// 此缩减探针执行范围（D5 补注）。old 为 nil（无历史锚可 diff）返回空——
// 无从 diff 不伪造漂移。
func DiffFaces(old *Anchor, faces map[string]any) []FaceDiff {
	if old == nil {
		return nil
	}
	out := []FaceDiff{}
	for _, face := range faceOrder {
		ov, okOld := old.Faces[face]
		nv, okNew := faces[face]
		if !okOld || !okNew || !faceEqual(ov, nv) {
			out = append(out, FaceDiff{Face: face, Old: ov, New: nv})
		}
	}
	return out
}

// checkFaces 四面结构校验：faces 须恰含四类契约面。值形状（哈希串/字段集）
// 票05 定形，本包不校验值。
func checkFaces(faces map[string]any) error {
	var missing, extra []string
	for _, f := range faceOrder {
		if _, ok := faces[f]; !ok {
			missing = append(missing, f)
		}
	}
	for f := range faces {
		if !slices.Contains(faceOrder, f) {
			extra = append(extra, f)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		sort.Strings(extra)
		return fmt.Errorf("契约锚须恰含四类契约面: 缺 %v, 多余 %v", missing, extra)
	}
	return nil
}

// anchorHash 锚内容哈希：compact JSON（map 键序 encoding/json 确定）→
// sha256 hex。哈希口径恒定＝dsh_version+faces 两字段，不含落盘 ts。
func anchorHash(a *Anchor) (string, error) {
	b, err := encodeCompact(a)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// faceEqual 形状相等：归一 JSON 字节比对（map 键序确定；数值统一 JSON 语义
// ——两侧各经一次序列化，int/float64 书写差被归一吸收）。
func faceEqual(a, b any) bool {
	ab, err1 := encodeCompact(a)
	bb, err2 := encodeCompact(b)
	return err1 == nil && err2 == nil && bytes.Equal(ab, bb)
}

// stampName 锚文件名：<本地时区 20060102_150405.000>_<全哈希>.json——时刻
// 毫秒戳供人读，排序真值是内容 ts（list），文件名只在 ts 并列时作次序微调。
func stampName(ts float64, hash string) string {
	stamp := time.UnixMilli(int64(ts * 1000)).Local().Format("20060102_150405.000")
	return stamp + "_" + hash + ".json"
}

// anchorEnt 目录扫描条目（Latest/滚动共用）。
type anchorEnt struct {
	name string
	path string
	ts   float64
}

// list 按命名规约 <stamp>_<64hex>.json 扫描并按 (ts, name) 降序。外来文件
// 不认不动（store.PruneOlderThan「非交接命名保留」同款）；坏文件跳过（ts
// 读不出即失序权，自然先被淘汰）。调用方持锁。
func (s *AnchorStore) list() []anchorEnt {
	des, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var ents []anchorEnt
	for _, de := range des {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".json") {
			continue
		}
		name := de.Name()
		hash := anchorHashFromName(name)
		if len(hash) != 64 {
			continue
		}
		if _, err := hex.DecodeString(hash); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			continue
		}
		var f anchorFile
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		ents = append(ents, anchorEnt{name: name, path: filepath.Join(s.dir, name), ts: f.TS})
	}
	sort.Slice(ents, func(i, j int) bool {
		if ents[i].ts != ents[j].ts {
			return ents[i].ts > ents[j].ts
		}
		return ents[i].name > ents[j].name
	})
	return ents
}

// anchorHashFromName 文件名尾段即全哈希（Write 命名规约的逆）。
func anchorHashFromName(name string) string {
	base := strings.TrimSuffix(name, ".json")
	if i := strings.LastIndexByte(base, '_'); i >= 0 {
		return base[i+1:]
	}
	return ""
}

// pruneLocked 滚动淘汰：保留最近 keepN 份。删除失败静默留待下轮（占用/
// 权限类瞬时故障不阻断落锚，store.PruneOlderThan 同款）。调用方持锁。
func (s *AnchorStore) pruneLocked() {
	ents := s.list()
	for i := s.keepN; i < len(ents); i++ {
		os.Remove(ents[i].path)
	}
}

// atomicWrite 同后缀 .tmp + os.Rename（store 同款；Windows os.Rename 即替换
// 语义）。
func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// encodeIndent 两空格缩进 JSON（锚文件人读友好，store index 同款）；
// SetEscapeHTML(false)＝非 ASCII 直出。
func encodeIndent(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
