package lifecycle

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

func testKey() catalog.PartitionKey {
	return catalog.PartitionKey{StorageNamespace: "ns/game-1", UTCDay: "2026-09-20"}
}

// recordingOps 记录调用顺序，可按步骤注入失败。
type recordingOps struct {
	calls     []string
	failAt    string
	checksum  string
	snapshotN int
}

func (r *recordingOps) ops() Ops {
	return Ops{
		Drain: func(key catalog.PartitionKey) error {
			r.calls = append(r.calls, "drain")
			return r.errIf("drain")
		},
		Snapshot: func(key catalog.PartitionKey, fromDir string) (string, error) {
			r.calls = append(r.calls, "snapshot")
			if err := r.errIf("snapshot"); err != nil {
				return "", err
			}
			r.snapshotN++
			return "snap-1", nil
		},
		Copy: func(key catalog.PartitionKey, snapshotID, toDir string) (string, error) {
			r.calls = append(r.calls, "copy")
			if err := r.errIf("copy"); err != nil {
				return "", err
			}
			return "copy-1", nil
		},
		Verify: func(key catalog.PartitionKey, copyID, toDir string) (string, error) {
			r.calls = append(r.calls, "verify")
			if err := r.errIf("verify"); err != nil {
				return "", err
			}
			if r.checksum == "" {
				r.checksum = "sha256:ok"
			}
			return r.checksum, nil
		},
		Attach: func(key catalog.PartitionKey, dirID string) error {
			r.calls = append(r.calls, "attach")
			return r.errIf("attach")
		},
		LeaseDrain: func(key catalog.PartitionKey, oldGen uint64) error {
			r.calls = append(r.calls, "lease-drain")
			return r.errIf("lease-drain")
		},
		Detach: func(key catalog.PartitionKey, dirID string) error {
			r.calls = append(r.calls, "detach")
			return r.errIf("detach")
		},
	}
}

func (r *recordingOps) errIf(op string) error {
	if r.failAt == op {
		return errors.New("simulated failure at " + op)
	}
	return nil
}

// happy path：状态走完、owner 切换、staging 期间不可查询、迟到事件路由 COLD。
// staging attach 成功也不得进入 QueryPlanner（显式夹具）。
func mustRec(t *testing.T, m *Manager) *catalog.Record {
	t.Helper()
	rec, ok := m.GetPartition("ns/game-1", "2026-09-20")
	require.True(t, ok)
	return rec
}

// Runbook B：每个迁移状态崩溃后 Recover 必须给出期望的 owner/query 侧。
// advanceCrash 把稳定记录推到目标状态，模拟“进程在该状态崩溃”。
func advanceCrash(cat *catalog.Catalog, t *testing.T, target catalog.MigrationState) {
	t.Helper()
	if catalog.IsFailure(target) {
		// 失败态：从 STAGING_VERIFY 失败（权威未切换）。
		advanceCrash(cat, t, catalog.StateStagingVerify)
		_, err := cat.Fail(testKey(), target, "simulated crash-fail")
		require.NoError(t, err)
		return
	}
	if target == "" {
		return
	}
	_, err := cat.BeginMigration(testKey(), catalog.OwnerCold, "cold-g2")
	require.NoError(t, err)
	if target == catalog.StateRoutingFrozen {
		return
	}
	for _, st := range catalog.HappyPath {
		if st == catalog.StateRoutingFrozen {
			continue
		}
		if st == catalog.StateOwnerSwitched {
			proj := &catalog.PublishedProjection{
				ManifestVersion:    "pv-cold-g2",
				CoverageComplete:   true,
				QueryLocationDirID: "cold-g2",
				QueryGeneration:    2,
			}
			_, err = cat.SwitchOwner(testKey(), catalog.OwnerCold, "cold-g2", 2, proj)
			require.NoError(t, err)
		} else {
			_, err = cat.Advance(testKey(), st)
			require.NoError(t, err)
		}
		if st == target {
			if st == catalog.StateCleaned {
				// CLEANED 完成态：journal complete + residual 清理。
				if rec, ok := cat.Get(testKey()); ok {
					rec.ResidualDirs = nil
					rec.LastVerifyCompleted = true
					rec.LastVerifyChecksum = "sha256:ok"
					require.NoError(t, cat.Put(rec))
				}
				require.NoError(t, cat.MarkJournalComplete(testKey(), "sha256:ok"))
			}
			return
		}
	}
}

// StartMigration 中途操作失败 → FAILED_RETRYABLE，Recover 仍给出权威侧。
// retention last-copy：未确认接收前拒绝 detach（owner 已切换但迁移未完成）。
// retention 放行时 detach 发生且 CLEANED。
// VLSupRuntime 薄适配：Status 缺 supervisor 时报错；Attach/Detach 账面可用。
func TestVLSupRuntime_Adapter(t *testing.T) {
	rt := NewVLSupRuntime(nil)
	_, err := rt.Status(vlsup.NamespaceHot)
	require.Error(t, err)

	require.NoError(t, rt.AttachStorage(vlsup.NamespaceHot, "hot-g1", "/data/hot-g1"))
	require.Contains(t, rt.AttachedDirs(), "hot-g1")
	require.NoError(t, rt.DetachStorage(vlsup.NamespaceHot, "hot-g1"))
	require.NotContains(t, rt.AttachedDirs(), "hot-g1")

	// 2026-10-29：冷热双层废除，所有归属都落到同一个受管 VL。
	require.Equal(t, vlsup.NamespaceHot, NamespaceForOwner(catalog.OwnerHot))
	require.Equal(t, vlsup.NamespaceHot, NamespaceForOwner(catalog.OwnerCold))
	require.Equal(t, vlsup.NamespaceHot, NamespaceForOwner(catalog.OwnerArchive))

	_ = time.Now()
}

// 复制完成后篡改 COLD 内容，Verify 必须对比真实快照而不是把目标目录与自身比较。
