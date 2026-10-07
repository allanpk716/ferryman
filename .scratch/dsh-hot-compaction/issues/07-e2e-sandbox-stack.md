# 票07 · E2E 沙箱栈：隔离 daemon + 隔离 profile web 实例

## What to build
tools/e2e_dsh/ 可复用夹具：①沙箱 daemon 启动脚本（测试 config：端口 25xxx、ttl 秒级、独立数据目录，不碰 ~/ferryman）；②隔离 profile 构建脚本（复制 profiles/web 为测试 profile、插件 daemonURL 指沙箱、独立 web 端口 25xxx）；③一键起停+健康检查。生产 3080/15700/15722/3081 与生产 profile 全程零接触。

## 验收标准
- [ ] 脚本可一键起栈、健康检查过（daemon /stats 应答、web 实例端口监听）、一键拆栈无残留（进程与端口干净）
- [ ] 脚本内硬校验：目标端口必须在 25xxx 段，越界即拒跑
- [ ] 运行两次幂等（栈已在则复用/重建均可，不叠进程）
- [ ] 沙箱 daemon 起的是本仓测试构建（go build 到临时产物），不经生产 exe

## Blocked by
无，可立即开始

## 涉及路径
- tools/e2e_dsh/（新建：脚本+README）
- 只读引用：~/.dsh/profiles/web（复制源）

## 副作用声明
端口 25xxx 段；起停进程；写独立临时数据目录；禁碰生产端口与生产数据目录

decision_refs: D5 D7
review_blocks: F3
