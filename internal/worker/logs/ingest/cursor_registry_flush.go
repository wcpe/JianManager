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
func (m *Manager) loadCursorRegistryIntoManager() {
	loaded := loadCursorRegistry(m.cursorRegistryPath())
	m.cursorRegistryMu.Lock()
	m.cursorRegistry = loaded
	m.cursorRegistryMu.Unlock()
	if n := len(loaded); n > 0 {
		slog.Info("采集游标已加载（重启将从上次位置续读）", "sources", n, "path", m.cursorRegistryPath())
	}
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
	out := c
	return &out
}

// flushCursorRegistry 把各源 tailer 的**当前**游标写回表中并落盘。
//
// 由采集轮尾部调用（已有周期节拍）。任一步失败只记日志、不阻断采集：
// 游标是可丢的优化，不是正确性前提。
//
// ## 锁序（必须遵守 pipeline.go 的纪律）
//
// `Pipeline.mu` 的注释写明「锁序为 p.mu→ledger/WAL 叶子锁；**调用方先释放 Manager.mu**，
// 不能反向等待采集锁」。而 Pipeline.Cursor() 必须持 p.mu（tailer.cursor/logicalPos 是
// Poll 在临界区内写的普通字段，不持锁读就是数据竞争）。两条合起来要求：
// **绝不能在持 m.mu 时调 p.Cursor()**——那会构造 m.mu → p.mu 的边，与采集轮
// （先经 cycleMu/registerMu 到 m.mu，再取 p.mu）形成反向等待，就是死锁形态。
//
// 所以这里分两步：(1) 持 m.mu 只把**管道指针切片**拷出来（O(源数)，微秒级）；
// (2) 放锁后再逐源调 Cursor()。代价是快照可能包含一个刚被摘除的源——那无害：
// 它的游标仍是一条真话，而 flush 只写“表”不写“源的存活状态”（源是否还在由 state 决定）。
func (m *Manager) flushCursorRegistry() {
	// 第 1 步：m.mu 内只拿指针。
	m.mu.Lock()
	type keyedPipe struct {
		key  string
		pipe *pipeline.Pipeline
	}
	pipes := make([]keyedPipe, 0, len(m.pipes))
	for key, p := range m.pipes {
		if p == nil {
			continue
		}
		pipes = append(pipes, keyedPipe{key: key, pipe: p})
	}
	m.mu.Unlock()
	if len(pipes) == 0 {
		return
	}

	// 第 2 步：锁外取游标（此处会拍 p.mu，必须已经不在 m.mu 内）。
	type entry struct {
		key    string
		cursor acquire.SourceCursor
	}
	entries := make([]entry, 0, len(pipes))
	for _, kp := range pipes {
		if c, ok := kp.pipe.Cursor(); ok {
			entries = append(entries, entry{key: kp.key, cursor: c})
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
		if prev, ok := m.cursorRegistry[e.key]; !ok || prev != e.cursor {
			m.cursorRegistry[e.key] = e.cursor
			changed = true
		}
	}
	if !changed {
		m.cursorRegistryMu.Unlock()
		return // 没变就不写盘：空转周期零磁盘 IO
	}
	snapshot := make(cursorRegistry, len(m.cursorRegistry))
	for key, cursor := range m.cursorRegistry {
		snapshot[key] = cursor
	}
	m.cursorRegistryMu.Unlock()
	if err := saveCursorRegistry(m.cursorRegistryPath(), snapshot); err != nil {
		slog.Warn("采集游标落盘失败（下一轮重试）", "error", err)
	}
}
