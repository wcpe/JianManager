package acquire

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// ArchiveImporter 从归档流式解析（压缩 gzip 与纯文本轮转两种形态，见 archive_format.go）。
// 约束：
//   - 轮转已关联时禁止整包按 hash 重导入；
//   - archive_object_id 仅 provenance，不参与 event_id；
//   - 接管前必须登记前段结束位置（由 rotation link 提供）；
//   - **归属闸**（2026-10-02 补）：归档只允许导入到"它所属的那个代次"。归属由轮转关联
//     或跨代次注册表命中给出；两者都没有 = 来源不可追溯，一律拒绝导入并记缺口告警。
//
// 形态**不在本类型里判定**：调用方（pipeline.ImportArchive）按文件头判定后调用对应的入口
// （ImportGzipBounded / ImportTextBounded）。这样形态判定与准入决策同处一处，且两种形态
// 在导入流程里除形态本身外**逐字共用**同一实现。
type ArchiveImporter struct {
	led    *ledger.Ledger
	key    ledger.SourceKey
	parser logtypes.SourceIdentity
	// lookupOwner 是跨代次注册表查询：给定 (源ID, 规范化路径, archive_object_id) 返回
	// 该归档**已被哪个代次导入过**。nil 表示无注册表（此时只认轮转关联这一条归属凭据）。
	//
	// 为什么需要它：本导入器的账本是**按代次分片**的（ledger 以 (源ID, 代次) 为键），
	// 因此它天然看不见其它代次的导入记录。没有跨代次视图时，"这个归档属于谁"只能靠
	// **假设当前代次**——而那正是"静默重复"的入口（同一份字节以不同 event_id 再进一次 VL，
	// 且新代次的 VerifiedRuns 认不了旧账）。
	lookupOwner func(logSourceID, cleanPath, objectID string) (string, bool)
	// backfill 是「按原代次补账」通道：命中其它代次且该代次账本可寻址时，由上层把归档
	// 记入**原代次**的账（补账），而不是导入到当前代次。
	//
	// 为什么放在回调里而不是本包实现：补账需要"跨代次的账本 + 段存 + 待投递账目"，
	// 这些都在上层（ingest Manager）手里；本包只有**当前代次**的账本。
	// 返回 nil 表示补账成功（本次不再导入到当前代次）；返回错误则退回"拒绝 + 记缺口"。
	backfill func(ownerGeneration, archivePath, objectID string) error
	// skippedAlreadyLinked 测试断言：已关联时跳过次数。
	skippedAlreadyLinked int
	// refusedForeign 测试断言：因归属不可追溯而被拒的次数。
	refusedForeign int
	// backfilled 测试断言：按原代次补账的次数。
	backfilled int
	// importedEvents 累计导入事件数。
	importedEvents int
}

// NewArchiveImporter 创建导入器。
func NewArchiveImporter(led *ledger.Ledger, key ledger.SourceKey) *ArchiveImporter {
	identity := logtypes.SourceIdentity{
		LogSourceID:      key.LogSourceID,
		SourceGeneration: key.SourceGeneration,
		ParserVersion:    "acquire-v1",
	}
	led.Ensure(key, identity)
	return &ArchiveImporter{led: led, key: key, parser: identity}
}

// SetOwnerRegistry 注入跨代次归属注册表（见 ArchiveImporter.lookupOwner）。
// 必须在首次 ImportGzip 之前调用；nil 表示不启用归属闸的注册表那一半。
func (a *ArchiveImporter) SetOwnerRegistry(fn func(logSourceID, cleanPath, objectID string) (string, bool)) {
	a.lookupOwner = fn
}

// SetBackfill 注入「按原代次补账」通道（见 ArchiveImporter.backfill）。
func (a *ArchiveImporter) SetBackfill(fn func(ownerGeneration, archivePath, objectID string) error) {
	a.backfill = fn
}

// Backfilled 返回按原代次补账的次数（观测/测试用）。
func (a *ArchiveImporter) Backfilled() int { return a.backfilled }

// RefusedForeign 返回因归属不可追溯而被拒的归档数（观测/测试用）。
func (a *ArchiveImporter) RefusedForeign() int { return a.refusedForeign }

// ImportResult 归档导入结果。
type ImportResult struct {
	Skipped bool
	Reason  string
	Events  []logtypes.Event
	// ArchiveObjectID provenance only。
	ArchiveObjectID string
	// ImportedCount 本次导入事件数。
	ImportedCount int
	// ResumedFrom 从归档内恢复的起始逻辑位置。
	ResumedFrom uint64
	// Partial 为 true 表示本次是**有界导入**的一段：归档尚未读完，调用方应再调一次续读。
	//
	// 判据来自读循环本身（是否见到 EOF），而不是从「事件数 == 上限」反推：
	// 反推无法区分「恰好读满且正好读完」与「读满但还有剩余」，会在最后一个整批处
	// 多打一次空转调用，或在恰好读完时误判未完成而把归档永远留在「未导入」态。
	Partial bool
}

// ArchiveObjectID 计算归档对象 provenance id（hash+size）；不参与 event_id。
func ArchiveObjectID(path string, size int64, contentHash string) string {
	payload := fmt.Sprintf("%s|%d|%s", path, size, contentHash)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// HashFile 内容摘要（provenance）。
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// ImportGzip 流式导入 gzip 归档。
// 若 rotation 已将该归档链接为逻辑源分段且已 Import，跳过整包重导入。
//
// 等价于 ImportGzipBounded(archivePath, 0)（不限量）——一次读完整包，行为与本修复前一致。
func (a *ArchiveImporter) ImportGzip(archivePath string) (*ImportResult, error) {
	return a.importArchive(archivePath, 0, formatGzip)
}

// ImportGzipBounded 是**有界**导入：单次最多产出 maxEvents 个事件，读满即停并回
// Partial=true + 已消费位置（ResumedFrom/segment EndPos），调用方下一轮再调一次继续读。
//
// maxEvents<=0 表示不限量（等价 ImportGzip）。
//
// 为什么要分批（缺陷二，2026-10-10 生产实测）：原实现把整个 gz 的事件一次性返回，
// 而调用方随后的单次 WAL 追加量 = 整个归档的事件数。`inst:220/file` 的 544,278 行 gz
// 解析出 **128,856** 个事件 = 单源条目上限（20000）的 6.4 倍 ⇒ 一次追加越闸 ⇒
// `单源积压超过上限，已暂停该源采集`；越闸后 Append 的首判据（已暂停）拒绝后续追加
// ⇒ 源停死、`delivery_pos` 恒 None、VL 侧 0 行，且 5 分钟内零自愈。
func (a *ArchiveImporter) ImportGzipBounded(archivePath string, maxEvents int) (*ImportResult, error) {
	return a.importArchive(archivePath, maxEvents, formatGzip)
}

// ImportText 导入**纯文本轮转归档**（BungeeCord 的 `proxy.log.N`）；不限量（等价
// ImportTextBounded(archivePath, 0)）。
//
// 形态不在这里判定：调用方必须先用 DetectArchiveFormat 判定，再调用对应入口。
// 本入口是「已确定是纯文本」时的执行者，因此它不会把 gzip 归档当纯文本读（那会产出
// 一堆二进制噪声事件），也不会把纯文本当 gzip 读（那正是本能力要消灭的缺口来源）。
func (a *ArchiveImporter) ImportText(archivePath string) (*ImportResult, error) {
	return a.importArchive(archivePath, 0, formatRotatedText)
}

// ImportTextBounded 是纯文本轮转归档的**有界**导入：语义与 ImportGzipBounded 逐条相同
// （单轮最多 maxEvents 条、读满即停、位置落回账本分段、下一轮从断点续读）。
//
// 为什么有界性对新形态同样必须成立：纯文本轮转文件可以很大（BungeeCord 的轮转上限按
// 文件大小触发，生产上单文件数万行是常态），一次性导入会重现缺陷二的越闸暂停。
func (a *ArchiveImporter) ImportTextBounded(archivePath string, maxEvents int) (*ImportResult, error) {
	return a.importArchive(archivePath, maxEvents, formatRotatedText)
}

// importArchive 是导入的**唯一实现**（maxEvents<=0 表示不限量）。
//
// format 只决定两件事：登记到账本分段的形态，以及「字节流 → 行文本流」的解码方式。
// 其余一切（归属闸、断点续读、有界分批、事件字段与 record 范围、缺口原因码）对两种形态
// 完全一致——「走完全相同的下游链路」在实现层就是这一条。
func (a *ArchiveImporter) importArchive(archivePath string, maxEvents int, format archiveFormat) (*ImportResult, error) {
	// 轮转关联检查：前段（latest.log / proxy.log.0）已链接且本归档已导入 → 不重复计数。
	link, linked := a.led.RotationLinked(a.key, archivePath)
	ent := a.led.Get(a.key)
	var existing *ledger.Segment
	if ent != nil {
		for i := range ent.Segments {
			if ent.Segments[i].Path == archivePath {
				seg := ent.Segments[i]
				existing = &seg
				if seg.Imported {
					a.skippedAlreadyLinked++
					return &ImportResult{
						Skipped:         true,
						Reason:          "rotation already linked; whole-archive reimport forbidden",
						ArchiveObjectID: seg.ArchiveObjectID,
						ResumedFrom:     seg.EndPos,
					}, nil
				}
				break
			}
		}
	}

	contentHash, size, err := HashFile(archivePath)
	if err != nil {
		// 权限/损坏：记缺口，不拖死后续源。
		_ = a.led.RecordGap(a.key, 0, 0, ledger.GapReasonArchiveUnreadable, err.Error())
		_ = a.markFailed(archivePath, format.kind, ledger.GapReasonArchiveUnreadable+": "+err.Error())
		return &ImportResult{Skipped: true, Reason: err.Error()}, nil
	}
	objID := ArchiveObjectID(archivePath, size, contentHash)

	f, err := os.Open(archivePath)
	if err != nil {
		_ = a.led.RecordGap(a.key, 0, 0, ledger.GapReasonArchiveOpenFailed, err.Error())
		_ = a.markFailed(archivePath, format.kind, ledger.GapReasonArchiveOpenFailed+": "+err.Error())
		return &ImportResult{Skipped: true, Reason: err.Error(), ArchiveObjectID: objID}, nil
	}
	defer f.Close()

	body, err := format.decode(f)
	if err != nil {
		// 形态不可解（当前只有 gzip 有这一阶段）：隔离/缺口可见。
		// 纯文本轮转没有容器头可校验，故它**不会**走到这里——也就不需要新的缺口原因码
		// （新原因若不在自动消解白名单里，就会成为「源暂停后永久停采」的新入口）。
		_ = a.led.RecordGap(a.key, 0, 0, format.decodeFailureReason, err.Error())
		_ = a.markFailed(archivePath, format.kind, format.decodeFailureReason+": "+err.Error())
		return &ImportResult{Skipped: true, Reason: format.decodeFailurePrefix + err.Error(), ArchiveObjectID: objID}, nil
	}
	defer body.Close()

	// 归属闸（2026-10-02）：归档只允许导入到**它所属的那个代次**。
	//
	// 位置刻意放在**解码/头校验**之后：归档损坏/打不开是更基础的事实，其原因码
	// （ARCHIVE_OPEN_FAILED / ARCHIVE_GZIP_CORRUPT）不应被归属问题顶掉（既有回归依赖它们）。
	//
	// 判据与语义（三条，按优先级）：
	//   ① 注册表命中且归属是**别的代次** → 拒绝。这是唯一的"静默重复"真入口：
	//      同一份字节以当前代次的 event_id 再进一次 VL（event_id 含代次），而 VL 侧没有唯一键
	//      可兜（canonical hash 明确不作为唯一键），新代次的 VerifiedRuns（代次内源位置区间凭据）
	//      也认不了旧账。⇒ 拒绝 + 记缺口 + 告警，交人工按原代次补账。
	//   ② 注册表命中本代次（重试/幂等）→ 放行。
	//   ③ 未命中（**首次见到**，含"首次登记时目录里已有的历史归档"）→ 放行，并在本次成功导入后
	//      由 MarkImported 把 (规范路径, archive_object_id) → 本代次 的绑定写进账本分段——
	//      这就是"实时边查表/无则登记"的**登记**动作。此后任何其它代次再看这个对象都会命中①而被拒。
	//
	// 为什么未命中不能直接拒绝（与"查不到就拒绝"的字面口径不同，此处是刻意的收窄）：
	// "首次登记时导入目录里已有的历史归档"是一条**已交付且有回归保护**的能力
	// （见 TestManagerAutoImportsHistoricalGzipBeforeCurrentFile：它**没有**轮转关联——
	//  归档先于本源存在，不可能有轮转事件）。若未命中即拒，该能力被直接删除，
	// 而这些归档将永远进不来（人工也无法指定一个不存在的"原代次"）。
	// 因此本闸把"拒绝"精确落在**跨代次**这一种形态上：它才是重复的来源；
	// 首次归属不是自创代次（代次仍由登记路径铸造），而是**首次登记**。
	if a.lookupOwner != nil {
		cleanPath := filepath.Clean(archivePath)
		if owner, found := a.lookupOwner(a.key.LogSourceID, cleanPath, objID); found && owner != a.key.SourceGeneration {
			// 命中其它代次：优先走**按原代次补账**（把这份数据记回它自己的账上），
			// 而不是导入到当前代次。补账成功即返回（本次不产生当前代次事件）。
			//
			// 补账失败（原代次账本不可寻址 / 段存或持久化失败）一律退回下面的
			// "拒绝 + 记缺口 + 告警"——宁可让人工看见，也绝不静默按当前代次导入。
			if a.backfill != nil {
				if bfErr := a.backfill(owner, archivePath, objID); bfErr == nil {
					a.backfilled++
					slog.Info("已按原代次补账（不归当前代次）",
						"logSourceID", a.key.LogSourceID, "generation", a.key.SourceGeneration,
						"ownerGeneration", owner, "archive", cleanPath)
					return &ImportResult{
						Skipped:         true,
						Reason:          "backfilled under original generation " + owner,
						ArchiveObjectID: objID,
					}, nil
				} else {
					slog.Warn("按原代次补账失败，退回拒绝并记缺口",
						"logSourceID", a.key.LogSourceID, "ownerGeneration", owner,
						"archive", cleanPath, "error", bfErr)
				}
			}
			reason := fmt.Sprintf(
				"归档 %s 已由代次 %q 导入，本代次 %q 不得重复归账（event_id 含代次，重复导入即静默重复）",
				cleanPath, owner, a.key.SourceGeneration)
			_ = a.led.RecordGap(a.key, 0, 0, ledger.GapReasonArchiveForeignGeneration, reason)
			_ = a.markFailed(archivePath, format.kind, ledger.GapReasonArchiveForeignGeneration+": "+reason)
			a.refusedForeign++
			slog.Warn("拒绝导入属于其它代次的归档（禁止归到当前代次）",
				"logSourceID", a.key.LogSourceID, "generation", a.key.SourceGeneration,
				"ownerGeneration", owner, "archive", cleanPath)
			return &ImportResult{Skipped: true, Reason: reason, ArchiveObjectID: objID}, nil
		}
	}

	// 接管起点：已链接轮转跳过 FileTailer 已读的物理前缀；历史归档按
	// ledger 最大逻辑位置顺序追加。失败重试沿用首次登记的 StartPos。
	var startFrom, segmentStart, skipBytes uint64
	// resumingMidArchive 标记「本次跳过的前缀来自**本分段上一次读到的地方**」（而不是
	// 轮转已覆盖前缀）。两种事实在 EOF 分支的处置不同，故必须显式区分。
	resumingMidArchive := false
	if existing != nil {
		segmentStart = existing.StartPos
		startFrom = existing.StartPos
	} else if linked {
		startFrom = link.EndPosFrom
		segmentStart = link.EndPosFrom
	} else if ent != nil {
		startFrom = ent.Positions.Read
		if ent.Positions.Durable > startFrom {
			startFrom = ent.Positions.Durable
		}
		for _, seg := range ent.Segments {
			if seg.EndPos > startFrom {
				startFrom = seg.EndPos
			}
		}
		segmentStart = startFrom
	}
	if linked {
		startFrom = link.EndPosFrom
		if existing != nil {
			segmentStart = existing.StartPos
		}
		if startFrom > segmentStart {
			skipBytes = startFrom - segmentStart
		}
	} else if existing != nil && existing.EndPos > segmentStart {
		// **断点续读**（缺陷二修复，2026-10-10）：本归档上次读到 `EndPos`，本次从那里继续。
		//
		// 为什么 `EndPos - StartPos` 就是「已消费的归档字节数」：本函数为归档内容分配的
		// 逻辑位置是**从 StartPos 起按行字节数顺序推进**的（见读循环里的 `pos = end`），
		// 而读循环总是从归档头部开始顺序读（不 seek）⇒ 已消费字节数 = pos - segmentStart。
		// 故按该差值丢弃 bufio 前导字节即可精确续读，无需记录额外游标。
		//
		// 为什么必须续读（不能靠「重头读 + event_id 幂等」）：那会把 N 轮的总读取量放大成
		// O(N²)（每轮都要重读并重解析整个归档前缀），对 12.9 万事件级的归档就是几十万次
		// 无谓解析——分批反而变成新的性能事故。续读让总读取量恒为归档的一遍。
		skipBytes = existing.EndPos - segmentStart
		startFrom = existing.EndPos
		resumingMidArchive = true
	}

	_ = a.led.RegisterSegment(a.key, ledger.Segment{
		Path:            archivePath,
		Kind:            format.kind,
		StartPos:        segmentStart,
		ArchiveObjectID: objID, // provenance only
		Imported:        false,
		ParserVersion:   a.parser.ParserVersion,
	})

	reader := bufio.NewReader(body)
	if skipBytes > 0 {
		if _, err := io.CopyN(io.Discard, reader, int64(skipBytes)); err != nil {
			if err == io.EOF {
				// 前缀比归档内容还长。**只有**「跳过的是轮转已覆盖前缀」才能据此断定
				// 「归档已被覆盖」（既有语义，逐字保留）。
				//
				// 断点续读形态下这**不能**当作已覆盖：EndPos 可能来自陈旧/回滚状态
				//（换机重挂数据根、state 重置），把它当「读完了」会把归档**静默标成
				// 已导入**而丢掉尾部数据 —— 那正是本修复要避免的静默丢。故这里如实报
				// 「未完成」且**不置 Imported**，由下一轮重试（重试时 EndPos 已被下方
				// RegisterSegment 修正为真实位置，故不会永久卡在这一支）。
				if resumingMidArchive {
					slog.Warn("归档断点续读越过了归档末尾，按未完成处理（不置已导入，下轮重试）",
						"logSourceID", a.key.LogSourceID, "archive", archivePath,
						"skipBytes", skipBytes, "segmentStart", segmentStart)
					return &ImportResult{Skipped: true,
						Reason:          "resume offset beyond end of archive; not marked imported",
						ArchiveObjectID: objID, ResumedFrom: startFrom}, nil
				}
				_ = a.MarkImported(archivePath, objID, segmentStart, startFrom)
				a.skippedAlreadyLinked++
				return &ImportResult{Skipped: true, Reason: "rotation prefix already covered",
					ArchiveObjectID: objID, ResumedFrom: startFrom}, nil
			}
			_ = a.led.RecordGap(a.key, startFrom, startFrom, ledger.GapReasonArchiveReadError, err.Error())
			return &ImportResult{Skipped: true, Reason: err.Error(), ArchiveObjectID: objID, ResumedFrom: startFrom}, nil
		}
	}
	var events []logtypes.Event
	pos := startFrom
	// reachedEOF 记录「本次读循环确实见到了归档末尾」。分批时它是区分「恰好读完」与
	// 「读满上限但还有剩余」的**唯一可靠判据**（见 ImportResult.Partial 的说明）。
	reachedEOF := false

	for {
		// 有界导入（缺陷二）：读满本批即停。已读位置由下方的 RegisterSegment 落回账本
		// （EndPos），故下一轮从断点续读而不是从头重读 —— 这是「分批不丢、不重」的关键：
		//   不丢 = 未读部分仍在归档里且归档未被标为已导入 ⇒ 下一轮必然重新发现；
		//   不重 = 已读前缀的位置已记录，续读时按字节跳过（见下方 skipBytes 计算）。
		if maxEvents > 0 && len(events) >= maxEvents {
			break
		}
		line, err := reader.ReadString('\n')
		if len(line) == 0 && err != nil {
			// 本次读取未拿到任何字节：若是非 EOF 错误（截断 gzip 的典型收尾形态——
			// 已成功读出行之后，下一次读取以 unexpected EOF 结束），必须留下可见缺口。
			// 修复前此处直接 break，缺口只在 `err != nil && len(line) > 0` 时才记录，
			// 导致「截断归档静默丢尾且不可见」，违反 FR-474 §5#1「截断可见」契约。
			if err != io.EOF {
				_ = a.led.RecordGap(a.key, pos, pos, ledger.GapReasonArchiveReadError, err.Error())
			}
			reachedEOF = err == io.EOF
			break
		}
		raw := strings.TrimRight(line, "\n")
		end := pos + uint64(len(line))
		ev := logtypes.BuildEvent(a.parser, logtypes.RecordRange{Start: pos, End: end},
			"", "", "", "archive", raw)
		// provenance：不改变 event_id。
		if ev.Fields == nil {
			ev.Fields = map[string]string{}
		}
		ev.Fields["archive_object_id"] = objID
		events = append(events, ev)
		pos = end

		if err != nil && err != io.EOF {
			_ = a.led.RecordGap(a.key, pos, pos, ledger.GapReasonArchiveReadError, err.Error())
			break
		}
		if err == io.EOF {
			reachedEOF = true
			break
		}
	}

	// 更新分段结束位置（逻辑源累计：startFrom + 归档内容长度）。
	_ = a.led.RegisterSegment(a.key, ledger.Segment{
		Path:            archivePath,
		Kind:            format.kind,
		StartPos:        segmentStart,
		EndPos:          pos,
		ArchiveObjectID: objID,
		Imported:        false,
		ParserVersion:   a.parser.ParserVersion,
	})
	if len(events) == 0 {
		// 本批没有产出事件：可能是「跳过前缀后刚好落在末尾」，也可能是「前缀越界」。
		//
		// **只有真的读到 EOF 才能判「已覆盖」**：分批形态下把「没读到东西」一律当成
		// 「读完了」，会把未消费的尾部**静默标成已导入**（本缺陷类里最危险的一种：
		// WAL 没丢但归档数据丢了）。故非 EOF 时如实报「未完成、未置导入」，留给下一轮。
		if !reachedEOF {
			return &ImportResult{Skipped: true,
				Reason:          "archive not fully consumed yet; retry next round",
				ArchiveObjectID: objID, ResumedFrom: startFrom, Partial: true}, nil
		}
		_ = a.MarkImported(archivePath, objID, segmentStart, pos)
		a.skippedAlreadyLinked++
		return &ImportResult{Skipped: true, Reason: "rotation prefix already covered",
			ArchiveObjectID: objID, ResumedFrom: startFrom}, nil
	}
	a.importedEvents += len(events)

	return &ImportResult{
		Events:          events,
		ArchiveObjectID: objID,
		ImportedCount:   len(events),
		ResumedFrom:     startFrom,
		// 读满上限但未见到 EOF ⇒ 归档还有剩余 ⇒ 调用方下一轮必须续读（见 ImportGzipBounded）。
		Partial: maxEvents > 0 && !reachedEOF,
	}, nil
}

// MarkImported is called only after the archive events are covered by durable
// WAL (or the linked prefix was already covered). This prevents a capacity or
// WAL failure from turning a discovered gzip into a silently skipped segment.
//
// 形态（kind）取自该路径上**已登记的分段**：本入口是形态无关的「已导入」登记
// （ingest 侧也会直接调用它），若在这里写死 gzip，纯文本轮转归档的形态会被改写，
// 账本随即失真。既无分段时按 gzip 登记——这不是"猜形态"，而是保持既有调用口径：
// 形态感知的导入路径（importArchive）总在调用前先登记分段，故带形态的归档到这里必然
// 已带有自己的形态。
func (a *ArchiveImporter) MarkImported(path, objectID string, start, end uint64) error {
	kind := ledger.SegmentKind("")
	if entry := a.led.Get(a.key); entry != nil {
		for _, segment := range entry.Segments {
			if segment.Path == path {
				start = segment.StartPos
				if segment.EndPos > end {
					end = segment.EndPos
				}
				kind = segment.Kind
				break
			}
		}
	}
	if !ledger.IsArchiveSegmentKind(kind) {
		kind = ledger.SegmentGzip
	}
	return a.led.RegisterSegment(a.key, ledger.Segment{
		Path: path, Kind: kind, StartPos: start, EndPos: end,
		ArchiveObjectID: objectID, Imported: true, ParserVersion: a.parser.ParserVersion,
	})
}

// MarkFailed quarantines an unchanged bad archive. The manager retries only
// after size or mtime changes, avoiding an unbounded gap on every poll.
//
// 等价于 markFailed(path, SegmentGzip, reason)：形态未知时按 gzip 登记，与历史逐字一致。
func (a *ArchiveImporter) MarkFailed(path, reason string) error {
	return a.markFailed(path, ledger.SegmentGzip, reason)
}

// markFailed 是 MarkFailed 的**形态感知**版本：登记失败态时保留该路径上既有的归档类形态
// （无既存分段时用本次形态）。
//
// 为什么形态必须保留：失败态是「这个归档暂时没导进来」的事实记录，它不改变字节的形态。
// 若在这里写死 gzip，纯文本轮转归档的失败登记会被记成 gzip —— 诊断面（形态统计）随之失真，
// 而 ingest 的待导入发现也按形态过滤，形态记错会让「已隔离、等文件变化再重试」这条语义
// 落到错误的形态上。
func (a *ArchiveImporter) markFailed(path string, kind ledger.SegmentKind, reason string) error {
	var segment ledger.Segment
	if entry := a.led.Get(a.key); entry != nil {
		for _, existing := range entry.Segments {
			if existing.Path == path {
				segment = existing
				break
			}
		}
	}
	segment.Path = path
	if !ledger.IsArchiveSegmentKind(segment.Kind) {
		// 无既存分段（首次失败）或该路径上只有非归档分段（tailer 的 rotated 等）：用本次形态。
		segment.Kind = kind
	}
	segment.Imported = false
	segment.ImportError = reason
	segment.ParserVersion = a.parser.ParserVersion
	if info, err := os.Stat(path); err == nil {
		segment.ObservedSize = info.Size()
		segment.ObservedModNano = info.ModTime().UnixNano()
	}
	return a.led.RegisterSegment(a.key, segment)
}

// SkippedAlreadyLinked 返回因轮转已关联而跳过的次数。
func (a *ArchiveImporter) SkippedAlreadyLinked() int { return a.skippedAlreadyLinked }

// ImportedEvents 返回累计导入事件数。
func (a *ArchiveImporter) ImportedEvents() int { return a.importedEvents }
