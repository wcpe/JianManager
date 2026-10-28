package register

import (
	"testing"

	"github.com/wcpe/JianManager/internal/worker/dirusage"
)

// fakeWorkDirUsage 固定快照的工作目录占用采集器（FR-003）。
type fakeWorkDirUsage struct {
	usages map[string]dirusage.Usage
}

func (f fakeWorkDirUsage) Usages() map[string]dirusage.Usage { return f.usages }

// TestCollectHeartbeatCarriesWorkDirUsages FR-003：采集器的读数必须进入心跳负载。
//
// 与 FR-485/FR-513 同源理由：心跳负载是内存结构，真机上只能看到落库结果；落库为空时
// 无法区分「没人采到」与「采到了没写」。这里把「采集 → 负载」一环单独钉死。
func TestCollectHeartbeatCarriesWorkDirUsages(t *testing.T) {
	SetWorkDirUsageObserver(fakeWorkDirUsage{usages: map[string]dirusage.Usage{
		"inst-a": {Bytes: 3 << 30, Available: true},
		"inst-b": {Bytes: 0, Available: false},
	}})
	t.Cleanup(func() { SetWorkDirUsageObserver(nil) })

	req := CollectHeartbeatData("node-uuid")

	if len(req.WorkDirUsages) != 2 {
		t.Fatalf("应上报 2 个实例，实际 %d", len(req.WorkDirUsages))
	}
	// 稳定顺序（UUID 升序）：map 迭代无序会让同一份数据每拍产生不同字节序列。
	if req.WorkDirUsages[0].InstanceUuid != "inst-a" || req.WorkDirUsages[1].InstanceUuid != "inst-b" {
		t.Fatalf("上报顺序应为 UUID 升序，实际 %q, %q",
			req.WorkDirUsages[0].InstanceUuid, req.WorkDirUsages[1].InstanceUuid)
	}
	a := req.WorkDirUsages[0]
	if !a.Available || a.WorkDirBytes != 3<<30 {
		t.Fatalf("可用实例读数不符: %+v", a)
	}
	// 不可用实例必须置 available=false 且**不带字节数**：带了会让忽略 available 的
	// 消费方读到 0 并当成真实占用（低估 → 超卖）。
	b := req.WorkDirUsages[1]
	if b.Available {
		t.Fatal("统计失败的实例必须置 available=false")
	}
	if b.WorkDirBytes != 0 {
		t.Fatalf("不可用实例不得携带字节数，实际 %d", b.WorkDirBytes)
	}
}

// TestCollectHeartbeatOmitsWorkDirUsagesWhenNotInjected 未注入采集器时整组不上报。
//
// 关键语义：CP 侧「本拍没有某实例的数据」= 保持既有值不动，而不是写入 0——
// 写 0 会让组配额用量凭空归零。
func TestCollectHeartbeatOmitsWorkDirUsagesWhenNotInjected(t *testing.T) {
	SetWorkDirUsageObserver(nil)
	req := CollectHeartbeatData("node-uuid")
	if len(req.WorkDirUsages) != 0 {
		t.Fatalf("未注入采集器时不应上报，实际 %d 条", len(req.WorkDirUsages))
	}
}

// TestApplyWorkDirUsagesSkipsEmptyUUID 空 UUID 的条目必须被跳过（CP 侧按 uuid 定位行，
// 空 uuid 只是无效负载）。
func TestApplyWorkDirUsagesSkipsEmptyUUID(t *testing.T) {
	req := CollectHeartbeatData("node-uuid")
	applyWorkDirUsages(req, map[string]dirusage.Usage{
		"":       {Bytes: 100, Available: true},
		"inst-a": {Bytes: 200, Available: true},
	})
	if len(req.WorkDirUsages) != 1 || req.WorkDirUsages[0].InstanceUuid != "inst-a" {
		t.Fatalf("空 UUID 应被跳过，实际 %+v", req.WorkDirUsages)
	}
}
