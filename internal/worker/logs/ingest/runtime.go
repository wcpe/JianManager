// Package ingest wires the durable acquisition pipeline into a Worker process.
// It intentionally keeps source state and projection state under the Worker data
// root; CP only sees the resulting Catalog/Query View contract.
package ingest

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/archive"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/eventstore"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// SourceConfig describes one persistent Worker log source.
type SourceConfig struct {
	LogSourceID      string               `json:"log_source_id" mapstructure:"log_source_id"`
	SourceGeneration string               `json:"source_generation" mapstructure:"source_generation"`
	Path             string               `json:"path" mapstructure:"path"`
	Mode             pipeline.AcquireMode `json:"mode" mapstructure:"mode"`
	RotateTo         string               `json:"rotate_to,omitempty" mapstructure:"rotate_to"`
	ArchiveGlob      string               `json:"archive_glob,omitempty" mapstructure:"archive_glob"`
	Stream           string               `json:"stream,omitempty" mapstructure:"stream"`
	SourceCategory   logtypes.Source      `json:"source_category,omitempty" mapstructure:"source_category"`
	StorageNamespace string               `json:"storage_namespace,omitempty" mapstructure:"storage_namespace"`
	UTCDay           string               `json:"utc_day,omitempty" mapstructure:"utc_day"`
}

type persistedSource struct {
	PublicationPending bool               `json:"publication_pending,omitempty"`
	Ledger             []ledger.Entry     `json:"ledger"`
	WAL                []acquire.WALEntry `json:"wal"`
	// WALRefs 是 WAL 条目的引用形式（B1a）：正文已在事件段存储中的条目只写引用，加载时
	// 按 EventID 水合。与 WAL **并存**（按条切分），二者互不覆盖、可分别存在。
	WALRefs []acquire.WALRef `json:"wal_refs,omitempty"`
	// EventsStoredThrough 是事件段存储已覆盖的最大 record_end（B1a 的**按条**判据来源）：
	// record_end <= 该值的 WAL 条目，其正文已 durable 在段存储中，持久化时只需引用。
	// 为什么不能按源级 EventsStored 一刀切：事件体是在投递路径（appendEvents）才写入段存储，
	// 未投递条目的正文尚未入库——按源切分会丢正文（曾由既有测试抓到，见方案文件）。
	EventsStoredThrough  uint64 `json:"events_stored_through,omitempty"`
	ProjectionGeneration string `json:"projection_generation"`
	// EventsStored 表示该源的 canonical 事件体已落在磁盘段（events/ 目录），不在本文件。
	EventsStored bool `json:"events_stored,omitempty"`
	// Events 仅在旧格式或段写入失败时使用；正常情况为空（权威副本在 eventstore）。
	Events []logtypes.Event `json:"events,omitempty"`
}

type persistedState struct {
	Sources       map[string]persistedSource `json:"sources"`
	SourceConfigs map[string]SourceConfig    `json:"source_configs,omitempty"`
	Instances     map[string]InstanceBinding `json:"instances,omitempty"`
}

// pendingSpoolRoot 是未绑定实例输出的持久化暂存目录。
const pendingSpoolRoot = "pending"

// Manager owns configured source pipelines and their durable state.
type Manager struct {
	mu      sync.Mutex
	cycleMu sync.Mutex
	// pendingMu 串行化暂存写入与注册接管，避免绑定切换时发生乱序或重复回放。
	pendingMu sync.Mutex
	root      string
	vl        *vlsup.Client
	vlRoute   func(SourceConfig) (*vlsup.Client, bool, error)
	cat       *catalog.Catalog
	journal   catalog.Journal
	archive   *archive.Registry
	sources   map[string]SourceConfig
	pipes     map[string]*pipeline.Pipeline
	state     persistedState
	statePath string
	// events 是 canonical 事件体的追加式磁盘段存储（FR-484）；权威副本，state 只存元数据。
	events              *eventstore.Store
	verificationTimeout time.Duration
	// verifyBackoffMin/verifyBackoffMax 是投影校验重试的退避区间（B1c）；0 用默认常量。
	verifyBackoffMin time.Duration
	verifyBackoffMax time.Duration
	capacityProvider func() (acquire.CapacityBudget, error)
	recoveryHold     func(SourceConfig, string) (bool, string)
	// sourceErrs 记录每源最近一次已上报的采集错误，避免同一错误每 250ms 刷屏。
	sourceErrs map[string]string
	// pendingMaxStream/pendingMaxTotal 是 pending 暂存上限（M-6）；0 表示用默认常量。
	// 作为字段以便测试用小额度覆盖真实限额路径，而非只断言常量本身。
	pendingMaxStream int64
	pendingMaxTotal  int64
}

// SetPendingSpoolLimits 覆盖 pending 暂存上限（测试用）。
func (m *Manager) SetPendingSpoolLimits(perStream, total int64) {
	if m == nil {
		return
	}
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	m.pendingMaxStream = perStream
	m.pendingMaxTotal = total
}

// noteSourceError 记录并报告该源是否应再次上报该错误（同一文本只报一次）。
func (m *Manager) noteSourceError(sourceID, msg string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sourceErrs == nil {
		m.sourceErrs = map[string]string{}
	}
	if m.sourceErrs[sourceID] == msg {
		return false
	}
	m.sourceErrs[sourceID] = msg
	return true
}

// Options constructs a production ingestion manager. A nil VL client leaves
// sources durable but explicitly not delivered, preserving visible gaps.
type Options struct {
	Root                string
	VL                  *vlsup.Client
	Catalog             *catalog.Catalog
	Journal             catalog.Journal
	Archive             *archive.Registry
	Sources             []SourceConfig
	VerificationTimeout time.Duration
	CapacityProvider    func() (acquire.CapacityBudget, error)
	RecoveryHold        func(SourceConfig, string) (bool, string)
	// VLRoute returns the Catalog-selected client and whether publication is
	// frozen. Nil preserves initial HOT behavior.
	VLRoute func(SourceConfig) (*vlsup.Client, bool, error)
}

type CutoverReadiness struct {
	LedgerReady bool
	CutoffTime  time.Time
	Reasons     []string
}

func (m *Manager) CutoverReadiness() CutoverReadiness {
	result := CutoverReadiness{LedgerReady: true, CutoffTime: time.Now().UTC()}
	if m == nil {
		return CutoverReadiness{Reasons: []string{"ingest_manager_unavailable"}}
	}
	pending, pendingErr := m.pendingSpoolState()
	if pendingErr != nil {
		result.LedgerReady = false
		result.Reasons = append(result.Reasons, "pending_spool_unreadable")
	}
	if len(pending) > 0 {
		result.LedgerReady = false
		for _, key := range pending {
			result.Reasons = append(result.Reasons, "pending_spool:"+key)
		}
	}
	m.mu.Lock()
	keys := make([]string, 0, len(m.pipes))
	for key := range m.pipes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pipes := make(map[string]*pipeline.Pipeline, len(m.pipes))
	sources := make(map[string]SourceConfig, len(m.sources))
	for key, pipe := range m.pipes {
		pipes[key] = pipe
		sources[key] = m.sources[key]
	}
	m.mu.Unlock()
	if len(keys) == 0 {
		if len(pending) == 0 && pendingErr == nil {
			return CutoverReadiness{Reasons: []string{"no_managed_log_sources"}}
		}
		result.LedgerReady = false
		result.Reasons = append(result.Reasons, "no_managed_log_sources")
		return result
	}
	for _, key := range keys {
		entry := pipes[key].Ledger().Get(pipes[key].Key())
		if entry == nil {
			result.LedgerReady = false
			result.Reasons = append(result.Reasons, key+":ledger_missing")
			continue
		}
		if entry.AcquirePaused {
			result.LedgerReady = false
			result.Reasons = append(result.Reasons, key+":acquire_paused")
		}
		if pipes[key].Ledger().UnresolvedGapCount(pipes[key].Key()) > 0 {
			result.LedgerReady = false
			result.Reasons = append(result.Reasons, key+":unresolved_gaps")
		}
		if entry.Positions.Durable < entry.Positions.Read {
			result.LedgerReady = false
			result.Reasons = append(result.Reasons, key+":durable_behind_read")
		}
		if entry.Positions.Reclaim < entry.Positions.Durable {
			result.LedgerReady = false
			result.Reasons = append(result.Reasons, key+":reclaim_behind_durable")
		}
		source := sources[key]
		m.mu.Lock()
		saved := m.state.Sources[key]
		m.mu.Unlock()
		closed, complete := m.publishedClosedForSource(source, saved)
		if !complete || closed < entry.Positions.Durable {
			result.LedgerReady = false
			result.Reasons = append(result.Reasons, key+":projection_not_published")
		}
	}
	return result
}

func (m *Manager) PrepareCutoverReadiness() CutoverReadiness {
	if m == nil {
		return CutoverReadiness{Reasons: []string{"ingest_manager_unavailable"}}
	}
	m.cycleMu.Lock()
	defer m.cycleMu.Unlock()
	m.mu.Lock()
	pipes := make([]*pipeline.Pipeline, 0, len(m.pipes))
	for _, pipe := range m.pipes {
		pipes = append(pipes, pipe)
	}
	m.mu.Unlock()
	var flushReasons []string
	for _, pipe := range pipes {
		if _, err := pipe.Flush(); err != nil {
			flushReasons = append(flushReasons, pipe.Key().String()+":flush_failed")
		}
	}
	if err := m.persist(); err != nil {
		flushReasons = append(flushReasons, "persist_failed")
	}
	result := m.CutoverReadiness()
	if len(flushReasons) > 0 {
		result.LedgerReady = false
		result.Reasons = append(result.Reasons, flushReasons...)
	}
	return result
}

func (m *Manager) ResolveCoveredGaps() error {
	if m == nil {
		return fmt.Errorf("ingest: manager unavailable")
	}
	m.cycleMu.Lock()
	defer m.cycleMu.Unlock()
	m.mu.Lock()
	pipes := make(map[string]*pipeline.Pipeline, len(m.pipes))
	sources := make(map[string]SourceConfig, len(m.sources))
	for key, pipe := range m.pipes {
		pipes[key] = pipe
		sources[key] = m.sources[key]
	}
	m.mu.Unlock()
	for key, pipe := range pipes {
		entry := pipe.Ledger().Get(pipe.Key())
		if entry == nil {
			return fmt.Errorf("ingest: source %s ledger missing", key)
		}
		for _, gap := range entry.Gaps {
			if !gap.Resolved && gap.Reason == "STDIO_RAW_WRITE_FAILED" {
				return fmt.Errorf("ingest: source %s has an unverified Raw write failure; projection alone cannot resolve it", key)
			}
		}
		source := sources[key]
		m.mu.Lock()
		saved := m.state.Sources[key]
		m.mu.Unlock()
		closed, complete := m.publishedClosedForSource(source, saved)
		if !complete {
			return fmt.Errorf("ingest: source %s has no complete published projection", key)
		}
		if closed < entry.Positions.Durable || entry.Positions.Reclaim < entry.Positions.Durable {
			return fmt.Errorf("ingest: source %s recovery coverage is behind durable position", key)
		}
		if _, err := pipe.Ledger().ResolveGapsThrough(pipe.Key(), closed, "verified published projection"); err != nil {
			return err
		}
		if err := pipe.Ledger().ResumeAcquire(pipe.Key()); err != nil {
			return err
		}
	}
	return m.persist()
}

// New restores source ledger/WAL/projection state and creates configured pipelines.
func New(opts Options) (*Manager, error) {
	if opts.Root == "" {
		return nil, fmt.Errorf("ingest: root is required")
	}
	if opts.Catalog == nil {
		return nil, fmt.Errorf("ingest: catalog is required")
	}
	m := &Manager{
		root: opts.Root, vl: opts.VL, vlRoute: opts.VLRoute, cat: opts.Catalog, journal: opts.Journal, archive: opts.Archive,
		sources: make(map[string]SourceConfig), pipes: make(map[string]*pipeline.Pipeline),
		state:               persistedState{Sources: make(map[string]persistedSource)},
		statePath:           filepath.Join(opts.Root, "var", "log", "ingest.state.json"),
		verificationTimeout: opts.VerificationTimeout,
		capacityProvider:    opts.CapacityProvider,
		recoveryHold:        opts.RecoveryHold,
	}
	if m.verificationTimeout <= 0 {
		// 5 分钟（原 30 秒）。依据 2026-09-28 生产实测：VL 的 /insert/jsonline 是「接收即
		// 返回 200、索引异步」，恢复期单批 5746 条的可见性延迟**超过 30 秒**——写入 200 成功、
		// 30 秒后校验仍报 `not fully visible before deadline`，而稍后查询该代号确有 5746 条。
		// 校验本就有指数退避重试，数据一旦可见即立刻通过，故放宽窗口只影响「最坏等待」，
		// 不影响成功路径的耗时。可用 Options.VerificationTimeout 覆盖。
		m.verificationTimeout = 5 * time.Minute
	}
	// 投影校验的退避参数（B1c）：默认 200ms 起、封顶 2s。字段化以便测试用短窗口驱动。
	if m.verifyBackoffMin <= 0 {
		m.verifyBackoffMin = defaultVerifyBackoffMin
	}
	if m.verifyBackoffMax <= 0 {
		m.verifyBackoffMax = defaultVerifyBackoffMax
	}
	// 事件体走追加式磁盘段（FR-484）：state 文件只留元数据，避免整份重写与常驻切片。
	store, err := eventstore.Open(filepath.Join(opts.Root, "var", "log", "events"))
	if err != nil {
		return nil, err
	}
	m.events = store
	if err := m.load(); err != nil {
		_ = store.Close()
		return nil, err
	}
	// 旧格式迁移：state 里仍内联事件体时搬到段存储（失败则保留旧格式继续，不丢数据）。
	if err := m.migrateInlineEvents(); err != nil {
		_ = store.Close()
		return nil, err
	}
	restoredSources := make(map[string]SourceConfig)
	for key, source := range m.state.SourceConfigs {
		restoredSources[key] = source
	}
	for _, source := range opts.Sources {
		restoredSources[source.LogSourceID+"/"+source.SourceGeneration] = source
	}
	keys := make([]string, 0, len(restoredSources))
	for key := range restoredSources {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, sourceKey := range keys {
		source := restoredSources[sourceKey]
		if err := m.Register(source); err != nil {
			return nil, err
		}
		key := source.LogSourceID + "/" + source.SourceGeneration
		m.mu.Lock()
		saved := m.state.Sources[key]
		m.mu.Unlock()
		recoveryEvents, err := m.canonicalRecoveryEvents(key, saved)
		if err != nil {
			return nil, err
		}
		if len(recoveryEvents) > 0 && m.vl != nil {
			// A new VL data root or a lost projection response must never rely on
			// the old physical generation. Rebuild a new isolated projection and
			// publish its manifest/watermark atomically.
			generation := nextProjectionGeneration(saved.ProjectionGeneration)
			m.mu.Lock()
			saved.ProjectionGeneration = generation
			m.state.Sources[key] = saved
			m.mu.Unlock()
			if _, err := m.writeProjection(source, recoveryEvents, generation, recoveryEvents, nil, true); err != nil {
				return nil, err
			}
			m.mu.Lock()
			p := m.pipes[key]
			m.mu.Unlock()
			if p != nil && p.DeliveryState() == logtypes.DeliveryUnknown {
				if err := p.ResolveUnknownThroughProjection(recoveryEvents); err != nil {
					return nil, err
				}
			}
			if err := m.releaseRecovery(source, recoveryEvents); err != nil {
				return nil, err
			}
		}
	}
	if err := m.recoverPendingSpools(); err != nil {
		_ = store.Close()
		return nil, err
	}
	return m, nil
}

func (m *Manager) recoverPendingSpools() error {
	m.mu.Lock()
	bindings := make([]InstanceBinding, 0, len(m.state.Instances))
	for _, binding := range m.state.Instances {
		bindings = append(bindings, binding)
	}
	m.mu.Unlock()
	for _, binding := range bindings {
		m.pendingMu.Lock()
		err := m.flushPendingForBinding(binding)
		m.pendingMu.Unlock()
		if err != nil {
			m.recordRawWriteFailure(binding, pendingFlushStream(err), 0)
			return fmt.Errorf("ingest: recover pending instance output: %w", err)
		}
	}
	return nil
}

func (m *Manager) pendingSpoolState() ([]string, error) {
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	root := filepath.Join(m.root, "var", "log", pendingSpoolRoot)
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pending []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		files, readErr := os.ReadDir(filepath.Join(root, entry.Name()))
		if readErr != nil {
			return nil, readErr
		}
		for _, file := range files {
			if !file.IsDir() && strings.HasSuffix(file.Name(), ".spool") {
				pending = append(pending, entry.Name()+"/"+strings.TrimSuffix(file.Name(), ".spool"))
			}
		}
	}
	sort.Strings(pending)
	return pending, nil
}

func DiskCapacityProvider(root string, configured acquire.CapacityBudget) func() (acquire.CapacityBudget, error) {
	return func() (acquire.CapacityBudget, error) {
		usage, err := disk.Usage(root)
		if err != nil {
			return acquire.CapacityBudget{}, err
		}
		budget := configured
		if budget.DegradedAtPercent <= 0 {
			budget.DegradedAtPercent = 80
		}
		if budget.PauseAtPercent <= 0 {
			budget.PauseAtPercent = 90
		}
		budget.DiskUsagePercent = usage.UsedPercent
		return budget, nil
	}
}

// Register adds one source; registration is idempotent by logical source key.
func (m *Manager) Register(source SourceConfig) error {
	if source.LogSourceID == "" || source.SourceGeneration == "" {
		return fmt.Errorf("ingest: source identity is required")
	}
	if source.Mode == "" {
		source.Mode = pipeline.ModeFilePrimary
	}
	if source.SourceCategory == "" {
		source.SourceCategory = logtypes.SourceInstance
	}
	if source.StorageNamespace == "" {
		source.StorageNamespace = source.LogSourceID
	}
	if source.UTCDay == "" {
		source.UTCDay = time.Now().UTC().Format("2006-01-02")
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.pipes[key]; ok {
		if m.sources[key] != source {
			return fmt.Errorf("ingest: source identity already bound to another configuration")
		}
		return nil
	}
	led := ledger.New()
	wal := newWAL(led, ledger.SourceKey{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration})
	if saved, ok := m.state.Sources[key]; ok {
		if err := led.Restore(saved.Ledger); err != nil {
			return err
		}
		if len(saved.WALRefs) > 0 {
			// 引用形式（B1a）：内联条目直接恢复，引用条目按 EventID 从事件段存储水合。
			missing := wal.RestoreMixed(saved.WAL, saved.WALRefs, m.eventBodyLookup(key))
			if len(missing) > 0 {
				// 取不到正文的条目不得静默丢：记可见缺口并告警（与 FR-474 §5#1 同口径）。
				var from, to uint64 = ^uint64(0), 0
				for _, r := range missing {
					if r.RecordStart < from {
						from = r.RecordStart
					}
					if r.RecordEnd > to {
						to = r.RecordEnd
					}
				}
				if from == ^uint64(0) {
					from = 0
				}
				_ = led.RecordGap(ledger.SourceKey{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration},
					from, to, "WAL_BODY_MISSING",
					fmt.Sprintf("%d 条 WAL 引用在事件段存储中找不到正文（B1a 引用恢复）", len(missing)))
				slog.Warn("WAL 引用恢复：部分条目正文缺失，已记可见缺口",
					"logSourceID", source.LogSourceID, "missing", len(missing))
			}
		} else if err := wal.Restore(saved.WAL); err != nil {
			return err
		}
	}
	p, err := pipeline.New(pipeline.Options{
		Mode: source.Mode, LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration,
		Path: source.Path, RotateTo: source.RotateTo, Stream: source.Stream,
		SourceCategory: source.SourceCategory, Ledger: led, WAL: wal,
		SuppressRecursiveVL: source.SourceCategory == logtypes.SourceWorker || source.SourceCategory == logtypes.SourceNode,
		CapacityProvider:    m.capacityProvider,
		DurablePersist:      m.persist,
		ReclaimProof:        func(events []logtypes.Event) error { return m.releaseRecovery(source, events) },
		Delivery: pipeline.FuncHook(func(events []logtypes.Event) (pipeline.DeliveryResult, error) {
			return m.deliver(source, events)
		}),
	})
	if err != nil {
		return err
	}
	m.sources[key] = source
	m.pipes[key] = p
	if m.state.SourceConfigs == nil {
		m.state.SourceConfigs = make(map[string]SourceConfig)
	}
	m.state.SourceConfigs[key] = source
	return nil
}

// Start polls all configured sources until ctx is cancelled.
func (m *Manager) Start(ctx context.Context) {
	if m == nil {
		return
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.pollOnce()
		}
	}
}

// Stop flushes multiline tails, persists ledger/WAL state, and leaves any
// unresolved delivery responsibility visible for the next process.
func (m *Manager) Stop() error {
	if m == nil {
		return nil
	}
	m.cycleMu.Lock()
	defer m.cycleMu.Unlock()
	m.mu.Lock()
	pipes := make([]*pipeline.Pipeline, 0, len(m.pipes))
	for _, p := range m.pipes {
		pipes = append(pipes, p)
	}
	m.mu.Unlock()
	for _, p := range pipes {
		_, _ = p.Flush()
	}
	if err := m.persist(); err != nil {
		return err
	}
	// 事件段句柄在持久化之后关闭：先让 state 元数据落定，再释放段写入端。
	if m.events != nil {
		return m.events.Close()
	}
	return nil
}

func (m *Manager) pollOnce() {
	m.cycleMu.Lock()
	defer m.cycleMu.Unlock()
	m.mu.Lock()
	type sourcePipe struct {
		source SourceConfig
		pipe   *pipeline.Pipeline
	}
	pipes := make([]sourcePipe, 0, len(m.pipes))
	for key, p := range m.pipes {
		pipes = append(pipes, sourcePipe{source: m.sources[key], pipe: p})
	}
	m.mu.Unlock()
	dirty := false
	for _, item := range pipes {
		p := item.pipe
		before, beforeOK := p.Positions()
		events, metadataChanged, err := m.pollSource(item.source, p)
		after, afterOK := p.Positions()
		if before != after || beforeOK != afterOK || len(events) > 0 || metadataChanged || err != nil {
			dirty = true
		}
		if err != nil {
			// The pipeline records the gap; the Worker process remains available.
			// 但静默吞掉错误会掩盖 reclaim/projection 持续失败（真机 64 源实测曾整轮 reclaim=0
			// 却无任何日志）。按源限流暴露一次，便于运维定位。
			if m.noteSourceError(item.source.LogSourceID, err.Error()) {
				slog.Warn("日志采集循环错误", "source", item.source.LogSourceID, "error", err)
			}
			continue
		}
	}
	if dirty {
		_ = m.persist()
	}
}

func (m *Manager) pollSource(source SourceConfig, p *pipeline.Pipeline) ([]logtypes.Event, bool, error) {
	if source.Mode != pipeline.ModeFilePrimary || source.Path == "" {
		events, err := p.Poll()
		return events, false, err
	}
	archives, err := discoverSourceArchives(source)
	if err != nil {
		_ = p.Ledger().RecordGap(p.Key(), 0, 0, "ARCHIVE_DISCOVERY_FAILED", err.Error())
		return nil, false, err
	}
	pending := pendingArchives(p, archives)
	if len(pending) > 0 {
		p.SetRotateTarget(pending[len(pending)-1])
	}

	// A fresh binding imports historical closed segments before assigning
	// positions to the current latest.log.
	var all []logtypes.Event
	metadataChanged := false
	if p.CurrentSegmentUnread() && len(pending) > 0 {
		imported, changed, importErr := importArchives(p, pending)
		all = append(all, imported...)
		metadataChanged = metadataChanged || changed
		if importErr != nil {
			return all, metadataChanged, importErr
		}
		if pos, ok := p.Positions(); ok {
			if err := p.RebaseCurrentSegment(pos.Durable); err != nil {
				return all, metadataChanged, err
			}
		}
		pending = pendingArchives(p, archives)
	}

	rotated, err := p.PrepareRotation()
	if err != nil {
		return all, metadataChanged, err
	}
	if rotated {
		metadataChanged = true
		closed, closeErr := p.FlushClosedSegment()
		all = append(all, closed...)
		if closeErr != nil {
			return all, metadataChanged, closeErr
		}
		if confirmErr := p.ConfirmRotationCoverage(); confirmErr != nil {
			return all, metadataChanged, confirmErr
		}
		if len(pending) == 0 {
			_ = p.Ledger().RecordGap(p.Key(), 0, 0, "ROTATED_SEGMENT_NOT_READY", "replacement latest.log detected before its rotated segment became readable")
			return all, metadataChanged, fmt.Errorf("ingest: rotated segment is not available yet")
		}
		imported, changed, importErr := importArchives(p, pending)
		all = append(all, imported...)
		metadataChanged = metadataChanged || changed
		if importErr != nil {
			return all, metadataChanged, importErr
		}
		if pos, ok := p.Positions(); ok {
			if err := p.RebaseCurrentSegment(pos.Durable); err != nil {
				return all, metadataChanged, err
			}
		}
	}

	polled, pollErr := p.Poll()
	all = append(all, polled...)
	return all, metadataChanged, pollErr
}

func discoverSourceArchives(source SourceConfig) ([]string, error) {
	pattern := strings.TrimSpace(source.ArchiveGlob)
	if pattern == "" {
		pattern = filepath.Join(filepath.Dir(source.Path), "*.gz")
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	sort.Slice(matches, func(i, j int) bool {
		leftName, rightName := filepath.Base(matches[i]), filepath.Base(matches[j])
		if leftName != rightName {
			return leftName < rightName
		}
		return matches[i] < matches[j]
	})
	return matches, nil
}

func pendingArchives(p *pipeline.Pipeline, paths []string) []string {
	entry := p.Ledger().Get(p.Key())
	imported := make(map[string]bool)
	if entry != nil {
		for _, segment := range entry.Segments {
			if segment.Kind == ledger.SegmentGzip && segment.Imported {
				imported[filepath.Clean(segment.Path)] = true
				continue
			}
			if segment.Kind == ledger.SegmentGzip && segment.ImportError != "" {
				if info, err := os.Stat(segment.Path); err == nil && info.Size() == segment.ObservedSize &&
					info.ModTime().UnixNano() == segment.ObservedModNano {
					imported[filepath.Clean(segment.Path)] = true
				}
			}
		}
	}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if !imported[filepath.Clean(path)] {
			out = append(out, path)
		}
	}
	return out
}

func importArchives(p *pipeline.Pipeline, paths []string) ([]logtypes.Event, bool, error) {
	var out []logtypes.Event
	changed := false
	for _, path := range paths {
		result, err := p.ImportArchive(path)
		if result != nil {
			changed = true
			out = append(out, result.Events...)
		}
		if err != nil {
			return out, changed, err
		}
	}
	return out, changed, nil
}

func (m *Manager) deliver(source SourceConfig, events []logtypes.Event) (pipeline.DeliveryResult, error) {
	if len(events) == 0 {
		return pipeline.DeliveryResult{HTTPStatus: 204}, nil
	}
	if m.vl == nil && m.vlRoute == nil {
		return pipeline.DeliveryResult{HTTPStatus: 0}, fmt.Errorf("ingest: VictoriaLogs client is not ready")
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	m.mu.Lock()
	saved := m.state.Sources[key]
	seen := make(map[string]string, len(saved.Events))
	for _, event := range saved.Events {
		event, err := normalizeCanonicalEvent(event)
		if err != nil {
			m.mu.Unlock()
			return pipeline.DeliveryResult{}, err
		}
		seen[event.EventID] = event.CanonicalHash
	}
	eventsStored := saved.EventsStored && m.events != nil
	m.mu.Unlock()
	// 事件体已落段时，身份集合从段流式重建（不持全量切片）；旧格式仍走内联切片。
	if eventsStored {
		if err := m.events.Iterate(key, func(event logtypes.Event) error {
			normalized, err := normalizeCanonicalEvent(event)
			if err != nil {
				return err
			}
			seen[normalized.EventID] = normalized.CanonicalHash
			return nil
		}); err != nil {
			return pipeline.DeliveryResult{}, err
		}
	}
	m.mu.Lock()
	toWrite := make([]logtypes.Event, 0, len(events))
	for _, event := range events {
		if event.Record.End <= event.Record.Start {
			m.mu.Unlock()
			return pipeline.DeliveryResult{HTTPStatus: http.StatusUnprocessableEntity}, fmt.Errorf("ingest: zero-width event %s", event.EventID)
		}
		var err error
		event, err = normalizeCanonicalEvent(event)
		if err != nil {
			m.mu.Unlock()
			return pipeline.DeliveryResult{HTTPStatus: http.StatusUnprocessableEntity}, err
		}
		if existing, exists := seen[event.EventID]; exists {
			if existing != event.CanonicalHash {
				m.mu.Unlock()
				return pipeline.DeliveryResult{}, fmt.Errorf("ingest: IDENTITY_CONFLICT for event_id %s", event.EventID)
			}
			continue
		}
		toWrite = append(toWrite, event)
		seen[event.EventID] = event.CanonicalHash
	}
	m.mu.Unlock()
	if len(toWrite) == 0 {
		return pipeline.DeliveryResult{HTTPStatus: 204}, nil
	}
	expected, err := m.canonicalRecoveryEvents(key, saved)
	if err != nil {
		return pipeline.DeliveryResult{}, err
	}
	generation := nextProjectionGeneration(saved.ProjectionGeneration)
	writeEvents := toWrite
	if saved.PublicationPending {
		writeEvents = expected
	}
	result, err := m.writeProjection(source, expected, generation, writeEvents, toWrite, saved.PublicationPending)
	if err != nil {
		return result, err
	}
	if os.Getenv("JIANMANAGER_LOG_INJECT_ACK_LOSS") == "1" {
		result.AckLost = true
	}
	return result, nil
}

func (m *Manager) writeProjection(source SourceConfig, events []logtypes.Event, generation string, writeEvents, archiveEvents []logtypes.Event, replace bool) (pipeline.DeliveryResult, error) {
	if len(events) == 0 {
		return pipeline.DeliveryResult{HTTPStatus: http.StatusNoContent}, nil
	}
	for _, event := range events {
		if event.Record.End <= event.Record.Start {
			return pipeline.DeliveryResult{HTTPStatus: http.StatusUnprocessableEntity}, fmt.Errorf("ingest: zero-width event %s", event.EventID)
		}
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	m.mu.Lock()
	saved := m.state.Sources[key]
	saved.ProjectionGeneration = generation
	saved.PublicationPending = true
	m.state.Sources[key] = saved
	m.mu.Unlock()
	// The attempted generation is durable before any VL write. A lost response
	// or failed Catalog commit must rebuild into a different physical target.
	if err := m.persist(); err != nil {
		return pipeline.DeliveryResult{}, err
	}
	grouped, days, err := groupEventsByUTCDay(source, events)
	if err != nil {
		return pipeline.DeliveryResult{}, err
	}
	archiveGrouped, _, err := groupEventsByUTCDay(source, archiveEvents)
	if err != nil {
		return pipeline.DeliveryResult{}, err
	}
	writeGrouped, writeDays, err := groupEventsByUTCDay(source, writeEvents)
	if err != nil {
		return pipeline.DeliveryResult{}, err
	}
	if replace {
		writeDays = days
		writeGrouped = grouped
	}
	result := pipeline.DeliveryResult{HTTPStatus: http.StatusNoContent}
	for _, day := range writeDays {
		daySource := source
		daySource.UTCDay = day
		dayResult, writeErr := m.writeProjectionDay(daySource, grouped[day], writeGrouped[day], generation, archiveGrouped[day], replace)
		if dayResult.HTTPStatus != 0 {
			result.HTTPStatus = dayResult.HTTPStatus
		}
		if writeErr != nil {
			return result, writeErr
		}
	}
	m.mu.Lock()
	saved = m.state.Sources[key]
	saved.ProjectionGeneration = generation
	saved.PublicationPending = false
	m.state.Sources[key] = saved
	m.mu.Unlock()
	// 事件体写入磁盘段（先内容、后引用）：写入成功并 fsync 后，再让 state 元数据引用它。
	// 契约 §4.3/§5.3 要求这是 VL 数据根丢失后重建 projection 的权威集合，故不裁剪只换介质。
	if err := m.appendEvents(key, events); err != nil {
		return result, err
	}
	return result, m.persist()
}

// appendEvents 把该源的 canonical 事件集合同步到磁盘段，并把该源标记为“事件体已落段”。
//
// 入参是**完整**权威集合（与旧版 `saved.Events` 的语义一致），函数内部只追加尚未落段的
// 尾部：段内已有事件数就是已落段前缀长度，而权威集合始终以该前缀开头（由
// canonicalRecoveryEvents 的构造顺序保证：先段内容、后 durable WAL），因此只需 append
// 其后的差额。这样每轮 poll 都幂等，不会把同一事件重复写入段。
//
// 标记与落段在同一临界区内推进：只有段写成功（已 fsync）后 state 才会引用它。
func (m *Manager) appendEvents(key string, events []logtypes.Event) error {
	if m.events == nil || len(events) == 0 {
		return nil
	}
	stored, err := m.events.Count(key)
	if err != nil {
		return err
	}
	if stored > len(events) {
		// 段内已落段事件多于权威集合：说明段内容与清单不一致（例如密封段被外部改写、
		// 或恢复集合构造有漏）。此时继续执行会静默“承认”一段谁也无法解释的前缀，
		// 并让缺失事件永久消失，故必须硬失败而非跳过追加。
		return fmt.Errorf("ingest: eventstore ahead of authoritative set for %s: stored=%d authoritative=%d", key, stored, len(events))
	}
	if stored < len(events) {
		if err := m.events.Append(key, events[stored:]); err != nil {
			return err
		}
	}
	m.mu.Lock()
	saved := m.state.Sources[key]
	saved.EventsStored = true
	// B1a：记录段存储已覆盖到的最大 record_end。`events` 是权威集合（按记录位置有序），
	// 其最大 record_end 即段存储已保证覆盖的范围；WAL 持久化据此按条决定「引用 or 内联」。
	var covered uint64
	for _, ev := range events {
		if ev.Record.End > covered {
			covered = ev.Record.End
		}
	}
	if covered > saved.EventsStoredThrough {
		saved.EventsStoredThrough = covered
	}
	// 已落段后不再保留内联副本，否则常驻切片会重新把 RSS 推高（FR-484 目标）。
	saved.Events = nil
	m.state.Sources[key] = saved
	m.mu.Unlock()
	return nil
}

// migrateInlineEvents 把旧格式（state 内联 events）搬到段存储并重写 state。
// 任一步失败即保留旧格式继续运行（不丢数据、不半途改格式）。
func (m *Manager) migrateInlineEvents() error {
	if m.events == nil {
		return nil
	}
	type pending struct {
		key    string
		events []logtypes.Event
	}
	var todo []pending
	m.mu.Lock()
	for key, saved := range m.state.Sources {
		if len(saved.Events) > 0 {
			todo = append(todo, pending{key: key, events: saved.Events})
		}
	}
	m.mu.Unlock()
	if len(todo) == 0 {
		return nil
	}
	// 先全部落段；任一端失败则整体放弃迁移（旧格式数据仍完整）。
	for _, p := range todo {
		if err := m.appendEvents(p.key, p.events); err != nil {
			return nil // 保留旧格式继续用，不阻断启动
		}
	}
	m.mu.Lock()
	for _, p := range todo {
		saved := m.state.Sources[p.key]
		if saved.EventsStored {
			continue
		}
		saved.Events = nil
		saved.EventsStored = true
		m.state.Sources[p.key] = saved
	}
	m.mu.Unlock()
	return m.persist()
}

const maxPublishedProjectionGenerations = 64

func (m *Manager) writeProjectionDay(source SourceConfig, events, writeEvents []logtypes.Event, generation string, archiveEvents []logtypes.Event, replace bool) (pipeline.DeliveryResult, error) {
	catKey := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay}
	expectedAuthority, _ := m.cat.Get(catKey)
	if current, ok := m.cat.Get(catKey); ok && !replace &&
		len(publishedSourceProjectionGenerations(current.PublishedProjection, source)) >= maxPublishedProjectionGenerations {
		replace = true
		writeEvents = events
	}
	client, frozen, err := m.clientForSource(source)
	if err != nil {
		return pipeline.DeliveryResult{}, err
	}
	// 诊断日志（2026-09-28 恢复期「批次未落地」排查）：VL 侧核对时，需要能对上
	// 「哪一批、多少条、什么时间范围、每片写入返回什么」。生产实测曾出现
	// 「校验等待 09-27/28 窗口的数据，而 VL 里只有该源 09-20 的数据」——需要本日志
	// 才能判定是「没写」还是「写了没落地」。
	batchFirst, batchLast := "", ""
	if len(writeEvents) > 0 {
		batchFirst = writeEvents[0].EventTimeUTC
		batchLast = writeEvents[len(writeEvents)-1].EventTimeUTC
	}
	slog.Info("投影写入开始",
		"generation", generation, "source", source.LogSourceID,
		"events", len(writeEvents), "archiveEvents", len(archiveEvents),
		"firstEventTime", batchFirst, "lastEventTime", batchLast,
		)
	status, err := m.insertInBatches(client, writeEvents, generation)
	slog.Info("投影写入结束",
		"generation", generation, "source", source.LogSourceID,
		"events", len(writeEvents), "status", status, "err", err)
	if err != nil {
		return pipeline.DeliveryResult{HTTPStatus: status}, err
	}
	if m.archive != nil && len(archiveEvents) > 0 {
		archivePayload, payloadErr := projectionPayload(archiveEvents, generation)
		if payloadErr != nil {
			return pipeline.DeliveryResult{HTTPStatus: status}, payloadErr
		}
		archiveKey := archive.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay, Generation: 1}
		if _, archiveErr := m.archive.RegisterRaw(context.Background(), archiveKey, archive.RawSource{
			Data: archivePayload, Origin: archive.OriginWorkerGenerated,
			LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration,
			ParserVersion: "worker-log-parser/v1", EventCount: uint64(len(archiveEvents)),
			EventTimeFromUTC: archiveEvents[0].EventTimeUTC,
			EventTimeToUTC:   archiveEvents[len(archiveEvents)-1].EventTimeUTC,
		}); archiveErr != nil {
			return pipeline.DeliveryResult{HTTPStatus: status}, archiveErr
		}
	}
	if err := m.verifyProjection(client, source, generation, writeEvents); err != nil {
		return pipeline.DeliveryResult{HTTPStatus: status}, err
	}
	if frozen {
		return pipeline.DeliveryResult{HTTPStatus: status}, fmt.Errorf("ingest: Catalog write route is frozen; verified staging generation %s remains unpublished", generation)
	}
	if err := m.publish(source, generation, events, replace, expectedAuthority); err != nil {
		return pipeline.DeliveryResult{HTTPStatus: status}, err
	}
	return pipeline.DeliveryResult{HTTPStatus: status}, nil
}

// insertBatchMaxEvents 是单次 VL 插入的批量上限。
//
// 背景（2026-09-28 生产事故）：vlsup 客户端是 http.Client{Timeout: 5 * time.Second}，
// 而恢复期会把积压一次性拼成一个大 payload 投递；当状态文件达 1.2 GB 时，
// 单请求远超 5s → `vlsup: insert request: ... context deadline exceeded`
// → writeProjectionDay 失败 → 「日志采集运行时创建失败」→ 采集静默停摆（无重试、无告警）。
// 分批后每批体量可控，单请求远低于该超时。
const insertBatchMaxEvents = 500

// insertInBatches 按条数上限把事件分批投递到 VL，返回最后一批的 HTTP 状态。
// 任一批失败即中断，并原样返回该批状态与错误（调用方据此判定投递结果）。
func (m *Manager) insertInBatches(client *vlsup.Client, events []logtypes.Event, generation string) (int, error) {
	status := 0
	for start := 0; start < len(events); start += insertBatchMaxEvents {
		end := start + insertBatchMaxEvents
		if end > len(events) {
			end = len(events)
		}
		payload, err := projectionPayload(events[start:end], generation)
		if err != nil {
			return status, err
		}
		st, err := client.InsertJSONLines(context.Background(), payload)
		status = st
		if err != nil {
			return status, err
		}
	}
	return status, nil
}

func projectionPayload(events []logtypes.Event, generation string) ([]byte, error) {
	var b strings.Builder
	for _, event := range events {
		line := map[string]any{
			"_time": event.EventTimeUTC, "_msg": event.Message,
			"event_id": event.EventID, "log_source_id": event.Source.LogSourceID,
			"source_generation": event.Source.SourceGeneration,
			"parser_version":    event.Source.ParserVersion,
			"record_start":      event.Record.Start, "record_end": event.Record.End,
			"ingest_time_utc": event.IngestTimeUTC, "level": event.Level, "stream": event.Stream,
			"canonical_content_hash": event.CanonicalHash, "projection_generation": generation,
		}
		for k, v := range event.Fields {
			line[k] = v
		}
		data, err := json.Marshal(line)
		if err != nil {
			return nil, err
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

// eventUTCDay 解析事件所属 UTC 日，与 normalizeCanonicalEvent 的时间策略一致：
// 优先事件语义时间，缺失/不可解析时回退 ingest 时间，再回退源配置的 UTCDay。
//
// 为什么必须回退而不是报错：真实日志行可能没有可解析的语义时间（解析失败或非标准格式）。
// 若此处直接报错，投递/投影路径会失败 → 受管恢复责任无法转移 → reclaim 永不推进 → WAL 保留
// 全部事件（真机 64 源实测：Worker RSS 涨到 2GiB、state 文件 285MB）。事件时间回退是既有契约行为。
func eventUTCDay(source SourceConfig, event logtypes.Event) (string, error) {
	for _, raw := range []string{event.EventTimeUTC, event.IngestTimeUTC} {
		if raw == "" {
			continue
		}
		if when, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return when.UTC().Format("2006-01-02"), nil
		}
	}
	if source.UTCDay != "" {
		return source.UTCDay, nil
	}
	return "", fmt.Errorf("ingest: event %s has no usable event/ingest time or partition day", event.EventID)
}

// canonicalEventTime 解析事件的规范时间：优先语义时间 event_time，缺失/不可解析时回退 ingest 时间。
// 与 normalizeCanonicalEvent 的时间策略一致（同一契约行为），避免「无解析时间的真实日志行」令
// 投递/校验路径失败而导致 reclaim 停滞。
func canonicalEventTime(event logtypes.Event) (time.Time, error) {
	for _, raw := range []string{event.EventTimeUTC, event.IngestTimeUTC} {
		if raw == "" {
			continue
		}
		if when, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return when.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("no usable event or ingest time")
}

func groupEventsByUTCDay(source SourceConfig, events []logtypes.Event) (map[string][]logtypes.Event, []string, error) {
	grouped := make(map[string][]logtypes.Event)
	for _, event := range events {
		day, err := eventUTCDay(source, event)
		if err != nil {
			return nil, nil, err
		}
		grouped[day] = append(grouped[day], event)
	}
	days := make([]string, 0, len(grouped))
	for day := range grouped {
		days = append(days, day)
	}
	sort.Strings(days)
	return grouped, days, nil
}

// publishedClosedForSource 返回该源已发布投影的保守封闭前缀（所有受影响分区的最小封闭水位）。
// 受影响分区由事件集合的 UTC 日推导：归档导入/轮转可能跨日，事件自身才是权威来源。
//
// 事件体在磁盘段时按日聚合最大末端位置（流式，不载入全量事件）；旧格式仍用内联切片。
func (m *Manager) publishedClosedForSource(source SourceConfig, saved persistedSource) (uint64, bool) {
	dayMaxEnd := map[string]uint64{}
	if saved.EventsStored && m.events != nil {
		key := source.LogSourceID + "/" + source.SourceGeneration
		agg, err := m.events.DayMaxEnd(key, func(ev logtypes.Event) (string, error) {
			return eventUTCDay(source, ev)
		})
		if err != nil {
			return 0, false
		}
		dayMaxEnd = agg
	} else {
		for _, event := range saved.Events {
			day, err := eventUTCDay(source, event)
			if err != nil {
				return 0, false
			}
			if event.Record.End > dayMaxEnd[day] {
				dayMaxEnd[day] = event.Record.End
			}
		}
	}
	days := make([]string, 0, len(dayMaxEnd))
	for day := range dayMaxEnd {
		days = append(days, day)
	}
	sort.Strings(days)
	if len(days) == 0 && source.UTCDay != "" {
		days = []string{source.UTCDay}
	}
	if len(days) == 0 {
		return 0, false
	}

	var closed uint64
	for _, day := range days {
		key := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: day}
		rec, ok := m.cat.Get(key)
		if !ok || rec.PublishedProjection == nil || !rec.PublishedProjection.CoverageComplete {
			return 0, false
		}
		projection := rec.PublishedProjection
		dayClosed := uint64(0)
		if len(projection.SourceProjections) > 0 {
			found := false
			for _, scope := range projection.SourceProjections {
				if scope.LogSourceID == source.LogSourceID && scope.SourceGeneration == source.SourceGeneration {
					dayClosed, found = scope.ClosedVisibleSeq, true
					break
				}
			}
			if !found {
				return 0, false
			}
		} else {
			dayClosed = projection.ClosedVisibleSeq["default"]
			if sourceClosed := projection.ClosedVisibleSeq[key.String()]; sourceClosed > dayClosed {
				dayClosed = sourceClosed
			}
		}
		if dayMaxEnd[day] > dayClosed {
			return 0, false
		}
		if dayClosed > closed {
			closed = dayClosed
		}
	}
	return closed, true
}

// verifyProjection checks the actual VL query input before publishing a
// generation. JSON-stream request completion does not prove individual rows
// were accepted, and an uncertain target must never become query authority.
func (m *Manager) clientForSource(source SourceConfig) (*vlsup.Client, bool, error) {
	if m.vlRoute != nil {
		client, frozen, err := m.vlRoute(source)
		if err != nil {
			return nil, frozen, err
		}
		if client == nil {
			return nil, frozen, fmt.Errorf("ingest: Catalog-selected VictoriaLogs client is unavailable")
		}
		return client, frozen, nil
	}
	if m.vl == nil {
		return nil, false, fmt.Errorf("ingest: VictoriaLogs client is not ready")
	}
	return m.vl, false, nil
}

// 投影校验的退避默认值（B1c）。
const (
	defaultVerifyBackoffMin = 200 * time.Millisecond
	defaultVerifyBackoffMax = 2 * time.Second
)

// nextVerifyBackoff 返回下一次退避时长：指数增长并封顶（B1c）。
func nextVerifyBackoff(cur, max time.Duration) time.Duration {
	if cur <= 0 {
		return defaultVerifyBackoffMin
	}
	if max <= 0 {
		max = defaultVerifyBackoffMax
	}
	next := cur * 2
	if next > max {
		next = max
	}
	return next
}

// verifyProjection 分批校验投影可见性。
//
// 分批是必须的：VL 对单次查询有内存上限，而恢复期的事件数是整批积压（实测 747,822 条会让 VL
// 返回 400 `cannot calculate [sort by (_time) desc limit ...]`）。但**分批必须让期望集覆盖整批**：
// 各分片的时间范围会重叠（真实事件常共享时间戳），第 N 片的查询会返回其他分片的事件；
// 若只以本片为期望集，这些记录会被误判为 unexpected（2026-09-28 生产实测）。
func (m *Manager) verifyProjection(client *vlsup.Client, source SourceConfig, generation string, events []logtypes.Event) error {
	allowed := make(map[string]logtypes.Event, len(events))
	for _, event := range events {
		allowed[event.EventID] = event
	}
	for start := 0; start < len(events); start += insertBatchMaxEvents {
		end := start + insertBatchMaxEvents
		if end > len(events) {
			end = len(events)
		}
		if err := m.verifyProjectionChunk(client, source, generation, events[start:end], allowed); err != nil {
			return err
		}
	}
	return nil
}

// verifyProjectionChunk 校验单批事件在 VL 中完全可见（退避重试 + 永久错误立即失败）。
// allowed 是「可接受集合」（整批积压的全部事件）：本批之外的记录只要属于该集合且内容一致即接受。
func (m *Manager) verifyProjectionChunk(client *vlsup.Client, source SourceConfig, generation string, events []logtypes.Event, allowed map[string]logtypes.Event) error {
	ctx, cancel := context.WithTimeout(context.Background(), m.verificationTimeout)
	defer cancel()
	// 退避重试（B1c）：原实现以固定 200ms 轮询直至超时（默认 30 秒 ≈ 最多约 150 次校验
	// 查询/源）。高负载时每次查询更慢、重试互相叠加，会把 VL 与磁盘一起压垮——2026-09-28
	// 生产事故的成因之一正是校验查询被如此重试（含 `cannot execute query [...]` 这类 4xx）。
	// 现改为指数退避封顶，并让「重试无意义」的错误（认证失败 / 4xx 语义错误）立即失败。
	backoff := m.verifyBackoffMin
	var lastErr error
	for {
		complete, err := m.verifyProjectionOnceAllowed(ctx, client, source, generation, events, allowed)
		if complete && err == nil {
			return nil
		}
		if err != nil {
			if vlsup.IsPermanent(err) {
				return fmt.Errorf("ingest: projection %s verification failed permanently: %w", generation, err)
			}
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("ingest: projection %s verification failed: %w", generation, lastErr)
			}
			return fmt.Errorf("ingest: projection %s not fully visible before deadline: %w", generation, ctx.Err())
		case <-time.After(backoff):
		}
		backoff = nextVerifyBackoff(backoff, m.verifyBackoffMax)
	}
}

func (m *Manager) verifyProjectionOnce(ctx context.Context, source SourceConfig, generation string, events []logtypes.Event) (bool, error) {
	return m.verifyProjectionOnceAllowed(ctx, m.vl, source, generation, events, nil)
}

// verifyProjectionOnceAllowed 执行一次校验查询。
//
// 与 verifyProjectionOnceWithClient 的唯一差别是 allowed：分批校验时，各片的时间范围重叠，
// 查询会返回**同批其他分片**的记录。这些记录必须被接受（否则误判 unexpected），
// 但仍要求内容逐字段一致、且不得重复出现；完整性只要求本片 events 全部可见。
// allowed 为 nil 时退化为「本片即全部」的原语义。
func (m *Manager) verifyProjectionOnceAllowed(ctx context.Context, client *vlsup.Client, source SourceConfig, generation string, events []logtypes.Event, allowed map[string]logtypes.Event) (bool, error) {
	if client == nil {
		return false, fmt.Errorf("ingest: VictoriaLogs verification client is unavailable")
	}
	want := make(map[string]logtypes.Event, len(events))
	var first, last time.Time
	for _, event := range events {
		if _, exists := want[event.EventID]; exists {
			return false, fmt.Errorf("duplicate canonical event_id %s", event.EventID)
		}
		when, err := canonicalEventTime(event)
		if err != nil {
			return false, fmt.Errorf("invalid canonical event time for %s: %w", event.EventID, err)
		}
		if first.IsZero() || when.Before(first) {
			first = when
		}
		if last.IsZero() || when.After(last) {
			last = when
		}
		want[event.EventID] = event
	}
	selector := "projection_generation:=" + strconv.Quote(generation) +
		" AND log_source_id:=" + strconv.Quote(source.LogSourceID) +
		" AND source_generation:=" + strconv.Quote(source.SourceGeneration)
	params := url.Values{
		"query": {selector + " | fields _time, _msg, event_id, level, stream, canonical_content_hash"},
		"limit": {strconv.Itoa(len(events) + 1)},
		"start": {first.Add(-time.Second).UTC().Format(time.RFC3339Nano)},
		"end":   {last.Add(time.Second).UTC().Format(time.RFC3339Nano)},
	}
	seen := make(map[string]bool, len(events))
	seenOther := make(map[string]bool)
	err := client.Stream(ctx, "/select/logsql/query", params, func(body io.Reader) error {
		scanner := bufio.NewScanner(io.LimitReader(body, 32<<20+1))
		scanner.Buffer(make([]byte, 64*1024), 4<<20)
		for scanner.Scan() {
			var row struct {
				EventID       string `json:"event_id"`
				CanonicalHash string `json:"canonical_content_hash"`
				EventTimeUTC  string `json:"_time"`
				Message       string `json:"_msg"`
				Level         string `json:"level"`
				Stream        string `json:"stream"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
				return err
			}
			event, inBatch := want[row.EventID]
			if !inBatch {
				other, ok := allowed[row.EventID]
				if !ok || seenOther[row.EventID] {
					return fmt.Errorf("unexpected or duplicate projection event_id %s", row.EventID)
				}
				if row.CanonicalHash != other.CanonicalHash || row.EventTimeUTC != other.EventTimeUTC ||
					row.Message != other.Message || row.Level != other.Level || row.Stream != other.Stream {
					return fmt.Errorf("projection content mismatch for event_id %s", row.EventID)
				}
				seenOther[row.EventID] = true
				continue
			}
			if seen[row.EventID] {
				return fmt.Errorf("unexpected or duplicate projection event_id %s", row.EventID)
			}
			if row.CanonicalHash != event.CanonicalHash || row.EventTimeUTC != event.EventTimeUTC ||
				row.Message != event.Message || row.Level != event.Level || row.Stream != event.Stream {
				return fmt.Errorf("projection content mismatch for event_id %s", row.EventID)
			}
			seen[row.EventID] = true
		}
		return scanner.Err()
	})
	if err != nil {
		return false, err
	}
	return len(seen) == len(want), nil
}

func (m *Manager) verifyProjectionOnceWithClient(ctx context.Context, client *vlsup.Client, source SourceConfig, generation string, events []logtypes.Event) (bool, error) {
	if client == nil {
		return false, fmt.Errorf("ingest: VictoriaLogs verification client is unavailable")
	}
	want := make(map[string]logtypes.Event, len(events))
	var first, last time.Time
	for _, event := range events {
		if _, exists := want[event.EventID]; exists {
			return false, fmt.Errorf("duplicate canonical event_id %s", event.EventID)
		}
		when, err := canonicalEventTime(event)
		if err != nil {
			return false, fmt.Errorf("invalid canonical event time for %s: %w", event.EventID, err)
		}
		if first.IsZero() || when.Before(first) {
			first = when
		}
		if last.IsZero() || when.After(last) {
			last = when
		}
		want[event.EventID] = event
	}
	selector := "projection_generation:=" + strconv.Quote(generation) +
		" AND log_source_id:=" + strconv.Quote(source.LogSourceID) +
		" AND source_generation:=" + strconv.Quote(source.SourceGeneration)
	params := url.Values{
		"query": {selector + " | fields _time, _msg, event_id, level, stream, canonical_content_hash"},
		"limit": {strconv.Itoa(len(events) + 1)},
		"start": {first.Add(-time.Second).UTC().Format(time.RFC3339Nano)},
		"end":   {last.Add(time.Second).UTC().Format(time.RFC3339Nano)},
	}
	seen := make(map[string]bool, len(events))
	err := client.Stream(ctx, "/select/logsql/query", params, func(body io.Reader) error {
		scanner := bufio.NewScanner(io.LimitReader(body, 32<<20+1))
		scanner.Buffer(make([]byte, 64*1024), 4<<20)
		for scanner.Scan() {
			var row struct {
				EventID       string `json:"event_id"`
				CanonicalHash string `json:"canonical_content_hash"`
				EventTimeUTC  string `json:"_time"`
				Message       string `json:"_msg"`
				Level         string `json:"level"`
				Stream        string `json:"stream"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
				return err
			}
			event, ok := want[row.EventID]
			if !ok || seen[row.EventID] {
				return fmt.Errorf("unexpected or duplicate projection event_id %s", row.EventID)
			}
			if row.CanonicalHash != event.CanonicalHash || row.EventTimeUTC != event.EventTimeUTC ||
				row.Message != event.Message || row.Level != event.Level || row.Stream != event.Stream {
				return fmt.Errorf("projection content mismatch for event_id %s", row.EventID)
			}
			seen[row.EventID] = true
		}
		return scanner.Err()
	})
	if err != nil {
		return false, err
	}
	return len(seen) == len(want), nil
}

// releaseRecovery transfers WAL responsibility only after the delivery result
// and canonical projection have both been persisted. The projection itself is
// the durable receiver; HTTP 2xx alone never reaches this method.
func (m *Manager) releaseRecovery(source SourceConfig, events []logtypes.Event) error {
	if len(events) == 0 {
		return nil
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	m.mu.Lock()
	p := m.pipes[key]
	generation := m.state.Sources[key].ProjectionGeneration
	m.mu.Unlock()
	if p == nil {
		return fmt.Errorf("ingest: pipeline %s missing for reclaim proof", key)
	}
	to := events[len(events)-1].Record.End
	positions, ok := p.Positions()
	if !ok {
		return fmt.Errorf("ingest: missing ledger positions for %s", key)
	}
	from := positions.Reclaim
	currentSegID := fmt.Sprintf("projection-recovery-%s-%d", generation, to)
	segID := currentSegID
	ref, exists := p.RecoveryCovering(from, to)
	if exists {
		segID = ref.SegmentID
	}
	if !exists {
		if from >= to {
			return nil
		}
		if err := p.BindRecoverySegment(segID, "projection://"+generation, from, to); err != nil {
			return err
		}
		ref, _ = p.RecoveryRef(segID)
	}
	if ref.State == logtypes.RecoveryStaged {
		if err := p.TransitionRecovery(segID, logtypes.RecoveryDurableVerified, "", ""); err != nil {
			return err
		}
		ref, _ = p.RecoveryRef(segID)
	}
	receiver := "projection:" + generation
	if ref.State == logtypes.RecoveryDurableVerified {
		if err := p.TransitionRecovery(segID, logtypes.RecoveryWALResponsibilityXfer, "", receiver); err != nil {
			return err
		}
		ref, _ = p.RecoveryRef(segID)
	}
	if ref.State == logtypes.RecoveryCleaned {
		return nil
	}
	if m.recoveryHold != nil {
		_, days, err := groupEventsByUTCDay(source, events)
		if err != nil {
			return err
		}
		for _, day := range days {
			daySource := source
			daySource.UTCDay = day
			hold, reason := m.recoveryHold(daySource, generation)
			if !hold {
				continue
			}
			if err := p.SetRecoveryHold(segID, true); err != nil {
				return err
			}
			if err := m.persist(); err != nil {
				return err
			}
			return fmt.Errorf("ingest: recovery segment %s retained by hold for %s: %s", segID, day, reason)
		}
		if err := p.SetRecoveryHold(segID, false); err != nil {
			return err
		}
		ref, _ = p.RecoveryRef(segID)
	}
	if ref.State == logtypes.RecoveryWALResponsibilityXfer {
		reason := logtypes.ReleaseProjectionBacked
		if ref.Path != "projection://"+generation {
			reason = logtypes.ReleaseNextCopyVerified
		}
		if err := p.TransitionRecovery(segID, logtypes.RecoveryReleased, reason, receiver); err != nil {
			return err
		}
		ref, _ = p.RecoveryRef(segID)
	}
	if ref.State != logtypes.RecoveryReleased {
		return fmt.Errorf("ingest: recovery segment %s cannot be completed from %s", segID, ref.State)
	}
	if positions.Reclaim < ref.CoversTo {
		if _, err := p.TryReclaim(); err != nil {
			return err
		}
	}
	if err := p.TransitionRecovery(segID, logtypes.RecoveryCleaned, logtypes.ReleaseProjectionBacked, receiver); err != nil {
		return err
	}
	return m.persist()
}

func nextProjectionGeneration(current string) string {
	if current == "" {
		return "projection-1"
	}
	var n int
	if _, err := fmt.Sscanf(current, "projection-%d", &n); err == nil && n > 0 {
		return fmt.Sprintf("projection-%d", n+1)
	}
	return current + "-rebuild"
}

// eventBodyLookup 返回「按 EventID 取回 canonical 事件体」的查询函数（B1a 引用水合）。
//
// 单次遍历该源的事件段存储建索引。存储不可用或遍历失败时返回恒 false 的查询函数——
// 调用方据此走「取不到正文 → 记可见缺口」的路径，而不是静默丢弃。
func (m *Manager) eventBodyLookup(key string) func(eventID string) (logtypes.Event, bool) {
	if m.events == nil {
		return func(string) (logtypes.Event, bool) { return logtypes.Event{}, false }
	}
	index := make(map[string]logtypes.Event)
	if err := m.events.Iterate(key, func(ev logtypes.Event) error {
		index[ev.EventID] = ev
		return nil
	}); err != nil {
		slog.Warn("WAL 引用恢复：遍历事件段存储失败，将按缺失记账", "key", key, "error", err)
		return func(string) (logtypes.Event, bool) { return logtypes.Event{}, false }
	}
	return func(eventID string) (logtypes.Event, bool) {
		ev, ok := index[eventID]
		return ev, ok
	}
}

// canonicalRecoveryEvents 返回该源完整的 canonical 事件集合（VL 数据根丢失后重建 projection 的权威输入）。
//
// 事件体来自磁盘段（eventstore）；仅当该源仍是旧格式（EventsStored=false）时才用内联切片。
// 集合语义不变：仍为**全部**已登账事件，不做任何裁剪。
func (m *Manager) canonicalRecoveryEvents(key string, saved persistedSource) ([]logtypes.Event, error) {
	seen := make(map[string]string, len(saved.WAL))
	events := make([]logtypes.Event, 0, len(saved.WAL))
	add := func(event logtypes.Event) error {
		if event.Record.End <= event.Record.Start {
			return nil // A legacy zero-width record cannot become a canonical event.
		}
		var err error
		event, err = normalizeCanonicalEvent(event)
		if err != nil {
			return err
		}
		// 该 event_id 的规范哈希已在本次登账集合中时不重复入账（只用于身份对账）。
		if existing, ok := seen[event.EventID]; ok {
			if existing != event.CanonicalHash {
				return fmt.Errorf("ingest: IDENTITY_CONFLICT for event_id %s", event.EventID)
			}
			return nil
		}
		seen[event.EventID] = event.CanonicalHash
		events = append(events, event)
		return nil
	}
	if saved.EventsStored && m.events != nil {
		if err := m.events.Iterate(key, add); err != nil {
			return nil, err
		}
	} else {
		for _, event := range saved.Events {
			if err := add(event); err != nil {
				return nil, err
			}
		}
	}
	for _, entry := range saved.WAL {
		if entry.Durable {
			if err := add(entry.Event); err != nil {
				return nil, err
			}
		}
	}
	return events, nil
}

func normalizeCanonicalEvent(event logtypes.Event) (logtypes.Event, error) {
	if event.EventTimeUTC != "" {
		return event, nil
	}
	when, err := time.Parse(time.RFC3339Nano, event.IngestTimeUTC)
	if err != nil {
		return event, fmt.Errorf("ingest: event %s has no stable event or ingest time: %w", event.EventID, err)
	}
	event.EventTimeUTC = when.UTC().Format(time.RFC3339Nano)
	event.CanonicalHash = logtypes.CanonicalContentHash(event.EventTimeUTC, event.Level, event.Stream, event.Message)
	return event, nil
}

func (m *Manager) publish(source SourceConfig, projectionGeneration string, events []logtypes.Event, replace bool, observed ...*catalog.Record) error {
	key := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay}
	maxEnd := uint64(0)
	for _, event := range events {
		if event.Record.End > maxEnd {
			maxEnd = event.Record.End
		}
	}
	if maxEnd == 0 {
		return nil
	}
	rec, ok := m.cat.Get(key)
	expected := rec.Clone()
	if len(observed) > 0 {
		expected = observed[0]
	}
	if !ok {
		rec = catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-"+projectionGeneration)
	}
	if rec.PublishedProjection == nil {
		rec.PublishedProjection = &catalog.PublishedProjection{}
	}
	proj := rec.PublishedProjection
	previousGenerations := publishedProjectionGenerations(proj)
	if len(proj.SourceProjections) == 0 {
		for _, ref := range proj.CoveredSourceGenerations {
			closed := proj.ClosedVisibleSeq[ref.LogSourceID+"/"+ref.SourceGeneration]
			if closed == 0 {
				closed = proj.ClosedVisibleSeq["default"]
			}
			proj.SourceProjections = append(proj.SourceProjections, catalog.SourceProjection{
				SourceGenerationRef: ref, ProjectionGenerations: append([]string(nil), previousGenerations...), ClosedVisibleSeq: closed})
		}
	}
	ref := catalog.SourceGenerationRef{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration}
	index := -1
	for i := range proj.SourceProjections {
		if proj.SourceProjections[i].SourceGenerationRef == ref {
			index = i
			break
		}
	}
	if index < 0 {
		index = len(proj.SourceProjections)
		scope := catalog.SourceProjection{SourceGenerationRef: ref}
		// Older single-source records may omit their redundant source reference.
		if len(proj.SourceProjections) == 0 && len(proj.CoveredSourceGenerations) == 0 && !replace {
			scope.ProjectionGenerations = previousGenerations
		}
		proj.SourceProjections = append(proj.SourceProjections, scope)
	}
	scope := &proj.SourceProjections[index]
	if replace {
		scope.ProjectionGenerations = []string{projectionGeneration}
	} else {
		scope.ProjectionGenerations = appendUniqueGeneration(scope.ProjectionGenerations, projectionGeneration)
	}
	scope.ClosedVisibleSeq = maxEnd
	sort.Slice(proj.SourceProjections, func(i, j int) bool {
		a, b := proj.SourceProjections[i], proj.SourceProjections[j]
		if a.LogSourceID != b.LogSourceID {
			return a.LogSourceID < b.LogSourceID
		}
		return a.SourceGeneration < b.SourceGeneration
	})
	proj.ProjectionGeneration = projectionGeneration
	proj.ProjectionGenerations = nil
	proj.CoveredSourceGenerations = nil
	proj.ClosedVisibleSeq = make(map[string]uint64)
	var maxClosed uint64
	for _, sourceProjection := range proj.SourceProjections {
		proj.CoveredSourceGenerations = append(proj.CoveredSourceGenerations, sourceProjection.SourceGenerationRef)
		for _, generation := range sourceProjection.ProjectionGenerations {
			proj.ProjectionGenerations = appendUniqueGeneration(proj.ProjectionGenerations, generation)
		}
		proj.ClosedVisibleSeq[sourceProjection.LogSourceID+"/"+sourceProjection.SourceGeneration] = sourceProjection.ClosedVisibleSeq
		if sourceProjection.ClosedVisibleSeq > maxClosed {
			maxClosed = sourceProjection.ClosedVisibleSeq
		}
	}
	proj.ClosedVisibleSeq[key.String()], proj.ClosedVisibleSeq["default"] = maxClosed, maxClosed
	manifestData, err := json.Marshal(proj.SourceProjections)
	if err != nil {
		return err
	}
	proj.ManifestVersion = fmt.Sprintf("manifest-%x", sha256.Sum256(manifestData))
	proj.CoverageComplete = true
	proj.QueryLocationDirID = rec.OwnerDirID
	proj.QueryGeneration = rec.Generation
	rec.PublishedProjection = proj
	return m.cat.PublishProjection(expected, rec)
}

func publishedProjectionGenerations(projection *catalog.PublishedProjection) []string {
	if projection == nil {
		return nil
	}
	if len(projection.ProjectionGenerations) > 0 {
		return append([]string(nil), projection.ProjectionGenerations...)
	}
	if projection.ProjectionGeneration != "" {
		return []string{projection.ProjectionGeneration}
	}
	return nil
}

func publishedSourceProjectionGenerations(projection *catalog.PublishedProjection, source SourceConfig) []string {
	if projection != nil && len(projection.SourceProjections) > 0 {
		for _, scope := range projection.SourceProjections {
			if scope.LogSourceID == source.LogSourceID && scope.SourceGeneration == source.SourceGeneration {
				return scope.ProjectionGenerations
			}
		}
		return nil
	}
	return publishedProjectionGenerations(projection)
}

func appendUniqueGeneration(generations []string, generation string) []string {
	for _, existing := range generations {
		if existing == generation {
			return generations
		}
	}
	return append(generations, generation)
}

func (m *Manager) load() error {
	b, err := os.ReadFile(m.statePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ingest: read state: %w", err)
	}
	if len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, &m.state); err != nil {
		return fmt.Errorf("ingest: decode state: %w", err)
	}
	if m.state.Sources == nil {
		m.state.Sources = make(map[string]persistedSource)
	}
	if m.state.Instances == nil {
		m.state.Instances = make(map[string]InstanceBinding)
	}
	return nil
}

func (m *Manager) persist() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, p := range m.pipes {
		prev := m.state.Sources[key]
		entry := persistedSource{Ledger: p.Ledger().Snapshot(), ProjectionGeneration: prev.ProjectionGeneration,
			PublicationPending: prev.PublicationPending, EventsStored: prev.EventsStored,
			EventsStoredThrough: prev.EventsStoredThrough}
		// B1a：按条切分——段存储已覆盖（record_end <= EventsStoredThrough）的条目只写引用；
		// 其余（尚未投递、正文未入库，或段存储不可用）必须内联保存，绝不丢正文。
		through := prev.EventsStoredThrough
		refs, inline := p.WAL().SnapshotSplit(func(ev logtypes.Event) bool {
			return entry.EventsStored && through > 0 && ev.Record.End <= through
		})
		entry.WALRefs = refs
		entry.WAL = append(entry.WAL, inline...)
		// 事件体已在磁盘段时不写回内联切片——这正是原先 state 涨到 180MB、每次 persist
		// 触发 130MB MarshalIndent 的根源（FR-484 阶段0 量测）。
		if !entry.EventsStored {
			entry.Events = append(entry.Events, prev.Events...)
		}
		m.state.Sources[key] = entry
	}
	// 元数据量小，用流式编码直接写文件，避免 “先 Marshal 出整份 []byte 再写” 的双份峰值。
	if err := os.MkdirAll(filepath.Dir(m.statePath), 0o755); err != nil {
		return err
	}
	tmp := m.statePath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err = enc.Encode(&m.state); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, m.statePath)
}

func newWAL(led *ledger.Ledger, key ledger.SourceKey) *acquire.WAL {
	return acquire.NewWAL(led, key)
}
