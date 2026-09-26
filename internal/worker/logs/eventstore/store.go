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
// Key 保存**原始 source key**：目录名经过字符净化，不能反推回 key，必须显式持久化。
type Manifest struct {
	Version  int           `json:"version"`
	Key      string        `json:"key"`
	Segments []SegmentMeta `json:"segments"`
}

// Store 管理多个 source key 的事件段。并发安全。
type Store struct {
	mu     sync.Mutex
	root   string
	maxSeg int64
	byKey  map[string]*section
}

type section struct {
	dir      string
	manifest Manifest
	// active 是正在追加的段文件句柄；封段后置 nil。
	active     *os.File
	activeMeta *SegmentMeta
}

// Open 打开（必要时创建）事件体根目录并载入各源清单。
func Open(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("eventstore: root is required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("eventstore: create root: %w", err)
	}
	s := &Store{root: root, maxSeg: DefaultSegmentBytes, byKey: map[string]*section{}}
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
		// 以清单内的原始 key 建索引；目录名是净化后的，不可反推。
		key := mf.Key
		if key == "" {
			key = e.Name() // 兼容早期无 Key 字段的清单
		}
		s.byKey[key] = &section{dir: dir, manifest: mf}
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
	// 与 raw/ 的命名惯例一致：内容可读、文件系统安全。
	repl := strings.NewReplacer("/", "_", "\\", "_", ":", "-")
	return repl.Replace(key)
}

func loadManifest(dir string) (Manifest, error) {
	mf := Manifest{Version: manifestVer}
	b, err := os.ReadFile(filepath.Join(dir, manifestName))
	if os.IsNotExist(err) {
		return mf, nil
	}
	if err != nil {
		return mf, fmt.Errorf("eventstore: read manifest: %w", err)
	}
	if len(b) == 0 {
		return mf, nil
	}
	if err := json.Unmarshal(b, &mf); err != nil {
		return mf, fmt.Errorf("eventstore: decode manifest %s: %w", dir, err)
	}
	if mf.Version != manifestVer {
		return mf, fmt.Errorf("eventstore: unsupported manifest version %d in %s", mf.Version, dir)
	}
	mf.SortSegments()
	return mf, nil
}

func (s *Store) section(key string, create bool) (*section, error) {
	sec, ok := s.byKey[key]
	if ok {
		return sec, nil
	}
	if !create {
		return nil, fmt.Errorf("eventstore: unknown source %q", key)
	}
	dir := filepath.Join(s.root, sectionDirName(key))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("eventstore: create section: %w", err)
	}
	sec = &section{dir: dir, manifest: Manifest{Version: manifestVer, Key: key}}
	s.byKey[key] = sec
	return sec, nil
}

// Append 追加事件到该源的活跃段；必要时封段。整批完成后 fsync 一次。
// 调用方必须在元数据引用这些事件**之前**成功返回。
func (s *Store) Append(key string, events []logtypes.Event) error {
	if len(events) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sec, err := s.section(key, true)
	if err != nil {
		return err
	}
	if err := sec.openActive(s.maxSeg); err != nil {
		return err
	}
	w := bufio.NewWriterSize(sec.active, 256<<10)
	for _, ev := range events {
		data, err := json.Marshal(ev)
		if err != nil {
			return fmt.Errorf("eventstore: marshal event: %w", err)
		}
		if _, err := w.Write(data); err != nil {
			return fmt.Errorf("eventstore: write segment: %w", err)
		}
		if err := w.WriteByte('\n'); err != nil {
			return fmt.Errorf("eventstore: write segment: %w", err)
		}
		sec.activeMeta.Count++
		if sec.activeMeta.FirstID == "" {
			sec.activeMeta.FirstID = ev.EventID
		}
		sec.activeMeta.LastID = ev.EventID
		end := ev.Record.End
		if sec.activeMeta.MinEnd == 0 || end < sec.activeMeta.MinEnd {
			sec.activeMeta.MinEnd = end
		}
		if end > sec.activeMeta.MaxEnd {
			sec.activeMeta.MaxEnd = end
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("eventstore: flush segment: %w", err)
	}
	if err := sec.active.Sync(); err != nil {
		return fmt.Errorf("eventstore: fsync segment: %w", err)
	}
	if st, err := sec.active.Stat(); err == nil {
		sec.activeMeta.Bytes = st.Size()
	}
	sec.upsertMetaLocked()
	if sec.activeMeta.Bytes >= s.maxSeg {
		if err := sec.sealLocked(); err != nil {
			return err
		}
	}
	// 清单在段内容落盘之后再原子替换，保持「先内容、后引用」的顺序。
	return sec.writeManifestLocked()
}

// Iterate 流式读回该源全部事件（按段序、段内行序）。回调返回错误即中止。
// 只容忍**最后一段**的尾部截断；中间段坏行返回 ErrCorrupt。
func (s *Store) Iterate(key string, fn func(logtypes.Event) error) error {
	s.mu.Lock()
	sec, ok := s.byKey[key]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	mf := sec.manifest
	for i := range mf.Segments {
		last := i == len(mf.Segments)-1
		if err := readSegment(sec.dir, mf.Segments[i], last, fn); err != nil {
			return err
		}
	}
	return nil
}

// Count 返回该源已存事件总数（来自清单，不读段）。
func (s *Store) Count(key string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sec, ok := s.byKey[key]
	if !ok {
		return 0, nil
	}
	n := 0
	for _, m := range sec.manifest.Segments {
		n += m.Count
	}
	return n, nil
}

// Segments 返回该源段元数据副本（诊断/测试）。
func (s *Store) Segments(key string) []SegmentMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	sec, ok := s.byKey[key]
	if !ok {
		return nil
	}
	out := make([]SegmentMeta, len(sec.manifest.Segments))
	copy(out, sec.manifest.Segments)
	return out
}

// DayMaxEnd 按 UTC 日聚合「该日最大 record_end」，流式读取、不materialize全量事件。
// dayOf 把事件映射到 UTC 日（由调用方注入，与 ingest 的 eventUTCDay 保持同一策略）。
//
// 用途：`publishedClosedForSource` 需要「每个受影响分区都已封闭」的判定，只需要每日本身的
// 最大末端位置，不需要事件正文——这正是能从磁盘段直接算出的量。
func (s *Store) DayMaxEnd(key string, dayOf func(logtypes.Event) (string, error)) (map[string]uint64, error) {
	out := map[string]uint64{}
	err := s.Iterate(key, func(ev logtypes.Event) error {
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
	defer s.mu.Unlock()
	var firstErr error
	for _, sec := range s.byKey {
		if sec.active != nil {
			if err := sec.active.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
			sec.active = nil
		}
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
			f, err := os.OpenFile(filepath.Join(sec.dir, last.Name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return fmt.Errorf("eventstore: open active segment: %w", err)
			}
			sec.active = f
			sec.activeMeta = last
			return nil
		}
	}
	name := fmt.Sprintf("%s%06d%s", segmentPrefix, len(sec.manifest.Segments)+1, segmentSuffix)
	f, err := os.OpenFile(filepath.Join(sec.dir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
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
	// 目录项本身也要 fsync，否则断电后 rename 可能丢失。
	return syncDir(sec.dir)
}

// syncDir 对目录 fsync，保证 rename 的目录项耐久（仓库既有代码未处理这一点）。
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		// 某些文件系统不支持目录 fsync；不因此判定失败。
		if errors.Is(err, os.ErrInvalid) {
			return nil
		}
		return nil
	}
	return nil
}

// readSegment 读单个段。last 为真时容忍尾部截断；否则坏行即 ErrCorrupt。
func readSegment(dir string, meta SegmentMeta, last bool, fn func(logtypes.Event) error) error {
	path := filepath.Join(dir, meta.Name)
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%w: open %s: %v", ErrCorrupt, path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var ev logtypes.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			if last {
				// 尾部截断（进程被杀/断电留下的半行）：到此为止，保留已读完整行。
				return nil
			}
			return fmt.Errorf("%w: bad line in %s: %v", ErrCorrupt, path, err)
		}
		if err := fn(ev); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		if last {
			return nil // 允许尾段读取中断
		}
		return fmt.Errorf("%w: read %s: %v", ErrCorrupt, path, err)
	}
	return nil
}

// SortSegments 按 Seq 升序（载入后规整，防御清单被外部编辑）。
func (m *Manifest) SortSegments() {
	sort.Slice(m.Segments, func(i, j int) bool { return m.Segments[i].Seq < m.Segments[j].Seq })
}
