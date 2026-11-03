package ingest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

var ErrMaterializationBudget = errors.New("ingest: 重建物化预算不足，只允许只读取证或安全复用")

func materializationLimitsOf(value *stateindex.MaterializationLimits) stateindex.MaterializationLimits {
	limits := stateindex.MaterializationLimits{WindowCopies: 4, HourlyCopies: 8, HourlyBytes: 1 << 30, CompactionReserveBytes: 1 << 28}
	if value != nil {
		if value.WindowCopies > 0 {
			limits.WindowCopies = value.WindowCopies
		}
		if value.HourlyCopies > 0 {
			limits.HourlyCopies = value.HourlyCopies
		}
		if value.HourlyBytes > 0 {
			limits.HourlyBytes = value.HourlyBytes
		}
		limits.CompactionReserveBytes = value.CompactionReserveBytes
	}
	return limits
}

// canonicalWindowKey 不含物理代次或摄取时刻，集合换序也不能绕过同内容副本预算。
func canonicalWindowKey(events []logtypes.Event) string {
	ordered := append([]logtypes.Event(nil), events...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].EventID != ordered[j].EventID {
			return ordered[i].EventID < ordered[j].EventID
		}
		if ordered[i].CanonicalHash != ordered[j].CanonicalHash {
			return ordered[i].CanonicalHash < ordered[j].CanonicalHash
		}
		if ordered[i].Record.Start != ordered[j].Record.Start {
			return ordered[i].Record.Start < ordered[j].Record.Start
		}
		return ordered[i].Record.End < ordered[j].Record.End
	})
	return fmt.Sprintf("canonical-%x", eventSetDigest(ordered))
}

func projectionWireBytes(events []logtypes.Event, generation string) (int64, error) {
	var total int64
	for start := 0; start < len(events); start += insertBatchMaxEvents {
		end := start + insertBatchMaxEvents
		if end > len(events) {
			end = len(events)
		}
		payload, err := projectionPayload(events[start:end], generation)
		if err != nil {
			return 0, err
		}
		total += int64(len(payload))
	}
	return total, nil
}

func (m *Manager) materializationNow() time.Time {
	if m.rebuilds != nil {
		return m.rebuilds.clockNow()
	}
	return time.Now()
}

func (m *Manager) reserveMaterializationClass(ctx context.Context, source SourceConfig, generation string, events, window []logtypes.Event, class string) (stateindex.MaterializationReservation, func(), error) {
	if class == "" {
		class = "rebuild"
	}
	m.mu.Lock()
	store := m.index
	stopped := m.stopped
	m.mu.Unlock()
	if stopped || store == nil {
		return stateindex.MaterializationReservation{}, nil, fmt.Errorf("ingest: 物化台账不可用，拒绝新增物理副本")
	}
	wireBytes, err := projectionWireBytes(events, generation)
	if err != nil {
		return stateindex.MaterializationReservation{}, nil, err
	}
	release := func() {}
	if m.resourceBudget != nil {
		work := vlsup.WorkBackground
		if class == "live_compaction" {
			work = vlsup.WorkWALDrain
		}
		if err := m.resourceBudget.Admit(work); err != nil {
			return stateindex.MaterializationReservation{}, nil, err
		}
		release, err = m.resourceBudget.Reserve(vlsup.ReservationStaging, wireBytes)
		if err != nil {
			return stateindex.MaterializationReservation{}, nil, err
		}
	}
	limits := m.materializationLimits
	if limits.WindowCopies == 0 {
		limits = materializationLimitsOf(nil)
	}
	if class == "live_compaction" {
		limits.WindowCopies = 4
		limits.HourlyCopies = 4
	}
	reservation, permitted, retryAt, err := store.ReserveMaterialization(ctx, stateindex.MaterializationRequest{
		Class:     class,
		SourceKey: source.LogSourceID + "/" + source.SourceGeneration, UTCDay: source.UTCDay,
		WindowKey: canonicalWindowKey(window), Generation: generation, EstimatedBytes: wireBytes, Now: m.materializationNow(),
	}, limits)
	if err != nil || !permitted {
		release()
		if err != nil {
			return reservation, nil, err
		}
		return reservation, nil, fmt.Errorf("%w (source=%s day=%s retry=%s)", ErrMaterializationBudget, source.LogSourceID, source.UTCDay, retryAt.UTC().Format(time.RFC3339))
	}
	// reserved 已经计费，未知必须在发送之前持久化；任何更新失败均零外发。
	if err := store.UpdateMaterialization(ctx, reservation.ID, stateindex.MaterializationUpdate{State: "unknown", Detail: "发送前持久预留；包括部分写入、响应丢失与发布失效"}); err != nil {
		release()
		return reservation, nil, err
	}
	return reservation, release, nil
}

func (m *Manager) updateMaterialization(ctx context.Context, reservation stateindex.MaterializationReservation, state string, verified, published bool, detail string) error {
	if reservation.ID == 0 {
		return nil
	}
	m.mu.Lock()
	store := m.index
	m.mu.Unlock()
	if store == nil {
		return fmt.Errorf("ingest: 物化回执仓储不可用")
	}
	return store.UpdateMaterialization(ctx, reservation.ID, stateindex.MaterializationUpdate{State: state, WrittenBytes: reservation.ReservedBytes, Verified: verified, Published: published, Detail: detail})
}

// 复用只消耗读取：同一稳定窗口的既有不可变代次仍须完整逐字段核验并通过新鲜发布CAS。
func (m *Manager) tryReuseMaterialization(ctx context.Context, source SourceConfig, events []logtypes.Event, snapshot *publicationSnapshot, yield func()) (bool, error) {
	m.mu.Lock()
	store := m.index
	m.mu.Unlock()
	if store == nil {
		return false, fmt.Errorf("ingest: 物化台账不可用")
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	rows, err := store.ListMaterializations(ctx, key, source.UTCDay, canonicalWindowKey(events), 0, 200)
	if err != nil {
		return false, err
	}
	if len(rows) == 0 {
		return false, nil
	}
	permit, err := m.cat.AcquireWritePermit(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
	if err != nil {
		return false, err
	}
	defer m.cat.ReleaseWritePermit(permit)
	client, _, err := m.clientForSource(source)
	if m.vlWritePermitRoute != nil {
		client, err = m.vlWritePermitRoute(source, permit)
	}
	if err != nil || client == nil {
		return false, fmt.Errorf("ingest: 复用目标不可读: %w", err)
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		observed, _ := m.cat.Get(permit.Key)
		if _, err := m.verifyProjectionCtxYieldRuns(ctx, client, source, rows[i].Generation, events, yield); err != nil {
			// 未知或只写了一部分时不往原代次补写，避免迟到的未知请求造成重复。
			continue
		}
		if err := m.publishSnapshot(source, rows[i].Generation, events, true, observed, snapshot, true, permit); err != nil {
			return false, err
		}
		if err := m.updateMaterialization(ctx, rows[i], "published", true, true, "已有不可变副本逐字段复核后安全复用，零物理写入"); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}
