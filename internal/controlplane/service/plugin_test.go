package service

import (
	"archive/zip"
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakeWorkerOps 是 pluginWorkerOps 的内存伪实现：以 dir → (filename → 内容存在性) 模拟实例文件树，
// 记录 Write/Delete/Rename 调用，供端到端覆盖 List/Upload/Delete/Toggle 的 gRPC 链路。
type fakeWorkerOps struct {
	mu sync.Mutex
	// files[dir] = 文件名集合（值为字节大小，仅用于断言展示）。
	files map[string]map[string]int64
	// listErrDirs 中的目录在 ListFiles 时返回错误（模拟目录不存在）。
	listErrDirs map[string]bool
	contents    map[string][]byte
	writeErrors map[string]string
	writes      []string
	deletes     []string
	renames     [][2]string
}

func newFakeWorker() *fakeWorkerOps {
	return &fakeWorkerOps{files: map[string]map[string]int64{}, listErrDirs: map[string]bool{}, contents: map[string][]byte{}, writeErrors: map[string]string{}}
}

func (f *fakeWorkerOps) put(dir, name string, size int64) {
	if f.files[dir] == nil {
		f.files[dir] = map[string]int64{}
	}
	f.files[dir][name] = size
}

func (f *fakeWorkerOps) putContent(dir, name string, content []byte) {
	f.put(dir, name, int64(len(content)))
	f.contents[dir+"/"+name] = content
}

func (f *fakeWorkerOps) ListFiles(_ context.Context, in *workerpb.ListFilesRequest, _ ...grpc.CallOption) (*workerpb.ListFilesResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErrDirs[in.Path] {
		return nil, context.DeadlineExceeded // 任意错误即可，被调用方当作「目录不存在」
	}
	resp := &workerpb.ListFilesResponse{}
	for name, size := range f.files[in.Path] {
		resp.Files = append(resp.Files, &workerpb.FileInfo{Name: name, Size: size})
	}
	return resp, nil
}

func (f *fakeWorkerOps) ReadFile(_ context.Context, in *workerpb.ReadFileRequest, _ ...grpc.CallOption) (*workerpb.ReadFileResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	content, ok := f.contents[in.Path]
	if !ok {
		return nil, context.DeadlineExceeded
	}
	return &workerpb.ReadFileResponse{Content: content}, nil
}

// UploadFile 模拟老 Worker（Unimplemented）：部署经 uploadToWorker 自动回退 WriteFile，
// 既有 writes/writeErrors 断言路径保持不变；流式路径由 file_upload_test.go 单独覆盖。
func (f *fakeWorkerOps) UploadFile(_ context.Context, _ ...grpc.CallOption) (workerpb.WorkerService_UploadFileClient, error) {
	return nil, status.Error(codes.Unimplemented, "unknown method UploadFile")
}

func (f *fakeWorkerOps) WriteFile(_ context.Context, in *workerpb.WriteFileRequest, _ ...grpc.CallOption) (*workerpb.WriteFileResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, in.Path)
	if msg := f.writeErrors[in.InstanceUuid+"/"+in.Path]; msg != "" {
		return &workerpb.WriteFileResponse{Success: false, Error: msg}, nil
	}
	if msg := f.writeErrors[in.Path]; msg != "" {
		return &workerpb.WriteFileResponse{Success: false, Error: msg}, nil
	}
	parts := strings.SplitN(in.Path, "/", 2)
	if len(parts) == 2 {
		f.put(parts[0], parts[1], int64(len(in.Content)))
	}
	return &workerpb.WriteFileResponse{Success: true}, nil
}

func (f *fakeWorkerOps) DeleteFile(_ context.Context, in *workerpb.DeleteFileRequest, _ ...grpc.CallOption) (*workerpb.DeleteFileResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, in.Path)
	return &workerpb.DeleteFileResponse{Success: true}, nil
}

func (f *fakeWorkerOps) RenameFile(_ context.Context, in *workerpb.RenameFileRequest, _ ...grpc.CallOption) (*workerpb.RenameFileResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renames = append(f.renames, [2]string{in.OldPath, in.NewPath})
	return &workerpb.RenameFileResponse{Success: true}, nil
}

// TestParsePluginEntry 覆盖启用/禁用识别、非法后缀过滤、大小写与 .disabled 剥离。
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
		{"资源包目录", "HighResPack.zip", "resourcepacks", true, "HighResPack.zip", true},
		{"禁用数据包", "SpawnTweaks.zip.disabled", "datapacks", true, "SpawnTweaks.zip", false},
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
		"",                         // 空
		"../EssentialsX.jar",       // 路径遍历
		"sub/dir/plugin.jar",       // 含分隔符
		"sub\\dir\\plugin.jar",     // Windows 分隔符
		"config.yml",               // 非 jar
		"EssentialsX.jar.disabled", // 展示名不应带 .disabled
	}
	for _, n := range invalid {
		require.ErrorIs(t, validatePluginName(n), ErrInvalidPluginName, n)
	}
}

// TestNormalizeDir 验证目录归一：仅四个受控目录保留，其余回落 plugins。
func TestNormalizeDir(t *testing.T) {
	require.Equal(t, "plugins", normalizeDir(""))
	require.Equal(t, "plugins", normalizeDir("plugins"))
	require.Equal(t, "mods", normalizeDir("mods"))
	require.Equal(t, "resourcepacks", normalizeDir("resourcepacks"))
	require.Equal(t, "datapacks", normalizeDir("datapacks"))
	require.Equal(t, "plugins", normalizeDir("../etc")) // 非法目录回落
	require.Equal(t, "plugins", normalizeDir("worlds"))
}

func TestValidatePluginFileName_ByDir(t *testing.T) {
	require.NoError(t, validatePluginFileName("plugins", "EssentialsX.jar"))
	require.NoError(t, validatePluginFileName("mods", "fabric-api.jar"))
	require.NoError(t, validatePluginFileName("resourcepacks", "HighResPack.zip"))
	require.NoError(t, validatePluginFileName("datapacks", "SpawnTweaks.zip"))

	require.ErrorIs(t, validatePluginFileName("plugins", "HighResPack.zip"), ErrInvalidPluginName)
	require.ErrorIs(t, validatePluginFileName("resourcepacks", "EssentialsX.jar"), ErrInvalidPluginName)
	require.ErrorIs(t, validatePluginFileName("datapacks", "SpawnTweaks.zip.disabled"), ErrInvalidPluginName)
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

// streamingFakeWorkerOps 模拟支持 UploadFile 的新 Worker：记录流式帧并落入内存文件树。
type streamingFakeWorkerOps struct {
	*fakeWorkerOps
	streams []*fakePluginUploadStream
}

type fakePluginUploadStream struct {
	grpc.ClientStream
	owner *streamingFakeWorkerOps
	sent  []*workerpb.UploadFileChunk
}

func (s *fakePluginUploadStream) Send(c *workerpb.UploadFileChunk) error {
	s.sent = append(s.sent, c)
	return nil
}

func (s *fakePluginUploadStream) CloseAndRecv() (*workerpb.UploadFileResponse, error) {
	if len(s.sent) == 0 {
		return &workerpb.UploadFileResponse{Success: false, Error: "缺少首帧"}, nil
	}
	var n int64
	for _, c := range s.sent {
		n += int64(len(c.Content))
	}
	if parts := strings.SplitN(s.sent[0].Path, "/", 2); len(parts) == 2 {
		s.owner.put(parts[0], parts[1], n)
	}
	return &workerpb.UploadFileResponse{Success: true, BytesWritten: n}, nil
}

func (f *streamingFakeWorkerOps) UploadFile(_ context.Context, _ ...grpc.CallOption) (workerpb.WorkerService_UploadFileClient, error) {
	s := &fakePluginUploadStream{owner: f}
	f.streams = append(f.streams, s)
	return s, nil
}

// TestPluginService_Upload_StreamsToNewWorker 新 Worker：部署经 UploadFile 流式（FR-304），
// 不再落 WriteFile unary；首帧携带部署目标路径。
func TestPluginService_Upload_StreamsToNewWorker(t *testing.T) {
	db := newPluginTestDB(t)
	node := model.Node{UUID: "node-stream", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&node).Error)
	inst := model.Instance{UUID: "inst-stream", NodeID: node.ID, Name: "srv", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, StartCommand: "x", WorkDir: "/srv/srv"}
	require.NoError(t, db.Create(&inst).Error)

	fake := &streamingFakeWorkerOps{fakeWorkerOps: newFakeWorker()}
	svc := NewPluginService(db, cpgrpc.NewClientPool(), nil)
	svc.workerResolver = func(string) (pluginWorkerOps, bool) { return fake, true }

	_, err := svc.Upload(inst.ID, "plugins", "Streamy.jar", []byte("jar-bytes"))
	require.NoError(t, err)

	require.Empty(t, fake.writes, "新 Worker 不得走 WriteFile 回退")
	require.Len(t, fake.streams, 2, "应先零帧探测再真传")
	frames := fake.streams[1].sent
	require.NotEmpty(t, frames)
	require.Equal(t, "inst-stream", frames[0].InstanceUuid)
	require.Equal(t, "plugins/Streamy.jar", frames[0].Path)
}

// TestPluginService_List_AggregatesAndDetectsStatus 端到端覆盖 List：
// 聚合 plugins/+mods/、识别启用/禁用、剥离 .disabled、忽略非 jar 与子目录，目录不存在跳过。
func TestPluginService_List_AggregatesAndDetectsStatus(t *testing.T) {
	svc, fake, id := newPluginSvcWithFake(t, nil)
	fake.put("plugins", "EssentialsX.jar", 100)
	fake.put("plugins", "WorldEdit.jar.disabled", 200)
	fake.put("plugins", "config.yml", 5) // 非 jar，忽略
	fake.put("mods", "fabric-api.jar", 300)
	fake.put("resourcepacks", "HighResPack.zip", 400)
	fake.put("datapacks", "SpawnTweaks.zip.disabled", 500)
	fake.listErrDirs["mods"] = false

	list, err := svc.List(id)
	require.NoError(t, err)
	require.Len(t, list, 5)

	byName := map[string]PluginInfo{}
	for _, p := range list {
		byName[p.Name] = p
	}
	require.True(t, byName["EssentialsX.jar"].Enabled)
	require.Equal(t, "plugins", byName["EssentialsX.jar"].Dir)
	require.False(t, byName["WorldEdit.jar"].Enabled) // .disabled 已剥离且标记禁用
	require.True(t, byName["fabric-api.jar"].Enabled)
	require.Equal(t, "mods", byName["fabric-api.jar"].Dir)
	require.True(t, byName["HighResPack.zip"].Enabled)
	require.Equal(t, "resourcepacks", byName["HighResPack.zip"].Dir)
	require.False(t, byName["SpawnTweaks.zip"].Enabled)
	require.Equal(t, "datapacks", byName["SpawnTweaks.zip"].Dir)
}

func TestPluginService_List_MissingDirsEmpty(t *testing.T) {
	svc, fake, id := newPluginSvcWithFake(t, nil)
	fake.listErrDirs["plugins"] = true
	fake.listErrDirs["mods"] = true
	fake.listErrDirs["resourcepacks"] = true
	fake.listErrDirs["datapacks"] = true
	list, err := svc.List(id)
	require.NoError(t, err)
	require.Empty(t, list)
}

func TestPluginService_List_ParsesMetadata(t *testing.T) {
	svc, fake, id := newPluginSvcWithFake(t, nil)
	fake.putContent("plugins", "MetaPlugin.jar", zipBytes(t, map[string]string{
		"plugin.yml": "name: MetaPlugin\nversion: 1.2.3\nauthor: OpsTeam\ndepend: [Vault, PlaceholderAPI]\n",
	}))
	fake.putContent("mods", "FabricThing.jar", zipBytes(t, map[string]string{
		"fabric.mod.json": `{"version":"4.5.6","authors":[{"name":"FabricOps"}],"depends":{"fabricloader":">=0.15","minecraft":"1.20.4"}}`,
	}))
	fake.putContent("mods", "ForgeThing.jar", zipBytes(t, map[string]string{
		"META-INF/mods.toml": `
[[mods]]
modId="forge_thing"
version="9.8.7"
authors="ForgeOps"

[[dependencies.forge_thing]]
modId="forge"

[[dependencies.forge_thing]]
modId="minecraft"
`,
	}))
	fake.putContent("resourcepacks", "HighResPack.zip", zipBytes(t, map[string]string{
		"pack.mcmeta": `{"pack":{"pack_format":15,"description":"High resolution pack"}}`,
	}))

	list, err := svc.List(id)
	require.NoError(t, err)
	byName := map[string]PluginInfo{}
	for _, p := range list {
		byName[p.Name] = p
	}

	require.Equal(t, "1.2.3", byName["MetaPlugin.jar"].Version)
	require.Equal(t, "OpsTeam", byName["MetaPlugin.jar"].Author)
	require.ElementsMatch(t, []string{"Vault", "PlaceholderAPI"}, byName["MetaPlugin.jar"].Dependencies)
	require.Equal(t, "4.5.6", byName["FabricThing.jar"].Version)
	require.Equal(t, "FabricOps", byName["FabricThing.jar"].Author)
	require.ElementsMatch(t, []string{"fabricloader", "minecraft"}, byName["FabricThing.jar"].Dependencies)
	require.Equal(t, "9.8.7", byName["ForgeThing.jar"].Version)
	require.Equal(t, "ForgeOps", byName["ForgeThing.jar"].Author)
	require.ElementsMatch(t, []string{"forge", "minecraft"}, byName["ForgeThing.jar"].Dependencies)
	require.Equal(t, "pack_format 15", byName["HighResPack.zip"].Version)
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

func TestPluginService_Upload_ZipManagedDirs(t *testing.T) {
	cases := []struct {
		name string
		dir  string
		file string
		want string
	}{
		{"资源包目录", "resourcepacks", "HighResPack.zip", "resourcepacks/HighResPack.zip"},
		{"数据包目录", "datapacks", "SpawnTweaks.zip", "datapacks/SpawnTweaks.zip"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, fake, id := newPluginSvcWithFake(t, nil)

			_, err := svc.Upload(id, c.dir, c.file, []byte("zip-bytes"))
			require.NoError(t, err)
			require.Equal(t, []string{c.want}, fake.writes)
		})
	}
}

func TestPluginService_Upload_RejectsExistingFileUnlessOverwrite(t *testing.T) {
	svc, fake, id := newPluginSvcWithFake(t, nil)
	fake.put("plugins", "EssentialsX.jar", 1)
	fake.put("plugins", "WorldEdit.jar.disabled", 1)

	_, err := svc.Upload(id, "plugins", "EssentialsX.jar", []byte("jar-bytes"))
	require.ErrorIs(t, err, ErrPluginFileExists)

	_, err = svc.Upload(id, "plugins", "WorldEdit.jar", []byte("jar-bytes"))
	require.ErrorIs(t, err, ErrPluginFileExists)

	_, err = svc.Upload(id, "plugins", "EssentialsX.jar", []byte("jar-bytes"), WithPluginOverwrite(true))
	require.NoError(t, err)
	require.Equal(t, []string{"plugins/EssentialsX.jar"}, fake.writes)
}

func TestPluginService_BatchDeploy_WritesAssetToMultipleInstances(t *testing.T) {
	assetSvc := newAssetSvcForPlugin(t)
	asset, err := assetSvc.Ingest(strings.NewReader("jar-bytes"), IngestParams{
		Type:     model.AssetTypePlugin,
		Name:     "EssentialsX",
		Filename: "EssentialsX.jar",
	})
	require.NoError(t, err)
	svc, fake, firstID := newPluginSvcWithFake(t, assetSvc)
	var first model.Instance
	require.NoError(t, svc.db.First(&first, firstID).Error)
	second := model.Instance{UUID: "inst-second", NodeID: first.NodeID, Name: "srv2", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, StartCommand: "x", WorkDir: "/srv/srv2"}
	require.NoError(t, svc.db.Create(&second).Error)

	result, err := svc.BatchDeploy(PluginBatchDeployRequest{
		AssetIDs:  []uint{asset.ID},
		Target:    PluginBatchTarget{IDs: []uint{firstID, second.ID}},
		Overwrite: true,
	}, nil, false)

	require.NoError(t, err)
	require.Equal(t, 2, result.RequestedInstances)
	require.Equal(t, 1, result.RequestedAssets)
	require.Equal(t, 2, result.Succeeded)
	require.Zero(t, result.Failed)
	require.ElementsMatch(t, []string{"plugins/EssentialsX.jar", "plugins/EssentialsX.jar"}, fake.writes)
}

func TestPluginService_BatchDeploy_SkipsInvisibleTargets(t *testing.T) {
	assetSvc := newAssetSvcForPlugin(t)
	asset, err := assetSvc.Ingest(strings.NewReader("jar-bytes"), IngestParams{Type: model.AssetTypePlugin, Filename: "EssentialsX.jar"})
	require.NoError(t, err)
	svc, _, id := newPluginSvcWithFake(t, assetSvc)

	result, err := svc.BatchDeploy(PluginBatchDeployRequest{
		AssetIDs: []uint{asset.ID},
		Target:   PluginBatchTarget{IDs: []uint{id, id + 999}},
	}, []uint{id}, true)

	require.NoError(t, err)
	require.Equal(t, 2, result.RequestedInstances)
	require.Equal(t, 1, result.Succeeded)
	require.Equal(t, 1, result.Skipped)
}

func TestPluginService_BatchDeploy_AggregatesPartialWorkerFailure(t *testing.T) {
	assetSvc := newAssetSvcForPlugin(t)
	asset, err := assetSvc.Ingest(strings.NewReader("jar-bytes"), IngestParams{Type: model.AssetTypePlugin, Filename: "EssentialsX.jar"})
	require.NoError(t, err)
	svc, fake, firstID := newPluginSvcWithFake(t, assetSvc)
	var first model.Instance
	require.NoError(t, svc.db.First(&first, firstID).Error)
	second := model.Instance{UUID: "inst-second", NodeID: first.NodeID, Name: "srv2", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, StartCommand: "x", WorkDir: "/srv/srv2"}
	require.NoError(t, svc.db.Create(&second).Error)
	fake.writeErrors["inst-second/plugins/EssentialsX.jar"] = "磁盘空间不足"

	result, err := svc.BatchDeploy(PluginBatchDeployRequest{
		AssetIDs:  []uint{asset.ID},
		Target:    PluginBatchTarget{IDs: []uint{firstID, second.ID}},
		Overwrite: true,
	}, nil, false)

	require.NoError(t, err)
	require.Equal(t, 1, result.Succeeded)
	require.Equal(t, 1, result.Failed)
	require.Len(t, result.Results, 2)
	require.Contains(t, result.Results, PluginBatchDeployItem{InstanceID: firstID, AssetID: asset.ID, OK: true})
	var failed PluginBatchDeployItem
	for _, item := range result.Results {
		if !item.OK {
			failed = item
		}
	}
	require.Equal(t, second.ID, failed.InstanceID)
	require.Contains(t, failed.Error, "磁盘空间不足")
}

func TestPluginService_BatchDeploy_RejectsExistingFileUnlessOverwrite(t *testing.T) {
	assetSvc := newAssetSvcForPlugin(t)
	asset, err := assetSvc.Ingest(strings.NewReader("jar-bytes"), IngestParams{Type: model.AssetTypePlugin, Filename: "EssentialsX.jar"})
	require.NoError(t, err)
	svc, fake, id := newPluginSvcWithFake(t, assetSvc)
	fake.put("plugins", "EssentialsX.jar", 1)

	result, err := svc.BatchDeploy(PluginBatchDeployRequest{
		AssetIDs: []uint{asset.ID},
		Target:   PluginBatchTarget{IDs: []uint{id}},
	}, nil, false)
	require.NoError(t, err)
	require.Equal(t, 1, result.Failed)
	require.Contains(t, result.Results[0].Error, "文件已存在")

	result, err = svc.BatchDeploy(PluginBatchDeployRequest{
		AssetIDs:  []uint{asset.ID},
		Target:    PluginBatchTarget{IDs: []uint{id}},
		Overwrite: true,
	}, nil, false)
	require.NoError(t, err)
	require.Equal(t, 1, result.Succeeded)
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

func TestPluginService_Toggle_ZipManagedDir(t *testing.T) {
	svc, fake, id := newPluginSvcWithFake(t, nil)
	fake.put("resourcepacks", "HighResPack.zip", 1)

	enabled, err := svc.Toggle(id, "resourcepacks", "HighResPack.zip")
	require.NoError(t, err)
	require.False(t, enabled)
	require.Equal(t, [2]string{"resourcepacks/HighResPack.zip", "resourcepacks/HighResPack.zip.disabled"}, fake.renames[0])
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

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}
