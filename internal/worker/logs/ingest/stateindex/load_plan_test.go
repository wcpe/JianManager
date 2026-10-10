package stateindex

import (
	"strings"
	"testing"
)

// 本文件守卫「加载路径不得产生临时 B-TREE 排序」这一不变量。
//
// 背景（生产事故）：source_wal 的 orderBy 曾写成 "seq, event_id, record_start, record_end"，
// 漏掉了主键首列 key。SQLite 只有在 ORDER BY 命中主键索引前缀时才省掉排序，从 seq 起排
// 对不上 (key, seq, ...) 前缀，于是**每一行都要进 TEMP B-TREE 物化**。该表在生产积累到
// 711 万行、body 合计约 5GB 后，排序数据溢出到 /var/tmp；空间不足时 SQLite 直接返回
// `database or disk is full (13)`，Worker 启动 6 次重试全败，采集永久停摆。
//
// 为什么用执行计划断言而不是「大表跑一遍测耗时」：耗时断言依赖机器且要在磁盘上造 GB 级
// 数据；而执行计划里的 `USE TEMP B-TREE FOR ORDER BY` 是这次事故的**直接成因**，也是
// 独立于数据量的确定性信号——有它就是线性放大到溢出，没有它主键索引直接出序。

// TestLoadQueriesAvoidTempBTree 断言每张表的加载查询都走索引出序、不落临时 B-TREE。
func TestLoadQueriesAvoidTempBTree(t *testing.T) {
	store := newTestStore(t)

	// 建表后立刻插入每个表的样例行，保证计划分析面对的是真实存在的索引。
	// 仅靠 CREATE TABLE 就足够让主键索引存在，但插行能让个别表的统计更接近实际。
	ctx := t.Context()
	state := State{
		Sources:     []SourceRow{{Key: "inst:1"}},
		Positions:   []PositionRow{{Key: "inst:1"}},
		Gaps:        []GapRow{{Key: "inst:1", ID: 1}},
		Projections: []ProjectionRow{{Key: "inst:1"}},
		Instances:   []InstanceRow{{UUID: "u1"}},
		Aux:         []AuxRow{{Key: "inst:1"}},
		WAL: []WALRow{{
			Key: "inst:1", Seq: 1, EventID: "e1", RecordStart: 1, RecordEnd: 2,
			Body: []byte("x"),
		}},
		Batches: []BatchRow{{Key: "inst:1", Ordinal: 0}},
	}
	for i := range state.WAL {
		state.WAL[i].FP = WALFingerprint(state.WAL[i])
	}
	if _, err := store.Apply(state); err != nil {
		t.Fatalf("写入样例状态失败: %v", err)
	}

	for index := range specs {
		spec := specs[index]
		columns := strings.Join(loadColumns[index], ", ")
		query := "EXPLAIN QUERY PLAN SELECT " + columns + " FROM " + spec.name + " ORDER BY " + spec.orderBy

		rows, err := store.db.QueryContext(ctx, query)
		if err != nil {
			t.Fatalf("%s: 取执行计划失败: %v", spec.name, err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				_ = rows.Close()
				t.Fatalf("%s: 扫描执行计划失败: %v", spec.name, err)
			}
			plan = append(plan, detail)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("%s: 遍历执行计划失败: %v", spec.name, err)
		}

		joined := strings.Join(plan, " | ")
		if strings.Contains(joined, "TEMP B-TREE") {
			t.Errorf("%s 的加载查询会落临时 B-TREE（ORDER BY 未命中索引）\n  计划: %s\n  orderBy: %s\n  keyCols: %v\n"+
				"修复方向：让 orderBy 与 keyCols（主键）同序，使 ORDER BY 命中主键索引前缀",
				spec.name, joined, spec.orderBy, spec.keyCols)
		}
	}
}

// TestOrderByMatchesPrimaryKey 断言每张表的加载排序键与主键列同序。
//
// 这是上一条断言的**成因守卫**：ORDER BY 之所以能走主键索引，正是因为二者同序。
// 直接锁住这个不变量，比只依赖执行计划更早失败、更容易定位（不必等 SQLite 版本变化）。
func TestOrderByMatchesPrimaryKey(t *testing.T) {
	for index := range specs {
		spec := specs[index]
		want := strings.Join(spec.keyCols, ", ")
		if spec.orderBy != want {
			t.Errorf("%s 的 orderBy 与主键不同序：\n  orderBy = %q\n  keyCols = %q\n"+
				"二者同序时 SQLite 才能用主键索引出序；不同序会退化为全表排序（生产曾因此把 "+
				"GB 级排序数据落到 /var/tmp 并报 database or disk is full）",
				spec.name, spec.orderBy, want)
		}
	}
}

// TestLoadRoundTripPreservesWALOrder 断言改排序键后 Load 的内存结果仍按规范顺序（lessWAL）。
//
// 对齐主键改变了 SQL 的行序（原先跨源按 seq 全局排序，现在按源分组），load 末尾的
// sortState 会重新排成 lessWAL 规范序。本用例锁住「跨源乱序写入、加载后仍规范有序」，
// 防止将来有人以为 SQL 已排好而删掉 sortState。
func TestLoadRoundTripPreservesWALOrder(t *testing.T) {
	store := newTestStore(t)

	// 刻意让两个源交错，且写入顺序与规范序不同。
	state := State{
		Sources: []SourceRow{{Key: "inst:1"}, {Key: "inst:2"}},
		WAL: []WALRow{
			{Key: "inst:2", Seq: 5, EventID: "e", RecordStart: 1, RecordEnd: 2, Body: []byte("b2")},
			{Key: "inst:1", Seq: 9, EventID: "e", RecordStart: 1, RecordEnd: 2, Body: []byte("a9")},
			{Key: "inst:1", Seq: 3, EventID: "e", RecordStart: 1, RecordEnd: 2, Body: []byte("a3")},
			{Key: "inst:2", Seq: 1, EventID: "e", RecordStart: 1, RecordEnd: 2, Body: []byte("b1")},
		},
	}
	for i := range state.WAL {
		state.WAL[i].FP = WALFingerprint(state.WAL[i])
	}
	if _, err := store.Apply(state); err != nil {
		t.Fatalf("写入状态失败: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if len(loaded.WAL) != len(state.WAL) {
		t.Fatalf("WAL 行数不符：得到 %d，期望 %d", len(loaded.WAL), len(state.WAL))
	}
	for i := 1; i < len(loaded.WAL); i++ {
		if lessWAL(loaded.WAL[i], loaded.WAL[i-1]) {
			t.Errorf("第 %d 行违反了 lessWAL 规范序：\n  前一行 = %+v\n  当前行 = %+v",
				i, loaded.WAL[i-1], loaded.WAL[i])
		}
	}
	// 规范序应以 (Key, Seq) 升序：inst:1/3, inst:1/9, inst:2/1, inst:2/5。
	wantOrder := []struct {
		key string
		seq uint64
	}{{"inst:1", 3}, {"inst:1", 9}, {"inst:2", 1}, {"inst:2", 5}}
	for i, want := range wantOrder {
		if loaded.WAL[i].Key != want.key || loaded.WAL[i].Seq != want.seq {
			t.Errorf("第 %d 行应为 %s/%d，实际 %s/%d",
				i, want.key, want.seq, loaded.WAL[i].Key, loaded.WAL[i].Seq)
		}
	}
}

// TestLoadOnLargeWALStaysBounded 用较多行验证加载不会退化为二次方或不落盘失败。
//
// 数据量刻意远小于生产（生产 711 万行），只作为「排序键对齐后加载仍正确」的冒烟，
// 真正的成因由前两条执行计划断言把关。
func TestLoadOnLargeWALStaysBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("数据量较大，-short 下跳过")
	}
	store := newTestStore(t)

	const perSource = 500
	state := State{Sources: []SourceRow{{Key: "inst:1"}, {Key: "inst:2"}}}
	for _, key := range []string{"inst:1", "inst:2"} {
		for i := 0; i < perSource; i++ {
			row := WALRow{
				Key: key, Seq: uint64(i + 1), EventID: "e",
				RecordStart: uint64(i), RecordEnd: uint64(i + 1),
				Body: []byte(strings.Repeat("x", 64)),
			}
			row.FP = WALFingerprint(row)
			state.WAL = append(state.WAL, row)
		}
	}
	if _, err := store.Apply(state); err != nil {
		t.Fatalf("写入 %d 行失败: %v", len(state.WAL), err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("加载 %d 行失败: %v", len(state.WAL), err)
	}
	if len(loaded.WAL) != len(state.WAL) {
		t.Fatalf("WAL 行数不符：得到 %d，期望 %d", len(loaded.WAL), len(state.WAL))
	}
}
