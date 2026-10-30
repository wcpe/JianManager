package ingest

// 游标表的读写入口与节拍（2026-10-30 裁定 D）。
//
// 调用点一共三处：
//   1. 启动时 loadCursorRegistryIntoManager —— 把上次的游标读进内存；
//   2. 登记时 resumeCursorFor —— 给管道一个续读点（没有就从 EOF 开始）；
//   3. 采集轮尾部 flushCursorRegistry —— 把当前游标整表写回盘。
//
// 第 3 处用「周期刷盘」而不是每字节刷：不 fsync，写一次整个文件（约 2KB），
// 最坏丢一个周期的进度，代价是那一段重读一遍（不丢数据）。

import (
	"log/slog"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// loadCursorRegistryIntoManager 在 New 阶段把游标表读进内存。
//
// 读失败/文件不存在一律当空表：游标丢了最多重读一段，绝不能因此拒绝启动采集。
//
// 这张表同时还承担**源配置的跨重启载体**（见 cursorRegistryEntry 注释）：索引库是
// `:memory:`，重启后 m.state 永远是空的，唯一记得「有哪些源」的持久面就是这里。
func (m *Manager) loadCursorRegistryIntoManager() {
	loaded := loadCursorRegistry(m.cursorRegistryPath())
	m.cursorRegistryMu.Lock()
	m.cursorRegistry = loaded
	m.cursorRegistryMu.Unlock()
	if n := len(loaded); n > 0 {
		withSource := 0
		for _, entry := range loaded {
			if entry.Source != nil {
				withSource++
			}
		}
		slog.Info("采集游标已加载（重启将从上次位置续读）",
			"sources", n, "withSourceConfig", withSource, "path", m.cursorRegistryPath())
	}
}

// sourcesFromCursorRegistry 把游标表里「带源配置」的行转成源配置（key → SourceConfig）。
//
// 只返回**可重建**的行：
//   - v1 行（没有源配置列）不返回——只恢复游标，不重建源；
//   - 游标路径与源配置路径不一致的行不返回：那说明两者不是同一次写入的产物，
//     按任一个重建都可能指向错的文件（宁可少一个源，也不建一个读错位置的源）。
//
// 实例源不在这里重算路径：重算靠 recalcInstanceSourceConfig（它需要 m.state.Instances，
// 而内存索引重启后为空），由 New 的启动恢复循环统一处理。
func (m *Manager) sourcesFromCursorRegistry() map[string]SourceConfig {
	m.cursorRegistryMu.Lock()
	defer m.cursorRegistryMu.Unlock()
	out := make(map[string]SourceConfig, len(m.cursorRegistry))
	for key, entry := range m.cursorRegistry {
		if entry.Source == nil {
			continue
		}
		if entry.Path != entry.Source.Path {
			slog.Warn("采集游标行内路径与源配置路径不一致，跳过该源的恢复",
				"source", key, "cursorPath", entry.Path, "sourcePath", entry.Source.Path)
			continue
		}
		out[key] = *entry.Source
	}
	return out
}

// resumeCursorFor 取一个源的续读点；没有就返回 nil（调用方会退回 EOF 起点）。
//
// 只返回**仍可用于当前文件**的游标；是否真的能用（inode/长度匹配）由
// acquire.FileTailer.ResumeFrom 复核——两层判据各管一段，不重复也不冲突。
func (m *Manager) resumeCursorFor(key, path string) *acquire.SourceCursor {
	m.cursorRegistryMu.Lock()
	defer m.cursorRegistryMu.Unlock()
	c, ok := m.cursorRegistry[key]
	if !ok || c.Path != path {
		return nil
	}
	out := c.SourceCursor
	return &out
}

// flushCursorRegistry 把各源 tailer 的**当前**游标与各源的**显式**配置写回表中并落盘。
//
// 由采集轮尾部与 Stop 调用。任一步失败只记日志、不阻断采集：
// 游标是可丢的优化，不是正确性前提。
//
// ## 为什么连源配置一起写
//
// 索引库是 `:memory:`，重启后源集合只能从这里恢复（见 cursorRegistryEntry）。
// 不写它，游标就是一条「读到哪」但没人知道「读什么」的死数据。
//
// ## 为什么取 m.state.SourceConfigs（而不是 m.sources）
//
// m.sources 是**生效**配置（Charset/TimeZone 里可能混着继承来的节点默认），
// m.state.SourceConfigs 才是**显式口径**（继承来的值不写回）。存的必须是后者：
// 存生效值会把「本次继承了 gbk」固化成「该源显式配了 gbk」，之后改节点默认重启对
// 这个源永不生效（TestNodeDefaultsFollowRestartAndExplicitValuesWin 第 1/2 轮吊销）。
//
// ## 锁序（必须遵守 pipeline.go 的纪律）
//
// `Pipeline.mu` 的注释写明「锁序为 p.mu→ledger/WAL 叶子锁；**调用方先释放 Manager.mu**，
// 不能反向等待采集锁」。而 Pipeline.Cursor() 必须持 p.mu（tailer.cursor/logicalPos 是
// Poll 在临界区内写的普通字段，不持锁读就是数据竞争）。两条合起来要求：
// **绝不能在持 m.mu 时调 p.Cursor()**——那会构造 m.mu → p.mu 的边，与采集轮
// （先经 cycleMu/registerMu 到 m.mu，再取 p.mu）形成反向等待，就是死锁形态。
//
// 所以这里分两步：(1) 持 m.mu 只把**管道指针切片与源配置副本**拷出来（O(源数)，微秒级）；
// (2) 放锁后再逐源调 Cursor()。代价是快照可能包含一个刚被摘除的源——那无害：
// 它的游标与配置仍是一条真话，而 flush 只写“表”不写“源的存活状态”。
func (m *Manager) flushCursorRegistry() {
	// 第 1 步：m.mu 内只拿指针与显式配置副本。
	m.mu.Lock()
	type keyedPipe struct {
		key    string
		pipe   *pipeline.Pipeline
		source *SourceConfig
	}
	pipes := make([]keyedPipe, 0, len(m.pipes))
	for key, p := range m.pipes {
		if p == nil {
			continue
		}
		item := keyedPipe{key: key, pipe: p}
		if source, ok := m.state.SourceConfigs[key]; ok {
			copy := source
			item.source = &copy
		}
		pipes = append(pipes, item)
	}
	m.mu.Unlock()
	if len(pipes) == 0 {
		return
	}

	// 第 2 步：锁外取游标（此处会拍 p.mu，必须已经不在 m.mu 内）。
	type entry struct {
		key    string
		cursor acquire.SourceCursor
		source *SourceConfig
	}
	entries := make([]entry, 0, len(pipes))
	for _, kp := range pipes {
		if c, ok := kp.pipe.Cursor(); ok {
			entries = append(entries, entry{key: kp.key, cursor: c, source: kp.source})
		}
	}
	if len(entries) == 0 {
		return
	}
	// 游标表的更新与快照必须在 cursorRegistryMu 内完成（登记路径会并发读它，见字段注释）。
	// 落盘在锁**外**：open+write+rename 是整表磁盘 IO，不能把登记路径堵在里面。
	m.cursorRegistryMu.Lock()
	if m.cursorRegistry == nil {
		// 零值 Manager（测试/探针直接构造）下没有这一步赋值会 panic。
		// 正常启动路径由 New → loadCursorRegistryIntoManager 初始化，这里只是兼容。
		m.cursorRegistry = cursorRegistry{}
	}
	changed := false
	for _, e := range entries {
		next := cursorRegistryEntry{SourceCursor: e.cursor}
		if e.source != nil {
			copy := *e.source
			next.Source = &copy
		}
		if prev, ok := m.cursorRegistry[e.key]; !ok || !sameCursorRegistryEntry(prev, next) {
			m.cursorRegistry[e.key] = next
			changed = true
		}
	}
	if !changed {
		m.cursorRegistryMu.Unlock()
		return // 没变就不写盘：空转周期零磁盘 IO
	}
	snapshot := make(cursorRegistry, len(m.cursorRegistry))
	for key, entry := range m.cursorRegistry {
		snapshot[key] = entry
	}
	m.cursorRegistryMu.Unlock()
	if err := saveCursorRegistry(m.cursorRegistryPath(), snapshot); err != nil {
		slog.Warn("采集游标落盘失败（下一轮重试）", "error", err)
	}
}

// sameCursorRegistryEntry 判定两行在语义上是否一致（用于「没变就不写盘」）。
func sameCursorRegistryEntry(a, b cursorRegistryEntry) bool {
	if a.SourceCursor != b.SourceCursor {
		return false
	}
	if a.Source == nil || b.Source == nil {
		return a.Source == nil && b.Source == nil
	}
	return *a.Source == *b.Source
}
