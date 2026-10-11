package service

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 本文件覆盖「卷积 N+1 队头阻塞」修复的回归面（生产实证 2026-10-09）：
//   - 批量预取已存在桶 vs 逐桶 COUNT 的结果必须逐字段一致（语义等价）；
//   - 分批写入在「部分桶已存在」「中途写入失败」后重跑，必须不漏插、不重复插入；
//   - 大输入下查询次数必须与**序列数**相关、与**桶数**无关（原实现为每桶一条 COUNT）；
//   - purge 分批等价删除，且保留期边界与改动前完全一致。

// seedChunked 分批插入种子数据：GORM 不会自动切分 Create 的参数数量，
// SQLite 单语句绑定参数上限约 3.2 万，大输入一次性 Create 会直接报错。
func seedChunked[T any](t *testing.T, db *gorm.DB, rows []T) {
	t.Helper()
	const chunk = 500
	for i := 0; i < len(rows); i += chunk {
		batch := rows[i:min(i+chunk, len(rows))]
		require.NoError(t, db.Create(&batch).Error)
	}
}

// ---------------------------------------------------------------------------
// 冻结的旧实现（改动前算法的逐行复刻），作为等价性断言的参照物。
//
// 为什么要在测试里复刻一份：等价性的证据必须是「两条独立实现给出相同结果」，
// 而不是「新实现等于我对新实现的期望」。故这里照抄改动前的
// Find 全表 + Order("ts") + 逐桶 COUNT + Create 流程，一字不改地保留其语义
// （包括对 ORDER BY 到达顺序的依赖），再与新实现对拍。
// ---------------------------------------------------------------------------

func legacyRollup5m(db *gorm.DB, now time.Time) error {
	cutoff := bucketStart(now, metricBucket5m)
	var raws []model.MetricSampleRaw
	if err := db.Where("ts < ?", cutoff).Order("ts").Find(&raws).Error; err != nil {
		return err
	}
	accs := map[string]*rawAcc{}
	var order []string
	for _, r := range raws {
		if r.Value == nil {
			continue
		}
		v := *r.Value
		b := bucketStart(r.TS, metricBucket5m)
		key := fmt.Sprintf("%d|%d", r.SeriesID, b.UnixNano())
		a := accs[key]
		if a == nil {
			a = &rawAcc{seriesID: r.SeriesID, bucket: b, min: v, max: v}
			accs[key] = a
			order = append(order, key)
		}
		a.sum += v
		a.count++
		if v < a.min {
			a.min = v
		}
		if v > a.max {
			a.max = v
		}
		a.last = v
	}
	for _, key := range order {
		a := accs[key]
		var exists int64
		if err := db.Model(&model.MetricRollup5m{}).
			Where("series_id = ? AND bucket_ts = ?", a.seriesID, a.bucket).Count(&exists).Error; err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		row := model.MetricRollup5m{
			SeriesID: a.seriesID, BucketTS: a.bucket,
			Avg: a.sum / float64(a.count), Min: a.min, Max: a.max, Last: a.last, Count: a.count,
		}
		if err := db.Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func legacyRollup1h(db *gorm.DB, now time.Time) error {
	cutoff := bucketStart(now, metricBucket1h)
	var fives []model.MetricRollup5m
	if err := db.Where("bucket_ts < ?", cutoff).Order("bucket_ts").Find(&fives).Error; err != nil {
		return err
	}
	accs := map[string]*fiveAcc{}
	var order []string
	for _, f := range fives {
		b := bucketStart(f.BucketTS, metricBucket1h)
		key := fmt.Sprintf("%d|%d", f.SeriesID, b.UnixNano())
		a := accs[key]
		if a == nil {
			a = &fiveAcc{seriesID: f.SeriesID, bucket: b, min: f.Min, max: f.Max}
			accs[key] = a
			order = append(order, key)
		}
		a.weightedSum += f.Avg * float64(f.Count)
		a.count += f.Count
		if f.Min < a.min {
			a.min = f.Min
		}
		if f.Max > a.max {
			a.max = f.Max
		}
		a.last = f.Last
	}
	for _, key := range order {
		a := accs[key]
		if a.count == 0 {
			continue
		}
		var exists int64
		if err := db.Model(&model.MetricRollup1h{}).
			Where("series_id = ? AND bucket_ts = ?", a.seriesID, a.bucket).Count(&exists).Error; err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		row := model.MetricRollup1h{
			SeriesID: a.seriesID, BucketTS: a.bucket,
			Avg: a.weightedSum / float64(a.count), Min: a.min, Max: a.max, Last: a.last, Count: a.count,
		}
		if err := db.Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

// newProbeSvc 建一个独立内存库（每个用例一套库，避免共享 cache 串扰）。
func newProbeSvc(t *testing.T, name string) *MetricService {
	t.Helper()
	dsn := "file:" + t.Name() + "-" + name + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.MetricSeries{}, &model.MetricSampleRaw{},
		&model.ProcessMetricSnapshot{},
		&model.MetricRollup5m{}, &model.MetricRollup1h{},
	))
	return NewMetricService(db)
}

// seedRawFixture 播种「多序列 × 多桶 + 缺测样本」的原始样本fixture。
// 每桶两点使 sum/min/max/count 都非平凡；nil 样本用于证明缺测仍被跳过。
func seedRawFixture(t *testing.T, db *gorm.DB, base time.Time, series []uint, buckets int) {
	t.Helper()
	rows := make([]model.MetricSampleRaw, 0, len(series)*buckets*3)
	for _, sid := range series {
		for b := 0; b < buckets; b++ {
			bt := base.Add(time.Duration(b) * metricBucket5m)
			// 每桶两点：值随序列与桶变化，保证 min/max/avg 各不相同。
			v1 := float64(sid*100+uint(b)) + 1
			v2 := float64(sid*100+uint(b)) + 5
			rows = append(rows,
				model.MetricSampleRaw{SeriesID: sid, TS: bt, Value: &v1},
				model.MetricSampleRaw{SeriesID: sid, TS: bt.Add(time.Minute), Value: &v2},
				// 缺测：必须不计入 sum/min/max/count，也不单独建桶。
				model.MetricSampleRaw{SeriesID: sid, TS: bt.Add(2 * time.Minute), Value: nil},
			)
		}
	}
	seedChunked(t, db, rows)
}

// readRollup5m / readRollup1h 按 (series_id, bucket_ts) 稳定排序读回聚合行，并清零自增 ID
// 以便逐字段对拍（ID 是实现细节，不属于聚合语义）。
func readRollup5m(t *testing.T, db *gorm.DB) []model.MetricRollup5m {
	t.Helper()
	var rows []model.MetricRollup5m
	require.NoError(t, db.Order("series_id, bucket_ts, id").Find(&rows).Error)
	for i := range rows {
		rows[i].ID = 0
	}
	return rows
}

func readRollup1h(t *testing.T, db *gorm.DB) []model.MetricRollup1h {
	t.Helper()
	var rows []model.MetricRollup1h
	require.NoError(t, db.Order("series_id, bucket_ts, id").Find(&rows).Error)
	for i := range rows {
		rows[i].ID = 0
	}
	return rows
}

// TestMetric_RollupBatch_EqualsLegacyPerBucketCount ① 批量预取与逐桶 COUNT 语义等价。
//
// 断言要点：同一份输入（3 序列 × 独立时间戳的多桶 + 缺测样本）下，新实现与冻结的旧实现
// 产出的 5m / 1h 聚合行**逐字段完全相同**（Avg/Min/Max/Last/Count），且行数相同。
// 规模刻意设计为候选桶数 > metricInsertBatch，以便同时覆盖「分批写入」路径。
func TestMetric_RollupBatch_EqualsLegacyPerBucketCount(t *testing.T) {
	base := metricBase()
	series := []uint{11, 22, 33}
	// 72 个 5m 桶 × 3 序列 = 216 候选行 > metricInsertBatch(200) → 必跨两个写入批次。
	const buckets5m = 72
	now := base.Add(buckets5m*metricBucket5m + 5*time.Minute) // 所有桶均已完结

	// 新实现侧。
	fresh := newProbeSvc(t, "new")
	seedRawFixture(t, fresh.db, base, series, buckets5m)
	require.NoError(t, fresh.rollup5m(now))

	// 冻结旧实现侧（同种输入）。
	legacy := newProbeSvc(t, "legacy")
	seedRawFixture(t, legacy.db, base, series, buckets5m)
	require.NoError(t, legacyRollup5m(legacy.db, now))

	got5m := readRollup5m(t, fresh.db)
	want5m := readRollup5m(t, legacy.db)
	require.NotEmpty(t, want5m, "fixture 必须真的产出聚合行，否则等价性断言是空转")
	require.Greaterf(t, len(want5m), metricInsertBatch, "候选行数应跨写入批次，实际 %d", len(want5m))
	require.Equal(t, want5m, got5m, "5m 聚合行必须与逐桶 COUNT 版逐字段一致")

	// 1h 档：直接播种 5m 表（rollup1h 的输入），并让候选数跨 metricInsertBatch。
	// 220 个 1h 桶 × 3 序列 = 660 行；每 1h 桶含两个 5m 桶，使 count 加权口径非平凡。
	const buckets1h = 220
	fiveRows := make([]model.MetricRollup5m, 0, len(series)*buckets1h*2)
	for _, sid := range series {
		for h := 0; h < buckets1h; h++ {
			ht := base.Add(time.Duration(h) * metricBucket1h)
			for _, off := range []time.Duration{0, 5 * time.Minute} {
				// count 与 avg 都不同，能暴露「加权 vs 算术平均」的口径差异。
				avg := float64(sid) * 10
				cnt := 2
				if off > 0 {
					avg, cnt = float64(sid)*10+7, 4
				}
				fiveRows = append(fiveRows, model.MetricRollup5m{
					SeriesID: sid, BucketTS: ht.Add(off),
					Avg: avg, Min: avg - 1, Max: avg + 1, Last: avg + 0.5, Count: cnt,
				})
			}
		}
	}
	now1h := base.Add(buckets1h*metricBucket1h + time.Hour)

	fresh1h := newProbeSvc(t, "new1h")
	seedChunked(t, fresh1h.db, fiveRows)
	require.NoError(t, fresh1h.rollup1h(now1h))

	legacy1h := newProbeSvc(t, "legacy1h")
	seedChunked(t, legacy1h.db, fiveRows)
	require.NoError(t, legacyRollup1h(legacy1h.db, now1h))

	got1h := readRollup1h(t, fresh1h.db)
	want1h := readRollup1h(t, legacy1h.db)
	require.NotEmpty(t, want1h)
	require.Greaterf(t, len(want1h), metricInsertBatch, "1h 候选行数应跨写入批次，实际 %d", len(want1h))
	require.Equal(t, want1h, got1h, "1h 聚合行必须与逐桶 COUNT 版逐字段一致")
}

// TestMetric_RollupBatch_LastIsNewestBucketSample ② last 语义：取桶内最新样本（含乱序写入）。
//
// 分批后扫描不再按 ts 全局排序，故 last 改为显式按 (ts, id) 取最大。本用例用乱序插入
// （更旧的拍后写到更大的 id）证明结果与写入顺序无关，且等于「ts 最大」的那一行。
func TestMetric_RollupBatch_LastIsNewestBucketSample(t *testing.T) {
	svc := newProbeSvc(t, "last")
	base := metricBase()

	// 故意乱序：先写桶内较新的拍（ts=+4m，值 40），再写较旧的拍（ts=+1m，值 10）。
	newer, older := 40.0, 10.0
	require.NoError(t, svc.db.Create(&[]model.MetricSampleRaw{
		{SeriesID: 5, TS: base.Add(4 * time.Minute), Value: &newer},
		{SeriesID: 5, TS: base.Add(time.Minute), Value: &older},
	}).Error)

	require.NoError(t, svc.rollup5m(base.Add(10*time.Minute)))

	rows := readRollup5m(t, svc.db)
	require.Len(t, rows, 1)
	require.Equal(t, 40.0, rows[0].Last, "last 必须取桶内最新拍，而非最后写入的一行")
	require.Equal(t, 2, rows[0].Count)
	require.Equal(t, 10.0, rows[0].Min)
	require.Equal(t, 40.0, rows[0].Max)
	require.InDelta(t, 25.0, rows[0].Avg, 1e-9)
}

// TestMetric_Rollup1hBatch_LastIsNewestFiveMinuteBucket last 在 1h 档同样取最新 5m 桶。
func TestMetric_Rollup1hBatch_LastIsNewestFiveMinuteBucket(t *testing.T) {
	svc := newProbeSvc(t, "last1h")
	base := metricBase()

	// 乱序：先写较晚的 5m 桶（+55m，last=99），再写较早的（+5m，last=11）。
	require.NoError(t, svc.db.Create(&[]model.MetricRollup5m{
		{SeriesID: 3, BucketTS: base.Add(55 * time.Minute), Avg: 90, Min: 90, Max: 90, Last: 99, Count: 1},
		{SeriesID: 3, BucketTS: base.Add(5 * time.Minute), Avg: 20, Min: 20, Max: 20, Last: 11, Count: 1},
	}).Error)

	require.NoError(t, svc.rollup1h(base.Add(2*time.Hour)))

	rows := readRollup1h(t, svc.db)
	require.Len(t, rows, 1)
	require.Equal(t, 99.0, rows[0].Last, "last 必须取 1h 桶内最新的 5m 桶")
	require.Equal(t, 2, rows[0].Count)
	require.InDelta(t, 55.0, rows[0].Avg, 1e-9) // (90+20)/2，count 加权
}

// TestMetric_RollupBatch_PartialExistingNoDupNoMiss ③ 中途已存在部分桶：不漏插、不重复插入。
//
// 模拟「上一轮只写完一部分就中断/重启」：库里已有部分桶（且带着哨兵值）。
// 断言要点：
//   - 已存在的桶保持**原值不被覆盖**（哨兵仍在）且每键**恰好一行**（未重复插入）；
//   - 缺失的桶被补齐为与干净运行一致的值；
//   - 再跑一次完全幂等（行集合逐字段不变）。
func TestMetric_RollupBatch_PartialExistingNoDupNoMiss(t *testing.T) {
	base := metricBase()
	series := []uint{7, 8}
	const buckets = 30
	now := base.Add(buckets*metricBucket5m + 5*time.Minute)

	// 参照组：干净运行，得到「应当收敛到」的目标行集。
	clean := newProbeSvc(t, "clean")
	seedRawFixture(t, clean.db, base, series, buckets)
	require.NoError(t, clean.rollup5m(now))
	want := readRollup5m(t, clean.db)
	require.NotEmpty(t, want)

	// 对照组：先把一部分桶（序列 7 的前 10 个桶）以哨兵值写入，模拟中断后的残留。
	svc := newProbeSvc(t, "partial")
	seedRawFixture(t, svc.db, base, series, buckets)

	sentinel := make([]model.MetricRollup5m, 0, 10)
	for b := 0; b < 10; b++ {
		sentinel = append(sentinel, model.MetricRollup5m{
			SeriesID: 7, BucketTS: base.Add(time.Duration(b) * metricBucket5m),
			Avg: 777, Min: 777, Max: 777, Last: 777, Count: 777,
		})
	}
	seedChunked(t, svc.db, sentinel)

	require.NoError(t, svc.rollup5m(now))

	// 与参照组同口径读回（readRollup5m 会清零自增 ID，ID 属实现细节不参与聚合语义对拍）。
	rows := readRollup5m(t, svc.db)
	require.Len(t, rows, len(want), "补齐后行数应与干净运行一致（既无重复也无遗漏）")

	// 每个 (series_id, bucket_ts) 恰好一行——这是「不重复插入」的直接证据
	// （索引非唯一，重复插入在库层面不会被拦下）。
	keys := map[string]int{}
	for _, r := range rows {
		keys[metricBucketKey(r.SeriesID, r.BucketTS)]++
	}
	for k, n := range keys {
		require.Equalf(t, 1, n, "桶 %s 出现 %d 行，存在重复插入", k, n)
	}

	// 哨兵桶必须原封不动；其余桶必须与干净运行逐字段一致。
	for _, r := range rows {
		if r.SeriesID == 7 && !r.BucketTS.Before(base) && r.BucketTS.Before(base.Add(10*metricBucket5m)) {
			require.Equal(t, 777.0, r.Avg, "已存在桶不得被覆盖")
			require.Equal(t, 777, r.Count, "已存在桶不得被覆盖")
			continue
		}
		found := false
		for _, w := range want {
			if w.SeriesID == r.SeriesID && w.BucketTS.Equal(r.BucketTS) {
				require.Equal(t, w, r, "缺失桶补齐值必须与干净运行一致")
				found = true
				break
			}
		}
		require.True(t, found, "出现了干净运行中不存在的桶 series=%d bucket=%v", r.SeriesID, r.BucketTS)
	}

	// 幂等：再跑一次不改动任何行。
	before := readRollup5m(t, svc.db)
	require.NoError(t, svc.rollup5m(now))
	require.Equal(t, before, readRollup5m(t, svc.db), "重跑必须完全幂等")

	// 1h 档同一性质（含「已存在则跳过」）。
	const buckets1h = 20
	fiveRows := make([]model.MetricRollup5m, 0, buckets1h*2)
	for h := 0; h < buckets1h; h++ {
		ht := base.Add(time.Duration(h) * metricBucket1h)
		fiveRows = append(fiveRows,
			model.MetricRollup5m{SeriesID: 9, BucketTS: ht, Avg: 10, Min: 9, Max: 11, Last: 10, Count: 2},
			model.MetricRollup5m{SeriesID: 9, BucketTS: ht.Add(5 * time.Minute), Avg: 20, Min: 19, Max: 21, Last: 20, Count: 3},
		)
	}
	now1h := base.Add(buckets1h*metricBucket1h + time.Hour)

	clean1h := newProbeSvc(t, "clean1h")
	seedChunked(t, clean1h.db, fiveRows)
	require.NoError(t, clean1h.rollup1h(now1h))
	want1h := readRollup1h(t, clean1h.db)
	require.Len(t, want1h, buckets1h)

	svc1h := newProbeSvc(t, "partial1h")
	seedChunked(t, svc1h.db, fiveRows)
	seedChunked(t, svc1h.db, []model.MetricRollup1h{
		{SeriesID: 9, BucketTS: base, Avg: 555, Min: 555, Max: 555, Last: 555, Count: 555},
		{SeriesID: 9, BucketTS: base.Add(metricBucket1h), Avg: 555, Min: 555, Max: 555, Last: 555, Count: 555},
	})
	require.NoError(t, svc1h.rollup1h(now1h))

	got1h := readRollup1h(t, svc1h.db)
	require.Len(t, got1h, len(want1h), "1h 补齐后行数一致")
	kept := 0
	for _, r := range got1h {
		if r.Avg == 555 {
			kept++
			continue
		}
		found := false
		for _, w := range want1h {
			if w.SeriesID == r.SeriesID && w.BucketTS.Equal(r.BucketTS) {
				require.Equal(t, w, r)
				found = true
				break
			}
		}
		require.True(t, found)
	}
	require.Equal(t, 2, kept, "两个哨兵桶都必须保留")
}

// TestMetric_RollupBatch_ConvergesAfterWriteFailure ④ 写入中途失败后重跑必须收敛到同一结果。
//
// 注入：让第 2 次 Create（即第二个写入批次）失败。第一批已落库，库处于「部分写入」状态。
// 断言：错误向上返回；移除注入后重跑，行集合与干净运行**逐字段一致**且无重复行。
// 这直接对应「崩溃/重启后重跑仍收敛」的要求。
func TestMetric_RollupBatch_ConvergesAfterWriteFailure(t *testing.T) {
	base := metricBase()
	series := []uint{4}
	const buckets = 300 // 300 候选 > metricInsertBatch(200) → 至少两个写入批次
	now := base.Add(buckets*metricBucket5m + 5*time.Minute)

	clean := newProbeSvc(t, "clean")
	seedRawFixture(t, clean.db, base, series, buckets)
	require.NoError(t, clean.rollup5m(now))
	want := readRollup5m(t, clean.db)
	require.Greaterf(t, len(want), metricInsertBatch, "需跨写入批次才能制造「部分写入」")

	svc := newProbeSvc(t, "fail")
	seedRawFixture(t, svc.db, base, series, buckets)

	var creates int
	const cbName = "test:fail_second_rollup_insert"
	require.NoError(t, svc.db.Callback().Create().Before("gorm:create").Register(cbName, func(tx *gorm.DB) {
		creates++
		if creates == 2 {
			tx.AddError(errors.New("注入：第二个写入批次失败"))
		}
	}))

	err := svc.rollup5m(now)
	require.Error(t, err, "写入失败必须向上返回，不能静默")
	require.Equal(t, 2, creates, "应已尝试第二个批次")

	partial := readRollup5m(t, svc.db)
	require.NotEmpty(t, partial, "第一批应已落库，制造出「部分写入」现场")
	require.Less(t, len(partial), len(want), "此时库内行数应少于目标")

	// 移除注入后重跑：必须补齐剩余桶且不重复插入。
	require.NoError(t, svc.db.Callback().Create().Remove(cbName))
	require.NoError(t, svc.rollup5m(now))

	got := readRollup5m(t, svc.db)
	require.Equal(t, want, got, "重跑后必须与干净运行逐字段一致（不漏插、不重复、不覆盖）")

	keys := map[string]int{}
	for _, r := range got {
		keys[metricBucketKey(r.SeriesID, r.BucketTS)]++
	}
	for k, n := range keys {
		require.Equalf(t, 1, n, "桶 %s 出现 %d 行，重跑产生了重复插入", k, n)
	}
}

// TestMetric_RollupBatch_QueryCountBoundedBySeriesNotBuckets ⑤ 查询次数必须与桶数解耦。
//
// 改动前每个候选桶发一条 COUNT（生产上 21 万桶 = 21 万次往返，且每次都要抢唯一连接）。
// 本用例以「同一序列、桶数 8 倍」两次测量，断言增量只来自分批粒度、远低于线性增长，
// 并用由常量推导的上界做硬约束——一旦退回逐桶 COUNT，该断言立刻变红。
func TestMetric_RollupBatch_QueryCountBoundedBySeriesNotBuckets(t *testing.T) {
	base := metricBase()

	measure := func(bucketCount int) int {
		db, counter := withCountingLogger(t, newProbeSvc(t, fmt.Sprintf("q%d", bucketCount)).db)
		svc := NewMetricService(db)
		seedRawFixture(t, svc.db, base, []uint{1}, bucketCount)
		now := base.Add(time.Duration(bucketCount)*metricBucket5m + 5*time.Minute)

		counter.count = 0
		require.NoError(t, svc.rollup5m(now))
		// 先取数再断言产出，避免把核对用的 COUNT 也计进来（否则测量值虚高一条）。
		measured := counter.count
		var produced int64
		require.NoError(t, svc.db.Model(&model.MetricRollup5m{}).Count(&produced).Error)
		require.Equal(t, int64(bucketCount), produced, "本轮必须真的产出聚合行，否则计数断言是空转")
		return measured
	}

	small := measure(200)
	large := measure(1600)

	// 硬上界：由常量推导，而非拍脑袋的常数。
	//   - 扫描：每批 metricScanBatch 行，且需一次空批才判定结束（故 +1）；
	//   - 预取：每块 metricPrefetchChunk 个候选键一条查询；
	//   - 写入：每批 metricInsertBatch 行一条查询。
	// fixture 每桶 3 行（2 有效 + 1 缺测）。
	scanRows := 1600 * 3
	scanBound := (scanRows+metricScanBatch-1)/metricScanBatch + 1
	bound := scanBound + (1600+metricPrefetchChunk-1)/metricPrefetchChunk +
		(1600+metricInsertBatch-1)/metricInsertBatch
	require.LessOrEqualf(t, large, bound,
		"查询次数 %d 超出自常量推导的上界 %d（避免回到与桶数成正比的 N+1）", large, bound)

	// 解耦断言：桶数涨 8 倍，查询增量只能是分批粒度级的，绝不能是线性的。
	require.LessOrEqualf(t, large-small, 24,
		"桶数 200→1600 时查询数增量 %d 过大，疑回归为逐桶 COUNT（线性增量应为 1400）", large-small)

	// 与旧实现的量级对比：旧实现至少每桶一条 COUNT。
	require.Lessf(t, large, 1600/50,
		"1600 个桶只允许 %d 条查询，实际 %d；旧实现为每桶一条 COUNT", 1600/50, large)

	t.Logf("查询次数：200 桶=%d，1600 桶=%d（旧实现约为 200/1600 条）", small, large)
}

// TestMetric_Rollup1hBatch_QueryCountBounded ⑤（1h 档）同上的 1h 版本。
func TestMetric_Rollup1hBatch_QueryCountBounded(t *testing.T) {
	base := metricBase()

	measure := func(bucketCount int) int {
		svc := newProbeSvc(t, fmt.Sprintf("q1h%d", bucketCount))
		rows := make([]model.MetricRollup5m, 0, bucketCount*2)
		for h := 0; h < bucketCount; h++ {
			ht := base.Add(time.Duration(h) * metricBucket1h)
			rows = append(rows,
				model.MetricRollup5m{SeriesID: 2, BucketTS: ht, Avg: 1, Min: 1, Max: 1, Last: 1, Count: 1},
				model.MetricRollup5m{SeriesID: 2, BucketTS: ht.Add(5 * time.Minute), Avg: 2, Min: 2, Max: 2, Last: 2, Count: 1},
			)
		}
		seedChunked(t, svc.db, rows)

		db, counter := withCountingLogger(t, svc.db)
		svc = NewMetricService(db)
		now := base.Add(time.Duration(bucketCount)*metricBucket1h + time.Hour)
		counter.count = 0
		require.NoError(t, svc.rollup1h(now))
		var produced int64
		require.NoError(t, svc.db.Model(&model.MetricRollup1h{}).Count(&produced).Error)
		require.Equal(t, int64(bucketCount), produced)
		return counter.count
	}

	small := measure(200)
	large := measure(1600)
	require.LessOrEqualf(t, large-small, 24,
		"1h 档桶数 200→1600 时查询增量 %d 过大，疑回归为逐桶 COUNT", large-small)
	require.Lessf(t, large, 1600/50, "1h 档 1600 桶只允许 %d 条查询，实际 %d", 1600/50, large)
	t.Logf("1h 查询次数：200 桶=%d，1600 桶=%d", small, large)
}

// TestMetric_RollupBatch_ScanIsBatched ⑥ 采样扫描必须真正分批：单批行数不超过 metricScanBatch。
//
// 用查询日志断言「一次 SELECT 取回的行数有界」。这是队头阻塞修复的核心——
// 单条巨查询（生产 54.5 万行 / 145.8 秒）被替换为多批小查询后，才可能让出锁。
func TestMetric_RollupBatch_ScanIsBatched(t *testing.T) {
	svc := newProbeSvc(t, "scan")
	base := metricBase()
	// 造 > 2 个扫描批的样本。
	const total = metricScanBatch*2 + 137
	rows := make([]model.MetricSampleRaw, 0, total)
	for i := 0; i < total; i++ {
		v := float64(i%50) + 1
		rows = append(rows, model.MetricSampleRaw{
			SeriesID: 1, TS: base.Add(time.Duration(i) * 30 * time.Second), Value: &v,
		})
	}
	seedChunked(t, svc.db, rows)

	var batchSizes []int
	batches := 0
	const cbName = "test:record_raw_scan_batch"
	require.NoError(t, svc.db.Callback().Query().After("gorm:query").Register(cbName, func(tx *gorm.DB) {
		if tx.Statement.Table != "metric_sample_raws" {
			return
		}
		batches++
		batchSizes = append(batchSizes, int(tx.RowsAffected))
	}))
	defer func() { _ = svc.db.Callback().Query().Remove(cbName) }()

	now := base.Add(time.Duration(total)*30*time.Second + time.Hour)
	require.NoError(t, svc.rollup5m(now))

	require.GreaterOrEqualf(t, batches, 3, "应分成至少 3 批扫描，实际 %d 批", batches)
	for i, n := range batchSizes {
		require.LessOrEqualf(t, n, metricScanBatch, "第 %d 批取回 %d 行，超过单批上限 %d", i, n, metricScanBatch)
	}
	// 聚合结果仍须正确：全部样本落在同一序列、按 5m 桶拆分后 count 之和等于总样本数。
	var sumCount int64
	require.NoError(t, svc.db.Model(&model.MetricRollup5m{}).Select("COALESCE(SUM(count),0)").Scan(&sumCount).Error)
	require.Equal(t, int64(total), sumCount, "分批扫描不得漏掉任何样本")
}

// TestMetric_PurgeBatch_DeletesAcrossBatches ⑦ purge 分批删除：跨批删净且不多删。
func TestMetric_PurgeBatch_DeletesAcrossBatches(t *testing.T) {
	svc := newProbeSvc(t, "purge")
	base := metricBase()
	now := base

	// > metricPurgeBatch 条超期 raw，以及近端样本。
	const oldCount = metricPurgeBatch + 500 // 2500 → 两个删除批
	rows := make([]model.MetricSampleRaw, 0, oldCount+20)
	for i := 0; i < oldCount; i++ {
		v := 1.0
		rows = append(rows, model.MetricSampleRaw{
			SeriesID: 1, TS: now.Add(-metricRawRetention).Add(-time.Duration(i+1) * time.Second), Value: &v,
		})
	}
	for i := 0; i < 20; i++ {
		v := 2.0
		rows = append(rows, model.MetricSampleRaw{
			SeriesID: 1, TS: now.Add(-time.Duration(i+1) * time.Second), Value: &v,
		})
	}
	seedChunked(t, svc.db, rows)

	require.NoError(t, svc.purge(now))

	var left int64
	require.NoError(t, svc.db.Model(&model.MetricSampleRaw{}).Count(&left).Error)
	require.Equal(t, int64(20), left, "超期行应全部删除、近端行全部保留")

	// 幂等：再删一次无副作用。
	require.NoError(t, svc.purge(now))
	require.NoError(t, svc.db.Model(&model.MetricSampleRaw{}).Count(&left).Error)
	require.Equal(t, int64(20), left)
}

// TestMetric_PurgeBatch_RetentionBoundariesUnchanged ⑧ 保留期边界逐表不变（48h / 30d / 400d）。
//
// 边界取值刻意落在「恰好等于保留期」上：改动前条件是 `ts < cutoff`（严格小于），
// 故恰好等于边界的行必须**保留**、早一秒的必须删除。
func TestMetric_PurgeBatch_RetentionBoundariesUnchanged(t *testing.T) {
	svc := newProbeSvc(t, "ttl")
	now := metricBase()

	five := 5.0
	// raw：保留 48h。
	require.NoError(t, svc.db.Create(&[]model.MetricSampleRaw{
		{SeriesID: 1, TS: now.Add(-metricRawRetention).Add(-time.Second), Value: &five}, // 删
		{SeriesID: 1, TS: now.Add(-metricRawRetention), Value: &five},                   // 留（边界）
		{SeriesID: 1, TS: now.Add(-time.Second), Value: &five},                          // 留
	}).Error)

	// 5m：保留 30d。
	require.NoError(t, svc.db.Create(&[]model.MetricRollup5m{
		{SeriesID: 1, BucketTS: now.Add(-metric5mRetention).Add(-time.Second), Count: 1}, // 删
		{SeriesID: 1, BucketTS: now.Add(-metric5mRetention), Count: 1},                   // 留（边界）
	}).Error)

	// 1h：保留 400d。
	require.NoError(t, svc.db.Create(&[]model.MetricRollup1h{
		{SeriesID: 1, BucketTS: now.Add(-metric1hRetention).Add(-time.Second), Count: 1}, // 删
		{SeriesID: 1, BucketTS: now.Add(-metric1hRetention), Count: 1},                   // 留（边界）
	}).Error)

	// 进程快照：保留 48h（跟 raw 同口径）。
	require.NoError(t, svc.db.Create(&[]model.ProcessMetricSnapshot{
		{NodeUUID: "n", InstanceUUID: "i", PID: 1, SampledAt: now.Add(-metricRawRetention).Add(-time.Second)}, // 删
		{NodeUUID: "n", InstanceUUID: "i", PID: 2, SampledAt: now.Add(-metricRawRetention)},                   // 留（边界）
		{NodeUUID: "n", InstanceUUID: "i", PID: 3, SampledAt: now.Add(-time.Second)},                          // 留
	}).Error)

	require.NoError(t, svc.purge(now))

	var raws []model.MetricSampleRaw
	require.NoError(t, svc.db.Order("ts").Find(&raws).Error)
	require.Len(t, raws, 2, "raw 48h 边界：早 1 秒删、恰好等于留")

	var fives []model.MetricRollup5m
	require.NoError(t, svc.db.Order("bucket_ts").Find(&fives).Error)
	require.Len(t, fives, 1, "5m 30d 边界：早 1 秒删、恰好等于留")

	var ones []model.MetricRollup1h
	require.NoError(t, svc.db.Order("bucket_ts").Find(&ones).Error)
	require.Len(t, ones, 1, "1h 400d 边界：早 1 秒删、恰好等于等于留")

	var snaps []model.ProcessMetricSnapshot
	require.NoError(t, svc.db.Order("pid").Find(&snaps).Error)
	require.Len(t, snaps, 2, "进程快照 48h 边界：早 1 秒删、恰好等于与近端留")
	require.Equal(t, int32(2), snaps[0].PID)
	require.Equal(t, int32(3), snaps[1].PID)
}

// TestMetric_BucketKeyNoCollision 复合键字符串编码必须无碰撞（否则会「误判已存在」而漏插）。
func TestMetric_BucketKeyNoCollision(t *testing.T) {
	base := metricBase()
	// 分隔符两侧都是纯十进制，拼接位置无歧义：这两组必须不同。
	require.NotEqual(t,
		metricBucketKey(1, base.Add(23*time.Nanosecond)),
		metricBucketKey(12, base.Add(3*time.Nanosecond)),
	)
	require.NotEqual(t, metricBucketKey(1, base), metricBucketKey(1, base.Add(time.Nanosecond)))
	// 同复合键必须稳定相同（幂等判定依赖它）。
	require.Equal(t, metricBucketKey(9, base), metricBucketKey(9, base))
	// 真实桶粒度上区分相邻桶。
	require.NotEqual(t,
		metricBucketKey(1, base),
		metricBucketKey(1, base.Add(metricBucket5m)),
	)
}
