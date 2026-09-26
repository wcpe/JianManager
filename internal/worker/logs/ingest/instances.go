package ingest

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
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

// ErrInstanceBindingPending 表示输出已安全写入暂存区，但实例日志绑定尚未到达。
var ErrInstanceBindingPending = errors.New("ingest: instance log binding pending")

type pendingFlushError struct {
	stream string
	err    error
}

func (e *pendingFlushError) Error() string { return fmt.Sprintf("%s: %v", e.stream, e.err) }
func (e *pendingFlushError) Unwrap() error { return e.err }

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
	if exists && existing != binding {
		return fmt.Errorf("ingest: instance log binding changed without a source transition")
	}

	// 暂存接管与绑定变更必须共用同一把锁，保证注册期间的输出不会插入回放中间。
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	if exists {
		if err := m.flushPendingForBinding(binding); err != nil {
			m.recordRawWriteFailure(binding, pendingFlushStream(err), 0)
			return fmt.Errorf("ingest: adopt pending instance output: %w", err)
		}
		if binding.Mode == pipeline.ModeStdioPrimary {
			for _, stream := range []string{"stdout", "stderr"} {
				if err := m.ensureRawFile(m.rawInstancePath(binding, stream)); err != nil {
					m.recordRawWriteFailure(binding, stream, 0)
					return err
				}
			}
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
	if err := m.flushPendingForBinding(binding); err != nil {
		m.recordRawWriteFailure(binding, pendingFlushStream(err), 0)
		return fmt.Errorf("ingest: adopt pending instance output: %w", err)
	}
	if binding.Mode == pipeline.ModeStdioPrimary {
		for _, stream := range []string{"stdout", "stderr"} {
			if err := m.ensureRawFile(m.rawInstancePath(binding, stream)); err != nil {
				m.recordRawWriteFailure(binding, stream, 0)
				return err
			}
		}
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

func (m *Manager) pendingInstancePath(uuid, stream string) string {
	identity := sha256.Sum256([]byte(uuid))
	return filepath.Join(m.root, "var", "log", pendingSpoolRoot, fmt.Sprintf("%x", identity), stream+".spool")
}

func (m *Manager) ensureRawFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

func (m *Manager) appendPendingInstanceOutput(uuid, stream string, data []byte) error {
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	return m.appendPendingInstanceOutputLocked(uuid, stream, data)
}

func (m *Manager) appendPendingInstanceOutputLocked(uuid, stream string, data []byte) error {
	path := m.pendingInstancePath(uuid, stream)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("ingest: pending spool directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("ingest: pending spool open: %w", err)
	}
	n, writeErr := f.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if closeErr := f.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return fmt.Errorf("ingest: pending spool write: %w", writeErr)
	}
	return nil
}

func (m *Manager) flushPendingForBinding(binding InstanceBinding) error {
	for _, stream := range []string{"stdout", "stderr"} {
		fail := func(err error) error { return &pendingFlushError{stream: stream, err: err} }
		spoolPath := m.pendingInstancePath(binding.UUID, stream)
		if _, err := os.Stat(spoolPath); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return fail(fmt.Errorf("ingest: pending spool stat: %w", err))
		}
		rawPath := m.rawInstancePath(binding, stream)
		if _, err := os.Stat(rawPath); os.IsNotExist(err) {
			if err := os.MkdirAll(filepath.Dir(rawPath), 0o700); err != nil {
				return fail(err)
			}
			if err := os.Rename(spoolPath, rawPath); err != nil {
				return fail(fmt.Errorf("ingest: adopt pending spool: %w", err))
			}
			continue
		} else if err != nil {
			return fail(fmt.Errorf("ingest: raw stat: %w", err))
		}
		spool, err := os.Open(spoolPath)
		if err != nil {
			return fail(fmt.Errorf("ingest: pending spool open: %w", err))
		}
		raw, err := os.OpenFile(rawPath, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			_ = spool.Close()
			return fail(fmt.Errorf("ingest: raw open: %w", err))
		}
		_, copyErr := io.Copy(raw, spool)
		if copyErr == nil {
			copyErr = raw.Sync()
		}
		if closeErr := raw.Close(); copyErr == nil {
			copyErr = closeErr
		}
		if closeErr := spool.Close(); copyErr == nil {
			copyErr = closeErr
		}
		if copyErr != nil {
			return fail(fmt.Errorf("ingest: pending spool flush: %w", copyErr))
		}
		if err := os.Remove(spoolPath); err != nil {
			return fail(fmt.Errorf("ingest: pending spool cleanup: %w", err))
		}
	}
	return nil
}

func pendingFlushStream(err error) string {
	var flushErr *pendingFlushError
	if errors.As(err, &flushErr) && flushErr.stream != "" {
		return flushErr.stream
	}
	return "stdout"
}

func (m *Manager) recordRawWriteFailure(binding InstanceBinding, stream string, size int) {
	if binding.Mode != pipeline.ModeStdioPrimary {
		return
	}
	key := binding.TargetID + "/" + stream + "/" + binding.Generation
	m.mu.Lock()
	pipe := m.pipes[key]
	m.mu.Unlock()
	if pipe == nil {
		return
	}
	entry := pipe.Ledger().Get(pipe.Key())
	if entry == nil {
		return
	}
	_ = pipe.Ledger().RecordGap(pipe.Key(), entry.Positions.Read, entry.Positions.Read, "STDIO_RAW_WRITE_FAILED", fmt.Sprintf("%d bytes require operator verification", size))
	_ = pipe.Ledger().PauseAcquire(pipe.Key(), "managed Raw write failed; manual recovery required")
	_ = m.persist()
}

func (m *Manager) appendRawInstanceOutputLocked(binding InstanceBinding, stream string, data []byte) error {
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
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// AppendInstanceOutput 先持久化原始字节，再交给常驻采集循环消费。
// FILE_PRIMARY 的 stdout/stderr 仍只用于控制台，避免重复采集。
func (m *Manager) AppendInstanceOutput(uuid, stream string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if stream != "stdout" && stream != "stderr" {
		return fmt.Errorf("ingest: unknown output stream")
	}
	m.mu.Lock()
	binding, ok := m.state.Instances[uuid]
	m.mu.Unlock()

	m.pendingMu.Lock()
	if !ok {
		// 注册可能在首次检查后完成，必须在暂存锁内重新读取绑定，避免把新输出留在
		// 已经完成回放的 spool 中。
		m.mu.Lock()
		binding, ok = m.state.Instances[uuid]
		m.mu.Unlock()
	}
	if !ok {
		writeErr := m.appendPendingInstanceOutputLocked(uuid, stream, data)
		m.pendingMu.Unlock()
		if writeErr != nil {
			return writeErr
		}
		return fmt.Errorf("%w: instance %s", ErrInstanceBindingPending, uuid)
	}
	if binding.Mode == pipeline.ModeFilePrimary {
		m.pendingMu.Unlock()
		return nil
	}
	writeErr := m.appendRawInstanceOutputLocked(binding, stream, data)
	m.pendingMu.Unlock()
	if writeErr != nil {
		m.recordRawWriteFailure(binding, stream, len(data))
	}
	return writeErr
}
