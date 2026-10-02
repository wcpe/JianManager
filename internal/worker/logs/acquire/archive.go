package acquire

import (
	"bufio"
	"compress/gzip"
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

// ArchiveImporter 从 gzip 归档流式解析。
// 约束：
//   - 轮转已关联时禁止整包按 hash 重导入；
//   - archive_object_id 仅 provenance，不参与 event_id；
//   - 接管前必须登记前段结束位置（由 rotation link 提供）；
//   - **归属闸**（2026-10-02 补）：归档只允许导入到"它所属的那个代次"。归属由轮转关联
//     或跨代次注册表命中给出；两者都没有 = 来源不可追溯，一律拒绝导入并记缺口告警。
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
func (a *ArchiveImporter) ImportGzip(archivePath string) (*ImportResult, error) {
	// 轮转关联检查：latest.log → .gz 已链接且已导入 → 不重复计数。
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
		_ = a.led.RecordGap(a.key, 0, 0, "ARCHIVE_UNREADABLE", err.Error())
		_ = a.MarkFailed(archivePath, "ARCHIVE_UNREADABLE: "+err.Error())
		return &ImportResult{Skipped: true, Reason: err.Error()}, nil
	}
	objID := ArchiveObjectID(archivePath, size, contentHash)

	f, err := os.Open(archivePath)
	if err != nil {
		_ = a.led.RecordGap(a.key, 0, 0, "ARCHIVE_OPEN_FAILED", err.Error())
		_ = a.MarkFailed(archivePath, "ARCHIVE_OPEN_FAILED: "+err.Error())
		return &ImportResult{Skipped: true, Reason: err.Error(), ArchiveObjectID: objID}, nil
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		// 损坏 gz：隔离/缺口可见。
		_ = a.led.RecordGap(a.key, 0, 0, "ARCHIVE_GZIP_CORRUPT", err.Error())
		_ = a.MarkFailed(archivePath, "ARCHIVE_GZIP_CORRUPT: "+err.Error())
		return &ImportResult{Skipped: true, Reason: "corrupt gzip: " + err.Error(), ArchiveObjectID: objID}, nil
	}
	defer gz.Close()

	// 归属闸（2026-10-02）：归档只允许导入到**它所属的那个代次**。
	//
	// 位置刻意放在 gzip 头校验**之后**：归档损坏/打不开是更基础的事实，其原因码
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
			_ = a.led.RecordGap(a.key, 0, 0, "ARCHIVE_FOREIGN_GENERATION", reason)
			_ = a.MarkFailed(archivePath, "ARCHIVE_FOREIGN_GENERATION: "+reason)
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
	}

	_ = a.led.RegisterSegment(a.key, ledger.Segment{
		Path:            archivePath,
		Kind:            ledger.SegmentGzip,
		StartPos:        segmentStart,
		ArchiveObjectID: objID, // provenance only
		Imported:        false,
		ParserVersion:   a.parser.ParserVersion,
	})

	reader := bufio.NewReader(gz)
	if skipBytes > 0 {
		if _, err := io.CopyN(io.Discard, reader, int64(skipBytes)); err != nil {
			if err == io.EOF {
				_ = a.MarkImported(archivePath, objID, segmentStart, startFrom)
				a.skippedAlreadyLinked++
				return &ImportResult{Skipped: true, Reason: "rotation prefix already covered",
					ArchiveObjectID: objID, ResumedFrom: startFrom}, nil
			}
			_ = a.led.RecordGap(a.key, startFrom, startFrom, "ARCHIVE_READ_ERROR", err.Error())
			return &ImportResult{Skipped: true, Reason: err.Error(), ArchiveObjectID: objID, ResumedFrom: startFrom}, nil
		}
	}
	var events []logtypes.Event
	pos := startFrom

	for {
		line, err := reader.ReadString('\n')
		if len(line) == 0 && err != nil {
			// 本次读取未拿到任何字节：若是非 EOF 错误（截断 gzip 的典型收尾形态——
			// 已成功读出行之后，下一次读取以 unexpected EOF 结束），必须留下可见缺口。
			// 修复前此处直接 break，缺口只在 `err != nil && len(line) > 0` 时才记录，
			// 导致「截断归档静默丢尾且不可见」，违反 FR-474 §5#1「截断可见」契约。
			if err != io.EOF {
				_ = a.led.RecordGap(a.key, pos, pos, "ARCHIVE_READ_ERROR", err.Error())
			}
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
			_ = a.led.RecordGap(a.key, pos, pos, "ARCHIVE_READ_ERROR", err.Error())
			break
		}
		if err == io.EOF {
			break
		}
	}

	// 更新分段结束位置（逻辑源累计：startFrom + 归档内容长度）。
	_ = a.led.RegisterSegment(a.key, ledger.Segment{
		Path:            archivePath,
		Kind:            ledger.SegmentGzip,
		StartPos:        segmentStart,
		EndPos:          pos,
		ArchiveObjectID: objID,
		Imported:        false,
		ParserVersion:   a.parser.ParserVersion,
	})
	if len(events) == 0 {
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
	}, nil
}

// MarkImported is called only after the archive events are covered by durable
// WAL (or the linked prefix was already covered). This prevents a capacity or
// WAL failure from turning a discovered gzip into a silently skipped segment.
func (a *ArchiveImporter) MarkImported(path, objectID string, start, end uint64) error {
	if entry := a.led.Get(a.key); entry != nil {
		for _, segment := range entry.Segments {
			if segment.Path == path {
				start = segment.StartPos
				if segment.EndPos > end {
					end = segment.EndPos
				}
				break
			}
		}
	}
	return a.led.RegisterSegment(a.key, ledger.Segment{
		Path: path, Kind: ledger.SegmentGzip, StartPos: start, EndPos: end,
		ArchiveObjectID: objectID, Imported: true, ParserVersion: a.parser.ParserVersion,
	})
}

// MarkFailed quarantines an unchanged bad archive. The manager retries only
// after size or mtime changes, avoiding an unbounded gap on every poll.
func (a *ArchiveImporter) MarkFailed(path, reason string) error {
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
	segment.Kind = ledger.SegmentGzip
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
