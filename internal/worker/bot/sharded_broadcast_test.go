package bot

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// broadcastCommandSchedule 的部分失败语义回归。
//
// 【背景】原实现把 merged.Accepted 初始置 true，循环里只要任一片 evt.Accepted
// 就保持 true（anyAccepted 只用于“全都没接受”的兜底）。于是 3 片里 1 片接受、
// 2 片拒绝时对外报 accepted=true——CP 据此认为命令计划已全量下发，
// 实际有 2/3 的 bot 收不到，而且没有任何信号让运维去查。
//
// 正确语义：**全部片接受才算接受**；任一片拒绝/报错/无回执都是整体失败，
// 且 Error 里要能看出有几片出了问题。
//
// 这里直接驱动 broadcastCommandSchedule（传入构造回执的闭包），
// 不依赖真实 bot-worker 子进程。

type shardReply struct {
	evt *BotWorkerEvent
	err error
}

// runBroadcast 用给定的逐片回执序列驱动一次广播。
func runBroadcast(t *testing.T, replies []shardReply) *BotWorkerEvent {
	t.Helper()
	s := newTestSharded(t, len(replies), len(replies)*10, nil)
	idx := 0
	evt, err := s.broadcastCommandSchedule(context.Background(), "req-1", "command-schedule-accepted",
		func(_ *Manager, _ context.Context, _ string) (*BotWorkerEvent, error) {
			r := replies[idx]
			idx++
			return r.evt, r.err
		})
	require.NoError(t, err, "广播本身不应返回 error（失败体现在 Accepted/Error 里）")
	require.NotNil(t, evt)
	return evt
}

// TestBroadcastCommandSchedule_AllAccepted 全部片接受 → accepted=true，无错误。
func TestBroadcastCommandSchedule_AllAccepted(t *testing.T) {
	evt := runBroadcast(t, []shardReply{
		{evt: &BotWorkerEvent{Evt: "command-schedule-accepted", Accepted: true, ScheduleRunID: "run-a"}},
		{evt: &BotWorkerEvent{Evt: "command-schedule-accepted", Accepted: true}},
		{evt: &BotWorkerEvent{Evt: "command-schedule-accepted", Accepted: true}},
	})
	assert.True(t, evt.Accepted)
	assert.Empty(t, evt.Error)
	assert.Equal(t, "run-a", evt.ScheduleRunID)
	assert.Equal(t, "command-schedule-accepted", evt.Evt)
}

// TestBroadcastCommandSchedule_PartialRejectIsFailure 1 接受 + 2 拒绝 → 必须报失败。
//
// 这是本次修复的核心用例：旧实现会错报 accepted=true。
func TestBroadcastCommandSchedule_PartialRejectIsFailure(t *testing.T) {
	evt := runBroadcast(t, []shardReply{
		{evt: &BotWorkerEvent{Evt: "command-schedule-accepted", Accepted: true}},
		{evt: &BotWorkerEvent{Evt: "command-schedule-accepted", Accepted: false, Error: "bot 不存在"}},
		{evt: &BotWorkerEvent{Evt: "command-schedule-accepted", Accepted: false}},
	})
	assert.False(t, evt.Accepted, "部分片拒绝时不得对外报接受")
	assert.Contains(t, evt.Error, "2 片拒绝", "错误信息要说清有几片拒绝：%s", evt.Error)
	assert.Contains(t, evt.Error, "共 3 片")
}

// TestBroadcastCommandSchedule_RPCErrorIsFailure 单片 RPC 报错也要整体失败。
func TestBroadcastCommandSchedule_RPCErrorIsFailure(t *testing.T) {
	evt := runBroadcast(t, []shardReply{
		{evt: &BotWorkerEvent{Evt: "command-schedule-accepted", Accepted: true}},
		{err: errors.New("shard s1 unreachable")},
	})
	assert.False(t, evt.Accepted)
	assert.Contains(t, evt.Error, "shard s1 unreachable", "原始错误要保留（运维靠它定位哪片挂了）")
}

// TestBroadcastCommandSchedule_NilEventCountsAsFailure 无回执不能当接受。
func TestBroadcastCommandSchedule_NilEventCountsAsFailure(t *testing.T) {
	evt := runBroadcast(t, []shardReply{
		{evt: &BotWorkerEvent{Evt: "command-schedule-accepted", Accepted: true}},
		{evt: nil},
	})
	assert.False(t, evt.Accepted, "无回执 = 未确认，不得算接受")
	assert.Contains(t, evt.Error, "1 片失败")
}

// TestBroadcastCommandSchedule_EvtFallbackToDeclaredType 各片都没回 Evt 时用调用方声明的类型。
//
// 上游按 `event.Evt != <期望值>` 判回执合法性，空 Evt 会被当成 IPC_FAILED。
func TestBroadcastCommandSchedule_EvtFallbackToDeclaredType(t *testing.T) {
	evt := runBroadcast(t, []shardReply{
		{evt: &BotWorkerEvent{Accepted: true}},
		{evt: &BotWorkerEvent{Accepted: true}},
	})
	assert.Equal(t, "command-schedule-accepted", evt.Evt)
	assert.True(t, evt.Accepted)
}

// TestBroadcastCommandSchedule_SingleShardPassthrough 单片直接透传（不走合并逻辑）。
func TestBroadcastCommandSchedule_SingleShardPassthrough(t *testing.T) {
	s := newTestSharded(t, 1, 10, nil)
	want := &BotWorkerEvent{Evt: "command-schedule-accepted", Accepted: false, Error: "直接透传"}
	evt, err := s.broadcastCommandSchedule(context.Background(), "req-solo", "command-schedule-accepted",
		func(_ *Manager, _ context.Context, reqID string) (*BotWorkerEvent, error) {
			assert.Equal(t, "req-solo", reqID, "单片不应改写 requestId")
			return want, nil
		})
	require.NoError(t, err)
	assert.Same(t, want, evt, "单片应原样透传，不做合并")
}
