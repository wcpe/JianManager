package acquire

import (
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// ArchiveImporter 从 gzip 归档流式解析。
// 约束：
//   - 轮转已关联时禁止整包按 hash 重导入；
//   - archive_object_id 仅 provenance，不参与 event_id；
//   - 接管前必须登记前段结束位置（由 rotation link 提供）。
type ArchiveImporter struct {
	led    *ledger.Ledger
	key    ledger.SourceKey
	parser logtypes.SourceIdentity
	// skippedAlreadyLinked 测试断言：已关联时跳过次数。
	skippedAlreadyLinked int
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
