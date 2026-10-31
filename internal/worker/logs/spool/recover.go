package spool

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
)

// StopReason 恢复扫描停下来的原因。
//
// 只有 StopClean 表示「扫到了本圈末尾」；其余都表示「这里就是尾部边界」（掉电截断、
// 环回残骸、或坏记录）。它们都**不是错误**：恢复不因此报错、不清空文件、不阻塞写入。
type StopReason int

const (
	// StopEmpty 文件里没有任何完整记录（全新文件，或首条就坏了）。
	StopEmpty StopReason = iota
	// StopClean 正好填满数据区。
	StopClean
	// StopPartialHeader 尾部不足一个完整帧头（掉电截断的典型形状）。
	StopPartialHeader
	// StopBadMagic 魔数不符（残骸或未写过的零区）。
	StopBadMagic
	// StopBadLength 长度字段荒谬（越过数据区尾部、或超过单条上限）。
	StopBadLength
	// StopBadCRC payload CRC 不符（只写了半条，或字节被覆盖）。
	StopBadCRC
	// StopSeqGap 序号不连续（跳号，或从头就不是 firstSeq）。
	StopSeqGap
	// StopPad 扫到圈尾死区填充帧，本圈在这里结束。
	StopPad
)

// String 返回可读的停因。
func (r StopReason) String() string {
	switch r {
	case StopEmpty:
		return "empty"
	case StopClean:
		return "clean"
	case StopPartialHeader:
		return "partial-header"
	case StopBadMagic:
		return "bad-magic"
	case StopBadLength:
		return "bad-length"
	case StopBadCRC:
		return "bad-crc"
	case StopSeqGap:
		return "seq-gap"
	case StopPad:
		return "pad"
	default:
		return fmt.Sprintf("stop-reason(%d)", int(r))
	}
}

// RecoveryInfo 一次恢复扫描的结论。
type RecoveryInfo struct {
	// Records 恢复出的完整条目数。
	Records uint64
	// Bytes 恢复出的有效字节数（含中间可能夹着的填充帧）。
	Bytes int64
	// FirstSeq 最旧可读条目的序号。
	FirstSeq uint64
	// NextSeq 下一个待分配的序号（= 最后一条的 seq + 1）。
	NextSeq uint64
	// ReadOff 读位物理偏移。
	ReadOff int64
	// WriteOff 尾部边界的物理偏移（后续 Append 从这里继续）。
	WriteOff int64
	// Stopped 停止原因。
	Stopped StopReason
	// StoppedOff 停止处的物理偏移。
	StoppedOff int64
}

// ringDist 返回环上从 from 到 to 的正向距离。
func ringDist(from, to, capacity int64) int64 {
	if capacity <= 0 {
		return 0
	}
	if to >= from {
		return to - from
	}
	return capacity - from + to
}

// recoverArea 顺序扇数据区，定出有效前缀与尾部边界。
//
// # 为什么从偏移 0 开始扇就已经足够
//
// 写入严格按物理顺序前进，同一圈内写过的物理区间永远是 [0, 写前沿) 的连续前缀。
// 所以从 0 扇到第一道断点，拿到的正好是「这一圈写过的全部」：
//   - 未环回：拿到 [0, writeOff)，包括已 Ack 的旧条目（它们还在磁盘上）。
//   - 已环回：拿到新一圈 [0, writeOff)；断点处是上一圈的残骸（seq 跳变）。
//
// # 遇到第一条异常即停，不跳过
//
// 跳过会把环回拼接点的残骸当数据（它们结构上完全合法）。停下的代价是「同圈内一条坏记录之后
// 的数据会丢」，但那本来就不可能靠扫文件名区分，而环中段损坏不是健康文件系统的预期事件。
//
// # 开销
//
// O(有效字节) 而非 O(容量)：扫到边界就停，不扫空着的容量。
func recoverArea(f *os.File, hdr fileHeader, collect bool) (RecoveryInfo, error) {
	_ = collect // 保留参数：未来若需「带内容恢复」可复用本函数的骨架

	info := RecoveryInfo{FirstSeq: firstSeq, NextSeq: firstSeq, Stopped: StopEmpty}
	capacity := hdr.capacity
	maxRecord := int64(hdr.maxRecord)

	hdrBuf := make([]byte, FrameHeaderSize)
	var prevSeq uint64
	off := int64(0)
	var payload []byte

	for {
		// 尾部不足一个帧头：到此为止。
		if capacity-off < FrameHeaderSize {
			if info.Records > 0 {
				info.Stopped = StopPartialHeader
			}
			info.StoppedOff = off
			break
		}
		if _, err := f.ReadAt(hdrBuf, FileHeaderSize+off); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				info.Stopped = StopPartialHeader
				info.StoppedOff = off
				break
			}
			return info, fmt.Errorf("spool: 恢复读帧头 (off=%d): %w", off, err)
		}

		// 填充帧：圈尾死区。恢复遇到它即停——跳过去会绕回偏移 0 无限扫描。
		if isPadHeader(hdrBuf) {
			info.Stopped = StopPad
			info.StoppedOff = off
			break
		}

		fr, ok := parseFrameHeader(hdrBuf, maxRecord)
		if !ok {
			if binary.LittleEndian.Uint32(hdrBuf[0:4]) != recordMagic {
				info.Stopped = StopBadMagic
			} else {
				info.Stopped = StopBadLength
			}
			info.StoppedOff = off
			break
		}

		// 序号必须严格连续递增：这是识破「上一圈残骸」与「半条记录头」的唯一手段。
		if prevSeq != 0 {
			if fr.seq != prevSeq+1 {
				info.Stopped = StopSeqGap
				info.StoppedOff = off
				break
			}
		} else if fr.seq != firstSeq {
			// 首条必须是 firstSeq：0 是「头落了盘而 seq 还没落」的半条记录;
			// 大于 firstSeq 则是上一圈的残骸。
			info.Stopped = StopSeqGap
			info.StoppedOff = off
			break
		}

		// 长度越过数据区尾部：荒谬。
		if fr.size() > capacity-off {
			info.Stopped = StopBadLength
			info.StoppedOff = off
			break
		}

		// 校验 payload CRC。
		if fr.length > 0 {
			if int64(cap(payload)) < int64(fr.length) {
				payload = make([]byte, fr.length)
			} else {
				payload = payload[:fr.length]
			}
			if _, err := f.ReadAt(payload, FileHeaderSize+off+FrameHeaderSize); err != nil {
				info.Stopped = StopBadCRC
				info.StoppedOff = off
				break
			}
			if crc32.Checksum(payload, castagnoli) != fr.crc {
				info.Stopped = StopBadCRC
				info.StoppedOff = off
				break // MUTATION
			}
		}

		// 通过：纳入有效前缀。
		if prevSeq == 0 {
			info.FirstSeq = fr.seq
		}
		prevSeq = fr.seq
		info.NextSeq = fr.seq + 1
		info.Records++
		info.Bytes += fr.size()
		off += fr.size()
		if off >= capacity {
			info.Stopped = StopClean
			info.StoppedOff = off
			break
		}
	}

	// 一条都没读出来时保持 StopEmpty（全新文件或首条即坏）。
	if info.Records > 0 && info.Stopped == StopEmpty {
		info.Stopped = StopClean
	}

	info.WriteOff = off
	info.ReadOff = 0
	if info.NextSeq < info.FirstSeq {
		info.NextSeq = info.FirstSeq
	}
	return info, nil
}
