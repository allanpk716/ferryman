# 票 05 · 配置写入器+三项体检(apply / --restore / doctor)

## What to build
新包 internal/provider 实现外科式配置写入器与体检:

1. **CC 写入**:~/.claude/settings.json 的 env.ANTHROPIC_BASE_URL→渡口地址;**其余一字不动**(他人键/hooks/plugins 原样;cc-switch 整文件重写是反面教材)。
2. **codex 两份写入**(~/.codex/config.toml 与 orca CODEX_HOME 那份,%APPDATA%/orca/codex-runtime-home/home/config.toml):base_url→渡口、保持 wire_api="responses"、`[features] hooks=true` 保住、PROXY_MANAGED 占位换 Ferryman 占位(渡口注入真钥)、其余节(mcp_servers/hooks.state 等)不碰。
3. **认证前置校验(F7)**:两份 codex 配置须为 apikey 形态(auth.json OPENAI_API_KEY 或 bearer 配置);chatgpt-OAuth 形态→报错转人工,不硬改。
4. **备份与还原(F5)**:首次写入前对三份配置逐一落时间戳备份(命名沿用本机既有 bak-ferryman 风格;orca 份现状无备份,由本项补齐);`--restore` 按最近一次接管前备份还原三份,回到 interim 拓扑。
5. **幂等**:重跑=校验+补缺(已正确项零写入);不做整文件重写。
6. **doctor 扩三项体检**:①CC 指向渡口;②codex 两份指向渡口且 wire_api=responses 且 hooks 旗标在位;③orca codex 健康(配置存在、指向渡口、认证形态合法)。挂进既有 doctor 检查清单与计数。
7. 单测:临时家目录覆盖真实世界三形态(cc-switch 代理形/直连形/orca 镜像形),断言外科性(他人键逐字节原样)、幂等(二跑零差异)、备份与 --restore 往返、doctor 真/假形态。**绝不写真机配置**。

## 验收标准
- [ ] 三形态输入的外科写入断言(他人键原样、目标行精确变更)
- [ ] 幂等:二跑零差异有断言
- [ ] 备份:三份齐(含 orca)、时间戳命名;--restore 还原回 interim 形态往返断言
- [ ] OAuth 形态拒绝转人工的断言
- [ ] doctor 三项真假形态断言+计数正确
- [ ] go test ./internal/provider/... ./internal/installer/... 全绿;测试全用临时目录,真机配置零触碰

## Blocked by
无,可立即开始

## 涉及路径
- internal/provider/(新建:writer.go/backup.go/doctor_probe.go + tests)
- internal/installer/doctor.go(+既有 doctor 测试文件同步扩)

## 副作用声明
- 独占验证命令:go test ./internal/provider/... ./internal/installer/...
- 测试全用 t.TempDir 仿家目录;不碰真机 ~/.claude、~/.codex、orca 家;不碰生产进程

decision_refs: D12, D9
review_blocks: F5, F7, F10
