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

func validateWorkDir(workDir string) error {
	cleaned := filepath.Clean(workDir)
	if cleaned == "." || cleaned == string(filepath.Separator) {
		return fmt.Errorf("ingest: refusing to collect logs from %q", workDir)
	}
	// 链接探测必须先于 EvalSymlinks：Windows 上 junction 不被 EvalSymlinks 解析——
	// 它在路径中会被原样返回（「解析后偏离原路径」判据假阴性），而含 junction 的更深
	// 路径会让 EvalSymlinks 直接报错、被误判为「尚未创建」而放行。junction 是 Windows 上
	// **无需特权**即可创建的链接形式（符号链接需 SeCreateSymbolicLinkPrivilege），
	// 打开动作同样会跟随它读到约定位置之外的文件，属同一判据面。
	if link, target, ok := firstReparseLink(cleaned); ok {
		return fmt.Errorf("ingest: refusing symlinked log work dir %q (link %q resolves to %q)", cleaned, link, target)
	}
	// 已存在的路径：解析符号链接后不得偏离原路径，否则说明中间某级是指向别处的链接。
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return nil // 尚未创建：无链接可跟随，交由后续采集按需创建。
	}
	if resolved != cleaned {
		return fmt.Errorf("ingest: refusing symlinked log work dir %q (resolves to %q)", cleaned, resolved)
	}
	return nil
}

// firstReparseLink 返回 path 中第一个链接级（Unix 符号链接 / Windows 符号链接或 junction）
// 及其目标；无链接时 ok=false。
//
// 为什么不能只用 filepath.EvalSymlinks：Windows 上 junction 的 Lstat 报 ModeIrregular
// 而非 ModeSymlink，EvalSymlinks 因此既不解析它、也无法穿过它（见 validateWorkDir 注释）。
// os.Readlink 对 Unix 符号链接与 Windows 两种链接（含 junction）都返回目标，故用它统一探测。
func firstReparseLink(path string) (link, target string, ok bool) {
	volume := filepath.VolumeName(path)
	rest := path[len(volume):]
	current := volume
	if strings.HasPrefix(rest, string(filepath.Separator)) {
		current += string(filepath.Separator)
		rest = rest[len(string(filepath.Separator)):]
	}
	for _, part := range strings.Split(rest, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		// 非链接（普通文件/目录）与不存在的级：os.Readlink 均返回错误。
		if t, err := os.Readlink(current); err == nil {
			return current, t, true
		}
	}
	return "", "", false
}

func (m *Manager) RegisterInstance(uuid, targetID, generation, mode, workDir string) error {
	if m == nil {
		return fmt.Errorf("ingest: instance acquisition unavailable")
	}
	if uuid == "" || !strings.HasPrefix(targetID, "inst:") || generation == "" || workDir == "" {
		return fmt.Errorf("ingest: incomplete instance log binding")
	}
	if err := validateWorkDir(workDir); err != nil {
		return err
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

// Pending 暂存上限（M-6）。未绑定实例的输出在绑定到达前无 pipeline/ledger 兜底，
// 若不加界，持续 stdout/stderr 可耗尽 Worker 数据盘，进而拖垮 ingest.state.json、
// 事件段、WAL 与 VL 运行时。上限取值需容纳正常的重启接管窗口（CP 不可达时可能持续
// 数分钟），同时远低于单盘容量。
const (
	// pendingMaxStreamBytes 单实例单流上限。
	pendingMaxStreamBytes = 64 << 20
	// pendingMaxTotalBytes 全部 pending 暂存总量上限。
	pendingMaxTotalBytes = 512 << 20
)

// pendingSpoolUsage 统计 pending 暂存目录的总字节数与每个文件的大小。
//
// 不缓存结果：spool 会在绑定时被 rename 或删除（见 flushPendingForBinding），
// 缓存会让限额在接管后残留并误伤正常采集。
func (m *Manager) pendingSpoolUsage() (total int64, perFile map[string]int64, err error) {
	root := filepath.Join(m.root, "var", "log", pendingSpoolRoot)
	perFile = map[string]int64{}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return 0, perFile, nil
	}
	if err != nil {
		return 0, nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		files, readErr := os.ReadDir(filepath.Join(root, entry.Name()))
		if readErr != nil {
			return 0, nil, readErr
		}
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".spool") {
				continue
			}
			info, statErr := file.Info()
			if statErr != nil {
				return 0, nil, statErr
			}
			size := info.Size()
			perFile[entry.Name()+"/"+file.Name()] = size
			total += size
		}
	}
	return total, perFile, nil
}

func (m *Manager) appendPendingInstanceOutputLocked(uuid, stream string, data []byte) error {
	// 与托管 Raw 写入同源的门禁：pending 也必须受磁盘阈值约束，否则它成为绕过
	// 暂停阈值的写放大路径（M-6）。
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
			return fmt.Errorf("ingest: pending spool disk usage reached pause threshold")
		}
	}
	path := m.pendingInstancePath(uuid, stream)
	rel := filepath.Base(filepath.Dir(path)) + "/" + filepath.Base(path)
	maxStream := m.pendingMaxStream
	if maxStream <= 0 {
		maxStream = pendingMaxStreamBytes
	}
	maxTotal := m.pendingMaxTotal
	if maxTotal <= 0 {
		maxTotal = pendingMaxTotalBytes
	}
	total, perFile, err := m.pendingSpoolUsage()
	if err != nil {
		return fmt.Errorf("ingest: pending spool usage: %w", err)
	}
	if perFile[rel]+int64(len(data)) > maxStream {
		return fmt.Errorf("ingest: pending spool exceeds per-stream limit for %s/%s", uuid, stream)
	}
	if total+int64(len(data)) > maxTotal {
		return fmt.Errorf("ingest: pending spool exceeds total limit")
	}
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
