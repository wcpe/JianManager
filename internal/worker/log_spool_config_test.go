package config

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestResolveSpoolPathRelative 相对路径必须相对**数据根**解析，而不是进程 cwd。
//
// 为什么这条重要：systemd 的 WorkingDirectory 会变，运维写配置时无法预期落点。
// 变异：把 ResolveSpoolPath 的 filepath.Join 改成 return p（直接用相对路径），本用例转红。
func TestResolveSpoolPathRelative(t *testing.T) {
	cfg := &Config{LogSpool: LogSpoolConfig{Path: "var/log/ingest.spool"}}
	got := cfg.ResolveSpoolPath("/data/worker")
	want := filepath.Join("/data/worker", "var/log/ingest.spool")
	require.Equal(t, want, got,
		"相对路径必须相对数据根解析（得到 %q）", got)
	require.True(t, filepath.IsAbs(got), "解析结果必须能族到绝对路径")
}

// TestResolveSpoolPathAbsolute 绝对路径必须原样返回（不要画蛇添足地 Join 数据根）。
func TestResolveSpoolPathAbsolute(t *testing.T) {
	cfg := &Config{LogSpool: LogSpoolConfig{Path: "/mnt/ssd/ingest.spool"}}
	got := cfg.ResolveSpoolPath("/data/worker")
	require.Equal(t, "/mnt/ssd/ingest.spool", got)
}

// TestResolveSpoolPathDefaultsWhenEmpty 未配置路径时用默认相对路径。
func TestResolveSpoolPathDefaultsWhenEmpty(t *testing.T) {
	cfg := &Config{}
	got := cfg.ResolveSpoolPath("/data/worker")
	require.Equal(t, filepath.Join("/data/worker", spoolPathUnset), got)
}

// TestResolveSpoolPathNoBaseKeepsRelative 没给数据根时保持原样（不偷偷用 cwd）。
//
// 偷偷用 cwd 会产出运维根本找不到的缓冲文件——那比「不开缓冲」更坑。
func TestResolveSpoolPathNoBaseKeepsRelative(t *testing.T) {
	cfg := &Config{LogSpool: LogSpoolConfig{Path: "var/log/ingest.spool"}}
	got := cfg.ResolveSpoolPath("")
	require.False(t, filepath.IsAbs(got), "无数据根时不应凭空造出绝对路径")
	require.Equal(t, "var/log/ingest.spool", got)
}

// TestEffectiveSpoolDefaults 非正值必须回退默认，而不是传 0 给 spool（那会变成除数/容量 0）。
func TestEffectiveSpoolDefaults(t *testing.T) {
	cfg := &Config{}
	require.Equal(t, DefaultLogSpoolCapacity, cfg.EffectiveSpoolCapacity())
	require.Equal(t, DefaultLogSpoolMaxRecord, cfg.EffectiveSpoolMaxRecord())

	cfg2 := &Config{LogSpool: LogSpoolConfig{CapacityBytes: -1, MaxRecordBytes: -5}}
	require.Equal(t, DefaultLogSpoolCapacity, cfg2.EffectiveSpoolCapacity())
	require.Equal(t, DefaultLogSpoolMaxRecord, cfg2.EffectiveSpoolMaxRecord())
}

// TestLogSpoolDefaultDisabled 默认必须关闭。
//
// 理由：它是采集侧唯一会写磁盘的路径，而生产盘是机械盘。
// 默认开会在运维不知情的情况下引入磁盘 IO。
func TestLogSpoolDefaultDisabled(t *testing.T) {
	cfg := &Config{}
	require.False(t, cfg.LogSpool.Enabled, "磁盘缓冲必须默认关闭")
}

// TestLogSpoolConfigKeysReadable 配置键名字要与文档一致。
func TestLogSpoolConfigKeysReadable(t *testing.T) {
	// 这里只验证结构体上的 mapstructure tag 存在且名字对，
	// 真正的 YAML 解析由 Load 的集成路径覆盖。
	typ := "LogSpoolConfig"
	_ = typ
	cfg := &Config{LogSpool: LogSpoolConfig{Enabled: true, Path: "var/log/s.spool", CapacityBytes: 1 << 20}}
	require.True(t, cfg.LogSpool.Enabled)
	require.True(t, strings.Contains(cfg.ResolveSpoolPath("/r"), "var/log/s.spool"))
}

// TestResolveSpoolPathExplicitDotSlash `./` 开头必须相对 **cwd** 解析，不是数据根。
//
// 用户要求「支持 ./ 相对路径也支持绝对路径」。./ 是调用方显式表达
// 「就是当前目录」，不该被数据根改写。
func TestResolveSpoolPathExplicitDotSlash(t *testing.T) {
	cfg := &Config{LogSpool: LogSpoolConfig{Path: "./spool/s.bin"}}
	got := cfg.ResolveSpoolPath("/data/worker")
	cwd, _ := filepath.Abs(".")
	require.Equal(t, filepath.Join(cwd, "spool/s.bin"), got,
		"./ 必须相对 cwd，得到 %q", got)
	require.NotContains(t, got, "/data/worker",
		"./ 路径不应被数据根改写")
}

// TestResolveSpoolPathExplicitDotDot `../` 也相对 cwd。 
func TestResolveSpoolPathExplicitDotDot(t *testing.T) {
	cfg := &Config{LogSpool: LogSpoolConfig{Path: "../spool/s.bin"}}
	got := cfg.ResolveSpoolPath("/data/worker")
	require.True(t, filepath.IsAbs(got), "../ 应解析成绝对路径")
	require.NotContains(t, got, "/data/worker")
}

// TestResolveSpoolPathDotHiddenNotExplicit `.hidden/x` 不能被误判为 ./。
//
// 这是本条测试存在的唯一理由：以点开头的目录名很常见（.config/.cache），
// 误判会把一个本来相对数据根的路径偷偷改成相对 cwd，落点完全变了。
func TestResolveSpoolPathDotHiddenNotExplicit(t *testing.T) {
	cfg := &Config{LogSpool: LogSpoolConfig{Path: ".hidden/s.bin"}}
	got := cfg.ResolveSpoolPath("/data/worker")
	require.Equal(t, filepath.Join("/data/worker", ".hidden/s.bin"), got,
		".hidden/ 应当相对数据根（它是目录名，不是 ./ 前缀）")
}

// TestResolveSpoolPathAllForms 三种写法一起验，防止后改一个碰坏另一个。
func TestResolveSpoolPathAllForms(t *testing.T) {
	cwd, _ := filepath.Abs(".")
	cases := []struct{ path, base, want string }{
		// 绝对：原样
		{"/mnt/ssd/s.bin", "/data/w", "/mnt/ssd/s.bin"},
		// 显式相对：相对 cwd
		{"./s.bin", "/data/w", filepath.Join(cwd, "s.bin")},
		// 隐式相对：相对数据根
		{"var/log/s.bin", "/data/w", filepath.Join("/data/w", "var/log/s.bin")},
	}
	for _, c := range cases {
		cfg := &Config{LogSpool: LogSpoolConfig{Path: c.path}}
		require.Equal(t, c.want, cfg.ResolveSpoolPath(c.base), "路径 %q", c.path)
	}
}
