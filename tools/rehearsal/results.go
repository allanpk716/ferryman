package main

// results.go — 彩排结果落盘（票05 What-to-build 第5条）：每次彩排写
// JSON+MD 到 .scratch/upgrade-reliability/rehearsal/（目录运行时创建）。
// JSON 供机器比对（CI/回归）；MD 供人查（场景×断言×耗时）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// checkRec 单条断言记录。
type checkRec struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// phaseRec 一个场景/故障注入阶段的记录。
type phaseRec struct {
	Name    string            `json:"name"`
	Pass    bool              `json:"pass"`
	Checks  []checkRec        `json:"checks"`
	Metrics map[string]string `json:"metrics,omitempty"`
	DurSec  float64           `json:"dur_sec"`
	Err     string            `json:"err,omitempty"`
}

// buildRec 两版影子 exe 的构建记录。
type buildRec struct {
	Route   string  `json:"route"` // 影子两版的可行路线说明（票面要求注明）
	OldTag  string  `json:"old_tag"`
	NewTag  string  `json:"new_tag"`
	OldSHA  string  `json:"old_sha"`
	NewSHA  string  `json:"new_sha"`
	OldSize int64   `json:"old_size"`
	NewSize int64   `json:"new_size"`
	DurSec  float64 `json:"dur_sec"`
}

// rehearsalReport 一次彩排的完整结论。
type rehearsalReport struct {
	Mode            string     `json:"mode"` // all | quick
	StartedAt       string     `json:"started_at"`
	RepoRoot        string     `json:"repo_root"`
	Builds          buildRec   `json:"builds"`
	PortsUsed       []int      `json:"ports_used"` // 全高位随机分配——零生产端口的证
	Phases          []phaseRec `json:"phases"`
	OverallPass     bool       `json:"overall_pass"`
	TotalDurSec     float64    `json:"total_dur_sec"`
	ResultsJSONPath string     `json:"results_json_path"`
	ResultsMDPath   string     `json:"results_md_path"`
}

// ck 断言构造糖。
func ck(name string, pass bool, detail string) checkRec {
	return checkRec{Name: name, Pass: pass, Detail: detail}
}

// writeReports 落盘 JSON+MD，返回两路径。
func writeReports(rep *rehearsalReport, outDir string) (string, string, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", "", err
	}
	stamp := time.Now().Format("20060102-150405")
	jsonPath := filepath.Join(outDir, "rehearsal-"+stamp+".json")
	mdPath := filepath.Join(outDir, "rehearsal-"+stamp+".md")

	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return "", "", err
	}
	if err := writeAtomic(jsonPath, append(b, '\n')); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(mdPath, []byte(renderMD(rep)), 0o644); err != nil {
		return "", "", err
	}
	rep.ResultsJSONPath, rep.ResultsMDPath = jsonPath, mdPath
	return jsonPath, mdPath, nil
}

// renderMD 人话报告。
func renderMD(rep *rehearsalReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Ferryman 升级彩排报告（%s）\n\n", rep.Mode)
	fmt.Fprintf(&b, "- 时间：%s（全程 %.0fs）\n", rep.StartedAt, rep.TotalDurSec)
	fmt.Fprintf(&b, "- 结论：%s\n\n", passMark(rep.OverallPass))
	fmt.Fprintf(&b, "## 影子两版路线\n\n%s\n\n", rep.Builds.Route)
	fmt.Fprintf(&b, "| 版本 | SHA256 前 12 | 大小 |\n|---|---|---|\n")
	fmt.Fprintf(&b, "| %s | %s | %d B |\n", rep.Builds.OldTag, shortSHA(rep.Builds.OldSHA), rep.Builds.OldSize)
	fmt.Fprintf(&b, "| %s | %s | %d B |\n\n", rep.Builds.NewTag, shortSHA(rep.Builds.NewSHA), rep.Builds.NewSize)
	fmt.Fprintf(&b, "## 阶段\n\n")
	for _, ph := range rep.Phases {
		fmt.Fprintf(&b, "### %s — %s（%.0fs）\n\n", ph.Name, passMark(ph.Pass), ph.DurSec)
		if ph.Err != "" {
			fmt.Fprintf(&b, "- 阶段错误：%s\n", ph.Err)
		}
		for _, c := range ph.Checks {
			fmt.Fprintf(&b, "- %s %s", passMark(c.Pass), c.Name)
			if c.Detail != "" {
				fmt.Fprintf(&b, " — %s", c.Detail)
			}
			b.WriteString("\n")
		}
		if len(ph.Metrics) > 0 {
			b.WriteString("\n<details><summary>指标</summary>\n\n")
			for k, v := range ph.Metrics {
				fmt.Fprintf(&b, "- %s: %s\n", k, v)
			}
			b.WriteString("\n</details>\n")
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "## 端口\n\n全部来自高位随机分配（%d 个），零生产端口访问：\n\n%s\n",
		len(rep.PortsUsed), intsJoin(rep.PortsUsed))
	return b.String()
}

func passMark(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func intsJoin(vs []int) string {
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = fmt.Sprint(v)
	}
	return strings.Join(parts, ", ")
}
