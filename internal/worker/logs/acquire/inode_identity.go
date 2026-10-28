package acquire

// inode 判据（2026-10-28，对齐 Filebeat 的 file_identity: inode_deviceid）。
//
// ## 它替代的是什么
//
// 旧实现回答“这个路径上的文件还是不是我上次读到那一份”用的是**内容锚**
// （见 content_anchor.go）。那套机制是为“跨文件已删除仍能认身份”设计的——
// 因为旧设计把**历史归档也当作采集源**，于是必须用内容哈希这种不依赖文件存在性的身份。
//
// 代价已经在生产上付出来了：早于 2026-10-21（b75b10db 引入内容锚）的旧账本
// 没有任何映射证据 ⇒ `ResolveContentAnchor` 永远找不到锚 ⇒ 一律 ARCHIVE_CONTENT_UNKNOWN
// ⇒ `pendingArchives` 对“ImportError 非空且文件未变”永久跳过 ⇒ 形成死循环。
// 实测：inst:317 的 105 个分段里 **0 个**带身份、当中 89 个被标 UNKNOWN，
// 19 个源里 10 个因此停采。
//
// ## 为什么 inode 是对的
//
// 位点语境里真正要问的是“**是不是同一个文件**”，不是“内容像不像”：
//
//	改名（latest.log → latest.log.1 → *.log.gz）  inode **不变**  ⇒ 同一份文件
//	同名路径换了内容（轮转槽位复用）          inode **改变**  ⇒ 不是同一份
//
// 内容哈希把这两种情形都归为“内容不同/相同”，信息量比 inode 少一层。
// 业界（Filebeat 默认 `inode_deviceid`、Fluent Bit 默认 `db.compare_filename=false`）
// 用的就是 inode，本文件只是把本项目对齐到同一口径。
//
// ## 为什么还要内容前缀
//
// inode 会被文件系统**复用**：文件被删后，新文件可能拿到同一个号。
// 所以只在“inode 相同 **且** 内容前缀也相同”时才认定同内容——两者都合才是真正同一份。
// 任一条件不足（旧账本无 inode / 内容前缀不符）就**返回 false**，
// 调用方回退到原有判据（内容锚），行为与接线前完全一致。

import (
	"os"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// inodeMatchesSegment 报告 segment 记录的文件身份与磁盘当前状态是否指向同一份内容。
//
// 三个必要条件（缺一即 false，且**不产生任何副作用**）：
//  1. 分段记录过 inode（InodeKnown）——旧账本没记过，不能把“没记”当成“匹配”；
//  2. 文件当前仍可取到 inode，且 (device, inode) 与记录一致；
//  3. 内容前缀也对得上（防 inode 复用）；分段没有内容身份时跳过这一条——
//     此时 inode 是唯一证据，仍然采信（旧账本补建身份前后都能工作）。
func inodeMatchesSegment(segment *ledger.Segment) bool {
	if segment == nil || !segment.InodeKnown {
		return false
	}
	// 设备号 + inode 全为 0 的“已知”是无意义记录（某些伪文件系统），不采信。
	if segment.DeviceID == 0 && segment.Inode == 0 {
		return false
	}
	info, err := os.Stat(segment.Path)
	if err != nil {
		return false
	}
	dev, ino, ok := fileIdentity(info)
	if !ok {
		return false // 本平台无 inode 语义（Windows）：回退到原有判据
	}
	if dev != segment.DeviceID || ino != segment.Inode {
		return false
	}
	// inode 相同：再用内容前缀扣一次 inode 复用。
	// 分段无内容身份时跳过此条（inode 是唯一证据）；有身份则必须对得上。
	if segment.IdentityBytes > 0 && segment.IdentitySHA256 != "" {
		if info.Size() < segment.IdentityBytes {
			return false // 文件比记录的前缀还短：内容确定变了
		}
		sum, sumErr := filePrefixSHA256(segment.Path, segment.IdentityBytes)
		if sumErr != nil || sum != segment.IdentitySHA256 {
			return false
		}
	}
	return true
}
