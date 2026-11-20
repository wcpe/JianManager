package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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

	st := collectServerStatus(root, false)

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
	st := collectServerStatus(filepath.Join(t.TempDir(), "not-created"), false)

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

	st := collectServerStatus(root, false)

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

	st := collectServerStatus(root, false)
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

// 磁盘统计默认关闭：这一步要递归整个数据根，而生产数据根可能有上万个小文件。
// 应急场景下命令卡住比少一个数字更糟，故必须显式 --disk 才做。
func TestCollectServerStatus_DiskIsOptIn(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "big.bin"), make([]byte, 2*1024*1024), 0o644))

	// 默认：不统计。
	st := collectServerStatus(root, false)
	assert.Nil(t, st.DiskUsageMB, "默认不应统计磁盘占用")

	// 显式开启：有值且至少 2 MB。
	st = collectServerStatus(root, true)
	require.NotNil(t, st.DiskUsageMB)
	assert.GreaterOrEqual(t, *st.DiskUsageMB, int64(2))
}

// 服务探测：每个组件都必须给出结论与来源，不能留空。
//
// 本机跑测试时 systemd 未必存在，因此只断言「结构完整」而非具体状态，
// 这样用例在 Linux CI 与 Windows 开发机上都稳定。
func TestDetectServices_Structure(t *testing.T) {
	svcs := detectServices()

	require.Len(t, svcs, len(components), "每个组件都应有一条结论")
	names := map[string]bool{}
	for _, s := range svcs {
		names[s.Name] = true
		assert.NotEmpty(t, s.Source, "%s 必须标注来源", s.Name)
		if s.Running {
			assert.Positive(t, s.PID, "%s 运行中时应有 PID", s.Name)
		} else {
			assert.NotEmpty(t, s.Note, "%s 未运行时应说明查过什么", s.Name)
		}
	}
	assert.True(t, names["control-plane"])
	assert.True(t, names["worker"])
}

// 进程名比对跨平台：去掉路径与 .exe 后缀。
func TestBinaryBase(t *testing.T) {
	cases := map[string]string{
		"/opt/jianmanager/bin/control-plane":    "control-plane",
		`C:\Program Files\JM\control-plane.exe`: "control-plane",
		"worker":                                "worker",
		"worker.exe":                            "worker",
		"/proc/self/exe":                        "exe",
	}
	for in, want := range cases {
		assert.Equal(t, want, binaryBase(in), "输入: %s", in)
	}
}

// tasklist CSV 解析要能处理带引号与空字段。
func TestSplitCSVLine(t *testing.T) {
	assert.Equal(t, []string{"a", "1"}, splitCSVLine(`"a",1`))
	assert.Equal(t, []string{"x"}, splitCSVLine("x"))
	assert.Equal(t, []string{"a", "", "c"}, splitCSVLine("a,,c"))
}

// 时长的粗粒度渲染。
func TestHumanizeDuration(t *testing.T) {
	assert.Equal(t, "3d2h", humanizeDuration(74*time.Hour))
	assert.Equal(t, "5h30m", humanizeDuration(5*time.Hour+30*time.Minute))
	assert.Equal(t, "12m", humanizeDuration(12*time.Minute))
}
