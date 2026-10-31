package spool

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"sync"
)

// Options 打开缓冲的配置。零值字段表示用默认值。
type Options struct {
	// Capacity 数据区字节数，默认 DefaultCapacity（100MiB）。
	// 对已存在的文件以文件头记录的容量为准；两者冲突会报错（要换容量就换路径）。
	Capacity int64
	// MaxRecord 单条 payload 上限，默认 DefaultMaxRecord（1MiB）。
	MaxRecord int
}

// Record 从缓冲读回的一条条目。
type Record struct {
	// Seq 写入时分配的单调序号，从 1 开始。
	Seq uint64
	// Payload 写入时提交的原始字节。
	Payload []byte
}

// Stats 运行期计数，供观测与断言；不参与正确性。
type Stats struct {
	Records  uint64
	Bytes    int64
	Capacity int64
	ReadSeq  uint64
	WriteSeq uint64
	Dropped  uint64
	Walls    uint64
}

// Spool 有界环形磁盘缓冲。零值不可用，必须经 Open 构造。
//
// 所有方法可被多 goroutine 并发调用（由内部 mutex 串行化），但它们共用一条磁盘顺序写路径，
// 并发不会更快。
type Spool struct {
	mu sync.Mutex

	f    *os.File
	path string

	capacity  int64
	maxRecord int64

	// readSeq/writeSeq 单调序号（从 1 开始）；used 为可读区（含填充帧）的环字节数。
	// 不变量：readSeq == writeSeq ⇔ used == 0。
	readSeq, writeSeq uint64
	readOff, writeOff int64
	used              int64
	dropped           uint64
	walls             uint64

	hdrBuf [FrameHeaderSize]byte
	wbuf   []byte

	recovered RecoveryInfo
	closed    bool
}

// Open 打开（或在不存在时创建）环形缓冲文件。
func Open(path string, opts Options) (*Spool, error) {
	hdr := fileHeader{capacity: opts.Capacity, maxRecord: opts.MaxRecord}
	if hdr.capacity <= 0 {
		hdr.capacity = DefaultCapacity
	}
	if hdr.maxRecord <= 0 {
		hdr.maxRecord = DefaultMaxRecord
	}
	if err := validateHeaderShape(hdr); err != nil {
		return nil, err
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("spool: 打开缓冲文件 %s: %w", path, err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
		}
	}()

	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("spool: 取文件状态 %s: %w", path, err)
	}

	if st.Size() == 0 {
		if _, err := f.Write(hdr.marshal()); err != nil {
			return nil, fmt.Errorf("spool: 写文件头 %s: %w", path, err)
		}
		if err := f.Truncate(FileHeaderSize + hdr.capacity); err != nil {
			return nil, fmt.Errorf("spool: 初始化数据区 %s: %w", path, err)
		}
	} else {
		existing, herr := readFileHeader(f)
		if herr != nil {
			return nil, fmt.Errorf("spool: %s: %w", path, herr)
		}
		if opts.Capacity > 0 && opts.Capacity != existing.capacity {
			return nil, fmt.Errorf("spool: %s 已按容量 %d 创建，与请求的 %d 冲突（换容量请换路径）",
				path, existing.capacity, opts.Capacity)
		}
		hdr = existing
		if want := FileHeaderSize + hdr.capacity; st.Size() != want {
			if err := f.Truncate(want); err != nil {
				return nil, fmt.Errorf("spool: 校正数据区大小 %s: %w", path, err)
			}
		}
	}

	s := &Spool{
		f:         f,
		path:      path,
		capacity:  hdr.capacity,
		maxRecord: int64(hdr.maxRecord),
	}

	info, err := recoverArea(f, hdr, false)
	if err != nil {
		return nil, fmt.Errorf("spool: 恢复 %s: %w", path, err)
	}
	s.recovered = info
	s.readSeq, s.writeSeq = info.FirstSeq, info.NextSeq
	s.readOff, s.writeOff = info.ReadOff, info.WriteOff
	s.used = ringDist(s.readOff, s.writeOff, s.capacity)
	if s.readSeq == s.writeSeq {
		s.readOff, s.writeOff, s.used = 0, 0, 0
	}
	ok = true
	return s, nil
}

func readFileHeader(f *os.File) (fileHeader, error) {
	b := make([]byte, FileHeaderSize)
	if _, err := f.ReadAt(b, 0); err != nil {
		return fileHeader{}, fmt.Errorf("%w: 读文件头: %w", ErrNotSpoolFile, err)
	}
	return unmarshalFileHeader(b)
}

// Path 缓冲文件路径。
func (s *Spool) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

// Recovered 返回 Open 时的恢复结论。
func (s *Spool) Recovered() RecoveryInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recovered
}

// Len 当前可读条目数。
func (s *Spool) Len() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeSeq - s.readSeq
}

// Stats 取一份运行期计数快照。
func (s *Spool) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{
		Records:  s.writeSeq - s.readSeq,
		Bytes:    s.used,
		Capacity: s.capacity,
		ReadSeq:  s.readSeq,
		WriteSeq: s.writeSeq,
		Dropped:  s.dropped,
		Walls:    s.walls,
	}
}

// Append 追写一条条目，返回分配到的序号。
//
// 调用方**不得**依赖它落盘：本方法不 fsync，返回 nil 只意味着数据已交给 OS 页缓存。
func (s *Spool) Append(payload []byte) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendLocked(payload)
}

// AppendMany 在一个临界区内连续追加多条，返回已写条数与错误。
func (s *Spool) AppendMany(payloads [][]byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range payloads {
		if _, err := s.appendLocked(p); err != nil {
			return i, err
		}
	}
	return len(payloads), nil
}

// appendLocked 追加一条（调用方已持锁）。
func (s *Spool) appendLocked(payload []byte) (uint64, error) {
	if s.closed {
		return 0, ErrClosed
	}
	if int64(len(payload)) > s.maxRecord {
		return 0, fmt.Errorf("%w: payload %d 字节 > 上限 %d", ErrRecordTooLarge, len(payload), s.maxRecord)
	}
	recSize := FrameHeaderSize + int64(len(payload))

	off, waste, err := s.reserveLocked(recSize)
	if err != nil {
		return 0, err
	}

	// 圈尾死区若放得下一个帧头，就显式落一道填充帧。
	if waste >= FrameHeaderSize {
		putPadHeader(s.hdrBuf[:], waste)
		if _, err := s.f.WriteAt(s.hdrBuf[:], FileHeaderSize+s.capacity-waste); err != nil {
			return 0, fmt.Errorf("spool: 写圈尾填充帧 (off=%d): %w", s.capacity-waste, err)
		}
		s.walls++
	}

	seq := s.writeSeq
	if int64(cap(s.wbuf)) < recSize {
		s.wbuf = make([]byte, recSize)
	}
	buf := s.wbuf[:recSize]
	putFrameHeader(buf, frame{
		seq:    seq,
		length: uint32(len(payload)),
		crc:    crc32.Checksum(payload, castagnoli),
	})
	copy(buf[FrameHeaderSize:], payload)
	// 一条记录一次 WriteAt：不 fsync、不拆小写，让页缓存合并成顺序写。
	if _, err := s.f.WriteAt(buf, FileHeaderSize+off); err != nil {
		return 0, fmt.Errorf("spool: 写记录 (seq=%d off=%d): %w", seq, off, err)
	}

	s.writeSeq++
	s.used += waste + recSize
	s.writeOff = off + recSize
	if s.writeOff >= s.capacity {
		s.writeOff = 0
	}
	return seq, nil
}

// placementLocked 计算本次写入的物理落点：能原地放就原地放，放不下就回绕到 0
// 并把圈尾剩余空间记为死区（waste）。
//
// 注意返回值只是**几何候选**，能不能真的用由 reserveLocked 按当前 used 判定。
func (s *Spool) placementLocked(recSize int64) (off, waste int64) {
	if s.writeOff+recSize > s.capacity {
		return 0, s.capacity - s.writeOff
	}
	return s.writeOff, 0
}

// reserveLocked 选一个能放 recSize 的落点，放不下就丢弃最旧条目腾空间。
//
// # 容量判断为何用「回绕后的占用量」而不是「used + waste + recSize」
//
// 回绕时写位归零、从 0 重新开始写，但**读位可能也在 0 之前的位置**（已环回多圈）。
// 此时真正的判据是：回绕后写的新区间 [0, recSize) 会不会撞上读位。
// 换句话：回绕后需要腾出的空间 = [0, recSize) 与当前可读区 [readOff, ...) 的重叠。
//
// 当 readOff == 0 时重叠就是整个 recSize；当 readOff > recSize 时完全没有重叠。
// 所以不能简单用 used+waste+recSize ≤ capacity（那会把不重叠的情况也当成放不下）。
func (s *Spool) reserveLocked(recSize int64) (off, waste int64, err error) {
	if recSize > s.capacity {
		return 0, 0, fmt.Errorf("%w: 记录 %d 字节超过数据区容量 %d", ErrRecordTooLarge, recSize, s.capacity)
	}
	for {
		off, waste = s.placementLocked(recSize)

		// 需要腾出多少空间才对？
		// 不回绕：新记录占 [writeOff, writeOff+recSize)，不能压到可读区（从 readOff 起算长度为 used）。
		// 回绕：新记录占 [0, recSize)，同样不能压到可读区。
		var need int64
		if waste == 0 {
			need = s.used - ringDist(s.writeOff, s.readOff, s.capacity) + recSize
			if need < 0 {
				need = 0
			}
		} else {
			// 回绕后的新记录从 0 开始。若读位也在 0 附近（readOff < recSize），
			// 则必须先腾掉 [0, recSize) 这段（它正是最旧的数据）。
			if s.readOff < recSize && s.readSeq < s.writeSeq {
				need = recSize - s.readOff
			} else {
				need = 0
			}
		}

		if need <= 0 || s.readSeq == s.writeSeq {
			if s.readSeq == s.writeSeq {
				// 缓冲已空：直接把写位归零重来，不会覆盖任何未投递数据。
				s.readOff, s.writeOff, s.used = 0, 0, 0
				return 0, 0, nil
			}
			return off, waste, nil
		}
		if err := s.dropOldestLocked(); err != nil {
			return 0, 0, err
		}
	}
}

// dropOldestLocked 丢弃最旧一条（不读 payload，只走帧头）。
func (s *Spool) dropOldestLocked() error {
	if s.readSeq == s.writeSeq {
		return nil
	}
	off, err := s.readFrameOffLocked(s.readOff)
	if err != nil {
		return err
	}
	f, next, err := s.locate(off)
	if err != nil {
		return err
	}
	_ = f
	s.used -= ringDist(s.readOff, next, s.capacity)
	s.readOff = next
	s.readSeq++
	s.dropped++
	if s.readSeq == s.writeSeq {
		s.used = 0
		s.readOff, s.writeOff = 0, 0
	}
	return nil
}

// readFrameOffLocked 从 off 出发跳过可能存在的圈尾填充帧，返回真正读到记录的位置。
//
// 读侧（Peek/Ack/丢弃）都必须先过这一步，否则会把填充帧当成记录，
// 或像修前的 bug 那样用错误偏移读 payload。
func (s *Spool) readFrameOffLocked(off int64) (int64, error) {
	for i := 0; i < 2; i++ { // 最多跳过一道填充帧（环里不可能连两道）
		isPad, next, err := s.skipPadAt(off)
		if err != nil {
			return 0, err
		}
		if !isPad {
			return off, nil
		}
		off = next
	}
	return off, nil
}

// locate 从偏移 off 处解析一条记录头。
//
// # 为什么不许记录跨圈尾
//
// 本包的写路径（appendLocked）保证：圈尾放不下时先写填充帧再回绕，所以**记录永不跨尾**。
// 若一条帧头声称的长度使它跨过圈尾，那它只能是**残骸**（旧版本、写到一半的掉电、外部修改）。
// 必须拒绝，否则 ReadAt 会读到数据区之外（EOF），把“文件坏了”报成“读失败”。
//
// # 为什么不在这里自动跳过填充帧
//
// 这里曾经在遇到填充帧时内部跳回 0 并继续解析，但它返回的 `next` 是基于跳转后的位置算的，
// 而**调用方（Peek/Ack）依旧用原始 off 去读 payload** ⇒ 读到数据区之外。
// 那个 bug 的教训：定位与读取必须是同一个偏移。现在把“跳过填充帧”交给调用方的循环，
// locate 只负责“在给定位置解释一条记录，否则报错”。
//
// 返回：记录头、下一条的物理偏移（已归零）、错误。
func (s *Spool) locate(off int64) (f frame, next int64, err error) {
	if off == s.capacity {
		off = 0
	}
	if s.capacity-off < FrameHeaderSize {
		return frame{}, 0, fmt.Errorf("%w: 偏移 %d 处不足一个帧头", ErrCorrupt, off)
	}
	if _, rerr := s.f.ReadAt(s.hdrBuf[:], FileHeaderSize+off); rerr != nil {
		return frame{}, 0, fmt.Errorf("spool: 读帧头 (off=%d): %w", off, rerr)
	}
	var ok bool
	f, ok = parseFrameHeader(s.hdrBuf[:], s.maxRecord)
	if !ok {
		return frame{}, 0, fmt.Errorf("%w: 偏移 %d 处帧头不可解析", ErrCorrupt, off)
	}
	// 记录必须完整落在圈内：跨尾就会被当成残骸。
	if off+f.size() > s.capacity {
		return frame{}, 0, fmt.Errorf("%w: 偏移 %d 处记录跨度 [%d,%d) 越过数据区末尾 %d",
			ErrCorrupt, off, off, off+f.size(), s.capacity)
	}
	next = off + f.size()
	if next >= s.capacity {
		next = 0
	}
	return f, next, nil
}

// skipPadAt 报告偏移 off 处是否是一道圈尾填充帧，并返回越过它之后的偏移。
//
// 让“跳过死区”成为调用方可见的显式一步，而不是藏在 locate 里——参见 locate 头部那段教训。
func (s *Spool) skipPadAt(off int64) (bool, int64, error) {
	if s.capacity-off < FrameHeaderSize {
		return false, 0, nil
	}
	if _, err := s.f.ReadAt(s.hdrBuf[:], FileHeaderSize+off); err != nil {
		return false, 0, fmt.Errorf("spool: 读帧头 (off=%d): %w", off, err)
	}
	if !isPadHeader(s.hdrBuf[:]) {
		return false, 0, nil
	}
	dead := int64(binary.LittleEndian.Uint32(s.hdrBuf[4:8]))
	if dead <= 0 || off+dead > s.capacity {
		return false, 0, fmt.Errorf("%w: 偏移 %d 处填充帧死区 %d 越界", ErrCorrupt, off, dead)
	}
	next := off + dead
	if next >= s.capacity {
		next = 0
	}
	return true, next, nil
}

// Peek 从读位开始读最多 max 条，**不**推进读位。
func (s *Spool) Peek(max int) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	available := s.writeSeq - s.readSeq
	if max <= 0 || available == 0 {
		return nil, nil
	}
	if uint64(max) > available {
		max = int(available)
	}
	out := make([]Record, 0, max)
	off := s.readOff
	for i := 0; i < max; i++ {
		// 先跳过可能存在的圈尾填充帧：定位与读取必须是同一个偏移。
		realOff, oerr := s.readFrameOffLocked(off)
		if oerr != nil {
			return out, oerr
		}
		f, next, err := s.locate(realOff)
		if err != nil {
			return out, err
		}
		if want := s.readSeq + uint64(i); f.seq != want {
			return out, fmt.Errorf("%w: 偏移 %d 处序号 %d，期望 %d", ErrCorrupt, realOff, f.seq, want)
		}
		payload, err := s.readPayload(realOff, f)
		if err != nil {
			return out, err
		}
		out = append(out, Record{Seq: f.seq, Payload: payload})
		off = next
	}
	return out, nil
}

// readPayload 读出并校验一条记录的 payload。
func (s *Spool) readPayload(off int64, f frame) ([]byte, error) {
	if f.length == 0 {
		if crc32.Checksum(nil, castagnoli) != f.crc {
			return nil, fmt.Errorf("%w: 偏移 %d 处空 payload CRC 不符", ErrCorrupt, off)
		}
		return nil, nil
	}
	buf := make([]byte, f.length)
	if _, err := s.f.ReadAt(buf, FileHeaderSize+off+FrameHeaderSize); err != nil {
		return nil, fmt.Errorf("spool: 读 payload (off=%d): %w", off, err)
	}
	if crc32.Checksum(buf, castagnoli) != f.crc {
		return nil, fmt.Errorf("%w: 偏移 %d 处 payload CRC 不符", ErrCorrupt, off)
	}
	return buf, nil
}

// Ack 推进读位，丢弃最旧的 n 条（已投递成功的那批）。
func (s *Spool) Ack(n int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if n < 0 {
		return fmt.Errorf("%w: 不能为负 %d", ErrBadAck, n)
	}
	available := s.writeSeq - s.readSeq
	if uint64(n) > available {
		return fmt.Errorf("%w: 可读 %d 条，请求确认 %d 条", ErrBadAck, available, n)
	}
	for i := 0; i < n; i++ {
		off, err := s.readFrameOffLocked(s.readOff)
		if err != nil {
			return err
		}
		f, next, err := s.locate(off)
		if err != nil {
			return err
		}
		_, _ = f, next
		s.used -= ringDist(s.readOff, next, s.capacity)
		s.readOff = next
		s.readSeq++
	}
	if s.readSeq == s.writeSeq {
		s.used = 0
		s.readOff, s.writeOff = 0, 0
	}
	return nil
}

// Close 关闭文件描述符。不刷盘、不 sync（红线）。
func (s *Spool) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.f.Close()
}

// 确保 binary 被用到。
var _ = binary.LittleEndian
