package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Registry 受管 Raw Archive：复制 + 校验 + 登记。
//
// 约束：
//   - STDIO_PRIMARY Worker 生成分段必须走 RegisterRaw；
//   - 实例 .gz 在登记前是 source，不是 archive；
//   - 同内容幂等登记；同 rel_path 不同内容禁止盲覆盖；
//   - 不同 generation 使用不同 PartitionKey 物理路径，互不覆盖；
//   - 对象损坏（校验失败）返回可重试错误，不写入 manifest。
type Registry struct {
	mu       sync.Mutex
	provider Provider
	manifest map[PartitionKey]*Manifest
	// engineVersion 写入新 manifest 的引擎标识（如受管 VL build_id）；空则用 DefaultEngineVersion。
	engineVersion string
	// now 可注入时钟。
	now func() time.Time
}

// NewRegistry 创建绑定 Provider 的受管归档登记处。
func NewRegistry(p Provider) *Registry {
	return &Registry{
		provider: p,
		manifest: make(map[PartitionKey]*Manifest),
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// SetEngineVersion 设置写入新 manifest 的引擎标识（FR-476 已审批的 VL build_id）。
// 空值保持 DefaultEngineVersion 占位；已存在的 manifest 不受影响。
func (r *Registry) SetEngineVersion(v string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.engineVersion = v
}

// Provider 返回底层 Provider。
func (r *Registry) Provider() Provider { return r.provider }

// RawSource 描述待登记的原始分段。
type RawSource struct {
	// SourcePath 实例/Worker 侧源文件路径；与 Data 二选一（Data 优先，便于单测）。
	SourcePath string
	// Data 内存载荷；非 nil 时不读 SourcePath。
	Data []byte
	// Origin 来源类别；空时默认 worker_generated。
	Origin RawOrigin
	// RelPath 分区内目标相对路径；空时按内容哈希派生 raw/objects/<sha256>。
	RelPath string
	// LogSourceID / SourceGeneration / ParserVersion 源身份，写入 manifest。
	LogSourceID      string
	SourceGeneration string
	ParserVersion    string
	// 可选时间范围，合并进 manifest.Coverage/TimeRange。
	EventTimeFromUTC string
	EventTimeToUTC   string
	EventCount       uint64
}

// RegisterResult 登记结果。
type RegisterResult struct {
	Manifest *Manifest
	Object   RawFile
	// AlreadyRegistered 为 true 表示幂等命中（同 object_id 已受管），未重复复制。
	AlreadyRegistered bool
	CopiedBytes       int64
}

// RegisterRaw 执行 copy + verify + register。
//
// 返回的 RawFile.State 恒为 ObjectStateManaged：只有登记成功后对象才是受管恢复来源。
func (r *Registry) RegisterRaw(ctx context.Context, key PartitionKey, src RawSource) (*RegisterResult, error) {
	if err := key.Validate(); err != nil {
		return nil, newError("RegisterRaw", key.String(), ReasonInvalidKey, false, err)
	}
	if src.Origin == "" {
		src.Origin = OriginWorkerGenerated
	}
	if src.ParserVersion == "" {
		src.ParserVersion = DefaultParserVersion
	}

	data, err := r.loadSource(src)
	if err != nil {
		return nil, newError("RegisterRaw", key.String(), ReasonObjectDamaged, true, err)
	}
	contentHash := ContentHash(data)
	objectID := ObjectIDFor(contentHash, int64(len(data)))
	relPath := src.RelPath
	if relPath == "" {
		relPath = "raw/objects/" + contentHash
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	m, err := r.ensureManifestLocked(ctx, key)
	if err != nil {
		return nil, err
	}

	// 幂等：同 object_id 已受管 → 直接返回，不重复写对象。
	if existing, ok := m.FindByObjectID(objectID); ok && existing.State == ObjectStateManaged {
		if sum, okSum := m.ChecksumFor(existing.RelPath); okSum && sum != contentHash {
			return nil, newError("RegisterRaw", key.String(), ReasonChecksumMismatch, true,
				fmt.Errorf("%w: object %s checksum drift", ErrChecksumMismatch, objectID))
		}
		stored, getErr := r.provider.Get(ctx, key, existing.RelPath)
		if getErr != nil || ContentHash(stored) != existing.ContentSHA256 {
			return nil, newError("RegisterRaw", key.String(), ReasonObjectDamaged, true,
				fmt.Errorf("%w: managed object %s no longer matches its manifest", ErrChecksumMismatch, objectID))
		}
		return &RegisterResult{
			Manifest:          cloneManifest(m),
			Object:            existing,
			AlreadyRegistered: true,
		}, nil
	}

	// 同路径冲突：已登记不同内容 → 禁止盲覆盖。
	if existing, ok := m.FindByRelPath(relPath); ok {
		if existing.ContentSHA256 != contentHash || existing.ObjectID != objectID {
			return nil, newError("RegisterRaw", key.String(), ReasonPathConflict, false,
				fmt.Errorf("%w: rel_path %s already holds object %s, refusing overwrite with %s",
					ErrPathConflict, relPath, existing.ObjectID, objectID))
		}
		// 同内容同路径：幂等。
		return &RegisterResult{
			Manifest:          cloneManifest(m),
			Object:            existing,
			AlreadyRegistered: true,
		}, nil
	}
	if m.Coverage.Complete {
		return nil, newError("RegisterRaw", key.String(), ReasonGenerationConflict, false,
			fmt.Errorf("sealed archive generation requires a new generation for additional objects"))
	}

	// 对象若已物理存在且内容不同 → 同样拒绝盲覆盖（可能来自未完成登记的残留）。
	if exists, _ := r.provider.Exists(ctx, key, relPath); exists {
		got, gerr := r.provider.Get(ctx, key, relPath)
		if gerr != nil || ContentHash(got) != contentHash {
			// 损坏/不一致：允许删除后重试写入（可重试），但不得静默覆盖登记过的 manifest 路径。
			if _, reg := m.FindByRelPath(relPath); reg {
				return nil, newError("RegisterRaw", key.String(), ReasonPathConflict, false,
					fmt.Errorf("%w: managed path %s content mismatch", ErrPathConflict, relPath))
			}
			// 未登记的残留损坏对象：清理后继续，标记为可重试场景已被处理。
			_ = r.provider.Delete(ctx, key, relPath)
		} else if ContentHash(got) == contentHash {
			// 物理对象已存在且内容一致：走 verify+登记，不视为冲突。
		}
	}

	// copy
	if err := r.provider.Put(ctx, key, relPath, data); err != nil {
		return nil, newError("RegisterRaw", key.String(), ReasonProviderUnavailable, true, err)
	}

	// verify read-back
	got, err := r.provider.Get(ctx, key, relPath)
	if err != nil {
		_ = r.provider.Delete(ctx, key, relPath)
		return nil, newError("RegisterRaw", key.String(), ReasonObjectDamaged, true, err)
	}
	if ContentHash(got) != contentHash {
		_ = r.provider.Delete(ctx, key, relPath)
		return nil, newError("RegisterRaw", key.String(), ReasonChecksumMismatch, true,
			fmt.Errorf("%w: wrote %s read-back %s", ErrChecksumMismatch, contentHash, ContentHash(got)))
	}

	rf := RawFile{
		ObjectID:         objectID,
		RelPath:          relPath,
		SizeBytes:        int64(len(data)),
		ContentSHA256:    contentHash,
		Origin:           src.Origin,
		State:            ObjectStateManaged,
		SourcePath:       src.SourcePath,
		LogSourceID:      src.LogSourceID,
		SourceGeneration: src.SourceGeneration,
		ParserVersion:    src.ParserVersion,
		RegisteredAt:     r.now(),
	}

	m.RawFiles = append(m.RawFiles, rf)
	if m.Checksums.Objects == nil {
		m.Checksums.Objects = map[string]string{}
	}
	m.Checksums.Objects[relPath] = contentHash
	r.mergeCoverageLocked(m, src)
	m.UpdatedAt = r.now()

	if err := m.Validate(); err != nil {
		// 回滚内存登记，对象保留在 provider 供重试（内容已校验一致）。
		m.RawFiles = m.RawFiles[:len(m.RawFiles)-1]
		delete(m.Checksums.Objects, relPath)
		return nil, newError("RegisterRaw", key.String(), ReasonInvalidManifest, true, err)
	}

	if err := r.saveManifestLocked(ctx, m); err != nil {
		m.RawFiles = m.RawFiles[:len(m.RawFiles)-1]
		delete(m.Checksums.Objects, relPath)
		return nil, newError("RegisterRaw", key.String(), ReasonProviderUnavailable, true, err)
	}

	return &RegisterResult{
		Manifest:    cloneManifest(m),
		Object:      rf,
		CopiedBytes: int64(len(data)),
	}, nil
}

// Seal verifies every managed object and persists the closed coverage in one
// manifest update. A sealed generation cannot admit additional raw objects.
func (r *Registry) Seal(ctx context.Context, key PartitionKey) (*Manifest, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	m, err := r.ensureManifestLocked(ctx, key)
	if err != nil {
		return nil, err
	}
	if len(m.RawFiles) == 0 || m.Coverage.EventCount == 0 {
		return nil, fmt.Errorf("archive: cannot seal empty recovery source")
	}
	for _, raw := range m.RawFiles {
		if raw.State != ObjectStateManaged {
			return nil, fmt.Errorf("archive: unmanaged object %s", raw.ObjectID)
		}
		data, err := r.provider.Get(ctx, key, raw.RelPath)
		if err != nil || ContentHash(data) != raw.ContentSHA256 {
			return nil, fmt.Errorf("archive: object %s verification failed", raw.ObjectID)
		}
	}
	sealed := cloneManifest(m)
	sealed.Coverage.Complete = true
	sealed.Coverage.EnumerationState = EnumerationExhausted
	sealed.UpdatedAt = r.now()
	if err := r.saveManifestLocked(ctx, sealed); err != nil {
		return nil, err
	}
	r.manifest[key] = sealed
	return cloneManifest(sealed), nil
}

// GetManifest 返回分区 manifest 副本；不存在时返回错误。
func (r *Registry) GetManifest(key PartitionKey) (*Manifest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.manifest[key]
	if !ok {
		// 尝试从 provider 加载（进程重启后恢复）。
		loaded, err := r.loadManifestFromProvider(context.Background(), key)
		if err != nil {
			return nil, newError("GetManifest", key.String(), ReasonInvalidManifest, false, err)
		}
		if loaded == nil {
			return nil, newError("GetManifest", key.String(), ReasonInvalidManifest, false,
				fmt.Errorf("%w: manifest not found for %s", ErrInvalidManifest, key))
		}
		r.manifest[key] = loaded
		return cloneManifest(loaded), nil
	}
	return cloneManifest(m), nil
}

// EnsureManifest 确保分区 manifest 存在（不存在则创建并持久化）。
func (r *Registry) EnsureManifest(ctx context.Context, key PartitionKey, parserVersion, engineVersion string) (*Manifest, error) {
	if err := key.Validate(); err != nil {
		return nil, newError("EnsureManifest", key.String(), ReasonInvalidKey, false, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := r.manifest[key]; ok {
		return cloneManifest(m), nil
	}
	m := NewManifest(key, parserVersion, engineVersion)
	if err := r.saveManifestLocked(ctx, m); err != nil {
		return nil, err
	}
	r.manifest[key] = m
	return cloneManifest(m), nil
}

// IsManaged 报告 object_id 是否已是该分区的受管归档对象。
func (r *Registry) IsManaged(key PartitionKey, objectID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.manifest[key]
	if !ok {
		return false
	}
	rf, found := m.FindByObjectID(objectID)
	return found && rf.State == ObjectStateManaged
}

// ObjectStateOf 返回对象在指定分区的受管状态。
func (r *Registry) ObjectStateOf(key PartitionKey, objectID string) ObjectState {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.manifest[key]
	if !ok {
		return ObjectStateSourceOnly
	}
	if rf, found := m.FindByObjectID(objectID); found && rf.State == ObjectStateManaged {
		return ObjectStateManaged
	}
	return ObjectStateSourceOnly
}

// PhysicalPath 返回 Provider 视角下对象的可读位置标识（Local 为文件路径；对象存储为 key）。
func (r *Registry) PhysicalPath(key PartitionKey, relPath string) string {
	if la, ok := r.provider.(*LocalArchive); ok {
		p, err := la.objectPath(key, relPath)
		if err != nil {
			return ""
		}
		return p
	}
	if store, ok := r.provider.(*ObjectStore); ok {
		k, err := store.objectKey(key, relPath)
		if err != nil {
			return ""
		}
		return k
	}
	return key.String() + "/" + relPath
}

// Keys 返回内存中已知的分区键。
func (r *Registry) Keys() []PartitionKey {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]PartitionKey, 0, len(r.manifest))
	for k := range r.manifest {
		out = append(out, k)
	}
	return out
}

func (r *Registry) loadSource(src RawSource) ([]byte, error) {
	if src.Data != nil {
		return src.Data, nil
	}
	if src.SourcePath == "" {
		return nil, fmt.Errorf("%w: RawSource requires Data or SourcePath", ErrArchive)
	}
	data, err := os.ReadFile(src.SourcePath)
	if err != nil {
		return nil, fmt.Errorf("read source %s: %w", src.SourcePath, err)
	}
	return data, nil
}

func (r *Registry) ensureManifestLocked(ctx context.Context, key PartitionKey) (*Manifest, error) {
	if m, ok := r.manifest[key]; ok {
		return m, nil
	}
	loaded, err := r.loadManifestFromProvider(ctx, key)
	if err != nil {
		return nil, newError("ensureManifest", key.String(), ReasonInvalidManifest, true, err)
	}
	if loaded == nil {
		// 仅驻留内存；首个成功登记才持久化，避免损坏上传留下空 manifest 伪影。
		engine := r.engineVersion
		if engine == "" {
			engine = DefaultEngineVersion
		}
		loaded = NewManifest(key, DefaultParserVersion, engine)
	}
	r.manifest[key] = loaded
	return loaded, nil
}

func (r *Registry) loadManifestFromProvider(ctx context.Context, key PartitionKey) (*Manifest, error) {
	exists, err := r.provider.Exists(ctx, key, ManifestRelPath)
	if err != nil {
		return nil, fmt.Errorf("archive: check manifest: %w", err)
	}
	if !exists {
		return nil, nil
	}
	data, err := r.provider.Get(ctx, key, ManifestRelPath)
	if err != nil {
		return nil, fmt.Errorf("archive: read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%w: unmarshal manifest: %v", ErrInvalidManifest, err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if m.Key() != key {
		return nil, fmt.Errorf("%w: manifest key %s != requested %s", ErrInvalidManifest, m.Key(), key)
	}
	return &m, nil
}

func (r *Registry) saveManifestLocked(ctx context.Context, m *Manifest) error {
	data, err := MarshalManifest(m)
	if err != nil {
		return newError("saveManifest", m.Key().String(), ReasonInvalidManifest, false, err)
	}
	if err := r.provider.Put(ctx, m.Key(), ManifestRelPath, data); err != nil {
		return newError("saveManifest", m.Key().String(), ReasonProviderUnavailable, true, err)
	}
	return nil
}

func (r *Registry) mergeCoverageLocked(m *Manifest, src RawSource) {
	// Source generations 去重追加。
	if src.LogSourceID != "" || src.SourceGeneration != "" {
		ref := SourceRef{LogSourceID: src.LogSourceID, SourceGeneration: src.SourceGeneration}
		found := false
		for _, existing := range m.Coverage.SourceGenerations {
			if existing == ref {
				found = true
				break
			}
		}
		if !found {
			m.Coverage.SourceGenerations = append(m.Coverage.SourceGenerations, ref)
		}
	}
	m.Coverage.EventCount += src.EventCount
	for _, rf := range m.RawFiles {
		_ = rf
	}
	// ByteCount 以已登记对象累计。
	var bytes uint64
	for _, rf := range m.RawFiles {
		if rf.SizeBytes > 0 {
			bytes += uint64(rf.SizeBytes)
		}
	}
	m.Coverage.ByteCount = bytes
	if src.EventTimeFromUTC != "" {
		if m.TimeRange.FromUTC == "" || src.EventTimeFromUTC < m.TimeRange.FromUTC {
			m.TimeRange.FromUTC = src.EventTimeFromUTC
		}
	}
	if src.EventTimeToUTC != "" {
		if m.TimeRange.ToUTC == "" || src.EventTimeToUTC > m.TimeRange.ToUTC {
			m.TimeRange.ToUTC = src.EventTimeToUTC
		}
	}
}

// MarshalManifest 序列化 manifest（roundtrip 测试与 provider 持久化共用）。
func MarshalManifest(m *Manifest) ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("%w: nil manifest", ErrInvalidManifest)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// UnmarshalManifest 反序列化并校验 manifest。
func UnmarshalManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

func cloneManifest(m *Manifest) *Manifest {
	if m == nil {
		return nil
	}
	c := *m
	if m.Checksums.Objects != nil {
		c.Checksums.Objects = make(map[string]string, len(m.Checksums.Objects))
		for k, v := range m.Checksums.Objects {
			c.Checksums.Objects[k] = v
		}
	}
	if m.RawFiles != nil {
		c.RawFiles = append([]RawFile(nil), m.RawFiles...)
	}
	if m.Coverage.PartialReasons != nil {
		c.Coverage.PartialReasons = append([]string(nil), m.Coverage.PartialReasons...)
	}
	if m.Coverage.SourceGenerations != nil {
		c.Coverage.SourceGenerations = append([]SourceRef(nil), m.Coverage.SourceGenerations...)
	}
	return &c
}

// SourceFilePathExists 辅助：判断实例侧源文件是否存在（.gz 在登记前只是 source）。
func SourceFilePathExists(path string) bool {
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// DeriveSourceRelPath 保留 helper：按源路径派生建议 rel_path（不强制使用）。
func DeriveSourceRelPath(srcPath, contentSHA256 string) string {
	base := filepath.Base(srcPath)
	if base == "" || base == "." || base == "/" {
		base = "segment.bin"
	}
	if contentSHA256 == "" {
		return "raw/sources/" + base
	}
	return "raw/sources/" + contentSHA256[:16] + "-" + base
}
