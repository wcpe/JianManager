package eventstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// ReadSnapshot 固定清单、文件身份及完整字节边界，调用方必须关闭以释放逻辑读取引用。
// 本包只追加而不回收或替换既有段（唯一例外是显式调用的 Prune，见 prune.go），
// 因此按读取者保活即可；每次遍历只同时打开一段。
// 追加、滚动及 Store.Close 不改变集合；本引用不授权回收，也不保证恶意外部删除后仍可读。
// 逻辑引用存续期间本实例在该源上登记为「在途读者」，Prune 据此拒绝回收——否则前移段名会让
// 已复制清单里的路径失效。
type ReadSnapshot struct {
	mu       sync.Mutex
	manifest Manifest
	segments []frozenSegment
	readers  int
	closed   bool
	released bool
	// onRelease 在逻辑引用真正释放时调用一次（向源登记归还读取引用）；nil 表示无需归还。
	onRelease func()
}

type frozenSegment struct {
	meta     SegmentMeta
	path     string
	identity os.FileInfo
}

// Snapshot 在短锁内复制清单，锁外捕获文件身份，不按历史段数保留句柄。
// 普通实例首次校准仍可修复崩溃尾部；OpenReadOnly 实例仅建立不修改介质的取证视图。
func (s *Store) Snapshot(key string) (*ReadSnapshot, error) {
	return s.snapshotContext(context.Background(), key)
}

func (s *Store) snapshotContext(ctx context.Context, key string) (snapshot *ReadSnapshot, result error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshot = &ReadSnapshot{manifest: Manifest{Version: manifestVer, Key: key}}
	sec := s.lookup(key)
	if sec == nil {
		return snapshot, nil
	}
	if err := sec.ensureVerifiedContext(ctx); err != nil {
		return nil, err
	}
	// 登记逻辑读取引用：判定「是否正在回收」与登记必须在同一临界区，否则可能与 Prune
	// 的「有读者就不回收」判据互相漏看，让读者拿到随后被前移的段路径。
	sec.viewMu.Lock()
	if sec.pruneActive {
		sec.viewMu.Unlock()
		return nil, errPruneInProgress
	}
	snapshot.manifest = cloneManifest(sec.committed)
	dir := sec.dir
	sec.liveSnapshots++
	sec.viewMu.Unlock()
	registered := true
	defer func() {
		// 构造失败时同样要归还引用，否则该源会被一个不存在的读者永久挡住回收。
		if registered {
			sec.releaseSnapshot()
		}
	}()
	for _, meta := range snapshot.manifest.Segments {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := filepath.Join(dir, meta.Name)
		stat, err := captureSegmentIdentity(path)
		if err != nil {
			return nil, err
		}
		if !stat.Mode().IsRegular() || stat.Size() < meta.Bytes || (meta.Sealed && stat.Size() != meta.Bytes) {
			return nil, fmt.Errorf("%w: snapshot byte boundary changed in %s", ErrCorrupt, path)
		}
		snapshot.segments = append(snapshot.segments, frozenSegment{meta: meta, identity: stat, path: path})
	}
	snapshot.onRelease = sec.releaseSnapshot
	registered = false
	return snapshot, nil
}

// releaseSnapshot 归还一次逻辑读取引用。
func (sec *section) releaseSnapshot() {
	sec.viewMu.Lock()
	if sec.liveSnapshots > 0 {
		sec.liveSnapshots--
	}
	sec.viewMu.Unlock()
}

// Manifest 返回固定清单的深副本，可作同一内容窗口的元数据凭据。
func (snapshot *ReadSnapshot) Manifest() Manifest {
	return cloneManifest(snapshot.manifest)
}

// Count 返回本引用的完整事件数，不随后续追加变化。
func (snapshot *ReadSnapshot) Count() int {
	n := 0
	for _, meta := range snapshot.manifest.Segments {
		n += meta.Count
	}
	return n
}

// Segments 返回固定段元数据的副本，修改副本不会改变本引用。
func (snapshot *ReadSnapshot) Segments() []SegmentMeta {
	return append([]SegmentMeta(nil), snapshot.manifest.Segments...)
}

// Iterate 仅遍历本引用冻结的完整字节；回调可重入存储，也可关闭本引用。
// Close 后不再接纳新读取，但已开始的读取可读完所有段，退出时才释放逻辑引用。
func (snapshot *ReadSnapshot) Iterate(fn func(logtypes.Event) error) error {
	return snapshot.iterateContext(context.Background(), fn)
}

func (snapshot *ReadSnapshot) iterateContext(ctx context.Context, fn func(logtypes.Event) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshot.mu.Lock()
	if snapshot.closed {
		snapshot.mu.Unlock()
		return os.ErrClosed
	}
	snapshot.readers++
	snapshot.mu.Unlock()
	defer func() {
		snapshot.mu.Lock()
		snapshot.readers--
		if snapshot.closed && snapshot.readers == 0 {
			snapshot.released = true
			// 回到源登记处归还读取引用：此后回收才可以前移这些段名。
			snapshot.releaseRefLocked()
		}
		snapshot.mu.Unlock()
	}()
	for _, segment := range snapshot.segments {
		if err := ctx.Err(); err != nil {
			return err
		}
		// 已接纳的逻辑读取即使遇到 Close，也必须继续读取尚未打开的后续段。
		if err := segment.iterate(ctx, fn); err != nil {
			return err
		}
	}
	return nil
}

// Close 拒绝新读，最后一个在途遍历真正退出后释放逻辑引用；不修改任何持久数据。
func (snapshot *ReadSnapshot) Close() error {
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	snapshot.closed = true
	if snapshot.readers == 0 {
		snapshot.released = true
		snapshot.releaseRefLocked()
	}
	return nil
}

// releaseRefLocked 在逻辑引用真正释放时（且仅一次）归还源读取登记。
// 调用方必须持有 snapshot.mu：released 只在该锁内被置真。
func (snapshot *ReadSnapshot) releaseRefLocked() {
	if snapshot.onRelease == nil {
		return
	}
	release := snapshot.onRelease
	// 先摘掉回调再调用：重复 Close 或「Close 后再遍历」都不会重复归还。
	snapshot.onRelease = nil
	release()
}

func (segment frozenSegment) iterate(ctx context.Context, fn func(logtypes.Event) error) (result error) {
	file, err := os.Open(segment.path)
	if err != nil {
		return snapshotPathError("open", segment.path, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			result = errors.Join(result, closeErr)
		}
	}()
	stat, err := file.Stat()
	if err != nil {
		return snapshotPathError("stat", segment.path, err)
	}
	if !os.SameFile(segment.identity, stat) || stat.Size() < segment.meta.Bytes ||
		(segment.meta.Sealed && stat.Size() != segment.meta.Bytes) {
		return fmt.Errorf("%w: snapshot identity or byte boundary changed in %s", ErrCorrupt, segment.path)
	}
	reader := io.NewSectionReader(file, 0, segment.meta.Bytes)
	actual, err := scanSegmentContext(ctx, reader, segment.path, segment.meta, false, fn)
	if err != nil {
		return err
	}
	if actual != segment.meta {
		return fmt.Errorf("%w: snapshot metadata mismatch in %s", ErrCorrupt, segment.path)
	}
	return nil
}

func captureSegmentIdentity(path string) (os.FileInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, snapshotPathError("open", path, err)
	}
	// 从句柄立即取得身份，避免部分平台的路径 Stat 延迟到 SameFile 时才重新打开路径。
	identity, statErr := file.Stat()
	if err := errors.Join(statErr, file.Close()); err != nil {
		return nil, snapshotPathError("stat", path, err)
	}
	return identity, nil
}

func snapshotPathError(operation, path string, err error) error {
	if os.IsNotExist(err) {
		return fmt.Errorf("%w: %s snapshot segment %s: %w", ErrCorrupt, operation, path, err)
	}
	// 句柄耗尽、权限或介质故障仍显式失败，但不能谎报成已经证实的正文损坏。
	return fmt.Errorf("eventstore: %s snapshot segment %s: %w", operation, path, err)
}
