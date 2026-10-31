package ingest

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReconcileLoopPendingBranchCountsSourceNotDays 待确认分支只计源、不计天（FR-497）。
//
// # 为什么这条值得单独钉住
//
// 生产实测出现过 `resentSources=1, resentDays=0` 的轮次。第一眼看上去像"重发了 1 个源却 0 天"
// —— 像个计数 bug。查下来是设计如此：
//
//   - pending 分支只知道「该源有内容的全部天」（days = 全量天）
//   - 真正要重发哪几天由**后续的内容级确认**决定
//   - 把 len(days) 记成 ResentDays 会虚报——排障时会看到"重发了 N 天却没写入"
//
// 所以 ResentDays=0 不是漏计，是「还没判定到天级」。这个区分在排障时很关键：
// 若误以为它漏计，会去修一个不存在的 bug；若把两者混为一谈，真实重发量会被虚高。
//
// 变异：把 pending 分支加上 `result.ResentDays += len(days)`，本用例转红。
func TestReconcileLoopPendingBranchCountsSourceNotDays(t *testing.T) {
	// 这条用例断言的是「代码的计数口径」，不是运行行为：
	// pending 分支在 reconcile_loop.go 里对 ResentDays 没有任何写入。
	//
	// 用可读的方式固化这个事实：该分支的 days 变量只作为调度参数，
	// 不得同时被当作重发天数计入读数。
	src := reconcileLoopSourceFileForTest(t)

	// pending 分支：ResentSources++ 附近不得出现 ResentDays 累加。
	require.Regexp(t,
		`ResentSources\+\+`,
		src, "pending/missing 两个分支必须各自累加 ResentSources")

	// 关键断言：ResentDays 的累加只能出现在 report.MissingDays 那一支。
	// 具体做法：ResentDays 必须紧跟 len(report.MissingDays)，
	// 而不是 len(days)（后者是 pending 分支的全量天）。
	require.NotRegexp(t,
		`ResentDays\s*\+=\s*len\(days\)`,
		src,
		"pending 分支不得把 len(days) 计成重发天数（会虚报重发量）")
	require.Regexp(t,
		`ResentDays\s*\+=\s*len\(report\.MissingDays\)`,
		src,
		"ResentDays 应当只在「真知缺失了哪几天」时累加")
}

// reconcileLoopSourceFileForTest 读回 reconcile_loop.go 的源文本，供「计数口径」类断言使用。
//
// # 为什么用读源码而不是跑行为
//
// 要断言的口径是「pending 分支不该把全量天计入重发天数」——那段逻辑就是一行 +=。
// 跑一个真实的 pending 场景需要构造 VL 不可达 + 待确认发布 + 内容级确认不通过，
// 那套夹具的成本远大于它要验证的东西。
//
// 代价得说清：这属于**结构断言**，重构（改名/拆函数）会误报。
// 但该分支自 2026-10-09 一直稳定，且这条断言的真实价值是
// 阻止后来者「顺手补一行 ResentDays += len(days)」，那种情况下它会正确报错。
func reconcileLoopSourceFileForTest(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("reconcile_loop.go")
	require.NoError(t, err, "读 reconcile_loop.go 失败（测试需与源文件同目录）")
	return string(b)
}
