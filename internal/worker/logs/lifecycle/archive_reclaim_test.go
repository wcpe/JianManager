package lifecycle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 本文件固定「已归档后回收热层」的**生产删除出口**（RemoveArchivedPartition）。
//
// 这条路径删的是不可逆的真实数据，故用例只断言两件事：
//   - 该拒绝的时候一步都不许走（未归档 / 证据不全 / 仍有 HOT 残留 / 仍被挂载）；
//   - 该删的时候 VL 已 detach、目录真的消失。
//
// 特别注意「未归档就拒绝」这条：它是「不得绕过 Catalog 直接删目录」的落点。
// 调用方（保留策略执行器）本来就会先迁移权威态，但这里仍然复核一次——
// 一次重试、一次并发轮次、一次人工补跑都可能绕过调用方的口头结论。

const reclaimDay = "2026-06-01"

func reclaimKey() catalog.PartitionKey {
	return catalog.PartitionKey{StorageNamespace: "inst:147", UTCDay: reclaimDay}
}

// reclaimFixture 构造 Catalog + 假的 HOT VL（记录 detach 调用）。
type reclaimFixture struct {
	cat      *catalog.Catalog
	key      catalog.PartitionKey
	physical *VLPartitionOps
	hotRoot  string
	// detached 记录 detach 调用次数。
	detached int
	// active 控制 /internal/partition/list 的返回。
	active bool
}

func newReclaimFixture(t *testing.T) *reclaimFixture {
	t.Helper()
	cat := catalog.New(nil)
	key := reclaimKey()
	rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "20260601")
	rec.PublishedProjection = &catalog.PublishedProjection{
		ManifestVersion:  "manifest-hot",
		CoverageComplete: true,
		CoveredSourceGenerations: []catalog.SourceGenerationRef{
			{LogSourceID: "source", SourceGeneration: "s1"},
		},
		ClosedVisibleSeq:      map[string]uint64{"source/s1": 1, "default": 1},
		QueryLocationDirID:    "20260601",
		QueryGeneration:       1,
		ProjectionGeneration:  "p-hot",
		ProjectionGenerations: []string{"p-hot"},
	}
	require.NoError(t, cat.Put(rec))

	f := &reclaimFixture{cat: cat, key: key, hotRoot: t.TempDir(), active: true}
	hotServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/partition/list":
			names := []string{}
			if f.active {
				names = append(names, "20260601")
			}
			require.NoError(t, json.NewEncoder(w).Encode(names))
		case "/internal/partition/detach":
			f.detached++
			f.active = false
		case "/internal/partition/snapshot/delete":
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(hotServer.Close)
	coldServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(coldServer.Close)
	hot, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: hotServer.URL})
	require.NoError(t, err)
	cold, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: coldServer.URL})
	require.NoError(t, err)
	physical, err := NewVLPartitionOps(hot, cold, f.hotRoot, t.TempDir(), cat)
	require.NoError(t, err)
	f.physical = physical

	// 真实落一个热层日分区目录，供删除断言。
	require.NoError(t, os.MkdirAll(filepath.Join(f.hotRoot, "partitions", "20260601"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(f.hotRoot, "partitions", "20260601", "part.bin"), []byte("data"), 0o644))
	return f
}

// archive 把记录迁移到 ARCHIVED（不标记回收）。
func (f *reclaimFixture) archive(t *testing.T) {
	t.Helper()
	_, _, err := f.cat.MarkArchived(f.key, catalog.ArchiveEvidence{
		ProviderKind: "local", ObjectPrefix: "inst:147/" + reclaimDay + "/g1",
		ManifestChecksum: "sha256:manifest",
		Coverage: catalog.ArchiveCoverage{
			ObjectCount: 1, SourceBytes: 8, StoredBytes: 4, Complete: true,
		},
	}, time.Now().UTC())
	require.NoError(t, err)
}

func (f *reclaimFixture) dayDirExists(t *testing.T) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(f.hotRoot, "partitions", "20260601"))
	return err == nil
}

// TestRemoveArchivedPartition_RefusesWhenNotArchived 未归档一律拒绝且不动目录。
func TestRemoveArchivedPartition_RefusesWhenNotArchived(t *testing.T) {
	f := newReclaimFixture(t)

	err := f.physical.RemoveArchivedPartition(context.Background(), f.key.StorageNamespace, f.key.UTCDay)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrArchiveReclaimRefused,
		"未归档时必须由 Catalog 复核拦下（不得绕过 Catalog 直接删目录）")
	require.Zero(t, f.detached, "拒绝时不得触碰 VL")
	require.True(t, f.dayDirExists(t), "拒绝时目录必须原封不动")
}

// TestRemoveArchivedPartition_RefusesWhenDayStillHot 同日仍有 HOT 成员时拒绝。
func TestRemoveArchivedPartition_RefusesWhenDayStillHot(t *testing.T) {
	f := newReclaimFixture(t)
	f.archive(t)
	// 同一天另一个命名空间仍属 HOT 权威：删目录会连带删掉它的数据。
	other := catalog.NewStableRecord(
		catalog.PartitionKey{StorageNamespace: "inst:148", UTCDay: reclaimDay}, catalog.OwnerHot, 1, "20260601-b")
	require.NoError(t, f.cat.Put(other))

	err := f.physical.RemoveArchivedPartition(context.Background(), f.key.StorageNamespace, f.key.UTCDay)
	require.ErrorIs(t, err, ErrArchiveReclaimRefused)
	require.Contains(t, err.Error(), "HOT 权威分区")
	require.Zero(t, f.detached)
	require.True(t, f.dayDirExists(t))
}

// TestRemoveArchivedPartition_RefusesWhenStillMounted VL 仍挂载时不删。
func TestRemoveArchivedPartition_RefusesWhenStillMounted(t *testing.T) {
	f := newReclaimFixture(t)
	f.archive(t)
	// detach 后仍报「活动」：模拟 VL 拒绝 detach（有在途写入/内部保护）。
	f.physical.Hot = mustClient(t, httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/partition/list":
			require.NoError(t, json.NewEncoder(w).Encode([]string{"20260601"}))
		case "/internal/partition/detach":
			// 什么都不做：仍处于活动状态
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})))

	err := f.physical.RemoveArchivedPartition(context.Background(), f.key.StorageNamespace, f.key.UTCDay)
	require.Error(t, err)
	require.Contains(t, err.Error(), "仍然处于挂载状态")
	require.True(t, f.dayDirExists(t), "VL 仍挂载时绝不能删目录")
}

// TestRemoveArchivedPartition_DeletesAfterDetach 正常路径：detach 后目录消失。
func TestRemoveArchivedPartition_DeletesAfterDetach(t *testing.T) {
	f := newReclaimFixture(t)
	f.archive(t)

	require.NoError(t, f.physical.RemoveArchivedPartition(context.Background(), f.key.StorageNamespace, f.key.UTCDay))
	require.Equal(t, 1, f.detached, "必须先 detach 再删（在 VL 持句柄时删目录只会解链接，磁盘不释放）")
	require.False(t, f.dayDirExists(t), "归档分区的热层目录必须被真正删除")
}

func mustClient(t *testing.T, srv *httptest.Server) *vlsup.Client {
	t.Helper()
	t.Cleanup(srv.Close)
	c, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	return c
}
