package spool

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

// TestRecoverStopsOnSeqGap 序号不连续必须停在断点。
//
// 这是「识破上一圈残骸」的唯一手段：环跑满一圈后新记录从偏移 0 覆盖旧字节，
// 写前沿后面留下的是上一圈的残骸——它们**依旧是 CRC 合法的记录**，只能靠序号识破。
//
// 变异验证（已实测）：把 `if fr.seq != prevSeq+1` 改成 `if false && ...` 后，本用例转红：
// 恢复会把残骸当成有效数据继续往后读。
func TestRecoverStopsOnSeqGap(t *testing.T) {
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

	// 把第 3 条（下标 2）的 seq 改成一个跳跃值（模拟「上一圈残骸」）。
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	offsets := frameOffsets(t, f, 5)
	seqOff := FileHeaderSize + offsets[2] + 12 // seq 在帧头 [12:20]
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, 999) // 跳号
	if _, err := f.WriteAt(buf, seqOff); err != nil {
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
		t.Fatalf("序号跳变前只有 2 条有效，Recovered.Records = %d, want 2", info.Records)
	}
	if info.Stopped != StopSeqGap {
		t.Fatalf("停因 = %v, want %v", info.Stopped, StopSeqGap)
	}
	if got := s2.Len(); got != 2 {
		t.Fatalf("Len = %d, want 2（序号断点之后不得被当成有效数据）", got)
	}
}

// TestRecoverStopsOnBadMagic 魔数不符必须停在断点（不是报错、不是清空）。
func TestRecoverStopsOnBadMagic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spool.bin")

	s, err := Open(path, Options{Capacity: testCapacity, MaxRecord: testMaxRecord})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 0; i < 4; i++ {
		if _, err := s.Append([]byte(fmt.Sprintf("rec-%d", i))); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	s.Close()

	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	offsets := frameOffsets(t, f, 4)
	// 把第 3 条（下标 2）的魔数抹掉。
	if _, err := f.WriteAt([]byte{0, 0, 0, 0}, FileHeaderSize+offsets[2]); err != nil {
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
		t.Fatalf("Recovered.Records = %d, want 2", info.Records)
	}
	if info.Stopped != StopBadMagic {
		t.Fatalf("停因 = %v, want %v", info.Stopped, StopBadMagic)
	}
}

// TestRecoveryKeepsWorkingAfterStop 停在坏记录上之后，**后续写入必须能继续**。
//
// 这是「一条坏记录不能阻塞后面所有记录」的正面验收：不是「跳过它」，
// 而是「把边界定在它那里、接着写」。如果实现变成一遇损坏就永久拒绝写入，本用例转红。
func TestRecoveryKeepsWorkingAfterStop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spool.bin")

	s, err := Open(path, Options{Capacity: testCapacity, MaxRecord: testMaxRecord})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.Append([]byte(fmt.Sprintf("rec-%d", i))); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	s.Close()

	// 破坏第 3 条的 CRC。
	f, _ := os.OpenFile(path, os.O_RDWR, 0o600)
	offsets := frameOffsets(t, f, 3)
	badOff := FileHeaderSize + offsets[2] + FrameHeaderSize
	one := make([]byte, 1)
	f.ReadAt(one, badOff)
	one[0] ^= 0xFF
	f.WriteAt(one, badOff)
	f.Close()

	s2, err := Open(path, Options{Capacity: testCapacity, MaxRecord: testMaxRecord})
	if err != nil {
		t.Fatalf("带坏记录重开: %v", err)
	}
	defer s2.Close()
	if got := s2.Len(); got != 2 {
		t.Fatalf("恢复后 Len = %d, want 2", got)
	}

	// 关键：还能写，而且写的能读回。
	if _, err := s2.Append([]byte("new-after-recovery")); err != nil {
		t.Fatalf("恢复后写入失败（坏记录不得阻塞后续）: %v", err)
	}
	recs, err := s2.Peek(10)
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("恢复后应有 2+1=3 条, got %d", len(recs))
	}
	if string(recs[2].Payload) != "new-after-recovery" {
		t.Fatalf("新写入的末条 = %q", recs[2].Payload)
	}
	// 新记录必须接着旧序号（不能重头分配）。
	if recs[2].Seq <= recs[1].Seq {
		t.Fatalf("新记录 seq=%d 应当大于前一条 %d", recs[2].Seq, recs[1].Seq)
	}
}

// TestSequenceContinuesAfterReopen 重开后新写入的序号必须接着旧的，不能重头。
//
// 否则靠 event_id 去重时会出现「两条不同数据共用同一 seq」的混淆面。
func TestSequenceContinuesAfterReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spool.bin")

	s, _ := Open(path, Options{Capacity: testCapacity, MaxRecord: testMaxRecord})
	for i := 0; i < 5; i++ {
		s.Append([]byte(fmt.Sprintf("rec-%d", i)))
	}
	s.Close()

	s2, err := Open(path, Options{Capacity: testCapacity, MaxRecord: testMaxRecord})
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	defer s2.Close()

	seq, err := s2.Append([]byte("after"))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if seq != 6 {
		t.Fatalf("重开后首条 seq = %d, want 6（必须接着旧的）", seq)
	}
	_ = sha
}

var sha = fmt.Sprintf("%x", crc32.Checksum([]byte("x"), castagnoli))
