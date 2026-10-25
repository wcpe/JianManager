package ingest

import (
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

func (m *Manager) importArchivesWithBudget(pipe *pipeline.Pipeline, paths []string) ([]logtypes.Event, bool, error) {
	if len(paths) > 0 && m.resourceBudget != nil {
		if err := m.resourceBudget.Admit(vlsup.WorkBackground); err != nil {
			return nil, false, err
		}
	}
	return importArchives(pipe, paths)
}
