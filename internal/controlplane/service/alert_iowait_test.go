package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// FR-485：iowait 与 cpu 同为百分比型指标（unit=pct），必须「自身即饱和度」。
//
// 变异验证：把 MetricNodeIOWait 从 saturationPercent 的 case 白名单里去掉，
// 本测试立即转红（返回 (0,false)）。这正是切片 4b 修掉的静默失效缺口——
// 它不报错、不告警，只是让饱和度型 iowait 规则永不触发。
func TestSaturationPercent_IOWaitIsSelfPercent(t *testing.T) {
	v := 83.5
	pct, ok := saturationPercent(model.MetricNodeIOWait, &v, nil)
	require.True(t, ok, "iowait 应被识别为百分比型指标（自身即饱和度）")
	require.InDelta(t, 83.5, pct, 1e-9)

	// 对照：磁盘已用指标走 used/max 分支，无配对上限时不得当成百分比处理，
	// 否则「磁盘百分比」会退化成拿字节数当百分比比较。
	disk := 512.0
	if _, ok := saturationPercent(model.MetricNodeDiskUsed, &disk, nil); ok {
		t.Fatal("无配对上限的 disk_used 不应被判为百分比型")
	}

	// 对照：磁盘在「有上限」时确实走 used/max → 百分比（条目要求的另一半）
	limit := 1024.0
	pct, ok = saturationPercent(model.MetricNodeDiskUsed, &disk, &limit)
	require.True(t, ok)
	require.InDelta(t, 50.0, pct, 1e-9)
}
