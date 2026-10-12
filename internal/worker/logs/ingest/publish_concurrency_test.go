package ingest

// 共享 namespace 的并发发布护栏（FR-498 跨源并发的正确性回归）。
//
// 背景：`publish` 是「读当前记录 → 应用本源的投影 → CAS 写回」的读-改-写。同一 namespace 会有
// 多个源（同一实例的 stdout/stderr、多文件源）。采集轮跨源并发之后，两个源会各自读到同一份旧
// 记录，后写者把先写者的 SourceProjections **整个覆盖**（实测：两个 stdio 源并发投递后记录里
// 只剩 1 条源投影，串行实现为 2 条）。修复是 `Manager.publishMu`：锁内重新读取当前记录作为合并
// 基础。本用例用「校验查询屏障」把两个源的发布对齐到同一时刻，从而稳定复现该竞态——
// 去掉 publishMu 即红（CAS 冲突使其中一个源发布失败，记录里只剩 1 条）。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// barrierVerifyVL 让「前 want 个校验查询」互相等待，直到全部到达才一起放行——
// 于是这些源的 publish 必然在同一时刻发生（校验是 publish 之前最后一个阻塞点）。
type barrierVerifyVL struct {
	inner   http.Handler
	want    int32
	arrived atomic.Int32
	once    sync.Once
	gate    chan struct{}
}

func (b *barrierVerifyVL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/select/logsql/query" && r.URL.Query().Get("start") != "" {
		if b.arrived.Add(1) >= b.want {
			b.once.Do(func() { close(b.gate) })
		}
		select {
		case <-b.gate:
		case <-time.After(5 * time.Second): // 屏障未被填满时放行，避免用例悬挂（断言会另行报错）
		}
	}
	b.inner.ServeHTTP(w, r)
}

func TestConcurrentPollPublishesAllSourcesInSharedNamespace(t *testing.T) {
	fixture := &projectionVLFixture{}
	barrier := &barrierVerifyVL{inner: fixture, want: 2, gate: make(chan struct{})}
	server := httptest.NewServer(barrier)
	defer server.Close()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)

	root := t.TempDir()
	day := runtimeTestUTCDay()
	cfgs := make([]SourceConfig, 0, 2)
	for i, stream := range []string{"stdout", "stderr"} {
		dir := filepath.Join(root, stream)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		path := filepath.Join(dir, "latest.log")
		require.NoError(t, os.WriteFile(path, []byte(pollTestLines(i*10, 2)), 0o600))
		cfgs = append(cfgs, SourceConfig{
			LogSourceID:      fmt.Sprintf("inst:shared/%s", stream),
			SourceGeneration: "g1",
			Path:             path,
			Mode:             pipeline.ModeFilePrimary,
			StorageNamespace: "inst:shared", // ← 同一 namespace：两个源共享一份 Catalog 记录
			UTCDay:           day,
		})
	}
	cat := catalog.New(nil)
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: cfgs})
	require.NoError(t, err)

	m.pollOnce()

	rec, ok := cat.Get(catalog.PartitionKey{StorageNamespace: "inst:shared", UTCDay: day})
	require.True(t, ok, "共享 namespace 必须产生一条 Catalog 记录")
	require.Len(t, rec.PublishedProjection.SourceProjections, 2,
		"并发轮里共享 namespace 的两个源都必须出现在投影清单中（读-改-写不得互相覆盖）")
}
