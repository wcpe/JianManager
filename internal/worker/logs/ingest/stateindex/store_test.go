package stateindex

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// 本文件是 stateindex 的包内用例（需要访问 testCrashHook 等内部注入点）。
// 其中崩溃用例以**子进程**形式运行：只有真正的进程异常终止（os.Exit / SIGKILL）才能验证
// 「未提交事务整体丢弃」——在同一进程内 pánic 或 return 都会走到 defer 里的 ROLLBACK，
// 那验证的是回滚语句而非崩溃语义。

// newTestStore 打开一个临时目录下的索引库。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "var", "log", "ingest.index.db"))
	if err != nil {
		t.Fatalf("打开索引库失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestStoreJournalModeIsWAL(t *testing.T) {
	store := newTestStore(t)
	var mode string
	if err := store.db.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("读取 journal_mode 失败: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("spec §2.1 要求 journal_mode=WAL，实测 %q", mode)
	}
	// 单写者单连接：连接池上限必须为 1。
	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("单写者要求连接上限为 1，实测 %d", got)
	}
}

// fixtureState 构造一份覆盖全部表的期望状态。
func fixtureState(sources int) State {
	var st State
	for i := 0; i < sources; i++ {
		key := fmt.Sprintf("node:%03d/g1", i)
		st.Sources = append(st.Sources, SourceRow{
			Key: key, LogSourceID: fmt.Sprintf("node:%03d", i), SourceGeneration: "g1",
			StorageNamespace: fmt.Sprintf("ns:%03d", i),
		})
		st.Positions = append(st.Positions, PositionRow{
			Key: key, ReadPos: uint64(1000 + i), DurablePos: uint64(900 + i), ReclaimPos: uint64(800 + i),
		})
		st.Projections = append(st.Projections, ProjectionRow{
			Key: key, Generation: "projection-1", EventsStoredThrough: uint64(700 + i),
		})
		st.Aux = append(st.Aux, AuxRow{
			Key:    key,
			Config: []byte(fmt.Sprintf(`{"log_source_id":"node:%03d","path":"/tmp/%03d.log"}`, i, i)),
			// payload 里放一段「随源数量增长」的内容，用于验证大规模夹具下的增量写入。
			Payload: []byte(`{"segments":[` + strings.Repeat(`{"path":"seg"},`, 8) + `{}]}`),
		})
		for seq := 1; seq <= 4; seq++ {
			body := []byte(fmt.Sprintf(`{"event_id":"ev-%03d-%d","message":"m%d"}`, i, seq, seq))
			st.WAL = append(st.WAL, WALRow{
				Key: key, Seq: uint64(seq), EventID: fmt.Sprintf("ev-%03d-%d", i, seq),
				RecordStart: uint64(seq * 10), RecordEnd: uint64(seq*10 + 9),
				Appended: true, Durable: seq < 4, Body: body,
				FP: fingerprint(body),
			})
		}
		st.Batches = append(st.Batches, BatchRow{
			Key: key, Ordinal: 0, StartPos: 1, EndPos: 100, State: "REQUEST_DONE",
		})
	}
	st.Instances = append(st.Instances, InstanceRow{
		UUID: "uuid-1", Namespace: "ns:000", Generation: "g1", Mode: "file_primary", WorkDir: "/srv/inst",
	})
	st.Gaps = append(st.Gaps, GapRow{
		Key: "node:000/g1", ID: 0, StartPos: 10, EndPos: 20, Reason: "PAUSED", Detail: "disk 95.0%",
	})
	return st
}

// normalizeForCompare 归一化两份状态以便逐字段比对：清掉写入路径专用字段与索引内部元数据
// （updated_at 是「该行内容最后变更时刻」，不属于上层状态，只用于 sqlite3 排障）。
func normalizeForCompare(st State) State {
	cleaned := State{
		Gaps: st.Gaps, Instances: st.Instances, Aux: st.Aux, Batches: st.Batches,
	}
	for _, row := range st.Sources {
		row.UpdatedAtUnixMilli = 0
		cleaned.Sources = append(cleaned.Sources, row)
	}
	for _, row := range st.Projections {
		row.UpdatedAtUnixMilli = 0
		cleaned.Projections = append(cleaned.Projections, row)
	}
	cleaned.Positions = st.Positions
	for _, row := range st.WAL {
		row.FP = 0
		cleaned.WAL = append(cleaned.WAL, row)
	}
	sortState(&cleaned)
	return cleaned
}

func TestStoreRoundTripsEveryTable(t *testing.T) {
	store := newTestStore(t)
	desired := fixtureState(3)
	stats, err := store.Apply(desired)
	if err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	if stats.RowsWritten == 0 {
		t.Fatal("首次写入应产生行")
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	assertSameState(t, desired, loaded)

	// 重新打开（模拟重启）：内容必须逐字段一致。
	path := store.Path()
	if err := store.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	if err := reopened.IntegrityCheck(); err != nil {
		t.Fatalf("integrity_check 未通过: %v", err)
	}
	reloaded, err := reopened.Load()
	if err != nil {
		t.Fatalf("重新读取失败: %v", err)
	}
	assertSameState(t, desired, reloaded)
}

// assertSameState 断言两份状态逐字段一致（含 NULL/空串等形态）。
func assertSameState(t *testing.T, want, got State) {
	t.Helper()
	normalizedWant := normalizeForCompare(want)
	normalizedGot := normalizeForCompare(got)
	if diff := stateDiff(normalizedWant, normalizedGot); diff != "" {
		t.Fatalf("索引内容与期望不一致：\n%s", diff)
	}
}

// stateDiff 返回两份状态的差异描述（空串表示一致）。
func stateDiff(want, got State) string {
	var differences []string
	compare := func(name string, countWant, countGot int) {
		if countWant != countGot {
			differences = append(differences, fmt.Sprintf("%s 行数 want=%d got=%d", name, countWant, countGot))
		}
	}
	compare("source", len(want.Sources), len(got.Sources))
	compare("position", len(want.Positions), len(got.Positions))
	compare("gap", len(want.Gaps), len(got.Gaps))
	compare("projection", len(want.Projections), len(got.Projections))
	compare("instance_binding", len(want.Instances), len(got.Instances))
	compare("source_aux", len(want.Aux), len(got.Aux))
	compare("source_wal", len(want.WAL), len(got.WAL))
	compare("delivery_batch", len(want.Batches), len(got.Batches))
	if len(differences) > 0 {
		return strings.Join(differences, "\n")
	}
	for i := range want.Sources {
		if fmt.Sprintf("%+v", want.Sources[i]) != fmt.Sprintf("%+v", got.Sources[i]) {
			differences = append(differences, fmt.Sprintf("source[%d] want=%+v got=%+v", i, want.Sources[i], got.Sources[i]))
		}
	}
	for i := range want.Positions {
		if fmt.Sprintf("%+v", want.Positions[i]) != fmt.Sprintf("%+v", got.Positions[i]) {
			differences = append(differences, fmt.Sprintf("position[%d] want=%+v got=%+v", i, want.Positions[i], got.Positions[i]))
		}
	}
	for i := range want.Gaps {
		if fmt.Sprintf("%+v", want.Gaps[i]) != fmt.Sprintf("%+v", got.Gaps[i]) {
			differences = append(differences, fmt.Sprintf("gap[%d] want=%+v got=%+v", i, want.Gaps[i], got.Gaps[i]))
		}
	}
	for i := range want.Projections {
		if fmt.Sprintf("%+v", want.Projections[i]) != fmt.Sprintf("%+v", got.Projections[i]) {
			differences = append(differences, fmt.Sprintf("projection[%d] want=%+v got=%+v", i, want.Projections[i], got.Projections[i]))
		}
	}
	for i := range want.Instances {
		if fmt.Sprintf("%+v", want.Instances[i]) != fmt.Sprintf("%+v", got.Instances[i]) {
			differences = append(differences, fmt.Sprintf("instance[%d] want=%+v got=%+v", i, want.Instances[i], got.Instances[i]))
		}
	}
	for i := range want.Aux {
		if fmt.Sprintf("%+v", want.Aux[i]) != fmt.Sprintf("%+v", got.Aux[i]) {
			differences = append(differences, fmt.Sprintf("aux[%d] want=%+v got=%+v", i, want.Aux[i], got.Aux[i]))
		}
	}
	for i := range want.WAL {
		if fmt.Sprintf("%+v", want.WAL[i]) != fmt.Sprintf("%+v", got.WAL[i]) {
			differences = append(differences, fmt.Sprintf("wal[%d] want=%+v got=%+v", i, want.WAL[i], got.WAL[i]))
		}
	}
	for i := range want.Batches {
		if fmt.Sprintf("%+v", want.Batches[i]) != fmt.Sprintf("%+v", got.Batches[i]) {
			differences = append(differences, fmt.Sprintf("batch[%d] want=%+v got=%+v", i, want.Batches[i], got.Batches[i]))
		}
	}
	return strings.Join(differences, "\n")
}

// TestStoreApplyWritesOnlyChangedRows 是「只写变更行」的存储层回归：全量写入一次后，
// 只改一个源的一行，第二次写入必须只写极少量行（退回整本重写会让 RowsWritten ≈ 全量行数）。
func TestStoreApplyWritesOnlyChangedRows(t *testing.T) {
	store := newTestStore(t)
	desired := fixtureState(64)
	first, err := store.Apply(desired)
	if err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	if first.RowsWritten < 64 {
		t.Fatalf("首次写入应落全部行，实测 %d", first.RowsWritten)
	}
	// 幂等重复写入：不得写任何行，也不得开事务（空闲轮询不得重写历史）。
	again, err := store.Apply(desired)
	if err != nil {
		t.Fatalf("幂等写入失败: %v", err)
	}
	if again.RowsWritten != 0 || again.RowsDeleted != 0 {
		t.Fatalf("内容未变时必须零写入，实测写 %d 删 %d", again.RowsWritten, again.RowsDeleted)
	}
	if _, opened := store.Samples()[len(store.Samples())-1].Duration, true; opened {
		last := store.Samples()[len(store.Samples())-1]
		if last.RowsWritten != 0 {
			t.Fatalf("空闲采样应记录零写入，实测 %d", last.RowsWritten)
		}
	}

	// 只推进一个源的游标 + 追加一条 WAL。
	changed := desired
	changed.Positions = append([]PositionRow(nil), desired.Positions...)
	changed.Positions[7].ReadPos += 4096
	changed.WAL = append(append([]WALRow(nil), desired.WAL...), WALRow{
		Key: "node:007/g1", Seq: 5, EventID: "ev-007-5",
		RecordStart: 50, RecordEnd: 59, Appended: true,
		Body: []byte(`{"event_id":"ev-007-5","message":"new"}`), FP: 0xabc,
	})
	second, err := store.Apply(changed)
	if err != nil {
		t.Fatalf("增量写入失败: %v", err)
	}
	if second.RowsWritten > 2 {
		t.Fatalf("单源变更只应写 1~2 行（游标 + 新 WAL 条目），实测 %d 行 / %d 字节",
			second.RowsWritten, second.BytesWritten)
	}
	if second.BytesWritten > 4096 {
		t.Fatalf("单源变更的写入字节应远小于全量，实测 %d 字节", second.BytesWritten)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	assertSameState(t, changed, loaded)
}

// TestStoreDeletesRemovedRows 验证行被移出期望状态时确实删除（源被移除、缺口被去掉）。
func TestStoreDeletesRemovedRows(t *testing.T) {
	store := newTestStore(t)
	desired := fixtureState(3)
	if _, err := store.Apply(desired); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	trimmed := desired
	trimmed.Sources = desired.Sources[:2]
	trimmed.Positions = desired.Positions[:2]
	trimmed.Projections = desired.Projections[:2]
	trimmed.Aux = desired.Aux[:2]
	trimmed.Batches = desired.Batches[:2]
	trimmed.WAL = desired.WAL[:4]
	trimmed.Gaps = nil
	stats, err := store.Apply(trimmed)
	if err != nil {
		t.Fatalf("裁剪写入失败: %v", err)
	}
	if stats.RowsDeleted == 0 {
		t.Fatal("被移除的行必须删除")
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	assertSameState(t, trimmed, loaded)
	if len(loaded.Gaps) != 0 {
		t.Fatalf("缺口应被清空，实测 %d 行", len(loaded.Gaps))
	}
}

// TestMirrorKeyRoundTrip 覆盖含分隔符的主键（日志源标识/路径可能自带 "|"）。
func TestMirrorKeyRoundTrip(t *testing.T) {
	cases := [][]any{
		{"node:1/g1"},
		{"inst:9/file|with|pipes/g1"},
		{"node:1/g1", int64(3)},
		{"weird|key", int64(7), "ev|id", uint64(10), uint64(20)},
		{"k", ""},
		{"k", nil},
	}
	for _, values := range cases {
		spec := tableSpec{name: "t", keyCols: make([]string, len(values))}
		decoded, err := decodeMirrorKey(spec, mirrorKey(values...))
		if err != nil {
			t.Fatalf("解码 %v 失败: %v", values, err)
		}
		if len(decoded) != len(values) {
			t.Fatalf("解码 %v 得到 %d 段", values, len(decoded))
		}
		for i := range values {
			if fmt.Sprintf("%v", decoded[i]) != fmt.Sprintf("%v", values[i]) {
				t.Fatalf("解码 %v 的第 %d 段为 %v", values, i, decoded[i])
			}
		}
	}
}

// --- 崩溃语义 -------------------------------------------------------------

// crashHelperDirEnv 是崩溃子进程的库目录环境变量。
const crashHelperDirEnv = "JM_STATEINDEX_CRASH_DIR"

// TestStateIndexCrashHelper 是崩溃子进程的入口（父用例通过 -test.run 定向拉起）。
//
// 模式由目录下的 mode 文件给出：
//   - exit：写入批次 1 后，在批次 2 的事务中途 os.Exit（未提交事务必须被丢弃）；
//   - kill：持续写批次，等父进程 SIGKILL（验证已提交前缀完整、无半截批次）。
func TestStateIndexCrashHelper(t *testing.T) {
	dir := os.Getenv(crashHelperDirEnv)
	if dir == "" {
		t.Skip("非崩溃子进程，跳过")
	}
	mode, err := os.ReadFile(filepath.Join(dir, "mode"))
	if err != nil {
		t.Fatalf("读取模式失败: %v", err)
	}
	store, err := Open(filepath.Join(dir, "ingest.index.db"))
	if err != nil {
		t.Fatalf("子进程打开索引失败: %v", err)
	}
	// 批次 1：完整提交。
	if _, err := store.Apply(batchState(1)); err != nil {
		t.Fatalf("子进程写入批次 1 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "batch1.done"), []byte("1"), 0o600); err != nil {
		t.Fatalf("写标记失败: %v", err)
	}
	switch strings.TrimSpace(string(mode)) {
	case "exit":
		// 批次 2：事务中途异常终止——提交前 os.Exit(1)，不走任何 defer/Rollback。
		store.testCrashHook = func() { os.Exit(1) }
		if _, err := store.Apply(batchState(2)); err != nil {
			t.Fatalf("子进程写入批次 2 失败: %v", err)
		}
		os.Exit(0) // 不该走到这里：hook 必须已终止进程
	case "kill":
		// 持续写入，等父进程强杀；每批一批一事务，故被杀时库中只应存在完整的批次前缀。
		for batch := 2; ; batch++ {
			if _, err := store.Apply(batchState(batch)); err != nil {
				t.Fatalf("子进程写入批次 %d 失败: %v", batch, err)
			}
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("batch%d.done", batch)), []byte("1"), 0o600); err != nil {
				t.Fatalf("写标记失败: %v", err)
			}
			time.Sleep(5 * time.Millisecond)
		}
	default:
		t.Fatalf("未知模式 %q", mode)
	}
}

// batchRows 是每批写入的行数（批次边界用于判定「无半截批次」）。
const batchRows = 5

// batchState 构造「前 n 批」的期望状态：**逐批累加**——第 n 批的 Apply 必须保留第 1..n-1 批的
// 行，只追加本批 5 行并推进游标/投影（这与真实持久化的语义一致：索引只增不减）。
// 第 n 批 WAL 行的 seq 区间为 [n*batchRows, (n+1)*batchRows)。
func batchState(n int) State {
	key := "node:crash/g1"
	st := State{
		Sources: []SourceRow{{Key: key, LogSourceID: "node:crash", SourceGeneration: "g1", StorageNamespace: "ns:crash"}},
		Positions: []PositionRow{{
			Key: key, ReadPos: uint64(n * batchRows), DurablePos: uint64(n * batchRows), ReclaimPos: 0,
		}},
		Projections: []ProjectionRow{{Key: key, Generation: fmt.Sprintf("projection-%d", n)}},
	}
	for batch := 1; batch <= n; batch++ {
		for i := 0; i < batchRows; i++ {
			seq := batch*batchRows + i
			body := []byte(fmt.Sprintf(`{"event_id":"ev-%d","message":"batch %d"}`, seq, batch))
			st.WAL = append(st.WAL, WALRow{
				Key: key, Seq: uint64(seq), EventID: fmt.Sprintf("ev-%d", seq),
				RecordStart: uint64(seq * 10), RecordEnd: uint64(seq*10 + 9),
				Appended: true, Durable: true, Body: body, FP: fingerprint(body),
			})
		}
		st.Batches = append(st.Batches, BatchRow{
			Key: key, Ordinal: int64(batch), StartPos: uint64((batch - 1) * batchRows), EndPos: uint64(batch * batchRows),
			State: "REQUEST_DONE",
		})
	}
	return st
}

// runCrashHelper 拉起崩溃子进程并返回其退出错误（nil 表示正常退出）。
func runCrashHelper(t *testing.T, dir, mode string) *exec.Cmd {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte(mode), 0o600); err != nil {
		t.Fatalf("写模式文件失败: %v", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestStateIndexCrashHelper$", "-test.v")
	cmd.Env = append(os.Environ(), crashHelperDirEnv+"="+dir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

// TestStoreCrashDiscardsUncommittedBatch 是「崩溃不丢」回归的主用例：
// 子进程在批次 2 的事务中途被异常终止（未提交），重启后索引必须仍可读、且只包含完整批次——
// 回退幅度不超过一个批次。
//
// 转红条件：若 Apply 不用事务（逐行 autocommit），批次 2 已写入的行会残留，
// 「无批次 2 的任何行」断言即失败；若提交时机不明确（先改内存后落库或先落库后校验），
// 也会被「已提交批次必须完整」与重复打开的一致性断言抓住。
func TestStoreCrashDiscardsUncommittedBatch(t *testing.T) {
	dir := t.TempDir()
	cmd := runCrashHelper(t, dir, "exit")
	if err := cmd.Run(); err == nil {
		t.Fatal("崩溃子进程应异常退出")
	}
	if _, err := os.Stat(filepath.Join(dir, "batch1.done")); err != nil {
		t.Fatalf("子进程应已完成批次 1：%v", err)
	}
	store, err := Open(filepath.Join(dir, "ingest.index.db"))
	if err != nil {
		t.Fatalf("崩溃后重新打开索引失败: %v", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.IntegrityCheck(); err != nil {
		t.Fatalf("崩溃后 integrity_check 未通过: %v", err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("崩溃后读取失败: %v", err)
	}
	// 已提交的批次 1 一行不少（seq 区间 [5,10)）。
	if len(loaded.WAL) != batchRows {
		t.Fatalf("已提交批次必须完整保留：期望 %d 行，实测 %d 行", batchRows, len(loaded.WAL))
	}
	for _, row := range loaded.WAL {
		if row.Seq < uint64(batchRows) || row.Seq >= uint64(2*batchRows) {
			t.Fatalf("崩溃后应只剩批次 1（seq∈[5,10)），发现 seq=%d", row.Seq)
		}
	}
	if len(loaded.Positions) != 1 || loaded.Positions[0].ReadPos != uint64(batchRows) {
		t.Fatalf("游标必须停在批次 1，实测 %+v", loaded.Positions)
	}
	// 索引必须还能继续用（追加批次 2 成功且内容正确）。
	if _, err := store.Apply(batchState(2)); err != nil {
		t.Fatalf("崩溃后写入失败: %v", err)
	}
	after, err := store.Load()
	if err != nil {
		t.Fatalf("崩溃后再次读取失败: %v", err)
	}
	if len(after.WAL) != 2*batchRows {
		t.Fatalf("恢复后应能续写：期望 %d 行，实测 %d", 2*batchRows, len(after.WAL))
	}
}

// TestStoreSIGKILLLeavesCompletePrefix 用真实 kill -9 验证：被强杀后库中只存在**完整的批次前缀**
// （行数必为 batchRows 的整数倍），不出现半截批次。
func TestStoreSIGKILLLeavesCompletePrefix(t *testing.T) {
	if testing.Short() {
		t.Skip("SIGKILL 用例在 -short 下跳过")
	}
	dir := t.TempDir()
	cmd := runCrashHelper(t, dir, "kill")
	if err := cmd.Start(); err != nil {
		t.Fatalf("拉起子进程失败: %v", err)
	}
	marker := filepath.Join(dir, "batch3.done")
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatal("子进程未在期限内写到批次 3")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("强杀子进程失败: %v", err)
	}
	_ = cmd.Wait()

	store, err := Open(filepath.Join(dir, "ingest.index.db"))
	if err != nil {
		t.Fatalf("强杀后重新打开索引失败: %v", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.IntegrityCheck(); err != nil {
		t.Fatalf("强杀后 integrity_check 未通过: %v", err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("强杀后读取失败: %v", err)
	}
	// 已观测到批次 3 的写入，故已提交前缀至少 3 批（≥15 行）。
	if len(loaded.WAL) < 3*batchRows {
		t.Fatalf("已观测到批次 3，提交前缀不得少于 3 批，实测 %d 行", len(loaded.WAL))
	}
	if len(loaded.WAL)%batchRows != 0 {
		t.Fatalf("强杀后不得出现半截批次：行数 %d 不是 %d 的整数倍", len(loaded.WAL), batchRows)
	}
	// 行 seq 必须是「完整批次」的并集：恰好 5..count+4（末尾绝不出现半截批次的行）。
	seqs := make(map[uint64]bool, len(loaded.WAL))
	maxSeq := uint64(0)
	for _, row := range loaded.WAL {
		seqs[row.Seq] = true
		if row.Seq > maxSeq {
			maxSeq = row.Seq
		}
	}
	if want := uint64(len(loaded.WAL)) + batchRows; maxSeq+1 != want {
		t.Fatalf("末尾不允许出现半截批次：maxSeq+1=%d，完整批次应到 %d", maxSeq+1, want)
	}
	for seq := uint64(batchRows); seq <= maxSeq; seq++ {
		if !seqs[seq] {
			t.Fatalf("已提交前缀中间缺失 seq=%d", seq)
		}
	}
	// 游标必须是某个完整批次的边界。
	batches := len(loaded.WAL) / batchRows
	if loaded.Positions[0].ReadPos != uint64(batches*batchRows) {
		t.Fatalf("游标应与已提交前缀一致：read=%d 期望 %d", loaded.Positions[0].ReadPos, batches*batchRows)
	}
	// 强杀后仍可继续写入。
	if _, err := store.Apply(batchState(batches + 1)); err != nil {
		t.Fatalf("强杀后继续写入失败: %v", err)
	}
}

// TestStoreConcurrentWriterSerialized 验证单写者：并发 Apply 不会互相破坏（连接池上限 1 +
// 事务串行），最终内容等于最后一次写入。
func TestStoreConcurrentWriterSerialized(t *testing.T) {
	store := newTestStore(t)
	done := make(chan struct{})
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func(n int) {
			defer func() { done <- struct{}{} }()
			if _, err := store.Apply(batchState(n + 1)); err != nil {
				errs <- err
			}
		}(i)
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	close(errs)
	for err := range errs {
		t.Fatalf("并发写入失败: %v", err)
	}
	if err := store.IntegrityCheck(); err != nil {
		t.Fatalf("并发写入后 integrity_check 未通过: %v", err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	// 并发写者各自提交整批状态（累加模型），最终必须是一整批的完整内容（无半截、无混批）。
	if len(loaded.WAL) == 0 || len(loaded.WAL)%batchRows != 0 {
		t.Fatalf("并发写入后必须是一整批的完整内容，实测 %d 行", len(loaded.WAL))
	}
	maxSeq := uint64(0)
	for _, row := range loaded.WAL {
		if row.Seq > maxSeq {
			maxSeq = row.Seq
		}
	}
	if maxSeq != uint64(len(loaded.WAL))+batchRows-1 {
		t.Fatalf("并发写入后行不完整：maxSeq=%d 行数=%d", maxSeq, len(loaded.WAL))
	}
}

func TestStoreLatencySampling(t *testing.T) {
	store := newTestStore(t)
	for i := 1; i <= 20; i++ {
		if _, err := store.Apply(batchState(i)); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}
	latency := store.Latency()
	if latency.Count != 20 {
		t.Fatalf("采样数应为 20，实测 %d", latency.Count)
	}
	if latency.P50 <= 0 || latency.P95 < latency.P50 || latency.Max < latency.P95 {
		t.Fatalf("分位读数不合法: %+v", latency)
	}
	if len(store.Samples()) != 20 {
		t.Fatalf("采样环应有 20 条，实测 %d", len(store.Samples()))
	}
}

// TestLoadingPreservesGapOrder 验证缺口顺序（上游按 Gaps[0] 判定首个未解缺口）。
func TestLoadingPreservesGapOrder(t *testing.T) {
	store := newTestStore(t)
	st := State{
		Sources: []SourceRow{{Key: "node:1/g1", LogSourceID: "node:1", SourceGeneration: "g1"}},
		Gaps: []GapRow{
			{Key: "node:1/g1", ID: 0, Reason: "PAUSED", Detail: "first", EndPos: 10},
			{Key: "node:1/g1", ID: 1, Reason: "TRUNCATED", Detail: "second", EndPos: 20},
			{Key: "node:1/g1", ID: 2, Reason: "ARCHIVE_DISCOVERY_FAILED", Detail: "third", EndPos: 30},
		},
	}
	if _, err := store.Apply(st); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	want := []string{"PAUSED", "TRUNCATED", "ARCHIVE_DISCOVERY_FAILED"}
	got := make([]string, 0, len(loaded.Gaps))
	for _, row := range loaded.Gaps {
		got = append(got, row.Reason)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("缺口顺序必须保持：want %v got %v", want, got)
	}
	// 解析中间某个缺口后，其余行必须仍在原位。
	st.Gaps[1].Resolved = true
	st.Gaps[1].Resolution = "manual"
	if _, err := store.Apply(st); err != nil {
		t.Fatalf("解算写入失败: %v", err)
	}
	loaded, err = store.Load()
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	sort.SliceStable(loaded.Gaps, func(i, j int) bool { return loaded.Gaps[i].ID < loaded.Gaps[j].ID })
	if len(loaded.Gaps) != 3 || !loaded.Gaps[1].Resolved || loaded.Gaps[1].Resolution != "manual" {
		t.Fatalf("缺口解算结果不正确: %+v", loaded.Gaps)
	}
}

// FR-496 加固：指纹归一化必须让「写入侧 Go 值」与「读回值」得到同一指纹——否则每次重启后
// 首次写入都会把整张表误判为「全部变更」而整表重写（生产 307MB 副本实测：74.7 万行缺口
// 及 position/projection 全表，单次持久化 47s / Stop 3s+，根因是 bool 字段的镜像与写入两侧
// 编码不同）。
//
// 转红说明：把 encodeValue 的 bool 分支改回「自成一类」（镜像侧读回 int64、写入侧 bool），
// 或让 []byte 与 string 不同编，本用例即红。
func TestFingerprintMatchesBetweenWriteAndRead(t *testing.T) {
	// 每种「写入侧形态」都给出其「读回形态」，两者必须同指纹。
	cases := [][2][]any{
		{[]any{true}, []any{int64(1)}},   // bool 落库读回 INTEGER
		{[]any{false}, []any{int64(0)}},  // bool 落库读回 INTEGER
		{[]any{int(7)}, []any{int64(7)}}, // int 落库读回 INTEGER
		{[]any{uint64(9)}, []any{int64(9)}},
		{[]any{int64(9)}, []any{int64(9)}},
		{[]any{"abc"}, []any{[]byte("abc")}}, // TEXT 驱动读回 string；BLOB 读回 []byte
		{[]any{nil}, []any{nil}},
	}
	for index, row := range cases {
		write, read := row[0], row[1]
		if fingerprint(write...) != fingerprint(read...) {
			t.Fatalf("用例 %d：写入侧 %v 与读回侧 %v 指纹不一致——重启后的首次写入会把该行误判为变更而整表重写",
				index, write, read)
		}
	}
	// 不同内容仍必须不同指纹（防把归一化做成「全一样」）。
	if fingerprint(true) == fingerprint(false) {
		t.Fatal("true/false 指纹不得相同")
	}
	if fingerprint(int64(1)) == fingerprint(int64(2)) {
		t.Fatal("1/2 指纹不得相同")
	}
}

// FR-496 加固：**重启后**幂等 Apply 必须零写入（指纹口径两侧一致的直接回归）。
//
// 转红说明：把 encodeValue 的 bool 分支改回自成一类，本用例在「首次打开→读镜像→Apply」
// 时就会把带 bool 字段的表（position/projection/gap）全部重写一遍，RowsWritten 远大于 0。
func TestStoreApplyAfterReopenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.db")
	st := fixtureState(4)
	store, err := Open(path)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	if _, err := store.Apply(st); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("重开失败: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	// 重读出的状态与期望一致（含 bool 字段）。
	loaded, err := reopened.Load()
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	assertSameState(t, st, loaded)
	stats, err := reopened.Apply(st)
	if err != nil {
		t.Fatalf("重写失败: %v", err)
	}
	if stats.RowsWritten != 0 || stats.RowsDeleted != 0 {
		t.Fatalf("重启后首次 Apply 必须零写入（指纹两侧一致），实测写 %d 删 %d",
			stats.RowsWritten, stats.RowsDeleted)
	}
}

// FR-496 加固（F3）：关闭索引句柄后 `-wal` 必须被归并截断，不得残留巨量 WAL。
//
// 转红说明：把 Store.Close 改回「只关连接不显式 checkpoint」（旧实现），本用例即红——
// sql.DB 的连接池关闭不保证以干净方式收尾，大事务后 `-wal` 会留在盘上数百 MB。
func TestStoreCloseCheckpointsWALAway(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	// 写入足够多的行，让 WAL 明显大于零。
	st := fixtureState(64)
	if _, err := store.Apply(st); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if store.WALBytes() <= 0 {
		t.Fatalf("大事务后 WAL 应非零（实测 %d）", store.WALBytes())
	}
	if err := store.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	if info, err := os.Stat(path + "-wal"); err == nil {
		t.Fatalf("关闭后 -wal 应被归并删除，实测仍存在（%d 字节）", info.Size())
	}
	if info, err := os.Stat(path + "-shm"); err == nil {
		t.Fatalf("关闭后 -shm 应被清理，实测仍存在（%d 字节）", info.Size())
	}
	// 关闭后库仍可正常打开。
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("重开失败: %v", err)
	}
	if err := reopened.IntegrityCheck(); err != nil {
		t.Fatalf("关闭+重开后 integrity_check 未通过: %v", err)
	}
	_ = reopened.Close()
}

// FR-496 加固：批量写入语句必须与表定义逐列对齐——列数不符时整批语句作废，绝不串位写入。
func TestBuildBatchUpsertRejectsColumnMismatch(t *testing.T) {
	spec := specs[tblGap]
	rows := []pendingRow{{row: rowData{values: func() ([]any, error) {
		return []any{"k", int64(0)}, nil // 只有 2 列，与 gap 的 8 列不符
	}}}}
	if _, _, _, err := buildBatchUpsert(spec, rows); err == nil {
		t.Fatal("列数不符时必须报错（否则批量语句会把列串位，静默写错数据）")
	}
}
