package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// stubSettings 是 SettingsReader 的测试替身：按固定 map 返回生效值。
type stubSettings map[string]string

func (s stubSettings) EffectiveValue(key string) string { return s[key] }

// makeBackupAt 写一条已完成备份并把 CreatedAt 校准到指定时刻（用 Update 绕过 gorm autoCreateTime）。
func makeBackupAt(t *testing.T, db *gorm.DB, instanceID uint, createdAt time.Time) *model.Backup {
	t.Helper()
	b := makeBackup(t, db, instanceID, model.BackupModeFull, nil, nil)
	require.NoError(t, db.Model(&model.Backup{}).Where("id = ?", b.ID).Update("created_at", createdAt).Error)
	return b
}

// countBackups 统计未软删的备份条数。
func countBackups(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&model.Backup{}).Count(&n).Error)
	return n
}

func newBackupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Backup{}))
	return db
}

// makeBackup 写一条已完成备份，manifest 为给定文件指纹集合。
func makeBackup(t *testing.T, db *gorm.DB, instanceID uint, mode model.BackupMode, parent *uint, entries []*workerpb.BackupManifestEntry) *model.Backup {
	t.Helper()
	mj, err := json.Marshal(entries)
	require.NoError(t, err)
	b := &model.Backup{
		InstanceID: instanceID,
		Name:       "bk",
		Mode:       mode,
		ParentID:   parent,
		Status:     model.BackupStatusCompleted,
		FilePath:   "var/backups/x.tar.gz",
		Manifest:   string(mj),
	}
	require.NoError(t, db.Create(b).Error)
	return b
}

func entry(path string, size, mtime int64) *workerpb.BackupManifestEntry {
	return &workerpb.BackupManifestEntry{Path: path, Size: size, ModTime: mtime}
}

// TestLatestCompleted 返回最近一次已完成备份，无则 nil。
func TestLatestCompleted(t *testing.T) {
	db := newBackupTestDB(t)
	svc := NewBackupService(db, nil)

	got, err := svc.latestCompleted(1)
	require.NoError(t, err)
	require.Nil(t, got)

	makeBackup(t, db, 1, model.BackupModeFull, nil, nil)
	last := makeBackup(t, db, 1, model.BackupModeFull, nil, nil)

	got, err = svc.latestCompleted(1)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, last.ID, got.ID)
}

// TestCreateIncrementalWithoutBase 无已完成备份时创建增量被拒。
func TestCreateIncrementalWithoutBase(t *testing.T) {
	db := newBackupTestDB(t)
	svc := NewBackupService(db, nil)

	_, err := svc.CreateWithOptions(1, "inc", CreateOptions{Incremental: true})
	require.ErrorIs(t, err, ErrNoFullBaseForIncremental)
}

// TestResolveChain 自增量回溯到全量基，顺序为全量基在前。
func TestResolveChain(t *testing.T) {
	db := newBackupTestDB(t)
	svc := NewBackupService(db, nil)

	full := makeBackup(t, db, 1, model.BackupModeFull, nil, nil)
	inc1 := makeBackup(t, db, 1, model.BackupModeIncremental, &full.ID, nil)
	inc2 := makeBackup(t, db, 1, model.BackupModeIncremental, &inc1.ID, nil)

	chain, err := svc.resolveChain(inc2)
	require.NoError(t, err)
	require.Len(t, chain, 3)
	require.Equal(t, full.ID, chain[0].ID) // 全量基在前
	require.Equal(t, inc1.ID, chain[1].ID)
	require.Equal(t, inc2.ID, chain[2].ID)
}

// TestResolveChain_BrokenParent 父备份缺失时报链断裂。
func TestResolveChain_BrokenParent(t *testing.T) {
	db := newBackupTestDB(t)
	svc := NewBackupService(db, nil)

	missing := uint(999)
	orphan := makeBackup(t, db, 1, model.BackupModeIncremental, &missing, nil)

	_, err := svc.resolveChain(orphan)
	require.Error(t, err)
	require.Contains(t, err.Error(), "断裂")
}

// TestChainManifest_MergesLatestFingerprint 合并链清单，后出现的覆盖同路径条目。
func TestChainManifest_MergesLatestFingerprint(t *testing.T) {
	db := newBackupTestDB(t)
	svc := NewBackupService(db, nil)

	// 全量：a(v1)、b(v1)。
	full := makeBackup(t, db, 1, model.BackupModeFull, nil, []*workerpb.BackupManifestEntry{
		entry("a.txt", 3, 100), entry("b.txt", 3, 100),
	})
	// 增量：a 改 v2、新增 c。
	inc := makeBackup(t, db, 1, model.BackupModeIncremental, &full.ID, []*workerpb.BackupManifestEntry{
		entry("a.txt", 5, 200), entry("b.txt", 3, 100), entry("c.txt", 4, 200),
	})

	// 以 inc 为父构建下一次增量的基准（应反映 a 的最新指纹与 c 的存在）。
	merged, err := svc.chainManifest(&inc.ID)
	require.NoError(t, err)

	m := map[string]*workerpb.BackupManifestEntry{}
	for _, e := range merged {
		m[e.Path] = e
	}
	require.Len(t, m, 3)
	require.Equal(t, int64(5), m["a.txt"].Size)   // 取最新（增量覆盖全量）
	require.Equal(t, int64(200), m["a.txt"].ModTime)
	require.Contains(t, m, "c.txt")
}

// TestDelete_RejectedWhenReferenced 删除有增量子备份的备份被拒，避免割裂链。
func TestDelete_RejectedWhenReferenced(t *testing.T) {
	db := newBackupTestDB(t)
	svc := NewBackupService(db, nil)

	full := makeBackup(t, db, 1, model.BackupModeFull, nil, nil)
	makeBackup(t, db, 1, model.BackupModeIncremental, &full.ID, nil)

	err := svc.Delete(full.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "增量备份依赖")

	// 叶子（无子）可删。
	leaf := makeBackup(t, db, 2, model.BackupModeFull, nil, nil)
	require.NoError(t, svc.Delete(leaf.ID))
}

// TestPruneExpired_RemovesOldKeepsRecent 保留 N 天：超 N 天的旧备份被裁剪、未超期的保留（FR-063）。
func TestPruneExpired_RemovesOldKeepsRecent(t *testing.T) {
	db := newBackupTestDB(t)
	svc := NewBackupService(db, nil)
	svc.SetSettingsReader(stubSettings{SettingKeyBackupRetentionDays: "30"})

	now := time.Now()
	old1 := makeBackupAt(t, db, 1, now.AddDate(0, 0, -40)) // 超 30 天
	old2 := makeBackupAt(t, db, 1, now.AddDate(0, 0, -31)) // 超 30 天
	recent := makeBackupAt(t, db, 1, now.AddDate(0, 0, -10)) // 未超期
	require.Equal(t, int64(3), countBackups(t, db))

	deleted := svc.pruneExpiredOnce()
	require.Equal(t, 2, deleted)

	// 超期的被删，未超期的留。
	require.Equal(t, int64(1), countBackups(t, db))
	var survivors []model.Backup
	require.NoError(t, db.Find(&survivors).Error)
	require.Len(t, survivors, 1)
	require.Equal(t, recent.ID, survivors[0].ID)

	// 已删的确实查不到（软删）。
	require.ErrorIs(t, db.First(&model.Backup{}, old1.ID).Error, gorm.ErrRecordNotFound)
	require.ErrorIs(t, db.First(&model.Backup{}, old2.ID).Error, gorm.ErrRecordNotFound)
}

// TestPruneExpired_ZeroDaysKeepsAll 保留天数<=0 视为不裁剪：全部保留。
func TestPruneExpired_ZeroDaysKeepsAll(t *testing.T) {
	db := newBackupTestDB(t)
	svc := NewBackupService(db, nil)
	svc.SetSettingsReader(stubSettings{SettingKeyBackupRetentionDays: "0"})

	makeBackupAt(t, db, 1, time.Now().AddDate(0, 0, -100))
	require.Equal(t, 0, svc.pruneExpiredOnce()) // 不裁剪
	require.Equal(t, int64(1), countBackups(t, db))
}

// TestPruneExpired_KeepsChainWhenChildNotExpired 超期全量基有未超期增量子时跳过该基（保链可恢复）。
func TestPruneExpired_KeepsChainWhenChildNotExpired(t *testing.T) {
	db := newBackupTestDB(t)
	svc := NewBackupService(db, nil)
	svc.SetSettingsReader(stubSettings{SettingKeyBackupRetentionDays: "30"})

	now := time.Now()
	// 全量基已超期，但其增量子未超期 → 删基会割裂链，应被 Delete 拒绝、本轮跳过。
	full := makeBackupAt(t, db, 1, now.AddDate(0, 0, -40))
	inc := makeBackup(t, db, 1, model.BackupModeIncremental, &full.ID, nil)
	require.NoError(t, db.Model(&model.Backup{}).Where("id = ?", inc.ID).Update("created_at", now.AddDate(0, 0, -5)).Error)

	deleted := svc.pruneExpiredOnce()
	require.Equal(t, 0, deleted)                 // 基被拒删，子未超期不在裁剪集
	require.Equal(t, int64(2), countBackups(t, db)) // 链完整保留
}
