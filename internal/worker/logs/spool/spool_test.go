package spool

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// testCapacity 测试用小容量，让“写满环回”跑得快。
// 必须满足 capacity ≥ 2*FrameHeaderSize + maxRecord。
const (
	testCapacity  = 64 * 1024
	testMaxRecord = 4 * 1024
)

func newTestSpool(t *testing.T, dir string) *Spool {
	t.Helper()
	s, err := Open(filepath.Join(dir, "spool.bin"), Options{Capacity: testCapacity, MaxRecord: testMaxRecord})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func payloadOf(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return b
}

// TestBasicRoundTrip 基本读写往返。
func TestBasicRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := newTestSpool(t, dir)
	defer s.Close()

	want := [][]byte{[]byte("hello"), []byte("world"), payloadOf(100)}
	for _, w := range want {
		if _, err := s.Append(w); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if got := s.Len(); got != 3 {
		t.Fatalf("Len = %d, want 3", got)
	}
	recs, err := s.Peek(10)
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("Peek 返回 %d 条, want 3", len(recs))
	}
	for i, r := range recs {
		if !bytes.Equal(r.Payload, want[i]) {
			t.Fatalf("第 %d 条内容不符: got %q want %q", i, r.Payload, want[i])
		}
		if r.Seq != uint64(i+1) {
			t.Fatalf("第 %d 条 seq = %d, want %d", i, r.Seq, i+1)
		}
	}
}

// TestAckAdvances 投递成功后推进读位。
func TestAckAdvances(t *testing.T) {
	dir := t.TempDir()
	s := newTestSpool(t, dir)
	defer s.Close()

	for i := 0; i < 5; i++ {
		if _, err := s.Append([]byte(fmt.Sprintf("rec-%d", i))); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := s.Ack(2); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if got := s.Len(); got != 3 {
		t.Fatalf("Ack 2 后 Len = %d, want 3", got)
	}
	recs, _ := s.Peek(10)
	if string(recs[0].Payload) != "rec-2" {
		t.Fatalf("Ack 后首条 = %q, want rec-2", recs[0].Payload)
	}
	// 超量 Ack 必须报错，不得静默截断。
	if err := s.Ack(100); err == nil {
		t.Fatal("Ack 超量应当报错")
	}
}

// TestReopenRecovers 正常关闭后重开，数据仍在。
func TestReopenRecovers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spool.bin")

	s, err := Open(path, Options{Capacity: testCapacity, MaxRecord: testMaxRecord})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 0; i < 7; i++ {
		if _, err := s.Append([]byte(fmt.Sprintf("rec-%d", i))); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(path, Options{Capacity: testCapacity, MaxRecord: testMaxRecord})
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	defer s2.Close()

	if got := s2.Len(); got != 7 {
		t.Fatalf("重开后 Len = %d, want 7（未 Ack 的必须全部恢复）", got)
	}
	recs, err := s2.Peek(10)
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	for i, r := range recs {
		want := fmt.Sprintf("rec-%d", i)
		if string(r.Payload) != want {
			t.Fatalf("第 %d 条 = %q, want %q", i, r.Payload, want)
		}
	}
	info := s2.Recovered()
	if info.Records != 7 {
		t.Fatalf("Recovered.Records = %d, want 7", info.Records)
	}
}

// TestRecoverTornTail 掉电在尾部写了半条：已完整的那部分必须全部可用。
//
// 这是本包最关键的用例：不 fsync 就意味着尾部随时可能是半条，
// 不能因此把整个缓冲当成坏的。
func TestRecoverTornTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spool.bin")

	s, err := Open(path, Options{Capacity: testCapacity, MaxRecord: testMaxRecord})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := s.Append([]byte(fmt.Sprintf("rec-%d", i))); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	// 再写一条，但只把它的头写进去、body 截掉一半（模拟掉电）。
	if _, err := s.Append(payloadOf(200)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	s.Close()

	// 手工把文件尾部截断：切掉最后 100 字节。
	//
	// **不能**直接截文件末 100 字节：文件是定长的（FileHeaderSize+capacity），
	// 真正的数据只占 [FileHeaderSize, FileHeaderSize+used)，后面全是零区。
	// 要模拟“掉电只写了一半”，必须截在**数据写入位置之后**。
	used := s.Stats().Bytes
	if err := os.Truncate(path, FileHeaderSize+used-100); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path, Options{Capacity: testCapacity, MaxRecord: testMaxRecord})
	if err != nil {
		t.Fatalf("带着截断尾部重开必须成功，得到: %v", err)
	}
	defer s2.Close()

	// 前 5 条必须完好。
	if got := s2.Len(); got != 5 {
		t.Fatalf("截断后 Len = %d, want 5（完整的前 5 条必须保留）", got)
	}
	recs, perr := s2.Peek(10)
	if perr != nil && len(recs) != 5 {
		t.Fatalf("Peek 出错且未拿到 5 条: %v", perr)
	}
	for i := 0; i < 5; i++ {
		want := fmt.Sprintf("rec-%d", i)
		if string(recs[i].Payload) != want {
			t.Fatalf("第 %d 条 = %q, want %q", i, recs[i].Payload, want)
		}
	}
	if info := s2.Recovered(); info.Stopped != StopPartialHeader && info.Stopped != StopBadCRC {
		t.Logf("停因 = %v（允许 partial-header 或 bad-crc）", info.Stopped)
	}

	// 关键：恢复后还能继续写，且新写的能读回。
	if _, err := s2.Append([]byte("after-recovery")); err != nil {
		t.Fatalf("恢复后写入失败: %v", err)
	}
	recs2, err := s2.Peek(20)
	if err != nil {
		t.Fatalf("恢复后 Peek: %v", err)
	}
	last := recs2[len(recs2)-1]
	if string(last.Payload) != "after-recovery" {
		t.Fatalf("恢复后写入的末条 = %q", last.Payload)
	}
}

// TestBadCRCDoesNotSkip 中间插入一条 CRC 损坏的记录：
// 必须停在它那里，**不能**跳过继续读后面的（那会把上一圈残骸当数据）。
func TestBadCRCDoesNotSkip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spool.bin")

	s, err := Open(path, Options{Capacity: testCapacity, MaxRecord: testMaxRecord})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := s.Append([]byte(fmt.Sprintf("rec-%d", i))); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	s.Close()

	// 把第 3 条（下标 2）的 payload 改坏一个字节。
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	offsets := frameOffsets(t, f, 5)
	badOff := FileHeaderSize + offsets[2] + FrameHeaderSize + 1
	buf := make([]byte, 1)
	if _, err := f.ReadAt(buf, badOff); err != nil {
		t.Fatal(err)
	}
	buf[0] ^= 0xFF
	if _, err := f.WriteAt(buf, badOff); err != nil {
		t.Fatal(err)
	}
	f.Close()

	s2, err := Open(path, Options{Capacity: testCapacity, MaxRecord: testMaxRecord})
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	defer s2.Close()

	info := s2.Recovered()
	if info.Records != 2 {
		t.Fatalf("坏记录前只有 2 条完整，Recovered.Records = %d, want 2", info.Records)
	}
	if info.Stopped != StopBadCRC {
		t.Fatalf("停因 = %v, want %v", info.Stopped, StopBadCRC)
	}
	// 第 3、4 条不得被当成可用数据（这是“不跳过”的核心断言）。
	if got := s2.Len(); got != 2 {
		t.Fatalf("Len = %d, want 2（坏记录之后的数据不得被读出）", got)
	}
}

// TestCapacityEvictsOldest 写满后丢最旧。
func TestCapacityEvictsOldest(t *testing.T) {
	dir := t.TempDir()
	s := newTestSpool(t, dir)
	defer s.Close()

	payload := payloadOf(997)
	n := 0
	// 容量 64KiB / 每条 1021 字节（997+24 帧头）⇒ 能装约 64 条；写 200 条必然淘汰。
	//
	// 为什么用 997 而不是 1000：1024 进 65536 正好整除（64×1024=65536），
	// 写位每次恰好落在圈尾，永远不会留下死区，于是永远不产生填充帧。
	// 那是**正确行为**（没死区就不需要填充），但会让“必然有 Walls”的断言失败。
	// 用一个除不尽的尺寸，让圈尾必然留下死区。
	for i := 0; i < 200; i++ {
		if _, err := s.Append(payload); err != nil {
			t.Fatalf("Append: %v", err)
		}
		n++
	}
	st := s.Stats()
	if st.Dropped == 0 {
		t.Fatalf("写 %d 条后应当有淘汰，Dropped = 0", n)
	}
	if st.Bytes > st.Capacity {
		t.Fatalf("可读字节 %d 超过容量 %d", st.Bytes, st.Capacity)
	}
	if st.Walls == 0 {
		t.Fatal("应当至少发生过一次圈尾填充")
	}
	// 最新的那条必须还在。
	recs, err := s.Peek(int(st.Records))
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if len(recs) == 0 {
		t.Fatal("淘汰后仍应有数据")
	}
	if recs[len(recs)-1].Seq != st.WriteSeq-1 {
		t.Fatalf("末条 seq = %d, want %d", recs[len(recs)-1].Seq, st.WriteSeq-1)
	}
}

// TestOversizedRecordRejected 单条超限直接拒收，不写入。
func TestOversizedRecordRejected(t *testing.T) {
	dir := t.TempDir()
	s := newTestSpool(t, dir)
	defer s.Close()

	if _, err := s.Append(payloadOf(testMaxRecord + 1)); err == nil {
		t.Fatal("超过单条上限必须报错")
	}
	if got := s.Len(); got != 0 {
		t.Fatalf("拒收后 Len = %d, want 0", got)
	}
}

// TestNotSpoolFileRefused 别人的文件不得被改坏。
func TestNotSpoolFileRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "other.bin")
	if err := os.WriteFile(path, []byte("this is not a spool file at all, but long enough to fill the header area 1234567890"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, Options{}); err == nil {
		t.Fatal("非本格式文件必须拒绝打开")
	}
}

// TestEmptyFileFresh 全新文件正常初始化。
func TestEmptyFileFresh(t *testing.T) {
	dir := t.TempDir()
	s := newTestSpool(t, dir)
	defer s.Close()
	if got := s.Len(); got != 0 {
		t.Fatalf("新文件 Len = %d, want 0", got)
	}
	recs, err := s.Peek(10)
	if err != nil {
		t.Fatalf("空文件 Peek 不应报错: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("空文件 Peek 返回 %d 条", len(recs))
	}
}

// TestRecoverBoundedScan 恢复成本 O(有效字节)，不扫空容量。
func TestRecoverBoundedScan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spool.bin")

	s, err := Open(path, Options{Capacity: 1 << 20, MaxRecord: testMaxRecord})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// 只写 10 条（约 300 字节），容量 1MiB。
	for i := 0; i < 10; i++ {
		if _, err := s.Append([]byte(fmt.Sprintf("rec-%d", i))); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	s.Close()

	s2, err := Open(path, Options{Capacity: 1 << 20, MaxRecord: testMaxRecord})
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	defer s2.Close()
	info := s2.Recovered()
	if info.Records != 10 {
		t.Fatalf("Recovered.Records = %d, want 10", info.Records)
	}
	// 停位应当在写完的位置附近，而不是扫到 1MiB 末尾。
	if info.StoppedOff > 4096 {
		t.Fatalf("恢复扇了 %d 字节，应当只扫有效前缀（~300B）", info.StoppedOff)
	}
}

// TestConcurrentAppend 并发写入安全。
func TestConcurrentAppend(t *testing.T) {
	dir := t.TempDir()
	s := newTestSpool(t, dir)
	defer s.Close()

	const goroutines, perG = 8, 20
	done := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			for i := 0; i < perG; i++ {
				if _, err := s.Append([]byte(fmt.Sprintf("g%d-%d", g, i))); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}(g)
	}
	for i := 0; i < goroutines; i++ {
		if err := <-done; err != nil {
			t.Fatalf("并发 Append: %v", err)
		}
	}
	st := s.Stats()
	total := uint64(goroutines * perG)
	if st.Dropped > 0 {
		t.Logf("发生淘汰 %d 条（容量不够，不算错）", st.Dropped)
	}
	if st.WriteSeq-st.ReadSeq != total-st.Dropped {
		t.Fatalf("可读 %d + 淘汰 %d != 写入 %d", st.WriteSeq-st.ReadSeq, st.Dropped, total)
	}
}

// frameOffsets 返回前 n 条记录在数据区内的物理偏移。
func frameOffsets(t *testing.T, f *os.File, n int) []int64 {
	t.Helper()
	out := make([]int64, 0, n)
	hdr := make([]byte, FrameHeaderSize)
	off := int64(0)
	for i := 0; i < n; i++ {
		if _, err := f.ReadAt(hdr, FileHeaderSize+off); err != nil {
			t.Fatalf("读第 %d 条帧头: %v", i, err)
		}
		length := int64(binary.LittleEndian.Uint32(hdr[4:8]))
		out = append(out, off)
		off += FrameHeaderSize + length
	}
	return out
}
