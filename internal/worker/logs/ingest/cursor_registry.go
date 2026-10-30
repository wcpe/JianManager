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
// 它只存三样东西：path、inode（含 device）、文件内 cursor。
// 它**不记录**：投递证据、缺口、四水位、分段、暂停——那些一概没有。
// 它唯一回答的问题是「上次读到这个文件的第几个字节」。所以它是一本 **offset registry**
// （对齐 Filebeat 的 registry 文件 / Fluent Bit 的 db 语义），不是账本。
//
// ## 盘上开销
//
// 一次写整个文件（每个源一行，20 源约 2KB）。**不 fsync**：掉电丢的是最后最多一个周期的进度，
// 后果只是那段重读一遍（可能重复投递），不会丢数据。这个取舍是刻意的——
// fsync 在机械盘上就是寻道+旋转惩罚，而那正是用户要避开的东西。

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
)

// cursorRegistry 是内存态的游标表：key = "log_source_id/source_generation"。
//
// 读写都在 Manager.mu 之外由调用方串行化（只会在登记路径与落库节拍上碰，不是高频路径）。
type cursorRegistry map[string]acquire.SourceCursor

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
		// 格式：key<TAB>path<TAB>dev<TAB>ino<TAB>cursor<TAB>offset
		fields := strings.Split(line, "\t")
		if len(fields) != 6 {
			continue // 单行坏不牵连整表：跳过它，其余照用
		}
		dev, err1 := strconv.ParseUint(fields[2], 10, 64)
		ino, err2 := strconv.ParseUint(fields[3], 10, 64)
		cursor, err3 := strconv.ParseInt(fields[4], 10, 64)
		offset, err4 := strconv.ParseUint(fields[5], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			continue
		}
		out[fields[0]] = acquire.SourceCursor{
			Path: fields[1], DeviceID: dev, Inode: ino,
			Cursor: cursor, LogicalPos: offset,
		}
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
	_, _ = w.WriteString("# jianmanager ingest cursor registry v1\n")
	for key, c := range reg {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\n",
			key, c.Path, c.DeviceID, c.Inode, c.Cursor, c.LogicalPos); err != nil {
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
