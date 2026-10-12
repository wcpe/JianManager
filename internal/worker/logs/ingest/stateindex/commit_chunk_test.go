package stateindex

// 本文件是「索引持久化提交单元切分」（FR-498 P0）的回归，三条断言各自可独立转红：
//
//	① 每事务行数有界（TestApplySplitsCommitsWithinRowBudget）——无时序，绝对可复现；
//	② 单提交单元耗时 p95（TestSingleCommitLatencyBoundedAtScale）——与「不切分」对照臂做比值
//	   （机器速度与 -race 的放大倍数在比值里约去），另有非竞态下的验收线绝对断言；
//	③ 切分前后内容逐字段等价（TestApplyChunkedMatchesUnchunked）——全量/增量/删除三轮。
//
// 另有删除批量化的语句级证明（TestBatchDeleteRemovesExactlyTheMirroredRows）。

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// newTestStoreWithBudget 打开一个指定提交单元预算的临时索引库。
func newTestStoreWithBudget(t *testing.T, budget CommitBudget) *Store {
	t.Helper()
	store, err := OpenWithBudget(filepath.Join(t.TempDir(), "var", "log", "ingest.index.db"), budget)
	if err != nil {
		t.Fatalf("打开索引库失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// chunkFixtureState 构造一份「行数足够触发切分」的期望状态：sources 个源、每源 walPerSource 条
// WAL 行（行数主体）+ 根表行。与 fixtureState 的区别只在规模，形态一致。
func chunkFixtureState(sources, walPerSource int) State {
	var st State
	for i := 0; i < sources; i++ {
		key := fmt.Sprintf("node:%03d/g1", i)
		st.Sources = append(st.Sources, SourceRow{
			Key: key, LogSourceID: fmt.Sprintf("node:%03d", i), SourceGeneration: "g1",
			StorageNamespace: fmt.Sprintf("ns:%03d", i),
		})
		st.Positions = append(st.Positions, PositionRow{
			Key: key, ReadPos: uint64(100000 + i), DurablePos: uint64(99000 + i), ReclaimPos: uint64(98000 + i),
		})
		st.Projections = append(st.Projections, ProjectionRow{
			Key: key, Generation: "projection-1", EventsStoredThrough: uint64(97000 + i),
		})
		st.Aux = append(st.Aux, AuxRow{
			Key: key, Config: []byte(fmt.Sprintf(`{"log_source_id":"node:%03d"}`, i)),
			Payload: []byte(`{"segments":[]}`),
		})
		for seq := 1; seq <= walPerSource; seq++ {
			body := []byte(fmt.Sprintf(`{"event_id":"ev-%03d-%d","message":"line %d of node %03d"}`, i, seq, seq, i))
			st.WAL = append(st.WAL, WALRow{
				Key: key, Seq: uint64(seq), EventID: fmt.Sprintf("ev-%03d-%d", i, seq),
				RecordStart: uint64(seq * 200), RecordEnd: uint64(seq*200 + 199),
				Appended: true, Durable: true, Body: body, FP: fingerprint(body),
			})
		}
		st.Batches = append(st.Batches, BatchRow{
			Key: key, Ordinal: 0, StartPos: 1, EndPos: 100, State: "REQUEST_DONE",
		})
	}
	st.Instances = append(st.Instances, InstanceRow{
		UUID: "uuid-1", Namespace: "ns:000", Generation: "g1", Mode: "file_primary", WorkDir: "/srv/inst",
	})
	return st
}

// TestApplySplitsCommitsWithinRowBudget 断言「每事务行数有界」：`MaxRows` 之内的夹具必须被切分，
// 且每条采样（= 一个提交单元）的行数不超过预算。
//
// 转红说明：把 commitChunks 摘掉（退回「一次 ApplyScoped 一个事务写全部行」），采样数恒为 1、
// 该样本的行数等于全量行数（本夹具 700+ 行 > 128 的预算）⇒ `len(samples) > 1` 与
// `sample.Rows() <= MaxRows` 两条断言同时红。
func TestApplySplitsCommitsWithinRowBudget(t *testing.T) {
	budget := CommitBudget{MaxRows: 128, MinRows: 32, Target: 40 * time.Millisecond}
	store := newTestStoreWithBudget(t, budget)
	desired := chunkFixtureState(8, 48) // 8×(1+1+1+1+48+1)=424 行，按 128/单元应切成 ≥4 个单元

	stats, err := store.Apply(desired)
	if err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if stats.MaxChunkRows > budget.MaxRows {
		t.Fatalf("单提交单元行数越界：预算 %d，实测最大 %d", budget.MaxRows, stats.MaxChunkRows)
	}
	samples := store.Samples()
	if len(samples) != stats.Chunks {
		t.Fatalf("采样数必须等于提交单元数：samples=%d chunks=%d", len(samples), stats.Chunks)
	}
	if len(samples) < 2 {
		t.Fatalf("本夹具必须被切分（%d 行 > 预算 %d），实测只产生 %d 个提交单元",
			stats.RowsWritten+stats.RowsDeleted, budget.MaxRows, len(samples))
	}
	for index, sample := range samples {
		if sample.Rows() > budget.MaxRows {
			t.Fatalf("第 %d 个提交单元有 %d 行，超过预算 %d", index, sample.Rows(), budget.MaxRows)
		}
		if sample.ChunkIndex != index || sample.Chunks != len(samples) {
			t.Fatalf("周期结构必须自洽：第 %d 个样本报 index=%d chunks=%d（共 %d）",
				index, sample.ChunkIndex, sample.Chunks, len(samples))
		}
	}
	if latency := store.Latency(); latency.MaxRows > budget.MaxRows {
		t.Fatalf("分位口径的最大单单元行数越界：%d > %d", latency.MaxRows, budget.MaxRows)
	}
	// 周期合计必须与全量行数一致：切分不得丢行、不得重复写行。
	// 夹具行数 = 8×(source+position+projection+aux + 48 WAL + batch) + 1 instance = 425。
	if stats.RowsWritten != 425 {
		t.Fatalf("全量写入应落 425 行，实测 %d", stats.RowsWritten)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	assertSameState(t, desired, loaded)
}

// TestApplyChunkedMatchesUnchunked 断言切分不改变内容：同一份期望状态分别写进「不切分」
// （预算远大于夹具）与「强切分」（每单元 8 行）两个库，逐字段比对读回结果。
//
// 三轮覆盖三种行序形态：首轮全量（父表 + 子表一起写）、第二轮增量（改一个源 + 追加 WAL +
// 推进水位）、第三轮删除（裁掉一个源与部分行）——单元边界会落在每一轮的任意位置。
//
// 转红说明：单元边界若破坏「写入按父表在前、删除按子表在前」的顺序，删除轮会撞外键约束直接
// 报错；若单元内的镜像更新提前（提交前写镜像），下一轮的差异比对会漏行 ⇒ 逐字段比对红。
func TestApplyChunkedMatchesUnchunked(t *testing.T) {
	unchunked := newTestStoreWithBudget(t, CommitBudget{MaxRows: 1 << 20, MinRows: 1 << 20, Target: time.Hour})
	chunked := newTestStoreWithBudget(t, CommitBudget{MaxRows: 8, MinRows: 4, Target: 40 * time.Millisecond})

	base := chunkFixtureState(4, 12)

	// 首轮：全量。
	if _, err := unchunked.Apply(base); err != nil {
		t.Fatalf("不切分库写入失败: %v", err)
	}
	if _, err := chunked.Apply(base); err != nil {
		t.Fatalf("切分库写入失败: %v", err)
	}
	assertSameState(t, mustLoad(t, unchunked), mustLoad(t, chunked))

	// 第二轮：增量（改一个源的游标/水位 + 追加两条 WAL + 新增一个缺口）。
	incremental := cloneState(base)
	for index := range incremental.Positions {
		if incremental.Positions[index].Key == "node:001/g1" {
			incremental.Positions[index].ReadPos += 4096
			incremental.Positions[index].DurablePos += 4096
			incremental.Positions[index].ReclaimPos += 4096
		}
	}
	for seq := 13; seq <= 14; seq++ {
		body := []byte(fmt.Sprintf(`{"event_id":"ev-001-%d","message":"appended %d"}`, seq, seq))
		incremental.WAL = append(incremental.WAL, WALRow{
			Key: "node:001/g1", Seq: uint64(seq), EventID: fmt.Sprintf("ev-001-%d", seq),
			RecordStart: uint64(seq * 200), RecordEnd: uint64(seq*200 + 199),
			Appended: true, Durable: true, Body: body, FP: fingerprint(body),
		})
	}
	incremental.Gaps = append(incremental.Gaps, GapRow{
		Key: "node:001/g1", ID: 0, StartPos: 5000, EndPos: 5100, Reason: "PAUSED", Detail: "probe",
	})
	if _, err := unchunked.Apply(incremental); err != nil {
		t.Fatalf("不切分库增量写入失败: %v", err)
	}
	if _, err := chunked.Apply(incremental); err != nil {
		t.Fatalf("切分库增量写入失败: %v", err)
	}
	assertSameState(t, mustLoad(t, unchunked), mustLoad(t, chunked))

	// 第三轮：删除（裁掉最后一个源的根表行与其子行，并去掉一条 WAL 行）。
	trimmed := cloneState(incremental)
	trimmed.Sources = trimmed.Sources[:3]
	trimmed.Positions = trimmed.Positions[:3]
	trimmed.Projections = trimmed.Projections[:3]
	trimmed.Aux = trimmed.Aux[:3]
	trimmed.Batches = trimmed.Batches[:3]
	trimmed.Gaps = nil
	var keptWAL []WALRow
	for _, row := range trimmed.WAL {
		if row.Key == "node:003/g1" || row.Seq == 13 {
			continue
		}
		keptWAL = append(keptWAL, row)
	}
	trimmed.WAL = keptWAL
	if _, err := unchunked.Apply(trimmed); err != nil {
		t.Fatalf("不切分库删除轮失败: %v", err)
	}
	chunkStats, err := chunked.Apply(trimmed)
	if err != nil {
		t.Fatalf("切分库删除轮失败: %v", err)
	}
	if chunkStats.RowsDeleted == 0 {
		t.Fatal("删除轮必须真的删掉行（否则本用例没有覆盖删除路径）")
	}
	assertSameState(t, mustLoad(t, unchunked), mustLoad(t, chunked))

	// 两个库的提交单元数必须不同：证明「切分」这件事真的发生了（否则上面等价是平凡结论）。
	if len(chunked.Samples()) <= len(unchunked.Samples()) {
		t.Fatalf("强切分库的提交单元数应远多于不切分库，实测 %d vs %d",
			len(chunked.Samples()), len(unchunked.Samples()))
	}
}

// TestBatchDeleteRemovesExactlyTheMirroredRows 是删除批量化的语句级证明：用**镜像里的真实键**
// 逐表构造多值 DELETE，在同一个事务里按「子表在前」的顺序执行，断言删掉的行数与该表实际减少的
// 行数逐表相等（多值语句把值串位或漏删都会立刻暴露）。
//
// 它同时覆盖四种主键形态：单列（source/position/projection/source_aux）、两列（gap/
// delivery_batch）、五列（source_wal）、非日志源归属（instance_binding 的 uuid）。
//
// 转红说明：把 buildBatchDelete 的多列形态改成 `IN (VALUES ...)` 之外的写法（例如把主键列名
// 与值错位）时会撞 SQL 错误或删错行；把单列形态错写成多列行值也会直接报语法错误。
func TestBatchDeleteRemovesExactlyTheMirroredRows(t *testing.T) {
	store := newTestStore(t)
	desired := fixtureState(4)
	// fixtureState 只给 node:000 挂了缺口；这里补一条挂在被删源上的缺口，让八张表都被覆盖到。
	desired.Gaps = append(desired.Gaps, GapRow{
		Key: "node:001/g1", ID: 0, StartPos: 5000, EndPos: 5100, Reason: "PAUSED", Detail: "batch delete probe",
	})
	if _, err := store.Apply(desired); err != nil {
		t.Fatalf("准备夹具失败: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	before, err := store.counts(context.Background())
	if err != nil {
		t.Fatalf("统计行数失败: %v", err)
	}

	const victim = "node:001/g1"
	// instance_binding 的归属是 uuid，单独取一行。
	victimInstance := "uuid-1"
	// 删除顺序：子表在前（外键约束）。
	order := []int{tblDeliveryBatch, tblSourceWAL, tblSourceAux, tblInstanceBinding, tblProjection, tblGap, tblPosition, tblSource}
	expected := Counts{}
	conn, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatalf("取连接失败: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("开启事务失败: %v", err)
	}
	for _, table := range order {
		spec := specs[table]
		owner := victim
		if table == tblInstanceBinding {
			owner = victimInstance
		}
		var rows []pendingRow
		for key := range store.mirror[spec.name][owner] {
			rows = append(rows, pendingRow{table: table, row: rowData{owner: owner, key: key}})
		}
		if len(rows) == 0 {
			t.Fatalf("%s 在夹具里没有被删的行（用例必须覆盖全部八张表）", spec.name)
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].row.key < rows[j].row.key })
		chunkStats := Stats{Written: map[string]int{}, Deleted: map[string]int{}}
		if err := store.applyTableDeletes(context.Background(), conn, spec, rows, &chunkStats); err != nil {
			t.Fatalf("批量删除 %s 失败: %v", spec.name, err)
		}
		if chunkStats.RowsDeleted != len(rows) {
			t.Fatalf("%s 应删除 %d 行，实测 %d", spec.name, len(rows), chunkStats.RowsDeleted)
		}
		// 批量化的直接判据：本表被删的行必须由**一条**多值 DELETE 完成（逐行删除时这里等于行数）。
		if chunkStats.Statements != 1 {
			t.Fatalf("%s 的 %d 行删除应合并成 1 条语句，实测 %d 条（批量化失效？）",
				spec.name, len(rows), chunkStats.Statements)
		}
		switch table {
		case tblSource:
			expected.Sources = int64(chunkStats.RowsDeleted)
		case tblPosition:
			expected.Positions = int64(chunkStats.RowsDeleted)
		case tblGap:
			expected.Gaps = int64(chunkStats.RowsDeleted)
		case tblProjection:
			expected.Projections = int64(chunkStats.RowsDeleted)
		case tblInstanceBinding:
			expected.Instances = int64(chunkStats.RowsDeleted)
		case tblSourceAux:
			expected.Aux = int64(chunkStats.RowsDeleted)
		case tblSourceWAL:
			expected.WAL = int64(chunkStats.RowsDeleted)
		case tblDeliveryBatch:
			expected.Batches = int64(chunkStats.RowsDeleted)
		}
	}
	if _, err := conn.ExecContext(context.Background(), "COMMIT"); err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	// 必须先把连接还回池再统计行数：库是单连接（spec §2.1），持有连接时再开查询会自等。
	if err := conn.Close(); err != nil {
		t.Fatalf("归还连接失败: %v", err)
	}
	after, err := store.counts(context.Background())
	if err != nil {
		t.Fatalf("统计行数失败: %v", err)
	}
	got := Counts{
		Sources: before.Sources - after.Sources, Positions: before.Positions - after.Positions,
		Gaps: before.Gaps - after.Gaps, Projections: before.Projections - after.Projections,
		Instances: before.Instances - after.Instances, Aux: before.Aux - after.Aux,
		WAL: before.WAL - after.WAL, Batches: before.Batches - after.Batches,
	}
	if got != expected {
		t.Fatalf("各表实际减少的行数必须与批量语句声明的行数逐表相等：\n期望 %+v\n实测 %+v", expected, got)
	}
	if expected.Sources == 0 || expected.WAL == 0 || expected.Positions == 0 {
		t.Fatalf("夹具必须覆盖到根表与 WAL 行，实测 %+v", expected)
	}
}

// TestSingleCommitLatencyBoundedAtScale 是「单次持久化 ≤50ms」的规模回归。
//
// 判据分三层，前两层与机器速度无关（可在 CI/-race 下稳定成立），第三层只在非竞态构建下生效：
//
//	① 每提交单元行数 ≤ 预算（硬约束，无时序）；
//	② p95(切分臂) < max(不切分臂) / 4 —— 两臂在同一台机器、同一次运行里测，单行成本与
//	   -race 的放大倍数在比值里约去；夹具按规模换算：13200 行 ÷ 1024 行/单元 ≈ 13 个单元，
//	   比值应有 ≈13× 的余量，故 4× 的阈值既稳又能转红；
//	③ 非竞态下直接断言 p95 ≤ 50 ms（验收线本身）。
//
// 转红说明：摘掉切分（一次事务写完全部行）时两臂等价 ⇒ ② 的比值退化为 1 倍并立刻红，
// 同时 ③ 在无竞态下也会红（实测单事务 13k 行 ≈ 数百毫秒）。
func TestSingleCommitLatencyBoundedAtScale(t *testing.T) {
	if testing.Short() {
		t.Skip("规模时延用例在 -short 下跳过")
	}
	const rows = 13200
	desired := chunkFixtureState(44, 300)
	if total := len(desired.WAL) + len(desired.Sources)*5 + len(desired.Instances); total < rows-500 {
		t.Fatalf("夹具规模不足：实测约 %d 行", total)
	}

	unbounded := newTestStoreWithBudget(t, CommitBudget{MaxRows: 1 << 20, MinRows: 1 << 20, Target: time.Hour})
	bounded := newTestStoreWithBudget(t, DefaultCommitBudget())

	if _, err := unbounded.Apply(desired); err != nil {
		t.Fatalf("不切分臂写入失败: %v", err)
	}
	stats, err := bounded.Apply(desired)
	if err != nil {
		t.Fatalf("切分臂写入失败: %v", err)
	}

	// ① 硬约束：每提交单元行数 ≤ 默认预算。
	budget := DefaultCommitBudget()
	for index, sample := range bounded.Samples() {
		if sample.Rows() > budget.MaxRows {
			t.Fatalf("第 %d 个提交单元有 %d 行，超过默认预算 %d", index, sample.Rows(), budget.MaxRows)
		}
	}
	if stats.Chunks < 8 {
		t.Fatalf("13200 行按 %d 行/单元应切成 ≥8 个单元，实测 %d", budget.MaxRows, stats.Chunks)
	}

	// ② 比值：切分臂的 p95 必须显著低于不切分臂的单次整轮耗时。
	boundedLatency := bounded.Latency()
	unboundedLatency := unbounded.Latency()
	if boundedLatency.P95*4 >= unboundedLatency.Max {
		t.Fatalf("切分臂 p95 未显著低于不切分臂整轮耗时：p95=%v，整轮=%v（两者同量级说明切分没生效）",
			boundedLatency.P95, unboundedLatency.Max)
	}
	t.Logf("切分臂 p95=%v max=%v 单元数=%d；不切分臂整轮=%v",
		boundedLatency.P95, boundedLatency.Max, stats.Chunks, unboundedLatency.Max)

	// ③ 绝对验收线（竞态构建下按开关收窄：-race 会把写入路径整体放慢数倍）。
	if !raceEnabled && boundedLatency.P95 > 50*time.Millisecond {
		t.Fatalf("非竞态构建下 p95 必须 ≤50ms（验收线），实测 %v", boundedLatency.P95)
	}
}

// cloneState 复制一份期望状态（深拷贝切片，避免两轮之间互相污染）。
func cloneState(st State) State {
	out := st
	out.Sources = append([]SourceRow(nil), st.Sources...)
	out.Positions = append([]PositionRow(nil), st.Positions...)
	out.Gaps = append([]GapRow(nil), st.Gaps...)
	out.Projections = append([]ProjectionRow(nil), st.Projections...)
	out.Instances = append([]InstanceRow(nil), st.Instances...)
	out.Aux = append([]AuxRow(nil), st.Aux...)
	out.WAL = append([]WALRow(nil), st.WAL...)
	out.Batches = append([]BatchRow(nil), st.Batches...)
	return out
}

func mustLoad(t *testing.T, store *Store) State {
	t.Helper()
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	return loaded
}
