// lanes_test.go — 断言器表驱动单测（票面验收标准②③的受测面）：
//   - 四痕各自缺一 → 对应项 fail（其余项不受染）；
//   - 压缩链 stub 掉服务面 → 服务面红灯且命令道通过不抵消（两道独立判定）；
//   - 契约缺失形态（注入「compactNow 不可用」）→ 记「契约缺失：服务面」红灯；
//   - 上报 ok 但归因到另一道＝本道 fail（执行未发生在本道，不抵消的归因面）；
//   - session-<uuid> 形态判定表。
package dshsandbox

import (
	"strings"
	"testing"
)

// greenTraces 四痕全过的基准事实。
func greenTraces() TraceFacts {
	return TraceFacts{
		GateDelta:     1,
		UsageRows:     2,
		SID:           "session-01234567-89ab-cdef-0123-456789abcdef",
		SIDSource:     "宿主 session/create 签发",
		HandoffOK:     true,
		HandoffDetail: "200",
	}
}

// greenCmdLane / greenSvcLane 两道各自的过线形态（命令道＝转录有命令生命周期；
// 服务面＝压缩痕无命令生命周期）。
func greenCmdLane() LaneFact {
	return LaneFact{Name: LaneCommandName, SessionID: "session-aaaa",
		Dispatched: true, ReportOK: true, ExecViaCommand: true,
		Detail: "上报 ok（prefix_tokens=8000）"}
}

func greenSvcLane() LaneFact {
	return LaneFact{Name: LaneServiceName, SessionID: "session-bbbb",
		Dispatched: true, ReportOK: true, ExecViaService: true,
		Detail: "上报 ok"}
}

func findItem(t *testing.T, items []ItemResult, name string) ItemResult {
	t.Helper()
	for _, it := range items {
		if it.Name == name {
			return it
		}
	}
	t.Fatalf("断言项 %s 不在结果里", name)
	return ItemResult{}
}

// TestEvaluateAllGreen 全过形态：六项全绿＋Summarize 全过短句。
func TestEvaluateAllGreen(t *testing.T) {
	items := Evaluate(greenTraces(), greenCmdLane(), greenSvcLane())
	if len(items) != 6 {
		t.Fatalf("项数=%d want 6", len(items))
	}
	if !AllGreen(items) {
		for _, it := range items {
			if !it.OK {
				t.Errorf("全过形态下 %s fail: %s", it.Name, it.Detail)
			}
		}
	}
	if s := Summarize(items); !strings.Contains(s, "全过") {
		t.Errorf("Summarize 全过短句异常: %s", s)
	}
}

// TestEvaluateTraceMissingOne 四痕各自缺一 → 对应项 fail、其余不染。
func TestEvaluateTraceMissingOne(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*TraceFacts)
		want string
	}{
		{"闸门未到达", func(f *TraceFacts) { f.GateDelta = 0 }, ChkGateArrival},
		{"事件上报断链", func(f *TraceFacts) { f.UsageRows = 0 }, ChkEventReport},
		{"会话键形态坏", func(f *TraceFacts) { f.SID = "session-xyz" }, ChkSessionKey},
		{"注入路径不可达", func(f *TraceFacts) { f.HandoffOK = false }, ChkInjectPath},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := greenTraces()
			tc.mut(&f)
			items := Evaluate(f, greenCmdLane(), greenSvcLane())
			if it := findItem(t, items, tc.want); it.OK {
				t.Errorf("%s 应 fail: %s", tc.want, it.Detail)
			}
			// 其余项不受染（四痕互不牵连；两道也不受四痕牵连）。
			for _, it := range items {
				if it.Name == tc.want {
					continue
				}
				if !it.OK {
					t.Errorf("%s 被牵连 fail: %s", it.Name, it.Detail)
				}
			}
		})
	}
}

// TestEvaluateServiceStubbedNotOffset 压缩链 stub 掉服务面（上报失败
// reason=error）→ 服务面红灯且命令道通过不抵消（验收标准②钉死句）。
func TestEvaluateServiceStubbedNotOffset(t *testing.T) {
	svc := greenSvcLane()
	svc.ReportOK = false
	svc.ReportReason = "error"
	items := Evaluate(greenTraces(), greenCmdLane(), svc)
	if it := findItem(t, items, ChkLaneCommand); !it.OK {
		t.Errorf("命令道应通过（不因服务面挂而降级）: %s", it.Detail)
	}
	if it := findItem(t, items, ChkLaneService); it.OK {
		t.Errorf("服务面应红灯（stub 掉即红，不因命令道过而豁免）")
	} else if !strings.Contains(it.Detail, "reason=error") {
		t.Errorf("服务面 fail detail 应带 reason: %s", it.Detail)
	}
	// 整体不绿（任何一道挂＝整体红——AllGreen 语义）。
	if AllGreen(items) {
		t.Errorf("一道挂时 AllGreen 应 false")
	}
	if s := Summarize(items); !strings.Contains(s, "lane_service") {
		t.Errorf("Summarize 应点名服务面未过: %s", s)
	}
}

// TestEvaluateServiceContractMissing 契约缺失形态：注入「compactNow 不可用」
// （ContractMissing）→ 记「契约缺失：服务面」红灯；命令道照常通过不抵消
// （验收标准③钉死句）。
func TestEvaluateServiceContractMissing(t *testing.T) {
	svc := LaneFact{Name: LaneServiceName, ContractMissing: true,
		Detail: "插件自报 no-compaction-channel（宿主无压缩通道）"}
	items := Evaluate(greenTraces(), greenCmdLane(), svc)
	if it := findItem(t, items, ChkLaneService); it.OK {
		t.Fatalf("契约缺失应红灯（不是跳过）")
	} else if !strings.Contains(it.Detail, "契约缺失：服务面") {
		t.Errorf("fail detail 应含「契约缺失：服务面」: %s", it.Detail)
	}
	if it := findItem(t, items, ChkLaneCommand); !it.OK {
		t.Errorf("命令道应通过（不抵消的另一面）: %s", it.Detail)
	}
}

// TestEvaluateCommandContractMissing 命令道契约缺失同样红灯（两道对称，
// 「契约缺失：命令道」形态）。
func TestEvaluateCommandContractMissing(t *testing.T) {
	cmd := LaneFact{Name: LaneCommandName, ContractMissing: true,
		Detail: "commands 服务面不可达"}
	items := Evaluate(greenTraces(), cmd, greenSvcLane())
	if it := findItem(t, items, ChkLaneCommand); it.OK || !strings.Contains(it.Detail, "契约缺失：命令道") {
		t.Errorf("命令道契约缺失应红灯且文案指名: %s", it.Detail)
	}
}

// TestEvaluateLaneMisattributedNotOffset 上报 ok 但执行归因到另一道＝本道
// fail——「各自执行」的归因面：服务面腿的派发走了命令道（合成组成失效的
// 典型形态）不得抵消。
func TestEvaluateLaneMisattributedNotOffset(t *testing.T) {
	// 服务面腿：上报 ok 但转录含命令生命周期（执行走了命令道）。
	svc := greenSvcLane()
	svc.ExecViaService = false
	svc.ExecViaCommand = true
	svc.Detail = "合成组成未生效（minimal preset 拒绝？）"
	items := Evaluate(greenTraces(), greenCmdLane(), svc)
	if it := findItem(t, items, ChkLaneService); it.OK {
		t.Fatalf("服务面未执行（执行走了命令道）应 fail——不抵消")
	} else if !strings.Contains(it.Detail, "未见服务面执行痕") {
		t.Errorf("misattribution detail 应指明未见服务面执行痕: %s", it.Detail)
	}
	// 对称面：命令道腿上报 ok 但无命令生命周期（执行走了服务面）。
	cmd := greenCmdLane()
	cmd.ExecViaCommand = false
	cmd.ExecViaService = true
	items = Evaluate(greenTraces(), cmd, greenSvcLane())
	if it := findItem(t, items, ChkLaneCommand); it.OK {
		t.Fatalf("命令道未执行应 fail——不抵消")
	}
}

// TestEvaluateNoDispatch 未派发形态：Dispatched=false → fail 指因（daemon
// 未触发/插件未领取）。
func TestEvaluateNoDispatch(t *testing.T) {
	svc := LaneFact{Name: LaneServiceName, SessionID: "session-b"}
	items := Evaluate(greenTraces(), greenCmdLane(), svc)
	if it := findItem(t, items, ChkLaneService); it.OK || !strings.Contains(it.Detail, "未见派发") {
		t.Errorf("未派发应 fail 且指因: %s", it.Detail)
	}
}

// TestEvaluateNoReport 派发了但窗口内无上报 → fail（「各自见到上报」面）。
func TestEvaluateNoReport(t *testing.T) {
	svc := greenSvcLane()
	svc.ReportOK = false
	svc.Dispatched = true
	items := Evaluate(greenTraces(), greenCmdLane(), svc)
	if it := findItem(t, items, ChkLaneService); it.OK {
		t.Errorf("无上报应 fail")
	}
}

// TestSIDIsSessionUUID 会话键形态表（宿主 session-${randomUUID()} 签发形状）。
func TestSIDIsSessionUUID(t *testing.T) {
	cases := []struct {
		sid  string
		want bool
	}{
		{"session-01234567-89ab-cdef-0123-456789abcdef", true},
		{"session-ffffffff-ffff-ffff-ffff-ffffffffffff", true},
		{"", false},
		{"session-", false},
		{"session-01234567-89ab-cdef-0123-456789abcdeg", false}, // 非 hex 尾
		{"session-0123456789ab-cdef-0123-456789abcdef", false},  // 分段错位
		{"session-01234567-89AB-CDEF-0123-456789ABCDEF", false}, // 大写不认（node randomUUID 恒小写）
		{"01234567-89ab-cdef-0123-456789abcdef", false},         // 缺前缀
		{"session-01234567-89ab-cdef-0123-456789abcde", false},  // 短一位
	}
	for _, tc := range cases {
		if got := SIDIsSessionUUID(tc.sid); got != tc.want {
			t.Errorf("SIDIsSessionUUID(%q)=%v want %v", tc.sid, got, tc.want)
		}
	}
}
