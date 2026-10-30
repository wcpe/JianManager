package acquire

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// FileTailer 从源文件 tail 采集：纯内存 cursor + 轮转检测。
// Multiline 未完成事件只推进 read_position，不推进 durable。
// defaultMaxLinesPerPoll 是单次 Poll 的读取行数上限（B1b **准入限流**）。
//
// 为什么必须有：一次 pass 读取无界会把整份文件攒成**一个超大批判**交给上层，而 WAL 积压上限
// 是在**追加之后**检查的——单批即穿透上限，暂停只对后续批次生效。生产事故（2026-09-28）正是
// 该形态：单源 10 万行一次性入库，状态文件被撑到 1.2 GB、每次持久化全量重写。
// 取 2000（约为默认积压上限 5000 条的 40%），使单批无法穿透上限；到额度即停在**行边界**，
// 游标停在已处理位置，剩余部分留待下轮续读，不丢数据。
const defaultMaxLinesPerPoll = 2000

type FileTailer struct {
	led    *ledger.Ledger
	key    ledger.SourceKey
	path   string
	hook   EventBoundaryHook
	wal    *WAL
	parser logtypes.SourceIdentity

	// cursor 当前文件内偏移（纯内存，不跨进程恢复；启动起点由 AnchorAtEOF 决定）。
	cursor int64
	// logicalPos 逻辑源位置（跨轮转累计）。
	logicalPos uint64
	// lastSize 上次观察到的文件大小，用于检测截断/轮转。
	lastSize int64
	// active 是否已打开过文件。
	active bool
	// segmentStart 是当前 live 文件在逻辑源中的基址；cursor 始终是文件内偏移。
	segmentStart uint64
	// physicalStart 是 segmentStart 对应的物理字节偏移，纯截断重锚后不再为零。
	physicalStart        uint64
	physicalMappingKnown bool
	// identity 固定取首次观察到的前缀，追加写不会改变；同路径替换时用于检测轮转。
	identityBytes  int64
	identitySHA256 string
	// identityDev/identityIno 是首次观察到的文件**物理身份**（同名替换时必然改变）。
	// identityOK 为 false 表示本平台拿不到身份，此时只靠「尺寸 + 前缀哈希」判据。
	identityDev uint64
	identityIno uint64
	identityOK  bool
	// tailSHA256 / tailHashAt（2026-10-06 原地重写检测 ✗✗）：[cursor-512, cursor) 尾窗指纹与其采点。
	// 既有检测只覆盖「首 512B + 物理身份」⇒ 对"保头吞尾"的原地重写结构性看不见 ✗（实例日志栈实测：
	// 文件头不动、尾部区域被吞 ⇒ 偏移漂移 ⇒ IDENTITY_CONFLICT ⇒ 水位钉死 ✗）。每次 Poll 收束后刷新 ✓。
	tailSHA256 string
	tailHashAt int64
	// rotateTo 轮转目标路径模板（可选）；检测到截断时登记关联。
	rotateTo string
	// rotationCount 轮转次数（测试断言）。
	rotationCount int
	// eventsEmitted 完整事件计数（用于轮转不双计验证）。
	eventsEmitted int
	// sourceCategory instance|worker|node；source=worker 同管道。
	sourceCategory logtypes.Source
	// maxLinesPerPoll 单次 Poll 的读取行数上限（B1b 准入限流）；0 表示用默认常量。
	// 作为字段以便测试用小额度覆盖真实限额路径，而非只断言常量本身。
	maxLinesPerPoll int
	// readLimits 在读取原始正文前限制单行与单轮字节，独立于追加后的 WAL 限额。
	readLimits ReadLimits
	// pendingPhysicalLine 只记录活文件半行的存在；持久游标仍留在半行首部，重启可原样重拼。
	pendingPhysicalLine bool
	// closedEvents 保存轮转闭合时由逐行钩子产出的事件，下一次 Poll 统一返回。
	closedEvents []logtypes.Event
	// truncationReanchored 标记「刚做过一次纯截断重锚」。
	// 为什么需要：重锚发生在 PrepareRotation 里，而 PrepareRotation 由 ingest 在 Poll 之前显式调用，
	// 调用方拿到 false（不是轮转）后紧接着就会 Poll。此时游标已越过歧义区，本轮再走后面的
	// 重写/水位指纹校验只会对同一段歧义区重复重锚并重复记缺口。故让 Poll 认这个标记并直接收束，
	// 与 Poll 内原地重写链「本轮不读歧义区、下轮起从新锚续读」保持同一硬序。
	truncationReanchored bool
}

// SetMaxLinesPerPoll 覆盖单次 Poll 的读取行数上限（0 表示沿用默认）。供测试与后续配置化使用。
func (t *FileTailer) SetMaxLinesPerPoll(n int) { t.maxLinesPerPoll = n }

// SetReadLimits 设置读取前的独立字节预算；失败不改变现有配置。
func (t *FileTailer) SetReadLimits(limits ReadLimits) error {
	normalized, err := limits.normalized()
	if err != nil {
		return err
	}
	t.readLimits = normalized
	return nil
}

// NewFileTailer 创建 tailer。hook 为 nil 时使用逐行 LineHook。
func NewFileTailer(led *ledger.Ledger, key ledger.SourceKey, path string, wal *WAL, hook EventBoundaryHook) *FileTailer {
	if hook == nil {
		hook = NewLineHook()
	}
	identity := logtypes.SourceIdentity{
		LogSourceID:      key.LogSourceID,
		SourceGeneration: key.SourceGeneration,
		ParserVersion:    "acquire-v1",
	}
	led.Ensure(key, identity)
	var live ledger.Segment
	if ent := led.Get(key); ent != nil {
		for _, seg := range ent.Segments {
			if seg.Path == path && seg.Kind == ledger.SegmentLive {
				live = seg
				break
			}
		}
	}
	if live.Path == "" {
		live = ledger.Segment{Path: path, Kind: ledger.SegmentLive, ParserVersion: identity.ParserVersion, PhysicalMappingKnown: true}
		_ = led.RegisterSegment(key, live)
	}
	t := &FileTailer{
		led:                  led,
		key:                  key,
		path:                 path,
		hook:                 hook,
		wal:                  wal,
		parser:               identity,
		sourceCategory:       logtypes.SourceInstance,
		readLimits:           DefaultReadLimits(),
		segmentStart:         live.StartPos,
		physicalStart:        live.PhysicalStart,
		physicalMappingKnown: live.PhysicalMappingKnown,
		identityBytes:        live.IdentityBytes,
		identitySHA256:       live.IdentitySHA256,
	}
	// cursor 纯内存（2026-10-30 P2.1）：不再从账本四水位重建。
	// 默认从文件头起读（构造函数的自然语义）；生产向的「不重读历史」由 pipeline 组装处
	// 显式调 AnchorAtEOF() 表达（见该方法注释）。
	t.cursor = 0
	t.lastSize = 0
	t.active = false
	return t
}

// AnchorAtEOF 把游标锚到当前文件末尾：本进程只采「自此之后」新写入的字节。
//
// ## 为什么必须由组装处显式决定
//
// 旧实现在 NewFileTailer 里按「账本 durable/read 四水位」恢复 cursor，前提是**账本跨重启存活**。
// P1（7456d716）已把索引库改成 `:memory:` SQLite —— 账本每进程启动都是空的，于是旧逻辑
// 得到 base=0 ⇒ cursor=0 ⇒ 每个源重启后都会把**整份历史日志重新读一遍并重新投递 VL**。
// 那不是恢复，是重复投递；源文件在生产上动辄几百 MB，机械盘上读一遍就是灾难。
//
// 用户裁定「直接读取到的往 vl 塞啊，内存队列满了就丢啊」——不承诺跨重启不丢，也就不需要
// 跨重启的游标。所以生产组装时一律调用本方法：**新进程只采自己出生之后写入的字节**。
//
// ## 代价（已知且接受）
//
// 进程停机期间源写入的字节不会被采集，也不做任何追溯。这是「不落盘」换来的直接后果，
// 与「有就有没有就算了」同一语义。
//
// ## 与 anchorAtEOFLocked 的区别
//
// 那个是**运行期**检测到重写/截断后的重锚，必须登记可见缺口（真的漏了字节）。
// 这里是**启动时**的起点选择：文件里既有内容不属于「被跳过的损失」，它在本进程出生前
// 就存在，故不记缺口、也不做身份重置（身份留给第一轮 PrepareRotation 去采）。
//
// ## 映射一致性
//
// 只推 cursor 与 logicalPos，不动 segmentStart/physicalStart：新进程的账本是空的，
// 活段总是 (StartPos=0, PhysicalStart=0)，于是「逻辑 = segmentStart + 文件内偏移」
// 与 physicalStart 的映射天然自洽（物理 size ↔ 逻辑 segmentStart+size）。
func (t *FileTailer) AnchorAtEOF() error {
	fi, err := os.Stat(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			// 文件尚未创建：cursor 留 0，首轮 Poll/PrepareRotation 会在文件出现后接管。
			// 那时从 0 读起正是同一个语义（文件是新的，内容都是新写入的）。
			return nil
		}
		return err
	}
	size := fi.Size()
	t.cursor = size
	t.lastSize = size
	t.logicalPos = t.segmentStart + uint64(size)
	t.active = false
	t.identityBytes, t.identitySHA256 = 0, ""
	t.identityDev, t.identityIno, t.identityOK = 0, 0, false
	t.tailSHA256, t.tailHashAt = "", 0
	// 让 checkPhysicalMapping 直接放行：刚 stat 到一个真文件，「逻辑 0 ↔ 物理 0」已成立。
	t.physicalMappingKnown = true
	t.physicalStart = 0
	return nil
}

// SetRotateTarget 设置轮转后的目标路径（用于登记 latest.log → rotated 关联）。
func (t *FileTailer) SetRotateTarget(path string) { t.rotateTo = path }

// SetSourceCategory 设置日志来源类别；source=worker 与实例同一采集管道。
func (t *FileTailer) SetSourceCategory(c logtypes.Source) { t.sourceCategory = c }

// LogicalPos 返回当前逻辑源位置。
func (t *FileTailer) LogicalPos() uint64 { return t.logicalPos }

// EventsEmitted 返回完整事件数。
func (t *FileTailer) EventsEmitted() int { return t.eventsEmitted }

// RotationCount 返回检测到的轮转次数。
func (t *FileTailer) RotationCount() int { return t.rotationCount }

// CurrentSegmentUnread reports whether no byte from the current live file has
// been consumed. The manager may prepend historical gzip segments only then.
func (t *FileTailer) CurrentSegmentUnread() bool {
	return t.cursor == 0 && t.logicalPos == t.segmentStart
}

// RebaseCurrentSegment moves an unread live file after newly imported historical
// segments. It never rewrites a file that has already contributed bytes.
func (t *FileTailer) RebaseCurrentSegment(pos uint64) error {
	if !t.CurrentSegmentUnread() {
		return fmt.Errorf("acquire: cannot rebase a consumed live segment")
	}
	t.segmentStart, t.logicalPos = pos, pos
	return t.registerLiveSegment()
}

// PrepareRotation checks size and the persisted stable prefix before reading the
// current path. This lets ArchiveImporter recover an unread rotated tail before
// bytes from the replacement latest.log receive logical positions.
func (t *FileTailer) PrepareRotation() (bool, error) {
	if err := t.checkPhysicalMapping(); err != nil {
		return false, err
	}
	fi, err := os.Stat(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			if t.rotateTo != "" {
				if _, rotateErr := os.Stat(t.rotateTo); rotateErr == nil && (t.cursor > 0 || t.pendingPhysicalLine) {
					if err := t.closeRotatedPhysicalLine(); err != nil {
						return false, err
					}
					t.detectRotationLocked()
					return true, nil
				}
			}
			return false, nil
		}
		return false, err
	}
	if t.identityBytes == 0 && fi.Size() > 0 {
		t.identityBytes = fi.Size()
		if t.identityBytes > 512 {
			t.identityBytes = 512
		}
		t.identitySHA256, err = filePrefixSHA256(t.path, t.identityBytes)
		if err != nil {
			return false, err
		}
		// 同一时刻记下物理身份；本平台拿不到时 identityOK=false，判据自动退回尺寸+哈希。
		t.identityDev, t.identityIno, t.identityOK = fileIdentity(fi)
		if err := t.registerLiveSegment(); err != nil {
			return false, err
		}
	}
	// 物理身份判据（用户质疑 1）：同名替换必然换 inode，这是唯一能无条件识破
	// 「替换文件更大、且开头与旧文件逐字节相同」的信号——尺寸判据与前缀哈希判据
	// 在那种形态下都会判为「未轮转」，导致新文件前 cursor 字节被静默跳过。
	// 只在已消费过字节（cursor > 0）时判定：cursor == 0 时没有需要接管的已读前缀。
	inodeReplaced := false
	if (t.cursor > 0 || t.pendingPhysicalLine) && t.identityOK {
		if dev, ino, ok := fileIdentity(fi); ok && (dev != t.identityDev || ino != t.identityIno) {
			inodeReplaced = true
		}
	}
	replaced := false
	if (t.cursor > 0 || t.pendingPhysicalLine) && t.identityBytes > 0 && fi.Size() >= t.identityBytes {
		current, hashErr := filePrefixSHA256(t.path, t.identityBytes)
		if hashErr != nil {
			return false, hashErr
		}
		replaced = current != t.identitySHA256
	}
	// **纯截断重写优先于轮转判定**（非原子截断：logrotate copytruncate / 应用自行 truncate；
	// 无 rename、inode 不变、尺寸回退到游标之前）。它不是轮转：既没有 .1 可以让 ArchiveImporter
	// 接管未读尾部，残留前缀又往往仍是**已投递过的同一批字节**。
	// 若按轮转处理（cursor 归零 + segmentStart=logicalPos），下一轮就会从文件头重读这段：
	// 物理内容照旧、逻辑位置却更高 ⇒ 同一批内容以**不同 event_id** 二次投递，而账本零缺口
	// （查询侧看不出重复，账本也认为一致）✗。故改走 reanchor 同族：锚到 EOF、禁止回头重读，
	// 并把可能丢失的尾部登记成**可见**缺口 ✓。
	if t.cursor > 0 && t.isInPlaceTruncationLocked(fi.Size(), replaced, inodeReplaced) {
		t.reanchorAfterTruncationLocked(fi.Size())
		// 返回 false：这不是轮转，调用方不得走「闭合旧段 / 接管轮转目标」的交接流程
		// （那会去找一个根本不存在的轮转段）。
		return false, nil
	}
	if (t.cursor > 0 || t.pendingPhysicalLine) && (fi.Size() < t.cursor || replaced || inodeReplaced) {
		if err := t.closeRotatedPhysicalLine(); err != nil {
			return false, err
		}
		t.detectRotationLocked()
		return true, nil
	}
	return false, nil
}

// isInPlaceTruncationLocked 判定「同 inode 的纯截断重写」（不是轮转）。四条判据须同时成立：
//
//  1. 尺寸回退到游标之前：尺寸**没有**回退的「保头吞尾」形态由 Poll 里的尾窗指纹那条链负责，
//     不在此处（否则两条链会抢同一个判定）。
//  2. 物理身份未变：inode 变了就是真正的同名替换（rename 轮转）——旧内容还在目标文件上，
//     必须走轮转语义，绝不能被当成「同一份内容」跳过 ✗。
//     拿不到物理身份（平台/文件系统不支持）时**不判定**：那时无法把「同名替换」与「原地截断」
//     分开，宁可沿用旧轮转语义（多投一次也好过把真轮转的新文件当成旧内容静默跳过）。
//  3. 没有可接管的轮转目标：copytruncate 的实质是「先复制、再截断」，此时未读尾部已被复制到
//     rotateTo 上、由 ArchiveImporter 接管 ⇒ 轮转语义本来就正确；若在此记缺口，等于给正常轮转
//     报假缺口，而 FILE_REWRITTEN 不在自动消解名单内，会把源永久阻塞。
//  4. 残留前缀仍是首次观察到的身份前缀（**逐字节相同**），或者短到没有可比对的窗口：
//     只有这样才能断言「残留字节就是已投递过的那些」。前缀已变的截断是「换了内容从头写」，
//     那段新字节必须从文件头读出来；若当作重复内容跳过，就变成静默丢内容 ✗。
func (t *FileTailer) isInPlaceTruncationLocked(size int64, replaced, inodeReplaced bool) bool {
	if t.cursor == 0 || size >= t.cursor {
		return false
	}
	if !t.identityOK || inodeReplaced {
		return false
	}
	if t.rotationTargetAvailable() {
		return false
	}
	comparable := t.identityBytes > 0 && size >= t.identityBytes
	return !comparable || !replaced
}

// rotationTargetAvailable 报告当前是否真的存在可接管的轮转目标。
// 口径与 PrepareRotation 的「源文件消失」分支一致：只有 rotateTo 真实存在才谈得上接管。
func (t *FileTailer) rotationTargetAvailable() bool {
	if t.rotateTo == "" {
		return false
	}
	_, err := os.Stat(t.rotateTo)
	return err == nil
}

// Poll 读取文件新增内容。返回本轮完整事件负载。
func (t *FileTailer) Poll() ([]logtypes.Event, error) {
	ent := t.led.Get(t.key)
	if ent == nil {
		return nil, fmt.Errorf("acquire: tailer source %s missing", t.key)
	}
	if ent.AcquirePaused {
		return nil, fmt.Errorf("acquire: tailer paused (%s)", ent.PauseReason)
	}
	if err := t.checkPhysicalMapping(); err != nil {
		return nil, err
	}
	_, err := t.PrepareRotation()
	if err != nil {
		return nil, err
	}
	// 本轮刚做过纯截断重锚：新锚已越过歧义区（残留前缀是已投递过的字节），本轮直接收束，
	// 不在同一轮里再走下面的重写/水位指纹校验（那会对同一段歧义区重复重锚、重复记缺口）。
	if t.truncationReanchored {
		t.truncationReanchored = false
		return nil, nil
	}
	events := t.closedEvents
	t.closedEvents = nil
	fi, err := os.Stat(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			// PrepareRotation 已完成一次幂等交接；活文件仍缺失时等待下一轮，不能递归重查同一路径。
			return events, nil
		}
		return nil, err
	}

	// **原地重写检测（2026-10-06 ✗✗）**：同 inode（Stat 未变）∧ 尺寸未倒退 ∧ **游标前 512B 窗口指纹不符**
	// ⇒ 判"保头吞尾"式重写 ✓。硬序：**先记可见缺口**（FILE_REWRITTEN，与 WAL_BODY_MISSING 同族 ✓，
	// 绝不静默 ✗）⇒ **跳过歧义区到 EOF 再锚**（不清零 cursor 重读：那会把已读前缀当新事件重灌 ✗）⇒
	// 段/身份重置（registerLiveSegment ✓，**不发 LinkRotation** ✗）⇒ 从新锚续读 ✓；逻辑位置继续累计 ✓。
	if t.cursor > 0 && fi.Size() >= t.cursor && t.tailSHA256 != "" && t.tailHashAt == t.cursor {
		from := t.cursor - 512
		if from < 0 {
			from = 0
		}
		if cur, herr := prefixSHA256At(t.path, from, t.cursor-from); herr == nil && cur != t.tailSHA256 {
			if err := t.reanchorAtEOFLocked(fi.Size(), "tailed file rewritten in place (same inode, header intact); re-anchored at EOF with visible gap"); err != nil {
				return events, err
			}
			return events, nil // 本轮不读歧义区；下轮起从新锚续读。
		}
	}

	if fi.Size() == t.cursor {
		return events, nil
	}

	f, err := os.Open(t.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if _, err := f.Seek(t.cursor, io.SeekStart); err != nil {
		return nil, err
	}

	limits := t.readLimits
	round := &roundReader{reader: f, remaining: limits.MaxBytesPerPoll}
	reader := bufio.NewReaderSize(round, 32<<10)
	lineStart := t.logicalPos

	// 准入限流（B1b）：本轮最多处理 maxLines 行，到额度即停在**行边界**——余下部分留在文件里、
	// 游标停在已处理位置，下轮续读。否则一次 pass 会把整份文件攒成超大批，单批穿透 WAL 上限。
	maxLines := t.maxLinesPerPoll
	if maxLines <= 0 {
		maxLines = defaultMaxLinesPerPoll
	}
	linesRead := 0

	// 为一整行留下额度，不能在单轮末尾把半行送进解析器，也不跨轮累积无界片段。
	for round.remaining+int64(reader.Buffered()) > int64(limits.MaxLineBytes) {
		line, err := readPhysicalLine(reader, limits.MaxLineBytes)
		if limitErr, ok := err.(*ReadLimitError); ok {
			limitErr.Path, limitErr.LogicalStart, limitErr.PhysicalFrom = t.path, lineStart, t.cursor
			return events, errors.Join(limitErr, t.recordReadLimit(limitErr))
		}
		if err != nil && err != io.EOF && err != errReadRoundBudget {
			return events, err
		}
		if len(line) == 0 {
			break
		}
		if line[len(line)-1] != '\n' {
			// 活文件的瞬时 EOF 不是物理行边界：原始字节留在源上，游标不跨过半行。
			t.pendingPhysicalLine = true
			break
		}
		t.pendingPhysicalLine = false
		linesRead++
		raw := physicalLineBody(line)
		absPos := lineStart
		lineEnd := absPos + uint64(len(line))

		eventEnd, complete, emit := t.hook.Feed(raw, absPos, lineEnd)
		// 逐行完整事件：record_end 取到换行之后，保证 durable cursor 与文件偏移一致。
		if complete && emit && eventEnd <= absPos+uint64(len(raw)) {
			eventEnd = lineEnd
		}
		if eventEnd == 0 {
			eventEnd = lineEnd
		}

		// 未完成多行：只推进 read_position，不 durable、不入 WAL。
		if !complete {
			if err := t.led.AdvanceRead(t.key, eventEnd); err != nil {
				return events, err
			}
			t.logicalPos = lineEnd
			lineStart = t.logicalPos
			t.cursor += int64(len(line))
			t.lastSize = fi.Size()
			t.active = true
			_ = t.registerLiveSegment()
			if err != nil && err != io.EOF {
				return events, err
			}
			if err == io.EOF {
				break
			}
			if linesRead >= maxLines {
				break // 准入限流（B1b）：余下部分留待下轮
			}
			continue
		}

		if emit {
			ev := logtypes.BuildEvent(t.parser, logtypes.RecordRange{Start: absPos, End: eventEnd},
				"", "", "", string(t.sourceCategory), string(raw))
			// event_time/ingest_time 由 normalize/上层填充；此处保留身份字段。
			events = append(events, ev)
			t.eventsEmitted++
		}

		if t.wal != nil && emit && len(events) > 0 {
			// 仅将本轮 complete 且 emit 的事件入 WAL；由调用方 Commit。
			_ = t.wal // Append 由 Poll 返回后由 Pipeline 统一处理，避免与 hook 双写
		}

		if err := t.led.AdvanceRead(t.key, eventEnd); err != nil {
			return events, err
		}
		t.logicalPos = lineEnd
		lineStart = t.logicalPos
		t.cursor += int64(len(line))
		t.lastSize = fi.Size()
		t.active = true
		_ = t.registerLiveSegment()

		if err != nil && err != io.EOF {
			return events, err
		}
		if err == io.EOF {
			break
		}
		if linesRead >= maxLines {
			break // 准入限流（B1b）：余下部分留待下轮续读，游标已停在行边界
		}
	}

	// 尾窗指纹刷新（原地重写检测的基准 ✓；每 Poll 一次，O(512) ✓）。
	t.refreshTailHashLocked()
	// 导出 normalize 协调点：cursor 与 read_position 已更新；durable 由 WAL Commit 推进。
	_ = filepath.Base(t.path)
	return events, nil
}

// FlushPartial 冲刷未完成多行缓冲。崩溃恢复语义：从 PendingStart 重新拼接。
func (t *FileTailer) FlushPartial() ([]byte, uint64, bool) {
	return t.hook.FlushPartial()
}

// PendingStart 未完成事件首行源位置。
func (t *FileTailer) PendingStart() uint64 { return t.hook.PendingStart() }

func (t *FileTailer) detectRotationLocked() {
	t.rotationCount++
	from := t.path
	to := t.rotateTo
	if to == "" {
		to = t.path + ".1"
	}
	// 同逻辑源：generation 不变。先只声明 durable 覆盖前缀；尚未闭合的
	// read 尾部仍由 ArchiveImporter 接管，不能因轮转检测而被跳过。
	covered := t.segmentStart
	if entry := t.led.Get(t.key); entry != nil && entry.Positions.Durable > covered {
		covered = entry.Positions.Durable
	}
	_ = t.led.LinkRotation(t.key, from, to, covered)
	_ = t.led.RegisterSegment(t.key, ledger.Segment{
		Path:                 to,
		Kind:                 ledger.SegmentRotated,
		StartPos:             t.segmentStart,
		PhysicalStart:        t.physicalStart,
		PhysicalMappingKnown: t.physicalMappingKnown,
		EndPos:               t.logicalPos,
		IdentityBytes:        t.identityBytes,
		IdentitySHA256:       t.identitySHA256,
		ParserVersion:        t.parser.ParserVersion,
	})
	t.segmentStart = t.logicalPos
	t.physicalStart, t.physicalMappingKnown = 0, true
	t.cursor = 0
	t.pendingPhysicalLine = false
	t.tailSHA256, t.tailHashAt = "", 0
	t.active = false
	t.identityBytes = 0
	t.identitySHA256 = ""
	// 物理身份一并作废：新文件要重新观察，否则下一轮会把「刚捕获的新身份」与
	// 「旧身份」比较，产生一次虚假轮转。
	t.identityDev, t.identityIno, t.identityOK = 0, 0, false
	_ = t.registerLiveSegment()
	// 逻辑位置继续累计，不归零（同一 generation 连续分段）。
}

// ConfirmRotationCoverage advances the linked prefix after Pipeline has closed
// the old segment EOF and durably committed it.
func (t *FileTailer) ConfirmRotationCoverage() error {
	to := t.rotateTo
	if to == "" {
		to = t.path + ".1"
	}
	entry := t.led.Get(t.key)
	if entry == nil {
		return fmt.Errorf("acquire: tailer source %s missing", t.key)
	}
	return t.led.LinkRotation(t.key, t.path, to, entry.Positions.Durable)
}

func (t *FileTailer) registerLiveSegment() error {
	return t.led.RegisterSegment(t.key, ledger.Segment{
		Path: t.path, Kind: ledger.SegmentLive, StartPos: t.segmentStart,
		PhysicalStart: t.physicalStart, PhysicalMappingKnown: t.physicalMappingKnown,
		EndPos: t.logicalPos, IdentityBytes: t.identityBytes,
		IdentitySHA256: t.identitySHA256, ParserVersion: t.parser.ParserVersion,
	})
}

// prefixSHA256At 计算文件 [from, from+size) 窗口的 SHA256（只读、有界 O(size) ✓）。
func prefixSHA256At(path string, from, size int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.CopyN(h, f, size); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// reanchorAtEOFLocked 把游标锚到 EOF 并登记**可见**缺口（w18 防护：禁止按旧偏移回读历史内容 ✗）——
// 缺的内容交给既有对账补齐 ✓。调用者负责本轮直接返回（不读歧义区 ✓）。
func (t *FileTailer) reanchorAtEOFLocked(size int64, detail string) error {
	end := t.logicalPos
	if size > t.cursor {
		end += uint64(size - t.cursor)
	}
	if err := t.led.RecordGap(t.key, t.logicalPos, end, ledger.GapReasonFileRewritten, detail); err != nil {
		return err
	}
	t.logicalPos = end
	t.anchorAtEOFLocked(size)
	return nil
}

// reanchorAfterTruncationLocked 是「纯截断重写」的重锚：与 reanchorAtEOFLocked 取同一套锚定动作，
// 但缺口区间按**真实损失**算，而不是按「被跳过的文件区间」算。
//
// 为什么必须区别对待：截断形态里旧的 cursor 大于新 size，若照搬 [cursor, size) 会得到一条
// 长度为 0 的退缺口（RecordGap 会把 end<start 夹平），等于「有损失却记了个空区间」✗。
// 真实损失是 **[已确认水位, 已消费位置)**——被截掉的字节再也读不回来（没有轮转目标接管），
// 故这一整段是否落库只能由对账裁定 ✓。
// 已确认落库的部分**不记缺口**：那会把一次「没有丢失任何内容」的截断变成源被永久阻塞
// （FILE_REWRITTEN 不在自动消解名单内，CHANGELOG 记过这条真机教训）。
func (t *FileTailer) reanchorAfterTruncationLocked(size int64) {
	durable := uint64(0)
	if ent := t.led.Get(t.key); ent != nil {
		durable = ent.Positions.Durable
	}
	if durable < t.logicalPos {
		_ = t.led.RecordGap(t.key, durable, t.logicalPos, ledger.GapReasonFileRewritten,
			"tailed file truncated in place (same inode, no rotation target holds the tail); unconfirmed tail is unrecoverable, surviving prefix skipped instead of re-delivering it")
	}
	t.anchorAtEOFLocked(size)
	// 通知 Poll：本轮已越过歧义区，直接收束（见字段说明）。
	t.truncationReanchored = true
}

// anchorAtEOFLocked 只做「把游标挪到 EOF 并重置段/身份状态」，不碰缺口登记。
// 抽出来是因为两种重写的**损失区间口径不同**（见两个调用方），而锚定动作必须完全一致。
func (t *FileTailer) anchorAtEOFLocked(size int64) {
	t.segmentStart = t.logicalPos
	t.physicalStart, t.physicalMappingKnown = uint64(size), true
	t.cursor = size
	t.pendingPhysicalLine = false
	t.lastSize = size
	t.active = false
	t.identityBytes, t.identitySHA256, t.tailSHA256, t.tailHashAt = 0, "", "", 0
	t.identityDev, t.identityIno, t.identityOK = 0, 0, false
	_ = t.registerLiveSegment()
}

// refreshTailHashLocked 维护「游标前 512B 窗口」指纹（每次 Poll 收束后调用一次，O(512) ✓）。
// 失败 ⇒ 清空指纹（下一轮走"不确定 ⇒ 停在旧位置＋告警"支路 ✗，绝不带漂移偏移续读 ✗）。
func (t *FileTailer) refreshTailHashLocked() {
	const window = 512
	from := t.cursor - window
	if from < 0 {
		from = 0
	}
	size := t.cursor - from
	if size <= 0 {
		t.tailSHA256, t.tailHashAt = "", t.cursor
		return
	}
	h, err := prefixSHA256At(t.path, from, size)
	if err != nil {
		t.tailSHA256, t.tailHashAt = "", t.cursor
		return
	}
	t.tailSHA256, t.tailHashAt = h, t.cursor
}

func filePrefixSHA256(path string, size int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyN(h, f, size); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
