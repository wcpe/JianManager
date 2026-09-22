package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// newBinaryVersionHarness 建二进制版本管理测试基座：真 SQLite + 假 worker + 两个制品。
//
// 返回的 assetID 按「低版本 → 高版本」顺序，便于用例表达升级/回滚方向。
func newBinaryVersionHarness(t *testing.T, worker *binaryWorkerStub) (*ProvisionService, *BinaryVersionService, *TaskService, *model.Node, []*model.Asset) {
	t.Helper()
	db := newInstanceTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.Task{}, &model.TaskLog{}, &model.Notification{}, &model.PlatformSetting{},
		&model.Asset{}, &model.InstanceBinaryBinding{},
	))
	node := &model.Node{UUID: "node-bv-" + t.Name(), Status: model.NodeStatusOnline, OS: "linux"}
	require.NoError(t, db.Create(node).Error)

	assetSvc := NewAssetService(db, nil)
	assets := make([]*model.Asset, 0, 2)
	for _, v := range []string{"1.0.0", "1.1.0"} {
		content := []byte("beacon-" + v)
		sum := sha256.Sum256(content)
		a := &model.Asset{
			Type: model.AssetTypeBlob, Name: "beacon", Version: v,
			Filename: "beacon-" + v + "-linux-amd64",
			SHA256:   hex.EncodeToString(sum[:]), Size: int64(len(content)),
			StorageState: model.AssetStorageHot,
		}
		require.NoError(t, db.Create(a).Error)
		assets = append(assets, a)
	}
	artifactSvc := NewArtifactVersionService(db, assetSvc)
	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(node.UUID, worker)
	instSvc := NewInstanceService(db, NewGroupService(db), pool)
	svc := NewProvisionService(db, pool, instSvc, NewCoreService(), nil, artifactSvc)
	taskSvc := NewTaskService(db)
	svc.SetTaskService(taskSvc)
	svc.SetBinaryAssets(assetSvc)
	svc.SetBinaryArtifactVersions(artifactSvc)
	require.NoError(t, db.Create(&model.PlatformSetting{
		Key: SettingKeyPlatformPublicBaseURL, Value: "https://cp.example.com",
	}).Error)

	bv := NewBinaryVersionService(db, svc)
	return svc, bv, taskSvc, node, assets
}

// provisionAssetInstance 用 kind=asset 搭一个 binary 实例（FR-468 绑定写入路径）。
func provisionAssetInstance(t *testing.T, svc *ProvisionService, taskSvc *TaskService, node *model.Node, asset *model.Asset, startCommand string) *model.Instance {
	t.Helper()
	inst, taskID, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID: node.ID, Name: "beacon-" + asset.Version, CoreType: "binary",
		BinarySource: &BinarySource{Kind: BinarySourceAsset, AssetID: asset.ID, Filename: asset.Filename},
		StartCommand: startCommand,
	}, 1)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateSucceeded, waitTaskTerminal(t, taskSvc, taskID).State)
	return inst
}

// TestBinaryBinding_WrittenOnAssetProvision 搭建时写入绑定：asset 来源绑定 assetID + 版本 + 摘要。
func TestBinaryBinding_WrittenOnAssetProvision(t *testing.T) {
	svc, _, taskSvc, node, assets := newBinaryVersionHarness(t, &binaryWorkerStub{})
	inst := provisionAssetInstance(t, svc, taskSvc, node, assets[0], "")

	var binding model.InstanceBinaryBinding
	require.NoError(t, svc.db.Where("instance_id = ?", inst.ID).First(&binding).Error)
	require.Equal(t, assets[0].ID, binding.CurrentAssetID)
	require.Equal(t, "1.0.0", binding.CurrentVersion)
	require.Equal(t, assets[0].Filename, binding.CurrentFilename)
	require.Equal(t, assets[0].SHA256, binding.CurrentSHA256)
	require.Zero(t, binding.PreviousAssetID, "首次搭建无回滚点")
}

// TestBinaryBinding_URLSourceHasNoAsset url 来源：无制品库版本，只记落盘名与摘要（验收项 ⑤）。
func TestBinaryBinding_URLSourceHasNoAsset(t *testing.T) {
	svc, bv, taskSvc, node, _ := newBinaryVersionHarness(t, &binaryWorkerStub{})
	inst, taskID, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID: node.ID, Name: "url-bin", CoreType: "binary",
		BinarySource: &BinarySource{Kind: BinarySourceURL, URL: "https://example.com/beacon", Filename: "beacon-custom"},
	}, 1)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateSucceeded, waitTaskTerminal(t, taskSvc, taskID).State)

	var binding model.InstanceBinaryBinding
	require.NoError(t, svc.db.Where("instance_id = ?", inst.ID).First(&binding).Error)
	require.Zero(t, binding.CurrentAssetID, "url 来源无制品库版本")
	require.Equal(t, "beacon-custom", binding.CurrentFilename)

	view, err := bv.View(inst.ID)
	require.NoError(t, err)
	require.True(t, view.NoLibraryVersion)
	require.Contains(t, view.Note, "无制品库版本")
}

// TestRebuildBinaryInstance_FreezesBoundVersion 重建**不漂移**：制品库出现更高版本后重建仍取绑定版本
// （FR-468 验收项 ②，对照 ADR-090 旧行为）。
func TestRebuildBinaryInstance_FreezesBoundVersion(t *testing.T) {
	worker := &binaryWorkerStub{}
	svc, _, taskSvc, node, assets := newBinaryVersionHarness(t, worker)
	inst := provisionAssetInstance(t, svc, taskSvc, node, assets[0], "")

	// 制品库「新增」更高版本：latestBeaconAsset 此时会选到它。
	require.NoError(t, svc.db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("status", model.InstanceStatusDamaged).Error)
	rebuildID, err := svc.RebuildInstance(context.Background(), inst.ID, 1)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateSucceeded, waitTaskTerminal(t, taskSvc, rebuildID).State)

	req := worker.lastRequest()
	require.NotNil(t, req)
	require.Contains(t, req.DownloadUrl, "/binary-assets/"+itoaU(assets[0].ID)+"/download",
		"重建必须取绑定版本 asset#%d，而不是最新的 asset#%d", assets[0].ID, assets[1].ID)
	require.Equal(t, assets[0].SHA256, req.Sha256)
	require.Equal(t, assets[0].Filename, req.DestFilename)
}

// TestRebuildBinaryInstance_ExplicitSourceWins 显式覆盖逃生口：请求给了 binarySource 时以请求为准。
func TestRebuildBinaryInstance_ExplicitSourceWins(t *testing.T) {
	worker := &binaryWorkerStub{}
	svc, _, taskSvc, node, assets := newBinaryVersionHarness(t, worker)
	inst := provisionAssetInstance(t, svc, taskSvc, node, assets[0], "")

	require.NoError(t, svc.db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Updates(map[string]any{
			"status":         model.InstanceStatusDamaged,
			"provision_spec": `{"nodeId":1,"name":"x","coreType":"binary"}`,
		}).Error)

	// 直接调重建内核：显式指定 node_file 来源（绕过放行根 → 走校验失败即可证明「以请求为准」）。
	_, err := svc.rebuildBinaryInstance(context.Background(), inst, ProvisionServerRequest{
		NodeID: node.ID, Name: inst.Name, CoreType: "binary",
		BinarySource: &BinarySource{Kind: BinarySourceNodeFile, NodePath: "/etc/passwd", Filename: "b"},
	}, 1, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "受控放行目录之外", "显式来源应覆盖绑定并被校验")
}

// TestUpgrade_SwitchesBindingAndStartCommand 升级：落盘名变化时同更 startCommand，绑定切换、回滚点写入。
func TestUpgrade_SwitchesBindingAndStartCommand(t *testing.T) {
	worker := &binaryWorkerStub{}
	svc, bv, taskSvc, node, assets := newBinaryVersionHarness(t, worker)
	inst := provisionAssetInstance(t, svc, taskSvc, node, assets[0], "")

	require.NoError(t, svc.db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("status", model.InstanceStatusStopped).Error)

	res, err := bv.Upgrade(context.Background(), inst.ID, assets[1].ID, 7)
	require.NoError(t, err)
	require.Equal(t, assets[0].ID, res.FromAssetID)
	require.Equal(t, assets[1].ID, res.ToAssetID)
	require.True(t, res.CommandChanged)
	require.Equal(t, model.TaskStateSucceeded, waitTaskTerminal(t, taskSvc, res.TaskID).State)

	// 落盘/启动命令/绑定三者一致（验收项 ①）。
	req := worker.lastRequest()
	require.Equal(t, assets[1].Filename, req.DestFilename)
	var got model.Instance
	require.NoError(t, svc.db.First(&got, inst.ID).Error)
	require.Equal(t, "./"+assets[1].Filename, got.StartCommand)

	var binding model.InstanceBinaryBinding
	require.NoError(t, svc.db.Where("instance_id = ?", inst.ID).First(&binding).Error)
	require.Equal(t, assets[1].ID, binding.CurrentAssetID)
	require.Equal(t, "1.1.0", binding.CurrentVersion)
	require.Equal(t, assets[1].SHA256, binding.CurrentSHA256)
	require.Equal(t, assets[0].ID, binding.PreviousAssetID, "升级前版本成为回滚点")
	require.Equal(t, assets[0].Filename, binding.PreviousFilename)
}

// TestUpgrade_ExplicitStartCommandPreserved 用户显式启动命令不被版本变更改写。
func TestUpgrade_ExplicitStartCommandPreserved(t *testing.T) {
	svc, bv, taskSvc, node, assets := newBinaryVersionHarness(t, &binaryWorkerStub{})
	// 显式命令不含二进制名（如 `./run.sh`）→ 版本变更不得改写它。
	inst := provisionAssetInstance(t, svc, taskSvc, node, assets[0], "./run.sh")
	require.NoError(t, svc.db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("status", model.InstanceStatusStopped).Error)

	res, err := bv.Upgrade(context.Background(), inst.ID, assets[1].ID, 7)
	require.NoError(t, err)
	require.False(t, res.CommandChanged)
	require.Equal(t, model.TaskStateSucceeded, waitTaskTerminal(t, taskSvc, res.TaskID).State)

	var got model.Instance
	require.NoError(t, svc.db.First(&got, inst.ID).Error)
	require.Equal(t, "./run.sh", got.StartCommand, "显式命令是运维意图，不该被系统改写")
}

// TestRollback_SymmetricToUpgrade 一级回滚与升级对称：绑定交换、落盘/命令恢复、可再回滚。
func TestRollback_SymmetricToUpgrade(t *testing.T) {
	svc, bv, taskSvc, node, assets := newBinaryVersionHarness(t, &binaryWorkerStub{})
	inst := provisionAssetInstance(t, svc, taskSvc, node, assets[0], "")
	require.NoError(t, svc.db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("status", model.InstanceStatusStopped).Error)

	up, err := bv.Upgrade(context.Background(), inst.ID, assets[1].ID, 7)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateSucceeded, waitTaskTerminal(t, taskSvc, up.TaskID).State)

	back, err := bv.Rollback(context.Background(), inst.ID, 7)
	require.NoError(t, err)
	require.Equal(t, assets[1].ID, back.FromAssetID)
	require.Equal(t, assets[0].ID, back.ToAssetID)
	require.Equal(t, model.TaskStateSucceeded, waitTaskTerminal(t, taskSvc, back.TaskID).State)

	var binding model.InstanceBinaryBinding
	require.NoError(t, svc.db.Where("instance_id = ?", inst.ID).First(&binding).Error)
	require.Equal(t, assets[0].ID, binding.CurrentAssetID, "已回滚到 1.0.0")
	require.Equal(t, assets[1].ID, binding.PreviousAssetID, "回滚本身也可再回滚（语义对称）")

	var got model.Instance
	require.NoError(t, svc.db.First(&got, inst.ID).Error)
	require.Equal(t, "./"+assets[0].Filename, got.StartCommand, "旧启动命令恢复")

	// 再回滚回到 1.1.0（交换语义成立）。
	again, err := bv.Rollback(context.Background(), inst.ID, 7)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateSucceeded, waitTaskTerminal(t, taskSvc, again.TaskID).State)
	require.NoError(t, svc.db.Where("instance_id = ?", inst.ID).First(&binding).Error)
	require.Equal(t, assets[1].ID, binding.CurrentAssetID)
}

// TestRollback_NoPreviousVersion 无回滚点时明确报错（验收项 ⑤）。
func TestRollback_NoPreviousVersion(t *testing.T) {
	svc, bv, taskSvc, node, assets := newBinaryVersionHarness(t, &binaryWorkerStub{})
	inst := provisionAssetInstance(t, svc, taskSvc, node, assets[0], "")
	require.NoError(t, svc.db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("status", model.InstanceStatusStopped).Error)

	_, err := bv.Rollback(context.Background(), inst.ID, 7)
	require.ErrorIs(t, err, ErrNoPreviousBinaryVersion)
	require.NoError(t, svc.db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Updates(map[string]any{"status": model.InstanceStatusStopped}).Error)
}

// TestUpgrade_RunningInstanceRejected 运行中拒绝版本变更（先停服的编排边界）。
func TestUpgrade_RunningInstanceRejected(t *testing.T) {
	svc, bv, taskSvc, node, assets := newBinaryVersionHarness(t, &binaryWorkerStub{})
	inst := provisionAssetInstance(t, svc, taskSvc, node, assets[0], "")
	require.NoError(t, svc.db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("status", model.InstanceStatusRunning).Error)

	_, err := bv.Upgrade(context.Background(), inst.ID, assets[1].ID, 7)
	require.ErrorIs(t, err, ErrBinaryUpgradeInstanceRunning)
	_, err = bv.Rollback(context.Background(), inst.ID, 7)
	require.ErrorIs(t, err, ErrBinaryUpgradeInstanceRunning)
}

// TestUpgrade_InvalidTarget 目标制品非法：空/不存在/等于当前版本均明确报错。
func TestUpgrade_InvalidTarget(t *testing.T) {
	svc, bv, taskSvc, node, assets := newBinaryVersionHarness(t, &binaryWorkerStub{})
	inst := provisionAssetInstance(t, svc, taskSvc, node, assets[0], "")
	require.NoError(t, svc.db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("status", model.InstanceStatusStopped).Error)

	_, err := bv.Upgrade(context.Background(), inst.ID, 0, 7)
	require.ErrorIs(t, err, ErrBinaryVersionTargetInvalid)

	_, err = bv.Upgrade(context.Background(), inst.ID, 999999, 7)
	require.ErrorIs(t, err, ErrBinaryVersionTargetInvalid)

	_, err = bv.Upgrade(context.Background(), inst.ID, assets[0].ID, 7)
	require.ErrorIs(t, err, ErrBinaryVersionTargetInvalid)
	require.Contains(t, err.Error(), "相同")
}

// TestView_DriftAndCandidates 版本视图：候选列表 + 漂移检测（启动命令不再指向绑定文件）。
func TestView_DriftAndCandidates(t *testing.T) {
	svc, bv, taskSvc, node, assets := newBinaryVersionHarness(t, &binaryWorkerStub{})
	inst := provisionAssetInstance(t, svc, taskSvc, node, assets[0], "")

	view, err := bv.View(inst.ID)
	require.NoError(t, err)
	require.True(t, view.Bound)
	require.Equal(t, assets[0].ID, view.CurrentAssetID)
	require.Equal(t, "1.0.0", view.CurrentVersion)
	require.False(t, view.HasRollback)
	require.False(t, view.DriftDetected)
	// 候选应含两个可用制品（当前 + 可升级）。
	require.Len(t, view.Candidates, 2)

	// 人工换文件：启动命令指向别的名字 → 漂移。
	require.NoError(t, svc.db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("start_command", "./beacon-manual").Error)
	view, err = bv.View(inst.ID)
	require.NoError(t, err)
	require.True(t, view.DriftDetected)
	require.Contains(t, view.DriftReason, "启动命令已不指向绑定文件")
}

// TestView_UnboundInstance 未登记绑定的实例（非 binary）→ 明确「未登记」而非报错。
func TestView_UnboundInstance(t *testing.T) {
	svc, bv, _, _, _ := newBinaryVersionHarness(t, &binaryWorkerStub{})
	inst := &model.Instance{
		NodeID: 1, Name: "mc", Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleUniversal, ProcessType: model.ProcessTypeDaemon, StartCommand: "java -jar s.jar",
	}
	require.NoError(t, svc.db.Create(inst).Error)

	view, err := bv.View(inst.ID)
	require.NoError(t, err)
	require.False(t, view.Bound)
	require.Contains(t, view.Note, "未登记二进制版本绑定")
}

// itoaU 无依赖的无符号整数字符串化（测试断言 URL 片段用）。
func itoaU(v uint) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
