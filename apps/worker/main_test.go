package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
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
