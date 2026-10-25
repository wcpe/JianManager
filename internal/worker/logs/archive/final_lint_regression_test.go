package archive

import (
	"context"
	"errors"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

func TestFinalLintArchiveGenerationRejectsInvalidAndOverflow(t *testing.T) {
	for _, raw := range []string{"18446744073709551616", "-1", "NaN", "", "0"} {
		t.Run(raw, func(t *testing.T) {
			key, err := partitionKey(query.QueryRequest{AuthorizedTargets: []string{"node:lint/2026-09-22/g" + raw}})
			require.Error(t, err)
			require.Equal(t, PartitionKey{}, key, "坏代次不能饱和或回退为合法键")
		})
	}
	for _, generation := range []uint64{1, math.MaxUint64} {
		key, err := partitionKey(query.QueryRequest{AuthorizedTargets: []string{"node:lint/2026-09-22/g" + strconv.FormatUint(generation, 10)}})
		require.NoError(t, err)
		require.Equal(t, generation, key.Generation)
	}
}

func TestFinalLintArchiveSummaryRejectsInvalidTimesAndEvictsStaleSuccess(t *testing.T) {
	for _, invalid := range []string{"", "not-a-time", "2026-13-22T12:00:00Z", "9999-12-31T23:59:59.999999999Z"} {
		t.Run(invalid, func(t *testing.T) {
			backend := NewQueryBackend(nil, nil)
			merge := "same-partition-and-objects"
			backend.rememberPublished(merge, "scope", &RehydrateJob{TaskID: "old"}, []ArchivedEvent{{EventTimeUTC: "2026-09-22T12:00:00Z"}})
			_, ok := backend.PublishedRehydrateSummary("old")
			require.True(t, ok)
			backend.rememberPublished(merge, "scope", &RehydrateJob{TaskID: "new"}, []ArchivedEvent{{EventTimeUTC: "2026-09-22T12:00:00Z"}, {EventTimeUTC: invalid}})
			_, ok = backend.PublishedRehydrateSummary("new")
			require.False(t, ok, "坏时间不能生成可验证成功摘要")
			_, ok = backend.PublishedRehydrateSummary("old")
			require.False(t, ok, "同一合并键的新摘要不可用时不得残留旧成功")
		})
	}
	backend := NewQueryBackend(nil, nil)
	backend.rememberPublished("empty", "scope", &RehydrateJob{TaskID: "empty"}, nil)
	_, ok := backend.PublishedRehydrateSummary("empty")
	require.False(t, ok)
}

func TestFinalLintArchiveSummaryComputesRealExtremaWithoutZeroSentinel(t *testing.T) {
	backend := NewQueryBackend(nil, nil)
	backend.rememberPublished("unordered", "scope", &RehydrateJob{TaskID: "valid"}, []ArchivedEvent{
		{EventTimeUTC: "2026-09-22T12:00:00Z"},
		{EventTimeUTC: "0001-01-01T00:00:00Z"},
		{EventTimeUTC: "2026-09-23T14:00:00+02:00"},
	})
	summary, ok := backend.PublishedRehydrateSummary("valid")
	require.True(t, ok)
	require.True(t, summary.Verified)
	require.True(t, summary.Published)
	require.Equal(t, uint64(3), summary.EventCount)
	require.Equal(t, "0001-01-01T00:00:00Z", summary.TimeRange.FromUTC)
	require.Equal(t, "2026-09-23T12:00:00.000000001Z", summary.TimeRange.ToUTC)
}

func TestFinalLintArchiveUnavailableTerminalDoesNotFabricateState(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "fail_not_found", true: "cancel_not_found"}[cancelled], func(t *testing.T) {
			backend, provider, key, req, ids, _ := newArchiveBudgetBackend(t, "terminal unavailable")
			tracker := &archiveBudgetTracker{}
			callbacks := tracker.callbacks()
			reserve := callbacks.ReserveStaging
			cause := errors.New("构造的预算失败")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			callbacks.ReserveStaging = func(ctx context.Context, n int64) (func(), error) {
				release, err := reserve(ctx, n)
				if err != nil {
					return nil, err
				}
				// 模拟终态记录不可用；消费者hold仍必须绑定实际退出而不是错误处理分支。
				backend.Tasks.mu.Lock()
				require.NotEmpty(t, backend.Tasks.byTask)
				for id := range backend.Tasks.byTask {
					delete(backend.Tasks.byTask, id)
				}
				backend.Tasks.mu.Unlock()
				if cancelled {
					cancel()
				}
				return release, cause
			}
			backend.SetResourceBudget(callbacks)
			res := backend.Rehydrate(ctx, req, ids)
			require.NotNil(t, res.Err)
			require.Empty(t, res.State, "未成功转换且无法读取的任务不能伪造失败或取消终态")
			require.Contains(t, res.Err.Message, cause.Error())
			require.Contains(t, res.Err.Message, ErrTaskNotFound.Error())
			_, reserves, releases, active, _ := tracker.snapshot()
			require.Equal(t, reserves, releases)
			require.Zero(t, active)
			opens, gets, _, n := provider.counts()
			require.Zero(t, opens)
			require.Zero(t, gets)
			require.Zero(t, n)
			backend.Tasks.mu.Lock()
			for _, hold := range backend.Tasks.holds {
				require.NotEqual(t, holdKindArchiveConsumer, hold.Kind)
			}
			backend.Tasks.mu.Unlock()
			// 未知任务的任务hold保留到既有期限，不能因未知状态提前允许清理原物。
			require.NotEmpty(t, backend.Tasks.ActiveHolds(key))
		})
	}
}

func TestFinalLintArchivePinFailureDoesNotFabricateState(t *testing.T) {
	backend, provider, _, req, ids, _ := newArchiveBudgetBackend(t, "pin task unavailable")
	tracker := &archiveBudgetTracker{}
	backend.SetResourceBudget(tracker.callbacks())
	sequence := 0
	backend.Tasks.opts.IDGen = func(prefix string) string {
		sequence++
		if prefix == "hold" {
			// Start持有manager锁时模拟记录丢失，pin必须拒绝且不能宣称终态转换成功。
			for id := range backend.Tasks.byTask {
				delete(backend.Tasks.byTask, id)
			}
		}
		return prefix + "-" + strconv.Itoa(sequence)
	}
	res := backend.Rehydrate(context.Background(), req, ids)
	require.NotNil(t, res.Err)
	require.Empty(t, res.State)
	require.Contains(t, res.Err.Message, "recovery task disappeared")
	require.Contains(t, res.Err.Message, ErrTaskNotFound.Error())
	admissions, reserves, releases, active, _ := tracker.snapshot()
	require.Zero(t, admissions)
	require.Zero(t, reserves)
	require.Zero(t, releases)
	require.Zero(t, active)
	opens, gets, _, n := provider.counts()
	require.Zero(t, opens)
	require.Zero(t, gets)
	require.Zero(t, n)
	backend.Tasks.mu.Lock()
	defer backend.Tasks.mu.Unlock()
	for _, hold := range backend.Tasks.holds {
		require.NotEqual(t, holdKindArchiveConsumer, hold.Kind)
	}
}

func TestFinalLintArchiveRepeatedCancelAndExpiredTaskKeepObservedState(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "repeated_cancel", true: "expired"}[expired], func(t *testing.T) {
			backend, _, key, req, ids, _ := newArchiveBudgetBackend(t, "already terminal")
			tracker := &archiveBudgetTracker{}
			backend.SetResourceBudget(tracker.callbacks())
			backend.SetPublisher(publisherFunc(func(_ context.Context, _ PartitionKey, _ []ArchivedEvent, taskID string) error {
				if expired {
					backend.Tasks.mu.Lock()
					entry := backend.Tasks.byTask[taskID]
					entry.mu.Lock()
					entry.job.Deadline = time.Now().Add(-time.Hour)
					entry.mu.Unlock()
					backend.Tasks.mu.Unlock()
					_, err := backend.Tasks.Complete(taskID, nil)
					require.Error(t, err)
				} else {
					require.NoError(t, backend.Tasks.Cancel(taskID))
					require.NoError(t, backend.Tasks.Cancel(taskID))
				}
				require.NotEmpty(t, backend.Tasks.ActiveHolds(key), "消费者未退出时取消或过期不能先拆除保护")
				return context.Canceled
			}))
			res := backend.Rehydrate(context.Background(), req, ids)
			require.NotNil(t, res.Err)
			state := TaskCancelled
			if expired {
				state = TaskStale
			}
			require.Equal(t, string(state), res.State)
			require.Empty(t, backend.Tasks.ActiveHolds(key))
			_, reserves, releases, active, _ := tracker.snapshot()
			require.Equal(t, reserves, releases)
			require.Zero(t, active)
		})
	}
}
