package pipeline

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/normalize"
)

// Options 构造端到端管道。
type Options struct {
	// Mode 默认 FILE_PRIMARY。
	Mode AcquireMode
	// LogSourceID / SourceGeneration 账本主键；ParserVersion 绑定 normalize。
	LogSourceID      string
	SourceGeneration string
	// Path FILE_PRIMARY 日志路径（latest.log）。
	Path string
	// RotateTo 轮转目标路径（可选）。
	RotateTo string
	// Reader STDIO_PRIMARY 输入；与 Path 互斥。
	Reader io.Reader
	// Stream stdout|stderr。
	Stream string
	// SourceCategory instance|worker|node。
	SourceCategory logtypes.Source
	// Delivery WAL durable 后的投递钩子。
	Delivery DeliveryHook
	// Location / BaseTime / IngestTimeUTC 传递给 normalize。
	Location      *time.Location
	BaseTime      time.Time
	IngestTimeUTC string
	// Limits 多行上限。
	Limits normalize.Limits
	// Capacity 可选容量预算；零值用 acquire.DefaultCapacityBudget。
	Capacity         *acquire.CapacityBudget
	CapacityProvider func() (acquire.CapacityBudget, error)
	// SuppressRecursiveVL source=worker 时禁止失败写回 VL。
	SuppressRecursiveVL bool
	// ReclaimProof 在请求结果已登记且投影完成后登记恢复责任证明。
	ReclaimProof func(events []logtypes.Event) error
	// DurablePersist 在 WAL 提交后、HTTP 投递前写入耐久快照。
	DurablePersist func() error
	// Ledger/WAL 可由 Worker 生产运行时注入持久恢复实例；为空时创建内存实例。
	Ledger *ledger.Ledger
	WAL    *acquire.WAL
}

// Pipeline 端到端：tail/stdio → normalize → WAL → DeliveryHook → CanReclaim 门禁。
type Pipeline struct {
	mode          AcquireMode
	key           ledger.SourceKey
	led           *ledger.Ledger
	wal           *acquire.WAL
	inner         *acquire.Pipeline
	tailer        *acquire.FileTailer
	imp           *acquire.ArchiveImporter
	bound         *NormalizeBoundary
	hook          DeliveryHook
	reclaimProof  func(events []logtypes.Event) error
	src           logtypes.SourceIdentity
	normalizeOpts normalize.Options
	reader        io.Reader
	// stdioBuf STDIO 会话内未消费行缓冲。
	stdioRead uint64
	// deliveredCount 累计已进入投递钩子的事件数（测试断言用）。
	//
	// 原先保留 []logtypes.Event 全量切片，会随会话内总事件数线性增长却无人读取内容
	// （FR-484 阶段5：64 源实测 ≈36MB），故只保留计数。
	deliveredCount int
	// lineMode false=normalize 多行；测试可切 LineHook 对照。
	useNormalize bool
}

// New 创建管道。Mode 默认 FILE_PRIMARY。
func New(opts Options) (*Pipeline, error) {
	mode := opts.Mode
	if mode == "" {
		mode = DefaultMode()
	}
	if !mode.Valid() {
		return nil, fmt.Errorf("pipeline: unknown acquire mode %q", mode)
	}
	if opts.LogSourceID == "" || opts.SourceGeneration == "" {
		return nil, fmt.Errorf("pipeline: log_source_id and source_generation are required")
	}
	key := ledger.SourceKey{
		LogSourceID:      opts.LogSourceID,
		SourceGeneration: opts.SourceGeneration,
	}
	src := logtypes.SourceIdentity{
		LogSourceID:      key.LogSourceID,
		SourceGeneration: key.SourceGeneration,
		ParserVersion:    normalize.ParserVersion,
	}
	led := opts.Ledger
	if led == nil {
		led = ledger.New()
	}
	wal := opts.WAL
	if wal == nil {
		wal = acquire.NewWAL(led, key)
	}

	stream := opts.Stream
	if stream == "" {
		stream = "stdout"
	}
	cat := opts.SourceCategory
	if cat == "" {
		cat = logtypes.SourceInstance
	}

	nopts := normalize.Options{
		Source:        src,
		Stream:        stream,
		IngestTimeUTC: opts.IngestTimeUTC,
		Location:      opts.Location,
		BaseTime:      opts.BaseTime,
		Limits:        opts.Limits,
	}
	bound := NewNormalizeBoundary(nopts)

	inner := acquire.NewPipeline(led, key, wal)
	inner.SetDurablePersist(opts.DurablePersist)
	if opts.Capacity != nil {
		inner.SetCapacityBudget(*opts.Capacity)
	}
	inner.SetCapacityProvider(opts.CapacityProvider)
	if opts.SuppressRecursiveVL || cat == logtypes.SourceWorker {
		inner.SetSourceWorker(true)
	}

	p := &Pipeline{
		mode:          mode,
		key:           key,
		led:           led,
		wal:           wal,
		inner:         inner,
		bound:         bound,
		hook:          opts.Delivery,
		reclaimProof:  opts.ReclaimProof,
		src:           src,
		normalizeOpts: nopts,
		reader:        opts.Reader,
		useNormalize:  true,
	}
	p.imp = acquire.NewArchiveImporter(led, key)

	if opts.Delivery != nil {
		inner.SetDeliver(func(events []logtypes.Event) (int, bool, error) {
			res, err := p.hook.Deliver(events)
			if err != nil {
				return 0, res.AckLost, err
			}
			p.deliveredCount += len(events)
			return res.HTTPStatus, res.AckLost, nil
		})
	}

	switch mode {
	case ModeFilePrimary:
		if opts.Path == "" {
			return nil, fmt.Errorf("pipeline: FILE_PRIMARY requires Path")
		}
		tailer := acquire.NewFileTailer(led, key, opts.Path, wal, bound)
		tailer.SetSourceCategory(cat)
		if opts.RotateTo != "" {
			tailer.SetRotateTarget(opts.RotateTo)
		}
		p.tailer = tailer
	case ModeStdioPrimary:
		if opts.Path != "" {
			// A managed Raw spool is durable before acquisition. Reuse the file
			// cursor machinery while retaining STDIO_PRIMARY source semantics.
			p.tailer = acquire.NewFileTailer(led, key, opts.Path, wal, bound)
			p.tailer.SetSourceCategory(cat)
			break
		}
		if opts.Reader == nil {
			return nil, fmt.Errorf("pipeline: STDIO_PRIMARY requires Reader")
		}
		_ = led.RegisterSegment(key, ledger.Segment{
			Path:          "stdio://managed-raw",
			Kind:          ledger.SegmentStdio,
			ParserVersion: src.ParserVersion,
		})
	}
	return p, nil
}

// Mode 返回当前采集模式。
func (p *Pipeline) Mode() AcquireMode { return p.mode }

// Key 返回账本源键。
func (p *Pipeline) Key() ledger.SourceKey { return p.key }

// Ledger 返回采集账本。
func (p *Pipeline) Ledger() *ledger.Ledger { return p.led }

// WAL 返回本地 WAL。
func (p *Pipeline) WAL() *acquire.WAL { return p.wal }

// Boundary 返回 normalize 边界钩子。
func (p *Pipeline) Boundary() *NormalizeBoundary { return p.bound }

// FileTailer 返回 FILE_PRIMARY tailer（STDIO 模式为 nil）。
func (p *Pipeline) FileTailer() *acquire.FileTailer { return p.tailer }

// DeliveredCount 返回累计已进入 DeliveryHook 的事件数（含成功记账）。
//
// 只保留计数而不保留事件内容：后者会随会话内总事件数线性增长却无人读取内容
// （FR-484 阶段5 实测 64 源 ≈36MB）。若将来确有读取事件内容的需求，应先明确
// 保留窗口与释放时机，不要恢复无界切片。
func (p *Pipeline) DeliveredCount() int { return p.deliveredCount }

// EventsEmitted 返回 normalize 边界累计事件数。
func (p *Pipeline) EventsEmitted() int { return p.bound.EventsEmitted() }

// SetRotateTarget 更新轮转目标。
func (p *Pipeline) SetRotateTarget(path string) {
	if p.tailer != nil {
		p.tailer.SetRotateTarget(path)
	}
}

func (p *Pipeline) PrepareRotation() (bool, error) {
	if p == nil || p.tailer == nil {
		return false, nil
	}
	return p.tailer.PrepareRotation()
}

func (p *Pipeline) CurrentSegmentUnread() bool {
	return p != nil && p.tailer != nil && p.tailer.CurrentSegmentUnread()
}

func (p *Pipeline) RebaseCurrentSegment(pos uint64) error {
	if p == nil || p.tailer == nil {
		return fmt.Errorf("pipeline: file tailer not configured")
	}
	return p.tailer.RebaseCurrentSegment(pos)
}

// FlushClosedSegment closes the pending multiline event when a live file was
// physically rotated. Unlike Stop/Flush, the old segment has a real EOF and may
// be published as complete before ArchiveImporter skips its covered prefix.
func (p *Pipeline) FlushClosedSegment() ([]logtypes.Event, error) {
	if p == nil || p.bound == nil {
		return nil, nil
	}
	p.bound.FlushComplete()
	events := p.bound.DrainEvents()
	if len(events) == 0 {
		return nil, nil
	}
	if err := p.ingest(events); err != nil {
		return events, err
	}
	return events, nil
}

func (p *Pipeline) ConfirmRotationCoverage() error {
	if p == nil || p.tailer == nil {
		return fmt.Errorf("pipeline: file tailer not configured")
	}
	return p.tailer.ConfirmRotationCoverage()
}

// Poll 读取增量：FILE_PRIMARY tail / STDIO 行流 → normalize 事件 → WAL → delivery。
// 返回本批已入 WAL 的完整事件（不含仍在缓冲的半条）。
func (p *Pipeline) Poll() ([]logtypes.Event, error) {
	switch p.mode {
	case ModeFilePrimary:
		return p.pollFile()
	case ModeStdioPrimary:
		if p.tailer != nil {
			return p.pollFile()
		}
		return p.pollStdio()
	default:
		return nil, fmt.Errorf("pipeline: unsupported mode %q", p.mode)
	}
}

func (p *Pipeline) pollFile() ([]logtypes.Event, error) {
	if p.tailer == nil {
		return nil, fmt.Errorf("pipeline: file tailer not configured")
	}
	// tailer 用 NormalizeBoundary：complete 时 emit=false，事件由 boundary 持有。
	if _, err := p.tailer.Poll(); err != nil {
		// 仍尝试冲刷已完整事件。
		evs := p.bound.DrainEvents()
		if len(evs) > 0 {
			_ = p.ingest(evs)
		}
		return evs, err
	}
	events := p.bound.DrainEvents()
	if len(events) == 0 {
		return nil, nil
	}
	if err := p.ingest(events); err != nil {
		return events, err
	}
	return events, nil
}

func (p *Pipeline) pollStdio() ([]logtypes.Event, error) {
	if p.reader == nil {
		return nil, fmt.Errorf("pipeline: stdio reader not configured")
	}
	sc := bufio.NewScanner(p.reader)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		abs := p.stdioRead
		end, _, _ := p.bound.Feed(line, abs, abs+uint64(len(line))+1)
		p.stdioRead = end
		// complete 与否：事件都在 boundary；stdio 只推进 read 水位。
		_ = p.led.AdvanceRead(p.key, end)
	}
	if err := sc.Err(); err != nil && err != io.EOF {
		_ = p.led.RecordGap(p.key, p.stdioRead, p.stdioRead, "STDIO_READ_ERROR", err.Error())
		return p.bound.DrainEvents(), err
	}
	batch := p.bound.DrainEvents()
	if len(batch) == 0 {
		return nil, nil
	}
	if err := p.ingest(batch); err != nil {
		return batch, err
	}
	return batch, nil
}

// Flush 冲刷多行半条事件（崩溃/轮转前）。显式 PARTIAL/OK 状态，不静默丢。
func (p *Pipeline) Flush() ([]logtypes.Event, error) {
	// tailer.FlushPartial → boundary.FlushPartial → normalize.FlushPartial。
	if p.tailer != nil {
		_, _, _ = p.tailer.FlushPartial()
	} else {
		_, _, _ = p.bound.FlushPartial()
	}
	events := p.bound.DrainEvents()
	if len(events) == 0 {
		return nil, nil
	}
	if err := p.ingest(events); err != nil {
		return events, err
	}
	return events, nil
}

// Drain Poll + Flush，一次取完当前可见完整事件。
func (p *Pipeline) Drain() ([]logtypes.Event, error) {
	polled, err := p.Poll()
	flushed, ferr := p.Flush()
	out := append(append([]logtypes.Event{}, polled...), flushed...)
	if err != nil {
		return out, err
	}
	return out, ferr
}

// ingest WAL append → Commit → DeliveryHook。容量/缺口由 acquire.Pipeline 处理。
func (p *Pipeline) ingest(events []logtypes.Event) error {
	if err := p.inner.Ingest(events); err != nil {
		return err
	}
	if p.DeliveryState() == logtypes.DeliveryUnknown {
		// An UNKNOWN request cannot be reclaimed from HTTP status alone. The
		// recovery path must first reconcile it from a durable projection.
		return nil
	}
	if p.reclaimProof != nil {
		return p.reclaimProof(events)
	}
	return nil
}

// ResolveUnknownThroughProjection keeps the UNKNOWN audit fact while advancing
// the delivery prefix after a persisted canonical projection was verified.
func (p *Pipeline) ResolveUnknownThroughProjection(events []logtypes.Event) error {
	if p == nil || len(events) == 0 {
		return nil
	}
	return p.led.ResolveDeliveryThroughRecovery(p.key, events[0].Record.Start, events[len(events)-1].Record.End)
}

// ImportArchive 导入 gzip 归档；已关联轮转时跳过整包，禁止双计。
func (p *Pipeline) ImportArchive(path string) (*acquire.ImportResult, error) {
	result, err := p.imp.ImportGzip(path)
	if err != nil || result == nil || result.Skipped || len(result.Events) == 0 {
		return result, err
	}
	normalizeOpts := p.normalizeOpts
	if day, dayErr := archiveUTCDay(path); dayErr == nil {
		normalizeOpts.BaseTime = day.Add(12 * time.Hour)
	}
	boundary := NewNormalizeBoundary(normalizeOpts)
	for _, raw := range result.Events {
		boundary.Feed([]byte(raw.Message), raw.Record.Start, raw.Record.End)
	}
	boundary.FlushComplete()
	events := boundary.DrainEvents()
	for i := range events {
		if events[i].Fields == nil {
			events[i].Fields = make(map[string]string)
		}
		events[i].Fields["archive_object_id"] = result.ArchiveObjectID
	}
	result.Events = events
	result.ImportedCount = len(events)
	ingestErr := p.ingest(events)
	pos, ok := p.Positions()
	if ok && len(events) > 0 && pos.Durable >= events[len(events)-1].Record.End {
		markErr := p.imp.MarkImported(path, result.ArchiveObjectID,
			events[0].Record.Start, events[len(events)-1].Record.End)
		if ingestErr == nil {
			ingestErr = markErr
		}
	}
	return result, ingestErr
}

func archiveUTCDay(path string) (time.Time, error) {
	name := filepath.Base(path)
	if len(name) < len("2006-01-02") {
		return time.Time{}, fmt.Errorf("pipeline: archive filename has no UTC day")
	}
	day := name[:len("2006-01-02")]
	if strings.Count(day, "-") != 2 {
		return time.Time{}, fmt.Errorf("pipeline: archive filename has no UTC day")
	}
	return time.ParseInLocation("2006-01-02", day, time.UTC)
}

// SetDelivery 运行时替换投递钩子。
func (p *Pipeline) SetDelivery(h DeliveryHook) {
	p.hook = h
	p.inner.SetDeliver(func(events []logtypes.Event) (int, bool, error) {
		if h == nil {
			return 0, false, nil
		}
		res, err := h.Deliver(events)
		if err != nil {
			return 0, res.AckLost, err
		}
		p.deliveredCount += len(events)
		return res.HTTPStatus, res.AckLost, nil
	})
}

// TryReclaim 经 logtypes.CanReclaim 门禁推进 reclaim_position。
// HTTP 2xx / REQUEST_DONE 单独不得回收；需恢复分段责任已转移且无 hold。
func (p *Pipeline) TryReclaim() (uint64, error) {
	return p.wal.TryReclaim()
}

// CanReclaimNow 按当前账本恢复分段状态评估是否允许回收。
func (p *Pipeline) CanReclaimNow() bool {
	ent := p.led.Get(p.key)
	if ent == nil {
		return false
	}
	for _, ref := range ent.RecoveryRefs {
		if ref.CoversFrom <= ent.Positions.Reclaim && ref.CoversTo > ent.Positions.Reclaim {
			return logtypes.CanReclaim(ent.Positions, ref.State, ref.ReleaseReason, ref.HasHold)
		}
	}
	// 无覆盖 reclaim 前缀的恢复分段：不得回收。
	return false
}

// BindRecoverySegment 登记受管恢复分段（STAGED）。
func (p *Pipeline) BindRecoverySegment(segID, path string, coversFrom, coversTo uint64) error {
	return p.wal.BindRecoverySegment(segID, path, coversFrom, coversTo)
}

// TransitionRecovery 推进恢复分段责任状态。
func (p *Pipeline) TransitionRecovery(segID string, to logtypes.RecoverySegmentState, reason logtypes.ReleaseReason, receiver string) error {
	return p.led.TransitionRecovery(p.key, segID, to, reason, receiver)
}

func (p *Pipeline) RecoveryRef(segID string) (ledger.RecoveryRef, bool) {
	entry := p.led.Get(p.key)
	if entry == nil {
		return ledger.RecoveryRef{}, false
	}
	for _, ref := range entry.RecoveryRefs {
		if ref.SegmentID == segID {
			return ref, true
		}
	}
	return ledger.RecoveryRef{}, false
}

func (p *Pipeline) RecoveryCovering(from, to uint64) (ledger.RecoveryRef, bool) {
	entry := p.led.Get(p.key)
	if entry == nil {
		return ledger.RecoveryRef{}, false
	}
	for _, ref := range entry.RecoveryRefs {
		if ref.State != logtypes.RecoveryCleaned && ref.CoversFrom <= from && ref.CoversTo >= to {
			return ref, true
		}
	}
	return ledger.RecoveryRef{}, false
}

func (p *Pipeline) SetRecoveryHold(segID string, hold bool) error {
	return p.led.SetRecoveryHold(p.key, segID, hold)
}

// Gaps 返回账本缺口（容量暂停/坏记录/投递失败可见）。
func (p *Pipeline) Gaps() []ledger.Gap {
	ent := p.led.Get(p.key)
	if ent == nil {
		return nil
	}
	return ent.Gaps
}

// Positions 返回四水位。
func (p *Pipeline) Positions() (logtypes.Positions, bool) {
	ent := p.led.Get(p.key)
	if ent == nil {
		return logtypes.Positions{}, false
	}
	return ent.Positions, true
}

// DeliveryState 返回最近批次投递状态。
func (p *Pipeline) DeliveryState() logtypes.DeliveryState {
	ent := p.led.Get(p.key)
	if ent == nil {
		return logtypes.DeliveryNotSent
	}
	return ent.DeliveryState
}

// Stats 返回 normalize 统计（事件数与行数分列）。
func (p *Pipeline) Stats() normalize.Stats { return p.bound.Stats() }

// EnsurePathFile 便捷：校验 FILE_PRIMARY 路径存在。
func EnsurePathFile(path string) error {
	if path == "" {
		return fmt.Errorf("pipeline: empty path")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("pipeline: stat %s: %w", path, err)
	}
	return nil
}
