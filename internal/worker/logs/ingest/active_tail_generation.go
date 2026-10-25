package ingest

import (
	"context"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 活跃尾代次资格仅在本进程完整写入、核验、发布后建立；不是可跨重启继承的许可。
type activeTailReceipt struct {
	day        string
	generation string
	closed     uint64
	authority  *catalog.Record
	token      string
}

func (m *Manager) claimActiveTailGeneration(source SourceConfig, events []logtypes.Event) (string, *generationReservation) {
	_, days, err := groupEventsByUTCDay(source, events)
	if err != nil || len(days) != 1 {
		return "", nil
	}
	daySource := source
	daySource.UTCDay = days[0]
	key := source.LogSourceID + "/" + source.SourceGeneration
	observed, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: days[0]})
	m.mu.Lock()
	pending := m.state.Sources[key].PublicationPending
	m.mu.Unlock()
	// 已知同日洞会阻止发布；写前就必须隔离，不能先把尾行塞进仍在PP里的活跃旧代次。
	if debt, debtErr := m.hasKnownUnpublishedDayDebt(context.Background(), daySource, events, observed, pending); debtErr != nil || debt {
		return "", nil
	}
	m.publishMu.Lock()
	defer m.publishMu.Unlock()
	current, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: days[0]})
	m.mu.Lock()
	defer m.mu.Unlock()
	cached, ok := m.activeTails[key]
	if !ok || cached.day != days[0] || !sameCatalogAuthority(cached.authority, current) || cached.token != sourcePublicationToken(current, daySource) {
		return "", nil
	}
	for _, event := range events {
		if event.Record.Start < cached.closed {
			return "", nil
		}
	}
	if reserved := m.generationReserved[key]; reserved != nil {
		if _, busy := reserved[cached.generation]; busy {
			return "", nil
		}
	}
	token := m.generationReservationSeq.Add(1)
	if !m.reserveGenerationLockedNoLock(key, cached.generation, "active-tail", token) {
		return "", nil
	}
	// 写前撤销资格：部分写、响应丢失、校验失败或CAS失效均不能再次追加到该未知代次。
	delete(m.activeTails, key)
	return cached.generation, &generationReservation{key: key, generation: cached.generation, token: token, owner: "active-tail"}
}

// 未获确认且失去活跃资格的旧代次可能留有被旧水位遮住的物理行；后续隔离写会改变最近尝试代次，不能只检查其是否仍在PP。
// 没有更早同日债务的重试必须预算化完整取代，不能换一代差集后扩大未知旧代次的可见前缀。
func (m *Manager) uncertainPublishedTailNeedsReplacement(ctx context.Context, source SourceConfig, saved persistedSource, events []logtypes.Event) (bool, error) {
	if !saved.PublicationPending {
		return false, nil
	}
	grouped, days, err := groupEventsByUTCDay(source, events)
	if err != nil {
		return false, err
	}
	for _, day := range days {
		daySource := source
		daySource.UTCDay = day
		current, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: day})
		debt, debtErr := m.hasKnownUnpublishedDayDebt(ctx, daySource, grouped[day], current, true)
		if debtErr != nil || debt {
			return false, debtErr
		}
	}
	return len(days) > 0, nil
}

func (m *Manager) rememberActiveTailGeneration(source SourceConfig, generation string, events []logtypes.Event) {
	_, days, err := groupEventsByUTCDay(source, events)
	if err != nil || len(days) != 1 {
		return
	}
	daySource := source
	daySource.UTCDay = days[0]
	m.publishMu.Lock()
	defer m.publishMu.Unlock()
	current, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: days[0]})
	if current == nil || current.PublishedProjection == nil {
		return
	}
	found := false
	for _, gen := range publishedSourceProjectionGenerations(current.PublishedProjection, daySource) {
		if gen == generation {
			found = true
			break
		}
	}
	if !found {
		return
	}
	closed := publishedEvidenceClosed(current, daySource)
	for _, event := range events {
		if event.Record.End > closed {
			return
		}
	}
	m.mu.Lock()
	if m.activeTails == nil {
		m.activeTails = make(map[string]activeTailReceipt)
	}
	m.activeTails[source.LogSourceID+"/"+source.SourceGeneration] = activeTailReceipt{day: days[0], generation: generation, closed: closed, authority: current, token: sourcePublicationToken(current, daySource)}
	m.mu.Unlock()
}
