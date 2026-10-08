// l0dump — 盲评泳道材料抽取器：对会话 jsonl 输出 L0 骨架与摆渡材料
// （与生产 FerrySession 同一 extract 管线，保证三泳道喂的是同一份料）。
//
// 用法: go run ./experiments/same-model-arm/l0dump -session <jsonl路径> -out <目录>
// 产物: <out>/skeleton.md（骨架泳道原文）、<out>/material.md（喂模型的材料）
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"ferryman/internal/extract"
)

func main() {
	session := flag.String("session", "", "会话 jsonl 路径")
	out := flag.String("out", "", "输出目录")
	flag.Parse()
	if *session == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "用法: l0dump -session <jsonl> -out <dir>")
		os.Exit(2)
	}
	facts, items, _ := extract.Extract(*session)
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir:", err)
		os.Exit(1)
	}
	skel := facts.SkeletonText()
	mat := extract.MaterialText(facts, items)
	for name, content := range map[string]string{
		"skeleton.md": skel,
		"material.md": mat,
	} {
		p := filepath.Join(*out, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
	}
	fmt.Printf("ok items=%d matTokens≈%d -> %s\n", len(items), extract.TokenEstimate(mat), *out)
}
