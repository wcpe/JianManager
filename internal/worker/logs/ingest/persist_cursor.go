package ingest

// 源采集游标的最小持久化（2026-10-30 用户裁定 D）。
//
// ## 背景：为什么不全落盘、也不全不落盘
//
// 用户先前的裁定是「机械盘不要做任何落盘」，但 P1（7456d716）把索引库改成 `:memory:` 后
// 出现了一个无法回避的副作用：**系统里再没有任何「这个源读到哪」的记忆**。
// 后果是重启与首次部署在代码里完全无法区分：
//
//   - 若一律从文件末尾起读 ⇒ 新部署/换绑定时，该文件里已有的日志**永远不会被采集**；
//   - 若一律从文件头起读 ⇒ 每次重启都把整份历史日志**重读重投 VL**（生产源文件几百 MB，机械盘）。
//
// 用户选了第三条：保留**极小**持久化——每个源一行几十字节，能同时满足「新源采全」与
// 「重启不重复」。
//
// ## 这个文件为什么不是「又一套账本」
//
// 它只存 path、inode（含 device）、文件内 cursor，以及**源登记所需的字段**。
// 它**不记录**：投递证据、缺口、四水位、分段、暂停——那些一概没有。
// 它回答两个问题：这个源是谁（采哪个文件、属于哪个命名空间）、上次读到第几字节。
// 所以它是一本 **offset registry**（对齐 Filebeat 的 registry 文件 / Fluent Bit 的 db 语义），
// 不是账本。
//
// 2026-10-30 第二轮补充（源配置跨重启丢失，缺陷实证 `SourceConfigs=0 pipes=0 游标表=1`）：
// 第一轮只存游标，默认「源配置由索引库负责」；而索引库同样是 `:memory:`，于是重启后
// 游标记着「读到 12345 字节」，系统却不知道「读的是哪个文件」。因此每行加上了
// 源登记字段（见 cursor_registry_row.go 的列布局）。加进来的只是「这个源是谁」与
// 「读到哪」——一对必须同生共死的事实：分开存会出现「有游标无源」或「有源无游标」
// 两种都必须特殊处理的半成品状态。
//
// ## 盘上开销
//
// 一次写整个文件（每个源一行，20 源约 2KB）。**不 fsync**：掉电丢的是最后最多一个周期的进度，
// 后果只是那段重读一遍（可能重复投递），不会丢数据。这个取舍是刻意的——
// fsync 在机械盘上就是寻道+旋转惩罚，而那正是用户要避开的东西。

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
)

// cursorRegistry 是内存态的游标表：key = "log_source_id/source_generation"。
//
// 读写都在 Manager.mu 之外由调用方串行化（只会在登记路径与落库节拍上碰，不是高频路径）。
type cursorRegistry map[string]cursorRegistryEntry

// cursorRegistryEntry 是游标表的一行：**游标**（必有）+ **源配置**（可选，nil = 这一行不带）。
//
// ## 为什么源配置也要落在这里（2026-10-30 第二轮：源配置跨重启丢失）
//
// 第一轮（f5b882f0）只存了游标，理由是「源配置由索引库负责」。但 P1 之后索引库是
// `:memory:` SQLite ⇒ 重启后 `m.state` 是空库 ⇒ `Register` 里 `m.state.Sources[key]`
// 恒不命中，而实例源本来就不在 `opts.Sources` 里（那条路是 CP 登记时才产生的）⇒
// 采集器重启后**根本不知道该采哪个文件**：游标里记着「读到第 12345 字节」，却没有
// 任何地方记得「读的是哪个文件、属于哪个源」。实测探针：重启后
// `SourceConfigs=0 pipes=0 游标表=1`。
//
// 于是这张表从纯粹的 offset registry 升为「offset + 源登记表」：它同时回答
// 「采哪个文件」与「读到第几字节」，重启后据此把源重建起来。代价是每源多约 80 字节。
type cursorRegistryEntry struct {
	// 游标部分内嵌：字段访问仍是 entry.Path / entry.Cursor，语义与第一轮完全一致。
	acquire.SourceCursor
	// Source 是**显式口径**的源配置（对应 m.state.SourceConfigs，不是 m.sources）。
	//
	// 为什么必须是显式口径而不是生效口径：Charset/TimeZone 的生效值里可能混着
	// 「本次运行继承来的节点默认」，把它落进表里等于把继承值固化成显式值，之后改
	// `log_ingest.time_zone` 重启对已登记源永不生效（defaults_restore_test.go 第 2 轮钉的
	// 就是这个语义）。空串 = 跟随节点默认。
	Source *SourceConfig
}

// cursorRegistryPath 返回游标文件路径（与索引库同目录，便于一起备份/清理）。
func (m *Manager) cursorRegistryPath() string {
	return filepath.Join(filepath.Dir(m.indexPath()), "ingest.cursor")
}

// loadCursorRegistry 从磁盘读游标表。
//
// 文件不存在、不可读、内容损坏 ⇒ 一律返回空表 + nil：
// 游标是**可丢失的优化**（丢了最多重读一段），绝不能因为读不回来而拒绝启动采集。
func loadCursorRegistry(path string) cursorRegistry {
	out := cursorRegistry{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, entry, ok := parseCursorRegistryRow(strings.Split(line, "\t"))
		if !ok {
			continue // 单行坏不牵连整表：跳过它，其余照用
		}
		out[key] = entry
	}
	return out
}

// saveCursorRegistry 把**整张表**写回磁盘（约 2KB/20 源）。
//
// 写法：临时文件 + rename。rename 在同一目录内是原子的 ⇒ 读方永远看到要么旧的完整表、
// 要么新的完整表，不会读到写了一半的行。
// **不 fsync**（见文件头）：掉电丢最多一个周期的进度，代价是那一段重读。
func saveCursorRegistry(path string, reg cursorRegistry) error {
	// 目录可能还不存在（首次部署、或测试的临时 root）——建目录比让游标永远写不进去合理。
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	_, _ = w.WriteString(cursorRegistryHeaderV2 + "\n")
	// 按 key 排序写：表本身无序，但固定顺序让「同内容两次写出的文件逐字节相同」，
	// 排障时可以直接 diff 两次落盘结果（源数 20 量级，排序成本可忽略）。
	keys := make([]string, 0, len(reg))
	for key := range reg {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := writeCursorRegistryRow(w, key, reg[key]); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// （已删除 syncCursorRegistry / logSourceKey：两者全仓库无调用方，而 syncCursorRegistry 的
// 注释还声称「只在登记时与落库节拍调用」——实际游标写入只有 flushCursorRegistry 一处，
// 留着会让读者以为还有第二条写入路径。主键统一用 key 字符串，见 Register 处的拼接。）
