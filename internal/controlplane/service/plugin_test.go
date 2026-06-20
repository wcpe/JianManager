package service

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	cpgrpc "github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/internal/platform/dataroot"
	"github.com/wxys233/JianManager/proto/workerpb"
)

// fakeWorkerOps 是 pluginWorkerOps 的内存伪实现：以 dir → (filename → 内容存在性) 模拟实例文件树，
// 记录 Write/Delete/Rename 调用，供端到端覆盖 List/Upload/Delete/Toggle 的 gRPC 链路。
type fakeWorkerOps struct {
	// files[dir] = 文件名集合（值为字节大小，仅用于断言展示）。
	files map[string]map[string]int64
	// listErrDirs 中的目录在 ListFiles 时返回错误（模拟目录不存在）。
	listErrDirs map[string]bool
	writes      []string
	deletes     []string
	renames     [][2]string
}

func newFakeWorker() *fakeWorkerOps {
	return &fakeWorkerOps{files: map[string]map[string]int64{}, listErrDirs: map[string]bool{}}
}

func (f *fakeWorkerOps) put(dir, name string, size int64) {
	if f.files[dir] == nil {
		f.files[dir] = map[string]int64{}
	}
	f.files[dir][name] = size
}

func (f *fakeWorkerOps) ListFiles(_ context.Context, in *workerpb.ListFilesRequest, _ ...grpc.CallOption) (*workerpb.ListFilesResponse, error) {
	if f.listErrDirs[in.Path] {
		return nil, context.DeadlineExceeded // 任意错误即可，被调用方当作「目录不存在」
	}
	resp := &workerpb.ListFilesResponse{}
	for name, size := range f.files[in.Path] {
		resp.Files = append(resp.Files, &workerpb.FileInfo{Name: name, Size: size})
	}
	return resp, nil
}

func (f *fakeWorkerOps) WriteFile(_ context.Context, in *workerpb.WriteFileRequest, _ ...grpc.CallOption) (*workerpb.WriteFileResponse, error) {
	f.writes = append(f.writes, in.Path)
	return &workerpb.WriteFileResponse{Success: true}, nil
}

func (f *fakeWorkerOps) DeleteFile(_ context.Context, in *workerpb.DeleteFileRequest, _ ...grpc.CallOption) (*workerpb.DeleteFileResponse, error) {
	f.deletes = append(f.deletes, in.Path)
	return &workerpb.DeleteFileResponse{Success: true}, nil
}

func (f *fakeWorkerOps) RenameFile(_ context.Context, in *workerpb.RenameFileRequest, _ ...grpc.CallOption) (*workerpb.RenameFileResponse, error) {
	f.renames = append(f.renames, [2]string{in.OldPath, in.NewPath})
	return &workerpb.RenameFileResponse{Success: true}, nil
}

// TestParsePluginEntry 覆盖启用/禁用识别、非 jar 过滤、大小写与 .disabled 剥离。
func TestParsePluginEntry(t *testing.T) {
	cases := []struct {
		name        string
		filename    string
		dir         string
		wantOK      bool
		wantName    string
		wantEnabled bool
	}{
		{"启用插件", "EssentialsX.jar", "plugins", true, "EssentialsX.jar", true},
		{"禁用插件", "EssentialsX.jar.disabled", "plugins", true, "EssentialsX.jar", false},
		{"模组目录", "fabric-api.jar", "mods", true, "fabric-api.jar", true},
		{"大写扩展名", "Foo.JAR", "plugins", true, "Foo.JAR", true},
		{"非 jar 文件忽略", "config.yml", "plugins", false, "", false},
		{"无扩展名忽略", "README", "plugins", false, "", false},
		{"禁用态非 jar 忽略", "notes.txt.disabled", "plugins", false, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info, ok := parsePluginEntry(c.filename, c.dir)
			require.Equal(t, c.wantOK, ok)
			if c.wantOK {
				require.Equal(t, c.wantName, info.Name)
				require.Equal(t, c.wantEnabled, info.Enabled)
				require.Equal(t, c.dir, info.Dir)
			}
		})
	}
}

// TestToggledName 验证启用↔禁用文件名变换与切换后状态。
func TestToggledName(t *testing.T) {
	target, enabled := toggledName("EssentialsX.jar")
	require.Equal(t, "EssentialsX.jar.disabled", target)
	require.False(t, enabled)

	target, enabled = toggledName("EssentialsX.jar.disabled")
	require.Equal(t, "EssentialsX.jar", target)
	require.True(t, enabled)
}

// TestValidatePluginName 验证名称安全校验：拒绝路径遍历、分隔符、非 jar 与带 .disabled 的展示名。
func TestValidatePluginName(t *testing.T) {
	valid := []string{"EssentialsX.jar", "world-edit.jar", "Foo.JAR"}
	for _, n := range valid {
		require.NoError(t, validatePluginName(n), n)
	}

	invalid := []string{
		"",                       // 空
		"../EssentialsX.jar",     // 路径遍历
		"sub/dir/plugin.jar",     // 含分隔符
		"sub\\dir\\plugin.jar",   // Windows 分隔符
		"config.yml",             // 非 jar
		"EssentialsX.jar.disabled", // 展示名不应带 .disabled
	}
	for _, n := range invalid {
		require.ErrorIs(t, validatePluginName(n), ErrInvalidPluginName, n)
	}
}

// TestNormalizeDir 验证目录归一：仅 plugins/mods，其余回落 plugins。
func TestNormalizeDir(t *testing.T) {
	require.Equal(t, "plugins", normalizeDir(""))
	require.Equal(t, "plugins", normalizeDir("plugins"))
	require.Equal(t, "mods", normalizeDir("mods"))
	require.Equal(t, "plugins", normalizeDir("../etc")) // 非法目录回落
	require.Equal(t, "plugins", normalizeDir("worlds"))
}

func newPluginTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/plugin.db"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Instance{}, &model.Node{}))
	// Windows 上需显式关闭底层连接，否则 TempDir 清理因文件占用失败。
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil && sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// TestPluginService_List_GuardErrors 覆盖 List 的前置校验：
// 实例不存在 / 工作目录未设 / 节点离线 / 节点未连接，均在触达 gRPC 前返回明确错误。
func TestPluginService_List_GuardErrors(t *testing.T) {
	db := newPluginTestDB(t)
	pool := cpgrpc.NewClientPool()
	svc := NewPluginService(db, pool, nil)

	// 实例不存在。
	_, err := svc.List(999)
	require.ErrorIs(t, err, ErrInstanceNotFound)

	// 在线节点（pool 中无连接）。
	node := model.Node{UUID: "node-uuid-1", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&node).Error)

	// 工作目录未设。
	noWorkDir := model.Instance{UUID: "inst-1", NodeID: node.ID, Name: "a", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, StartCommand: "x", WorkDir: ""}
	require.NoError(t, db.Create(&noWorkDir).Error)
	_, err = svc.List(noWorkDir.ID)
	require.ErrorIs(t, err, ErrWorkDirNotSet)

	// 工作目录已设但节点未连接（pool 中无该 UUID）。
	withWorkDir := model.Instance{UUID: "inst-2", NodeID: node.ID, Name: "b", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, StartCommand: "x", WorkDir: "/srv/b"}
	require.NoError(t, db.Create(&withWorkDir).Error)
	_, err = svc.List(withWorkDir.ID)
	require.ErrorIs(t, err, ErrNodeNotConnected)

	// 离线节点。
	offNode := model.Node{UUID: "node-uuid-2", Status: model.NodeStatusOffline}
	require.NoError(t, db.Create(&offNode).Error)
	offInst := model.Instance{UUID: "inst-3", NodeID: offNode.ID, Name: "c", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, StartCommand: "x", WorkDir: "/srv/c"}
	require.NoError(t, db.Create(&offInst).Error)
	_, err = svc.List(offInst.ID)
	require.ErrorIs(t, err, ErrNodeNotOnline)
}

// TestPluginService_Mutations_RejectBadName 验证写操作在触达 gRPC 前拒绝非法名/目录穿越。
func TestPluginService_Mutations_RejectBadName(t *testing.T) {
	db := newPluginTestDB(t)
	svc := NewPluginService(db, cpgrpc.NewClientPool(), nil)

	_, err := svc.Upload(1, "plugins", "../evil.jar", []byte("x"))
	require.ErrorIs(t, err, ErrInvalidPluginName)

	err = svc.Delete(1, "plugins", "bad/name.jar")
	require.ErrorIs(t, err, ErrInvalidPluginName)

	_, err = svc.Toggle(1, "plugins", "notes.txt")
	require.ErrorIs(t, err, ErrInvalidPluginName)
}

// newPluginSvcWithFake 构建一个注入伪 Worker 的 PluginService，并在 DB 写入一个可用实例。
// 返回服务、伪 Worker、实例 ID。
func newPluginSvcWithFake(t *testing.T, asset *AssetService) (*PluginService, *fakeWorkerOps, uint) {
	t.Helper()
	db := newPluginTestDB(t)
	node := model.Node{UUID: "node-fake", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&node).Error)
	inst := model.Instance{UUID: "inst-fake", NodeID: node.ID, Name: "srv", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, StartCommand: "x", WorkDir: "/srv/srv"}
	require.NoError(t, db.Create(&inst).Error)

	fake := newFakeWorker()
	svc := NewPluginService(db, cpgrpc.NewClientPool(), asset)
	svc.workerResolver = func(string) (pluginWorkerOps, bool) { return fake, true }
	return svc, fake, inst.ID
}

// TestPluginService_List_AggregatesAndDetectsStatus 端到端覆盖 List：
// 聚合 plugins/+mods/、识别启用/禁用、剥离 .disabled、忽略非 jar 与子目录，目录不存在跳过。
func TestPluginService_List_AggregatesAndDetectsStatus(t *testing.T) {
	svc, fake, id := newPluginSvcWithFake(t, nil)
	fake.put("plugins", "EssentialsX.jar", 100)
	fake.put("plugins", "WorldEdit.jar.disabled", 200)
	fake.put("plugins", "config.yml", 5) // 非 jar，忽略
	fake.put("mods", "fabric-api.jar", 300)
	fake.listErrDirs["mods"] = false

	list, err := svc.List(id)
	require.NoError(t, err)
	require.Len(t, list, 3)

	byName := map[string]PluginInfo{}
	for _, p := range list {
		byName[p.Name] = p
	}
	require.True(t, byName["EssentialsX.jar"].Enabled)
	require.Equal(t, "plugins", byName["EssentialsX.jar"].Dir)
	require.False(t, byName["WorldEdit.jar"].Enabled) // .disabled 已剥离且标记禁用
	require.True(t, byName["fabric-api.jar"].Enabled)
	require.Equal(t, "mods", byName["fabric-api.jar"].Dir)
}

func TestPluginService_List_MissingDirsEmpty(t *testing.T) {
	svc, fake, id := newPluginSvcWithFake(t, nil)
	fake.listErrDirs["plugins"] = true
	fake.listErrDirs["mods"] = true
	list, err := svc.List(id)
	require.NoError(t, err)
	require.Empty(t, list)
}

// TestPluginService_Upload_IngestsAndDeploys 验证上传先入制品库（去重）再部署到目标目录。
func TestPluginService_Upload_IngestsAndDeploys(t *testing.T) {
	assetSvc := newAssetSvcForPlugin(t)
	svc, fake, id := newPluginSvcWithFake(t, assetSvc)

	asset, err := svc.Upload(id, "", "EssentialsX.jar", []byte("jar-bytes"))
	require.NoError(t, err)
	require.NotNil(t, asset)
	require.Equal(t, model.AssetTypePlugin, asset.Type)
	require.Equal(t, sha256hex([]byte("jar-bytes")), asset.SHA256)
	// 默认目录 plugins/。
	require.Equal(t, []string{"plugins/EssentialsX.jar"}, fake.writes)

	// 再次上传相同内容：制品库去重复用同一记录。
	asset2, err := svc.Upload(id, "mods", "EssentialsX.jar", []byte("jar-bytes"))
	require.NoError(t, err)
	require.Equal(t, asset.ID, asset2.ID)
	require.Equal(t, "mods/EssentialsX.jar", fake.writes[1])
}

// TestPluginService_Delete_ResolvesDisabledName 验证删除能命中禁用态文件名。
func TestPluginService_Delete_ResolvesDisabledName(t *testing.T) {
	svc, fake, id := newPluginSvcWithFake(t, nil)
	fake.put("plugins", "WorldEdit.jar.disabled", 1)

	require.NoError(t, svc.Delete(id, "plugins", "WorldEdit.jar"))
	require.Equal(t, []string{"plugins/WorldEdit.jar.disabled"}, fake.deletes)
}

func TestPluginService_Delete_NotFound(t *testing.T) {
	svc, _, id := newPluginSvcWithFake(t, nil)
	require.ErrorIs(t, svc.Delete(id, "plugins", "Ghost.jar"), ErrPluginNotFound)
}

// TestPluginService_Toggle_BothDirections 验证启用→禁用、禁用→启用的重命名与返回状态。
func TestPluginService_Toggle_BothDirections(t *testing.T) {
	svc, fake, id := newPluginSvcWithFake(t, nil)
	fake.put("plugins", "EssentialsX.jar", 1)

	enabled, err := svc.Toggle(id, "plugins", "EssentialsX.jar")
	require.NoError(t, err)
	require.False(t, enabled)
	require.Equal(t, [2]string{"plugins/EssentialsX.jar", "plugins/EssentialsX.jar.disabled"}, fake.renames[0])

	// 模拟文件已被禁用，再切回启用。
	delete(fake.files["plugins"], "EssentialsX.jar")
	fake.put("plugins", "EssentialsX.jar.disabled", 1)
	enabled, err = svc.Toggle(id, "plugins", "EssentialsX.jar")
	require.NoError(t, err)
	require.True(t, enabled)
	require.Equal(t, [2]string{"plugins/EssentialsX.jar.disabled", "plugins/EssentialsX.jar"}, fake.renames[1])
}

// newAssetSvcForPlugin 构建一个独立的制品库服务（独立 DB + 临时数据根），供上传去重测试使用。
func newAssetSvcForPlugin(t *testing.T) *AssetService {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/asset.db"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Asset{}))
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil && sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	root, err := dataroot.Init(filepath.Join(t.TempDir(), "data"))
	require.NoError(t, err)
	return NewAssetService(db, root)
}
