package main

import (
	"context"
	"log/slog"
	"net"
	"strconv"
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
	const ringCap = 512 // stateindex.sampleRing：采样环只保留最近 512 次持久化
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
