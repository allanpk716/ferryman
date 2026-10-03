# 票 03 · 插件骨架 ferryman-dsh＋挂载自检＋测试地基

## What to build
仓内新子目录 `plugin/ferryman-dsh/`（TypeScript、零运行时依赖，分发先 `file:./`）：插件 manifest 与入口骨架、生命周期挂接点（pre-step/created/session-event/disposed/status 五事件位留接口，本票只搭骨不实现业务逻辑——业务对接是票 05）、挂载自检（对渡口路由做健康检查＋daemon 版本对账，自检失败用户可见）、零依赖测试地基——`node --test --experimental-strip-types`（本机 Node v22.14 已验）可跑的最小测试样例（自检逻辑先行 TDD）。协议事实（插件 manifest 形状、事件挂接 API、agent.inject 语义）对调研克隆 dsh 插件体系源码钉夹具注释（标注 file:line）。

## 验收标准
- [ ] `plugin/ferryman-dsh/` 骨架落盘：manifest＋入口＋五事件位接口＋自检实现
- [ ] 自检：健康检查/版本对账逻辑有表驱动测试且 `node --test --experimental-strip-types` 跑绿（命令与输出落日志）
- [ ] 零依赖：无 node_modules、无 package.json 依赖项（dev 亦无）
- [ ] 协议事实夹具注释带源码 file:line 出处

## Blocked by
无，可立即开始

## 涉及路径
plugin/ferryman-dsh/

## 副作用声明
测试用 `node --test --experimental-strip-types`（本机已装 Node v22.14，无需安装任何东西）；输出落 `.scratch/dsh-phase2-finish/logs/t03-*.log`；不联网。

## decision_refs
D11（测试纪律）、spec「插件 ferryman-dsh」节

## review_blocks
无
