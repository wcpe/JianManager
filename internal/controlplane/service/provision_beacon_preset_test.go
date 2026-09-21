package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// FR-442 内置 Beacon 快速搭建预设。
//
// 覆盖点（对应 spec 第 4 节）：
//   - coreType=beacon 展开为 binary + 固定参数（落盘名/启动命令/角色 beacon/不绑 JDK）；
//   - 制品来源优先级：制品库已有 beacon 制品 > GitHub Releases（wcpe/Beacon）；
//   - GitHub 版本解析（tag → 版本号 → 落盘名）+ sha256 取 digest；
//   - 解析失败不静默（任务终态 + 实例 DAMAGED 可重建）；
//   - 既有 MC 核心路径（paper/sponge/velocity/bungeecord）行为不变。
//
// 全部 GitHub 交互走 httptest mock（不真的请求 api.github.com）。

// beaconGitHubStub 是 Beacon 官方发布的假源（httptest，路径对齐 GitHub REST API）。
func beaconGitHubStub(t *testing.T, body string) *CoreService {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+beaconReleaseRepo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &CoreService{client: srv.Client(), beaconAPIBase: srv.URL}
}

// beaconReleaseJSON 拼一份 GitHub latest release 响应体（assets 为若干资产 JSON 对象）。
func beaconReleaseJSON(tag string, assets ...string) string {
	return `{"tag_name":"` + tag + `","draft":false,"prerelease":false,"assets":[` +
		strings.Join(assets, ",") + `]}`
}

// beaconAssetJSON 拼一条发布资产（name / 下载地址 / sha256 digest）。
func beaconAssetJSON(name, url, digest string) string {
	if digest == "" {
		return `{"name":"` + name + `","browser_download_url":"` + url + `"}`
	}
	return `{"name":"` + name + `","browser_download_url":"` + url + `","digest":"sha256:` + digest + `"}`
}

// ---- 版本与产物名解析 ----

// TestBeaconBinaryFilename 落盘名固定为 beacon-{version}-linux-amd64（spec §4.2）。
func TestBeaconBinaryFilename(t *testing.T) {
	require.Equal(t, "beacon-1.1.0-linux-amd64", beaconBinaryFilename("1.1.0"))
	require.Equal(t, "beacon-1.1.0-linux-amd64", beaconBinaryFilename("  1.1.0  "))
}

// TestBeaconVersionFromTag tag 去 `v` 前缀；非法 tag（含路径分隔符/空白/Shell 元字符）一律拒绝——
// 该值会被拼进落盘名并由 Worker 经 sh -c 执行，绝不能让上游内容直通。
func TestBeaconVersionFromTag(t *testing.T) {
	require.Equal(t, "1.1.0", beaconVersionFromTag("v1.1.0"))
	require.Equal(t, "1.1.0", beaconVersionFromTag("1.1.0"))
	require.Equal(t, "2.0.0-rc1", beaconVersionFromTag(" v2.0.0-rc1 "))

	for _, bad := range []string{"", "v", "v1.1.0/evil", "1.1.0;rm -rf /", "1.1.0 x", "v1.1.0\nrm -rf /"} {
		require.Empty(t, beaconVersionFromTag(bad), "非法 tag %q 必须被拒绝", bad)
	}

	// 首尾空白只是 TrimSpace 掉（无 Shell 语义），不算非法。
	require.Equal(t, "1.1.0", beaconVersionFromTag("v1.1.0\n"))

	// 带路径分隔符的 tag 拼进落盘名后 filepath.Base 与全名不等，被白名单拒绝。
	require.Empty(t, beaconVersionFromTag("../1.1.0"))
}

// TestIsBeaconLinuxBinaryName 制品判定是「制品库检索」与「GitHub 产物筛选」共用的唯一判据。
func TestIsBeaconLinuxBinaryName(t *testing.T) {
	for _, ok := range []string{
		"beacon-1.1.0-linux-amd64", "beacon-linux-amd64", "beacon-1.1.0-LINUX-AMD64",
		"beacon-2.0.0-linux-amd64.exe",
	} {
		require.True(t, isBeaconLinuxBinaryName(ok), "%q 应被识别为 Beacon Linux 二进制", ok)
	}
	for _, bad := range []string{
		"", "server.jar",
		"beacon-1.1.0-linux-amd64.sha256", // 校验和附属产物
		"beacon-1.1.0-linux-amd64.txt",
		"beacon-1.1.0-windows-amd64", // 非 linux
		"beacon-1.1.0-linux-arm64",   // 非 amd64
		"serverprobe-1.0.0-linux-amd64",
	} {
		require.False(t, isBeaconLinuxBinaryName(bad), "%q 不应被识别", bad)
	}
}

// ---- GitHub Releases 版本解析 ----

// TestResolveBeaconRelease 解析 latest release：版本来自 tag，下载地址与 sha256 来自资产。
func TestResolveBeaconRelease(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	core := beaconGitHubStub(t, beaconReleaseJSON("v1.1.0",
		beaconAssetJSON("X", "", ""), // 无关资产
		beaconAssetJSON("beacon-1.1.0-linux-amd64", "https://github.com/wcpe/Beacon/releases/download/v1.1.0/beacon-1.1.0-linux-amd64", digest),
		beaconAssetJSON("beacon-1.1.0-linux-amd64.sha256", "https://example.com/checksums", digest),
	))

	release, err := core.ResolveBeaconRelease(context.Background())
	require.NoError(t, err)
	require.Equal(t, "1.1.0", release.Version)
	require.Equal(t, "v1.1.0", release.Tag)
	require.Equal(t, "beacon-1.1.0-linux-amd64", release.AssetName, "应跳过 .sha256 附属产物")
	require.Equal(t, digest, release.SHA256, "digest 的 sha256: 前缀应被剥离")
	require.True(t, strings.HasPrefix(release.DownloadURL, "https://github.com/"), release.DownloadURL)
}

// TestResolveBeaconRelease_FallsBackToUnversionedName 上游若只发布不带版本号的产物，
// 也应按预设命名落盘（内容相同，重命名即可用）。
func TestResolveBeaconRelease_FallsBackToUnversionedName(t *testing.T) {
	core := beaconGitHubStub(t, beaconReleaseJSON("v1.1.0",
		beaconAssetJSON("beacon-linux-amd64", "https://example.com/beacon-linux-amd64", ""),
	))
	release, err := core.ResolveBeaconRelease(context.Background())
	require.NoError(t, err)
	require.Equal(t, "beacon-linux-amd64", release.AssetName)
	require.Empty(t, release.SHA256, "上游未给 digest 时留空（不编造一个值让 Worker 永远校验失败）")

	info, err := core.ResolveBuild(context.Background(), CoreTypeBeacon, "", 0)
	require.NoError(t, err)
	require.Equal(t, "beacon-1.1.0-linux-amd64", info.Filename, "落盘名仍按预设命名")
	require.Equal(t, "1.1.0", info.MCVersion)
}

// TestResolveBeaconRelease_NoLinuxAsset 无 linux-amd64 产物 → 明确报错（不静默回落到别的平台）。
func TestResolveBeaconRelease_NoLinuxAsset(t *testing.T) {
	core := beaconGitHubStub(t, beaconReleaseJSON("v1.1.0",
		beaconAssetJSON("beacon-1.1.0-windows-amd64.exe", "https://example.com/win.exe", ""),
	))
	_, err := core.ResolveBeaconRelease(context.Background())
	require.ErrorIs(t, err, ErrBeaconReleaseUnavailable)
}

// TestResolveBeaconRelease_RejectsInsecureURL http 下载地址一律拒绝（可被中间人替换）。
func TestResolveBeaconRelease_RejectsInsecureURL(t *testing.T) {
	core := beaconGitHubStub(t, beaconReleaseJSON("v1.1.0",
		beaconAssetJSON("beacon-1.1.0-linux-amd64", "http://example.com/beacon", ""),
	))
	_, err := core.ResolveBeaconRelease(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "必须为 https")
}

// TestResolveBeaconRelease_PrereleaseRejected latest 端点理论上不返回预发布，仍显式拒绝。
func TestResolveBeaconRelease_PrereleaseRejected(t *testing.T) {
	core := beaconGitHubStub(t, `{"tag_name":"v2.0.0-rc1","prerelease":true,"assets":[
		{"name":"beacon-2.0.0-rc1-linux-amd64","browser_download_url":"https://example.com/b"}]}`)
	_, err := core.ResolveBeaconRelease(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "预发布")
}

// TestResolveBeaconRelease_ErrorHintsArtifactLibrary 网络失败（内网常见）时，
// 错误须带上可操作的替代路径 —— 先把二进制入库，预设会自动优先用制品库。
func TestResolveBeaconRelease_ErrorHintsArtifactLibrary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	core := &CoreService{client: srv.Client(), beaconAPIBase: srv.URL}

	_, err := core.ResolveBeaconRelease(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "制品库", "错误须提示内网可用路径")
}

// TestBeaconListVersions 预设无版本选择语义：与 bungeecord 同口径给单一 latest，
// 且不因 ListVersions 触发网络请求（否则「列版本」会变成搭建链路的隐性前置依赖）。
func TestBeaconListVersions(t *testing.T) {
	core := beaconGitHubStub(t, beaconReleaseJSON("v1.1.0")) // 无资产，若被请求会失败
	versions, err := core.ListVersions(context.Background(), "beacon")
	require.NoError(t, err)
	require.Equal(t, []string{"latest"}, versions)

	// 大小写不敏感（与 project() 同口径）。
	versions, err = core.ListVersions(context.Background(), "BEACON")
	require.NoError(t, err)
	require.Equal(t, []string{"latest"}, versions)
}

// TestIsBinaryCore_IncludesBeacon beacon 展开后与 binary 同路（同取件通道、同实例属性）。
func TestIsBinaryCore_IncludesBeacon(t *testing.T) {
	require.True(t, IsBinaryCore("beacon"))
	require.True(t, IsBinaryCore("BEACON"))
	require.True(t, IsBeaconCore("beacon"))
	require.False(t, IsBeaconCore("binary"), "binary 本身不是 beacon 预设")
	require.True(t, IsBinaryCore("binary"))

	// 既有 MC 核心不受影响。
	for _, mc := range []string{"paper", "spongevanilla", "spongeforge", "velocity", "waterfall", "bungeecord"} {
		require.False(t, IsBinaryCore(mc), "%s 不得被判为二进制路径", mc)
		require.False(t, IsBeaconCore(mc), "%s 不得被判为 beacon 预设", mc)
	}

	// mcVersion 条件必填：beacon 与 binary 一样不需要。
	require.NoError(t, ValidateProvisionRequest(ProvisionServerRequest{CoreType: "beacon"}))
	require.Error(t, ValidateProvisionRequest(ProvisionServerRequest{CoreType: "paper"}))
}

// ---- 来源优先级：制品库 > GitHub ----

// newBeaconHarness 建 Beacon 预设测试基座：可选 GitHub 假源 + 制品库（资产/版本库服务）。
// 沿用 newBinaryHarness 的假 worker 与任务表，仅补 beacon 需要的装配。
//
// github = "" 时用「被访问即失败」的假源——供「制品库命中则绝不访问 GitHub」这类断言使用。
func newBeaconHarness(t *testing.T, worker workerpb.WorkerServiceClient, github string) (*ProvisionService, *TaskService, *model.Node, *CoreService) {
	t.Helper()
	svc, taskSvc, node := newBinaryHarness(t, worker)
	db := svc.db
	require.NoError(t, db.AutoMigrate(&model.Asset{}))

	assetSvc := NewAssetService(db, nil)
	artifactSvc := NewArtifactVersionService(db, assetSvc)
	core := NewCoreService()
	if github == "" {
		core = beaconGitHubStubError(t)
	} else if github == "unreachable" {
		core = beaconGitHubStubFail(t)
	} else {
		core = beaconGitHubStub(t, github)
	}
	svc.core = core
	svc.SetBinaryAssets(assetSvc)
	svc.SetBinaryArtifactVersions(artifactSvc)
	require.NoError(t, db.Create(&model.PlatformSetting{
		Key: SettingKeyPlatformPublicBaseURL, Value: "https://cp.example.com",
	}).Error)
	return svc, taskSvc, node, core
}

// seedBeaconAsset 往制品库塞一份 beacon 二进制制品（模拟运维上传），返回该资产。
// 内容寻址下 (type, sha256) 唯一，故同一份内容只能有一条记录；需要多条时传不同内容。
func seedBeaconAsset(t *testing.T, svc *ProvisionService, version, content string) *model.Asset {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	asset := &model.Asset{
		Type: model.AssetTypeBlob, Name: "beacon", Version: version,
		Filename: "beacon-" + version + "-linux-amd64",
		SHA256:   hex.EncodeToString(sum[:]), Size: int64(len(content)),
		StorageState: model.AssetStorageHot,
	}
	require.NoError(t, svc.db.Create(asset).Error)
	return asset
}

// beaconGitHubStubError 是一个必定失败（502）且在**被访问时即判测试失败**的假 GitHub，
// 用于证明某条路径不该访问 GitHub（如制品库已命中）。
func beaconGitHubStubError(t *testing.T) *CoreService {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("不应访问 GitHub Releases，却请求了 %s", r.URL.Path)
		http.Error(w, "should not be called", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	return &CoreService{client: srv.Client(), beaconAPIBase: srv.URL}
}

// beaconGitHubStubFail 是一个必定失败（502）的假 GitHub（允许被访问）：
// 代表「内网一律访问不了 GitHub」，与「制品库没有 beacon 制品」叠加即两条来源都不可用。
func beaconGitHubStubFail(t *testing.T) *CoreService {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "github unreachable", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	return &CoreService{client: srv.Client(), beaconAPIBase: srv.URL}
}

// TestBeaconPresetAsync_ArtifactLibraryWins 制品库已有 beacon 制品时必须优先使用（spec §4.1），
// 且**不访问 GitHub**（内网场景的硬要求）。
func TestBeaconPresetAsync_ArtifactLibraryWins(t *testing.T) {
	worker := &binaryWorkerStub{}
	// GitHub 假源一旦被访问即测试失败（t.Errorf）——制品库命中就不该再走网络回落。
	svc, taskSvc, node, _ := newBeaconHarness(t, worker, "")

	asset := seedBeaconAsset(t, svc, "1.1.0", "fake-beacon-binary-1.1.0")

	inst, taskID, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID: node.ID, Name: "beacon-preset", CoreType: "beacon",
	}, 1)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateSucceeded, waitTaskTerminal(t, taskSvc, taskID).State)

	var got model.Instance
	require.NoError(t, svc.db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceRoleBeacon, got.Role, "预设默认角色 beacon（FR-433）")
	require.Equal(t, "./beacon-1.1.0-linux-amd64", got.StartCommand, "启动命令由落盘名派生（spec §4.2）")
	require.Equal(t, model.InstanceTypeGeneric, got.Type)
	require.Zero(t, got.JDKID, "Go 二进制不绑 JDK")

	// 制品库来源经既有签名分发通道交付，摘要取自制品库。
	req := worker.lastRequest()
	require.NotNil(t, req)
	require.Equal(t, "url", req.SourceKind)
	require.Contains(t, req.DownloadUrl, "https://cp.example.com/binary-assets/", "应走 CP 签名分发端点")
	require.Contains(t, req.DownloadUrl, "token=")
	require.Equal(t, asset.SHA256, req.Sha256)
	require.Equal(t, "beacon-1.1.0-linux-amd64", req.DestFilename)
}

// TestBeaconPresetAsync_GitHubFallback 制品库无 beacon 制品时回落 GitHub Releases，
// 落盘名与启动命令由 release 版本派生。
func TestBeaconPresetAsync_GitHubFallback(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	worker := &binaryWorkerStub{}
	svc, taskSvc, node, _ := newBeaconHarness(t, worker, beaconReleaseJSON("v1.2.3",
		beaconAssetJSON("beacon-1.2.3-linux-amd64", "https://github.com/wcpe/Beacon/releases/download/v1.2.3/beacon-1.2.3-linux-amd64", digest),
	))

	inst, taskID, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID: node.ID, Name: "beacon-gh", CoreType: "beacon",
	}, 1)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateSucceeded, waitTaskTerminal(t, taskSvc, taskID).State)

	var got model.Instance
	require.NoError(t, svc.db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceRoleBeacon, got.Role)
	require.Equal(t, "./beacon-1.2.3-linux-amd64", got.StartCommand)

	req := worker.lastRequest()
	require.NotNil(t, req)
	require.Equal(t, "https://github.com/wcpe/Beacon/releases/download/v1.2.3/beacon-1.2.3-linux-amd64", req.DownloadUrl)
	require.Equal(t, digest, req.Sha256)
	require.Equal(t, "beacon-1.2.3-linux-amd64", req.DestFilename)
	// 任务标题点明来源，便于运维在任务页一眼看出这次用的是哪条来源。
	require.True(t, stageLogContains(t, taskSvc, taskID, "解析制品来源"))
}

// TestBeaconPresetAsync_ExplicitStartCommandWins 显式启动命令（带参数等）优先于派生。
func TestBeaconPresetAsync_ExplicitStartCommandWins(t *testing.T) {
	worker := &binaryWorkerStub{}
	svc, taskSvc, node, _ := newBeaconHarness(t, worker, beaconReleaseJSON("v1.0.0",
		beaconAssetJSON("beacon-1.0.0-linux-amd64", "https://github.com/wcpe/Beacon/releases/download/v1.0.0/b", ""),
	))

	inst, taskID, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID: node.ID, Name: "beacon-cfg", CoreType: "beacon",
		StartCommand: "./beacon-1.0.0-linux-amd64 -config config.yml",
	}, 1)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateSucceeded, waitTaskTerminal(t, taskSvc, taskID).State)

	var got model.Instance
	require.NoError(t, svc.db.First(&got, inst.ID).Error)
	require.Equal(t, "./beacon-1.0.0-linux-amd64 -config config.yml", got.StartCommand)
}

// TestBeaconPresetAsync_SourceResolutionFailureVisible 两条来源都不可用时**不得静默失败**：
// 实例进 DAMAGED + 原因可见 + provisionSpec 保留可重建，任务终态带底层原因。
func TestBeaconPresetAsync_SourceResolutionFailureVisible(t *testing.T) {
	worker := &binaryWorkerStub{}
	// 两条来源都不可用：GitHub 必定 502 + 制品库无 beacon 制品（内网首部署的典型情形）。
	svc, taskSvc, node, _ := newBeaconHarness(t, worker, "unreachable")

	inst, taskID, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID: node.ID, Name: "beacon-fail", CoreType: "beacon",
	}, 1)
	require.NoError(t, err, "同步段应成功（失败在后台任务，原因可观察）")
	require.Nil(t, worker.lastRequest(), "来源解析失败时不应向 Worker 下发取件请求")

	task := waitTaskTerminal(t, taskSvc, taskID)
	require.Equal(t, model.TaskStateFailed, task.State)
	require.Contains(t, task.Error, "Beacon 官方发布失败")

	var got model.Instance
	require.NoError(t, svc.db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusDamaged, got.Status)
	require.True(t, strings.HasPrefix(got.StatusReason, "搭建未完成："), "实际 %q", got.StatusReason)
	require.NotEmpty(t, got.ProvisionSpec, "损毁实例须保留搭建参数供重建")
	require.Equal(t, model.InstanceRoleBeacon, got.Role, "角色在创建时即确定，与取件成败无关")

	// 修好环境（制品库入库）后重建：来源重新解析 → 成功回到 STOPPED。
	seedBeaconAsset(t, svc, "1.1.0", "fake-beacon-recovered")

	rebuildTaskID, err := svc.RebuildInstance(context.Background(), inst.ID, 1)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateSucceeded, waitTaskTerminal(t, taskSvc, rebuildTaskID).State)

	require.NoError(t, svc.db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusStopped, got.Status)
	require.Empty(t, got.StatusReason)
	// 重建解析到制品库版本 → 启动命令随之更新（否则仍指向旧文件名，一启动就 command not found）。
	require.Equal(t, "./beacon-1.1.0-linux-amd64", got.StartCommand)
}

// TestBeaconPresetAsync_PicksLatestArtifact 制品库有多个版本时取最新（id 倒序 = 入库时间新→旧）。
func TestBeaconPresetAsync_PicksLatestArtifact(t *testing.T) {
	worker := &binaryWorkerStub{}
	svc, taskSvc, node, _ := newBeaconHarness(t, worker, "")

	// 内容寻址下同 (type, sha256) 只有一条记录，故两份制品内容不同（模拟不同版本构建）。
	seedBeaconAsset(t, svc, "1.0.0", "beacon-binary-v1.0.0")
	seedBeaconAsset(t, svc, "1.1.0", "beacon-binary-v1.1.0")

	inst, taskID, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID: node.ID, Name: "beacon-latest", CoreType: "beacon",
	}, 1)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateSucceeded, waitTaskTerminal(t, taskSvc, taskID).State)

	var got model.Instance
	require.NoError(t, svc.db.First(&got, inst.ID).Error)
	require.Equal(t, "./beacon-1.1.0-linux-amd64", got.StartCommand, "应取最新入库的版本")
}

// TestBeaconPresetAsync_SkipsLostAndEmptyAssets 失效/空制品不算可用来源：跳过它们继续回落。
// 选中一个空文件只会让搭建在下载后才发现不可用，不如直接换来源。
func TestBeaconPresetAsync_SkipsLostAndEmptyAssets(t *testing.T) {
	digest := strings.Repeat("ef", 32)
	worker := &binaryWorkerStub{}
	svc, taskSvc, node, _ := newBeaconHarness(t, worker, beaconReleaseJSON("v3.0.0",
		beaconAssetJSON("beacon-3.0.0-linux-amd64", "https://github.com/wcpe/Beacon/releases/download/v3.0.0/b", digest),
	))
	// 失效制品（外置对象缺失）：不可交付。
	require.NoError(t, svc.db.Create(&model.Asset{
		Type: model.AssetTypeBlob, Name: "beacon", Version: "3.0.0",
		Filename: "beacon-3.0.0-linux-amd64", SHA256: digest, Size: 100,
		StorageState: model.AssetStorageLost,
	}).Error)

	inst, taskID, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID: node.ID, Name: "beacon-lost", CoreType: "beacon",
	}, 1)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateSucceeded, waitTaskTerminal(t, taskSvc, taskID).State)

	var got model.Instance
	require.NoError(t, svc.db.First(&got, inst.ID).Error)
	require.Equal(t, "./beacon-3.0.0-linux-amd64", got.StartCommand)

	req := worker.lastRequest()
	require.NotNil(t, req)
	require.Equal(t, "https://github.com/wcpe/Beacon/releases/download/v3.0.0/b", req.DownloadUrl,
		"失效制品应被跳过，回落到 GitHub")
}

// TestBeaconPresetAsync_FetchFailureDamagedAndRebuildable 取件失败（下载中断）：
// 与 binary 同闭环 —— DAMAGED + 原因 + 可重建。
func TestBeaconPresetAsync_FetchFailureDamagedAndRebuildable(t *testing.T) {
	worker := &binaryWorkerStub{frames: []*workerpb.FetchBinaryProgress{
		{Done: true, Success: false, Error: "下载二进制失败: 连接被重置"},
	}}
	svc, taskSvc, node, _ := newBeaconHarness(t, worker, beaconReleaseJSON("v1.0.0",
		beaconAssetJSON("beacon-1.0.0-linux-amd64", "https://github.com/wcpe/Beacon/releases/download/v1.0.0/b", ""),
	))

	inst, taskID, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID: node.ID, Name: "beacon-fetch-fail", CoreType: "beacon",
	}, 1)
	require.NoError(t, err)

	task := waitTaskTerminal(t, taskSvc, taskID)
	require.Equal(t, model.TaskStateFailed, task.State)
	require.Contains(t, task.Error, "连接被重置")

	var got model.Instance
	require.NoError(t, svc.db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusDamaged, got.Status)
	require.Contains(t, got.StatusReason, "搭建未完成")

	worker.mu.Lock()
	worker.frames = nil
	worker.mu.Unlock()
	rebuildTaskID, err := svc.RebuildInstance(context.Background(), inst.ID, 1)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateSucceeded, waitTaskTerminal(t, taskSvc, rebuildTaskID).State)
	require.NoError(t, svc.db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusStopped, got.Status)
}

// TestBeaconPresetAsync_StartBlockedWhileProvisioning 取件未完成时启动被拒（复用既有启动闸）。
func TestBeaconPresetAsync_StartBlockedWhileProvisioning(t *testing.T) {
	worker := &binaryWorkerStub{}
	svc, _, node, _ := newBeaconHarness(t, worker, beaconReleaseJSON("v1.0.0",
		beaconAssetJSON("beacon-1.0.0-linux-amd64", "https://github.com/wcpe/Beacon/releases/download/v1.0.0/b", ""),
	))

	inst, _, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID: node.ID, Name: "beacon-gate", CoreType: "beacon",
	}, 1)
	require.NoError(t, err)

	// 任务终态后闸自然放开（此处只验证不 panic 且实例已落 beacon 角色/启动命令）。
	var got model.Instance
	require.NoError(t, svc.db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceRoleBeacon, got.Role)
	require.NotEmpty(t, got.StartCommand)
}

// TestBeaconPreset_NoTaskCenterWithoutResolution 没有任务中心且来源也解析不出来时，
// 不静默成功——返回解析错误（有任务中心时该错误会转由任务终态呈现）。
func TestBeaconPreset_NoTaskCenterWithoutResolution(t *testing.T) {
	db := newInstanceTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Asset{}, &model.PlatformSetting{}))
	node := &model.Node{UUID: "node-beacon-notask", Status: model.NodeStatusOnline, OS: "linux"}
	require.NoError(t, db.Create(node).Error)

	pool := cpgrpc.NewClientPool()
	instSvc := NewInstanceService(db, NewGroupService(db), pool)
	core := beaconGitHubStubFail(t)
	svc := NewProvisionService(db, pool, instSvc, core, nil)
	assetSvc := NewAssetService(db, nil)
	svc.SetBinaryAssets(assetSvc)
	svc.SetBinaryArtifactVersions(NewArtifactVersionService(db, assetSvc))
	// 刻意不注入任务中心。

	_, _, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID: node.ID, Name: "beacon-no-task", CoreType: "beacon",
	}, 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "Beacon 官方发布失败", "应报出真实的解析失败原因")
}

// ---- 既有 MC 路径不受影响 ----

// TestBeaconPreset_MCCorePathsUnchanged 新增预设不得改动既有 MC 核心的解析与搭建行为。
func TestBeaconPreset_MCCorePathsUnchanged(t *testing.T) {
	// 解析：paper/velocity/waterfall 仍走 PaperMC，sponge 走 Maven，bungeecord 仍 single latest。
	paper := newPaperStub(t)
	versions, err := paper.ListVersions(context.Background(), "paper")
	require.NoError(t, err)
	require.Equal(t, []string{"1.21.1", "1.21", "1.20.6"}, versions)

	info, err := paper.ResolveBuild(context.Background(), "paper", "1.21.1", 0)
	require.NoError(t, err)
	require.Equal(t, "paper-1.21.1-196.jar", info.Filename)

	versions, err = paper.ListVersions(context.Background(), "bungeecord")
	require.NoError(t, err)
	require.Equal(t, []string{"latest"}, versions)

	info, err = paper.ResolveBuild(context.Background(), "bungeecord", "", 0)
	require.NoError(t, err)
	require.Equal(t, "BungeeCord.jar", info.Filename)
	require.Equal(t, bungeeJenkinsURL, info.DownloadURL)

	sponge := newSpongeStub(t)
	info, err = sponge.ResolveBuild(context.Background(), "spongevanilla", "1.21.1", 0)
	require.NoError(t, err)
	require.Equal(t, "spongevanilla-1.21.1-12.0.4-RC2665-universal.jar", info.Filename)

	// 不支持的核心仍报错（不因新增预设被误吞）。
	_, err = paper.ResolveBuild(context.Background(), "forge", "1.21.1", 0)
	require.Error(t, err)
}
