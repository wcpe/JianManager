package ingest

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// InstanceBinding is durable acquisition metadata, not CP business state.
// It preserves holder/source identity when CP replays instance registration.
type InstanceBinding struct {
	UUID       string               `json:"uuid"`
	TargetID   string               `json:"target_id"`
	Generation string               `json:"generation"`
	Mode       pipeline.AcquireMode `json:"mode"`
	WorkDir    string               `json:"work_dir"`
}

func (m *Manager) MissingInstanceBindings(instanceIDs []string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var missing []string
	for _, id := range instanceIDs {
		if _, ok := m.state.Instances[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing
}

func (m *Manager) RegisterInstance(uuid, targetID, generation, mode, workDir string) error {
	if m == nil {
		return fmt.Errorf("ingest: instance acquisition unavailable")
	}
	if uuid == "" || !strings.HasPrefix(targetID, "inst:") || generation == "" || workDir == "" {
		return fmt.Errorf("ingest: incomplete instance log binding")
	}
	binding := InstanceBinding{UUID: uuid, TargetID: targetID, Generation: generation, Mode: pipeline.AcquireMode(mode), WorkDir: filepath.Clean(workDir)}
	if !binding.Mode.Valid() {
		return fmt.Errorf("ingest: invalid instance acquisition mode")
	}
	m.cycleMu.Lock()
	defer m.cycleMu.Unlock()
	m.mu.Lock()
	existing, exists := m.state.Instances[uuid]
	m.mu.Unlock()
	if exists {
		if existing != binding {
			return fmt.Errorf("ingest: instance log binding changed without a source transition")
		}
		return nil
	}
	streams := []string{"stdout"}
	if binding.Mode == pipeline.ModeStdioPrimary {
		streams = append(streams, "stderr")
	}
	for _, stream := range streams {
		path := filepath.Join(binding.WorkDir, "logs", "latest.log")
		sourceID := binding.TargetID + "/file"
		if binding.Mode == pipeline.ModeStdioPrimary {
			path = m.rawInstancePath(binding, stream)
			sourceID = binding.TargetID + "/" + stream
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		}
		if err := m.Register(SourceConfig{LogSourceID: sourceID, SourceGeneration: generation,
			Mode: binding.Mode, Path: path, Stream: stream, SourceCategory: logtypes.SourceInstance,
			StorageNamespace: targetID, ArchiveGlob: fileArchiveGlob(binding.Mode, path)}); err != nil {
			return err
		}
	}
	m.mu.Lock()
	if m.state.Instances == nil {
		m.state.Instances = make(map[string]InstanceBinding)
	}
	m.state.Instances[uuid] = binding
	m.mu.Unlock()
	if err := m.persist(); err != nil {
		m.mu.Lock()
		delete(m.state.Instances, uuid)
		m.mu.Unlock()
		return err
	}
	return nil
}

func fileArchiveGlob(mode pipeline.AcquireMode, livePath string) string {
	if mode != pipeline.ModeFilePrimary {
		return ""
	}
	return filepath.Join(filepath.Dir(livePath), "*.gz")
}

func (m *Manager) rawInstancePath(binding InstanceBinding, stream string) string {
	identity := sha256.Sum256([]byte(binding.UUID + "\x00" + binding.Generation))
	return filepath.Join(m.root, "var", "log", "raw", fmt.Sprintf("%x", identity), stream+".log")
}

// AppendInstanceOutput persists the original bytes before the acquisition loop
// consumes them. FILE_PRIMARY stdout remains console-only to avoid double ingestion.
func (m *Manager) AppendInstanceOutput(uuid, stream string, data []byte) (writeErr error) {
	if len(data) == 0 {
		return nil
	}
	if stream != "stdout" && stream != "stderr" {
		return fmt.Errorf("ingest: unknown output stream")
	}
	m.mu.Lock()
	binding, ok := m.state.Instances[uuid]
	defer func() {
		if writeErr != nil && ok && binding.Mode == pipeline.ModeStdioPrimary {
			key := binding.TargetID + "/" + stream + "/" + binding.Generation
			if pipe := m.pipes[key]; pipe != nil {
				entry := pipe.Ledger().Get(pipe.Key())
				if entry != nil {
					_ = pipe.Ledger().RecordGap(pipe.Key(), entry.Positions.Read, entry.Positions.Read, "STDIO_RAW_WRITE_FAILED", fmt.Sprintf("%d bytes require operator verification", len(data)))
					_ = pipe.Ledger().PauseAcquire(pipe.Key(), "managed Raw write failed; manual recovery required")
				}
			}
		}
		m.mu.Unlock()
		if writeErr != nil && ok {
			_ = m.persist()
		}
	}()
	if !ok {
		return fmt.Errorf("ingest: instance output has no durable source binding")
	}
	if binding.Mode == pipeline.ModeFilePrimary {
		return nil
	}
	if m.capacityProvider != nil {
		budget, err := m.capacityProvider()
		if err != nil {
			return err
		}
		pause := budget.PauseAtPercent
		if pause <= 0 {
			pause = 90
		}
		if budget.DiskUsagePercent >= pause {
			return fmt.Errorf("ingest: managed Raw disk usage reached pause threshold")
		}
	}
	f, err := os.OpenFile(m.rawInstancePath(binding, stream), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}
