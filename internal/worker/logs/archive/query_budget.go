package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

const (
	archiveManifestBytes         int64 = 4 << 20
	archiveManifestStagingBytes  int64 = 8 * archiveManifestBytes
	archiveMaxRawBytes           int64 = 64 << 20
	archiveMaxLineBytes                = 1 << 20
	archiveReadBufferBytes             = 32 << 10
	archiveMaxPublishedSummaries       = 128
	holdKindArchiveConsumer            = "archive_consumer"
)

// RehydrateResourceBudget 由装配层绑定真实后台准入与 staging 预留，避免依赖预算实现包。
// Admit 必须绑定 WorkBackground，ReserveStaging 必须返回本次预留的幂等释放函数。
type RehydrateResourceBudget struct {
	Admit          func(context.Context) error
	ReserveStaging func(context.Context, int64) (func(), error)
}

// RehydrateReuseChecker 只复用仍有当前发布凭据的成功任务，不把历史成功冒充当前就绪。
type RehydrateReuseChecker interface {
	CanReuseRehydrate(context.Context, PartitionKey, string) bool
}

// SetResourceBudget 保留旧构造签名，预算回调在每次请求开始时冻结。
func (b *QueryBackend) SetResourceBudget(budget RehydrateResourceBudget) {
	b.budgetMu.Lock()
	b.budget = budget
	b.budgetMu.Unlock()
}

func (b *QueryBackend) resourceBudget() RehydrateResourceBudget {
	b.budgetMu.RLock()
	defer b.budgetMu.RUnlock()
	return b.budget
}

func (b RehydrateResourceBudget) admit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.Admit == nil && b.ReserveStaging == nil {
		return nil
	}
	if b.Admit == nil || b.ReserveStaging == nil {
		return fmt.Errorf("archive: incomplete resource budget callbacks")
	}
	return b.Admit(ctx)
}

func (b RehydrateResourceBudget) reserve(ctx context.Context, n int64) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b.Admit == nil && b.ReserveStaging == nil {
		return func() {}, nil
	}
	if b.ReserveStaging == nil {
		return nil, fmt.Errorf("archive: staging reservation unavailable")
	}
	release, err := b.ReserveStaging(ctx, n)
	if err != nil {
		if release != nil {
			release()
		}
		return nil, err
	}
	if release == nil {
		return nil, fmt.Errorf("archive: staging reservation has no release handle")
	}
	var once sync.Once
	return func() { once.Do(release) }, nil
}

// archiveObjectOpener 是可选的有界读取接缝；缺失时拒绝，绝不回退到全量 Get。
type archiveObjectOpener interface {
	OpenArchiveObject(context.Context, PartitionKey, string) (io.ReadCloser, error)
}

// OpenArchiveObject 仅打开文件，调用方先准入并预留，再按清单与硬界消费正文。
func (l *LocalArchive) OpenArchiveObject(ctx context.Context, key PartitionKey, relPath string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := l.objectPath(key, relPath)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

// OpenArchiveObject 借用不可变内存对象；Put 替换副本而不修改已打开对象。
func (o *ObjectStore) OpenArchiveObject(ctx context.Context, key PartitionKey, relPath string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	objectKey, err := o.objectKey(key, relPath)
	if err != nil {
		return nil, err
	}
	o.mu.RLock()
	data, ok := o.mem[objectKey]
	o.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: archive object missing", ErrProviderUnavailable)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// OpenArchiveObject 复用既有带取消的 S3 下载流，不在 Provider 内 ReadAll。
func (p *RemoteS3Provider) OpenArchiveObject(ctx context.Context, key PartitionKey, relPath string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	objectKey, err := p.objectKey(key, relPath)
	if err != nil {
		return nil, err
	}
	return p.store.Download(ctx, objectKey)
}

func openArchiveObject(ctx context.Context, provider Provider, key PartitionKey, relPath string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	opener, ok := provider.(archiveObjectOpener)
	if !ok {
		return nil, fmt.Errorf("archive: provider has no bounded object reader")
	}
	reader, err := opener.OpenArchiveObject(ctx, key, relPath)
	if err != nil {
		if reader != nil {
			_ = reader.Close()
		}
		return nil, err
	}
	if reader == nil {
		return nil, fmt.Errorf("archive: bounded reader is unavailable")
	}
	return reader, nil
}

// queryManifest 只让查询恢复路径使用有界元数据读取，不改变 Registry 的登记/持久化协议。
func (b *QueryBackend) queryManifest(ctx context.Context, key PartitionKey) (*Manifest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r := b.Registry
	r.mu.Lock()
	cached := r.manifest[key]
	if cached != nil {
		if err := boundedManifestMetadata(cached); err != nil {
			r.mu.Unlock()
			return nil, err
		}
		out := cloneManifest(cached)
		r.mu.Unlock()
		return out, nil
	}
	r.mu.Unlock()
	reader, err := openArchiveObject(ctx, r.provider, key, ManifestRelPath)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(&archiveContextReader{ctx: ctx, reader: reader}, archiveManifestBytes+1))
	closeErr := reader.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(data)) > archiveManifestBytes {
		return nil, fmt.Errorf("archive: manifest exceeds bounded metadata limit")
	}
	// 先按字段的数组/映射数量扫描，避免4MiB的“[{},{}...]”在类型解码时放大成巨量结构体。
	if err := preflightManifestJSON(json.NewDecoder(bytes.NewReader(data)), "root", 0); err != nil {
		return nil, err
	}
	manifest, err := UnmarshalManifest(data)
	if err != nil {
		return nil, err
	}
	if manifest.Key() != key {
		return nil, fmt.Errorf("archive: invalid bounded manifest identity")
	}
	if err := boundedManifestMetadata(manifest); err != nil {
		return nil, err
	}
	r.mu.Lock()
	if cached = r.manifest[key]; cached != nil {
		if err := boundedManifestMetadata(cached); err != nil {
			r.mu.Unlock()
			return nil, err
		}
		manifest = cloneManifest(cached)
	} else {
		r.manifest[key] = cloneManifest(manifest)
	}
	r.mu.Unlock()
	return manifest, nil
}

func boundedManifestMetadata(manifest *Manifest) error {
	if len(manifest.RawFiles) > archiveMaxObjects || len(manifest.Checksums.Objects) > archiveMaxObjects || len(manifest.Coverage.SourceGenerations) > int(archiveMaxEvents) || len(manifest.Coverage.PartialReasons) > archiveMaxObjects {
		return fmt.Errorf("archive: excessive manifest metadata cardinality")
	}
	remaining := archiveManifestBytes
	consume := func(n int) bool {
		if int64(n) > remaining {
			return false
		}
		remaining -= int64(n)
		return true
	}
	for _, value := range []string{manifest.SchemaVersion, manifest.ParserVersion, manifest.EngineVersion, manifest.StorageNamespace, manifest.UTCDay, manifest.TimeRange.FromUTC, manifest.TimeRange.ToUTC} {
		if !consume(len(value)) {
			return fmt.Errorf("archive: excessive manifest metadata bytes")
		}
	}
	for _, raw := range manifest.RawFiles {
		if !consume(256) {
			return fmt.Errorf("archive: excessive manifest metadata bytes")
		}
		for _, value := range []string{raw.ObjectID, raw.RelPath, raw.ContentSHA256, string(raw.Origin), string(raw.State), raw.SourcePath, raw.LogSourceID, raw.SourceGeneration, raw.ParserVersion} {
			if !consume(len(value)) {
				return fmt.Errorf("archive: excessive manifest metadata bytes")
			}
		}
	}
	for key, value := range manifest.Checksums.Objects {
		if !consume(96) || !consume(len(key)) || !consume(len(value)) {
			return fmt.Errorf("archive: excessive manifest metadata bytes")
		}
	}
	for _, ref := range manifest.Coverage.SourceGenerations {
		if !consume(64) || !consume(len(ref.LogSourceID)) || !consume(len(ref.SourceGeneration)) {
			return fmt.Errorf("archive: excessive manifest metadata bytes")
		}
	}
	for _, reason := range manifest.Coverage.PartialReasons {
		if !consume(16) || !consume(len(reason)) {
			return fmt.Errorf("archive: excessive manifest metadata bytes")
		}
	}
	return nil
}

type archiveContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *archiveContextReader) Read(dst []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(dst)
}

func validateArchiveObjectIDs(objectIDs []string) error {
	if len(objectIDs) == 0 || len(objectIDs) > archiveMaxObjects {
		return fmt.Errorf("archive: invalid bounded object count")
	}
	for _, id := range objectIDs {
		if len(id) == 0 || len(id) > 256 {
			return fmt.Errorf("archive: invalid bounded object identity")
		}
	}
	return nil
}

type archiveLoadPlan struct {
	raws           []RawFile
	bytes, staging int64
	records        uint64
}

func planArchiveLoad(manifest *Manifest, objectIDs []string, decode bool) (archiveLoadPlan, error) {
	var plan archiveLoadPlan
	if err := manifest.Validate(); err != nil {
		return plan, err
	}
	if len(objectIDs) == 0 || len(objectIDs) > archiveMaxObjects || len(manifest.RawFiles) > archiveMaxObjects {
		return plan, fmt.Errorf("archive: invalid bounded object count")
	}
	seen := make(map[string]bool, len(objectIDs))
	for _, id := range objectIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		raw, ok := manifest.FindByObjectID(id)
		if !ok {
			return plan, fmt.Errorf("archive object %s not found", id)
		}
		if raw.State != ObjectStateManaged || raw.SizeBytes <= 0 || raw.SizeBytes > archiveMaxRawBytes-plan.bytes {
			return plan, fmt.Errorf("archive: unknown or excessive raw byte estimate")
		}
		plan.bytes += raw.SizeBytes
		plan.raws = append(plan.raws, raw)
	}
	if decode {
		if manifest.Coverage.ByteCount != uint64(plan.bytes) || manifest.Coverage.EventCount == 0 || manifest.Coverage.EventCount > archiveMaxEvents {
			return plan, fmt.Errorf("archive: unknown or excessive canonical record estimate")
		}
		plan.records = manifest.Coverage.EventCount
		// 原始/解码字符串与物化缓冲计四倍输入；每行另计2KiB覆盖事件切片、去重、时间校验和任务快照。
		// 单行解码前后临时缓冲各一份；发布器自己的VL写入预留仍独立执行，不能挤占WAL预留。
		lineBytes := plan.bytes
		if lineBytes > archiveMaxLineBytes {
			lineBytes = archiveMaxLineBytes
		}
		plan.staging = 4*plan.bytes + int64(plan.records)*2048 + 2*lineBytes + archiveReadBufferBytes
	} else {
		plan.staging = archiveReadBufferBytes
	}
	return plan, nil
}

// pinArchiveConsumer 的生存期绑定实际消费者，不因任务取消、租约或TTL先到而提前拆除原物。
func (m *RehydrateManager) pinArchiveConsumer(ctx context.Context, key PartitionKey, job *RehydrateJob) (context.Context, func(), error) {
	if m == nil {
		return ctx, func() {}, nil
	}
	consumerCtx, cancel := context.WithCancel(ctx)
	if job != nil {
		deadline := job.Deadline
		if !job.Lease.ExpiresAt.IsZero() && (deadline.IsZero() || job.Lease.ExpiresAt.Before(deadline)) {
			deadline = job.Lease.ExpiresAt
		}
		if !deadline.IsZero() {
			cancel()
			consumerCtx, cancel = context.WithDeadline(ctx, deadline)
		}
	}
	m.mu.Lock()
	if job != nil {
		entry := m.byTask[job.TaskID]
		if entry == nil {
			m.mu.Unlock()
			cancel()
			return nil, nil, fmt.Errorf("archive: recovery task disappeared")
		}
		entry.mu.Lock()
		if entry.job.Terminal() || !entry.job.Lease.Active(m.now()) {
			entry.mu.Unlock()
			m.mu.Unlock()
			cancel()
			return nil, nil, fmt.Errorf("archive: recovery task or lease is no longer active")
		}
		entry.cancel = cancel
		entry.mu.Unlock()
	}
	holdID := m.nextID("consumer")
	hold := CleanupHold{HoldID: holdID, Kind: holdKindArchiveConsumer, Partition: key, Generation: key.Generation, Reason: "archive-consumer-in-flight"}
	if job != nil {
		hold.TaskID = job.TaskID
	}
	m.holds[holdID] = hold
	m.mu.Unlock()
	var once sync.Once
	return consumerCtx, func() { once.Do(func() { cancel(); m.mu.Lock(); delete(m.holds, holdID); m.mu.Unlock() }) }, nil
}

func (m *RehydrateManager) setQueryOriginals(taskID string, events []ArchivedEvent) error {
	m.mu.Lock()
	entry := m.byTask[taskID]
	m.mu.Unlock()
	if entry == nil {
		return fmt.Errorf("archive: recovery task disappeared")
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.job.Terminal() || !entry.job.Lease.Active(m.now()) || (!entry.job.Deadline.IsZero() && !m.now().Before(entry.job.Deadline)) {
		return fmt.Errorf("archive: recovery task or lease is no longer active")
	}
	entry.events = append([]ArchivedEvent(nil), events...)
	return nil
}

func (m *RehydrateManager) dropQueryPayload(taskID string) {
	m.mu.Lock()
	entry := m.byTask[taskID]
	m.mu.Unlock()
	if entry == nil {
		return
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	entry.events = nil
	if entry.job.Result != nil {
		entry.job.Result.Events = nil
		entry.job.Result.OriginalTimes = nil
	}
}

// RehydrateSummary 仅保留成功恢复的有界摘要，不让任务查询重新复制已经释放的正文。
type RehydrateSummary struct {
	TaskID     string
	EventCount uint64
	TimeRange  TimeRange
	Verified   bool
	Published  bool
}

type rehydratePublishedSummary struct {
	RehydrateSummary
	permissionScope string
}

// PublishedRehydrateSummary 返回最近128项内的成功摘要；不存在不代表历史任务未成功。
func (b *QueryBackend) PublishedRehydrateSummary(taskID string) (RehydrateSummary, bool) {
	b.budgetMu.RLock()
	defer b.budgetMu.RUnlock()
	for _, summary := range b.published {
		if summary.TaskID == taskID {
			return summary.RehydrateSummary, true
		}
	}
	return RehydrateSummary{}, false
}

func rehydrateSummaryTimeRange(events []ArchivedEvent) (TimeRange, error) {
	if len(events) == 0 {
		return TimeRange{}, fmt.Errorf("archive: published summary has no events")
	}
	var first, last time.Time
	var fromUTC string
	for index, event := range events {
		when, err := time.Parse(time.RFC3339Nano, event.EventTimeUTC)
		if err != nil {
			return TimeRange{}, fmt.Errorf("archive: invalid published event time: %w", err)
		}
		// 第一条按位置初始化；合法公元一年零点不能被误作未初始化时间。
		if index == 0 || when.Before(first) {
			first, fromUTC = when, event.EventTimeUTC
		}
		if index == 0 || when.After(last) {
			last = when
		}
	}
	end := last.Add(time.Nanosecond).UTC()
	if end.Year() > 9999 {
		return TimeRange{}, fmt.Errorf("archive: published time range exceeds RFC3339 bound")
	}
	return TimeRange{FromUTC: fromUTC, ToUTC: end.Format(time.RFC3339Nano)}, nil
}

func (b *QueryBackend) rememberPublished(merge, scope string, job *RehydrateJob, events []ArchivedEvent) {
	timeRange, err := rehydrateSummaryTimeRange(events)
	if err != nil {
		// 摘要不可核实时撤销复用凭据，既不伪造零时间，也不沿用同键旧成功。
		b.budgetMu.Lock()
		delete(b.published, merge)
		b.budgetMu.Unlock()
		return
	}
	summary := rehydratePublishedSummary{permissionScope: scope, RehydrateSummary: RehydrateSummary{TaskID: job.TaskID, EventCount: uint64(len(events)), TimeRange: timeRange, Verified: true, Published: true}}
	b.budgetMu.Lock()
	defer b.budgetMu.Unlock()
	if b.published == nil {
		b.published = make(map[string]rehydratePublishedSummary)
	}
	if len(b.published) >= archiveMaxPublishedSummaries {
		for key := range b.published {
			delete(b.published, key)
			break
		}
	}
	b.published[merge] = summary
}

func (b *QueryBackend) reusePublished(ctx context.Context, key PartitionKey, objectIDs []string, scope string) (*RehydrateJob, bool) {
	b.budgetMu.RLock()
	summary, ok := b.published[MergeKey(key, objectIDs)]
	b.budgetMu.RUnlock()
	checker, canCheck := b.Publisher.(RehydrateReuseChecker)
	if !ok || summary.permissionScope != scope || !canCheck || !checker.CanReuseRehydrate(ctx, key, summary.TaskID) {
		return nil, false
	}
	job, err := b.Tasks.Get(summary.TaskID)
	return job, err == nil && job.State == TaskSucceeded
}

// CanReuseRehydrate 只认当前完整Catalog投影凭据；真实读取的ready/lease仍由既有查询链检查。
func (p *VLRehydratePublisher) CanReuseRehydrate(ctx context.Context, key PartitionKey, taskID string) bool {
	if ctx.Err() != nil || p == nil || p.Catalog == nil || p.VL == nil {
		return false
	}
	record, ok := p.Catalog.Get(catalog.PartitionKey{StorageNamespace: key.StorageNamespace, UTCDay: key.UTCDay})
	if !ok || record.Owner != catalog.OwnerArchive || record.Generation != key.Generation || record.RecoveryRequired || record.PublishedProjection == nil {
		return false
	}
	projection := record.PublishedProjection
	if !projection.CoverageComplete || projection.ManifestVersion == "" || projection.QueryGeneration != key.Generation || projection.QueryLocationDirID != record.OwnerDirID || projection.ProjectionGeneration != "rehydrate-"+taskID {
		return false
	}
	for _, generation := range projection.ProjectionGenerations {
		if generation == projection.ProjectionGeneration {
			return true
		}
	}
	return false
}
