package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// 契约冻结的版本常量。schema/parser/engine 必须写入 manifest，禁止运行时静默更换。
const (
	SchemaVersionV1      = "archive-manifest/v1"
	DefaultParserVersion = "worker-log-parser/v1"
	// DefaultEngineVersion 由 FR-475 资产审批注入真实 VL build_id；foundation 先登记占位语义。
	DefaultEngineVersion = "victorialogs/pending-asset-approval"

	ManifestRelPath = "manifest.json"
)

// 枚举/覆盖状态（与契约 Coverage.enumeration_state 对齐）。
const (
	EnumerationOpen      = "OPEN"
	EnumerationExhausted = "EXHAUSTED"
	EnumerationStale     = "STALE"
	EnumerationCancelled = "CANCELLED"
)

// RawOrigin 标识受管 Raw 对象的来源类别。
type RawOrigin string

const (
	// OriginWorkerStdio STDIO_PRIMARY：Worker 生成的受管 Raw 分段，必须经 copy+verify+register。
	OriginWorkerStdio RawOrigin = "worker_stdio"
	// OriginInstanceGz 实例目录 .gz：登记前是 source，不是 archive；登记成功后才有受管副本。
	OriginInstanceGz RawOrigin = "instance_gz"
	// OriginWorkerGenerated 其他 Worker 生成的受管分段（非 STDIO）。
	OriginWorkerGenerated RawOrigin = "worker_generated"
)

// ObjectState 受管状态。实例 .gz 在登记前保持 SourceOnly。
type ObjectState string

const (
	ObjectStateSourceOnly ObjectState = "SOURCE_ONLY"
	ObjectStateManaged    ObjectState = "MANAGED_ARCHIVE"
)

// TaskState Rehydrate 任务状态；取值对齐 proto LogTaskState。
type TaskState string

const (
	TaskQueued    TaskState = "LOG_TASK_QUEUED"
	TaskRunning   TaskState = "LOG_TASK_RUNNING"
	TaskSucceeded TaskState = "LOG_TASK_SUCCEEDED"
	TaskFailed    TaskState = "LOG_TASK_FAILED"
	TaskCancelled TaskState = "LOG_TASK_CANCELLED"
	// TaskStale 覆盖超时/租约过期；RPC 边界映射到 proto LOG_TASK_STALE。
	TaskStale TaskState = "LOG_TASK_STALE"
)

// 结构化失败原因（可观测，禁止只返回模糊 error 字符串）。
const (
	ReasonObjectDamaged       = "OBJECT_DAMAGED"
	ReasonChecksumMismatch    = "CHECKSUM_MISMATCH"
	ReasonPathConflict        = "PATH_CONFLICT"
	ReasonDiskReserveUnmet    = "DISK_RESERVE_UNMET"
	ReasonTaskTimeout         = "TASK_TIMEOUT"
	ReasonTaskCancelled       = "TASK_CANCELLED"
	ReasonOriginalTimeMutated = "ORIGINAL_TIME_MUTATED"
	ReasonGenerationConflict  = "GENERATION_CONFLICT"
	ReasonInvalidKey          = "INVALID_PARTITION_KEY"
	ReasonInvalidManifest     = "INVALID_MANIFEST"
	ReasonProviderUnavailable = "PROVIDER_UNAVAILABLE"
	ReasonTaskNotFound        = "TASK_NOT_FOUND"
	ReasonTaskTerminal        = "TASK_TERMINAL"
	ReasonLeaseExpired        = "LEASE_EXPIRED"
)

// PartitionKey 归档分区键：storage_namespace + UTC 日 + generation。
// 禁止按实例维度拆分天然分区。
type PartitionKey struct {
	StorageNamespace string `json:"storage_namespace"`
	// UTCDay 形如 "2006-01-02"。
	UTCDay string `json:"utc_day"`
	// Generation 与 Catalog 分区 generation 对齐；不同 generation 物理路径隔离。
	Generation uint64 `json:"generation"`
}

// String 返回规范键字符串：namespace/day/g{generation}。
func (k PartitionKey) String() string {
	return fmt.Sprintf("%s/%s/g%d", k.StorageNamespace, k.UTCDay, k.Generation)
}

// Validate 校验分区键完整性。
func (k PartitionKey) Validate() error {
	if strings.TrimSpace(k.StorageNamespace) == "" {
		return fmt.Errorf("%w: storage_namespace required", ErrInvalidKey)
	}
	if strings.TrimSpace(k.UTCDay) == "" {
		return fmt.Errorf("%w: utc_day required", ErrInvalidKey)
	}
	if len(k.UTCDay) != 10 {
		return fmt.Errorf("%w: utc_day must be YYYY-MM-DD, got %q", ErrInvalidKey, k.UTCDay)
	}
	if _, err := time.Parse("2006-01-02", k.UTCDay); err != nil {
		return fmt.Errorf("%w: utc_day parse: %v", ErrInvalidKey, err)
	}
	if k.Generation == 0 {
		return fmt.Errorf("%w: generation must be > 0", ErrInvalidKey)
	}
	return nil
}

// TimeRange 源事件覆盖的 UTC 时间范围，闭开区间 [FromUTC, ToUTC)。
type TimeRange struct {
	FromUTC string `json:"from_utc,omitempty"`
	ToUTC   string `json:"to_utc,omitempty"`
}

// SourceRef manifest coverage 中登记的源分段引用。
type SourceRef struct {
	LogSourceID      string `json:"log_source_id"`
	SourceGeneration string `json:"source_generation"`
}

// Coverage 归档覆盖摘要；与契约 Coverage 字段语义对齐（foundation 子集）。
type Coverage struct {
	Complete          bool        `json:"complete"`
	PartialReasons    []string    `json:"partial_reasons,omitempty"`
	EventCount        uint64      `json:"event_count"`
	ByteCount         uint64      `json:"byte_count"`
	SourceGenerations []SourceRef `json:"source_generations,omitempty"`
	EnumerationState  string      `json:"enumeration_state,omitempty"`
}

// Checksums 内容校验和：对象 relPath → sha256 hex。
type Checksums struct {
	Objects map[string]string `json:"objects,omitempty"`
}

// RawFile 受管 Raw 文件登记项。
type RawFile struct {
	// ObjectID 内容身份（sha256 of payload）；归档重试不得改变已有对象的 object_id 语义。
	ObjectID string `json:"object_id"`
	// RelPath 分区内相对路径；物理路径 = provider(key, RelPath)。
	RelPath          string      `json:"rel_path"`
	SizeBytes        int64       `json:"size_bytes"`
	ContentSHA256    string      `json:"content_sha256"`
	Origin           RawOrigin   `json:"origin"`
	State            ObjectState `json:"state"`
	SourcePath       string      `json:"source_path,omitempty"`
	LogSourceID      string      `json:"log_source_id,omitempty"`
	SourceGeneration string      `json:"source_generation,omitempty"`
	ParserVersion    string      `json:"parser_version,omitempty"`
	RegisteredAt     time.Time   `json:"registered_at,omitempty"`
}

// Manifest 受管归档清单。字段与 FR-477 §3.1 / FR-472 契约固定项一一对应。
type Manifest struct {
	SchemaVersion    string    `json:"schema_version"`
	ParserVersion    string    `json:"parser_version"`
	EngineVersion    string    `json:"engine_version"`
	Generation       uint64    `json:"generation"`
	StorageNamespace string    `json:"storage_namespace"`
	UTCDay           string    `json:"utc_day"`
	Checksums        Checksums `json:"checksums"`
	TimeRange        TimeRange `json:"time_range"`
	Coverage         Coverage  `json:"coverage"`
	RawFiles         []RawFile `json:"raw_files"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// NewManifest 构造指定分区的空 manifest。
func NewManifest(key PartitionKey, parserVersion, engineVersion string) *Manifest {
	if parserVersion == "" {
		parserVersion = DefaultParserVersion
	}
	if engineVersion == "" {
		engineVersion = DefaultEngineVersion
	}
	now := time.Now().UTC()
	return &Manifest{
		SchemaVersion:    SchemaVersionV1,
		ParserVersion:    parserVersion,
		EngineVersion:    engineVersion,
		Generation:       key.Generation,
		StorageNamespace: key.StorageNamespace,
		UTCDay:           key.UTCDay,
		Checksums:        Checksums{Objects: map[string]string{}},
		Coverage: Coverage{
			Complete:         false,
			EnumerationState: EnumerationOpen,
		},
		RawFiles:  []RawFile{},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// Key 从 manifest 还原分区键。
func (m *Manifest) Key() PartitionKey {
	return PartitionKey{
		StorageNamespace: m.StorageNamespace,
		UTCDay:           m.UTCDay,
		Generation:       m.Generation,
	}
}

// Validate 校验 manifest 固定字段与内部一致性。
func (m *Manifest) Validate() error {
	if m == nil {
		return fmt.Errorf("%w: nil manifest", ErrInvalidManifest)
	}
	if m.SchemaVersion != SchemaVersionV1 {
		return fmt.Errorf("%w: schema_version=%q want %q", ErrInvalidManifest, m.SchemaVersion, SchemaVersionV1)
	}
	if m.ParserVersion == "" {
		return fmt.Errorf("%w: parser_version required", ErrInvalidManifest)
	}
	if m.EngineVersion == "" {
		return fmt.Errorf("%w: engine_version required", ErrInvalidManifest)
	}
	key := m.Key()
	if err := key.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if m.Generation != key.Generation {
		return fmt.Errorf("%w: generation field %d != key %d", ErrInvalidManifest, m.Generation, key.Generation)
	}
	if m.Checksums.Objects == nil {
		m.Checksums.Objects = map[string]string{}
	}
	for i, rf := range m.RawFiles {
		if rf.RelPath == "" {
			return fmt.Errorf("%w: raw_files[%d] rel_path required", ErrInvalidManifest, i)
		}
		if rf.ContentSHA256 == "" {
			return fmt.Errorf("%w: raw_files[%d] content_sha256 required", ErrInvalidManifest, i)
		}
		if rf.ObjectID == "" {
			return fmt.Errorf("%w: raw_files[%d] object_id required", ErrInvalidManifest, i)
		}
		if sum, ok := m.Checksums.Objects[rf.RelPath]; ok && sum != rf.ContentSHA256 {
			return fmt.Errorf("%w: checksum mismatch for %s", ErrInvalidManifest, rf.RelPath)
		}
	}
	return nil
}

// ChecksumFor 返回登记对象的内容校验和。
func (m *Manifest) ChecksumFor(relPath string) (string, bool) {
	if m == nil || m.Checksums.Objects == nil {
		return "", false
	}
	sum, ok := m.Checksums.Objects[relPath]
	return sum, ok
}

// FindByRelPath 按相对路径查找 raw file。
func (m *Manifest) FindByRelPath(relPath string) (RawFile, bool) {
	if m == nil {
		return RawFile{}, false
	}
	for _, rf := range m.RawFiles {
		if rf.RelPath == relPath {
			return rf, true
		}
	}
	return RawFile{}, false
}

// FindByObjectID 按 object_id 查找 raw file。
func (m *Manifest) FindByObjectID(objectID string) (RawFile, bool) {
	if m == nil {
		return RawFile{}, false
	}
	for _, rf := range m.RawFiles {
		if rf.ObjectID == objectID {
			return rf, true
		}
	}
	return RawFile{}, false
}

// HasObject 报告内容 object_id 是否已受管登记。
func (m *Manifest) HasObject(objectID string) bool {
	_, ok := m.FindByObjectID(objectID)
	return ok
}

// SortedObjectIDs 返回按字典序排序的 object_id 列表（合并键/核验用）。
func (m *Manifest) SortedObjectIDs() []string {
	if m == nil {
		return nil
	}
	ids := make([]string, 0, len(m.RawFiles))
	for _, rf := range m.RawFiles {
		ids = append(ids, rf.ObjectID)
	}
	sort.Strings(ids)
	return ids
}

// ContentHash 计算内容 sha256 hex。
func ContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ObjectIDFor 内容身份：hash(sha256, size)。与 acquire.ArchiveObjectID 语义一致但独立，
// 避免 archive 包反向依赖 acquire。
func ObjectIDFor(contentSHA256 string, size int64) string {
	payload := fmt.Sprintf("%s|%d", contentSHA256, size)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// MergeKey 计算 Rehydrate 并发合并键：同一分区 + 同一组 object_id（排序后）复用在途任务。
func MergeKey(key PartitionKey, objectIDs []string) string {
	ids := append([]string(nil), objectIDs...)
	sort.Strings(ids)
	// 去重，保证 {a,a,b} 与 {a,b} 合并键一致。
	uniq := ids[:0]
	for i, id := range ids {
		if i == 0 || id != ids[i-1] {
			uniq = append(uniq, id)
		}
	}
	return key.String() + "|" + strings.Join(uniq, ",")
}

// 本包业务错误基类。
var (
	ErrArchive             = errors.New("archive")
	ErrInvalidKey          = fmt.Errorf("%w: invalid partition key", ErrArchive)
	ErrInvalidManifest     = fmt.Errorf("%w: invalid manifest", ErrArchive)
	ErrChecksumMismatch    = fmt.Errorf("%w: checksum mismatch", ErrArchive)
	ErrPathConflict        = fmt.Errorf("%w: path conflict", ErrArchive)
	ErrProviderUnavailable = fmt.Errorf("%w: provider unavailable", ErrArchive)
	ErrTaskNotFound        = fmt.Errorf("%w: rehydrate task not found", ErrArchive)
	ErrTaskTerminal        = fmt.Errorf("%w: rehydrate task already terminal", ErrArchive)
	ErrDiskReserve         = fmt.Errorf("%w: disk reserve unmet", ErrArchive)
	ErrTimeMutated         = fmt.Errorf("%w: original _time mutated", ErrArchive)
	ErrGenerationConflict  = fmt.Errorf("%w: generation isolation violated", ErrArchive)
)

// Error 结构化归档/恢复错误：携带 reason 与 retryable 标记。
type Error struct {
	Op        string
	Key       string
	Reason    string
	Retryable bool
	Err       error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil archive error>"
	}
	base := fmt.Sprintf("archive %s", e.Op)
	if e.Key != "" {
		base += " key=" + e.Key
	}
	if e.Reason != "" {
		base += " reason=" + e.Reason
	}
	if e.Err != nil {
		base += ": " + e.Err.Error()
	}
	return base
}

func (e *Error) Unwrap() error { return e.Err }

// IsRetryable 报告该失败是否允许重试（对象损坏/上传中断可重试）。
func (e *Error) IsRetryable() bool { return e != nil && e.Retryable }

func newError(op, key, reason string, retryable bool, err error) *Error {
	return &Error{Op: op, Key: key, Reason: reason, Retryable: retryable, Err: err}
}
