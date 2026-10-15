package accounts

// gate_entry.go — 闸门入口标识的单一定义点（spec #34 评审 F3）：打字/作答两
// 入口（#36/ADR-0028）共用同一份闸门状态机，入口标识要落到三个消费方
// （daemon 判定与 block 记账、notify 通知文案、账本 block 科目的 entry 可选
// 字段），不得各自拼写裸串 "answer"——本包是三方都能导入的最低层（notify
// 不得 import daemon：daemon → notify 已是既有依赖向，反向成环）。
//
// 字符串底层保证线协议零变化：钩子 POST body 的 entry 值、账本行 entry 字段、
// notify 缝参数都照旧是裸串；常量是唯一拼写点，Go 侧比较经类型收窄。

// GateEntry 闸门入口标识：打字（UserPromptSubmit，缺省）与作答
// （PostToolUse+AskUserQuestion，#36/ADR-0028）。
type GateEntry string

const (
	// GateEntryTyping 打字入口＝空串（缺省）：旧钩子不带 entry 键、未知值也
	// 按打字处理（gate.go 既有语义——不猜未知值）。
	GateEntryTyping GateEntry = ""
	// GateEntryAnswer 作答入口：CC 的 PostToolUse (AskUserQuestion) 钩子分支
	// 发给闸门端点的 entry 值——作答与打字共用同一份台账判定（拦截阈值/
	// 有效交接/待交接标记/判热/三态/fail-open），差异仅在：强续前缀不适用
	// 作答、拦截文案「原话」换「你的选择」并带 cctrans.AnswerGateBlockMarker、
	// 记账/通知带入口标识。
	GateEntryAnswer GateEntry = "answer"
)
