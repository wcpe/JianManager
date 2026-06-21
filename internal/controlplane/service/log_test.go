package service

import (
	"bufio"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

// newLogSvc 构造测试用 LogService：内存 SQLite + 临时数据根。
func newLogSvc(t *testing.T, cfg config.LogStoreConfig) (*LogService, *dataroot.Root, *gorm.DB) {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.LogEntry{}))
	root, err := dataroot.Init(filepath.Join(t.TempDir(), "data"))
	require.NoError(t, err)
	return NewLogService(db, root, cfg), root, db
}

func defaultCfg() config.LogStoreConfig {
	return config.LogStoreConfig{
		Enabled:                true,
		PersistPlatform:        true,
		RetentionDays:          14,
		MaxTotalMB:             512,
		ArchiveIntervalMinutes: 30,
	}
}

// seed 直接写入一条日志，绕过异步通道，便于确定性断言检索/归档。
func seed(t *testing.T, db *gorm.DB, e model.LogEntry) {
	t.Helper()
	require.NoError(t, db.Create(&e).Error)
}

func TestLog_QueryFilters(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	base := time.Now()

	seed(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: 1, NodeID: 9, Message: "server started", Time: base.Add(-3 * time.Minute)})
	seed(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelError, InstanceID: 1, NodeID: 9, Message: "crash NPE here", Time: base.Add(-2 * time.Minute)})
	seed(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: 2, NodeID: 9, Message: "another instance line", Time: base.Add(-1 * time.Minute)})
	seed(t, db, model.LogEntry{Source: model.LogSourceControlPlane, Level: model.LogLevelWarn, Message: "platform warn", Time: base})

	// 无过滤：全部 4 条，按时间倒序。
	page, err := svc.Query(LogFilter{})
	require.NoError(t, err)
	require.EqualValues(t, 4, page.Total)
	require.Len(t, page.Items, 4)
	require.Equal(t, "platform warn", page.Items[0].Message) // 最新在前

	// 按实例过滤。
	iid := uint(1)
	page, err = svc.Query(LogFilter{InstanceID: &iid})
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Total)

	// 按级别过滤。
	lvl := model.LogLevelError
	page, err = svc.Query(LogFilter{Level: &lvl})
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total)
	require.Equal(t, "crash NPE here", page.Items[0].Message)

	// 关键字过滤（DB 侧 LIKE）。
	page, err = svc.Query(LogFilter{Keyword: "crash"})
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total)

	// 来源过滤。
	src := model.LogSourceControlPlane
	page, err = svc.Query(LogFilter{Source: &src})
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total)

	// 时间范围过滤。
	from := base.Add(-90 * time.Second)
	page, err = svc.Query(LogFilter{From: &from})
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Total) // 仅最近 1 分钟内 + platform
}

func TestLog_QueryPagination(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	base := time.Now()
	for i := 0; i < 25; i++ {
		seed(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: 1, Message: "line", Time: base.Add(time.Duration(i) * time.Second)})
	}
	page, err := svc.Query(LogFilter{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.EqualValues(t, 25, page.Total)
	require.Len(t, page.Items, 10)

	page, err = svc.Query(LogFilter{Page: 3, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 5) // 最后一页
}

func TestLog_AccessibleInstanceIsolation(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	now := time.Now()
	seed(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: 1, Message: "a", Time: now})
	seed(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: 2, Message: "b", Time: now})

	// 收敛到实例 1。
	page, err := svc.Query(LogFilter{InstanceIDs: []uint{1}})
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total)

	// 空集合（无可访问实例）：返回 0 条而非全部。
	page, err = svc.Query(LogFilter{InstanceIDs: []uint{}})
	require.NoError(t, err)
	require.EqualValues(t, 0, page.Total)
}

func TestLog_Export(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	base := time.Now()
	seed(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: 1, Message: "first", Time: base.Add(-2 * time.Minute)})
	seed(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: 1, Message: "second", Time: base})

	items, err := svc.Export(LogFilter{}, 0)
	require.NoError(t, err)
	require.Len(t, items, 2)
	// 导出按时间正序，便于阅读。
	require.Equal(t, "first", items[0].Message)
	require.Equal(t, "second", items[1].Message)
}

func TestLog_IngestAsyncFlush(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	svc.Start()
	defer svc.Stop()

	for i := 0; i < 5; i++ {
		require.True(t, svc.Ingest(IngestEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: 1, Message: "async line"}))
	}

	// 轮询等待异步批量落库（避免对 flush 时序硬编码）。
	require.Eventually(t, func() bool {
		var n int64
		db.Model(&model.LogEntry{}).Count(&n)
		return n == 5
	}, 5*time.Second, 50*time.Millisecond)
}

func TestLog_IngestDisabledAndEmpty(t *testing.T) {
	cfg := defaultCfg()
	cfg.Enabled = false
	svc, _, _ := newLogSvc(t, cfg)
	require.False(t, svc.Enabled())
	require.False(t, svc.Ingest(IngestEntry{Message: "x"})) // 未启用直接丢弃

	svc2, _, _ := newLogSvc(t, defaultCfg())
	require.False(t, svc2.Ingest(IngestEntry{Message: ""})) // 空正文丢弃
}

func TestLog_ArchiveBeforeRetention(t *testing.T) {
	cfg := defaultCfg()
	cfg.RetentionDays = 7
	cfg.MaxTotalMB = 0 // 仅测时间保留
	svc, root, db := newLogSvc(t, cfg)

	old := time.Now().AddDate(0, 0, -10)
	recent := time.Now().AddDate(0, 0, -1)
	seed(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: 1, Message: "old line", Time: old})
	seed(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: 1, Message: "recent line", Time: recent})

	n, err := svc.archiveBeforeRetention()
	require.NoError(t, err)
	require.Equal(t, 1, n) // 仅旧的被归档

	// 表内只剩近的。
	var cnt int64
	db.Model(&model.LogEntry{}).Count(&cnt)
	require.EqualValues(t, 1, cnt)

	// 归档文件存在且含旧条目。
	archive := filepath.Join(root.LogDir(), "logs-"+old.Format("2006-01-02")+".ndjson")
	requireFileContains(t, archive, "old line")
}

func TestLog_ArchiveOverCapacity(t *testing.T) {
	cfg := defaultCfg()
	cfg.RetentionDays = 0 // 仅测总量
	cfg.MaxTotalMB = 0
	svc, root, db := newLogSvc(t, cfg)

	base := time.Now()
	for i := 0; i < 10; i++ {
		seed(t, db, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: 1, Message: "cap line", Time: base.Add(time.Duration(i) * time.Second)})
	}

	// MaxTotalMB<=0 时不清理。
	n, err := svc.archiveOverCapacity()
	require.NoError(t, err)
	require.Equal(t, 0, n)

	// MaxTotalMB 足以容纳全部 10 行（按 singleEntryBytes 折算行阈值远大于 10），也不清理。
	svc.cfg.MaxTotalMB = 512
	n, err = svc.archiveOverCapacity()
	require.NoError(t, err)
	require.Equal(t, 0, n)

	// 直接复用生产的归档落盘+删除逻辑，验证「裁剪到上限」会把最旧条目落盘并删表。
	removed := trimToRows(t, svc, db, 6)
	require.Equal(t, 4, removed) // 10 → 6，删 4
	var cnt int64
	db.Model(&model.LogEntry{}).Count(&cnt)
	require.EqualValues(t, 6, cnt)

	// 被裁剪的最旧条目应出现在归档文件。
	archive := filepath.Join(root.LogDir(), "logs-"+base.Format("2006-01-02")+".ndjson")
	requireFileContains(t, archive, "cap line")
}

// trimToRows 调用 flushArchiveAndDelete 把表裁剪到 maxRows 行（测试辅助，复用生产归档落盘+删除逻辑）。
func trimToRows(t *testing.T, svc *LogService, db *gorm.DB, maxRows int) int {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&model.LogEntry{}).Count(&count).Error)
	toRemove := int(count) - maxRows
	if toRemove <= 0 {
		return 0
	}
	var batch []model.LogEntry
	require.NoError(t, db.Order("time ASC").Order("id ASC").Limit(toRemove).Find(&batch).Error)
	require.NoError(t, svc.flushArchiveAndDelete(batch))
	return len(batch)
}

func TestLog_PurgeArchivesOlderThan(t *testing.T) {
	svc, root, _ := newLogSvc(t, defaultCfg())
	dir := root.LogDir()
	oldFile := filepath.Join(dir, "logs-2000-01-01.ndjson")
	newFile := filepath.Join(dir, "logs-9999-01-01.ndjson")
	require.NoError(t, os.WriteFile(oldFile, []byte("x\n"), 0o644))
	require.NoError(t, os.WriteFile(newFile, []byte("y\n"), 0o644))
	// 把旧文件 mtime 设成很久以前。
	past := time.Now().AddDate(0, 0, -30)
	require.NoError(t, os.Chtimes(oldFile, past, past))

	removed, err := svc.PurgeArchivesOlderThan(7)
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	require.NoFileExists(t, oldFile)
	require.FileExists(t, newFile)
}

func TestLog_IngestInstanceOutput(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))

	node := model.Node{UUID: "node-uuid-1", Name: "n1", Host: "127.0.0.1"}
	require.NoError(t, db.Create(&node).Error)
	inst := model.Instance{
		UUID:        "inst-uuid-1",
		Name:        "srv",
		NodeID:      node.ID,
		Type:        model.InstanceTypeGeneric,
		ProcessType: model.ProcessTypeDirect,
		StartCommand: "echo hi",
	}
	require.NoError(t, db.Create(&inst).Error)

	svc.Start()
	defer svc.Stop()

	// 多行 stdout chunk：拆成多条，空行跳过。
	svc.IngestInstanceOutput("node-uuid-1", "inst-uuid-1", "stdout", "line one\n\nline two\r\n", 0)
	// stderr 归为 error 级。
	svc.IngestInstanceOutput("node-uuid-1", "inst-uuid-1", "stderr", "boom", 0)

	require.Eventually(t, func() bool {
		var n int64
		db.Model(&model.LogEntry{}).Count(&n)
		return n == 3 // line one, line two, boom
	}, 5*time.Second, 50*time.Millisecond)

	// ID 解析正确回填。
	var entries []model.LogEntry
	require.NoError(t, db.Order("id ASC").Find(&entries).Error)
	for _, e := range entries {
		require.Equal(t, inst.ID, e.InstanceID)
		require.Equal(t, node.ID, e.NodeID)
		require.Equal(t, model.LogSourceInstance, e.Source)
	}
	// stderr 那条是 error 级。
	var errCount int64
	db.Model(&model.LogEntry{}).Where("level = ?", model.LogLevelError).Count(&errCount)
	require.EqualValues(t, 1, errCount)
}

func TestLog_PersistSlogHandler(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	svc.Start()
	defer svc.Stop()

	// 底层 handler 丢弃输出（io.Discard），只验证落库侧。
	inner := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})
	h := NewPersistSlogHandler(inner, svc)
	logger := slog.New(h)

	logger.Info("control plane started", "addr", ":8080")
	logger.Error("something failed", "err", "boom")
	// 带 SkipPersist 的记录不落库。
	logger.Warn("internal noise", SkipPersist())

	require.Eventually(t, func() bool {
		var n int64
		db.Model(&model.LogEntry{}).Where("source = ?", model.LogSourceControlPlane).Count(&n)
		return n == 2
	}, 5*time.Second, 50*time.Millisecond)

	// 属性平铺进正文，便于检索。
	var hit int64
	db.Model(&model.LogEntry{}).Where("message LIKE ?", "%addr=:8080%").Count(&hit)
	require.EqualValues(t, 1, hit)

	// 级别映射正确。
	var errCount int64
	db.Model(&model.LogEntry{}).Where("source = ? AND level = ?", model.LogSourceControlPlane, model.LogLevelError).Count(&errCount)
	require.EqualValues(t, 1, errCount)
}

func TestLog_PersistSlogHandlerBypass(t *testing.T) {
	// 未启用持久化时直接返回 inner（零开销旁路）。
	cfg := defaultCfg()
	cfg.PersistPlatform = false
	svc, _, _ := newLogSvc(t, cfg)
	inner := slog.NewTextHandler(io.Discard, nil)
	require.Equal(t, inner, NewPersistSlogHandler(inner, svc))
}

func TestLog_ResolveNodeFallback(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))
	node := model.Node{UUID: "node-only", Name: "n", Host: "127.0.0.1"}
	require.NoError(t, db.Create(&node).Error)

	svc.Start()
	defer svc.Stop()

	// 实例 UUID 查不到（实例未登记），但节点 UUID 能解析：日志仍带 nodeID 落库。
	svc.IngestInstanceOutput("node-only", "missing-instance", "stdout", "orphan line", 0)
	require.Eventually(t, func() bool {
		var n int64
		db.Model(&model.LogEntry{}).Where("node_id = ?", node.ID).Count(&n)
		return n == 1
	}, 5*time.Second, 50*time.Millisecond)
}

func TestLog_ArchiveOverCapacity_Triggers(t *testing.T) {
	cfg := defaultCfg()
	cfg.RetentionDays = 0
	cfg.MaxTotalMB = 1 // 1MB → 约 2048 行阈值
	svc, root, db := newLogSvc(t, cfg)

	// 插入超过阈值的行：阈值 = 1MB/512B = 2048，插 2100 触发裁剪到 2048。
	rows := make([]model.LogEntry, 0, 2100)
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 2100; i++ {
		rows = append(rows, model.LogEntry{Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: 1, Message: "x", Time: base.Add(time.Duration(i) * time.Millisecond)})
	}
	require.NoError(t, db.CreateInBatches(&rows, 500).Error)

	n, err := svc.archiveOverCapacity()
	require.NoError(t, err)
	require.Equal(t, 2100-2048, n) // 裁掉超出部分

	var cnt int64
	db.Model(&model.LogEntry{}).Count(&cnt)
	require.EqualValues(t, 2048, cnt)

	// 归档文件已生成。
	archive := filepath.Join(root.LogDir(), "logs-"+base.Format("2006-01-02")+".ndjson")
	require.FileExists(t, archive)
}

func TestLog_SlogHandlerWithAttrs(t *testing.T) {
	svc, _, db := newLogSvc(t, defaultCfg())
	svc.Start()
	defer svc.Stop()

	inner := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})
	// WithAttrs 累积的属性应一并落库进正文。
	logger := slog.New(NewPersistSlogHandler(inner, svc)).With("component", "scheduler")
	logger.Info("tick")

	require.Eventually(t, func() bool {
		var n int64
		db.Model(&model.LogEntry{}).Where("message LIKE ?", "%component=scheduler%").Count(&n)
		return n == 1
	}, 5*time.Second, 50*time.Millisecond)
}

func requireFileContains(t *testing.T, path, substr string) {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.Contains(sc.Text(), substr) {
			return
		}
	}
	t.Fatalf("归档文件 %s 未包含 %q", path, substr)
}
