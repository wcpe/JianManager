package spool

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

// 磁盘格式常量。改动其中任何一项都必须同步 FileMagic 的版本位，否则旧文件会被按新格式解析。
const (
	// FileMagic 文件头魔数（8 字节 ASCII，末位是格式版本）。
	FileMagic = "JMSPOOL1"

	// formatVersion 与 FileMagic 的版本位同步。
	formatVersion = 1

	// FileHeaderSize 文件头字节数（定长，只在创建文件时写一次、此后从不改写）。
	FileHeaderSize = 64

	// FrameHeaderSize 每条帧的定长头字节数。
	FrameHeaderSize = 24

	// DefaultCapacity 默认数据区容量（对齐官方 vlagent 的 maxDiskUsagePerURL 量级）。
	DefaultCapacity int64 = 100 << 20

	// DefaultMaxRecord 默认单条 payload 上限（1MiB）。超过它的记录不会被写入。
	DefaultMaxRecord = 1 << 20

	// firstSeq 首个分配到的序号。序号从 1 起是硬约束，0 永远非法（见 doc.go）。
	firstSeq uint64 = 1
)

// 两种帧的魔数（小端存储的 4 字节 ASCII）。
const (
	recordMagic uint32 = 0x50534D4A // "JMSP" 数据记录
	padMagic    uint32 = 0x4450414A // "JAPD" 圈尾死区填充帧
)

// castagnoli CRC32C 查表。选 CRC32C 而非 IEEE：amd64 有 SSE4.2 硬件指令，
// 校验整环只需几十毫秒；IEEE 只有软件表，且两者对「识别掉电截断」的检错能力无差别。
var castagnoli = crc32.MakeTable(crc32.Castagnoli)

var (
	// ErrNotSpoolFile 目标文件不是本缓冲格式（魔数、版本、头长度或头里的容量不合法）。
	// 此时本包不会写入任何字节，避免把别人的文件改坏。
	ErrNotSpoolFile = errors.New("spool: 文件不是本缓冲格式")

	// ErrRecordTooLarge 单条记录超过 MaxRecord（或超过数据区容量），永远装不下。
	// 调用方应当把它当作「这条丢弃」处理，而不是重试。
	ErrRecordTooLarge = errors.New("spool: 记录超过单条上限")

	// ErrCorrupt 运行时发现记录不再自洽。
	// 正常路径不会出现：出现即说明有人在改这个文件，或恢复漏了情况。
	// 刻意不静默跳过——把问题顶回调用方，由它 Close + 重新 Open 让恢复重新定边界。
	ErrCorrupt = errors.New("spool: 记录损坏")

	// ErrClosed 缓冲已关闭。
	ErrClosed = errors.New("spool: 缓冲已关闭")

	// ErrBadAck 确认条数超过可读条数。
	ErrBadAck = errors.New("spool: 确认条数非法")
)

// errHeaderCRC 文件头 CRC 不符。与 ErrNotSpoolFile 区分开：
// 后者说明根本不是我们的文件（不能动），前者说明是我们的文件但头坏了。
var errHeaderCRC = errors.New("spool: 文件头 CRC 不符")

// fileHeader 文件头语义：数据区容量与单条 payload 上限。
type fileHeader struct {
	capacity  int64
	maxRecord int
}

// marshal 编码 64 字节文件头，crc 覆盖 [0,32) 的 CRC32C。
func (h fileHeader) marshal() []byte {
	b := make([]byte, FileHeaderSize)
	copy(b[0:8], FileMagic)
	binary.LittleEndian.PutUint32(b[8:], formatVersion)
	binary.LittleEndian.PutUint32(b[12:], FileHeaderSize)
	binary.LittleEndian.PutUint64(b[16:], uint64(h.capacity))
	binary.LittleEndian.PutUint32(b[24:], uint32(h.maxRecord))
	// b[28:32] 保留位与 b[36:64] 填充恒为 0。
	binary.LittleEndian.PutUint32(b[32:], crc32.Checksum(b[:32], castagnoli))
	return b
}

// unmarshalFileHeader 解析文件头。魔数/版本/头长度/容量不合法时返回 ErrNotSpoolFile；
// CRC 不符时返回 errHeaderCRC（数据的可用性靠逐条记录的 CRC，不靠它）。
func unmarshalFileHeader(b []byte) (fileHeader, error) {
	if len(b) < FileHeaderSize {
		return fileHeader{}, fmt.Errorf("%w: 文件头只有 %d 字节", ErrNotSpoolFile, len(b))
	}
	if string(b[0:8]) != FileMagic {
		return fileHeader{}, fmt.Errorf("%w: 魔数 %q", ErrNotSpoolFile, string(b[0:8]))
	}
	if v := binary.LittleEndian.Uint32(b[8:]); v != formatVersion {
		return fileHeader{}, fmt.Errorf("%w: 格式版本 %d", ErrNotSpoolFile, v)
	}
	if hs := binary.LittleEndian.Uint32(b[12:]); hs != FileHeaderSize {
		return fileHeader{}, fmt.Errorf("%w: 头长度 %d", ErrNotSpoolFile, hs)
	}
	h := fileHeader{
		capacity:  int64(binary.LittleEndian.Uint64(b[16:])),
		maxRecord: int(binary.LittleEndian.Uint32(b[24:])),
	}
	// 容量荒谬的文件头不可用：它会让寻址与容量判定全部失准。
	if h.maxRecord < 1 || h.capacity < 2*FrameHeaderSize+int64(h.maxRecord) {
		return fileHeader{}, fmt.Errorf("%w: 容量 %d / 单条上限 %d 不合法", ErrNotSpoolFile, h.capacity, h.maxRecord)
	}
	if crc32.Checksum(b[:32], castagnoli) != binary.LittleEndian.Uint32(b[32:]) {
		return h, errHeaderCRC
	}
	return h, nil
}

// validateHeaderShape 校验容量与单条上限的组合能不能承载最大的那条记录。
//
// 要求 capacity ≥ 2*FrameHeaderSize + maxRecord：一条满尺寸记录回绕到偏移 0 时占
// [0, 24+maxRecord)，还得给下一次回绕在圈尾留得下一道填充帧头（24B），否则会出现
// 「圈尾剩下的字节既放不下记录也放不下填充帧」、谁都跳不过去的死锁。
func validateHeaderShape(hdr fileHeader) error {
	if hdr.maxRecord < 1 {
		return fmt.Errorf("spool: 单条记录上限 %d 不合法", hdr.maxRecord)
	}
	if need := 2*FrameHeaderSize + int64(hdr.maxRecord); hdr.capacity < need {
		return fmt.Errorf("spool: 容量 %d 小于下限 %d (= 2×%d 帧头 + %d 单条上限)",
			hdr.capacity, need, FrameHeaderSize, hdr.maxRecord)
	}
	return nil
}

// frame 是一条已通过魔数与长度本地校验的记录头。payload 的 CRC 由调用方校验。
type frame struct {
	seq    uint64
	length uint32
	crc    uint32
}

// size 该记录占用的总字节数（定长头 + payload）。
func (f frame) size() int64 { return FrameHeaderSize + int64(f.length) }

// parseFrameHeader 解析 24 字节记录头并做**本地**校验（魔数、长度上限、保留位）。
// 返回 false 即「此处不是一条可用的完整记录」。
//
// 校验保留位是刻意的：新写入的保留位恒为 0，于是残骸即便碰巧凑出合法魔数与合法长度，
// 还要再过一道 2^-32 的关卡，不花额外成本。
func parseFrameHeader(b []byte, maxRecord int64) (frame, bool) {
	if len(b) < FrameHeaderSize {
		return frame{}, false
	}
	if binary.LittleEndian.Uint32(b[0:4]) != recordMagic {
		return frame{}, false
	}
	length := binary.LittleEndian.Uint32(b[4:8])
	if int64(length) > maxRecord {
		return frame{}, false
	}
	if binary.LittleEndian.Uint32(b[20:24]) != 0 {
		return frame{}, false
	}
	return frame{
		seq:    binary.LittleEndian.Uint64(b[12:20]),
		length: length,
		crc:    binary.LittleEndian.Uint32(b[8:12]),
	}, true
}

// putFrameHeader 写入 24 字节记录头。dst 长度必须 ≥ FrameHeaderSize。
func putFrameHeader(dst []byte, f frame) {
	binary.LittleEndian.PutUint32(dst[0:4], recordMagic)
	binary.LittleEndian.PutUint32(dst[4:8], f.length)
	binary.LittleEndian.PutUint32(dst[8:12], f.crc)
	binary.LittleEndian.PutUint64(dst[12:20], f.seq)
	binary.LittleEndian.PutUint32(dst[20:24], 0)
}

// putPadHeader 写入圈尾死区填充帧头：告知读写两侧「从这到数据区末尾全是死区」。
//
// 没有它就无法区分「这里是放不下而跳过的死区」与「这里是上一圈的真记录」——
// 后者结构上完全合法，会被当成活数据。
func putPadHeader(dst []byte, dead int64) {
	binary.LittleEndian.PutUint32(dst[0:4], padMagic)
	binary.LittleEndian.PutUint32(dst[4:8], uint32(dead))
	for i := 8; i < FrameHeaderSize; i++ {
		dst[i] = 0
	}
}

// isPadHeader 报告这 24 字节是不是填充帧头。
func isPadHeader(b []byte) bool {
	return len(b) >= FrameHeaderSize && binary.LittleEndian.Uint32(b[0:4]) == padMagic
}
