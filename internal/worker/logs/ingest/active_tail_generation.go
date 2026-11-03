package ingest

import (
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

// 未获确认且失去活跃资格的旧代次可能留有被旧水位遮住的物理行；后续隔离写会改变最近尝试代次，不能只检查其是否仍在PP。
// 没有更早同日债务的重试必须预算化完整取代，不能换一代差集后扩大未知旧代次的可见前缀。
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
