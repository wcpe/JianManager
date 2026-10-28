package stateindex

import (
	"fmt"
	"path/filepath"
	"testing"
)

// 本文件是 FR-486 ③「优化大索引加载性能」的测量与验收夹具。
//
// 为什么单独成文件而不是复用 load_scaling_bench_fr486_test.go：那个文件只覆盖
// 「批次行随源数增长」这一种形态，用于回答「规模效应是否存在」；本文件用于回答
// 「加载路径的**单位行成本**由什么决定、优化后是否达标」，因此需要
//
//   - 明确的验收口径（10k / 100k 两个点位，报告 ms/op 与 us/row）；
//   - 多种行形态（batch / gap / wal）分别测量，因为三者的列宽、NULL 分布、
//     文本列数量都不同，混在一种形态里看不出各自的热点；
//   - 可重复性：同一份夹具既作优化前基线，也作优化后对照，避免「换了夹具才变快」。
//
// 口径：只测 Store.Load()（纯读取 + 全表扫描 + 行解码 + sortState），
// 不含 Open 与 Apply（那是写路径与迁移路径的成本，不是「重启后启动期加载」的成本）。
//
// # 已测得的结论（FR-486 ③，2026-10-26，供后人不必重做）
//
// 基线（本机 100k 批次行）：Load() ≈ 421 ms，单位成本 ≈ 4.2 µs/行，线性、不随规模超线性。
//
// profile 与分层实测表明**瓶颈在驱动，不在本包**：
//   - stateindex 自身代码仅占 ~3%；时间在 modernc.org/sqlite 的 rows.Next(31%)、
//     Xsqlite3VdbeExec(32%) 与逐列 API（columnText/columnType/columnDeclinedType 等 ~25%）；
//   - count(*) 只遍历 b-tree 不取列 = 3 ms；
//   - **仅驱动 rows.Next()、连 Scan 都不做**，取 5 列 = 357~477 ms。
//     也就是说上层无论怎么写，都突破不了这个地板：100k 行 ≤100 ms 意味着每列 ~0.2 µs，
//     而实测驱动单次列 API 就要 0.5~0.8 µs。
//
// 以下手段逐一实现并做过交错 A/B（只读句柄 + 中位数 + 固定 CPU），**均落在 ±15% 噪声内、
// 不可复现，故未采纳**：
//   - 去掉 SQL 侧 ORDER BY（load 末尾 sortState 已建立规范序，SQL 排序是空操作）：−12%~+9%，符号反复；
//     注意 load_plan_test.go 的「orderBy 必须与主键同序」断言是为**防临时 B-TREE 溢出**服务的，
//     与「能否省掉排序」是两个问题，不要为性能删除该不变量；
//   - 类型化扫描替代 scanRow→[]any→appendRow：±1%（分配次数 1,149,850 → 1,150,074，无变化）；
//   - 按行数预分配切片容量：−11%~−2%，不稳定；
//   - 并行分片读（多只读连接 + 键区间切分）：−5%~+7%，无收益；
//   - WITHOUT ROWID：−3%；
//   - PRAGMA mmap_size / 加大 cache_size：<2%；
//   - group_concat / 单列拼接 / json 聚合把多列压成一列：端到端更慢。
//
// 唯一确定性成立的一条：文本列改用 CAST(col AS BLOB) 取回，可绕开驱动 TEXT 分支的
// columnDeclinedType（内部 GoString+ToUpper，各一次分配）。分配次数是无噪声判据：
// 单文本列 400,021→200,020（每行省 2.00）、双文本列 700,021→400,021（每行 3.00）。
// 但其墙钟收益同样落在噪声内，单独立项不足以支撑改动。
//
// 启动期还有一处**比 Load 本身更大**的成本（已单独量化）：New() 先 openIndex() 的
// loadMirror 全表扫一遍建指纹镜像（≈593 ms），随后 load() 又 Load() 同一批行（≈488 ms），
// 同一份数据被完整扫了两遍，第 1 遍占启动期的 55%。
//
// 唯一能把量级压下去的是升级 modernc.org/sqlite（现 v1.23.1 → v1.61.0，同夹具
// Scan→typed 422 ms → 290 ms，约 −31%）；但它仍不足以到 100 ms，且属于跨仓库依赖变更。

// benchShape 是一份受控规模的索引内容形态。
type benchShape struct {
	name string
	// sources 是源数；它只决定 source/position/projection/source_aux 的行数（O(源数)），
	// 主体行数由下面三个乘数决定。
	sources int
	// batches/gaps/wal 是「每源」的行数乘数。
	batches int
	gaps    int
	wal     int
	// bodyLen 是 WAL 正文长度（0 表示引用条目，Body 为 nil）。
	bodyLen int
}

// rows 返回该形态的总行数（即被加载的行数）。
func (s benchShape) rows() int {
	return s.sources * (1 + 1 + 1 + 1 + s.batches + s.gaps + s.wal)
}

// buildBenchState 按形态构造期望状态。
//
// 刻意让文本列的长度贴近生产（源键是形如 inst:<uuid>/file/instance:<uuid>@worker:<uuid>
// 的长文本），因为加载路径的字符串分配成本与列宽直接相关——用 "src-1" 这种短键测出来的
// 数字会显著低估真实成本。
func buildBenchState(shape benchShape) State {
	perSource := 1 + 1 + 1 + 1 + shape.batches + shape.gaps + shape.wal
	st := State{
		Sources:     make([]SourceRow, 0, shape.sources),
		Positions:   make([]PositionRow, 0, shape.sources),
		Projections: make([]ProjectionRow, 0, shape.sources),
		Aux:         make([]AuxRow, 0, shape.sources),
		Batches:     make([]BatchRow, 0, shape.sources*shape.batches),
		Gaps:        make([]GapRow, 0, shape.sources*shape.gaps),
		WAL:         make([]WALRow, 0, shape.sources*shape.wal),
	}
	_ = perSource
	for i := 0; i < shape.sources; i++ {
		// 生产键形如 inst:147/file/instance:<uuid>@worker:<uuid>，长度约 85 字节。
		key := fmt.Sprintf("inst:%d/file/instance:2b1f4c8e-0000-4000-8000-%012d@worker:9a7d3e51-0000-4000-8000-%012d", i, i, i)
		st.Sources = append(st.Sources, SourceRow{
			Key:                key,
			LogSourceID:        fmt.Sprintf("log-source-%d", i),
			SourceGeneration:   fmt.Sprintf("generation-%d", i),
			StorageNamespace:   "storage-namespace-prod",
			UpdatedAtUnixMilli: int64(1700000000000 + i),
		})
		st.Positions = append(st.Positions, PositionRow{
			Key: key, ReadPos: uint64(i) * 4096, DurablePos: uint64(i) * 4096,
			ReclaimPos: uint64(i) * 1024, AcquirePaused: i%7 == 0, PauseReason: "manual-pause",
		})
		st.Projections = append(st.Projections, ProjectionRow{
			Key: key, Generation: fmt.Sprintf("projection-generation-%d", i),
			EventsStoredThrough: uint64(i) * 100, Pending: i%3 == 0,
			UpdatedAtUnixMilli: int64(1700000000000 + i),
		})
		st.Aux = append(st.Aux, AuxRow{
			Key: key, Config: []byte(`{"path":"/srv/minecraft/logs/latest.log","tags":["prod","eu"]}`),
			Payload: []byte(fmt.Sprintf(`{"generation":"g%d","pending":false}`, i)),
		})
		for b := 0; b < shape.batches; b++ {
			st.Batches = append(st.Batches, BatchRow{
				Key: key, Ordinal: int64(b), StartPos: uint64(b) * 10, EndPos: uint64(b)*10 + 10,
				State: "delivered",
			})
		}
		for g := 0; g < shape.gaps; g++ {
			st.Gaps = append(st.Gaps, GapRow{
				Key: key, ID: int64(g), StartPos: uint64(g) * 100, EndPos: uint64(g)*100 + 50,
				Reason: "truncated-window", Detail: "日志窗口被轮转截断，待人工解算",
				Resolved: false,
			})
		}
		for w := 0; w < shape.wal; w++ {
			row := WALRow{
				Key: key, Seq: uint64(w + 1), EventID: fmt.Sprintf("event-%d-%d", i, w),
				RecordStart: uint64(w) * 256, RecordEnd: uint64(w)*256 + 256,
				Appended: true, Durable: true,
			}
			if shape.bodyLen > 0 {
				row.Body = []byte(fmt.Sprintf(`{"level":"INFO","msg":"%s"}`, repeatByte('x', shape.bodyLen)))
			}
			row.FP = WALFingerprint(row)
			st.WAL = append(st.WAL, row)
		}
	}
	return st
}

func repeatByte(b byte, n int) string {
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = b
	}
	return string(buf)
}

// applyAll 反复调用 Apply 直到 Stats.Incomplete 为假（每周期总预算耗尽后续跑）。
//
// 为什么夹具必须这样做：DefaultCycleMaxRows=131072 看起来够用，但 DefaultCycleMaxDuration=3s
// 在本机（XEON E5-2630 v4，单核较慢 + 高负载）会先触发 —— 实测 100k 批次行首次 Apply 只落了
// 90,828 行就返回 Incomplete，基准因此读到「少了一万多行」的夹具，测出来的耗时会系统性偏低。
func applyAll(store *Store, st State) error {
	for round := 0; ; round++ {
		stats, err := store.Apply(st)
		if err != nil {
			return fmt.Errorf("第 %d 轮写入失败: %w", round+1, err)
		}
		if !stats.Incomplete {
			return nil
		}
		if round > 512 {
			return fmt.Errorf("写入 %d 轮仍未完成，夹具规模可能越过了预算设计前提", round+1)
		}
	}
}

// benchmarkLoad 是各形态共用的测量骨架：先落库（不计时），再反复 Load（计时）。
func benchmarkLoad(b *testing.B, shape benchShape) {
	b.Helper()
	path := filepath.Join(b.TempDir(), "index.db")
	store, err := Open(path)
	if err != nil {
		b.Fatalf("打开索引失败: %v", err)
	}
	defer func() { _ = store.Close() }()

	st := buildBenchState(shape)
	// Apply 受「每周期总预算」（DefaultCycleMaxRows/DefaultCycleMaxDuration）约束，
	// 一次调用写不完 10 万行就返回 Incomplete —— 必须续跑到完，否则夹具规模与测量规模不符。
	if err := applyAll(store, st); err != nil {
		b.Fatalf("写入期望状态失败: %v", err)
	}
	wantRows := shape.rows()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := store.Load()
		if err != nil {
			b.Fatalf("Load 失败: %v", err)
		}
		if len(got.Batches) != len(st.Batches) || len(got.Gaps) != len(st.Gaps) || len(got.WAL) != len(st.WAL) {
			b.Fatalf("读出规模不符：batches %d/%d gaps %d/%d wal %d/%d",
				len(got.Batches), len(st.Batches), len(got.Gaps), len(st.Gaps), len(got.WAL), len(st.WAL))
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(wantRows), "rows")
	// us/row 是跨规模可比的单位成本，用来判断「线性还是超线性」。
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(wantRows)/1000.0, "us/row")
}

// BenchmarkLoad100k 是验收基准：10k 与 100k 两个点位，验收线 ≤100ms @ 100k。
//
// 跑法（必须设置 TMPDIR，/tmp 分区曾满）：
//
//	TMPDIR=/home/wxys233/tt go test ./internal/worker/logs/ingest/stateindex/ \
//	    -run '^$' -bench BenchmarkLoad100k -benchtime 3x
func BenchmarkLoad100k(b *testing.B) {
	cases := []benchShape{
		// 主体是批次行：投递批次随投递次数增长，是稳态运行下最常见的形态。
		{name: "10k/batch", sources: 100, batches: 100},
		{name: "100k/batch", sources: 200, batches: 500},
	}
	for _, shape := range cases {
		shape := shape
		b.Run(shape.name, func(b *testing.B) { benchmarkLoad(b, shape) })
	}
}

// BenchmarkLoadShapes 分形态测量，用于 profile 时区分列宽/NULL/文本量带来的差异。
func BenchmarkLoadShapes(b *testing.B) {
	cases := []benchShape{
		// 缺口行：8 列、含两个可空 TEXT 列（detail/resolution），生产曾积累 74.7 万行。
		{name: "100k/gap", sources: 100, gaps: 1000},
		// WAL 引用条目：9 列、body 为 NULL，主键 5 列均为长文本/整数混合。
		{name: "100k/wal-ref", sources: 100, wal: 1000},
		// WAL 带正文：body 非空，考察 []byte 分配成本。
		{name: "100k/wal-body", sources: 100, wal: 1000, bodyLen: 256},
	}
	for _, shape := range cases {
		shape := shape
		b.Run(shape.name, func(b *testing.B) { benchmarkLoad(b, shape) })
	}
}
