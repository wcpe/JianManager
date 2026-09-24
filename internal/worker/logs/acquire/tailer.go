package acquire

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// FileTailer 从源文件 tail 采集：cursor resume + 轮转检测。
// Multiline 未完成事件只推进 read_position，不推进 durable。
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
	// identity 固定取首次观察到的前缀，追加写不会改变；同路径替换时用于检测轮转。
	identityBytes  int64
	identitySHA256 string
	// rotateTo 轮转目标路径模板（可选）；检测到截断时登记关联。
	rotateTo string
	// rotationCount 轮转次数（测试断言）。
	rotationCount int
	// eventsEmitted 完整事件计数（用于轮转不双计验证）。
	eventsEmitted int
	// sourceCategory instance|worker|node；source=worker 同管道。
	sourceCategory logtypes.Source
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
		live = ledger.Segment{Path: path, Kind: ledger.SegmentLive, ParserVersion: identity.ParserVersion}
		_ = led.RegisterSegment(key, live)
	}
	t := &FileTailer{
		led:            led,
		key:            key,
		path:           path,
		hook:           hook,
		wal:            wal,
		parser:         identity,
		sourceCategory: logtypes.SourceInstance,
		segmentStart:   live.StartPos,
		identityBytes:  live.IdentityBytes,
		identitySHA256: live.IdentitySHA256,
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
		t.cursor = int64(base - t.segmentStart)
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
	fi, err := os.Stat(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			if t.rotateTo != "" {
				if _, rotateErr := os.Stat(t.rotateTo); rotateErr == nil && t.cursor > 0 {
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
		if err := t.registerLiveSegment(); err != nil {
			return false, err
		}
	}
	replaced := false
	if t.cursor > 0 && t.identityBytes > 0 && fi.Size() >= t.identityBytes {
		current, hashErr := filePrefixSHA256(t.path, t.identityBytes)
		if hashErr != nil {
			return false, hashErr
		}
		replaced = current != t.identitySHA256
	}
	if t.cursor > 0 && (fi.Size() < t.cursor || replaced) {
		t.detectRotationLocked()
		return true, nil
	}
	return false, nil
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

	_, err := t.PrepareRotation()
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			// 文件消失：可能是轮转中间态；若设置了 rotateTo 且存在则接管。
			if t.rotateTo != "" {
				if _, rerr := os.Stat(t.rotateTo); rerr == nil {
					t.detectRotationLocked()
					return t.Poll()
				}
			}
			return nil, nil
		}
		return nil, err
	}

	if fi.Size() == t.cursor {
		return nil, nil
	}

	f, err := os.Open(t.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if _, err := f.Seek(t.cursor, io.SeekStart); err != nil {
		return nil, err
	}

	reader := bufio.NewReader(f)
	var events []logtypes.Event
	lineStart := t.logicalPos

	for {
		line, err := reader.ReadString('\n')
		if len(line) == 0 && err != nil {
			break
		}
		hasNL := len(line) > 0 && line[len(line)-1] == '\n'
		raw := line
		if hasNL {
			raw = line[:len(line)-1]
		}
		absPos := lineStart
		lineEnd := absPos + uint64(len(raw))
		if hasNL {
			lineEnd++ // 计入换行
		}

		eventEnd, complete, emit := t.hook.Feed([]byte(raw), absPos, lineEnd)
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
	}

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
		Path:           to,
		Kind:           ledger.SegmentRotated,
		StartPos:       t.segmentStart,
		EndPos:         t.logicalPos,
		IdentityBytes:  t.identityBytes,
		IdentitySHA256: t.identitySHA256,
		ParserVersion:  t.parser.ParserVersion,
	})
	t.segmentStart = t.logicalPos
	t.cursor = 0
	t.active = false
	t.identityBytes = 0
	t.identitySHA256 = ""
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
		EndPos: t.logicalPos, IdentityBytes: t.identityBytes,
		IdentitySHA256: t.identitySHA256, ParserVersion: t.parser.ParserVersion,
	})
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
