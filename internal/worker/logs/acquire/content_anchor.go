package acquire

// 内容锚（content anchor）——归档「槽位复用」判据的真源。
//
// ## 要治的缺陷
//
// 归档「已导入」的判据原本**只有路径**：账本分段 `Imported=true` 即整包跳过
// （见 importArchive 的跳过分支），而账本分段按路径 upsert、**从不删除**。
// BungeeCord 的 java.util.logging `%g` 是**槽位复用**语义（`proxy.log.0` 是活文件，
// 轮转时 `.0→.1`、`.1→.2` 移位改名），于是同一个路径上的内容会不断被换掉：
//
//   - 主流形态（进程活着）：落进槽位的新内容正是原活文件内容，已被 FileTailer 读过并投递；
//     此时跳过是正确的（不重复导入）。
//   - 丢失窗口（Worker 停机期间跨过轮转）：落进槽位的内容**从未被任何链路读过**，
//     或被读过但**从未 durable 投递**（停机瞬间的读尾）。按路径判「已导入」⇒ 静默丢日志。
//
// ## 为什么判据不能是「字节数/文件长度」
//
// 曾经试过「已消费字节数 vs 文件长度」：真实轮转下槽位里的新内容**从头**就是已投递过的
// 字节（它整份就是原活文件），长度比较会把「恰好等长」判成已覆盖、把「变长」判成未覆盖，
// 两种都错——上一轮实测它会把**已投递内容重导一遍**。长度不是内容身份：它既不回答
// 「这是不是同一份内容」，也不回答「这份内容已经投递到哪个字节」。
//
// ## 判据：内容身份 + 内容自己的逻辑锚
//
// 账本里每条分段都记录了「它当时看到的内容前缀身份」（IdentityBytes + IdentitySHA256，
// 由 FileTailer 在观察活文件/轮转落点时写入）。前缀身份是**内容的函数**，因此可以跨路径匹配：
//
//  1. 算出待导入文件的内容身份（前缀 ≤512B 的 SHA256 + 长度）。代价是**有界小读**，
//     绝不整文件读一遍（归档可能上百 MB，而本判据每轮采集都要跑）。
//  2. 在**整条账本**（不限路径）里找记录同一身份的锚。找到即证明「这份内容在这个逻辑源的
//     哪个位置」——`Base` = 内容第 0 字节对应的逻辑位置，`Covered` = 已投递覆盖到的位置。
//  3. 已覆盖字节数 = Covered − Base。它 ≥ 文件长度 ⇒ 整份都已投递/导入 ⇒ 跳过（不重复导入）；
//     否则 ⇒ 还有**未被任何链路投递过**的尾巴 ⇒ 必须从 `Base + 已覆盖` 起导入。
//
// 为什么按内容而不是按路径找锚（2026-10-22 实测）：移位改名会把**已导入的内容往下挪一格**，
// 那个槽位的分段记录的是它上一轮导入的**别的**内容。若只按「路径上的分段」取锚，就会拿
// 别人的 Base 去跳过本文件的前缀——实测产生过 1 字节的碎片事件（把 `...-old` 的尾巴当成
// 一条日志导入）。按内容找锚天然免疫槽位错位。
//
// 找不到锚不等于已覆盖：陌生内容不能盲目导入，也不能成功消失。
// 裁定返回未知，由发现与导入共用的 AssessArchive 登记稳定可见缺口并保留原物。

import (
	"os"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// contentIdentityBytes 内容身份窗口长度上限（与 FileTailer 的稳定前缀口径一致）。
//
// 为什么是 512：FileTailer 记录活文件身份时用的就是 min(文件长度, 512)（见 PrepareRotation），
// 沿用同一口径才能与账本里已有的锚**逐字匹配**——换一个窗口长度会让所有存量锚失配，
// 判据退化成「永远找不到锚 ⇒ 永远跳过」。
const contentIdentityBytes int64 = 512

// ContentAnchor 描述「某一内容」在逻辑源里的位置。
type ContentAnchor struct {
	// Base 该内容第 0 字节对应的逻辑位置。
	Base uint64
	// PhysicalStart 是逻辑 Base 对应的物理起点；重锚前的存活前缀不能再分配新身份。
	PhysicalStart uint64
	// Covered 该内容已投递/已导入覆盖到的逻辑位置（exclusive）。
	Covered uint64
	// Path 提供该锚的账本分段路径（诊断用）。
	Path string
}

// CoveredBytes 返回该内容已被覆盖的字节数（有界到 [0, size]）。
func (a ContentAnchor) CoveredBytes(size int64) int64 {
	if a.Covered < a.Base {
		return 0
	}
	span := a.PhysicalStart + a.Covered - a.Base
	if size >= 0 && span >= uint64(size) {
		return size
	}
	return int64(span)
}

// ArchiveDisposition 明确区分已覆盖、可按原锚补尾和无法证明归属三种状态。
type ArchiveDisposition string

const (
	ArchiveCovered ArchiveDisposition = "covered"
	ArchiveTail    ArchiveDisposition = "tail"
	ArchiveUnknown ArchiveDisposition = "unknown"
	// ArchiveUnknownReason 不在可自动消解允许名单内，须恢复归属证据或人工裁定。
	ArchiveUnknownReason = "ARCHIVE_CONTENT_UNKNOWN"
)

// ReimportDecision 是「已导入分段是否必须重新导入」的裁定结果。
type ReimportDecision struct {
	Disposition ArchiveDisposition
	// Import 为 true 表示必须导入（槽位内容已被换掉，且内容锚证明还有未投递字节）。
	Import bool
	// Anchor 该内容自己的锚（Import 为 true 时有效）。
	Anchor ContentAnchor
	// CoveredBytes 该内容已投递覆盖的字节数（Import 为 true 时有效）。
	CoveredBytes int64
	// Reason 判定的可观测理由（跳过时写进 ImportResult.Reason，便于现场排障）。
	Reason string
}

// DecideArchiveReimport 裁定一个 **Imported=true** 的分段是否必须重新导入。
//
// 这是「槽位复用」判据的**唯一真源**：归档发现（ingest.pendingArchives）与归档导入
// （importArchive 的整包跳过分支）必须调用同一个函数，否则两处会漂移成
// 「发现说待导入、导入说已导入」（白做功）或反过来的「发现说已导入、导入没机会判」（静默丢）。
//
// 三条分支（顺序即优先级）：
//  1. 内容没变（分段记录的前缀身份 == 文件当前前缀身份）⇒ 跳过。这是主流形态，
//     也是「已投递内容不得重复导入」的守护点。
//  2. 内容变了 / 分段是旧账本（无身份）⇒ 找内容锚。找不到 ⇒ 未知，必须登记可见隔离。
//  3. 找到锚 ⇒ 算已覆盖字节数：覆盖满 ⇒ 跳过（它是被移位改名挪过来的已投递内容）；
//     没覆盖满 ⇒ 导入（剩下的是**从未被任何链路投递过**的字节）。
func DecideArchiveReimport(entry *ledger.Entry, segment *ledger.Segment) ReimportDecision {
	if segment == nil {
		return ReimportDecision{Disposition: ArchiveTail, Import: true, Reason: "archive has not been registered"}
	}
	if !segment.Imported {
		if !segment.PhysicalMappingKnown && (segment.StartPos != 0 || segment.EndPos != 0 || segment.IdentityBytes > 0 || segment.IdentitySHA256 != "" || segment.ArchiveObjectID != "") && (ledger.IsArchiveSegmentKind(segment.Kind) || segment.Kind == ledger.SegmentRotated) {
			return ReimportDecision{Disposition: ArchiveUnknown, Reason: "archive checkpoint has no proven physical/logical mapping"}
		}
		// 进行中的归档也可能被槽位复用；只有内容身份未变化才可沿用旧断点。
		// tailer 的 rotated 明文可合法转换为 gzip，不能把编码变化错判为陌生归档。
		if !ledger.IsArchiveSegmentKind(segment.Kind) || segment.IdentityBytes <= 0 || segment.IdentitySHA256 == "" {
			return ReimportDecision{Disposition: ArchiveTail, Import: true, Reason: "segment is not marked imported"}
		}
		if sum, err := filePrefixSHA256(segment.Path, segment.IdentityBytes); err == nil && sum == segment.IdentitySHA256 {
			return ReimportDecision{Disposition: ArchiveTail, Import: true, Reason: "same archive content has an unfinished checkpoint"}
		}
	}
	size, err := archiveFileSize(segment.Path)
	if err != nil {
		// stat 失败：文件可能刚被轮转删除。保持跳过——不能让一次瞬时的 stat 失败
		// 变成「每轮重试同一个不存在的归档」。
		return ReimportDecision{Disposition: ArchiveUnknown, Reason: "archive unavailable; coverage cannot be proved"}
	}
	if size == 0 {
		// 空归档（java.util.logging 的 0 字节 `.lck` 就是这种）：没有字节可导。
		return ReimportDecision{Disposition: ArchiveCovered, Reason: "empty archive; nothing to import"}
	}
	// ① 内容没变（快路：已导入记录自己就是这份内容的锚）。
	if segment.IdentitySHA256 != "" && segment.IdentityBytes > 0 && size >= segment.IdentityBytes {
		if sum, idErr := filePrefixSHA256(segment.Path, segment.IdentityBytes); idErr == nil && sum == segment.IdentitySHA256 &&
			(segment.ObservedSize == size || (segment.ObservedSize == 0 && segment.EndPos >= segment.StartPos && segment.EndPos-segment.StartPos >= uint64(size))) {
			return ReimportDecision{Disposition: ArchiveCovered, Reason: "same content already imported; whole-archive reimport forbidden"}
		}
	}
	// ②③ 内容变了 / 旧账本：只能靠内容锚证明「还有没人投递过的字节」。
	durable := uint64(0)
	if entry != nil {
		durable = entry.Positions.Durable
	}
	anchor, ok := ResolveContentAnchor(entry, segment.Path, size, durable)
	if !ok {
		// 未知内容保持原物，不分配新逻辑身份；调用方必须登记可见隔离，不能冒充已覆盖。
		return ReimportDecision{Disposition: ArchiveUnknown, Reason: "no content anchor for current slot content; archive quarantined for ownership recovery"}
	}
	covered := anchor.CoveredBytes(size)
	if covered >= size {
		return ReimportDecision{
			Disposition: ArchiveCovered,
			Reason:      "slot content already covered by delivered bytes; whole-archive reimport forbidden",
		}
	}
	return ReimportDecision{
		Disposition:  ArchiveTail,
		Import:       true,
		Anchor:       anchor,
		CoveredBytes: covered,
		Reason:       "slot content changed and still has undelivered bytes",
	}
}

// archiveFileSize 取归档文件长度（stat 失败即返回错误）。
func archiveFileSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// ContentIdentity 计算文件的内容身份：min(文件长度, 512) 字节的前缀 SHA256 与长度。
//
// 返回 (identityBytes, identitySHA256)。空文件返回 (0, "")。
// 代价：一次有界小读（≤512B），与归档大小无关。
func ContentIdentity(path string) (int64, string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, "", err
	}
	size := fi.Size()
	if size <= 0 {
		return 0, "", nil
	}
	window := size
	if window > contentIdentityBytes {
		window = contentIdentityBytes
	}
	sum, err := filePrefixSHA256(path, window)
	if err != nil {
		return 0, "", err
	}
	return window, sum, nil
}

// ResolveContentAnchor 在**整条账本**里为某个文件找内容锚。
//
// size 是文件当前长度（调用方已 stat 过，避免重复 stat）。
// 找不到唯一可信的锚时返回 ok=false —— 调用方必须据此保持「跳过」（见文件头注释）。
//
// 匹配口径：分段记录的 (IdentityBytes, IdentitySHA256) 必须是该文件**前缀**的摘要。
// 同一份内容可能在账本里留下多条锚（活文件分段 + 轮转落点分段 + 归档分段），
// 此时取**已覆盖字节数最多**的那条：覆盖得更多意味着「已投递得更多」，宁可少导也不要重复导。
//
// durable 传入逻辑源的已确认投递水位：`Covered` 取 min(分段.EndPos, durable)。
// 为什么不能直接用分段 EndPos：FileTailer 的 EndPos 是**读位置**，读到的字节可能还没 durable
// （WAL 追加失败/停机在提交前）。把读位置当已投递会跳过从未落库的字节 ⇒ 静默丢数据。
// 实测形态：停机瞬间 `read=83 / durable=56`，差的那 27 字节正是本能力要救回来的内容。
func ResolveContentAnchor(entry *ledger.Entry, path string, size int64, durable uint64) (ContentAnchor, bool) {
	if entry == nil || size <= 0 {
		return ContentAnchor{}, false
	}
	// 按窗口长度分组：同一窗口只读一次文件（窗口种类极少：512 与大文件长度）。
	memo := make(map[int64]string, 4)
	best := ContentAnchor{}
	bestCovered := int64(-1)
	for i := range entry.Segments {
		seg := &entry.Segments[i]
		if !seg.PhysicalMappingKnown || seg.IdentitySHA256 == "" || seg.IdentityBytes <= 0 {
			continue
		}
		window := seg.IdentityBytes
		if window > size {
			// 记录的前缀比文件还长：内容不可能相同（被截断/换成长度更短的内容）。
			continue
		}
		sum, ok := memo[window]
		if !ok {
			var err error
			sum, err = filePrefixSHA256(path, window)
			if err != nil {
				return ContentAnchor{}, false
			}
			memo[window] = sum
		}
		if sum != seg.IdentitySHA256 {
			continue
		}
		covered := seg.EndPos
		if durable < covered {
			// 读位置尚未 durable：只认已确认投递的部分（见函数头注释）。
			covered = durable
		}
		if covered < seg.StartPos {
			continue
		}
		candidate := ContentAnchor{Base: seg.StartPos, PhysicalStart: seg.PhysicalStart, Covered: covered, Path: seg.Path}
		// 按「已覆盖字节数」比较而不是按绝对位置：不同锚的 Base 可能不同，
		// 比绝对位置会挑到「Base 更大但实际覆盖更少」的锚 ⇒ 多导 ⇒ 重复入库。
		if coveredBytes := candidate.CoveredBytes(size); coveredBytes > bestCovered {
			best = candidate
			bestCovered = coveredBytes
		}
	}
	return best, bestCovered >= 0
}
