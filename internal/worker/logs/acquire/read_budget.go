package acquire

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// ReadLimits 独立于 WAL 积压阈值，在读取和构造正文之前限定物理行与单轮字节。
// 单行上限包含 CR/LF 等原始分隔字节，不以字符数、净化后正文或事件数代替。
type ReadLimits struct {
	MaxLineBytes    int
	MaxBytesPerPoll int64
}

// DefaultReadLimits 是所有文件与有限归档入口共用的默认真源。
func DefaultReadLimits() ReadLimits {
	return ReadLimits{MaxLineBytes: 256 << 10, MaxBytesPerPoll: 4 << 20}
}

func (l ReadLimits) normalized() (ReadLimits, error) {
	defaults := DefaultReadLimits()
	if l.MaxLineBytes == 0 {
		l.MaxLineBytes = defaults.MaxLineBytes
	}
	if l.MaxBytesPerPoll == 0 {
		l.MaxBytesPerPoll = defaults.MaxBytesPerPoll
	}
	if l.MaxLineBytes < 1 || l.MaxBytesPerPoll <= int64(l.MaxLineBytes) {
		return ReadLimits{}, fmt.Errorf("acquire: read limits require maxLineBytes > 0 and maxBytesPerPoll > maxLineBytes")
	}
	return l, nil
}

const ReadLineTooLongReason = "READ_LINE_TOO_LONG"

// ReadLimitError 表示受管背压，不代表已截断、已导入或已放弃源字节。
type ReadLimitError struct {
	Path         string
	LogicalStart uint64
	PhysicalFrom int64
	Limit        int
}

func (e *ReadLimitError) Error() string {
	return fmt.Sprintf("acquire: physical line exceeds %d bytes at %q physical=%d logical=%d; source retained under backpressure", e.Limit, e.Path, e.PhysicalFrom, e.LogicalStart)
}

var errReadRoundBudget = errors.New("acquire: read round byte budget exhausted")

// roundReader 在底层读取发生前扣减额度；bufio 的预读也必须计入，不能先分配完再限流。
type roundReader struct {
	reader    io.Reader
	remaining int64
	read      int64
}

func (r *roundReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, errReadRoundBudget
	}
	if int64(len(p)) > r.remaining {
		p = p[:int(r.remaining)]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	r.read += int64(n)
	return n, err
}

// readPhysicalLine 只按固定小块累积到单行上限。超限时不返回可供 Feed 的截断片段。
// EOF 携带的最后一行只供有限输入显式闭合；活文件调用者必须等到换行再 Feed。
func readPhysicalLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > limit-len(line) {
			return nil, &ReadLimitError{Limit: limit}
		}
		line = append(line, fragment...)
		if err == bufio.ErrBufferFull {
			continue
		}
		return line, err
	}
}

func physicalLineBody(line []byte) []byte {
	if len(line) > 0 && line[len(line)-1] == '\n' {
		line = line[:len(line)-1]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
	}
	return line
}
