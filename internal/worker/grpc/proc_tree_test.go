package grpc

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProcNode 用内存树验证进程选择与聚合算法：不启动任何进程、不碰文件系统。
// 这些算法只关心「树长什么样、每层报什么数」，与操作系统无关；
// 真实进程只用于验证平台读取能力（见 instance_metrics_test.go 的端到端用例）。
type fakeProcNode struct {
	pid             int32
	name            string
	children        []procNode
	rss             uint64
	cpu             float64
	createdAtUnixMs int64

	nameErr     error
	childrenErr error
	rssErr      error
	cpuErr      error
	createErr   error
}

func (f *fakeProcNode) PID() int32 { return f.pid }

func (f *fakeProcNode) Name(context.Context) (string, error) {
	return f.name, f.nameErr
}

func (f *fakeProcNode) Children(context.Context) ([]procNode, error) {
	return f.children, f.childrenErr
}

func (f *fakeProcNode) RSS(context.Context) (uint64, error) { return f.rss, f.rssErr }

func (f *fakeProcNode) CPUPercent(context.Context) (float64, error) { return f.cpu, f.cpuErr }

func (f *fakeProcNode) CreatedAtUnixMs(context.Context) (int64, error) {
	return f.createdAtUnixMs, f.createErr
}

// TestResolveGameProcIn 覆盖选游戏进程的全部规则分支。
func TestResolveGameProcIn(t *testing.T) {
	t.Run("越过 wrapper 与 shell 选中 java", func(t *testing.T) {
		java := &fakeProcNode{pid: 3, name: "java.exe"}
		root := &fakeProcNode{pid: 1, name: "wrapper", children: []procNode{
			&fakeProcNode{pid: 2, name: "sh", children: []procNode{java}},
		}}
		require.Equal(t, int32(3), resolveGameProcIn(context.Background(), root).PID())
	})

	t.Run("多个 java 时取最深", func(t *testing.T) {
		deepJava := &fakeProcNode{pid: 4, name: "java"}
		root := &fakeProcNode{pid: 1, name: "root", children: []procNode{
			&fakeProcNode{pid: 2, name: "java", children: []procNode{
				&fakeProcNode{pid: 3, name: "sh", children: []procNode{deepJava}},
			}},
		}}
		require.Equal(t, int32(4), resolveGameProcIn(context.Background(), root).PID())
	})

	t.Run("大小写不敏感", func(t *testing.T) {
		root := &fakeProcNode{pid: 1, name: "root", children: []procNode{
			&fakeProcNode{pid: 2, name: "JAVA.EXE"},
		}}
		require.Equal(t, int32(2), resolveGameProcIn(context.Background(), root).PID())
	})

	t.Run("无 java 回退最深后代", func(t *testing.T) {
		root := &fakeProcNode{pid: 1, name: "root", children: []procNode{
			&fakeProcNode{pid: 2, name: "sh", children: []procNode{&fakeProcNode{pid: 3, name: "bedrock_server"}}},
		}}
		require.Equal(t, int32(3), resolveGameProcIn(context.Background(), root).PID())
	})

	t.Run("无后代回退根", func(t *testing.T) {
		root := &fakeProcNode{pid: 1, name: "solo"}
		require.Equal(t, int32(1), resolveGameProcIn(context.Background(), root).PID())
	})

	t.Run("nil 根返回 nil", func(t *testing.T) {
		assert.Nil(t, resolveGameProcIn(context.Background(), nil))
	})

	t.Run("名字读取失败不中断遍历", func(t *testing.T) {
		root := &fakeProcNode{pid: 1, name: "root", nameErr: errors.New("拒绝访问"),
			children: []procNode{&fakeProcNode{pid: 2, name: "java"}}}
		require.Equal(t, int32(2), resolveGameProcIn(context.Background(), root).PID())
	})

	t.Run("子进程读取失败只断该分支", func(t *testing.T) {
		broken := &fakeProcNode{pid: 2, name: "broken", childrenErr: errors.New("拒绝访问")}
		root := &fakeProcNode{pid: 1, name: "root", children: []procNode{
			broken,
			&fakeProcNode{pid: 3, name: "java"},
		}}
		assert.NotNil(t, resolveGameProcIn(context.Background(), root))
	})

	t.Run("环路不死循环", func(t *testing.T) {
		a := &fakeProcNode{pid: 1, name: "a"}
		b := &fakeProcNode{pid: 2, name: "b", children: []procNode{a}}
		a.children = []procNode{b}
		require.Equal(t, int32(2), resolveGameProcIn(context.Background(), a).PID())
	})

	t.Run("节点数达上限即停", func(t *testing.T) {
		root := &fakeProcNode{pid: 1, name: "root"}
		current := root
		for i := int32(2); i <= procTreeMaxNodes+10; i++ {
			child := &fakeProcNode{pid: i, name: "n"}
			current.children = []procNode{child}
			current = child
		}
		// 不 panic、不越界即可；上限内深度受 procTreeMaxNodes 约束。
		assert.NotNil(t, resolveGameProcIn(context.Background(), root))
	})
}

// TestCollectProcessTreeIn 覆盖整树收集。
func TestCollectProcessTreeIn(t *testing.T) {
	t.Run("含根并覆盖全部后代", func(t *testing.T) {
		root := &fakeProcNode{pid: 1, name: "wrapper", children: []procNode{
			&fakeProcNode{pid: 2, name: "sh", children: []procNode{&fakeProcNode{pid: 3, name: "java.exe"}}},
		}}
		nodes, err := collectProcessTreeIn(context.Background(), root)
		require.NoError(t, err)
		pids := make([]int32, 0, len(nodes))
		for _, node := range nodes {
			pids = append(pids, node.PID())
		}
		assert.ElementsMatch(t, []int32{1, 2, 3}, pids)
	})

	t.Run("同一 PID 只收一次", func(t *testing.T) {
		shared := &fakeProcNode{pid: 2, name: "shared"}
		root := &fakeProcNode{pid: 1, name: "root", children: []procNode{shared, shared}}
		nodes, err := collectProcessTreeIn(context.Background(), root)
		require.NoError(t, err)
		assert.Len(t, nodes, 2)
	})

	t.Run("ctx 取消即中断", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := collectProcessTreeIn(ctx, &fakeProcNode{pid: 1, name: "root"})
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("子进程读取失败向上报错", func(t *testing.T) {
		root := &fakeProcNode{pid: 1, name: "root", childrenErr: errors.New("拒绝访问")}
		_, err := collectProcessTreeIn(context.Background(), root)
		require.Error(t, err)
	})
}

// TestSumTreeMetricsIn 覆盖整树聚合与不可用语义。
func TestSumTreeMetricsIn(t *testing.T) {
	ctx := context.Background()

	t.Run("RSS 求和", func(t *testing.T) {
		total, ok := sumTreeRSSIn(ctx, []procNode{
			&fakeProcNode{pid: 1, rss: 100},
			&fakeProcNode{pid: 2, rss: 250},
		})
		assert.True(t, ok)
		assert.Equal(t, int64(350), total)
	})

	t.Run("任一 RSS 读取失败即整体不可用", func(t *testing.T) {
		_, ok := sumTreeRSSIn(ctx, []procNode{
			&fakeProcNode{pid: 1, rss: 100},
			&fakeProcNode{pid: 2, rssErr: errors.New("拒绝访问")},
		})
		assert.False(t, ok, "部分成功不得当作整树可用")
	})

	t.Run("RSS 溢出保护", func(t *testing.T) {
		_, ok := sumTreeRSSIn(ctx, []procNode{
			&fakeProcNode{pid: 1, rss: math.MaxUint64 - 1},
			&fakeProcNode{pid: 2, rss: 10},
		})
		assert.False(t, ok)
	})

	t.Run("CPU 求和", func(t *testing.T) {
		total, ok := sumTreeCPUIn(ctx, []procNode{
			&fakeProcNode{pid: 1, cpu: 12.5},
			&fakeProcNode{pid: 2, cpu: 7.5},
		})
		assert.True(t, ok)
		assert.InDelta(t, 20, total, 0.001)
	})

	t.Run("任一 CPU 读取失败即整体不可用", func(t *testing.T) {
		_, ok := sumTreeCPUIn(ctx, []procNode{&fakeProcNode{pid: 1, cpuErr: errors.New("拒绝访问")}})
		assert.False(t, ok)
	})

	t.Run("空树可用且为零", func(t *testing.T) {
		total, ok := sumTreeRSSIn(ctx, nil)
		assert.True(t, ok)
		assert.Zero(t, total)
	})
}

// TestUptimeIn 覆盖运行时长换算。
func TestUptimeIn(t *testing.T) {
	ctx := context.Background()

	t.Run("由创建时刻换算", func(t *testing.T) {
		seconds, ok := uptimeIn(ctx, &fakeProcNode{createdAtUnixMs: 1_700_000_000_000})
		assert.True(t, ok)
		assert.Greater(t, seconds, float64(0))
	})

	t.Run("创建时刻为零不可用", func(t *testing.T) {
		_, ok := uptimeIn(ctx, &fakeProcNode{createdAtUnixMs: 0})
		assert.False(t, ok)
	})

	t.Run("读取失败不可用", func(t *testing.T) {
		_, ok := uptimeIn(ctx, &fakeProcNode{createErr: errors.New("拒绝访问")})
		assert.False(t, ok)
	})
}
