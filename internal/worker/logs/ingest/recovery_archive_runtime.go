package ingest

import (
	"context"
	"log/slog"

	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// 每轮只归档有限历史；仓储真实落盘回执与领域短锁复核全部成功后才摘除活动引用。
func (m *Manager) archiveCleanedRecovery(pipe *pipeline.Pipeline) {
	if m == nil || pipe == nil {
		return
	}
	limit := 16
	if m.indexCommit.MaxRows > 0 && m.indexCommit.MaxRows < limit {
		limit = m.indexCommit.MaxRows
	}
	if m.indexCommit.CycleMaxRows > 0 && m.indexCommit.CycleMaxRows < limit {
		limit = m.indexCommit.CycleMaxRows
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.persistHotBudgetOf())
	defer cancel()
	if _, err := pipe.Ledger().ArchiveCleanedRecovery(ctx, pipe.Key(), limit); err != nil {
		slog.Warn("恢复历史审计归档未完成，活动责任原样保留", "source", pipe.Key().String(), "error", err)
	}
}
