package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// server status 的价值在于「不看代码也知道部署在哪、缺什么」，
// 因此测试重点放在：目录/配置探测准确、告警可操作、无副作用。

// 标准布局应被完整识别，且不产生告警。
func TestCollectServerStatus_StandardLayout(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"bin", "etc", "var/servers", "var/log"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, d), 0o755))
	}
	for _, f := range []string{"etc/control-plane.yml", "etc/worker.yml"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, f), []byte("{}"), 0o644))
	}

	st := collectServerStatus(root)

	assert.Equal(t, root, st.DataRoot)
	assert.True(t, st.DataRootOK)
	assert.True(t, st.Layout.Bin && st.Layout.Etc && st.Layout.Servers && st.Layout.Log)
	assert.Empty(t, st.Warnings, "标准布局不应产生告警")
	require.Len(t, st.Configs, 2)
	assert.ElementsMatch(t, []string{"control-plane.yml", "worker.yml"}, sortedConfigNames(st.Configs))
	for _, c := range st.Configs {
		assert.True(t, c.Exists, "%s 应被识别为已安装", c.Name)
	}
}

// 数据根不存在时要明确点出来，而不是静默给出全 false。
func TestCollectServerStatus_MissingRoot(t *testing.T) {
	st := collectServerStatus(filepath.Join(t.TempDir(), "not-created"))

	assert.False(t, st.DataRootOK)
	assert.False(t, st.Layout.Etc)
	joined := ""
	for _, w := range st.Warnings {
		joined += w + "\n"
	}
	assert.Contains(t, joined, "数据根不存在")
}

// 只有 CP、没有 Worker 时，应准确反映「worker.yml 未安装」而不报错。
// 同机只装一个组件是合法部署。
func TestCollectServerStatus_OnlyControlPlane(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"etc", "var/servers"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, d), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "etc/control-plane.yml"), []byte("{}"), 0o644))

	st := collectServerStatus(root)

	byName := map[string]bool{}
	for _, c := range st.Configs {
		byName[c.Name] = c.Exists
	}
	assert.True(t, byName["control-plane.yml"], "CP 配置应存在")
	assert.False(t, byName["worker.yml"], "Worker 配置应识别为未安装")
	// 已装一个组件，不应提示「未找到任何配置」。
	for _, w := range st.Warnings {
		assert.NotContains(t, w, "未找到 control-plane.yml 或 worker.yml")
	}
}

// 实例统计与 jmctl list 口径一致：无 PID 文件时为 0，不应报错。
func TestCollectServerStatus_NoInstances(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "var/servers"), 0o755))

	st := collectServerStatus(root)
	assert.Equal(t, 0, st.Instances.Total)
	assert.Equal(t, 0, st.Instances.Alive)
}

// 参数解析：--data-dir 的两种写法都要认，未知参数必须报错（避免静默忽略打错的值）。
func TestRunServerStatus_ArgParsing(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "etc"), 0o755))

	assert.NoError(t, runServerStatus([]string{"--data-dir", root}))
	assert.NoError(t, runServerStatus([]string{"--data-dir=" + root}))
	assert.Error(t, runServerStatus([]string{"--data-dir"}), "缺值应报错")
	assert.Error(t, runServerStatus([]string{"--nope"}), "未知参数应报错")
}

// 未给子命令时应打印用法而不是报错退出。
func TestRunServer_NoSubcommand(t *testing.T) {
	assert.NoError(t, runServer(nil))
	assert.Error(t, runServer([]string{"unknown"}), "未知子命令应报错")
}

// dirSizeMB 在遇到不可读子目录时必须继续统计，不能整体失败。
//
// 生产数据根里可能有 root 拥有的子目录，普通用户跑 status 不该因此拿到 0。
func TestDirSizeMB_TolerantToErrors(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.bin"), make([]byte, 1024*1024), 0o644))

	// 1 MiB 应至少计入 1 MB（整除截断，允许等于 1）。
	assert.GreaterOrEqual(t, dirSizeMB(root), int64(1))

	// 指向不存在的目录不应 panic。
	assert.Equal(t, int64(0), dirSizeMB(filepath.Join(root, "nope")))
}
