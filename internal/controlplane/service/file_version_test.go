package service

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	cpgrpc "github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/internal/controlplane/model"
)

func newFileVersionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/fv.db"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.FileVersion{}, &model.Instance{}, &model.Node{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil && sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestFileVersionService_Versions_ReturnsLatestFirst(t *testing.T) {
	db := newFileVersionTestDB(t)
	svc := NewFileVersionService(db, nil, DefaultFileVersionConfig())

	if _, err := svc.saveVersion(1, "config.yml", []byte("a: 1\n"), 7, nil); err != nil {
		t.Fatalf("保存版本 1 失败: %v", err)
	}
	if _, err := svc.saveVersion(1, "config.yml", []byte("a: 2\n"), 7, nil); err != nil {
		t.Fatalf("保存版本 2 失败: %v", err)
	}

	versions, err := svc.Versions(1, "config.yml")
	if err != nil {
		t.Fatalf("Versions 失败: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("期望 2 个版本, 实际 %d", len(versions))
	}
	if versions[0].ID <= versions[1].ID {
		t.Fatalf("版本应按 ID 倒序: %+v", versions)
	}
	if versions[0].Size != int64(len("a: 2\n")) {
		t.Fatalf("最新版本 size 应为 %d, 实际 %d", len("a: 2\n"), versions[0].Size)
	}
	if versions[0].AuthorID != 7 {
		t.Fatalf("作者 ID 应为 7, 实际 %d", versions[0].AuthorID)
	}
}

func TestFileVersionService_Versions_IsolatedByInstanceAndPath(t *testing.T) {
	db := newFileVersionTestDB(t)
	svc := NewFileVersionService(db, nil, DefaultFileVersionConfig())

	if _, err := svc.saveVersion(1, "a.txt", []byte("x"), 1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.saveVersion(2, "a.txt", []byte("y"), 1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.saveVersion(1, "b.txt", []byte("z"), 1, nil); err != nil {
		t.Fatal(err)
	}

	got, err := svc.Versions(1, "a.txt")
	if err != nil || len(got) != 1 {
		t.Fatalf("实例 1 的 a.txt 应只有 1 条, 实际 %d err=%v", len(got), err)
	}
	other, err := svc.Versions(2, "a.txt")
	if err != nil || len(other) != 1 || other[0].ID == got[0].ID {
		t.Fatalf("实例 2 应有独立版本: %+v err=%v", other, err)
	}
}

func TestFileVersionService_Versions_EmptyWhenNoRecord(t *testing.T) {
	db := newFileVersionTestDB(t)
	svc := NewFileVersionService(db, nil, DefaultFileVersionConfig())
	versions, err := svc.Versions(99, "missing.txt")
	if err != nil {
		t.Fatalf("Versions 不应报错: %v", err)
	}
	if len(versions) != 0 {
		t.Fatalf("期望空列表, 实际 %+v", versions)
	}
}

func TestFileVersionService_Prune_EnforcesRetentionLimit(t *testing.T) {
	db := newFileVersionTestDB(t)
	svc := NewFileVersionService(db, nil, FileVersionConfig{MaxPerFile: 3})

	for i := 0; i < 6; i++ {
		if _, err := svc.saveVersion(1, "f.txt", []byte{byte('a' + i)}, 1, nil); err != nil {
			t.Fatalf("保存第 %d 个版本失败: %v", i, err)
		}
	}

	versions, err := svc.Versions(1, "f.txt")
	if err != nil {
		t.Fatalf("Versions 失败: %v", err)
	}
	if len(versions) != 3 {
		t.Fatalf("保留上限为 3, 实际保留 %d", len(versions))
	}
	// 应保留最新的 3 个（内容 d/e/f），最旧的被裁剪。
	latest, err := decodeContent(mustContent(t, db, versions[0].ID))
	if err != nil {
		t.Fatal(err)
	}
	if string(latest) != "f" {
		t.Fatalf("最新版本内容应为 f, 实际 %q", latest)
	}
}

func TestFileVersionService_Prune_Unlimited(t *testing.T) {
	db := newFileVersionTestDB(t)
	svc := NewFileVersionService(db, nil, FileVersionConfig{MaxPerFile: 0, MaxSizeBytes: 1024})

	for i := 0; i < 5; i++ {
		if _, err := svc.saveVersion(1, "f.txt", []byte{byte('a' + i)}, 1, nil); err != nil {
			t.Fatal(err)
		}
	}
	versions, err := svc.Versions(1, "f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 5 {
		t.Fatalf("MaxPerFile<=0 应不裁剪, 实际保留 %d", len(versions))
	}
}

func TestFileVersionService_SaveVersion_BinarySafe(t *testing.T) {
	db := newFileVersionTestDB(t)
	svc := NewFileVersionService(db, nil, DefaultFileVersionConfig())

	// 含 NUL 与高位字节的二进制内容（如 jar/zip）。
	raw := []byte{0x00, 0x01, 0xFF, 0xFE, 0x7F, 0x80, 'P', 'K'}
	id, err := svc.saveVersion(1, "plugin.jar", raw, 1, nil)
	if err != nil {
		t.Fatalf("保存二进制版本失败: %v", err)
	}
	stored := mustContent(t, db, id)
	got, err := decodeContent(stored)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if string(got) != string(raw) {
		t.Fatalf("二进制内容回滚后不一致: 期望 %v, 实际 %v", raw, got)
	}
}

func TestFileVersionService_Diff_TextVersions(t *testing.T) {
	db := newFileVersionTestDB(t)
	svc := NewFileVersionService(db, nil, DefaultFileVersionConfig())

	from, err := svc.saveVersion(1, "x.txt", []byte("line1\nline2\n"), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	to, err := svc.saveVersion(1, "x.txt", []byte("line1\nline2-changed\n"), 1, nil)
	if err != nil {
		t.Fatal(err)
	}

	d, err := svc.Diff(1, "x.txt", from, to)
	if err != nil {
		t.Fatalf("Diff 失败: %v", err)
	}
	if d.Binary {
		t.Fatalf("文本内容不应标记为二进制")
	}
	if !strings.Contains(d.UnifiedDiff, "line2-changed") {
		t.Fatalf("diff 应包含变更行, 实际:\n%s", d.UnifiedDiff)
	}
}

func TestFileVersionService_Diff_BinaryDetected(t *testing.T) {
	db := newFileVersionTestDB(t)
	svc := NewFileVersionService(db, nil, DefaultFileVersionConfig())

	from, err := svc.saveVersion(1, "x.bin", []byte("text"), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	to, err := svc.saveVersion(1, "x.bin", []byte{0x00, 0xFF, 0xFE}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}

	d, err := svc.Diff(1, "x.bin", from, to)
	if err != nil {
		t.Fatalf("Diff 失败: %v", err)
	}
	if !d.Binary {
		t.Fatalf("含非 UTF-8 字节应标记为二进制")
	}
	if d.UnifiedDiff != "" {
		t.Fatalf("二进制 diff 文本应为空, 实际 %q", d.UnifiedDiff)
	}
}

func TestFileVersionService_Diff_MissingVersion(t *testing.T) {
	db := newFileVersionTestDB(t)
	svc := NewFileVersionService(db, nil, DefaultFileVersionConfig())
	if _, err := svc.Diff(1, "x.txt", 999, 1000); err == nil {
		t.Fatalf("不存在的源版本应返回错误")
	}
}

func TestFileVersionService_Rollback_VersionNotFound(t *testing.T) {
	db := newFileVersionTestDB(t)
	svc := NewFileVersionService(db, nil, DefaultFileVersionConfig())
	// 版本不存在时应在触达 gRPC 前返回错误（pool=nil 不会被解引用）。
	if _, err := svc.Rollback(1, "x.txt", 12345, 1); err == nil {
		t.Fatalf("回滚不存在的版本应返回错误")
	}
}

func TestNewFileVersionService_AppliesDefaultWhenZeroConfig(t *testing.T) {
	db := newFileVersionTestDB(t)
	svc := NewFileVersionService(db, nil, FileVersionConfig{})
	def := DefaultFileVersionConfig()
	if svc.cfg.MaxPerFile != def.MaxPerFile || svc.cfg.MaxSizeBytes != def.MaxSizeBytes {
		t.Fatalf("零值配置应回落默认值, 实际 %+v", svc.cfg)
	}
}

func TestDecodeContent_InvalidBase64(t *testing.T) {
	if _, err := decodeContent("!!!not-base64!!!"); err == nil {
		t.Fatalf("非法 base64 应返回错误")
	}
	// sanity：合法 base64 能还原。
	enc := base64.StdEncoding.EncodeToString([]byte("hello"))
	got, err := decodeContent(enc)
	if err != nil || string(got) != "hello" {
		t.Fatalf("合法 base64 还原失败: got=%q err=%v", got, err)
	}
}

func TestFileVersionService_Client_WorkDirNotSet(t *testing.T) {
	db := newFileVersionTestDB(t)
	node := model.Node{Name: "n1", Host: "127.0.0.1", Secret: "s", Status: model.NodeStatusOnline}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	inst := model.Instance{Name: "i1", NodeID: node.ID, WorkDir: ""}
	if err := db.Create(&inst).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewFileVersionService(db, cpgrpc.NewClientPool(), DefaultFileVersionConfig())

	if err := svc.SnapshotBeforeWrite(inst.ID, "a.txt", 1); err != ErrWorkDirNotSet {
		t.Fatalf("无工作目录应返回 ErrWorkDirNotSet, 实际 %v", err)
	}
}

func TestFileVersionService_Client_NodeNotConnected(t *testing.T) {
	db := newFileVersionTestDB(t)
	node := model.Node{Name: "n2", Host: "127.0.0.1", Secret: "s", Status: model.NodeStatusOnline}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	inst := model.Instance{Name: "i2", NodeID: node.ID, WorkDir: "/srv/i2"}
	if err := db.Create(&inst).Error; err != nil {
		t.Fatal(err)
	}
	// 空连接池 → Get 返回 false → ErrNodeNotConnected。
	svc := NewFileVersionService(db, cpgrpc.NewClientPool(), DefaultFileVersionConfig())

	if err := svc.SnapshotBeforeWrite(inst.ID, "a.txt", 1); err != ErrNodeNotConnected {
		t.Fatalf("节点未连接应返回 ErrNodeNotConnected, 实际 %v", err)
	}

	// Diff(to=0) 需读当前文件，同样经 client()，应传递连接错误。
	if _, err := svc.saveVersion(inst.ID, "a.txt", []byte("x"), 1, nil); err != nil {
		t.Fatal(err)
	}
	vs, err := svc.Versions(inst.ID, "a.txt")
	if err != nil || len(vs) != 1 {
		t.Fatalf("应有 1 个版本: %+v err=%v", vs, err)
	}
	if _, err := svc.Diff(inst.ID, "a.txt", vs[0].ID, 0); err != ErrNodeNotConnected {
		t.Fatalf("Diff(to=当前) 在节点未连接时应返回 ErrNodeNotConnected, 实际 %v", err)
	}

	// Rollback 在版本存在但节点未连接时，应在写回前返回连接错误。
	if _, err := svc.Rollback(inst.ID, "a.txt", vs[0].ID, 1); err != ErrNodeNotConnected {
		t.Fatalf("Rollback 在节点未连接时应返回 ErrNodeNotConnected, 实际 %v", err)
	}
}

// mustContent 直接从库读取某版本的原始存储内容（base64 字符串）。
func mustContent(t *testing.T, db *gorm.DB, id uint) string {
	t.Helper()
	var v model.FileVersion
	if err := db.First(&v, id).Error; err != nil {
		t.Fatalf("读取版本 #%d 失败: %v", id, err)
	}
	return v.Content
}
