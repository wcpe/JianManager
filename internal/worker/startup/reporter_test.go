package startup

import (
	"testing"
	"time"
)

// FR-486 范围②：启动进度上报器。
//
// 这些用例只验「不拖累启动」这条硬约束——上报是对外网络调用，必须保证
// 任何失败路径都不会把异常抛回启动主链路。真正的端到端上报由 CP 侧用例与真机验收覆盖。

// TestNewReporter_EmptyAddrDisables 无 CP 地址时不创建上报器。
//
// 为什么重要：单机/离线场景下 worker.yml 可能没有 control_plane。
// 若仍创建 reporter，后续每次上报都会尝试建连并耗尽超时——那是最坏的情况：
// 一个没有 CP 的 Worker 因为「要上报进度给不存在的 CP」而变慢。
func TestNewReporter_EmptyAddrDisables(t *testing.T) {
	if r := NewReporter(ReporterConfig{}); r != nil {
		t.Fatal("地址为空应返回 nil，调用方据此跳过上报")
	}
}

// TestReport_NilReceiverIsSafe 零值可用：装配层未注入时调用不得 panic。
func TestReport_NilReceiverIsSafe(t *testing.T) {
	var r *Reporter
	r.Report(PhaseConfig, time.Millisecond, time.Millisecond, 1, 7) // 不 panic 即通过
	r.Close()
}

// TestReport_UnreachablePSDoesNotBlock 关键约束：CP 不可达时上报必须**快速返回**。
//
// 这条守的是 FR-486 最容易搞坏的地方——为了观测启动，反而把启动拖慢。
// 断言上界取 reportTimeout + 宽裕量：若实现里用了 grpc 默认超时或无限重试，
// 这里会超时失败，直接把问题挡在合并前。
func TestReport_UnreachablePSDoesNotBlock(t *testing.T) {
	// 指向一个必然无人监听的本地端口（TEST-NET 语义，连接会被立即拒绝或超时）
	r := NewReporter(ReporterConfig{
		ControlPlaneAddr: "127.0.0.1:1",
		EnrollToken:      "test-token",
		NodeName:         "test-node",
	})
	if r == nil {
		t.Fatal("有地址时应创建 reporter")
	}
	defer r.Close()

	start := time.Now()
	r.Report(PhaseIngestStateLoad, 5*time.Second, 10*time.Second, 5, 7)
	elapsed := time.Since(start)

	// 允许 reportTimeout 的 3 倍余量（CI 抖动），但仍必须远小于「每阶段卡几十秒」
	if elapsed > 3*reportTimeout {
		t.Fatalf("CP 不可达时上报耗时过长：%v（应 ≤ %v）", elapsed, 3*reportTimeout)
	}

	// 首次失败后应永久禁用：第二次上报不应再付一次建连/超时代价
	if !r.disabled {
		t.Fatal("建连失败后应置 disabled，避免后续每个阶段都重付一次超时")
	}
	start2 := time.Now()
	r.Report(PhaseRegister, 0, 11*time.Second, 6, 7)
	if d := time.Since(start2); d > 50*time.Millisecond {
		t.Fatalf("已禁用后上报应近似零耗时，实际 %v", d)
	}
}

// TestClose_Idempotent 未建连时 Close 安全，重复 Close 也不 panic。
func TestClose_Idempotent(t *testing.T) {
	r := NewReporter(ReporterConfig{ControlPlaneAddr: "127.0.0.1:1"})
	r.Close()
	r.Close()
	// 未建连的 reporter 关闭后仍可安全再上报（会因 disabled 或重建连而失败，但不 panic）
	r.Report(PhaseConfig, 0, 0, 0, 7)
}
