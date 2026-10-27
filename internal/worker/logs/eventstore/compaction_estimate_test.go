package eventstore

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// diskEvidence 记录一个文件的完整取证指纹：大小、mtime、权限、内容摘要。
// mtime 必须参与比较——只读要求是「不碰文件」，光比内容会漏掉原地重写同内容这类改动。
type diskEvidence struct {
	size  int64
	mode  os.FileMode
	mtime int64
	hash  [sha256.Size]byte
}

func captureDiskEvidence(t *testing.T, root string) map[string]diskEvidence {
	t.Helper()
	out := make(map[string]diskEvidence)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if entry.IsDir() {
			out[entry.Name()+"/"] = diskEvidence{mode: entry.Type()}
			files, err := os.ReadDir(path)
			require.NoError(t, err)
			for _, file := range files {
				info, err := os.Stat(filepath.Join(path, file.Name()))
				require.NoError(t, err)
				body, err := os.ReadFile(filepath.Join(path, file.Name()))
				require.NoError(t, err)
				out[filepath.Join(entry.Name(), file.Name())] = diskEvidence{
					size:  info.Size(),
					mode:  info.Mode(),
					mtime: info.ModTime().UnixNano(),
					hash:  sha256.Sum256(body),
				}
			}
			continue
		}
		info, err := os.Stat(path)
		require.NoError(t, err)
		body, err := os.ReadFile(path)
		require.NoError(t, err)
		out[entry.Name()] = diskEvidence{
			size:  info.Size(),
			mode:  info.Mode(),
			mtime: info.ModTime().UnixNano(),
			hash:  sha256.Sum256(body),
		}
	}
	return out
}

// aggregateSegments 是测试侧对清单的独立求和，用来与 CompactionEstimate 对账。
func aggregateSegments(segments []SegmentMeta) SourceCompactionEstimate {
	per := SourceCompactionEstimate{}
	for _, meta := range segments {
		per.Segments++
		per.Bytes += meta.Bytes
		per.Events += int64(meta.Count)
		if meta.Sealed {
			per.SealedSegments++
			per.SealedBytes += meta.Bytes
		}
	}
	per.ActiveSegments = per.Segments - per.SealedSegments
	return per
}

// 空 store：返回零值且不 panic，估算字段全为 0。
func TestCompactionEstimateEmptyStoreIsZero(t *testing.T) {
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	est, err := s.CompactionEstimate()
	require.NoError(t, err)
	require.Equal(t, CompactionEstimate{}, est, "空库必须返回零值")
	require.Zero(t, est.SampleCount, "无抽样母体时样本数必须为 0")
	require.Zero(t, est.EstimatedGzipBytes)
	require.Zero(t, est.EstimatedSavedBytes)
}

// 多源多段：逐项断言计数与字节数，并与 Segments() 的独立求和交叉对账。
func TestCompactionEstimateCountsAcrossSources(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.SetMaxSegmentBytes(1024) // 极小段，强制滚动出多个已封印段

	keys := []string{"inst:1/g1", "inst:2/g1"}
	for i, key := range keys {
		for n := 0; n < 40; n++ {
			require.NoError(t, s.Append(key, []logtypes.Event{ev(i*100 + n)}))
		}
	}

	est, err := s.CompactionEstimate()
	require.NoError(t, err)

	require.Equal(t, len(keys), est.Sources)
	require.Len(t, est.PerSource, len(keys), "每个源都应有一条明细")
	require.Equal(t, keys[0], est.PerSource[0].Key, "明细必须按 key 升序")
	require.Equal(t, keys[1], est.PerSource[1].Key)

	var want CompactionEstimate
	for i, key := range keys {
		segments := s.Segments(key)
		require.NotEmpty(t, segments)
		expected := aggregateSegments(segments)
		expected.Key = key
		require.Equal(t, expected, est.PerSource[i], "逐源明细必须与清单一致")

		want.Sources++
		want.Segments += expected.Segments
		want.SealedSegments += expected.SealedSegments
		want.ActiveSegments += expected.ActiveSegments
		want.TotalBytes += expected.Bytes
		want.SealedBytes += expected.SealedBytes
		want.TotalEvents += expected.Events
	}

	require.Equal(t, want.Sources, est.Sources)
	require.Equal(t, want.Segments, est.Segments)
	require.Equal(t, want.SealedSegments, est.SealedSegments)
	require.Equal(t, want.ActiveSegments, est.ActiveSegments)
	require.Equal(t, want.TotalBytes, est.TotalBytes)
	require.Equal(t, want.SealedBytes, est.SealedBytes)
	require.Equal(t, want.TotalEvents, est.TotalEvents)

	// 段滚动后必须同时存在两类段，否则上面的 sealed/active 断言没有区分力。
	require.Greater(t, est.SealedSegments, 0, "应存在已封印段")
	require.Greater(t, est.ActiveSegments, 0, "每个源最后一段未封印")
	require.Equal(t, est.Segments, est.SealedSegments+est.ActiveSegments)
	require.Equal(t, int64(len(keys)*40), est.TotalEvents, "事件总数必须与写入量一致")
	require.Less(t, est.SealedBytes, est.TotalBytes+1, "已封印字节不得超过总字节")
}

// 只读证明：调用前后段文件与清单的内容、大小、权限、mtime 完全不变。
func TestCompactionEstimateLeavesDiskUntouched(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.SetMaxSegmentBytes(1024)
	for i := 0; i < 30; i++ {
		require.NoError(t, s.Append("inst:1/g1", []logtypes.Event{ev(i)}))
	}
	require.NoError(t, s.Append("inst:2/g1", []logtypes.Event{ev(1)}))

	before := captureDiskEvidence(t, dir)
	require.NotEmpty(t, before)

	first, err := s.CompactionEstimate()
	require.NoError(t, err)
	second, err := s.CompactionEstimate()
	require.NoError(t, err)
	require.Equal(t, first, second, "同一静止状态下的两次评估必须完全一致")

	after := captureDiskEvidence(t, dir)
	require.Equal(t, before, after, "评估不得改动任何段文件或清单（含 mtime）")

	// 抽样确实打开过已封印段，否则「磁盘未变」是因为根本没读。
	require.Greater(t, first.SampleCount, 0, "应实际抽样已封印段")
	require.Greater(t, first.SampleBytes, int64(0))
}

// 估算路径：有已封印段时给出非零抽样与估算，且比值、收益都落在合理区间。
func TestCompactionEstimateSamplingProducesNonZeroEstimate(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// 用默认段大小写入足够多的重复结构事件，再强制封段，制造一个真实可压的已封印段。
	events := make([]logtypes.Event, 0, 200)
	for i := 0; i < 200; i++ {
		events = append(events, repeatingEvent(i))
	}
	require.NoError(t, s.Append("inst:1/g1", events))
	s.SetMaxSegmentBytes(1) // 下一次追加即触发封段
	require.NoError(t, s.Append("inst:1/g1", []logtypes.Event{repeatingEvent(200)}))
	// 恢复默认段大小后再追加：新建的段保持未封印，用来验证 sealed/active 的区分。
	s.SetMaxSegmentBytes(DefaultSegmentBytes)
	require.NoError(t, s.Append("inst:1/g1", []logtypes.Event{repeatingEvent(201)}))

	est, err := s.CompactionEstimate()
	require.NoError(t, err)

	require.Equal(t, 1, est.Sources)
	require.Equal(t, 2, est.Segments)
	require.Equal(t, 1, est.SealedSegments)
	require.Equal(t, 1, est.ActiveSegments)
	require.Equal(t, 1, est.SampleCount, "只有 1 个已封印段时样本数为 1")
	require.Greater(t, est.SampleBytes, int64(0))
	require.Greater(t, est.SampleGzipBytes, int64(0))
	require.Greater(t, est.SampleRatio, float64(0))
	require.LessOrEqual(t, est.SampleRatio, float64(1), "压缩比封顶 1.0")
	require.Greater(t, est.EstimatedGzipBytes, int64(0), "有样本时估算必须非零")
	require.Greater(t, est.SampleBytes, est.SampleGzipBytes, "重复结构的事件体应可压")
	require.Greater(t, est.EstimatedSavedBytes, int64(0), "可压数据应给出正收益估算")
	require.Equal(t, est.SealedBytes, est.EstimatedGzipBytes+est.EstimatedSavedBytes,
		"估算的两部分必须正好覆盖已封印字节")

	t.Logf("抽样估算：源=%d 段=%d 已封印段=%d 样本=%d 样本原始=%d 样本压缩后=%d 比值=%.4f 已封印字节=%d 估算压后=%d 估算可省=%d",
		est.Sources, est.Segments, est.SealedSegments, est.SampleCount,
		est.SampleBytes, est.SampleGzipBytes, est.SampleRatio,
		est.SealedBytes, est.EstimatedGzipBytes, est.EstimatedSavedBytes)
}

// 只读取证实例上同样可用：它不校准、不写盘，只汇总既有清单。
func TestCompactionEstimateWorksOnReadOnlyStore(t *testing.T) {
	dir := t.TempDir()
	writer, err := Open(dir)
	require.NoError(t, err)
	writer.SetMaxSegmentBytes(512)
	for i := 0; i < 20; i++ {
		require.NoError(t, writer.Append("inst:1/g1", []logtypes.Event{ev(i)}))
	}
	require.NoError(t, writer.Close())

	before := captureDiskEvidence(t, dir)

	ro, err := OpenReadOnly(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ro.Close() })

	est, err := ro.CompactionEstimate()
	require.NoError(t, err)
	require.Equal(t, 1, est.Sources)
	require.Equal(t, int64(20), est.TotalEvents)
	require.Greater(t, est.SealedSegments, 0)
	require.Greater(t, est.SampleCount, 0)

	require.Equal(t, before, captureDiskEvidence(t, dir), "只读实例的评估同样不得改动磁盘")
}

// 抽样上限：段数远超上限时只抽 3 个，且按源轮转，不会全部落在同一个源上。
func TestCompactionEstimateCapsSamplesAndSpreadsAcrossSources(t *testing.T) {
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.SetMaxSegmentBytes(512) // 每个源都会产生远多于 3 个已封印段

	keys := []string{"inst:1/g1", "inst:2/g1"}
	for i, key := range keys {
		for n := 0; n < 20; n++ {
			require.NoError(t, s.Append(key, []logtypes.Event{ev(i*100 + n)}))
		}
	}

	est, err := s.CompactionEstimate()
	require.NoError(t, err)
	require.Greater(t, est.SealedSegments, compactionSampleLimit, "已封印段数应超过抽样上限")
	require.Equal(t, compactionSampleLimit, est.SampleCount, "抽样数必须被上限截断")
	require.LessOrEqual(t, est.SampleBytes, int64(compactionSampleLimit)*compactionSampleByteLimit,
		"单样本读取量必须有上界")

	samples := pickSealedSamples(s.snapshotSourceManifests(), compactionSampleLimit)
	require.Len(t, samples, compactionSampleLimit)
	require.NotEqual(t, samples[0].dir, samples[1].dir, "轮转取样必须跨源，不能都取自同一源")
}

// repeatingEvent 生成结构高度重复的事件体，保证 gzip 有可观测的压缩收益。
func repeatingEvent(i int) logtypes.Event {
	return logtypes.BuildEvent(
		logtypes.SourceIdentity{LogSourceID: "inst:1", SourceGeneration: "g1", ParserVersion: "acquire-v1"},
		logtypes.RecordRange{Start: uint64(i * 10), End: uint64(i*10 + 9)},
		"2026-09-24T00:00:00Z", "2026-09-24T00:00:01Z", "INFO", "stdout",
		fmt.Sprintf("repeating-payload-%04d [Server thread/INFO]: chunk saved for world 'world_nether' region r.0.0.mca", i%8),
	)
}
