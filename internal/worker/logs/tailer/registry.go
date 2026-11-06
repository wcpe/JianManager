package tailer

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// FileState 是一个文件的位点（对齐 Fluent Bit `db` 的 `inode=offset`，
// 但额外带 fingerprint 与路径用于诊断）。
type FileState struct {
	// Identity 是文件身份（fingerprint 为主）。作为 registry 的 key。
	Identity FileIdentity `json:"identity"`
	// Path 是最后一次见到的路径。
	//
	// 轮转后路径会变（latest.log → 2026-11-06-14.log），但 fingerprint 不变
	// ⇒ 用同一个 registry 条目继续跟。所以 Path 只是诊断信息，不参与身份判定。
	Path string `json:"path"`
	// Offset 是已读到的字节位置。
	Offset int64 `json:"offset"`
	// Size 是上次读时的文件大小。
	//
	// 用途：下次打开时若当前 size < 记录的 size ⇒ 文件被截断（copytruncate 或重写），
	// 此时 offset 已失效，必须重置。这是 Fluent Bit `db.compare_filename`
	// 与 Filebeat `rotation.external.strategy.copytruncate` 共同处理的问题。
	Size int64 `json:"size"`
	// LastActiveUnix 是最后一次读到新内容的秒级时间戳（用于 CloseInactive）。
	LastActiveUnix int64 `json:"last_active"`
}

// Registry 是位点表的磁盘存储（对齐 Fluent Bit 的 `db` / Filebeat 的 registry）。
//
// # 格式选择：JSONL 而不是 SQLite
//
// Fluent Bit 用 SQLite（`db`），Filebeat 用自己的 append-only log。
// 我们选 JSONL：
//
//   - 依赖最少（不引入 sqlite 驱动）；
//   - 人可读，排障时 cat 就能看；
//   - 规模小（20 源 × 几个文件 = 几十行），不需要索引。
//
// # 不 fsync（对齐 Fluent Bit `db.sync=normal`）
//
// 写临时文件 + rename 保证原子性（读方要么看到旧完整表，要么新完整表）。
// 不调 Sync：掉电丢的是“最后最多一个周期的进度”，后果是那段重读，
// 而重读对一个采日志的系统是可接受的（可能产生重复，但不会丢）。
//
// 这也与用户 2026-10-30 的裁定一致：机械盘不做 fsync。
type Registry struct {
	path  string
	mu    sync.Mutex
	m     map[string]FileState // key: Identity.Key()
	dirty bool
}

// OpenRegistry 加载（或初始化）位点表。
//
// 路径不存在不是错：首次运行本就没有位点。
func OpenRegistry(path string) (*Registry, error) {
	r := &Registry{path: path, m: make(map[string]FileState)}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil // 首次运行
		}
		return nil, fmt.Errorf("tailer: 打开位点表 %s: %w", path, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	// 单行上限放宽到 1MB：Path 可能很长，但不至于真到 1MB。
	// 超限行会被跳过（不拖死整个表）——与 Fluent Bit 对损坏行的处理同精神。
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var st FileState
		if err := json.Unmarshal(line, &st); err != nil {
			continue // 坏行不影响其余行
		}
		if st.Identity.Key() == "ino:0:0" {
			continue // 没有可用身份的行无意义
		}
		r.m[st.Identity.Key()] = st
	}
	// Scanner 出错（如超长行）不阻断：已读到的部分照用。
	return r, sc.Err()
}

// Get 查一个文件的位点。
func (r *Registry) Get(id FileIdentity) (FileState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.m[id.Key()]
	return st, ok
}

// GetGrowing 查一个文件的位点，按指纹**前缀**容忍小文件增长。
//
// # 为什么不能只用 Get
//
// 指纹只哈希前 min(1024, 文件大小) 字节。文件小于 1024 时，追加会让参与哈希的字节
// 变多 ⇒ 指纹变 ⇒ Get 不命中 ⇒ 整文件从头重读，已投递的行全部重复。
//
// # 判据（可证，不靠猜）
//
// 对每条**同路径**且 Size < 文件当前大小 的记录，用**记录当时的前缀长度**重算指纹；
// 相等即证明这是同一文件的延长（sha256 碰撞可忽）。
//
// # 为何限定同路径
//
// 指纹本来就是为了避开 inode 复用（文件删了后 inode 可能被新文件拿到）。
// 用前缀相等去跨路径匹配，把「同模板的两个实例日志」当成一个的风险就回来了；
// 限定同路径后，最坏情况是同一路径被重写——那时前缀本来就不同，自然不会命中。
func (r *Registry) GetGrowing(id FileIdentity, path string, size int64) (FileState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if st, ok := r.m[id.Key()]; ok {
		return st, true
	}
	if id.Fingerprint == "" || path == "" {
		return FileState{}, false
	}
	clean := filepath.Clean(path)
	for _, st := range r.m {
		if st.Path == "" || filepath.Clean(st.Path) != clean {
			continue
		}
		// 只接受「记录时更短、现在更长」的方向：反向（文件被截断）不能复用 offset。
		if st.Size <= 0 || st.Size >= size {
			continue
		}
		n := FingerprintPrefixLen(st)
		if n <= 0 {
			continue
		}
		fp, ok := fingerprintOfPrefix(clean, n)
		if ok && fp == st.Identity.Fingerprint {
			return st, true
		}
	}
	return FileState{}, false
}

// Put 写入/更新一个文件的位点（内存，不立即落盘）。
func (r *Registry) Put(st FileState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[st.Identity.Key()] = st
	r.dirty = true
}

// Delete 删除一个文件的位点（对齐 Filebeat `clean_removed`）。
//
// 何时调：文件已确认消失（不在扫描结果里）且已过期。
// 不在文件刚消失时就删：轮转瞬间文件会短暂“消失”（改名中间态）。
func (r *Registry) Delete(id FileIdentity) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[id.Key()]; ok {
		delete(r.m, id.Key())
		r.dirty = true
	}
}

// Snapshot 返回当前全部位点（副本，供观测）。
func (r *Registry) Snapshot() []FileState {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]FileState, 0, len(r.m))
	for _, st := range r.m {
		out = append(out, st)
	}
	return out
}

// Flush 把位点表写回磁盘。
//
// 写法：临时文件 + rename（同目录内 rename 是原子的）。
// **不 fsync**：见类型注释。
//
// 没有变更时零磁盘 IO（与旧实现的 `sameCursorRegistryEntry` 判据同精神）。
func (r *Registry) Flush() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.dirty {
		return nil
	}
	if r.path == "" {
		r.dirty = false
		return nil // 未配置路径 = 仅内存（测试或临时部署）
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return fmt.Errorf("tailer: 建位点表目录: %w", err)
	}
	tmp := r.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("tailer: 建临时位点表: %w", err)
	}
	w := bufio.NewWriter(f)
	// 排序写出：同内容两次写出逐字节相同，便于 diff 排障。
	keys := make([]string, 0, len(r.m))
	for k := range r.m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, k := range keys {
		line, mErr := json.Marshal(r.m[k])
		if mErr != nil {
			continue
		}
		if _, wErr := w.Write(append(line, '\n')); wErr != nil {
			_ = f.Close()
			return fmt.Errorf("tailer: 写位点表: %w", wErr)
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, r.path); err != nil {
		return fmt.Errorf("tailer: 替换位点表: %w", err)
	}
	r.dirty = false
	return nil
}
