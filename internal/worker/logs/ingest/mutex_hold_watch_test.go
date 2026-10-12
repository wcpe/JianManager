package ingest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 本文件是「候选 2：m.mu 慢持锁观测」的确定性红/绿：
//
// 现场：m.mu 是全局热锁（代码侧 61 个调用点、pprof 点名 14 个等待者），但"谁持锁最久"
// 此前**无任何测量** ⇒ 收窄只能靠猜。修法：在关键临界区（投递规划/校验凭据/落段标记）
// 上加计时，超阈（100ms）计数并按节流窗口告警 —— 纯观测、零语义改动。
//
// 绿（现状）：慢持锁（>100ms）计数 +1；快持锁不计；红（变异：阈值改 1 小时）⇒ 慢持锁不计数 ⇒ 转红。
func TestMutexHoldWatchCountsSlowHold(t *testing.T) {
	m := &Manager{}
	// 预置告警节流窗口：本用例只验证计数，不打日志（避免测试输出噪声）。
	m.mutexHoldWarnAt.Store(time.Now().UnixNano())

	slow := m.watchMutexHold("test.slow")
	time.Sleep(120 * time.Millisecond)
	slow.done()
	require.Equal(t, int64(1), m.MutexHoldSlowCount(), "超阈值持锁必须计数")

	fast := m.watchMutexHold("test.fast")
	fast.done()
	require.Equal(t, int64(1), m.MutexHoldSlowCount(), "未超阈值不得计数")
}

// 变异记录（2026-10-04 实测，逐字还原）：
//   - 变异：`mutexHoldSlowThreshold` 由 100ms 改为 time.Hour ⇒ 慢持锁不计数 ⇒ 首断言转红；还原后转绿。
