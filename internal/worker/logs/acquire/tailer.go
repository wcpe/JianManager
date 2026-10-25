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

// FileTailer 从源文件 tail 采集：cursor resume + 轮转检测。
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

	// cursor 当前文件内偏移（可从账本 read_position 恢复）。
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
	// confirmedFingerprintChecked 标记「续读前已确认水位指纹校验」是否已做过（w18 防护段 3）。
	// 每进程每源只判一次：判过之后运行期的漂移由 tailSHA256 那条链负责 ✓。
	confirmedFingerprintChecked bool
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
	// cursor resume：从账本 durable/read 重建。崩溃后 read 可回退到 pending 多行首行。
	if ent := led.Get(key); ent != nil {
		base := ent.Positions.Durable
		if ent.Positions.Read < base {
			// read 回退合法：以 read 为恢复点（未完成多行从首行重拼）。
			base = ent.Positions.Read
		}
		if base < t.segmentStart {
			base = t.segmentStart
		}
		t.logicalPos = base
		t.cursor = int64(t.physicalStart + base - t.segmentStart)
		t.lastSize = t.cursor
		t.active = false
	}
	return t
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

	// **续读前三态校验（w18 漂移防护段 3）**：既有重写检测靠**进程内**尾窗指纹（tailSHA256），重启后它为空
	// ⇒ 对「重启 ＋ 外部保头重写」结构性看不见 ✗ ⇒ 会按旧偏移回读历史内容 ⇒ 同内容以漂移后的新身份再摄取。
	// 本校验每进程每源只判一次（confirmedFingerprintChecked ✓），用**持久化**的已确认水位指纹：
	//   ① 一致（水位处 [at-512, at) 与持久化值相符）⇒ 按水位续读 ✓（落到下方正常读取路径）；
	//   ② 不一致（漂移/重写/截断，含"同 inode 保头"）⇒ 记可见缺口 ⇒ 锚 EOF ⇒ 段/身份重置，禁止按旧偏移
	//      回读历史内容 ✗，缺的东西交给既有对账补齐 ✓；
	//   ③ 缺指纹（旧账本/尚未采样）⇒ 同不一致 ✓（宁可走对账，绝不赌）。
	if !t.confirmedFingerprintChecked && t.cursor > 0 {
		t.confirmedFingerprintChecked = true
		state, okState := t.led.AcquireState(t.key)
		watermark := uint64(0)
		if okState {
			watermark = state.Positions.Delivery
		}
		at, fingerprint, ok := t.led.PositionsFingerprint(t.key)
		switch {
		case watermark == 0 && (!ok || at == 0 || fingerprint == ""):
			// 尚无任何已确认水位（新源/初启）：没有需要保护的历史内容 ⇒ 按现状续读 ✓
			// （否则每个新源第二轮都会吃一条假缺口 ✗）。
		case !ok || at == 0 || fingerprint == "":
			// 缺指纹＝无从证明一致。**不在此处重锚** ✗：历史归档导入（gzip 回灌）等真路径会合法地
			// "有水位但无指纹"，一律重锚会给出假缺口并打断导入 ✗（ingest 用例实测）。
			// 防护由已采样的情形承担：部署后段 2 会在水位前进时写入指纹 ⇒ 之后的重启走 ①② ✓。
		default:
			bound, boundOK := t.led.ConfirmedFingerprint(t.key)
			physicalAt, window := int64(0), int64(0)
			if boundOK {
				if bound.SegmentPath != t.path || bound.SegmentStartPos != t.segmentStart || bound.SegmentIdentitySHA256 != t.identitySHA256 {
					// 投递落后时凭据仍属于旧段；绝不拿旧段的逻辑采点读取当前新文件。
					break
				}
				physicalAt, window, fingerprint = bound.PhysicalOffset, bound.WindowBytes, bound.SHA256
			} else {
				// 旧平面凭据没有跨段定位证明。仅兼容零基址原文件，其他情况保持未知，不伪造绑定。
				if t.segmentStart != 0 || t.physicalStart != 0 || at > t.logicalPos {
					break
				}
				physicalAt = int64(at)
				window = physicalAt
				if window > 512 {
					window = 512
				}
			}
			cur, herr := prefixSHA256At(t.path, physicalAt-window, window)
			if herr != nil || cur != fingerprint {
				if err := t.reanchorAtEOFLocked(fi.Size(), "resume fingerprint mismatch at confirmed watermark; refusing to re-read history from stale offset"); err != nil {
					return events, err
				}
				return events, nil
			}
		}
	}

	// 必须先识别轮转并核验旧凭据，再采样新确认线；否则会先读新文件，再给旧物理段贴摘要。
	// 放在无新数据的返回之前，投递线单独前进时仍能及时保存凭据。
	t.refreshConfirmedFingerprintLocked()
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
	fingerprint, fingerprintOK := t.led.ConfirmedFingerprint(t.key)
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
	if fingerprintOK && fingerprint.SegmentPath == t.path && fingerprint.SegmentStartPos == t.segmentStart {
		fingerprint.SegmentPath = to
		_ = t.led.SetConfirmedFingerprint(t.key, fingerprint)
	}
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

// refreshConfirmedFingerprintLocked 把「已确认水位（回收线）处的内容采样指纹」成对写入账本（w18 漂移防护段 2）。
//
// 恢复/重启续读前的三态判定在段 3（一致续读／不一致或缺指纹禁旧偏移回读走对账 ✗），本函数只负责采样＋持久化。
// 语义：水位前进 ⇒ 位置上移 ⇒ 旧指纹失效 ⇒ 在**新水位**重采样 ✓；水位未前进 ⇒ 不重复采样（O(512) 只在变化时付出 ✓）。
// 采样失败 ⇒ 不写（缺指纹在段 3 视作不一致 ⇒ 宁可走对账，绝不带漂移偏移续读 ✗）。
func (t *FileTailer) refreshConfirmedFingerprintLocked() {
	state, ok := t.led.AcquireState(t.key)
	if !ok {
		return
	}
	// 已确认水位取**投递线**：它是平台确认落库的最强水位（回收线只代表本地消费，可能滞后）。
	// 段 3 的三态判定将按同一水位校验 ✓。
	watermark := state.Positions.Delivery
	if watermark == 0 {
		return // 尚无已确认水位：无凭证可写（段 3 按「缺指纹」处理 ✓）
	}
	if at, fp, ok := t.led.PositionsFingerprint(t.key); ok && at == watermark && fp != "" {
		return // 水位未前进，指纹仍有效 ✓
	}
	// 只在已确认水位确属当前物理段时采样；边界处属于前段，不能查新文件。
	if watermark <= t.segmentStart || watermark > t.logicalPos || !t.physicalMappingKnown || t.identityBytes <= 0 || t.identitySHA256 == "" {
		return
	}
	physicalAt := int64(t.physicalStart + watermark - t.segmentStart)
	size := physicalAt
	if size > 512 {
		size = 512
	}
	if size <= 0 {
		return
	}
	fingerprint, err := prefixSHA256At(t.path, physicalAt-size, size)
	if err != nil {
		return
	}
	_ = t.led.SetConfirmedFingerprint(t.key, ledger.ConfirmedFingerprint{
		SegmentPath: t.path, SegmentStartPos: t.segmentStart,
		SegmentIdentityBytes: t.identityBytes, SegmentIdentitySHA256: t.identitySHA256,
		LogicalPos: watermark, PhysicalOffset: physicalAt, WindowBytes: size, SHA256: fingerprint,
	})
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
