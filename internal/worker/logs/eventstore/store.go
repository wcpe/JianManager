// Package eventstore 提供 Worker 日志 canonical 事件体的**追加式磁盘段存储**。
//
// 背景（FR-484）：事件体原先作为 `persistedSource.Events` 常驻内存并随 ingest.state.json 整份
// 重写。真机 64 源实测 state 文件 180MB，且单次 persist 的 MarshalIndent 瞬时分配把 Go 堆高水位
// 顶到 ≈656MiB heapInuse 且 sys 不归还 OS（见 .tmp/fr444-experiments/phase0/attribution.txt）。
//
// 设计约束（均来自契约）：
//   - 事件体**一个不丢**：本包只改变存放介质，不改变 canonical 集合语义（契约 §4.3/§5.3）。
//   - 提交顺序：先写入事件体并 fsync，再让调用方发布引用它的元数据。调用方不得反序。
//   - 只容忍**最后一段的尾部截断**（进程被杀/断电留下的半行）；中间段损坏 = 硬失败，
//     绝不静默猜测——宁可不启动采集，也不能用残缺集合重建投影。
package eventstore

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// DefaultSegmentBytes 单段目标字节数。写满即封段新建，保证单段可控、便于将来按段丢弃。
const DefaultSegmentBytes int64 = 8 << 20

const (
	segmentPrefix = "seg-"
	segmentSuffix = ".ndjson"
	manifestName  = "MANIFEST.json"
	manifestVer   = 1
)

// ErrCorrupt 表示段内容损坏（非尾部截断），调用方必须硬失败而不是继续。
var ErrCorrupt = errors.New("eventstore: corrupt segment")

// ErrReadOnly 表示只读取证实例拒绝任何追加或持久修复。
var ErrReadOnly = errors.New("eventstore: read-only store")

// SegmentMeta 描述一个已封闭/在写的段。只存元数据，不持事件体。
type SegmentMeta struct {
	Seq     int    `json:"seq"`
	Name    string `json:"name"`
	Count   int    `json:"count"`
	Bytes   int64  `json:"bytes"`
	MinEnd  uint64 `json:"min_end"`
	MaxEnd  uint64 `json:"max_end"`
	Sealed  bool   `json:"sealed"`
	FirstID string `json:"first_event_id,omitempty"`
	LastID  string `json:"last_event_id,omitempty"`
}

// Manifest 是段清单。与段内容一起构成可校验的持久结构。
// Key 保存原始源键；旧目录有损净化、新目录使用稳定摘要，两者都不能反推原始键。
type Manifest struct {
	Version  int           `json:"version"`
	Key      string        `json:"key"`
	Segments []SegmentMeta `json:"segments"`
}

// Store 管理多个 source key 的事件段。并发安全。
type Store struct {
	// 全局锁只保护源映射与配置，最长为关闭时复制源指针，不跨文件操作或源写锁。
	mu       sync.Mutex
	root     string
	maxSeg   int64
	byKey    map[string]*section
	closed   bool
	readOnly bool
}

type section struct {
	// 锁序为源写锁到清单锁；写锁可跨本源校准与耐久写入，但不跨全局锁或任何回调。
	mu          sync.Mutex
	dir         string
	manifest    Manifest
	initialized bool
	closed      bool
	readOnly    bool
	writeErr    error
	// 工作清单只归写锁所有；读侧只在短锁内复制成功发布的清单，不能看见在途追加。
	viewMu     sync.RWMutex
	committed  Manifest
	verified   bool
	active     *os.File
	activeMeta *SegmentMeta
}

// Open 打开（必要时创建）事件体根目录并载入各源清单。
func Open(root string) (*Store, error) {
	return openStore(root, false)
}

// OpenReadOnly 只读取证已存在的根；不创建目录，不截断尾部，也不发布校准清单。
// 只读实例固定本次打开的源清单，并以实际完整行校验前缀；不能用于追加或移交恢复责任。
func OpenReadOnly(root string) (*Store, error) {
	return openStore(root, true)
}

func openStore(root string, readOnly bool) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("eventstore: root is required")
	}
	if !readOnly {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return nil, fmt.Errorf("eventstore: create root: %w", err)
		}
	}
	s := &Store{root: root, maxSeg: DefaultSegmentBytes, byKey: map[string]*section{}, readOnly: readOnly}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("eventstore: read root: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		mf, err := loadManifest(dir)
		if err != nil {
			return nil, err
		}
		// 空目录没有源身份，不从有损目录名猜测原始键。
		if mf.Key == "" {
			continue
		}
		if isDigestDirName(e.Name()) && e.Name() != sectionDirName(mf.Key) {
			return nil, fmt.Errorf("%w: manifest key does not match directory %s", ErrCorrupt, dir)
		}
		if _, duplicate := s.byKey[mf.Key]; duplicate {
			return nil, fmt.Errorf("%w: duplicate source key %q", ErrCorrupt, mf.Key)
		}
		s.byKey[mf.Key] = &section{dir: dir, manifest: cloneManifest(mf), committed: cloneManifest(mf), initialized: true, readOnly: readOnly}
	}
	return s, nil
}

// SetMaxSegmentBytes 覆盖段大小（测试用）。
func (s *Store) SetMaxSegmentBytes(n int64) {
	if n <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxSeg = n
}

func sectionDirName(key string) string {
	digest := sha256.Sum256([]byte(key))
	return "source-" + hex.EncodeToString(digest[:])
}

func isDigestDirName(name string) bool {
	if !strings.HasPrefix(name, "source-") || len(name) != len("source-")+sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(name, "source-"))
	return err == nil
}

func cloneManifest(mf Manifest) Manifest {
	mf.Segments = append([]SegmentMeta(nil), mf.Segments...)
	return mf
}

func loadManifest(dir string) (Manifest, error) {
	var mf Manifest
	entries, err := os.ReadDir(dir)
	if err != nil {
		return mf, fmt.Errorf("eventstore: read section %s: %w", dir, err)
	}
	physical := make(map[string]bool)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), segmentPrefix) && strings.HasSuffix(entry.Name(), segmentSuffix) {
			if entry.IsDir() {
				return mf, fmt.Errorf("%w: segment is a directory in %s", ErrCorrupt, dir)
			}
			physical[entry.Name()] = true
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, manifestName))
	if os.IsNotExist(err) && len(physical) == 0 {
		return mf, nil
	}
	if err != nil {
		return mf, fmt.Errorf("%w: read manifest %s: %w", ErrCorrupt, dir, err)
	}
	if len(b) == 0 {
		return mf, fmt.Errorf("%w: empty manifest in %s", ErrCorrupt, dir)
	}
	if err := json.Unmarshal(b, &mf); err != nil {
		return mf, fmt.Errorf("%w: decode manifest %s: %w", ErrCorrupt, dir, err)
	}
	if mf.Version != manifestVer {
		return mf, fmt.Errorf("%w: unsupported manifest version %d in %s", ErrCorrupt, mf.Version, dir)
	}
	if mf.Key == "" {
		return mf, fmt.Errorf("%w: manifest has no source key in %s", ErrCorrupt, dir)
	}
	mf.SortSegments()
	seenSeq := make(map[int]bool, len(mf.Segments))
	for i, meta := range mf.Segments {
		if meta.Seq != i+1 || seenSeq[meta.Seq] || meta.Count < 0 || meta.Bytes < 0 ||
			meta.Name != fmt.Sprintf("%s%06d%s", segmentPrefix, meta.Seq, segmentSuffix) ||
			(i < len(mf.Segments)-1 && !meta.Sealed) {
			return mf, fmt.Errorf("%w: invalid segment metadata in %s", ErrCorrupt, dir)
		}
		seenSeq[meta.Seq] = true
		if !physical[meta.Name] {
			return mf, fmt.Errorf("%w: missing segment %s in %s", ErrCorrupt, meta.Name, dir)
		}
		delete(physical, meta.Name)
	}
	if len(physical) != 0 {
		return mf, fmt.Errorf("%w: unlisted durable segment in %s", ErrCorrupt, dir)
	}
	return mf, nil
}

// calibrateLocked 首次校验完整段，只修复未封末段在真实文件末尾的半行与清单滞后。
// 完整坏行及非文件末尾读取错误不能成为缩短权威集合的理由。
func (sec *section) calibrateLocked() error {
	return sec.calibrateLockedContext(context.Background())
}

func (sec *section) calibrateLockedContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sec.viewMu.RLock()
	verified := sec.verified
	sec.viewMu.RUnlock()
	if verified {
		return nil
	}
	if sec.readOnly {
		return sec.verifyReadOnlyLocked(ctx)
	}
	if !sec.initialized {
		if err := os.MkdirAll(sec.dir, 0o700); err != nil {
			return fmt.Errorf("eventstore: create section: %w", err)
		}
		mf, err := loadManifest(sec.dir)
		if err != nil {
			return err
		}
		if mf.Key != "" && mf.Key != sec.manifest.Key {
			return fmt.Errorf("%w: existing directory belongs to %q", ErrCorrupt, mf.Key)
		}
		if mf.Key != "" {
			sec.manifest = mf
		} else if err := sec.writeManifestLocked(); err != nil {
			return err
		}
		sec.initialized = true
	}
	candidate := cloneManifest(sec.manifest)
	changed := false
	for i, meta := range candidate.Segments {
		last := i == len(candidate.Segments)-1
		actual, err := calibrateSegment(ctx, sec.dir, meta, last)
		if err != nil {
			return err
		}
		if actual != meta {
			if !last || meta.Sealed {
				return fmt.Errorf("%w: sealed segment metadata mismatch in %s", ErrCorrupt, meta.Name)
			}
			candidate.Segments[i] = actual
			changed = true
		}
	}
	previous := sec.manifest
	sec.manifest = candidate
	if changed {
		if err := sec.writeManifestLocked(); err != nil {
			sec.manifest = previous
			return err
		}
	}
	sec.viewMu.Lock()
	sec.committed = cloneManifest(candidate)
	sec.verified = true
	sec.viewMu.Unlock()
	return nil
}

func (s *Store) section(key string, create bool) (*section, error) {
	sec, ok := s.byKey[key]
	if ok {
		return sec, nil
	}
	if !create {
		return nil, fmt.Errorf("eventstore: unknown source %q", key)
	}
	if key == "" {
		return nil, fmt.Errorf("eventstore: source key is required")
	}
	dir := filepath.Join(s.root, sectionDirName(key))
	mf := Manifest{Version: manifestVer, Key: key}
	sec = &section{dir: dir, manifest: mf, committed: cloneManifest(mf)}
	s.byKey[key] = sec
	return sec, nil
}

// Append 追加事件到该源的活跃段；必要时封段。整批完成后 fsync 一次。
// 调用方必须在元数据引用这些事件**之前**成功返回，且返回前不能并发修改输入事件或字段映射。
func (s *Store) Append(key string, events []logtypes.Event) error {
	if s.readOnly {
		return ErrReadOnly
	}
	if len(events) == 0 {
		return nil
	}
	if err := validateAppendEvents(events); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return os.ErrClosed
	}
	sec, err := s.section(key, true)
	maxSeg := s.maxSeg
	s.mu.Unlock()
	if err != nil {
		return err
	}
	sec.mu.Lock()
	defer sec.mu.Unlock()
	if sec.closed {
		return os.ErrClosed
	}
	if sec.writeErr != nil {
		return fmt.Errorf("eventstore: reopen required after failed append: %w", sec.writeErr)
	}
	if err := sec.calibrateLocked(); err != nil {
		return err
	}
	if err := sec.appendLocked(events, maxSeg); err != nil {
		// 失败批次可以已写到磁盘，但本进程读侧只能使用先前成功清单；重开后再校准恢复。
		sec.writeErr = err
		return err
	}
	return nil
}

func (sec *section) appendLocked(events []logtypes.Event, maxSeg int64) error {
	if err := sec.openActive(maxSeg); err != nil {
		return err
	}
	w := bufio.NewWriterSize(sec.active, 256<<10)
	for _, ev := range events {
		data, err := json.Marshal(ev)
		if err != nil {
			return fmt.Errorf("eventstore: marshal event: %w", err)
		}
		if len(data)+1 > maxEventLineBytes {
			return fmt.Errorf("eventstore: event exceeds %d-byte line limit", maxEventLineBytes)
		}
		if _, err := w.Write(data); err != nil {
			return fmt.Errorf("eventstore: write segment: %w", err)
		}
		if err := w.WriteByte('\n'); err != nil {
			return fmt.Errorf("eventstore: write segment: %w", err)
		}
		addEventMeta(sec.activeMeta, ev)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("eventstore: flush segment: %w", err)
	}
	if err := sec.active.Sync(); err != nil {
		return fmt.Errorf("eventstore: fsync segment: %w", err)
	}
	st, err := sec.active.Stat()
	if err != nil {
		return fmt.Errorf("eventstore: stat segment: %w", err)
	}
	sec.activeMeta.Bytes = st.Size()
	sec.upsertMetaLocked()
	if sec.activeMeta.Bytes >= maxSeg {
		if err := sec.sealLocked(); err != nil {
			return err
		}
	}
	return sec.writeManifestLocked()
}

// Iterate 按一次固定快照流式读回完整事件，回调可重入本存储；返回错误即中止。
func (s *Store) Iterate(key string, fn func(logtypes.Event) error) (result error) {
	snapshot, err := s.Snapshot(key)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := snapshot.Close(); closeErr != nil {
			result = errors.Join(result, closeErr)
		}
	}()
	return snapshot.Iterate(fn)
}

// Count 返回成功提交且经首次校准的完整事件数，不把在途追加的元数据当作权威。
// 需要计数与后续遍历严格同集合时，应复用同一 ReadSnapshot。
func (s *Store) Count(key string) (int, error) {
	sec := s.lookup(key)
	if sec == nil {
		return 0, nil
	}
	if err := sec.ensureVerified(); err != nil {
		return 0, err
	}
	sec.viewMu.RLock()
	defer sec.viewMu.RUnlock()
	n := 0
	for _, meta := range sec.committed.Segments {
		n += meta.Count
	}
	return n, nil
}

func (s *Store) lookup(key string) *section {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byKey[key]
}

func (sec *section) ensureVerified() error {
	return sec.ensureVerifiedContext(context.Background())
}

func (sec *section) ensureVerifiedContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sec.viewMu.RLock()
	verified := sec.verified
	sec.viewMu.RUnlock()
	if verified {
		return nil
	}
	sec.mu.Lock()
	defer sec.mu.Unlock()
	return sec.calibrateLockedContext(ctx)
}

// Segments 返回最近成功提交的元数据副本，不触发扫描或暴露在途写入。
func (s *Store) Segments(key string) []SegmentMeta {
	sec := s.lookup(key)
	if sec == nil {
		return nil
	}
	sec.viewMu.RLock()
	defer sec.viewMu.RUnlock()
	return append([]SegmentMeta(nil), sec.committed.Segments...)
}

// DayMaxEnd 按 UTC 日聚合「该日最大 record_end」，流式读取、不materialize全量事件。
// dayOf 把事件映射到 UTC 日（由调用方注入，与 ingest 的 eventUTCDay 保持同一策略）。
//
// 用途：`publishedClosedForSource` 需要「每个受影响分区都已封闭」的判定，只需要每日本身的
// 最大末端位置，不需要事件正文——这正是能从磁盘段直接算出的量。
func (s *Store) DayMaxEnd(key string, dayOf func(logtypes.Event) (string, error)) (map[string]uint64, error) {
	// ctx 形态是唯一实现（见 DayMaxEndCtx）：此处传 Background，保持既有签名与语义。
	return s.DayMaxEndCtx(context.Background(), key, dayOf)
}

// DayMaxEndCtx 是 DayMaxEnd 的**可取消**形态：ctx 在逐行回调处校验，取消即中止段扫描并返回
// ctx 错误（不是「空结果」——那会被上层的「投影不完整」吞掉，把取消伪装成一次普通失败）。
//
// 为什么必须可取消：段扫描的成本随源数据量增长（生产单源段达数十万行、全库 18GB），而本函数的
// 调用方（ingest 的整节点解算）是一个会被 HTTP/RPC 截止时间打断的显式运维动作。不可取消即意味着
// 「调用方已经放弃等待，服务端还在为没人要的响应扫盘」——这正是 2026-10-02 现场 30 分钟停摆的形态。
func (s *Store) DayMaxEndCtx(ctx context.Context, key string, dayOf func(logtypes.Event) (string, error)) (out map[string]uint64, result error) {
	if ctx == nil {
		ctx = context.Background()
	}
	snapshot, err := s.snapshotContext(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := snapshot.Close(); closeErr != nil {
			out = nil
			result = errors.Join(result, closeErr)
		}
	}()
	out = map[string]uint64{}
	err = snapshot.iterateContext(ctx, func(ev logtypes.Event) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		day, err := dayOf(ev)
		if err != nil {
			return err
		}
		if ev.Record.End > out[day] {
			out[day] = ev.Record.End
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Close 关闭活跃段句柄。
func (s *Store) Close() error {
	s.mu.Lock()
	s.closed = true
	sections := make([]*section, 0, len(s.byKey))
	for _, sec := range s.byKey {
		sections = append(sections, sec)
	}
	s.mu.Unlock()
	var firstErr error
	for _, sec := range sections {
		sec.mu.Lock()
		sec.closed = true
		if sec.active != nil {
			if err := sec.active.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
			sec.active = nil
		}
		sec.mu.Unlock()
	}
	return firstErr
}

func (sec *section) openActive(maxSeg int64) error {
	if sec.active != nil {
		return nil
	}
	// 复用未封段的最后一段；否则新建。
	if len(sec.manifest.Segments) > 0 {
		last := &sec.manifest.Segments[len(sec.manifest.Segments)-1]
		if !last.Sealed {
			f, err := os.OpenFile(filepath.Join(sec.dir, last.Name), os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return fmt.Errorf("eventstore: open active segment: %w", err)
			}
			sec.active = f
			sec.activeMeta = last
			return nil
		}
	}
	name := fmt.Sprintf("%s%06d%s", segmentPrefix, len(sec.manifest.Segments)+1, segmentSuffix)
	f, err := os.OpenFile(filepath.Join(sec.dir, name), os.O_CREATE|os.O_EXCL|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("eventstore: create segment: %w", err)
	}
	sec.active = f
	sec.manifest.Segments = append(sec.manifest.Segments, SegmentMeta{Seq: len(sec.manifest.Segments) + 1, Name: name})
	sec.activeMeta = &sec.manifest.Segments[len(sec.manifest.Segments)-1]
	_ = maxSeg
	return nil
}

func (sec *section) upsertMetaLocked() {
	if sec.activeMeta == nil {
		return
	}
	for i := range sec.manifest.Segments {
		if sec.manifest.Segments[i].Seq == sec.activeMeta.Seq {
			sec.manifest.Segments[i] = *sec.activeMeta
			sec.activeMeta = &sec.manifest.Segments[i]
			return
		}
	}
}

func (sec *section) sealLocked() error {
	if sec.activeMeta == nil {
		return nil
	}
	if sec.active != nil {
		if err := sec.active.Close(); err != nil {
			return fmt.Errorf("eventstore: close sealed segment: %w", err)
		}
		sec.active = nil
	}
	sec.activeMeta.Sealed = true
	sec.upsertMetaLocked()
	sec.activeMeta = nil
	return nil
}

func (sec *section) writeManifestLocked() error {
	data, err := json.MarshalIndent(sec.manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("eventstore: marshal manifest: %w", err)
	}
	tmp := filepath.Join(sec.dir, manifestName+".tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("eventstore: create manifest tmp: %w", err)
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("eventstore: write manifest: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(sec.dir, manifestName)); err != nil {
		return fmt.Errorf("eventstore: publish manifest: %w", err)
	}
	// 目录项也必须耐久；只有所有发布步骤成功后，才能向读侧提交清单。
	if err := syncDir(sec.dir); err != nil {
		return fmt.Errorf("eventstore: sync manifest directory: %w", err)
	}
	sec.viewMu.Lock()
	sec.committed = cloneManifest(sec.manifest)
	sec.viewMu.Unlock()
	return nil
}

// syncDir 先验证目录，再执行平台明确支持的目录同步；Windows 不承诺目录项断电耐久。
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	info, err := d.Stat()
	if err != nil {
		return errors.Join(err, d.Close())
	}
	if !info.IsDir() {
		return errors.Join(fmt.Errorf("eventstore: expected directory: %s", dir), d.Close())
	}
	return errors.Join(syncDirectoryHandle(d), d.Close())
}

// SortSegments 按 Seq 升序（载入后规整，防御清单被外部编辑）。
func (m *Manifest) SortSegments() {
	sort.Slice(m.Segments, func(i, j int) bool { return m.Segments[i].Seq < m.Segments[j].Seq })
}
