package stateindex

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSQLStatsCountsSlowStatementsOnly 钉住 FR-485 慢查询计数的三条判据：
//   - 正常语句不计数；
//   - 超过阈值才计数并更新最大值；
//   - 未装配（nil Store）安全返回零值，不 panic。
//
// 为什么不真的造一条 50ms 的 SQL：那会让用例本身跑 50ms 起（CI 上更久），
// 且「SQL 有多慢」取决于机器而非逻辑。这里直接驱动观测器本身——
// 被测的是「计数口径」，不是 sqlite 的延迟。
func TestSQLStatsCountsSlowStatementsOnly(t *testing.T) {
	s := &Store{}

	// 快语句：不计数。
	s.observeSQL("fast")()
	require.Zero(t, s.SQLStats().SlowQueries, "低于阈值不得计数")

	// 慢语句：计数并更新最大值。
	done := s.observeSQL("slow")
	time.Sleep(slowQueryThreshold + 5*time.Millisecond)
	done()

	st := s.SQLStats()
	require.EqualValues(t, 1, st.SlowQueries, "超过阈值必须计数")
	require.GreaterOrEqual(t, st.SlowQueryMax, slowQueryThreshold,
		"最大耗时不得小于阈值")
	require.False(t, st.SlowQueryLastAt.IsZero(), "慢语句必须留下时刻")

	// 再来一条更慢的：最大值必须前进，计数累加。
	done2 := s.observeSQL("slower")
	time.Sleep(slowQueryThreshold + 20*time.Millisecond)
	done2()
	st2 := s.SQLStats()
	require.EqualValues(t, 2, st2.SlowQueries)
	require.Greater(t, st2.SlowQueryMax, st.SlowQueryMax, "最大值必须单调前进")
}

// TestSQLStatsNilStoreSafe 钉住「未装配时抓 /metrics 不得 panic」。
//
// 这条契约与 retention 侧三个 Source 接口一致：带类型的 nil 指针塞进接口时
// `src != nil` 为真，所以实现方法自身必须对 nil 安全。
func TestSQLStatsNilStoreSafe(t *testing.T) {
	var s *Store
	st := s.SQLStats()
	require.Zero(t, st.SlowQueries)
	require.Zero(t, st.SlowQueryMax)
	require.True(t, st.SlowQueryLastAt.IsZero())

	// observeSQL 的 nil 形态也必须安全（返回可调用的空函数）。
	s.observeSQL("x")()
}
