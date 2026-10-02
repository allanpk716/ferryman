# 票06 · 文档面:CLI.md + AGENTS.md + README 瘦身(agent 使用契约)

## What to build
面向 AI agent 的使用契约三件:①docs/CLI.md(新建):全部子命令×旗标×退出码(0/1/2 契约+空态语义)×--json 字段表(脱敏示例)×危险面标注(update 换装会停守护换盘面、install-* 写宿主配置、upstream use 中断在途请求、stop 停守护、provider apply 改四份宿主配置);以 usage 常量与票01/02/03/04 落定的实际行为为准逐一核对,不抄过时描述;②AGENTS.md(repo 根,新建):agent 用本程序的操作契约——何时用哪条命令(探活=doctor/status、停=stop、供应商=provider 族、升级=update)、输出怎么读(文本/--json)、错误处理(exit code 语义)、MCP 工具面(ferryman mcp 六件只读)、配置事实源(config.example.toml)、术语表指路(CONTEXT.md);③README 瘦身:删 Python 时代残留(uv run/e0/eval-set/eval 三命令已不存在),开发节改 ferryman.exe 直调+指路 cmd/ferryman/README.md 与 docs/CLI.md,待办区 status/stop 落地后同步勾销。

## 验收标准
- [ ] CLI.md 覆盖 run() 全部子命令与面板族 flags,每命令有退出码与危险面标注
- [ ] CLI.md 的 --json 字段表与实际输出一致(抽查三命令)
- [ ] AGENTS.md 自包含:新会话 agent 不读源码即可正确调用、知道输出与错误怎么读
- [ ] README 无 uv/e0/eval-set/eval 残留;全部命令示例 ferryman.exe 直调
- [ ] 文内交叉链接有效(相对路径)

## Blocked by
票01、票02、票03、票04(CLI 面最终形态先定,文档不写未来时)

## 涉及路径
docs/CLI.md(新建)
AGENTS.md(新建)
README.md

## 副作用声明
无验证命令;纯文档

## decision_refs
D8

## review_blocks
无
