package logtail

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/wcpe/JianManager/internal/worker/logs/logconf"
	"sort"
	"strings"
	"sync"
)

// StaticSource 是一条**非实例**日志源（节点自身日志等）的归属声明。
//
// 它与 BindingEntry 的区别在于寻址方式：BindingEntry 按「实例 UUID」寻址、
// 按 WorkDir 前缀匹配；StaticSource 按**具体文件/目录**寻址，与服务无实例归属。
//
// 两者共享同一个 Resolver 口径（TargetID + "/" + stream 作为 log_source_id），
// 因此查询侧不需要区分“这条日志是实例的还是节点的”。
type StaticSource struct {
	// TargetID 是查询侧看到的目标，形如 `node:1`（与 CP 联邦查询的 node:<id> 同口径）。
	TargetID string
	// Path 是该源的具体文件路径（也允许目录：目录下的文件全部归本源源）。
	Path string
	// Stream 是该源的流名（如 stdout / file / worker）。空时默认 "file"。
	Stream string
}

// BindingEntry 是一个实例的日志目标绑定。
//
// # 为什么需要落盘
//
// 新采集器按 glob 扫文件，而「这个文件属于哪个源」只能由 CP 下发的实例登记决定
// （`inst:<实例ID>`）。登记只发生在 CP 调 CreateInstance 的那一刻；Worker 重启后
// 在 CP 重新下发前的那段时间里，扫描到的所有行都因「无法识别源」被跳过——
// 而 ReadFromHead 会把这些行从文件头读一遍后丢弃，**那是不可恢复的丢数据**。
//
// 所以绑定要像位点一样落盘：重启后立即能定位源，不等 CP。
type BindingEntry struct {
	UUID string `json:"uuid"`
	// TargetID 是 CP 侧的日志目标，形如 `inst:318`（与分区 storage_namespace 同一口径）。
	TargetID string `json:"target_id"`
	// WorkDir 是实例工作目录（文件源按它做前缀匹配）。
	WorkDir string `json:"work_dir"`
	// Mode 是采集模式（FILE_PRIMARY / STDIO_PRIMARY）。
	//
	// 它决定 stdout 输出该不该落到 stdio 文件：FILE_PRIMARY 实例的进程输出与
	// 它自己写的 latest.log **是同一份内容**，再写一遍就是重复采集。
	Mode string `json:"mode"`
}

// bindingsFile 是磁盘格式（带版本号，便于将来迁移）。
type bindingsFile struct {
	Version int            `json:"version"`
	Entries []BindingEntry `json:"entries"`
}

const bindingsFileVersion = 1

// Bindings 是「实例 → 日志目标」的绑定表，持久化为单个 JSON 文件。
//
// # 为什么不复用旧链路的 stateindex
//
// 那套索引是旧采集器的四水位账本载体（阶段三要删）。绑定只需要「uuid → targetID + workdir」，
// 一个几十行的 JSON 就够；用 SQLite 引入的是一整套依赖，而收益只是查询方便一点。
//
// # 并发与落盘
//
// 读多写少（每次启动/登记写一次，解析是每批行都读）。写用临时文件 + rename 保证原子性；
// 不 fsync——丢的是「最后几条绑定」，重启后等 CP 重下发即可，而重读文件本身不丢数据。
type Bindings struct {
	path      string
	stdioRoot string

	mu      sync.RWMutex
	entries map[string]BindingEntry
	// static 是非实例源（节点自身日志等）的静态归属，按声明顺序保存。
	//
	// 它不落盘：与实例绑定不同，它的来源是配置文件本身，重启后由装配层重新
	// repopulate 一遍即可。落盘反而会让“改了配置但盘上还是旧的”成为一个隐患。
	static []StaticSource
	dirty  bool
}

// OpenBindings 加载（或初始化）绑定表。
//
// path 为空表示纯内存（测试用）；stdioRoot 参与解析（该目录下的文件按 uuid 归源）。
// 文件不存在不是错：首次运行本来就没有绑定。
func OpenBindings(path, stdioRoot string) (*Bindings, error) {
	b := &Bindings{path: path, stdioRoot: stdioRoot, entries: make(map[string]BindingEntry)}
	if strings.TrimSpace(path) == "" {
		return b, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return b, nil
		}
		return nil, fmt.Errorf("logtail: 读取绑定表 %s: %w", path, err)
	}
	var f bindingsFile
	if err := json.Unmarshal(data, &f); err != nil {
		// 损坏不阻断采集：全量当作「无绑定」，等 CP 重下发覆盖。
		// （位点表的坏行同样是跳过而不是拒启——采集不该因文件写坏而停摆。）
		slog.Warn("logtail: 绑定表损坏，按空表继续（等 CP 重新下发实例登记）", "path", path, "error", err)
		return b, nil
	}
	for _, e := range f.Entries {
		if e.UUID == "" || e.TargetID == "" {
			continue
		}
		b.entries[e.UUID] = e
	}
	return b, nil
}

// Entries 返回当前全部绑定的副本（按 UUID 排序，便于断言与日志对比）。
//
// 供启动期 Catalog 重建用：它需要知道「哪些命名空间是平台管理的」，
// 不能把 VL 里的探针数据也建成分区。
func (b *Bindings) Entries() []BindingEntry {
	if b == nil {
		return nil
	}
	b.mu.RLock()
	out := make([]BindingEntry, 0, len(b.entries))
	for _, e := range b.entries {
		out = append(out, e)
	}
	b.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].UUID < out[j].UUID })
	return out
}

// WorkDirs 返回当前已绑定实例的工作目录（去重、已 Clean）。
//
// 供采集器每轮现取采集 glob——实例由 CP 在运行期下发，启动时不知道全量。
func (b *Bindings) WorkDirs() []string {
	if b == nil {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	seen := make(map[string]struct{}, len(b.entries))
	out := make([]string, 0, len(b.entries))
	for _, e := range b.entries {
		if e.WorkDir == "" {
			continue
		}
		if _, dup := seen[e.WorkDir]; dup {
			continue
		}
		seen[e.WorkDir] = struct{}{}
		out = append(out, e.WorkDir)
	}
	return out
}

// PathsProvider 返回采集 glob 的实时提供函数（供 tailer.CollectorConfig.ExtraPaths）。
//
// 每轮扫描现算，因此**新建实例的日志无需重启就能被采到**。
func (b *Bindings) PathsProvider() func() []string {
	return func() []string { return DefaultPathsFor(b.WorkDirs()) }
}

// Path 返回绑定表路径（空串 = 纯内存）。
func (b *Bindings) Path() string {
	if b == nil {
		return ""
	}
	return b.path
}

// AddStatic 登记一批非实例源（节点自身日志等）。
//
// 与 Put 不同，它不落盘：静态源的权威是配置文件，重启后由装配层重新登记。
// 落盘会让“改了配置、盘上还是旧的”变成隐患。
//
// 空 TargetID / Path 的条目被跳过（配置层已有校验，这里只兜底不 panic）。
func (b *Bindings) AddStatic(sources []StaticSource) {
	if b == nil || len(sources) == 0 {
		return
	}
	// 归一：去空白、清理路径、补默认 stream。
	clean := make([]StaticSource, 0, len(sources))
	for _, s := range sources {
		target := strings.TrimSpace(s.TargetID)
		path := filepath.Clean(strings.TrimSpace(s.Path))
		if target == "" || path == "" || path == "." {
			continue
		}
		stream := strings.TrimSpace(s.Stream)
		if stream == "" {
			stream = "file"
		}
		clean = append(clean, StaticSource{TargetID: target, Path: path, Stream: stream})
	}
	b.mu.Lock()
	b.static = clean
	b.mu.Unlock()
}

// StaticSources 返回已登记的静态源（供启动日志与排障）。
func (b *Bindings) StaticSources() []StaticSource {
	if b == nil {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]StaticSource, len(b.static))
	copy(out, b.static)
	return out
}

// resolveStatic 按静态源解析路径。
//
// 匹配规则：路径相等，或路径在静态源的目录下（目录形式的源）。
// 取**最长**匹配的 Path：允许“一个目录源 + 其中某文件单独归另一个源”这类叠加。
func (b *Bindings) resolveStatic(clean string) (StaticSource, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var best StaticSource
	bestLen := -1
	for _, s := range b.static {
		if _, ok := relUnderRoot(clean, s.Path); !ok {
			continue
		}
		if len(s.Path) > bestLen {
			best, bestLen = s, len(s.Path)
		}
	}
	return best, bestLen >= 0
}

// Put 登记/更新一个实例的绑定，并立即落盘。
//
// 立即落盘而不是攒批：登记是低频事件（启动/建实例），而「重启后能立刻定位源」
// 完全依赖它已经在盘上。一次 rename 的代价在这个频率下可以忽略。
func (b *Bindings) Put(e BindingEntry) error {
	if b == nil || e.UUID == "" || e.TargetID == "" {
		return nil
	}
	e.WorkDir = filepath.Clean(strings.TrimSpace(e.WorkDir))
	if e.WorkDir == "." {
		e.WorkDir = ""
	}
	b.mu.Lock()
	old, ok := b.entries[e.UUID]
	b.entries[e.UUID] = e
	if !ok || old != e {
		b.dirty = true
	}
	b.mu.Unlock()
	return b.Flush()
}

// Get 按 uuid 取绑定。
func (b *Bindings) Get(uuid string) (BindingEntry, bool) {
	if b == nil {
		return BindingEntry{}, false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	e, ok := b.entries[uuid]
	return e, ok
}

// Len 返回已知绑定数（供启动日志与排障）。
func (b *Bindings) Len() int {
	if b == nil {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.entries)
}

// Flush 把绑定表原子落盘（临时文件 + rename）。
//
// 不 fsync：与位点表同口径（2026-10-30 裁定）。掉电丢的最后几条绑定由 CP 重下发修复。
func (b *Bindings) Flush() error {
	if b == nil || strings.TrimSpace(b.path) == "" {
		return nil
	}
	b.mu.Lock()
	if !b.dirty {
		b.mu.Unlock()
		return nil
	}
	entries := make([]BindingEntry, 0, len(b.entries))
	for _, e := range b.entries {
		entries = append(entries, e)
	}
	b.dirty = false
	b.mu.Unlock()

	data, err := json.MarshalIndent(bindingsFile{Version: bindingsFileVersion, Entries: entries}, "", "  ")
	if err != nil {
		return fmt.Errorf("logtail: 编码绑定表: %w", err)
	}
	dir := filepath.Dir(b.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("logtail: 创建绑定表目录 %s: %w", dir, err)
	}
	tmp := b.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("logtail: 写绑定表临时文件: %w", err)
	}
	if err := os.Rename(tmp, b.path); err != nil {
		return fmt.Errorf("logtail: 绑定表落盘: %w", err)
	}
	return nil
}

// BindingsPathFor 由位点表路径推出绑定表路径（同在 `var/log/` 下）。
//
// 为什么跟位点表放一起：两者生命周期一致（都属于「重启后接着上次继续」的持久状态），
// 运维清理时也不会只清掉一半。
func BindingsPathFor(registryPath string) string {
	p := strings.TrimSpace(registryPath)
	if p == "" {
		return ""
	}
	return p + ".bindings.json"
}

// Resolver 返回把文件路径解析成源标签的函数（实现 SourceResolver）。
//
// # 推导规则（对齐旧链路的源身份口径）
//
//   - stdio 文件（`<stdioRoot>/<uuid>/<stream>.log`）⇒ `inst:<id>/<stream>`、stream
//   - 实例工作目录下的文件 ⇒ `inst:<id>/file`、stream=stdout
//
// 「`/file`」这个后缀不是随便定的：旧链路 FILE_PRIMARY 的源标识正是
// `TargetID + "/file"`（见 ingest/policy_instances.go）。沿用同一口径，历史数据与
// 新数据落在同一个 `log_source_id` 上，查询侧与分面统计不需要区分两套。
//
// 未登记的路径返回空串——**不猜**。猜错会把 A 实例的日志写进 B 的源，比查不到更难查。
func (b *Bindings) Resolver() SourceResolver {
	return SourceResolverFunc(func(path string) (string, string) {
		if b == nil {
			return "", ""
		}
		clean := filepath.Clean(path)
		// 1) stdio 桥接文件：<stdioRoot>/<uuid>/<YYYY-MM-DD>/<stream>.log
		if root := strings.TrimSpace(b.stdioRoot); root != "" {
			if rel, ok := relUnderRoot(clean, root); ok {
				parts := strings.Split(rel, string(filepath.Separator))
				if len(parts) == 3 {
					stream := strings.TrimSuffix(parts[2], filepath.Ext(parts[2]))
					if e, ok := b.Get(parts[0]); ok {
						return e.TargetID + "/" + stream, stream
					}
				}
				return "", ""
			}
		}
		// 2) 实例工作目录下的文件：取**最长**匹配的工作目录（嵌套部署时避免被父目录抢走）。
		b.mu.RLock()
		var best BindingEntry
		bestLen := -1
		for _, e := range b.entries {
			if e.WorkDir == "" || e.Mode == logconf.ModeStdioPrimary {
				continue
			}
			if _, ok := relUnderRoot(clean, e.WorkDir); !ok {
				continue
			}
			if len(e.WorkDir) > bestLen {
				best, bestLen = e, len(e.WorkDir)
			}
		}
		b.mu.RUnlock()
		if bestLen >= 0 {
			return best.TargetID + "/file", "stdout"
		}
		// 3) 静态源（非实例：节点自身日志等）。
		//
		// 放在实例匹配之后：实例目录是**更具体**的归属，一个节点级目录源
		// （如整个日志目录）不应抢走其中某实例自己的文件。
		if s, ok := b.resolveStatic(clean); ok {
			return s.TargetID + "/" + s.Stream, s.Stream
		}
		return "", ""
	})
}

// relUnderRoot 返回 path 相对 root 的部分；不在 root 下时 ok=false。
//
// 比 strings.HasPrefix 多一步：`/a/bc` 不能被当成 `/a/b` 的子路径（缺少分隔符判断会误判）。
func relUnderRoot(path, root string) (string, bool) {
	root = filepath.Clean(root)
	if root == "" || root == "." {
		return "", false
	}
	if path == root {
		return "", true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	return strings.TrimPrefix(path, prefix), true
}
