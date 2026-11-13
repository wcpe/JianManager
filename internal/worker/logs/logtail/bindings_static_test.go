package logtail

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBindingsResolver_StaticSource 验证非实例日志（节点自身日志）能被归源。
//
// 回归背景（2026-11-10 开发实例真机实测）：配了 log_tailer.paths 指向
// /srv/dev/worker/logs/worker.log，但 Resolver 只认「实例工作目录」与「stdio 目录」，
// 于是每轮扫描都报「文件暂不可采（尚未识别所属源）」，位点不推进，一条也采不到；
// 而后端又把 `worker` 类别映射到 `node:` 前缀 ⇒ 前瑞选“worker”永远空。
func TestBindingsResolver_StaticSource(t *testing.T) {
	b, err := OpenBindings("", "/data/stdio")
	require.NoError(t, err)

	b.AddStatic([]StaticSource{
		{TargetID: "node:1", Path: "/srv/dev/worker/logs/worker.log", Stream: "file"},
	})
	resolve := b.Resolver()

	// 精确路径：采得到（这是修复前失败的用例）。
	id, stream := resolve.Resolve("/srv/dev/worker/logs/worker.log")
	assert.Equal(t, "node:1/file", id, "节点自身日志必须能归源")
	assert.Equal(t, "file", stream)

	// 不相关路径：仍不得乱猜（返空，与修复前行为一致）。
	id, _ = resolve.Resolve("/srv/dev/cp/logs/cp.log")
	assert.Equal(t, "", id, "未登记的路径不得猜出源")
}

// TestBindingsResolver_StaticSourceDir 验证目录形式的静态源。
func TestBindingsResolver_StaticSourceDir(t *testing.T) {
	b, err := OpenBindings("", "")
	require.NoError(t, err)

	b.AddStatic([]StaticSource{
		{TargetID: "node:7", Path: "/var/log/jm", Stream: "worker"},
	})
	resolve := b.Resolver()

	id, stream := resolve.Resolve("/var/log/jm/worker.log")
	assert.Equal(t, "node:7/worker", id)
	assert.Equal(t, "worker", stream)

	// 前缀不能误判：/var/log/jmx 不在 /var/log/jm 下。
	id, _ = resolve.Resolve("/var/log/jmx/other.log")
	assert.Equal(t, "", id, "缺少分隔符判断会误判父目录")
}

// TestBindingsResolver_InstanceWinsOverStatic 确认实例归属优先于静态源。
//
// 一个节点级目录源（如整个日志目录）不应抢走其中某实例自己的文件：
// 实例归属是**更具体**的事实，静态源只是兵底。
func TestBindingsResolver_InstanceWinsOverStatic(t *testing.T) {
	b, err := OpenBindings("", "")
	require.NoError(t, err)

	// 静态源覆盖整个 /srv/servers（相当于“把这目录下所有日志当节点日志”）。
	b.AddStatic([]StaticSource{{TargetID: "node:1", Path: "/srv/servers", Stream: "file"}})
	// 其中一个子目录其实是实例 318 的工作目录。
	require.NoError(t, b.Put(BindingEntry{
		UUID: "u-318", TargetID: "inst:318", WorkDir: "/srv/servers/s318",
	}))
	resolve := b.Resolver()

	id, _ := resolve.Resolve("/srv/servers/s318/logs/latest.log")
	assert.Equal(t, "inst:318/file", id, "实例归属必须优先于目录级静态源")

	// 实例目录外的文件才落到静态源。
	id, _ = resolve.Resolve("/srv/servers/other.txt")
	assert.Equal(t, "node:1/file", id)
}

// TestBindingsResolver_NoStatic 确认不配静态源时行为不变（零回归）。
func TestBindingsResolver_NoStatic(t *testing.T) {
	b, err := OpenBindings("", "/data/stdio")
	require.NoError(t, err)
	resolve := b.Resolver()

	id, _ := resolve.Resolve("/srv/dev/worker/logs/worker.log")
	assert.Equal(t, "", id, "未登记静态源时不得凭空归源")
}

// TestBindingsAddStatic_Normalize 确认 AddStatic 的归一与剔除。
func TestBindingsAddStatic_Normalize(t *testing.T) {
	b, err := OpenBindings("", "")
	require.NoError(t, err)

	b.AddStatic([]StaticSource{
		{TargetID: "  node:1  ", Path: "  /var/log/a.log  "}, // 空白应被裁掉，stream 缺省为 file
		{TargetID: "", Path: "/var/log/b.log"},               // 无 target：剔除
		{TargetID: "node:2", Path: ""},                       // 无 path：剔除
	})

	got := b.StaticSources()
	require.Len(t, got, 1, "空 target 或空 path 的条目应被剔除")
	assert.Equal(t, "node:1", got[0].TargetID)
	assert.Equal(t, "/var/log/a.log", got[0].Path)
	assert.Equal(t, "file", got[0].Stream, "stream 缺省值应为 file")
}
