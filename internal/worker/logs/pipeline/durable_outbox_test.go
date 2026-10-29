package pipeline

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// TestDurableOutboxDefaultSynchronousAppendRejectDrainsAfterUnlock 守住「追加被拒后仍须**真实**
// 同步完成一次存量排空，且不得重入采集锁自锁」。
//
// ## 2026-10-29 改写（触发形态）
//
// 旧夹具靠 `WALLimits{MaxEntries: 1}` 造「积压越上限 ⇒ 源暂停 ⇒ 追加被拒」。
// 积压闸已改成**丢弃**（不暂停、不背压，见 acquire.WAL 的 noteDrop 注释与 9a09d2fb），
// 所以那个形态**不存在了**，旧夹具在改动前就已经转红（删前基线已实测）。
//
// 但用例的**真实关切一点也没变**：`handleAppendRejected` 在未接调度者时走
// `deliverPendingBestEffort`，它必须（①）在同步调用返回前真的把存量投出去（不能只登记
// 一个无人处理的待办），（②）不得重入 `p.mu` 自锁（那会把采集锁死）。
// 现在改用**仍然存在**的拒绝形态：容量暂停（`acquire: paused (disk full)`，
// WAL appendLocked 的首道闸，未受积压语义改动影响）。
//
// 转红方式（实测）：把 handleAppendRejected 里的 deliverPendingBestEffort 改成空操作
// ⇒ callsAfter == callsBefore，本用例最后一条断言红。
func TestDurableOutboxDefaultSynchronousAppendRejectDrainsAfterUnlock(t *testing.T) {
	var calls atomic.Int64
	var failOnce atomic.Bool
	failOnce.Store(true)
	p, err := New(Options{LogSourceID: "sync-reject", SourceGeneration: "g1", Mode: ModeFilePrimary, Path: buildConcurrencyFixture(t, 3), Delivery: FuncHook(func([]logtypes.Event, bool) (DeliveryResult, error) {
		calls.Add(1)
		if failOnce.CompareAndSwap(true, false) {
			return DeliveryResult{HTTPStatus: 500}, errors.New("injected delivery failure")
		}
		return DeliveryResult{HTTPStatus: 200}, nil
	})})
	require.NoError(t, err)

	// 第一轮投递失败 ⇒ 事件留在 WAL（durable 但未确认），这就是“待排空的存量”。
	_, err = p.Poll()
	require.Error(t, err)
	require.NotEmpty(t, p.WAL().Snapshot(), "投递失败的事件必须留在 WAL 里")
	before := calls.Load()

	// 造一次真实的追加拒绝：容量暂停（WAL appendLocked 的首道闸）。
	require.NoError(t, p.Ledger().PauseAcquire(p.Key(), "disk full"))
	state, ok := p.Ledger().AcquireState(p.Key())
	require.True(t, ok)
	require.True(t, state.Paused, "自然触发容量门而非伪造钩子")

	done := make(chan error, 1)
	go func() { _, flushErr := p.Flush(); done <- flushErr }()
	select {
	case err := <-done:
		require.Error(t, err, "本轮新行被拒仍须如实返回拒绝")
		require.ErrorContains(t, err, "paused", "被拒的原因必须是容量暂停（积压闸已不再产生暂停）")
	case <-time.After(time.Second):
		t.Fatal("默认同步Append拒绝fallback重入采集p.mu自锁")
	}
	require.Greater(t, calls.Load(), before, "同步调用返回前仍须真实完成保留WAL排空，不可改成只登记无人处理的待办")
}

func TestDurableOutboxAllEmitEntriesCommitOutsideAcquisitionLock(t *testing.T) {
	for _, kind := range []string{"Poll", "Drain", "Flush", "Stale", "Rotation"} {
		t.Run(kind, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			var armed atomic.Bool
			var once sync.Once
			var scheduled, proofs, external atomic.Int64
			path := buildConcurrencyFixture(t, 2)
			p, err := New(Options{LogSourceID: "outbox-pipeline", SourceGeneration: "g1", Mode: ModeFilePrimary, Path: path,
				CommittedPersist: func(context.Context, []acquire.CommitIdentity) error {
					if armed.Load() {
						once.Do(func() { close(entered); <-release })
					}
					return nil
				},
				DeferredDelivery: func() { scheduled.Add(1) }, ReclaimProof: func([]logtypes.Event) error { proofs.Add(1); return nil },
				Delivery: FuncHook(func([]logtypes.Event, bool) (DeliveryResult, error) {
					external.Add(1)
					return DeliveryResult{HTTPStatus: 200}, nil
				}),
			})
			require.NoError(t, err)
			if kind == "Flush" || kind == "Stale" || kind == "Rotation" {
				_, err = p.Poll()
				require.NoError(t, err)
			}
			before := scheduled.Load()
			armed.Store(true)
			done := make(chan error, 1)
			go func() {
				var err error
				switch kind {
				case "Poll":
					_, err = p.Poll()
				case "Drain":
					_, err = p.Drain()
				case "Flush":
					_, err = p.Flush()
				case "Stale":
					_, err = p.FlushStaleMultiline(time.Now().Add(time.Hour), time.Millisecond)
				case "Rotation":
					_, err = p.FlushClosedSegment()
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("入口没有真实到达提交阶段")
			}
			short := make(chan struct{})
			go func() { p.SetRotateTarget("rotated.log"); close(short) }()
			select {
			case <-short:
			case <-time.After(100 * time.Millisecond):
				t.Fatal("提交回调仍持有采集状态锁")
			}
			require.Equal(t, before, scheduled.Load(), "真正耐久完成前不能交办成功")
			close(release)
			require.NoError(t, <-done)
			require.Equal(t, before+1, scheduled.Load())
			require.Zero(t, external.Load(), "异步入口只登记源，不在采集路径做网络")
			require.Zero(t, proofs.Load(), "不得借旧DeliveryState执行回收成功尾段")
		})
	}
}

func TestDurableOutboxWALAndPersistFailuresNeverDispatch(t *testing.T) {
	for _, kind := range []string{"wal", "sqlite", "incomplete", "handoff"} {
		t.Run(kind, func(t *testing.T) {
			var external, scheduled atomic.Int64
			p, err := New(Options{LogSourceID: "outbox-fail", SourceGeneration: "g1", Mode: ModeFilePrimary, Path: buildConcurrencyFixture(t, 2),
				CommittedPersist: func(context.Context, []acquire.CommitIdentity) error {
					if kind != "wal" {
						return errors.New(kind)
					}
					return nil
				},
				DeferredDelivery: func() { scheduled.Add(1) }, Delivery: FuncHook(func([]logtypes.Event, bool) (DeliveryResult, error) {
					external.Add(1)
					return DeliveryResult{HTTPStatus: 200}, nil
				}),
			})
			require.NoError(t, err)
			if kind == "wal" {
				p.WAL().SetFsync(func() error { return errors.New("fsync failed") })
			}
			_, err = p.Drain()
			require.Error(t, err)
			_, err = p.DeliverPending()
			require.Error(t, err)
			require.Zero(t, scheduled.Load())
			require.Zero(t, external.Load())
			require.Empty(t, p.Ledger().Get(p.Key()).RequestDoneRanges)
			require.NotEmpty(t, p.WAL().Snapshot())
		})
	}
}
