package ws

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 分片重组（§2.5）的单测：正常重组、缺帧检出、超时丢弃、上限保护。
// 这些用例直接驱动 chunkAssembler，不经过 WS（协议层用例见 chunk_frame_test.go）。

// mkChunk 构造一片分片信封。
func mkChunk(groupID string, seq, total int, data string) chunkRequest {
	return chunkRequest{GroupID: groupID, Seq: seq, Total: total, OfType: "command_result", Data: data}
}

// splitBase64 按每片 charsPerChunk 个 base64 字符切分（模拟探针 FrameChunker 的切法：
// 4 的整数倍，每片可独立解码）。
func splitBase64(t *testing.T, raw []byte, charsPerChunk int) []string {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString(raw)
	require.Equal(t, 0, len(encoded)%4, "测试构造的片长必须是 4 的整数倍")
	var out []string
	for from := 0; from < len(encoded); from += charsPerChunk {
		to := from + charsPerChunk
		if to > len(encoded) {
			to = len(encoded)
		}
		out = append(out, encoded[from:to])
	}
	return out
}

// TestChunkAssembler_Reassemble 正常重组：整组到齐后拼出的字节与切分前完全一致。
func TestChunkAssembler_Reassemble(t *testing.T) {
	a := newChunkAssembler()
	original := []byte(`{"type":"event","event":"command_result","data":{"requestId":"r1","success":true,"output":"hello world"}}`)
	parts := splitBase64(t, original, 16)
	require.Greater(t, len(parts), 1, "用例前提：帧被切成多片")

	var assembled []byte
	for i, p := range parts {
		raw, err := a.accept("inst-1", mkChunk("g1", i, len(parts), p))
		require.NoError(t, err)
		if raw != nil {
			assembled = raw
		}
	}
	require.NotNil(t, assembled)
	assert.Equal(t, string(original), string(assembled), "重组结果必须与切分前逐字节相同")
	completed, incomplete, rejected := a.stats()
	assert.Equal(t, int64(1), completed)
	assert.Equal(t, int64(0), incomplete)
	assert.Equal(t, int64(0), rejected)
	assert.Equal(t, 0, a.pending("inst-1"), "完成后组占位应归还")
}

// TestChunkAssembler_OutOfOrder 乱序到达也能正确重组（按 seq 拼，不按到达顺序）。
func TestChunkAssembler_OutOfOrder(t *testing.T) {
	a := newChunkAssembler()
	original := []byte(strings.Repeat("abcdefgh", 20))
	parts := splitBase64(t, original, 32)

	var assembled []byte
	// 逆序投递。
	for i := len(parts) - 1; i >= 0; i-- {
		raw, err := a.accept("inst-1", mkChunk("g-ord", i, len(parts), parts[i]))
		require.NoError(t, err)
		if raw != nil {
			assembled = raw
		}
	}
	assert.Equal(t, string(original), string(assembled))
}

// TestChunkAssembler_MissingFrame 缺帧：不完整组不返回任何数据，不会被误判为到齐。
func TestChunkAssembler_MissingFrame(t *testing.T) {
	a := newChunkAssembler()
	original := []byte(strings.Repeat("0123456789", 30))
	parts := splitBase64(t, original, 16)
	require.GreaterOrEqual(t, len(parts), 3)

	// 故意跳过 seq=1。
	for i, p := range parts {
		if i == 1 {
			continue
		}
		raw, err := a.accept("inst-1", mkChunk("g-miss", i, len(parts), p))
		require.NoError(t, err)
		assert.Nil(t, raw, "缺帧时不得返回半成品")
	}
	assert.Equal(t, 1, a.pending("inst-1"), "缺帧的组仍在途，等超时回收")
	completed, incomplete, _ := a.stats()
	assert.Equal(t, int64(0), completed)
	assert.Equal(t, int64(0), incomplete, "尚未超时，不该计 INCOMPLETE_CHUNK")
}

// TestChunkAssembler_TimeoutDropsGroup 超时：不完整组被丢弃并计 INCOMPLETE_CHUNK，
// 组占位归还，后续同 groupId 的片能重新起一组。
func TestChunkAssembler_TimeoutDropsGroup(t *testing.T) {
	a := newChunkAssembler()
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	original := []byte(strings.Repeat("x", 100))
	parts := splitBase64(t, original, 16)
	require.GreaterOrEqual(t, len(parts), 2)

	// 只发第一片。
	raw, err := a.accept("inst-1", mkChunk("g-timeout", 0, len(parts), parts[0]))
	require.NoError(t, err)
	require.Nil(t, raw)
	require.Equal(t, 1, a.pending("inst-1"))

	// 跨过 30s 重组超时。
	now = now.Add(ChunkAssembleTimeout + time.Second)
	drops := a.sweepNow()
	require.Len(t, drops, 1, "超时组应被清扫出来")
	assert.Equal(t, "g-timeout", drops[0].GroupID)
	assert.Equal(t, 1, drops[0].Received)
	assert.Equal(t, len(parts), drops[0].Total)
	assert.Equal(t, "INCOMPLETE_CHUNK", drops[0].Reason)
	assert.Equal(t, 0, a.pending("inst-1"), "超时后占位必须归还")

	_, incomplete, _ := a.stats()
	assert.Equal(t, int64(1), incomplete)
}

// TestChunkAssembler_ParallelGroupsCapped 单实例并行重组数上限 8：第 9 组整组拒收。
func TestChunkAssembler_ParallelGroupsCapped(t *testing.T) {
	a := newChunkAssembler()
	for i := 0; i < ChunkMaxParallelPerInstance; i++ {
		_, err := a.accept("inst-1", mkChunk("g"+string(rune('a'+i)), 0, 4, "AAAA"))
		require.NoError(t, err, "第 %d 组应被接受", i+1)
	}
	require.Equal(t, ChunkMaxParallelPerInstance, a.pending("inst-1"))

	_, err := a.accept("inst-1", mkChunk("g-over", 0, 4, "AAAA"))
	require.ErrorIs(t, err, ErrChunkTooManyGroups, "第 9 组应被整组拒收")
	assert.Equal(t, ChunkMaxParallelPerInstance, a.pending("inst-1"), "拒收不得占位")

	// 不同实例互不影响。
	_, err = a.accept("inst-2", mkChunk("g-a", 0, 4, "AAAA"))
	require.NoError(t, err, "并行上限是按实例计的")
}

// TestChunkAssembler_BadEnvelope 非法信封（seq 越界 / total 非正 / 片数超限）直接拒收。
func TestChunkAssembler_BadEnvelope(t *testing.T) {
	a := newChunkAssembler()
	cases := []struct {
		name string
		req  chunkRequest
		want error
	}{
		{"total 非正", chunkRequest{GroupID: "g", Seq: 0, Total: 0, Data: "AAAA"}, ErrChunkBadTotal},
		{"seq 越界", chunkRequest{GroupID: "g", Seq: 5, Total: 2, Data: "AAAA"}, ErrChunkSeqOutOfRange},
		{"seq 负值", chunkRequest{GroupID: "g", Seq: -1, Total: 2, Data: "AAAA"}, ErrChunkSeqOutOfRange},
		{"片数超限", chunkRequest{GroupID: "g", Seq: 0, Total: chunkMaxChunksPerGroup + 1, Data: "AAAA"}, ErrChunkTooManyChunks},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := a.accept("inst-1", tc.req)
			require.ErrorIs(t, err, tc.want)
		})
	}
	_, _, rejected := a.stats()
	assert.Equal(t, int64(len(cases)), rejected)
}

// TestChunkAssembler_BadBase64 片数据非法 base64：整组丢弃并计拒收，不返回半成品。
func TestChunkAssembler_BadBase64(t *testing.T) {
	a := newChunkAssembler()
	_, err := a.accept("inst-1", mkChunk("g-bad", 0, 2, "AAAA"))
	require.NoError(t, err)
	_, err = a.accept("inst-1", mkChunk("g-bad", 1, 2, "@@@@")) // @ 不是 base64 字母
	require.ErrorIs(t, err, ErrChunkBadBase64)
	assert.Equal(t, 0, a.pending("inst-1"), "坏组必须立即回收")
}

// TestChunkAssembler_TotalMismatch 同组 total 变化（groupId 被复用/对端 bug）：整组作废。
func TestChunkAssembler_TotalMismatch(t *testing.T) {
	a := newChunkAssembler()
	_, err := a.accept("inst-1", mkChunk("g-dup", 0, 3, "AAAA"))
	require.NoError(t, err)

	_, err = a.accept("inst-1", mkChunk("g-dup", 1, 5, "AAAA"))
	require.ErrorIs(t, err, ErrChunkBadTotal)
	assert.Equal(t, 0, a.pending("inst-1"), "作废后不占位")
}

// TestChunkAssembler_DuplicateSeq 重复片覆盖数据但不重复计数（不会虚高到"到齐"）。
func TestChunkAssembler_DuplicateSeq(t *testing.T) {
	a := newChunkAssembler()
	original := []byte(strings.Repeat("dup", 40))
	parts := splitBase64(t, original, 24)
	require.GreaterOrEqual(t, len(parts), 2)

	raw, err := a.accept("inst-1", mkChunk("g-dup2", 0, len(parts), parts[0]))
	require.NoError(t, err)
	require.Nil(t, raw)
	// 重复投 seq=0 三次：recv 仍为 1，不应触发完成。
	for i := 0; i < 3; i++ {
		raw, err = a.accept("inst-1", mkChunk("g-dup2", 0, len(parts), parts[0]))
		require.NoError(t, err)
		require.Nil(t, raw, "重复片不得推高完成判定")
	}
	var assembled []byte
	for i := 1; i < len(parts); i++ {
		raw, err = a.accept("inst-1", mkChunk("g-dup2", i, len(parts), parts[i]))
		require.NoError(t, err)
		if raw != nil {
			assembled = raw
		}
	}
	assert.Equal(t, string(original), string(assembled))
}

// TestChunkAssembler_DropInstance 会话结束时清掉该实例的全部在途组。
func TestChunkAssembler_DropInstance(t *testing.T) {
	a := newChunkAssembler()
	_, err := a.accept("inst-1", mkChunk("g-a", 0, 3, "AAAA"))
	require.NoError(t, err)
	_, err = a.accept("inst-1", mkChunk("g-b", 0, 3, "AAAA"))
	require.NoError(t, err)
	_, err = a.accept("inst-2", mkChunk("g-c", 0, 3, "AAAA"))
	require.NoError(t, err)

	drops := a.dropInstance("inst-1")
	assert.Len(t, drops, 2)
	assert.Equal(t, 0, a.pending("inst-1"))
	assert.Equal(t, 1, a.pending("inst-2"), "其他实例不受影响")
}
