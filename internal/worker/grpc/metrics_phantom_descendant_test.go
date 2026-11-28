package grpc

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPrunePhantomDescendants 锁住「幻影子进程」剪枝规则（CI 实测成因见 prunePhantomDescendants）。
//
// 场景复刻：Windows 不重新挂靠孤儿进程，父进程退出后其子进程仍保留陈旧 ParentProcessID；
// 该 PID 被复用给本实例根进程后，那个「旧父留下的孩子」会被枚举成本实例的子进程。
// 判据只有一条——子进程的创建时间不可能早于其父。
func TestPrunePhantomDescendants(t *testing.T) {
	const (
		rootPID    = 1104
		wrapperPID = 2208
		shellPID   = 3312
		javaPID    = 4416
		phantomPID = 6536 // 陈旧 ParentProcessID 指向 rootPID 的他人进程
		orphanPID  = 7640 // 陈旧 ParentProcessID 指向 shellPID 的他人进程
	)

	t.Run("真实后代按创建时间递增全部保留", func(t *testing.T) {
		links := map[int32]treeLink{
			rootPID:    {createTime: 1000},
			wrapperPID: {ppid: rootPID, createTime: 1010},
			shellPID:   {ppid: wrapperPID, createTime: 1020},
			javaPID:    {ppid: shellPID, createTime: 1030},
		}
		assert.ElementsMatch(t, []int32{wrapperPID, shellPID, javaPID}, prunePhantomDescendants(rootPID, links))
	})

	t.Run("创建时间早于根的陈旧子进程被剔除", func(t *testing.T) {
		links := map[int32]treeLink{
			rootPID:    {createTime: 1000},
			wrapperPID: {ppid: rootPID, createTime: 1010},
			shellPID:   {ppid: wrapperPID, createTime: 1020},
			javaPID:    {ppid: shellPID, createTime: 1030},
			// 该进程由「上一个占用 PID 1104 的进程」创建，创建时间早于本实例根。
			phantomPID: {ppid: rootPID, createTime: 900},
		}
		got := prunePhantomDescendants(rootPID, links)
		sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
		assert.Equal(t, []int32{wrapperPID, shellPID, javaPID}, got, "不得把他人进程纳入清理等待")
	})

	t.Run("挂在中间层上的陈旧子进程同样被剔除", func(t *testing.T) {
		links := map[int32]treeLink{
			rootPID:    {createTime: 1000},
			wrapperPID: {ppid: rootPID, createTime: 1010},
			shellPID:   {ppid: wrapperPID, createTime: 1020},
			javaPID:    {ppid: shellPID, createTime: 1030},
			// 创建时间早于其声称的父（shell）：链上一处不成立即整条不认。
			orphanPID: {ppid: shellPID, createTime: 1015},
		}
		assert.ElementsMatch(t, []int32{wrapperPID, shellPID, javaPID}, prunePhantomDescendants(rootPID, links))
	})

	t.Run("幻影子进程的子进程按不可达一并剔除", func(t *testing.T) {
		links := map[int32]treeLink{
			rootPID:    {createTime: 1000},
			phantomPID: {ppid: rootPID, createTime: 900},
			// 幻影的孩子：即使创建时间晚于根，也不属本实例。
			orphanPID: {ppid: phantomPID, createTime: 1200},
		}
		assert.Empty(t, prunePhantomDescendants(rootPID, links))
	})

	t.Run("根缺失时不产出任何监视目标", func(t *testing.T) {
		assert.Empty(t, prunePhantomDescendants(rootPID, map[int32]treeLink{}))
	})
}
