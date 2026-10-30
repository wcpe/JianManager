package acquire

import "os"

// SourceCursor 是一个源「上次读到这里」的最小持久化表示（2026-10-30 裁定 D）。
//
// 它只有四个字段，因为它的唯一职责就是回答「重启后从这个文件的第几个字节继续读」。
// 它刻意**不包含**投递证据、缺口、四水位、暂停——那些不在最小持久化的范围内。
//
// DeviceID+Inode 用于识别「这个路径上的文件还是不是上次那一份」：
// 日志轮转（rename）会让路径指向新文件，inode 改变 ⇒ 游标作废，应从头读新文件。
type SourceCursor struct {
	// Path 是采集时观察到的文件路径。路径变了说明绑定改了，游标不适用。
	Path string
	// DeviceID / Inode 是文件物理身份；0/0 表示平台拿不到（Windows），此时靠 Path+Cursor 兜底。
	DeviceID uint64
	Inode    uint64
	// Cursor 是**文件内字节偏移**（不是逻辑位置）。恢复时若文件已短于它，说明被截断，只能从头读。
	Cursor int64
	// LogicalPos 是对应的**逻辑源位置**（跨轮转累计），恢复后新事件的 record_start 从它继续，
	// 以保证与重启前已投递的字节不重叠。
	LogicalPos uint64
}

// ResumeFrom 从持久化游标恢复采集起点。
//
// ## 决策表（每条都必须能独立解释，否则重启就会丢字节或重复投递）
//
//	Path 变了         ⇒ 当作新文件，从头读（绑定换了，旧游标无关）
//	文件不存在         ⇒ 保持现状，等文件出现后由首轮 Poll 接管
//	有 inode 且不匹配   ⇒ 已轮转/被替换：从**当前文件头**读（新文件内容还没采过）
//	文件比游标短        ⇒ 被截断：只能从头读
//	其余             ⇒ 从游标继续，逻辑位置接着 LogicalPos
//
// 与 AnchorAtEOF 的关系：**两者互斥**。AnchorAtEOF 是「这个源从没采过」的默认起点；
// ResumeFrom 是「采过，而且找得到上次的位置」。登记路径先查游标表，找到就 ResumeFrom，
// 找不到（首次部署/换绑定）才退回 AnchorAtEOF。
func (t *FileTailer) ResumeFrom(c SourceCursor) error {
	if c.Path != t.path {
		return nil // 绑定换了：旧游标不属于这个文件，保持从头读
	}
	fi, err := os.Stat(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	// 物理身份比对：inode 变了就是另一份文件（轮转/同名替换），旧游标不可用。
	if c.DeviceID != 0 || c.Inode != 0 {
		if dev, ino, ok := fileIdentity(fi); ok && (dev != c.DeviceID || ino != c.Inode) {
			return nil
		}
	}
	if fi.Size() < c.Cursor {
		return nil // 被截断：旧游标在文件尾之后，只能从头读
	}
	t.cursor = c.Cursor
	t.lastSize = c.Cursor
	t.logicalPos = c.LogicalPos
	t.active = false
	// 已有可信游标 ⇒ 映射前提成立（逻辑 ↔ 物理的对应关系由游标本身证明）。
	t.physicalMappingKnown = true
	t.physicalStart = 0
	return nil
}

// Cursor 返回当前游标快照（供持久化）。
//
// 取 inode 是 O(1) 的 stat；拿不到身份（Windows）时返回 0/0，恢复侧退化为只靠 Path+Cursor。
func (t *FileTailer) Cursor() SourceCursor {
	out := SourceCursor{Path: t.path, Cursor: t.cursor, LogicalPos: t.logicalPos}
	if fi, err := os.Stat(t.path); err == nil {
		if dev, ino, ok := fileIdentity(fi); ok {
			out.DeviceID, out.Inode = dev, ino
		}
	}
	return out
}
