package eventstore

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

const (
	// compactionSampleLimit 抽样实测的段数上限。
	// 抽 3 段足够覆盖「多源」与「多段」两种形态，同时把评估的读盘量压在几十 MiB 以内，
	// 让它可以作为一次同步的只读查询完成。
	compactionSampleLimit = 3

	// compactionSampleByteLimit 单个样本最多参与压缩的字节数；超出则只压前缀。
	// 正常配置下段大小即 DefaultSegmentBytes（8MiB），因此常规情况整段参与抽样。
	compactionSampleByteLimit int64 = DefaultSegmentBytes
)

// CompactionEstimate 是全库段体积的**只读评估**结果。
//
// 它只回答「若把这些段 gzip 一遍大约能省多少」，**不回答**「去省」：本包不提供任何压缩、改写、
// 替换或删除段的路径，本方法也不向磁盘写入任何字节。段只增不减是契约硬约束（见 snapshot.go
// 中「本包只追加而不回收或替换既有段」以及 runtime.go 中「段只增不减且没有裁剪路径」）。
//
// 语义边界：
//   - 计数与字节数取自各源**最近成功提交的清单**，不触发首次校准（校准可能会截断崩溃残留的
//     半行，属于写盘）。因此崩溃后未封印段的清单可能略大于物理完整行，下一次正常
//     Append/读取的校准会自行修正。
//   - Estimated* 是**基于抽样的估算**，不是精确值，也不构成可达收益的承诺。
type CompactionEstimate struct {
	// Sources 是源目录数（每个 source key 对应一个目录）。
	Sources int `json:"sources"`
	// Segments / SealedSegments / ActiveSegments 是段总数、已封印段数与未封印段数。
	Segments       int `json:"segments"`
	SealedSegments int `json:"sealed_segments"`
	ActiveSegments int `json:"active_segments"`
	// TotalBytes / SealedBytes 是段字节合计（清单口径）。
	TotalBytes  int64 `json:"total_bytes"`
	SealedBytes int64 `json:"sealed_bytes"`
	// TotalEvents 是各段 Count 合计。
	TotalEvents int64 `json:"total_events"`

	// SampleCount 是实际完成压缩的样本段数。为 0 表示没有抽样依据（无已封印段或抽样全部失败），
	// 此时下面四个样本字段与两个 Estimate 字段都是零值。
	SampleCount int `json:"sample_count"`
	// SampleBytes / SampleGzipBytes 是样本的原始字节与压缩后字节合计；SampleRatio 是二者之比
	// （已封顶 1.0）。
	SampleBytes     int64   `json:"sample_bytes"`
	SampleGzipBytes int64   `json:"sample_gzip_bytes"`
	SampleRatio     float64 `json:"sample_ratio"`

	// EstimatedGzipBytes / EstimatedSavedBytes 是**抽样外推**结果，只针对已封印段：
	// 抽样母体就是已封印段，未封印段不在估算范围内（其字节仍计入 TotalBytes）。
	EstimatedGzipBytes  int64 `json:"estimated_gzip_bytes"`
	EstimatedSavedBytes int64 `json:"estimated_saved_bytes"`

	// PerSource 是逐源明细，按 source key 升序，顺序稳定便于比对。
	PerSource []SourceCompactionEstimate `json:"per_source"`
}

// SourceCompactionEstimate 是单个源的段体积明细（清单口径，不含估算）。
type SourceCompactionEstimate struct {
	Key            string `json:"key"`
	Segments       int    `json:"segments"`
	SealedSegments int    `json:"sealed_segments"`
	ActiveSegments int    `json:"active_segments"`
	Bytes          int64  `json:"bytes"`
	SealedBytes    int64  `json:"sealed_bytes"`
	Events         int64  `json:"events"`
}

// sourceManifestSnapshot 是一个源在某一时刻的清单副本与目录位置。
// 只在锁内复制，随后所有统计与抽样都在锁外进行。
type sourceManifestSnapshot struct {
	key      string
	dir      string
	segments []SegmentMeta
}

// CompactionEstimate 汇总全库（所有 source 的所有段）的段体积，并基于已封印段抽样估算 gzip 收益。
//
// 并发：先在 Store.mu 内复制源指针集合，再在源视图锁内复制清单，锁外完成求和与抽样读盘；
// 持锁期间不做文件 IO，也不做长计算。
//
// 只读：本方法只用 os.Open 读取段文件，绝不写入、截断、重命名或删除任何文件；它也不触发首次
// 校准，因此调用前后磁盘内容与 mtime 保持不变。
//
// 错误：已封印段按契约永不变更，读不出它属于介质或权限异常，不能静默折算成「零收益」。
// 此时仍返回已经聚合好的元数据部分，并通过 error 报告抽样失败原因。
func (s *Store) CompactionEstimate() (CompactionEstimate, error) {
	sources := s.snapshotSourceManifests()

	var est CompactionEstimate
	if len(sources) > 0 {
		est.PerSource = make([]SourceCompactionEstimate, 0, len(sources))
	}
	for _, src := range sources {
		per := SourceCompactionEstimate{Key: src.key}
		for _, meta := range src.segments {
			per.Segments++
			per.Bytes += meta.Bytes
			per.Events += int64(meta.Count)
			if meta.Sealed {
				per.SealedSegments++
				per.SealedBytes += meta.Bytes
			}
		}
		per.ActiveSegments = per.Segments - per.SealedSegments

		est.Sources++
		est.Segments += per.Segments
		est.SealedSegments += per.SealedSegments
		est.ActiveSegments += per.ActiveSegments
		est.TotalBytes += per.Bytes
		est.SealedBytes += per.SealedBytes
		est.TotalEvents += per.Events
		est.PerSource = append(est.PerSource, per)
	}

	if est.SealedSegments == 0 {
		// 没有已封印段就没有抽样母体：跳过估算，估算字段保持零值。
		return est, nil
	}
	return est, estimateSealedGzip(sources, &est)
}

// snapshotSourceManifests 在短锁内复制源集合与各自的已提交清单，返回按 key 升序的稳定序列。
func (s *Store) snapshotSourceManifests() []sourceManifestSnapshot {
	s.mu.Lock()
	sections := make(map[string]*section, len(s.byKey))
	for key, sec := range s.byKey {
		sections[key] = sec
	}
	s.mu.Unlock()

	out := make([]sourceManifestSnapshot, 0, len(sections))
	for key, sec := range sections {
		sec.viewMu.RLock()
		segments := append([]SegmentMeta(nil), sec.committed.Segments...)
		dir := sec.dir
		sec.viewMu.RUnlock()
		out = append(out, sourceManifestSnapshot{key: key, dir: dir, segments: segments})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// segmentSample 是一个待抽样的已封印段。
type segmentSample struct {
	dir  string
	meta SegmentMeta
}

// pickSealedSamples 按源轮转挑选最多 limit 个已封印段，避免样本全部落在段数最多的单个源上。
// 输入已按 key 排序、段已按 Seq 升序，因此挑选结果可重复。
func pickSealedSamples(sources []sourceManifestSnapshot, limit int) []segmentSample {
	if limit <= 0 {
		return nil
	}
	candidates := make([][]SegmentMeta, len(sources))
	longest := 0
	for i, src := range sources {
		for _, meta := range src.segments {
			if meta.Sealed {
				candidates[i] = append(candidates[i], meta)
			}
		}
		if len(candidates[i]) > longest {
			longest = len(candidates[i])
		}
	}
	samples := make([]segmentSample, 0, limit)
	for round := 0; round < longest && len(samples) < limit; round++ {
		for i := range candidates {
			if round >= len(candidates[i]) {
				continue
			}
			samples = append(samples, segmentSample{dir: sources[i].dir, meta: candidates[i][round]})
			if len(samples) >= limit {
				break
			}
		}
	}
	return samples
}

// estimateSealedGzip 对已封印段抽样实压，并把平均压缩比外推到 SealedBytes。
func estimateSealedGzip(sources []sourceManifestSnapshot, est *CompactionEstimate) error {
	var failures []error
	for _, sample := range pickSealedSamples(sources, compactionSampleLimit) {
		raw, packed, err := gzipSizeOfSegment(sample.dir, sample.meta)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if raw <= 0 {
			continue
		}
		est.SampleCount++
		est.SampleBytes += raw
		est.SampleGzipBytes += packed
	}
	if est.SampleBytes <= 0 {
		if len(failures) == 0 {
			failures = append(failures, fmt.Errorf("eventstore: compaction sample yielded no bytes"))
		}
		return errors.Join(failures...)
	}

	ratio := float64(est.SampleGzipBytes) / float64(est.SampleBytes)
	if ratio > 1 {
		// 不可压数据上 gzip 会略微膨胀；把比值封顶在 1.0，报告「无收益」比报告负收益更有意义。
		ratio = 1
	}
	est.SampleRatio = ratio
	est.EstimatedGzipBytes = int64(float64(est.SealedBytes) * ratio)
	est.EstimatedSavedBytes = est.SealedBytes - est.EstimatedGzipBytes
	return errors.Join(failures...)
}

// countingWriter 只统计写入字节数，不保留任何内容。
type countingWriter struct {
	n int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}

// gzipSizeOfSegment 以只读方式打开段，用默认压缩级别压一遍它的（至多
// compactionSampleByteLimit 字节的）前缀，返回参与压缩的原始字节数与压缩后字节数。
// 全程只用 O_RDONLY 打开，不写入、不截断、不替换该文件。
func gzipSizeOfSegment(dir string, meta SegmentMeta) (raw, packed int64, result error) {
	path := filepath.Join(dir, meta.Name)
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, fmt.Errorf("eventstore: open compaction sample %s: %w", path, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			result = errors.Join(result, closeErr)
		}
	}()

	counter := &countingWriter{}
	writer := gzip.NewWriter(counter) // 默认压缩级别，与任务约定一致
	written, err := io.Copy(writer, io.LimitReader(file, compactionSampleByteLimit))
	if err != nil {
		return 0, 0, errors.Join(fmt.Errorf("eventstore: compress compaction sample %s: %w", path, err), writer.Close())
	}
	if err := writer.Close(); err != nil {
		return 0, 0, fmt.Errorf("eventstore: finish compaction sample %s: %w", path, err)
	}
	return written, counter.n, nil
}
