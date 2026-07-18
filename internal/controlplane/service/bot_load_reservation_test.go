package service

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBotLoadReservationStore_ReplaceExcludeAndExpire(t *testing.T) {
	clock := &botLoadFakeClock{now: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	store := NewBotLoadReservationStore(clock, time.Minute)
	limits := map[uint]int{1: 50, 2: 20}

	_, err := store.Replace(1, map[uint]int{1: 30, 2: 10}, limits)
	require.NoError(t, err)
	require.Equal(t, map[uint]int{1: 30, 2: 10}, store.Snapshot(0))
	require.Empty(t, store.Snapshot(1))

	_, err = store.Replace(1, map[uint]int{1: 10}, limits)
	require.NoError(t, err)
	require.Equal(t, map[uint]int{1: 10}, store.Snapshot(0))

	_, err = store.Replace(2, map[uint]int{1: 45}, limits)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrBotLoadReservationCapacity))
	require.Equal(t, map[uint]int{1: 10}, store.Snapshot(0))

	clock.Advance(time.Minute + time.Nanosecond)
	store.Cleanup()
	require.Empty(t, store.Snapshot(0))
	_, err = store.Replace(2, map[uint]int{1: 45}, limits)
	require.NoError(t, err)
}

func TestBotLoadReservationStore_ConcurrentReservationsDoNotOversell(t *testing.T) {
	clock := &botLoadFakeClock{now: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	store := NewBotLoadReservationStore(clock, time.Minute)
	limits := map[uint]int{1: 50}
	var successes atomic.Int32
	var wg sync.WaitGroup

	for runID := uint(1); runID <= 20; runID++ {
		wg.Add(1)
		go func(runID uint) {
			defer wg.Done()
			if _, err := store.Replace(runID, map[uint]int{1: 10}, limits); err == nil {
				successes.Add(1)
			}
		}(runID)
	}
	wg.Wait()

	require.EqualValues(t, 5, successes.Load())
	require.Equal(t, 50, store.Snapshot(0)[1])
}

func TestBotLoadReservationStore_RestorePreviousLease(t *testing.T) {
	clock := &botLoadFakeClock{now: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	store := NewBotLoadReservationStore(clock, time.Minute)
	limits := map[uint]int{1: 50}
	oldLease, err := store.Replace(9, map[uint]int{1: 30}, limits)
	require.NoError(t, err)
	newLease, err := store.Replace(9, map[uint]int{1: 20}, limits)
	require.NoError(t, err)

	require.True(t, store.RestoreIfCurrent(9, newLease.Revision, oldLease))
	require.Equal(t, 30, store.Snapshot(0)[1])
	require.False(t, store.RestoreIfCurrent(9, newLease.Revision, oldLease))
}
