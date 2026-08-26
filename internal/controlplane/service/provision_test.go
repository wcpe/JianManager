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
	"google.golang.org/grpc"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

func TestLaunchSpecForProvision_SpongeForgeKeepsMinecraftJavaLaunchModel(t *testing.T) {
	core := &CoreInfo{Runtime: &CoreRuntimeInfo{Distribution: "spongeforge", ForgeVersion: "1.21.1-52.1.5", LaunchJar: "forge-1.21.1-52.1.5-server.jar"}}
	spec, err := launchSpecForProvision(ProvisionServerRequest{MemoryMb: 4096, JvmArgs: []string{"-XX:+UseG1GC"}}, core, "linux")
	require.NoError(t, err)
	require.Equal(t, "forge-1.21.1-52.1.5-server.jar", spec.CoreJar)
	require.Equal(t, []string{"user_jvm_args.txt", "libraries/net/minecraftforge/forge/1.21.1-52.1.5/unix_args.txt"}, spec.JavaArgFiles)
	require.Equal(t, 4096, spec.MemoryMb)
	require.Equal(t, []string{"-XX:+UseG1GC"}, spec.JvmArgs)

	_, err = launchSpecForProvision(ProvisionServerRequest{}, &CoreInfo{Runtime: &CoreRuntimeInfo{Distribution: "spongeforge"}}, "linux")
	require.Error(t, err)
}

type fakeProvisionWorker struct {
	workerpb.WorkerServiceClient
	create   *workerpb.CreateInstanceRequest
	download *workerpb.DownloadCoreRequest
	install  *workerpb.InstallForgeServerRequest
	probe    *workerpb.DeployServerProbeRequest
	writes   map[string]string
}

func (f *fakeProvisionWorker) CreateInstance(_ context.Context, in *workerpb.CreateInstanceRequest, _ ...grpc.CallOption) (*workerpb.CreateInstanceResponse, error) {
	f.create = in
	return &workerpb.CreateInstanceResponse{Success: true}, nil
}

func (f *fakeProvisionWorker) DownloadCore(_ context.Context, in *workerpb.DownloadCoreRequest, _ ...grpc.CallOption) (*workerpb.DownloadCoreResponse, error) {
	f.download = in
	return &workerpb.DownloadCoreResponse{Success: true}, nil
}

func (f *fakeProvisionWorker) InstallForgeServer(_ context.Context, in *workerpb.InstallForgeServerRequest, _ ...grpc.CallOption) (*workerpb.InstallForgeServerResponse, error) {
	f.install = in
	return &workerpb.InstallForgeServerResponse{Success: true, LaunchJar: in.LaunchJar}, nil
}

// DeployServerProbe 桩：本地存在内嵌探针制品时 provisionOnWorker 会走探针部署分支，
// 桩返回成功以免命中内嵌 WorkerServiceClient 接口的 nil 方法而空指针 panic
// （干净检出/CI 无内嵌 jar 时该分支被跳过，不会走到这里）。
func (f *fakeProvisionWorker) DeployServerProbe(_ context.Context, in *workerpb.DeployServerProbeRequest, _ ...grpc.CallOption) (*workerpb.DeployServerProbeResponse, error) {
	f.probe = in
	return &workerpb.DeployServerProbeResponse{Success: true}, nil
}

func (f *fakeProvisionWorker) WriteConfig(_ context.Context, in *workerpb.WriteConfigRequest, _ ...grpc.CallOption) (*workerpb.WriteConfigResponse, error) {
	if f.writes == nil {
		f.writes = map[string]string{}
	}
	f.writes[in.Path] = in.Content
	return &workerpb.WriteConfigResponse{Success: true}, nil
}

func TestBuildServerProperties(t *testing.T) {
	props := buildServerProperties(25566, 25566, false)
	for _, want := range []string{
		"server-port=25566",
		"online-mode=false",
		"enable-query=true",
		"query.port=25566",
	} {
		if !strings.Contains(props, want) {
			t.Fatalf("server.properties 缺少 %q:\n%s", want, props)
		}
	}
	// online-mode=true 透传（独立正版服）
	if on := buildServerProperties(25566, 25566, true); !strings.Contains(on, "online-mode=true") {
		t.Fatalf("online-mode=true 未透传:\n%s", on)
	}
}

func TestProvisionOnWorker_DeploysResolvedProbeByControlPlaneURL(t *testing.T) {
	artifacts, assets := newArtifactVersionService(t)
	pkg, source, err := artifacts.EnsureDefaultServerProbe()
	require.NoError(t, err)
	jar := []byte("serverprobe-0.2.0")
	sum := sha256.Sum256(jar)
	asset, err := assets.Ingest(strings.NewReader(string(jar)), IngestParams{
		Type:     model.AssetTypeServerProbe,
		Name:     "ServerProbe",
		Version:  "0.2.0",
		Filename: "ServerProbe-0.2.0.jar",
	})
	require.NoError(t, err)
	version := &model.ArtifactVersion{
		PackageID: pkg.ID, SourceID: source.ID, Version: "0.2.0", ReleaseRef: "v0.2.0",
		AssetName: "ServerProbe-0.2.0.jar", ExpectedSHA256: hex.EncodeToString(sum[:]),
		SourceURL: "https://github.example/ServerProbe-0.2.0.jar", AssetID: asset.ID,
	}
	require.NoError(t, artifacts.db.Create(version).Error)
	require.NoError(t, artifacts.SetPackageDefaultVersion(pkg.ID, version.ID))

	node := &model.Node{UUID: "probe-node", Name: "probe-node", Host: "127.0.0.1", Secret: "secret", Status: model.NodeStatusOnline}
	require.NoError(t, artifacts.db.Create(node).Error)
	inst := &model.Instance{Name: "paper", NodeID: node.ID, Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDirect, StartCommand: "java", ProbePort: 29940}
	require.NoError(t, artifacts.db.Create(inst).Error)

	pool := cpgrpc.NewClientPool()
	worker := &fakeProvisionWorker{}
	pool.SetWorkerClientForTest(node.UUID, worker)
	svc := NewProvisionService(artifacts.db, pool, nil, nil, nil, artifacts)
	err = svc.deployServerProbe(context.Background(), inst, node, "https://cp.example.test/base")
	require.NoError(t, err)
	require.NotNil(t, worker.probe)
	require.Empty(t, worker.probe.Jar)
	require.Empty(t, worker.probe.LibrariesZip)
	require.Equal(t, version.Version, worker.probe.Version)
	require.Equal(t, version.ExpectedSHA256, worker.probe.Sha256)
	require.Contains(t, worker.probe.DownloadUrl, "/base/probe-artifacts/")
}

func TestProvisionServer_RejectsProxyCore(t *testing.T) {
	svc := &ProvisionService{}

	_, err := svc.ProvisionServer(context.Background(), ProvisionServerRequest{CoreType: "velocity"})

	require.Error(t, err)
	require.Contains(t, err.Error(), "代理核心")
}

func TestProvisionServer_SpongeVanillaUsesGenericBackendFlow(t *testing.T) {
	db := newInstanceTestDB(t)
	node := model.Node{UUID: "node-sponge", Status: model.NodeStatusOnline, OS: "linux"}
	require.NoError(t, db.Create(&node).Error)

	coreRepo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/spongevanilla/maven-metadata.xml":
			_, _ = w.Write([]byte(`<metadata><versioning><versions><version>1.21.1-12.0.4-RC2665</version></versions></versioning></metadata>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer coreRepo.Close()

	pool := cpgrpc.NewClientPool()
	fake := &fakeProvisionWorker{}
	pool.SetWorkerClientForTest(node.UUID, fake)
	instSvc := NewInstanceService(db, NewGroupService(db), pool)
	coreSvc := &CoreService{client: coreRepo.Client(), base: coreRepo.URL, spongeBase: coreRepo.URL}
	svc := NewProvisionService(db, pool, instSvc, coreSvc, nil)
	online := true

	inst, err := svc.ProvisionServer(context.Background(), ProvisionServerRequest{
		NodeID:     node.ID,
		Name:       "Sponge Backend",
		CoreType:   "spongevanilla",
		MCVersion:  "1.21.1",
		MemoryMb:   1024,
		OnlineMode: &online,
	})

	require.NoError(t, err)
	require.Equal(t, model.InstanceRoleBackend, inst.Role)
	require.Equal(t, model.ProcessTypeDaemon, inst.ProcessType)
	require.Equal(t, model.InstanceStatusStopped, inst.Status)
	require.True(t, strings.HasPrefix(inst.WorkDir, "var/servers/sponge-backend-"), inst.WorkDir)
	require.NotNil(t, fake.create)
	require.Equal(t, inst.UUID, fake.create.InstanceUuid)
	require.Equal(t, "server.jar", fake.download.DestFilename)
	require.Empty(t, fake.download.Sha256)
	require.Contains(t, fake.download.DownloadUrl, "/spongevanilla/1.21.1-12.0.4-RC2665/")
	require.Equal(t, "eula=true\n", fake.writes["eula.txt"])
	require.Contains(t, fake.writes["server.properties"], "online-mode=true")
	require.Contains(t, fake.writes["server.properties"], "server-port=")
}

func TestProvisionServer_SpongeForgeUsesForgeArgFiles(t *testing.T) {
	cases := []struct {
		name    string
		nodeOS  string
		wantArg string
	}{
		{name: "windows", nodeOS: "windows", wantArg: "@libraries/net/minecraftforge/forge/1.21.1-52.1.5/win_args.txt"},
		{name: "linux", nodeOS: "linux", wantArg: "@libraries/net/minecraftforge/forge/1.21.1-52.1.5/unix_args.txt"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			db := newInstanceTestDB(t)
			node := model.Node{UUID: "node-spongeforge-" + tt.name, Status: model.NodeStatusOnline, OS: tt.nodeOS}
			require.NoError(t, db.Create(&node).Error)

			coreRepo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/spongeforge/maven-metadata.xml":
					_, _ = w.Write([]byte(`<metadata><versioning><versions><version>1.21.1-52.1.5-12.0.4-RC2665</version></versions></versioning></metadata>`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer coreRepo.Close()

			pool := cpgrpc.NewClientPool()
			fake := &fakeProvisionWorker{}
			pool.SetWorkerClientForTest(node.UUID, fake)
			instSvc := NewInstanceService(db, NewGroupService(db), pool)
			coreSvc := &CoreService{client: coreRepo.Client(), base: coreRepo.URL, spongeBase: coreRepo.URL, forgeBase: coreRepo.URL}
			svc := NewProvisionService(db, pool, instSvc, coreSvc, nil)

			_, err := svc.ProvisionServer(context.Background(), ProvisionServerRequest{
				NodeID:    node.ID,
				Name:      "SpongeForge Backend " + tt.name,
				CoreType:  "spongeforge",
				MCVersion: "1.21.1",
				MemoryMb:  4096,
				JvmArgs:   []string{"-XX:+UseG1GC"},
			})

			require.NoError(t, err)
			require.NotNil(t, fake.create)
			require.Contains(t, fake.create.StartCommand, "@user_jvm_args.txt")
			require.Contains(t, fake.create.StartCommand, tt.wantArg)
			require.NotContains(t, fake.create.StartCommand, "-jar forge-1.21.1-52.1.5-server.jar")
			require.NotNil(t, fake.install)
			require.Equal(t, "forge-1.21.1-52.1.5-server.jar", fake.install.LaunchJar)
			require.Contains(t, fake.install.ForgeInstallerUrl, "/1.21.1-52.1.5/forge-1.21.1-52.1.5-installer.jar")
		})
	}
}
