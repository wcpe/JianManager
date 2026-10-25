package ingest

import (
	"fmt"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// CanonicalDayMigrationInput 只为 Catalog 固定成员提供冻结投影内的 canonical 正文。
// 缺失、未知或超预算不会回退到 VL 物理扫描或未经持久证明的 WAL。
func (m *Manager) CanonicalDayMigrationInput(member *catalog.Record) ([]logtypes.Event, error) {
	if member == nil || member.PublishedProjection == nil || !member.PublishedProjection.CoverageComplete || member.PublishedProjection.ConflictCount != 0 {
		return nil, fmt.Errorf("ingest: canonical migration projection proof unavailable")
	}
	scopes := member.PublishedProjection.SourceProjections
	if len(scopes) == 0 {
		return nil, fmt.Errorf("ingest: canonical migration requires explicit source scopes")
	}
	m.mu.Lock()
	store := m.events
	recovering := m.startupRecovery.InProgress
	m.mu.Unlock()
	if store == nil || recovering {
		return nil, fmt.Errorf("ingest: canonical migration input is not ready")
	}
	const maxEvents = 100000
	const maxBytes = 64 << 20
	var events []logtypes.Event
	var bytes int64
	seen := make(map[string]string)
	for _, scope := range scopes {
		if scope.LogSourceID == "" || scope.SourceGeneration == "" {
			return nil, fmt.Errorf("ingest: canonical migration source identity missing")
		}
		var maxEnd uint64
		snapshot, err := store.Snapshot(scope.LogSourceID + "/" + scope.SourceGeneration)
		if err != nil {
			return nil, err
		}
		err = snapshot.Iterate(func(event logtypes.Event) error {
			if event.Source.LogSourceID != scope.LogSourceID || event.Source.SourceGeneration != scope.SourceGeneration {
				return fmt.Errorf("ingest: canonical migration source identity mismatch")
			}
			if event.Record.End > scope.ClosedVisibleSeq {
				return nil
			}
			source := SourceConfig{LogSourceID: scope.LogSourceID, SourceGeneration: scope.SourceGeneration, StorageNamespace: member.Key.StorageNamespace, UTCDay: member.Key.UTCDay}
			day, err := eventUTCDay(source, event)
			if err != nil {
				return err
			}
			if day != member.Key.UTCDay {
				return nil
			}
			event, err = normalizeCanonicalEvent(event)
			if err != nil {
				return err
			}
			if event.Record.End <= event.Record.Start {
				return fmt.Errorf("ingest: canonical migration record interval invalid")
			}
			if previous, ok := seen[event.EventID]; ok {
				if previous != event.CanonicalHash {
					return fmt.Errorf("ingest: canonical migration identity conflict")
				}
				return nil
			}
			seen[event.EventID] = event.CanonicalHash
			if event.Record.End > maxEnd {
				maxEnd = event.Record.End
			}
			bytes += int64(len(event.Message) + len(event.EventID) + len(event.CanonicalHash) + 512)
			if len(events) >= maxEvents || bytes > maxBytes {
				return fmt.Errorf("ingest: canonical migration input budget exceeded")
			}
			events = append(events, event)
			return nil
		})
		closeErr := snapshot.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if maxEnd != scope.ClosedVisibleSeq {
			return nil, fmt.Errorf("ingest: canonical migration input does not reach captured source prefix")
		}
	}
	return events, nil
}
