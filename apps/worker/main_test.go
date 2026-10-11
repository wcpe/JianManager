package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
)

func TestLocalWSAddrOnlyUsesLoopback(t *testing.T) {
	listener, err := net.Listen("tcp", localWSAddr(0))
	if err != nil {
		t.Fatalf("监听本机 WebSocket 地址失败: %v", err)
	}
	defer listener.Close()

	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("解析监听地址失败: %v", err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("Worker WebSocket 必须仅监听 IPv4 回环地址，实际为 %q", host)
	}
	if _, err := strconv.Atoi(port); err != nil {
		t.Fatalf("监听端口无效: %q", port)
	}
}

// persistSampleAt 造一条采样：started 时刻开始、写入 written 行、删除 deleted 行、写入 bytes 字节。
func persistSampleAt(started time.Time, written, deleted int, bytes int64) stateindex.Sample {
	return stateindex.Sample{
		StartedAt:    started,
		Duration:     14 * time.Millisecond,
		RowsWritten:  written,
		RowsDeleted:  deleted,
		BytesWritten: bytes,
	}
}

// testPersistLatency 造一个取值固定的分位读数，用于断言 p50/p95/max 的口径不随 Delta 改动。
func testPersistLatency(count int) stateindex.Latency {
	return stateindex.Latency{
		Count: count,
		P50:   14 * time.Millisecond,
		P95:   62 * time.Millisecond,
		Max:   65 * time.Millisecond,
	}
}

// sampleLogCapture 是只收集属性、不做格式化的 slog.Handler：断言字段值比解析日志文本稳，
// 也能让字段名变更直接转红（缺字段即失败）。
type sampleLogCapture struct {
	attrs []slog.Attr
}

func (c *sampleLogCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *sampleLogCapture) Handle(_ context.Context, record slog.Record) error {
	record.Attrs(func(attr slog.Attr) bool {
		c.attrs = append(c.attrs, attr)
		return true
	})
	return nil
}

func (c *sampleLogCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *sampleLogCapture) WithGroup(string) slog.Handler      { return c }

// reset 清空已收集的属性，便于逐个窗口断言。
func (c *sampleLogCapture) reset() { c.attrs = nil }

// int64Attr 取一条 int64 属性；缺字段直接判失败。
func (c *sampleLogCapture) int64Attr(t *testing.T, key string) int64 {
	t.Helper()
	for _, attr := range c.attrs {
		if attr.Key == key {
			return attr.Value.Int64()
		}
	}
	keys := make([]string, 0, len(c.attrs))
	for _, attr := range c.attrs {
		keys = append(keys, attr.Key)
	}
	t.Fatalf("采样日志缺少字段 %q，实际字段：%v", key, keys)
	return 0
}

// captureSampleLog 把 slog 默认 logger 换成属性收集器；测试结束自动还原。
func captureSampleLog(t *testing.T) *sampleLogCapture {
	t.Helper()
	capture := &sampleLogCapture{}
	previous := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return capture
}

// assertWindowDelta 断言这条日志里的三个 Delta 都等于「本窗口新增」，并顺带守住分位字段口径。
func assertWindowDelta(t *testing.T, capture *sampleLogCapture, window string, wantWritten, wantDeleted, wantBytes int64) {
	t.Helper()
	gotWritten := capture.int64Attr(t, "rowsWrittenDelta")
	gotDeleted := capture.int64Attr(t, "rowsDeletedDelta")
	gotBytes := capture.int64Attr(t, "bytesWrittenDelta")
	if gotWritten != wantWritten || gotDeleted != wantDeleted || gotBytes != wantBytes {
		t.Fatalf("%s 的三个 Delta 都应是「本窗口新增」，实际 written/deleted/bytes = %d/%d/%d，期望 %d/%d/%d",
			window, gotWritten, gotDeleted, gotBytes, wantWritten, wantDeleted, wantBytes)
	}
	if got := capture.int64Attr(t, "p95ms"); got != 62 {
		t.Fatalf("%s 的分位口径不应随 Delta 改动：p95ms = %d，期望 62", window, got)
	}
}

// TestLogIndexPersistSampleDeltaIsWindowIncrement 守「窗口增量」口径：环未满 + 连续多个窗口时，
// 每个窗口的 Delta 只能是本窗口新增的量，不能是环内求和（环未满时环和 = 自启动累计值）。
//
// 为什么能转红：把实现改回旧写法（rowsDeletedDelta 直接取环和、written/bytes 取相邻环和之差）后，
// 窗口二会报出 7/7/370——其中 7 是自启动累计的删除量，而本窗口真实只删了 1 行。
func TestLogIndexPersistSampleDeltaIsWindowIncrement(t *testing.T) {
	base := time.Now()
	// 窗口一：3 次持久化，共写 30 行 / 删 6 行 / 300 字节。
	window1Samples := []stateindex.Sample{
		persistSampleAt(base.Add(1*time.Second), 10, 2, 100),
		persistSampleAt(base.Add(2*time.Second), 10, 2, 100),
		persistSampleAt(base.Add(3*time.Second), 10, 2, 100),
	}
	// 窗口二：环仍未满（整个环只有 4 条样本），本窗口只发生 1 次持久化，量与窗口一明显不同。
	window2Samples := append(append([]stateindex.Sample(nil), window1Samples...),
		persistSampleAt(base.Add(64*time.Second), 7, 1, 70))

	capture := captureSampleLog(t)
	window := &logIndexPersistWindow{cursor: base}

	logIndexPersistSample(window, testPersistLatency(len(window1Samples)), window1Samples)
	assertWindowDelta(t, capture, "窗口一", 30, 6, 300)

	capture.reset()
	logIndexPersistSample(window, testPersistLatency(len(window2Samples)), window2Samples)
	assertWindowDelta(t, capture, "窗口二", 7, 1, 70)
}

// TestLogIndexPersistSampleDeltaSurvivesFullRing 守环写满后的口径：环满时「环和」是滑动窗口和，
// 相邻读数相减得到的是「本窗口新增 − 同期被挤出的旧样本」，稳态下趋近 0，不是增量。
//
// 为什么能转红：改回旧写法后，本用例的窗口二会报 rowsWrittenDelta = 7 - 10 = -3、
// bytesWrittenDelta = 70 - 100 = -30（写成负数），而真实增量只有新滑入的那一次 7/1/70。
func TestLogIndexPersistSampleDeltaSurvivesFullRing(t *testing.T) {
	// 本用例自造 512 规模的切片，只为让「首窗口即写满环、次窗口挤出最早样本」的语义在少量样本下
	// 可复现；**不是**生产环容量——生产环容量见 stateindex.sampleRing（4096），随其改动不影响本用例。
	const ringCap = 512
	base := time.Now()
	// 第一窗口结束时环刚好写满：最早那条（值刻意非零）将在下一窗口滑出观测范围。
	firstRing := make([]stateindex.Sample, 0, ringCap)
	firstRing = append(firstRing, persistSampleAt(base.Add(1*time.Second), 10, 2, 100))
	for index := 1; index < ringCap; index++ {
		firstRing = append(firstRing, persistSampleAt(base.Add(time.Duration(index+1)*time.Second), 0, 0, 0))
	}
	// 第二窗口：又发生 1 次持久化，环被裁到最近 ringCap 条。
	secondRing := append(append([]stateindex.Sample(nil), firstRing[1:]...),
		persistSampleAt(base.Add(600*time.Second), 7, 1, 70))
	if len(secondRing) != ringCap {
		t.Fatalf("采样环快照长度应为 %d，实际 %d", ringCap, len(secondRing))
	}

	capture := captureSampleLog(t)
	window := &logIndexPersistWindow{cursor: base}

	// 第一次读数只建立游标：采样器与采集循环同时启动，此刻环里的样本都发生在本窗口之内。
	logIndexPersistSample(window, testPersistLatency(ringCap), firstRing)
	if got := capture.int64Attr(t, "rowsDeletedDelta"); got != 2 {
		t.Fatalf("首窗口的删除增量应为环内已有的 2 行，实际 %d", got)
	}

	capture.reset()
	logIndexPersistSample(window, testPersistLatency(ringCap), secondRing)
	assertWindowDelta(t, capture, "环满后的窗口", 7, 1, 70)
}

// TestNormalizeSubcommandArgsAcceptsSpaceAndEqualsForms 守「用法注释与解析器一致」：
// `--k v` 与 `--k=v` 必须解析出同一个结果。
//
// 为什么能转红：回到「只认等号」的旧行为后，用例一的 `--source`/`--from` 会被整条丢弃，
// 归一化结果只剩 `--to=200`（甚至为空），断言立即失败——那正是「按注释敲参数却导出全源全区间」的静默失真。
func TestNormalizeSubcommandArgsAcceptsSpaceAndEqualsForms(t *testing.T) {
	known := []string{"--data-dir", "--source", "--from", "--to", "--at"}

	space, err := normalizeSubcommandArgs(
		[]string{"--data-dir", "data-root", "--source", "inst:1/stdout", "--from", "100", "--to", "200"}, known...)
	if err != nil {
		t.Fatalf("空格分隔写法应被接受，实际报错: %v", err)
	}
	equal, err := normalizeSubcommandArgs(
		[]string{"--data-dir=data-root", "--source=inst:1/stdout", "--from=100", "--to=200"}, known...)
	if err != nil {
		t.Fatalf("等号写法应被接受，实际报错: %v", err)
	}
	want := []string{"--data-dir=data-root", "--source=inst:1/stdout", "--from=100", "--to=200"}
	if len(space) != len(want) {
		t.Fatalf("空格写法的归一化结果长度应为 %d，实际 %v", len(want), space)
	}
	for index := range want {
		if space[index] != want[index] {
			t.Fatalf("空格写法的第 %d 项应为 %q，实际 %q（整串 %v）", index, want[index], space[index], space)
		}
	}
	for index := range want {
		if equal[index] != want[index] {
			t.Fatalf("等号写法与空格写法应等价：第 %d 项 %q ≠ %q", index, equal[index], want[index])
		}
	}
}

// TestNormalizeSubcommandArgsRejectsUnknownAndMissingValues 守「诊断工具宁可失败也不要静默」：
// 未知参数、非参数、缺值、空值、下一参又是另一个参数，全部必须报错。
func TestNormalizeSubcommandArgsRejectsUnknownAndMissingValues(t *testing.T) {
	known := []string{"--data-dir", "--source", "--from", "--to", "--at"}
	cases := []struct {
		name string
		args []string
	}{
		{"未知参数", []string{"--sourc", "inst:1/stdout"}},
		{"裸位置参数", []string{"inst:1/stdout"}},
		{"末位缺值", []string{"--source"}},
		{"下一参是另一个参数", []string{"--from", "--to", "200"}},
		{"取值为空", []string{"--source="}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeSubcommandArgs(tc.args, known...)
			if err == nil {
				t.Fatalf("应报错并退出，实际被静默接受：%v", got)
			}
			if got != nil {
				t.Fatalf("报错时不得返回部分结果，实际 %v", got)
			}
		})
	}
}

// TestFlagValueRejectsDuplicates 守「重复参数不许静默取末值」：`--out a --out b` 在旧实现里会
// 先截断 a、再只写 b，留下一个被清零的 a —— 现在的语义是直接报错。
func TestFlagValueRejectsDuplicates(t *testing.T) {
	value, err := flagValue([]string{"--out=a.json", "--data-dir=root"}, "--out")
	if err != nil {
		t.Fatalf("单一取值不应报错: %v", err)
	}
	if value != "a.json" {
		t.Fatalf("取值应为 a.json，实际 %q", value)
	}
	if _, err := flagValue([]string{"--out=a.json", "--out=b.json"}, "--out"); err == nil {
		t.Fatal("重复 --out 应报错")
	}
	if value, err := flagValue([]string{"--data-dir=root"}, "--out"); err != nil || value != "" {
		t.Fatalf("缺省 --out 应返回空串且不报错，实际 %q / %v", value, err)
	}
}

// TestWriteIndexExportAtomicallyOverwritesTarget 守成功路径：导出成功后目标文件就是新的合法
// JSON 快照，且目录里不留临时文件。
func TestWriteIndexExportAtomicallyOverwritesTarget(t *testing.T) {
	root := t.TempDir()
	// 造一个真实可读的索引库（导出真源）：Open 会建表，空库即可导出。
	store, err := stateindex.Open(filepath.Join(root, "var", "log", "ingest.index.db"))
	if err != nil {
		t.Fatalf("创建索引库失败: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("关闭索引库失败: %v", err)
	}

	outPath := filepath.Join(root, "snapshot.json")
	if err := os.WriteFile(outPath, []byte("old-snapshot"), 0o600); err != nil {
		t.Fatalf("预置旧快照失败: %v", err)
	}
	if err := writeIndexExportAtomically(root, outPath); err != nil {
		t.Fatalf("导出应成功，实际报错: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("读取导出结果失败: %v", err)
	}
	if !json.Valid(data) {
		t.Fatalf("导出结果应为合法 JSON，实际 %q", string(data))
	}
	assertNoTempExportLeftovers(t, filepath.Dir(outPath))
}

// TestWriteIndexExportAtomicallyKeepsOldSnapshotOnFailure 守失败路径（本轮修复的核心理由）：
// 目标文件是「回滚前的快照」，导出失败**不得**把它清零。
//
// 为什么能转红：改回 `os.OpenFile(out, O_CREATE|O_TRUNC|O_WRONLY)` 后再导出的旧写法，
// 本用例里旧快照会被截断成 0 字节 ⇒ 断言读到空内容即失败。
func TestWriteIndexExportAtomicallyKeepsOldSnapshotOnFailure(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "snapshot.json")
	const oldSnapshot = "old-snapshot"
	if err := os.WriteFile(outPath, []byte(oldSnapshot), 0o600); err != nil {
		t.Fatalf("预置旧快照失败: %v", err)
	}

	// 根目录下没有索引库 ⇒ 导出必然失败。
	if err := writeIndexExportAtomically(filepath.Join(dir, "empty-root"), outPath); err == nil {
		t.Fatal("索引库缺失时导出应报错")
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("旧快照应仍然存在: %v", err)
	}
	if string(data) != oldSnapshot {
		t.Fatalf("导出失败后旧快照必须原样保留，实际内容 %q", string(data))
	}
	assertNoTempExportLeftovers(t, dir)
}

// assertNoTempExportLeftovers 断言导出目录里没有残留的临时文件（成功/失败路径都不许留）。
func assertNoTempExportLeftovers(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取导出目录失败: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("导出目录残留临时文件 %q", entry.Name())
		}
	}
}
