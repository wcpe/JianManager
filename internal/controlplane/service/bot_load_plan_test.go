package service

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type botLoadFakeClock struct {
	now time.Time
}

func (c *botLoadFakeClock) Now() time.Time { return c.now }
func (c *botLoadFakeClock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
}

func readyBotLoadNode(id uint, available int, generation int64) BotLoadNodeCapacity {
	return BotLoadNodeCapacity{
		NodeID: id, NodeUUID: "node-uuid-" + string(rune('a'+id)), NodeName: "node",
		Online: true, BotWorkerReady: true, MaxBots: available, AvailableBots: available,
		CapacityGeneration: generation,
	}
}

func TestBotLoadPlanner_Table(t *testing.T) {
	start := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		target      int
		nodes       []BotLoadNodeCapacity
		nodeIDs     []uint
		wantReady   bool
		wantNodes   []uint
		wantCounts  []int
		wantBlocker string
	}{
		{
			name:   "500 分配到十个节点",
			target: 500,
			nodes: func() []BotLoadNodeCapacity {
				out := make([]BotLoadNodeCapacity, 10)
				for i := range out {
					out[i] = readyBotLoadNode(uint(i+1), 50, int64(i+1))
				}
				return out
			}(),
			wantReady:  true,
			wantNodes:  []uint{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
			wantCounts: []int{50, 50, 50, 50, 50, 50, 50, 50, 50, 50},
		},
		{
			name:       "容量不均按轮转填充",
			target:     100,
			nodes:      []BotLoadNodeCapacity{readyBotLoadNode(1, 80, 1), readyBotLoadNode(2, 20, 1)},
			wantReady:  true,
			wantNodes:  []uint{1, 2, 1},
			wantCounts: []int{50, 20, 30},
		},
		{
			name:        "容量不足不生成部分计划",
			target:      101,
			nodes:       []BotLoadNodeCapacity{readyBotLoadNode(1, 50, 1), readyBotLoadNode(2, 50, 1)},
			wantBlocker: BotLoadCapacityInsufficientCode,
		},
		{
			name:       "用户节点顺序保留",
			target:     120,
			nodes:      []BotLoadNodeCapacity{readyBotLoadNode(1, 50, 1), readyBotLoadNode(2, 50, 1), readyBotLoadNode(3, 50, 1)},
			nodeIDs:    []uint{3, 1, 2},
			wantReady:  true,
			wantNodes:  []uint{3, 1, 2},
			wantCounts: []int{50, 50, 20},
		},
		{
			name:   "用户指定失联节点会阻断",
			target: 10,
			nodes: []BotLoadNodeCapacity{
				readyBotLoadNode(1, 50, 1),
				{NodeID: 2, NodeUUID: "node-2", NodeName: "offline", UnavailableReason: BotLoadUnavailableNodeOffline},
			},
			nodeIDs:     []uint{2, 1},
			wantBlocker: BotLoadNodeUnavailableCode,
		},
		{
			name:       "同节点容量超过五十生成多批",
			target:     120,
			nodes:      []BotLoadNodeCapacity{readyBotLoadNode(7, 120, 9)},
			wantReady:  true,
			wantNodes:  []uint{7, 7, 7},
			wantCounts: []int{50, 50, 20},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := (BotLoadPlanner{}).Plan(BotLoadPlanRequest{
				RunID: 42, RunUUID: "run-uuid", TargetBots: tt.target, NodeCapacities: tt.nodes,
				ExecutorNodeIDs: tt.nodeIDs, ConnectRatePerSecondPerNode: 5, ConnectStartAt: start,
			})
			require.NoError(t, err)
			require.Equal(t, tt.wantReady, got.Ready)
			if tt.wantBlocker != "" {
				require.Empty(t, got.Allocations)
				require.Contains(t, blockerCodes(got.Blockers), tt.wantBlocker)
				return
			}
			require.Equal(t, tt.wantNodes, allocationNodeIDs(got.Allocations))
			require.Equal(t, tt.wantCounts, allocationCounts(got.Allocations))
			for i, allocation := range got.Allocations {
				require.Equal(t, i+1, allocation.Ordinal)
				require.NotEmpty(t, allocation.BatchID)
				require.NotEmpty(t, allocation.IdempotencyKey)
				require.GreaterOrEqual(t, allocation.PlannedCount, 1)
				require.LessOrEqual(t, allocation.PlannedCount, 50)
				require.Equal(t, 200, allocation.ConnectIntervalMS)
			}
		})
	}
}

func TestBotLoadPlanner_AutomaticOrderAndDeterminism(t *testing.T) {
	start := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	nodes := []BotLoadNodeCapacity{
		readyBotLoadNode(4, 20, 7),
		readyBotLoadNode(2, 80, 8),
		readyBotLoadNode(1, 80, 9),
	}
	req := BotLoadPlanRequest{
		RunID: 88, RunUUID: "run-stable", TargetBots: 170, NodeCapacities: nodes,
		ConnectRatePerSecondPerNode: 5, ConnectStartAt: start,
	}
	first, err := (BotLoadPlanner{}).Plan(req)
	require.NoError(t, err)
	second, err := (BotLoadPlanner{}).Plan(req)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, []uint{1, 2, 4, 1, 2}, allocationNodeIDs(first.Allocations))
	require.Equal(t, start.Add(10*time.Second), first.Allocations[3].ConnectStartAt)

	hash1, err := BotLoadAllocationHash(req.RunID, req.TargetBots, first.Allocations)
	require.NoError(t, err)
	hash2, err := BotLoadAllocationHash(req.RunID, req.TargetBots, second.Allocations)
	require.NoError(t, err)
	require.Equal(t, hash1, hash2)
}

func TestBotLoadPlanToken_SignedContract(t *testing.T) {
	clock := &botLoadFakeClock{now: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	signer, err := NewBotLoadPlanTokenSigner([]byte("test-only-plan-token-secret"), clock)
	require.NoError(t, err)
	generations := []BotLoadNodeGeneration{{NodeID: 1, CapacityGeneration: 7}, {NodeID: 2, CapacityGeneration: 9}}
	token, expiresAt, err := signer.Issue(12, "allocation-hash", generations)
	require.NoError(t, err)
	require.Equal(t, clock.Now().Add(time.Minute), expiresAt)
	expectation := BotLoadPlanTokenExpectation{RunID: 12, AllocationHash: "allocation-hash", CapacityGenerations: generations}
	require.NoError(t, signer.Verify(token, expectation))

	tests := []struct {
		name   string
		mutate func() (string, BotLoadPlanTokenExpectation)
	}{
		{"run 不匹配", func() (string, BotLoadPlanTokenExpectation) { e := expectation; e.RunID = 13; return token, e }},
		{"allocation hash 变化", func() (string, BotLoadPlanTokenExpectation) {
			e := expectation
			e.AllocationHash = "changed"
			return token, e
		}},
		{"容量世代变化", func() (string, BotLoadPlanTokenExpectation) {
			e := expectation
			e.CapacityGenerations = []BotLoadNodeGeneration{{NodeID: 1, CapacityGeneration: 8}, {NodeID: 2, CapacityGeneration: 9}}
			return token, e
		}},
		{"token 被篡改", func() (string, BotLoadPlanTokenExpectation) {
			last := token[len(token)-1]
			replacement := byte('A')
			if last == replacement {
				replacement = 'B'
			}
			return token[:len(token)-1] + string(replacement), expectation
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotToken, gotExpectation := tt.mutate()
			err := signer.Verify(gotToken, gotExpectation)
			require.Error(t, err)
			require.True(t, errors.Is(err, ErrBotLoadCapacityChanged))
			var changed *BotLoadCapacityChangedError
			require.ErrorAs(t, err, &changed)
			require.Equal(t, BotLoadCapacityChangedCode, changed.Code)
		})
	}

	clock.Advance(time.Minute + time.Nanosecond)
	err = signer.Verify(token, expectation)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrBotLoadCapacityChanged))
}

func TestNewBotLoadPlanTokenSigner_RejectsEmptySecret(t *testing.T) {
	_, err := NewBotLoadPlanTokenSigner(nil, &botLoadFakeClock{now: time.Now()})
	require.Error(t, err)
}

func blockerCodes(blockers []BotLoadIssue) []string {
	out := make([]string, 0, len(blockers))
	for _, blocker := range blockers {
		out = append(out, blocker.Code)
	}
	return out
}

func allocationNodeIDs(allocations []BotLoadAllocation) []uint {
	out := make([]uint, 0, len(allocations))
	for _, allocation := range allocations {
		out = append(out, allocation.ExecutorNodeID)
	}
	return out
}

func allocationCounts(allocations []BotLoadAllocation) []int {
	out := make([]int, 0, len(allocations))
	for _, allocation := range allocations {
		out = append(out, allocation.PlannedCount)
	}
	return out
}
