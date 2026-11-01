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
	"time"

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

// segmentFileName 是段文件的唯一命名来源：清单校验、追加建段、回收前移都必须走它，
// 避免「Seq 与文件名」的对应关系在多处各写一遍而分叉。
func segmentFileName(seq int) string {
	return fmt.Sprintf("%s%06d%s", segmentPrefix, seq, segmentSuffix)
}

// isSegmentName 报告名字是否落在段命名空间内（与 loadManifest 的物理段识别同一判据）。
func isSegmentName(name string) bool {
	return strings.HasPrefix(name, segmentPrefix) && strings.HasSuffix(name, segmentSuffix)
}

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
	// manifestSyncEvery / manifestSyncInterval 是新建源的清单落盘策略模板
	// （见 SetManifestSyncPolicy；默认零值 = 只在段集合变化时落盘）。
	manifestSyncEvery    int
	manifestSyncInterval time.Duration
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
	// liveSnapshots 是在途 ReadSnapshot 计数（受 viewMu 保护）。
	// 回收会前移段名，使已复制清单的读者手里那条路径失效，故有读者时一律不回收。
	liveSnapshots int
	// pruneActive 表示该源正在回收（受 viewMu 保护）：此刻清单与段名可能短暂不一致，
	// 新读者必须被挡住而不是拿到撕裂视图。
	pruneActive bool
	// manifestSyncEvery / manifestSyncInterval 是本源「清单落盘收敛」的两个上界
	// （≤0 表示该维度不设上界；两者都为 0 即「只在段集合变化时落盘」）。见 manifestPersistDueLocked。
	manifestSyncEvery    int
	manifestSyncInterval time.Duration
	// manifestDirtyAppends 是自上次清单落盘以来的追加次数（受 mu 保护）。
	manifestDirtyAppends int
	// manifestPersistedAt 是上次清单落盘完成的时刻（受 mu 保护；零值表示尚未落盘）。
	manifestPersistedAt time.Time
	// manifestDirty 表示内存清单有**未落盘**的变更（受 mu 保护）。
	// 它存在的意义：策略允许把「派生元数据」的落盘滞后，但**干净关闭必须收尾**——
	// 否则一次正常停机也会在磁盘上留下滞后清单，让运维看到的 MANIFEST.json 与实际段不符。
	manifestDirty bool
	// segmentCreated 表示本批追加新建了段（受 mu 保护）。段集合是**非派生**信息：
	// 清单与物理段不一致会让重启直接硬失败（unlisted durable segment），故它的落盘永不滞后。
	segmentCreated bool
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

// MemoryRoot 返回一个**在内存文件系统上**的事件段根目录（零磁盘 IO）。
//
// ## 为什么这样做而不是重写 eventstore（2026-10-30 用户裁定）
//
// 原话：「因为当前是机械盘不要做任何落盘的操作 只做最基础的」。
//
// eventstore 与文件系统的耦合很深（*os.File、MANIFEST、校准、回收前滚），
// 重写成纯内存实现风险远大于收益。而它的职责本来就是「投递前的暂存 + 投递后可回收」，
// 数据本身就是短命的——放到 tmpfs 上语义完全不变，只是写入不再碰到机械盘。
//
// 选择顺序：
//  1. /dev/shm（本机实测 126G tmpfs，纯内存）；
//  2. 退而使用调用方给的 fallback（通常是系统临时目录，也可能是 tmpfs）。
//
// 为什么不直接返回空用内存 map：那要重写 segment/manifest 整套，且会丢掉
// 「按段滚动 + 回收」的内存上界能力——一个源的无界增长仍然要有人管。
//
// ## 安全不变式：不同数据根绝不共享同一个段根（2026-11-02）
//
// 调用方必须传入**能唯一标识该数据根**的 base，而不是 `filepath.Base(opts.Root)`。
// 为什么这是个安全边界而不是命名习惯：段根下的段文件会被 `Prune` / 回收前滚 / 启动
// 时 `recoverPruneJournal(allowWrite=true)` 删除。只需要两个数据根的**末级目录名相同**
// （例如生产 `--data-dir /home/wxys233/jm/prod/worker/data` 与任意实验件 `.../data`
// 的 basename 都是 `data`），它们就会落到同一个 `/dev/shm/jianmanager-events/data`
// ——实验进程的回收会真删生产的事件段。这不是读数污染，是数据删除。
//
// 用 `SegmentRootFor(root)` 生成 base（本包提供，含绝对路径哈希）。
func MemoryRoot(base string) string {
	const shm = "/dev/shm"
	if fi, err := os.Stat(shm); err == nil && fi.IsDir() {
		return filepath.Join(shm, "jianmanager-events", base)
	}
	return filepath.Join(os.TempDir(), "jianmanager-events", base)
}

// SegmentRootFor 把一个**数据根绝对路径**映射成唯一的段根基名。
//
// 为什么不能只用 filepath.Base：basename 不唯一。生产与实验件都可叫 `data`，
// 而段根下的 Prune/回收前滚会删文件——共享段根等于互相删除对方的事件段。
//
// 构造：`<basename>-<绝对路径 sha256 前 12 位>`。保留可读的 basename 便于运维辨认
// （`ls /dev/shm/jianmanager-events/` 一眼能看出是谁），哈希尾缀保证唯一。
// 不可读路径（Getwd 失败、相对路径无法绝对化）退化为只保留已清洗的 basename，
// 此时仍优于原实现（至少消灭了「所有同名目录全撞」这个面）。
func SegmentRootFor(dataRoot string) string {
	cleaned := filepath.Clean(strings.TrimSpace(dataRoot))
	base := filepath.Base(cleaned)
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = "root"
	}
	abs, err := filepath.Abs(cleaned)
	if err != nil {
		abs = cleaned
	}
	// EvalSymlinks 尽力而为：符号链指向同一目录时应得到同一段根。
	if resolved, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		abs = resolved
	}
	sum := sha256.Sum256([]byte(abs))
	return base + "-" + hex.EncodeToString(sum[:])[:12]
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
		// 回收前滚必须早于清单载入：中途崩溃的目录对 loadManifest 而言是「清单与物理段不符」，
		// 先把它收敛到某一份完整清单再校验，才谈得上「失败即拒绝启动」。
		if err := recoverPruneJournal(dir, !readOnly); err != nil {
			return nil, err
		}
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
		s.byKey[mf.Key] = &section{dir: dir, manifest: cloneManifest(mf), committed: cloneManifest(mf), initialized: true, readOnly: readOnly,
			manifestSyncEvery: s.manifestSyncEvery, manifestSyncInterval: s.manifestSyncInterval}
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

// SetManifestSyncPolicy 设置「清单落盘收敛」的两个上界（0 表示该维度不设上界）。
//
// 背景（机械盘 IO 收敛，2026-xx）：追加路径原先**每批**都重写并 fsync 清单
// （`write tmp` → `fsync` → `rename` → `fsync(dir)`），叠加段数据自身的 fsync，一次
// `Append` 就是 3 次耐久调用。而清单里**未封末段**的 count/bytes/minEnd/maxEnd/首末身份
// 都是**可从段内容逐字段重算**的派生信息（`scanSegment`/`calibrateSegment`），因此
// 「清单暂时落后于实物」在重启时会被校准精确修复，不丢任何事件体。
//
// 语义边界（不降级安全性）：
//   - **段集合**变化（新建段、段被封印）永远**立即**落盘 —— 否则重启会看到
//     `unlisted durable segment`（不可接受）或把已封段当未封段；
//   - 未封末段的**派生元数据**允许滞后，由 `calibrateSegment` 在下次打开时按实物精确重建；
//   - 进程内读侧（快照/计数）**立即**看到新元数据（`publishManifestLocked`），不受本策略影响。
//
// every > 0 表示「最多滞后 every 次追加」；interval > 0 表示「最多滞后一个时间窗」。
// 两者都为 0（默认）= 只在段集合变化时落盘 —— 这是机械盘上的最优设置，且不丢数据。
func (s *Store) SetManifestSyncPolicy(every int, interval time.Duration) {
	if every < 0 {
		every = 0
	}
	if interval < 0 {
		interval = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manifestSyncEvery = every
	s.manifestSyncInterval = interval
	for _, sec := range s.byKey {
		sec.mu.Lock()
		sec.manifestSyncEvery = every
		sec.manifestSyncInterval = interval
		sec.mu.Unlock()
	}
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
			meta.Name != segmentFileName(meta.Seq) ||
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
	sec = &section{dir: dir, manifest: mf, committed: cloneManifest(mf),
		manifestSyncEvery: s.manifestSyncEvery, manifestSyncInterval: s.manifestSyncInterval}
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
	// 段数据 fsync：**不可省** —— 调用方（ingest）在元数据里引用这些事件之前，本批必须耐久。
	observeSync(obsSyncSegment)
	if err := sec.active.Sync(); err != nil {
		return fmt.Errorf("eventstore: fsync segment: %w", err)
	}
	st, err := sec.active.Stat()
	if err != nil {
		return fmt.Errorf("eventstore: stat segment: %w", err)
	}
	sec.activeMeta.Bytes = st.Size()
	sec.upsertMetaLocked()
	// setChanged：段集合（新建段 / 封印标记）发生变化 ⇒ 清单**必须**立即落盘，不可滞后。
	// 段集合是**非派生**信息：清单与物理段不一致会让重启直接硬失败（unlisted durable segment）。
	setChanged := sec.segmentCreated
	sec.segmentCreated = false
	if sec.activeMeta.Bytes >= maxSeg {
		if err := sec.sealLocked(); err != nil {
			return err
		}
		setChanged = true
	}
	sec.manifestDirty = true
	if setChanged || sec.manifestPersistDueLocked() {
		// 走持久化：清单发布只允许发生在**落盘成功之后**（失败不得把候选元数据泄给读侧，
		// 见 TestFailedManifestPublicationCannotLeakUncommittedMetadata）。
		return sec.persistManifestLocked()
	}
	// 清单滞后但**可校准修复**：未封末段的 count/bytes/首末身份/覆盖区间都是派生值，
	// 下次打开由 calibrateSegment 按段内容逐字段重算（事件体本身已 fsync，一个不丢）。
	// 但进程内读者不能滞后——否则重算差集时以为事件还不存在，会重复追加放大写入。
	sec.publishManifestLocked()
	return nil
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

// TestRoot 返回本 Store 实际使用的段根目录（**仅供测试**）。
//
// 为什么需要：2026-11-02（`28abb04d`）起段根由**数据根绝对路径派生**
// （`SegmentRootFor`），不再位于 `<数据根>/var/log/events`。测试若写死旧路径就会拿到
// `no such file or directory`——那测的不再是「段目录里的内容」，而是「段目录在哪」；
// 前者是需要钉的行为，后者不是（而且它本来就不该由测试假设）。
//
// 走这个入口而不是在测试里重新拼路径：段根算法只应该有一处实现，测试不应成为第二个副本
// （那会在下一次算法演进时无声地分叉）。
func (s *Store) TestRoot() string { return s.root }

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
		// 干净关闭**尽力**把滞后的清单收尾（见 manifestDirty 的说明）：停机后磁盘上的
		// MANIFEST.json 与实际段一致，运维与只读取证看到的才是真身，而不是「待校准」状态。
		//
		// 为什么这里的失败**不**传播给调用方：收尾只是一次便利性的收敛，不是正确性依赖——
		// 清单滞后本来就被「下次打开按段内容精确校准」兜住（与崩溃走同一条恢复路径）。
		// 而 Close 的既有契约是「关句柄」，若在清单路径损坏时新增一个关闭期错误，会让
		// Worker 停机流程凭空多出一种失败态，收益与代价不相称。
		if sec.manifestDirty && sec.initialized && !sec.readOnly {
			_ = sec.persistManifestLocked()
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
	name := segmentFileName(len(sec.manifest.Segments) + 1)
	f, err := os.OpenFile(filepath.Join(sec.dir, name), os.O_CREATE|os.O_EXCL|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("eventstore: create segment: %w", err)
	}
	sec.active = f
	sec.manifest.Segments = append(sec.manifest.Segments, SegmentMeta{Seq: len(sec.manifest.Segments) + 1, Name: name})
	sec.activeMeta = &sec.manifest.Segments[len(sec.manifest.Segments)-1]
	// 段集合变化**必须**在本批末尾落盘（见 appendLocked 的 setChanged）。
	// 不在这里提前落盘的理由：既有契约是「段数据 fsync 先于清单发布」，且崩溃恢复回归
	// （snapshot_recovery_test.go）显式验证「数据已耐久、清单发布失败」这一现场必须可观测。
	sec.segmentCreated = true
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

// writeManifestLocked 把清单**落盘**并在成功后**发布**给读侧。
//
// 保留这个名字（而不是让调用点直呼 persistManifestLocked）是为了让「段集合变化 / 校准修复 /
// 建库 / 回收前移」这些**必须耐久**的路径有一个语义明确、不会被误改成滞后写入的入口；
// 真正的落盘实现见 persistManifestLocked。
func (sec *section) writeManifestLocked() error {
	return sec.persistManifestLocked()
}

// publishManifestLocked 只把当前清单**发布**给读侧（快照/计数/按天覆盖），不做任何磁盘 IO。
//
// 与持久化分离的理由见 SetManifestSyncPolicy：清单里未封末段的派生元数据允许比实物滞后，
// 但**进程内读者不能滞后**——否则读者会认为刚追加的事件还不存在（重算差集后重复追加）。
func (sec *section) publishManifestLocked() {
	sec.viewMu.Lock()
	sec.committed = cloneManifest(sec.manifest)
	sec.viewMu.Unlock()
}

// manifestPersistDueLocked 判断「派生元数据滞后」是否已达配置上界（0/0 表示不设上界）。
func (sec *section) manifestPersistDueLocked() bool {
	if sec.manifestSyncEvery <= 0 && sec.manifestSyncInterval <= 0 {
		return false
	}
	sec.manifestDirtyAppends++
	if sec.manifestSyncEvery > 0 && sec.manifestDirtyAppends >= sec.manifestSyncEvery {
		return true
	}
	if sec.manifestSyncInterval > 0 {
		if sec.manifestPersistedAt.IsZero() || time.Since(sec.manifestPersistedAt) >= sec.manifestSyncInterval {
			return true
		}
	}
	return false
}

// persistManifestLocked 把清单原子落盘（tmp → fsync → rename → fsync(dir)），成功后发布给读侧。
//
// 一次调用含 **2 次耐久 syscall**（清单文件 fsync + 目录 fsync），因此调用点必须受
// SetManifestSyncPolicy / 段集合变化 的约束，不得在每次追加时无条件执行（机械盘每次
// fsync 都是一次寻道；实测本路径是采集持久化的主要 IO 放大源）。
func (sec *section) persistManifestLocked() error {
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
		observeSync(obsSyncManifestFile)
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
	sec.manifestDirtyAppends = 0
	sec.manifestPersistedAt = time.Now()
	sec.manifestDirty = false
	sec.publishManifestLocked()
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
	observeSync(obsSyncDirectory)
	return errors.Join(syncDirectoryHandle(d), d.Close())
}

// SortSegments 按 Seq 升序（载入后规整，防御清单被外部编辑）。
func (m *Manifest) SortSegments() {
	sort.Slice(m.Segments, func(i, j int) bool { return m.Segments[i].Seq < m.Segments[j].Seq })
}
