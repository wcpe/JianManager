package tailer

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"time"
)

// Harvester 负责读取**单个文件**（对齐 Filebeat `harvester` / Fluent Bit 的单文件 reader）。
//
// # 职责边界（很重要）
//
// 它只管「从一个文件的某位置开始，把新行读出来」。它不知道：
//
//   - 这个文件属于哪个源（那是 tag 的事）
//   - 读出的行要不要重试投递（那是输出侧的事）
//   - 数据完不完整（采集器不管这个）
//
// 这种职责单一正是 Fluent Bit / Filebeat 能做简单的原因。
//
// # 与旧 FileTailer 的区别
//
// 旧实现把「文件读取」与「账本登记」（segment / 水位 / 缺口）绑在一个结构里，
// 于是每读一行都要碰账本。本实现不碰账本，只维护一个 offset，
// 并且这个 offset 定期落到 registry。
//
// # 轮转怎么处理
//
// **不做特判**。靠 fingerprint 自然跟随：
//
//	轮转前：latest.log， fp=abc，offset=1000
//	轮转后：2026-11-06-14.log，fp=abc（内容未变），offset=1000
//	        新的 latest.log，fp=xyz（内容不同）⇒ 新条目，从 0 读
//
// Filebeat 文档对此的表述："The fingerprint follows the file content, so the input
// detects copytruncate rotation on its own." 我们用同一机制。
//
// 唯一需要特判的是**截断**（同一文件被清空重写）：offset 超过当前 size 时重置为 0。
type Harvester struct {
	path     string
	identity FileIdentity
	file     *os.File
	reader   *bufio.Reader

	// offset 是已读到的字节位置（下一个未读字节）。
	offset int64
	// maxLine 是单行上限。超过则截断该行（不丢后续）。
	maxLine int
	// pending 是不完整的末行（尚未遇到换行符）。
	//
	// 为什么需要：写入是流式的，读取时最后一行很可能是写了一半的。
	// 直接当成完整行会产出被截断的日志；丢弃又会在下一轮重复读。
	// 正确做法：保留在 pending 里，下次读到换行符再拼完整。
	pending []byte
	// lastActive 是最后一次读到新内容的时间。
	lastActive time.Time
}

// NewHarvester 打开一个文件准备读取。
//
// startAt 是期望的起始位置。实际位置会经过校验：
//   - startAt > 当前 size ⇒ 文件被截断（或换了个文件）⇒ 从 0 读
//   - startAt == size     ⇒ 已读到末尾，等着读新内容
func NewHarvester(f DiscoveredFile, startAt int64, maxLine int) (*Harvester, error) {
	f2, err := os.Open(f.Path)
	if err != nil {
		return nil, fmt.Errorf("tailer: 打开 %s: %w", f.Path, err)
	}
	h := &Harvester{
		path:       f.Path,
		identity:   f.Identity,
		file:       f2,
		maxLine:    maxLine,
		lastActive: time.Now(),
	}
	if h.maxLine <= 0 {
		h.maxLine = 64 * 1024 // 单行 64KB 默认上限
	}
	// 起始位置校验：见 NewHarvester 注释。
	actual := startAt
	if actual < 0 || actual > f.Size {
		actual = 0
	}
	if _, err := f2.Seek(actual, io.SeekStart); err != nil {
		_ = f2.Close()
		return nil, fmt.Errorf("tailer: 定位 %s 到 %d: %w", f.Path, actual, err)
	}
	h.offset = actual
	h.reader = bufio.NewReaderSize(f2, 64*1024)
	return h, nil
}

// Path 返回当前文件路径。
func (h *Harvester) Path() string { return h.path }

// Identity 返回文件身份。
func (h *Harvester) Identity() FileIdentity { return h.identity }

// Offset 返回已读到的位置。
func (h *Harvester) Offset() int64 { return h.offset }

// LastActive 返回最后一次读到新内容的时间。
func (h *Harvester) LastActive() time.Time { return h.lastActive }

// SetPath 在轮转后更新路径（fingerprint 不变，但路径变了）。
//
// 只改展示用的路径，不影响读取位置——文件句柄指向的还是同一个 inode。
func (h *Harvester) SetPath(p string) { h.path = p }

// Close 关闭底层文件。
func (h *Harvester) Close() error {
	if h.file == nil {
		return nil
	}
	err := h.file.Close()
	h.file = nil
	return err
}

// LineHandler 接收一行（不含换行符）。返回 error 会中止本次读取。
type LineHandler func(line []byte) error

// ReadLines 读取**当前可得的全部完整行**，逐行交给 handler。
//
// # 为什么只读完整行
//
// 见 pending 字段的注释。写入流式的，末行常常是半行；
// 当成完整行会产出被截断的日志，丢弃又会在下一轮重复读。
//
// # 达到 maxLine 怎么办
//
// 截断该行（保留前 maxLine 字节）并丢弃剩余，避免一行损坏拖死整个文件。
// 对齐 Fluent Bit `skip_long_lines` 的精神（我们选截断而非跳过，因为截断至少留下痕迹）。
func (h *Harvester) ReadLines(handler LineHandler) (lines int, err error) {
	if h.reader == nil {
		return 0, fmt.Errorf("tailer: harvester 已关闭")
	}
	for {
		chunk, readErr := h.reader.ReadSlice('\n')
		if len(chunk) > 0 {
			h.offset += int64(len(chunk))
			h.pending = append(h.pending, chunk...)
			h.lastActive = time.Now()
		}
		switch {
		case readErr == nil:
			// 拿到完整行（含 \n）。
			line := h.takePendingLine()
			if len(line) > 0 {
				if herr := handler(line); herr != nil {
					return lines, herr
				}
				lines++
			}
			continue
		case readErr == bufio.ErrBufferFull:
			// 行超过缓冲区。继续读，直到凑出换行或达到 maxLine。
			if len(h.pending) >= h.maxLine {
				// 截断：交出目前已有内容，后续部分丢弃到下一个换行。
				line := make([]byte, h.maxLine)
				copy(line, h.pending[:h.maxLine])
				h.pending = h.pending[:0]
				if herr := handler(line); herr != nil {
					return lines, herr
				}
				lines++
				if derr := h.discardUntilNewline(); derr != nil {
					return lines, nil // EOF：本次读完
				}
			}
			continue
		case readErr == io.EOF:
			// 末尾：剩余 pending 可能是半行，**保留不给**。
			// 但若 pending 已经很长且文件不再增长，说明可能是缺尾换行的文件——
			// 这个边界交给上层决定（通过 FlushPending），本函数不自己猜。
			return lines, nil
		default:
			return lines, readErr
		}
	}
}

// takePendingLine 从 pending 里取出一个完整行（去掉 \n 与 \r）并从缓冲删除。
func (h *Harvester) takePendingLine() []byte {
	for i := 0; i < len(h.pending); i++ {
		if h.pending[i] != '\n' {
			continue
		}
		line := make([]byte, i)
		copy(line, h.pending[:i])
		// 去掉行尾 \r（CRLF）
		if n := len(line); n > 0 && line[n-1] == '\r' {
			line = line[:n-1]
		}
		h.pending = append(h.pending[:0], h.pending[i+1:]...)
		return line
	}
	return nil
}

// discardUntilNewline 丢弃到下一个换行符（用于超长行截断后）。返回 nil 表示成功，EOF 也返回 nil。
func (h *Harvester) discardUntilNewline() error {
	for {
		_, err := h.reader.ReadSlice('\n')
		if err == nil {
			return nil
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return nil // EOF 或其它错误：本次就到这里
	}
}

// PendingLength 返回未闭合末行的长度（供观测与 FlushPending 判断）。
func (h *Harvester) PendingLength() int { return len(h.pending) }

// FlushPending 强制交出 pending 里的内容（即使没有换行）。
//
// # 何时用
//
// 文件被判定为“不再增长”（如轮转后的旧文件、或 CloseInactive 到期）时，
// 残留的半行应该交出去而不是永久丢弃。
//
// 这与 Filebeat/Fluent Bit 对"未闭合多行"的处理一致：文件结束时 flush。
func (h *Harvester) FlushPending(handler LineHandler) error {
	if len(h.pending) == 0 {
		return nil
	}
	line := make([]byte, len(h.pending))
	copy(line, h.pending)
	if n := len(line); n > 0 && line[n-1] == '\r' {
		line = line[:n-1]
	}
	h.pending = h.pending[:0]
	if len(line) == 0 {
		return nil
	}
	return handler(line)
}

// Stat 返回当前文件状态（用于截断检测与活跃判定）。
func (h *Harvester) Stat() (size int64, modTime time.Time, err error) {
	if h.file == nil {
		return 0, time.Time{}, fmt.Errorf("tailer: harvester 已关闭")
	}
	fi, statErr := h.file.Stat()
	if statErr != nil {
		return 0, time.Time{}, statErr
	}
	return fi.Size(), fi.ModTime(), nil
}

// DetectTruncation 报告文件是否被截断（当前 size 小于已读 offset）。
//
// # 为什么需要它
//
// copytruncate 轮转（或应用自己清空日志）会让 offset 失效。
// 此时不重置就会永远读不到新内容（seek 到文件末尾之后）。
//
// 官方做法：Fluent Bit 检测到 inode 不变但 size 变小就重置 offset；
// Filebeat 用 `rotation.external.strategy.copytruncate` 显式声明。
// 我们无条件检测（比 Filebeat 要求显式配置更安全），触发时重置为 0。
func (h *Harvester) DetectTruncation() (bool, error) {
	size, _, err := h.Stat()
	if err != nil {
		return false, err
	}
	if size < h.offset {
		// 文件被截断：重新从头读。
		if _, seekErr := h.file.Seek(0, io.SeekStart); seekErr != nil {
			return true, seekErr
		}
		h.reader.Reset(h.file)
		h.offset = 0
		h.pending = h.pending[:0]
		return true, nil
	}
	return false, nil
}
