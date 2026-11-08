package catalog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// QueryLeaseJournal 的独立修订和文件不参与权威 journal 的 CAS 序号。
type QueryLeaseJournal interface {
	AppendLeaseDelta(LeaseDelta) error
	CompactLeaseDeltas([]LeaseDelta) error
	LeaseDeltaSnapshot() ([]LeaseDelta, error)
}

type leaseDeltaLog struct {
	mu   sync.Mutex
	path string
	// degraded 表示**没有**耐久租约旁路文件，租约退化为进程内内存态。
	//
	// # 为什么是「降级」而不是「不可用」（2026-11-05 生产事故后的语义修正）
	//
	// 旧字段叫 unavailable，且 loadLocked 直接返回错误。后果：只要 journal 存储没提供
	// 租约旁路（例如被代理一层攒批），Catalog 的租约准入就全线拒绝、**整个查询面挂掉**
	// ——生产实证：部署后所有 target 都是
	// `query lease admission rejected: catalog: custom journal has no durable lease store`。
	//
	// 这是 fail-closed 用错了地方：对**正确性**该 fail-closed（宁可不返回数据，
	// 也不返回错数据）；但租约是**可用性的辅助机制**——它是短生命周期的运行时状态，
	// 视图 TTL 到期就失效，进程重启后旧视图 ID 本来就不该复活。
	// 丢了它的唯一后果是「旧视图失效得早一点」，不该升级成「拒绝服务」。
	//
	// 所以现在：无旁路文件 ⇒ 只存内存、不报错、打 WARN（可见、不静默 ✗）。
	degraded bool
	entries  []LeaseDelta
	loaded   bool
}

// LeaseStorePath 实现 LeaseStoreProvider：租约旁路文件与 journal 同目录，
// 以 `.leases` 后缀区分（不参与权威 journal 的 CAS 序号）。
//
// 为什么用方法而不是让调用方读 path 字段：这样 JournalWithStore 可以把它
// **透传给被包裹的 store**（见 BatchedStore.LeaseStorePath），不依赖具体类型。
func (s *JSONLFileStore) LeaseStorePath() (string, bool) {
	if s == nil || s.path == "" {
		return "", false
	}
	return s.path + ".leases", true
}

func cloneLeaseDelta(in LeaseDelta) LeaseDelta {
	out := in
	out.InvalidViews = append([]string(nil), in.InvalidViews...)
	out.Keys = append([]PartitionKey(nil), in.Keys...)
	out.Snapshots = append([]QuerySnapshot(nil), in.Snapshots...)
	for i := range out.Snapshots {
		out.Snapshots[i].Projection = nil
	}
	return out
}

func (l *leaseDeltaLog) loadLocked() error {
	// 降级：没有耐久旁路文件 ⇒ 纯内存（不报错）。见 degraded 字段的注释。
	if l.degraded || l.path == "" {
		l.loaded = true
		return nil
	}
	if l.loaded {
		return nil
	}
	l.loaded = true
	f, err := os.Open(l.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 65536), 4<<20)
	var bytes int
	for scanner.Scan() {
		bytes += len(scanner.Bytes())
		if bytes > 64<<20 {
			return fmt.Errorf("catalog: lease journal exceeds bounded recovery size")
		}
		var delta LeaseDelta
		if err := json.Unmarshal(scanner.Bytes(), &delta); err != nil {
			return err
		}
		l.entries = append(l.entries, cloneLeaseDelta(delta))
	}
	return scanner.Err()
}
func (l *leaseDeltaLog) snapshot() ([]LeaseDelta, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.loadLocked(); err != nil {
		return nil, err
	}
	out := make([]LeaseDelta, len(l.entries))
	for i, entry := range l.entries {
		out[i] = cloneLeaseDelta(entry)
	}
	return out, nil
}
func (l *leaseDeltaLog) append(delta LeaseDelta) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.loadLocked(); err != nil {
		return err
	}
	delta = cloneLeaseDelta(delta)
	if l.path != "" {
		data, err := json.Marshal(delta)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(append(data, '\n'))
		if writeErr == nil {
			writeErr = f.Sync()
		}
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	l.entries = append(l.entries, delta)
	return nil
}
func (l *leaseDeltaLog) compact(entries []LeaseDelta) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	// 降级：没有耐久旁路文件 ⇒ 只更新内存态（不报错）。见 degraded 字段的注释。
	if l.degraded || l.path == "" {
		l.entries = make([]LeaseDelta, len(entries))
		for i, entry := range entries {
			l.entries[i] = cloneLeaseDelta(entry)
		}
		l.loaded = true
		return nil
	}
	next := make([]LeaseDelta, len(entries))
	for i, entry := range entries {
		next[i] = cloneLeaseDelta(entry)
	}
	if l.path != "" {
		dir := filepath.Dir(l.path)
		f, err := os.CreateTemp(dir, ".catalog-leases-*")
		if err != nil {
			return err
		}
		name := f.Name()
		defer os.Remove(name)
		encoder := json.NewEncoder(f)
		for _, entry := range next {
			if err = encoder.Encode(entry); err != nil {
				_ = f.Close()
				return err
			}
		}
		if err = f.Sync(); err != nil {
			_ = f.Close()
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		if err = os.Rename(name, l.path); err != nil {
			return err
		}
		parent, err := os.Open(dir)
		if err != nil {
			return err
		}
		err = parent.Sync()
		closeErr := parent.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	l.entries = next
	l.loaded = true
	return nil
}

func (j *MemJournal) AppendLeaseDelta(delta LeaseDelta) error       { return j.leases.append(delta) }
func (j *MemJournal) CompactLeaseDeltas(entries []LeaseDelta) error { return j.leases.compact(entries) }
func (j *MemJournal) LeaseDeltaSnapshot() ([]LeaseDelta, error)     { return j.leases.snapshot() }
func (j *JournalWithStore) AppendLeaseDelta(delta LeaseDelta) error { return j.leases.append(delta) }
func (j *JournalWithStore) CompactLeaseDeltas(entries []LeaseDelta) error {
	return j.leases.compact(entries)
}
func (j *JournalWithStore) LeaseDeltaSnapshot() ([]LeaseDelta, error) { return j.leases.snapshot() }
