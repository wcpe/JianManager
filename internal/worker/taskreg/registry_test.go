package taskreg

import (
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// 生命周期：Start→进度/日志→Succeed，快照如实反映；Drop 后不再出现。
func TestRegistry_Lifecycle(t *testing.T) {
	r := New()
	r.Start("t1")

	snap := r.Snapshot()
	require.Len(t, snap, 1)
	require.Equal(t, "running", snap[0].State)
	require.EqualValues(t, 0, snap[0].Progress)

	r.SetProgress("t1", 42)
	r.AppendLog("t1", "下载中 42%")
	snap = r.Snapshot()
	require.EqualValues(t, 42, snap[0].Progress)
	// 日志行编码为 "<绝对序号>\t<正文>"（供 CP 幂等追加）。
	require.Equal(t, []string{"0\t下载中 42%"}, snap[0].RecentLogLines)

	r.Succeed("t1", `{"path":"/opt/jdk"}`)
	snap = r.Snapshot()
	require.Equal(t, "succeeded", snap[0].State)
	require.EqualValues(t, 100, snap[0].Progress)
	require.Equal(t, `{"path":"/opt/jdk"}`, snap[0].Result)

	r.Drop("t1")
	require.Empty(t, r.Snapshot())
}

// 失败路径：Fail 置 failed + error。
func TestRegistry_Fail(t *testing.T) {
	r := New()
	r.Start("t2")
	r.Fail("t2", "下载返回 HTTP 404")
	snap := r.Snapshot()
	require.Len(t, snap, 1)
	require.Equal(t, "failed", snap[0].State)
	require.Equal(t, "下载返回 HTTP 404", snap[0].Error)
}

// 进度越界被夹紧到 0~100。
func TestRegistry_ProgressClamp(t *testing.T) {
	r := New()
	r.Start("t3")
	r.SetProgress("t3", 150)
	require.EqualValues(t, 100, r.Snapshot()[0].Progress)
	r.SetProgress("t3", -5)
	require.EqualValues(t, 0, r.Snapshot()[0].Progress)
}

// 日志超过上限按环形截断，仅保留最近 recentLogCap 行；绝对序号不重置（截断后仍单调）。
func TestRegistry_LogRingTruncate(t *testing.T) {
	r := New()
	r.Start("t4")
	total := recentLogCap + 10
	for i := 0; i < total; i++ {
		r.AppendLog("t4", "line")
	}
	lines := r.Snapshot()[0].RecentLogLines
	require.Len(t, lines, recentLogCap)
	// 首行的绝对序号应为 total-recentLogCap（最旧 10 行已被丢弃，序号未重置）。
	require.Equal(t, strconv.Itoa(total-recentLogCap)+"\tline", lines[0])
	require.Equal(t, strconv.Itoa(total-1)+"\tline", lines[len(lines)-1])
}

// 并发读写不竞态（go test -race 守护）。
func TestRegistry_ConcurrentSafe(t *testing.T) {
	r := New()
	r.Start("t5")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			r.SetProgress("t5", int32(n))
			r.AppendLog("t5", "x")
			_ = r.Snapshot()
		}(i)
	}
	wg.Wait()
	require.NotEmpty(t, r.Snapshot())
}

// 未知 taskID 的更新被安全忽略，不 panic。
func TestRegistry_UnknownTaskIgnored(t *testing.T) {
	r := New()
	r.SetProgress("nope", 10)
	r.AppendLog("nope", "x")
	r.Succeed("nope", "")
	r.Fail("nope", "")
	require.Empty(t, r.Snapshot())
}
