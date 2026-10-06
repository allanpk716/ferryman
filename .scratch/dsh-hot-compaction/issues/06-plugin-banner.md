# 票06 · 浏览器横幅："本会话已压缩归档，直接继续"

## What to build
宿主半面观察会话压缩完成事件（事件名从克隆查证钉入代码注释），经既有卡片数据通道（ferrymanBlocked Remote 或新增 banner 数据源）通知浏览器半面；client.js 新增横幅组件：复用 conversation.composer.dock 槽+窄容器浮层先例，无按钮，文案"本会话已压缩归档（交接已存档），直接继续"，展示一次后随用户下次发消息消失。

## 验收标准
- [ ] node --test 全绿：横幅组件渲染（fake React 先例）+ 显示/消失状态机
- [ ] 事件观察点带克隆 file:line 注释
- [ ] 既有测试零回归

## Blocked by
票05（数据通道在轮询臂侧同族）

## 涉及路径
- plugin/ferryman-dsh/src/（事件观察）
- plugin/ferryman-dsh/client.js（横幅组件）
- plugin/ferryman-dsh/test/（横幅测试）

## 副作用声明
默认只跑 node --test

decision_refs: D3
review_blocks: 无
