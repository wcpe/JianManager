package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// TestCapabilityBaselineShortWindow 是 FR-498 的**短窗**容量读数（不是长稳，不能替代 9×30min）。
//
// 目的：在当前 HEAD 上给出一个可复算的内存基线，回答「60 源稳态 RSS 是否会随吞吐线性增长」。
// 长稳窗（9×30min）需要独占宿主，本用例刻意不做——它只跑 60 源 × 30 行/s × N 秒。
func TestCapabilityBaselineShortWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("容量读数在 -short 下跳过")
	}
	const (
		sources = 60
		lines   = 60 // 每源行数（一次性写入，随后 poll 采集）
	)
	root := t.TempDir()
	logDir := filepath.Join(root, "src")
	require.NoError(t, os.MkdirAll(logDir, 0o755))

	configs := make([]SourceConfig, 0, sources)
	paths := make([]string, 0, sources)
	for i := 0; i < sources; i++ {
		id := fmt.Sprintf("inst:cap-%03d", i)
		p := filepath.Join(logDir, fmt.Sprintf("cap-%03d.log", i))
		require.NoError(t, os.WriteFile(p, nil, 0o644))
		paths = append(paths, p)
		configs = append(configs, SourceConfig{
			LogSourceID: id, SourceGeneration: "g1", Path: p,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: id, UTCDay: runtimeTestUTCDay(),
		})
	}

	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)

	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{
		Root: root, Catalog: cat, Journal: cat.Journal(), Sources: configs,
	})
	require.NoError(t, err)

	// 先登记再写内容（FILE_PRIMARY 源 StartAtEOF，登记后写入才会被采到）。
	for i, p := range paths {
		var body []byte
		for n := 0; n < lines; n++ {
			body = append(body, []byte(fmt.Sprintf(
				"[12:00:%02d] [Server thread/INFO]: src=%d seq=%d padding=%s\n",
				n%60, i, n, "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"))...)
		}
		require.NoError(t, os.WriteFile(p, body, 0o644))
	}

	started := time.Now()
	for round := 0; round < 3; round++ {
		m.pollOnce()
	}
	elapsed := time.Since(started)

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	heapMiB := float64(after.HeapAlloc) / (1 << 20)
	rssMiB := float64(after.Sys) / (1 << 20)

	t.Logf("FR-498 短窗读数：%d 源 × %d 行 = %d 行事件", sources, lines, sources*lines)
	t.Logf("  采集耗时（3 轮 poll）= %s", elapsed.Round(time.Millisecond))
	t.Logf("  HeapAlloc=%.1f MiB  Sys(≈RSS 上界)=%.1f MiB  基线 Sys=%.1f MiB",
		heapMiB, rssMiB, float64(base.Sys)/(1<<20))
	t.Logf("  每源常驻 = %.2f KiB", heapMiB*1024/float64(sources))

	// 判据：60 源规模下堆占用不应随源数线性膨胀到「每源 > 1 MiB」。
	require.Less(t, heapMiB/float64(sources), 1.0,
		"每源堆占用必须 < 1 MiB（超出说明有按源常驻的正文/索引副本）")
}
