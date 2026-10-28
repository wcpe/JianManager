package stateindex

import (
	"fmt"
	"path/filepath"
	"testing"
)

// FR-486 测量夹具：加载耗时是否随索引行数线性增长。
//
// 为什么需要它（而不是只看空数据根的真机启动）：空状态下 load() 是 0ms，
// 测不出规模效应；而 load() 的实现在 Store.load 里对**每张表做全表 SELECT + ORDER BY**
// （store.go:823），persist 那套「只写变更源」的增量优化**不覆盖 load**。
// 因此规模效应必须用受控行数测出来，否则「③ 是否仍需做」只能靠猜。
//
// 口径：只测 Load()（纯读取 + 全表扫描 + 行解码），不含 SQLite 打开与迁移，
// 因为 openIndex 里的迁移只在旧 JSON 首次迁移时发生一次，不是稳态成本。

// buildState 构造一份含 nSources 个源、每源 batchesPerSource 个批次与同等规模
// gap/WAL 行的期望状态。行数主体是 batches（生产实测占绝对多数，见 plan.go 注释
// 「gap/source_wal/delivery_batch，占行数绝对多数」）。
func buildState(nSources, batchesPerSource int) State {
	st := State{
		Sources:     make([]SourceRow, 0, nSources),
		Positions:   make([]PositionRow, 0, nSources),
		Projections: make([]ProjectionRow, 0, nSources),
		Aux:         make([]AuxRow, 0, nSources),
	}
	for i := 0; i < nSources; i++ {
		key := fmt.Sprintf("src-%05d", i)
		st.Sources = append(st.Sources, SourceRow{
			Key:              key,
			LogSourceID:      fmt.Sprintf("log-%05d", i),
			SourceGeneration: "g1",
			StorageNamespace: "ns",
		})
		st.Positions = append(st.Positions, PositionRow{
			Key: key, ReadPos: uint64(i * 100), DurablePos: uint64(i * 100),
		})
		for b := 0; b < batchesPerSource; b++ {
			st.Batches = append(st.Batches, BatchRow{
				Key:      key,
				Ordinal:  int64(b),
				StartPos: uint64(b * 10),
				EndPos:   uint64(b*10 + 10),
				State:    "delivered",
			})
		}
	}
	return st
}

// BenchmarkLoadScaling 量化 Load() 随行数的增长。
//
// 跑法：go test ./internal/worker/logs/ingest/stateindex/ -run '^$' -bench BenchmarkLoadScaling -benchtime 1x
func BenchmarkLoadScaling(b *testing.B) {
	// 规模点位：批次总数 ≈ 1k / 10k / 50k / 100k
	cases := []struct {
		name         string
		sources      int
		batchesPerSr int
	}{
		{"rows-1k", 10, 100},
		{"rows-10k", 100, 100},
		{"rows-50k", 100, 500},
		{"rows-100k", 200, 500},
	}
	for _, c := range cases {
		c := c
		b.Run(c.name, func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "index.db")
			store, err := Open(path)
			if err != nil {
				b.Fatalf("打开索引失败: %v", err)
			}
			defer func() { _ = store.Close() }()

			st := buildState(c.sources, c.batchesPerSr)
			if _, err := store.Apply(st); err != nil {
				b.Fatalf("写入期望状态失败: %v", err)
			}
			rows := len(st.Sources) + len(st.Positions) + len(st.Projections) + len(st.Aux) + len(st.Batches)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				got, err := store.Load()
				if err != nil {
					b.Fatalf("Load 失败: %v", err)
				}
				if len(got.Batches) != len(st.Batches) {
					b.Fatalf("读出批次行数不符：期望 %d，实际 %d", len(st.Batches), len(got.Batches))
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(rows), "index_rows")
		})
	}
}
