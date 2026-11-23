package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// preflight 取容量时必须优先走 Refresh（实时快照），而非 Snapshot（15s TTL 缓存）。
//
// 真机事故：舰队长时间运行后，容量缓存里的快照已陈旧（ObservedAt 不再推进），
// 预检据此判定 CAPACITY_SNAPSHOT_STALE + 可用容量 0，导致明明有空闲容量也启动不了新批次，
// 需反复重试直到偶尔命中新鲜缓存。预检是「启动前的实时校验」，必须读实时值。

// fakeRefreshingCapacityProvider 同时实现 Snapshot 与 Refresh，并记录各自被调用次数。
type fakeRefreshingCapacityProvider struct {
	snapshotCalls int
	refreshCalls  int
	snapshot      BotLoadCapacitySnapshot
	refreshed     BotLoadCapacitySnapshot
}

func (p *fakeRefreshingCapacityProvider) Snapshot(context.Context, uint) (*BotLoadCapacitySnapshot, error) {
	p.snapshotCalls++
	out := p.snapshot
	return &out, nil
}

func (p *fakeRefreshingCapacityProvider) Refresh(context.Context, uint) (*BotLoadCapacitySnapshot, error) {
	p.refreshCalls++
	out := p.refreshed
	return &out, nil
}

func TestPreflightCapacity_PrefersRefresh(t *testing.T) {
	p := &fakeRefreshingCapacityProvider{
		snapshot:  BotLoadCapacitySnapshot{NodeCapacities: []BotLoadNodeCapacity{{NodeID: 1, AvailableBots: 0, UnavailableReason: BotLoadUnavailableSnapshotStale}}},
		refreshed: BotLoadCapacitySnapshot{NodeCapacities: []BotLoadNodeCapacity{{NodeID: 1, AvailableBots: 42}}},
	}

	got, err := RefreshBotLoadCapacity(context.Background(), p, 7)
	require.NoError(t, err)

	assert.Equal(t, 1, p.refreshCalls, "应调用 Refresh 拿实时快照")
	assert.Equal(t, 0, p.snapshotCalls, "不应退回缓存读")
	require.Len(t, got.NodeCapacities, 1)
	assert.Equal(t, 42, got.NodeCapacities[0].AvailableBots, "应采用 Refresh 的返回值")
}

// TestPreflightCapacity_FallsBackToSnapshot 验证替身只实现 Snapshot 时仍可用（向后兼容）。
func TestPreflightCapacity_FallsBackToSnapshot(t *testing.T) {
	p := &fakeBotLoadCapacityProvider{snapshot: BotLoadCapacitySnapshot{
		NodeCapacities: []BotLoadNodeCapacity{{NodeID: 1, AvailableBots: 9}},
	}}

	got, err := RefreshBotLoadCapacity(context.Background(), p, 3)
	require.NoError(t, err)
	require.Len(t, got.NodeCapacities, 1)
	assert.Equal(t, 9, got.NodeCapacities[0].AvailableBots)
	assert.Equal(t, []uint{3}, p.excludes, "应把 runID 作为排除项传入")
}
