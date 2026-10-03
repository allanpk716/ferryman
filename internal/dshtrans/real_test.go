package dshtrans

// 真机会话日志实测（P2-1 完成定义的「真会话日志实测通过」）。
//
// 门：环境变量 FERRYMAN_DSH_REAL_SESSIONS 指向 ~/.dsh/sessions（默认跳过
// ——hermetic CI 不依赖本机数据；本机验收时显式置位跑）。双重真值对账：
//   - 头行 cwd → ProjectKey == 磁盘上的项目目录名（规范化规则回钉真机）；
//   - 头行 id == DecodeSegment(会话目录名)（转义单射性回钉）；
//   - 全部代文件可整读（帧扫描铺满、无残帧）、usage 行四列齐全。

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRealSessions(t *testing.T) {
	root := os.Getenv("FERRYMAN_DSH_REAL_SESSIONS")
	if root == "" {
		t.Skip("FERRYMAN_DSH_REAL_SESSIONS 未置位——跳过真机夹具（本机验收时指到 ~/.dsh/sessions）")
	}
	projects, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("读会话根: %v", err)
	}
	sessions := 0
	var usageRows int
	for _, proj := range projects {
		if !proj.IsDir() {
			continue
		}
		dirs, err := os.ReadDir(filepath.Join(root, proj.Name()))
		if err != nil {
			continue
		}
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			sessions++
			dir := filepath.Join(root, proj.Name(), d.Name())
			gen, ok := LatestGeneration(dir)
			if !ok {
				t.Errorf("%s: 无规范代文件", dir)
				continue
			}
			// ① 头行可读、id 与目录名单射对账。
			line, ok := ReadHeaderLine(gen.Path, gen.Zstd)
			if !ok {
				t.Errorf("%s: 头行不可读", gen.Path)
				continue
			}
			h, ok := ParseHeaderLine(line)
			if !ok {
				t.Errorf("%s: 头行不可解析: %.80s", gen.Path, line)
				continue
			}
			decID, ok := DecodeSegment(d.Name())
			if !ok || decID != h.ID {
				t.Errorf("%s: 目录名解码 %q ≠ 头行 id %q", dir, decID, h.ID)
			}
			// ② cwd → ProjectKey == 磁盘项目目录名（_no-cwd 目录除外——那是
			//    projectDir 的 undefined-cwd 分支，非 ProjectKey 产物）。
			if proj.Name() != "_no-cwd" && h.Cwd != "" {
				key, err := ProjectKey(h.Cwd)
				if err != nil {
					t.Errorf("%s: ProjectKey 报错: %v", dir, err)
				} else if key != proj.Name() {
					t.Errorf("%s: ProjectKey(%q) = %q ≠ 磁盘目录名", dir, h.Cwd, key)
				}
			}
			// ③ 整文件可读：帧/行全消费、无错误。
			res := TailText(gen.Path, gen.Zstd, 0)
			if res.Err != nil {
				t.Errorf("%s: 尾读报错: %v", gen.Path, res.Err)
			}
			if info, err := os.Stat(gen.Path); err == nil && res.NewOffset != info.Size() {
				t.Errorf("%s: 消费偏移 %d ≠ 文件大小 %d（真机文件不应有残帧）",
					gen.Path, res.NewOffset, info.Size())
			}
			// ④ usage 行解析（有则四列结构齐全）。
			rows, _, _ := ParseChunk(res.Text, "")
			usageRows += len(rows)
			for i, r := range rows {
				if r.InputTokens < 0 || r.OutputTokens < 0 || r.Seq < 0 || !r.HasTS {
					t.Errorf("%s: rows[%d] 形状异常: %+v", gen.Path, i, r)
				}
			}
		}
	}
	if sessions == 0 {
		t.Fatalf("根目录 %s 下无会话目录", root)
	}
	t.Logf("真机会话 %d 个、usage 出行 %d 条（四列解析通过）", sessions, usageRows)
}
