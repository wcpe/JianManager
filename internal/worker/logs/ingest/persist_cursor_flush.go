package ingest

// 游标表的读写入口与节拍（2026-10-30 裁定 D）。
//
// 调用点一共四处：
//   1. 启动时 loadCursorRegistryIntoManager —— 把上次的游标与源登记读进内存；
//   2. 登记时 resumeCursorFor —— 给管道一个续读点（没有就从 EOF 开始）；
//   3. 采集轮尾部 flushCursorRegistry —— 把当前游标整表写回盘（周期节拍）；
//   4. 关停时 flushCursorRegistryOnStop —— 只在**确有变更**时补写一次。
//
// 第 3 处用「周期刷盘」而不是每字节刷：不 fsync，写一次整个文件（约 2KB），
// 最坏丢一个周期的进度，代价是那一段重读一遍（不丢数据）。

import (
	"fmt"
	"log/slog"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
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
			// 两个路径都打出来：运维一眼就能分辨是「绑定换了路径」还是「有人手改了文件」。
			// 选「少一个源」而不是「拿其中一个路径建源」：日志采集读错位置的代价是
			// 重复投递或丢内容，两者都比少一个源难查得多。
			slog.Warn("采集游标行内路径与源配置路径不一致，跳过该源的恢复（下次登记会重建）",
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
	m.applyCursorRegistryCandidates(m.collectCursorRegistryCandidates())
}

// flushCursorRegistryOnStop 是**关停路径**的补写：只在游标表确有变更时落盘。
//
// 为什么关停要单独一次（而不是只靠采集轮节拍）：进程可能「登记后立即停」（部署/重启风暴），
// 那一轮采集轮从未跑过 ⇒ 源登记从未落盘 ⇒ 下次启动又不知道采哪个文件。
//
// 为什么与 flushCursorRegistry 同一个实现：变更判据（sameCursorRegistryEntry）只该有一个，
// 两套判据就一定会分叉。区别只在名字——它把「关停时不该无条件做整表 IO」这件事写在调用点上。
// 正常关停前采集轮已刷过，这里绝大多数时候是 changed=false ⇒ 零磁盘 IO。
func (m *Manager) flushCursorRegistryOnStop() {
	m.applyCursorRegistryCandidates(m.collectCursorRegistryCandidates())
}

// cursorRegistryCandidate 是一条待写入游标表的候选行（游标 + 显式口径的源配置）。
type cursorRegistryCandidate struct {
	key    string
	cursor acquire.SourceCursor
	source *SourceConfig
}

// collectCursorRegistryCandidates 收集各源的**当前**游标与**显式**配置。
//
// 锁纪律见 flushCursorRegistry 的注释：m.mu 内只拷指针与配置副本，p.Cursor() 一律在锁外。
func (m *Manager) collectCursorRegistryCandidates() []cursorRegistryCandidate {
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
		return nil
	}

	// 第 2 步：锁外取游标（此处会拍 p.mu，必须已经不在 m.mu 内）。
	entries := make([]cursorRegistryCandidate, 0, len(pipes))
	for _, kp := range pipes {
		if c, ok := kp.pipe.Cursor(); ok {
			entries = append(entries, cursorRegistryCandidate{key: kp.key, cursor: c, source: kp.source})
			// 落盘前把这个「将被跳过」的区间登记成可见缺口（2026-11-05）。
			m.recordCursorSkipGap(kp.pipe)
		}
	}
	return entries
}

// recordCursorSkipGap 把 `[已投递, 已读)` 区间登记为 `CURSOR_SKIPPED_ON_RESTART` 缺口。
//
// # 它在说什么（「静默丢数据变成可见」的那个转折点）
//
// 游标表落盘的是 tailer 的**已读位**。而 WAL 是纯内存队列、重启即丢；账本来自纯内存
// 索引（`stateindex.OpenInMemory`）重启恒空 ⇒ `rollbackReadToReclaim` 的前提
// `m.state.Sources[key]` 不成立 ⇒ **永不执行**。
//
// 于是新进程从「已读位」继续读，`[已投递, 已读)` 那段就**永久跳过了**：
// WAL 已亡、账本空白、游标已越过。原本这个过程**完全无声**——
// 现在每次落盘前把它记下来，运维就能查到「哪一段没投递成功就丢掉了」，
// 然后从源重采或裁定永久丢失。
//
// # 为何不直接回退游标把它重读一遍
//
// 重读能让数据不丢，但代价是重投；而 **VictoriaLogs 不按 event_id 去重**
// （2026-11-05 实测：同一行写两次 count = 2）⇒ 每次重启都会多写一遍。
// 而 `Delivery` 不前进的条件（VL 持续失败 / 磁盘暂停）恰恰是最需要重启的场景
// ⇒ 重复量随重启次数**无界增长**。
//
// 两边代价不对称时选诚实：承认丢，但让它可查、可告警、可人工裁定。
//
// # 为何用 WARN 而不是 INFO
//
// 这是数据丢失。虽然区间通常很小（单轮未投递量），但它是**唯一**会留下痕迹的地方——
// 用 INFO 会淹没在正常启动日志里。
//
// # 为何不进「可自动消解」名单
//
// 与 `DELIVER_ERROR` 语义不同：后者重试能解决（事件还在 WAL / 还可重读），
// 本原因只能从**源重采**或人工裁定。混用会让自愈环反复尝试一个永远不会成功的动作。
func (m *Manager) recordCursorSkipGap(p *pipeline.Pipeline) {
	if m == nil || p == nil {
		return
	}
	pos, ok := p.Positions()
	if !ok {
		// 账本还没建（刚登记的源）：没有「已投递水位」可参照，也就无从判定跳过区间。
		return
	}
	// 跳过区间 = [Delivery, Read)。两者同属账本坐标系（`RecordRange` 由 RecordBase 对齐）。
	if pos.Read <= pos.Delivery {
		return // 没有未投递的已读字节（高频正常态，零开销）
	}
	from, to := pos.Delivery, pos.Read
	// Detail 写清三件事（区间大小、为何跳、怎么补）——运维看这一条就能判断怎么补救。
	detail := fmt.Sprintf(
		"restart cursor already passed %d un-delivered bytes [%d,%d); "+
			"WAL is memory-only so those bytes died with the previous process and "+
			"rollbackReadToReclaim never runs because the index is in-memory; "+
			"remedy: re-collect this range from the source, or accept it as permanently lost",
		to-from, from, to)
	if err := p.Ledger().RecordGap(p.Key(), from, to, ledger.GapReasonCursorSkippedOnRestart, detail); err != nil {
		slog.Warn("登记「重启跳过未投递区间」缺口失败（该段丢失将不可见）",
			"logSourceID", p.Key().LogSourceID, "from", from, "to", to, "error", err)
		return
	}
	slog.Warn("重启将跳过一段未投递的已读字节（已登记缺口，可从源重采）",
		"logSourceID", p.Key().LogSourceID,
		"sourceGeneration", p.Key().SourceGeneration,
		"from", from, "to", to, "bytes", to-from,
		"delivery", pos.Delivery, "read", pos.Read)
}

// applyCursorRegistryCandidates 把候选行并入内存表，**有变更才落盘**。返回是否写了盘。
func (m *Manager) applyCursorRegistryCandidates(entries []cursorRegistryCandidate) bool {
	if len(entries) == 0 {
		return false
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
		return false // 没变就不写盘：空转周期与正常关停都零磁盘 IO
	}
	snapshot := make(cursorRegistry, len(m.cursorRegistry))
	for key, entry := range m.cursorRegistry {
		snapshot[key] = entry
	}
	m.cursorRegistryMu.Unlock()
	if err := saveCursorRegistry(m.cursorRegistryPath(), snapshot); err != nil {
		slog.Warn("采集游标落盘失败（下一轮重试）", "error", err)
		return false
	}
	return true
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
