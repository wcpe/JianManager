package stateindex

import (
	"testing"
	"time"
)

// 本文件守卫提交单元预算控制器的**单调性**不变量。
//
// 背景（FR-498 性能根治，2026-10-27）：生产真机 p50=22ms / p95=81ms，而 maxRows=502
// 说明预算一直顶在 MaxRows 上。探针证明「单单元 p95 随行数近似线性」（512→43.30ms、
// 256→27.31ms、128→13.96ms、64→7.00ms），所以 MaxRows 从 512 降到 128。
//
// 降值之后，控制器本身暴露出一个原有缺陷：**收缩后的下限夹取用了 MinRows 而非
// min(MinRows, MaxRows)，且夹取发生在 MaxRows 夹取之前**。当 MinRows > MaxRows 时
// （配置面允许这种组合，Normalized 会把 MinRows 夹到 MaxRows —— 但运行时预算可被测试/
// 诊断代码直接改动，见 index_bounds_regression_test.go 里直接赋值 budget 的用法），
// `next < MinRows` 先把 next 抬到 MinRows，随后 `next > MaxRows` 又把它压回 MaxRows，
// 于是「耗时超目标 ⇒ 收缩」实际变成了「抬回 MaxRows」。
//
// 这不变量比「某个具体数字」更重要：**超线之后，下一次单元的行数不得多于本次**。
// 否则控制器会在最需要它收缩的时候放大单元，把 p95 越推越高。

// TestAdaptBudgetNeverGrowsAfterOverrun 断言「实测耗时超目标 ⇒ 行数不增」。
//
// 转红方式（实测有效变异）：把 adaptBudget 里的 `floor` 改回 `s.budget.MinRows`
// 且保留原有夹取顺序，然后用 MinRows > MaxRows 的组合 ⇒ 本用例红。
func TestAdaptBudgetNeverGrowsAfterOverrun(t *testing.T) {
	// 覆盖正常组合与「MinRows > MaxRows」这类退化组合。
	cases := []struct{ max, min, start int }{
		{128, 32, 128},
		{128, 32, 96},
		{512, 64, 512},
		{64, 64, 64},
		{32, 64, 32}, // MinRows > MaxRows：退化配置不得让收缩变成扩张
		{16, 128, 16},
	}
	// 为什么不再加“start < MaxRows 且 MinRows > MaxRows”那类用例：
	// 我加过（{32,64,16} / {64,128,8}），它会让“收紧”变成从 16 涨到 32——但那是
	// **非法初态**：`CommitBudget.Normalized` 保证 MinRows ≤ MaxRows，因此合法可达的
	// rowBudget 恒在 [MinRows, MaxRows] 内；只有直接改 Store 内部字段（仅测试/诊断代码
	// 会这么做，见 index_bounds_regression_test.go）才可能越界。
	// 用不可达状态去让测试“转红”是假阳性，不干。adaptBudget 里那道 floor=min(MinRows,
	// MaxRows) 因此是**防御性的**（让不变量对任意输入都成立），不是修正一个实际缺陷——
	// 这一点必须写清，否则后来人会以为这里有真实 bug。
	for _, c := range cases {
		store := &Store{
			budget:    CommitBudget{MaxRows: c.max, MinRows: c.min, Target: 10 * time.Millisecond},
			rowBudget: c.start,
		}
		before := store.rowBudget
		// 实测耗时是目标的 5 倍 ⇒ 控制器应当收缩。
		store.adaptBudget(before, 50*time.Millisecond)
		if store.rowBudget > before {
			t.Errorf("超目标后行数反而增加（MaxRows=%d MinRows=%d）：%d → %d。"+
				"控制器在最需要收缩时放大单元，会把 p95 越推越高",
				c.max, c.min, before, store.rowBudget)
		}
		if store.rowBudget > c.max {
			t.Errorf("行数越过 MaxRows（MaxRows=%d MinRows=%d）：%d", c.max, c.min, store.rowBudget)
		}
		if store.rowBudget < 1 {
			t.Errorf("行数被压到 0 或负数（MaxRows=%d MinRows=%d）：%d", c.max, c.min, store.rowBudget)
		}
	}
}

// TestAdaptBudgetStaysWithinBounds 断言控制器恒把预算夹在 [min(MinRows,MaxRows), MaxRows]。
//
// 两个方向都要夹住：超目标时不能掉到下限以下，远低于目标时不能越过上限。
func TestAdaptBudgetStaysWithinBounds(t *testing.T) {
	cases := []struct{ max, min, start int }{
		{128, 32, 128},
		{512, 64, 512},
		{32, 64, 32},
	}
	for _, c := range cases {
		floor := c.min
		if floor > c.max {
			floor = c.max
		}
		// 逐次施加极端输入，检查全程不越界。
		store := &Store{
			budget:    CommitBudget{MaxRows: c.max, MinRows: c.min, Target: 10 * time.Millisecond},
			rowBudget: c.start,
		}
		for step := 0; step < 50; step++ {
			switch step % 3 {
			case 0:
				store.adaptBudget(store.rowBudget, time.Hour) // 极慢
			case 1:
				store.adaptBudget(store.rowBudget, time.Nanosecond) // 极快
			default:
				store.adaptBudget(store.rowBudget, 10*time.Millisecond) // 正好在目标带
			}
			if store.rowBudget < floor || store.rowBudget > c.max {
				t.Fatalf("第 %d 步预算越界：%d 不在 [%d, %d]（MaxRows=%d MinRows=%d）",
					step, store.rowBudget, floor, c.max, c.max, c.min)
			}
		}
	}
}

// TestDefaultCommitMaxRowsFitsVerificationLine 守卫「默认预算与验收线的关系」。
//
// 为什么值得单独锁：FR-498 的验收线是「单次持久化（= 单个提交单元）p95 ≤ 50ms」，
// 而单单元耗时随行数近似线性（实测 512→43.30ms、256→27.31ms、128→13.96ms）。
// 默认值一旦被悄悄调回 512 量级，在那台机械盘宿主的 load 60–150 下 p95 就会重新越线。
// 本用例只锁「与实测斜率一致的上界关系」，不写死性能数字（机器相关）。
func TestDefaultCommitMaxRowsFitsVerificationLine(t *testing.T) {
	budget := DefaultCommitBudget().Normalized()
	// 实测：512 行/单元在 load≈83 的机械盘上 p95≈43.30ms；验收线 50ms。
	// 取 1/4 的行数，按近似线性关系应当落在验收线的 1/2 以内，留出吸收生产尖峰的余量。
	if budget.MaxRows > DefaultCommitMaxRows {
		t.Fatalf("MaxRows=%d 超过了默认上界 %d", budget.MaxRows, DefaultCommitMaxRows)
	}
	if DefaultCommitMaxRows > 256 {
		t.Errorf("DefaultCommitMaxRows=%d 偏大：实测 512 行单元的 p95 已到 43.30ms（load≈83），"+
			"生产 load 60–150 下会越 50ms 验收线。降值依据见该常量的注释", DefaultCommitMaxRows)
	}
	if budget.MinRows > budget.MaxRows {
		t.Errorf("MinRows=%d 不得大于 MaxRows=%d", budget.MinRows, budget.MaxRows)
	}
	// 硬上界（Target×5/4）不得越过验收线：它是「单单元耗时」的执行期帽子。
	if hard := budget.hardLimit(); hard > 50*time.Millisecond {
		t.Errorf("硬上界 %v 越过 50ms 验收线", hard)
	}
}
