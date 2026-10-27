package eventstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 本文件是**机械盘 IO 收敛**的取证入口：它量化「一次追加触发几次耐久 syscall」，
// 而不是只测墙钟（墙钟会被页缓存、RAID 缓存与队列深度污染，无法证明 IO 次数真的变少）。
//
// 为什么按「次」而非「毫秒」立据：
//   - 机械盘（ROTA=1，现场实测 75 IOPS / 4K 随机写 13ms）上，每次 fsync 基本等于一次寻道，
//     所以「耐久调用次数」与「寻道次数」同阶，是可比、可复现的指标；
//   - 反之，同一台机器上换个 RAID 缓存策略，耗时能差一个数量级，结论会漂。

// appendCost 返回 n 次追加（每次一批事件）消耗的各类耐久调用次数。
func appendCost(t *testing.T, s *Store, key string, batches, perBatch int) (segment, manifest, directory int64) {
	t.Helper()
	baseSegment, baseManifest, baseDirectory := SyncCounts()
	for i := 0; i < batches; i++ {
		events := make([]logtypes.Event, 0, perBatch)
		for j := 0; j < perBatch; j++ {
			events = append(events, ev(i*perBatch+j))
		}
		require.NoError(t, s.Append(key, events))
	}
	segment, manifest, directory = SyncCounts()
	return segment - baseSegment, manifest - baseManifest, directory - baseDirectory
}

// TestManifestSyncPolicyReducesDurableCalls 逐档比对「每次追加都落清单」（旧行为）
// 与「只在段集合变化时落清单」（机械盘档）的耐久调用次数，并确认
// **段数据 fsync 一次不少**（事件体耐久是崩溃一致性的底线，任何策略都不得削减）。
func TestManifestSyncPolicyReducesDurableCalls(t *testing.T) {
	const batches = 20
	for _, tc := range []struct {
		name     string
		every    int
		interval time.Duration
	}{
		{name: "每次追加都落清单（旧行为）", every: 1},
		{name: "每 8 次追加落一次清单", every: 8},
		{name: "只在段集合变化时落清单（机械盘档）", every: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Open(t.TempDir())
			require.NoError(t, err)
			defer func() { require.NoError(t, s.Close()) }()
			s.SetManifestSyncPolicy(tc.every, tc.interval)
			// 段足够大 ⇒ 本用例内不发生封段/新建段，把「段集合变化」这一必落盘因素排除掉，
			// 否则测到的是封段成本而不是策略差别。
			s.SetMaxSegmentBytes(1 << 30)
			segment, manifest, directory := appendCost(t, s, "inst:1/g1", batches, 10)
			require.EqualValues(t, batches, segment, "段数据 fsync 必须**每次追加各一次**，任何策略都不得削减")
			t.Logf("策略=%s 追加=%d 段fsync=%d 清单fsync=%d 目录fsync=%d 合计耐久调用=%d 每条事件摊薄=%.3f",
				tc.name, batches, segment, manifest, directory, segment+manifest+directory,
				float64(segment+manifest+directory)/float64(batches*10))
		})
	}
}

// TestDeferredManifestRecoversExactMetadataOnReopen 证明「清单滞后」是可精确校准修复的：
// 把清单回退到只记了前 3 条的状态（模拟崩溃时最后一次清单落盘丢失），重开后
// 必须按段内容逐字段重算出**完整**的 10 条，且不多不少。
func TestDeferredManifestRecoversExactMetadataOnReopen(t *testing.T) {
	root := t.TempDir()
	key := "inst:1/g1"
	s, err := Open(root)
	require.NoError(t, err)
	s.SetMaxSegmentBytes(1 << 30)
	require.NoError(t, s.Append(key, []logtypes.Event{ev(0), ev(1), ev(2)}))
	require.NoError(t, s.Close())

	// 抓一份「滞后清单」：此刻它是精确的，把它另存为快照，再追加 7 条（模拟清单未落盘），
	// 然后把快照写回 —— 等价于「数据已耐久、清单停留在旧版本」的崩溃现场。
	dir := filepath.Join(root, sectionDirName(key))
	stale, err := os.ReadFile(filepath.Join(dir, manifestName))
	require.NoError(t, err)

	reopened, err := Open(root)
	require.NoError(t, err)
	reopened.SetMaxSegmentBytes(1 << 30)
	var tail []logtypes.Event
	for i := 3; i < 10; i++ {
		tail = append(tail, ev(i))
	}
	require.NoError(t, reopened.Append(key, tail))
	require.NoError(t, reopened.Close())

	require.NoError(t, os.WriteFile(filepath.Join(dir, manifestName), stale, 0o600))

	for round := 0; round < 3; round++ {
		recovered, err := Open(root)
		require.NoError(t, err)
		count, err := recovered.Count(key)
		require.NoError(t, err)
		require.Equal(t, 10, count, "清单滞后必须由校准精确补全（第 %d 轮）", round+1)
		segments := recovered.Segments(key)
		require.Len(t, segments, 1)
		require.Equal(t, 10, segments[0].Count)
		require.Equal(t, ev(0).EventID, segments[0].FirstID)
		require.Equal(t, ev(9).EventID, segments[0].LastID)
		require.EqualValues(t, ev(9).Record.End, segments[0].MaxEnd)
		require.EqualValues(t, ev(0).Record.End, segments[0].MinEnd)
		got := collect(t, recovered, key)
		require.Len(t, got, 10, "事件体一个都不能丢")
		require.NoError(t, recovered.Close())
	}
}

// TestDeferredManifestStillPersistsSegmentSetImmediately 证明本策略的**硬边界**：
// 段集合变化（新建段 / 封段）永不滞后 —— 否则重开会撞上 unlisted durable segment（硬失败）。
func TestDeferredManifestStillPersistsSegmentSetImmediately(t *testing.T) {
	root := t.TempDir()
	key := "inst:1/g1"
	s, err := Open(root)
	require.NoError(t, err)
	// 极小的段 ⇒ 每次追加都新建段/封段，正是「段集合变化」路径。
	s.SetMaxSegmentBytes(1)
	s.SetManifestSyncPolicy(0, 0)
	for i := 0; i < 5; i++ {
		require.NoError(t, s.Append(key, []logtypes.Event{ev(i)}))
	}
	require.NoError(t, s.Close())

	dir := filepath.Join(root, sectionDirName(key))
	body, err := os.ReadFile(filepath.Join(dir, manifestName))
	require.NoError(t, err)
	var persisted Manifest
	require.NoError(t, unmarshalManifest(body, &persisted))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var physical []string
	for _, e := range entries {
		if isSegmentName(e.Name()) {
			physical = append(physical, e.Name())
		}
	}
	require.Len(t, persisted.Segments, len(physical),
		"清单列出的段必须与物理段一一对应（否则重开会硬失败）")
	reopened, err := Open(root)
	require.NoError(t, err)
	count, err := reopened.Count(key)
	require.NoError(t, err)
	require.Equal(t, 5, count)
	require.NoError(t, reopened.Close())
}

// unmarshalManifest 只在本文件的断言里用，避免把 json 依赖扩散到生产代码。
func unmarshalManifest(body []byte, out *Manifest) error {
	return json.Unmarshal(body, out)
}

// TestDeferredManifestStillVisibleToInProcessReaders 证明「落盘滞后」绝不等于「读侧滞后」：
// 未封末段的派生元数据必须立刻对快照/计数可见，否则采集侧重算差集时会以为事件还不存在，
// 于是重复追加 —— 那会把「省一次 fsync」变成「多写一份数据」，方向完全相反。
func TestDeferredManifestStillVisibleToInProcessReaders(t *testing.T) {
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()
	s.SetMaxSegmentBytes(1 << 30)
	key := "inst:1/g1"
	require.NoError(t, s.Append(key, []logtypes.Event{ev(0)}))
	for i := 1; i < 6; i++ {
		require.NoError(t, s.Append(key, []logtypes.Event{ev(i)}), "滞后策略下追加不得报错")
		count, err := s.Count(key)
		require.NoError(t, err)
		require.Equal(t, i+1, count, "第 %d 次追加后读侧必须立刻看到新事件", i)
		segments := s.Segments(key)
		require.Equal(t, i+1, segments[0].Count)
		require.Equal(t, ev(i).EventID, segments[0].LastID)
	}
	require.Len(t, collect(t, s, key), 6)
}

// BenchmarkAppendManifestPolicies 在**当前机器**上实测两种清单策略的单次追加成本。
//
// 为什么用 Benchmark 而不是普通 Test：耗时断言在 CI 上必然不稳定（换盘、换 RAID 缓存策略
// 都能差一个数量级，见文件头的说明），所以这里只提供测量入口，由 `go test -bench` 显式触发，
// 不参与常规 `go test`。判据仍然是上一用例的**耐久调用次数**，耗时只作为现场佐证。
func BenchmarkAppendManifestPolicies(b *testing.B) {
	for _, tc := range []struct {
		name  string
		every int
	}{
		{name: "old_every_append", every: 1},
		{name: "mechanical_segment_set_only", every: 0},
	} {
		b.Run(tc.name, func(b *testing.B) {
			store, err := Open(b.TempDir())
			if err != nil {
				b.Fatalf("打开失败: %v", err)
			}
			defer func() { _ = store.Close() }()
			store.SetManifestSyncPolicy(tc.every, 0)
			store.SetMaxSegmentBytes(1 << 30)
			events := make([]logtypes.Event, 0, 10)
			for i := 0; i < 10; i++ {
				events = append(events, ev(i))
			}
			baseSegment, baseManifest, baseDirectory := SyncCounts()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := store.Append("inst:1/g1", events); err != nil {
					b.Fatalf("追加失败: %v", err)
				}
			}
			b.StopTimer()
			segment, manifest, directory := SyncCounts()
			segment, manifest, directory = segment-baseSegment, manifest-baseManifest, directory-baseDirectory
			b.ReportMetric(float64(segment+manifest+directory)/float64(b.N), "fsync/op")
			b.ReportMetric(float64(segment)/float64(b.N), "segfsync/op")
			b.ReportMetric(float64(manifest+directory)/float64(b.N), "manfsync/op")
		})
	}
}
