package service

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 二进制/Beacon 版本管理（FR-468，见 ADR-090 补记）。
//
// 版本真源是制品库 Asset（内容寻址），本文件维护「实例 ↔ 版本」绑定，并承载
// 受控升级 / 一级回滚。核心语义修订：**重建不再重新解析来源**（旧行为会让运行中的
// 实例在制品库出新版本后静默升降级），而是读绑定冻结版本；「吃到新版本」由
// BinaryVersionService.Upgrade 显式承担。显式覆盖逃生口：请求里显式给了 binarySource
// 时仍以请求为准。

// 二进制版本管理错误。
var (
	// ErrBinaryVersionNotBound 实例未登记二进制绑定（非 binary/beacon 实例，或搭建早于 FR-468）。
	ErrBinaryVersionNotBound = errors.New("实例未登记二进制版本绑定")
	// ErrNoPreviousBinaryVersion 没有可回滚的上一版本（从未升级过）。
	ErrNoPreviousBinaryVersion = errors.New("没有可回滚的上一版本")
	// ErrBinaryVersionTargetInvalid 目标制品不是可用的二进制（空/失效/名非法）。
	ErrBinaryVersionTargetInvalid = errors.New("目标制品不是可用的二进制")
)

// instanceBinaryBinding 读取实例的二进制绑定；不存在返回 (nil, nil)。
//
// 查询失败（如迁移未覆盖本表的极端情况）同样返回 (nil, nil) 并降级：版本管理是增强层，
// 它的缺失不该阻断搭建/重建这些基础能力（回落到 FR-441/442 的既有行为）。
func (p *ProvisionService) instanceBinaryBinding(instanceID uint) (*model.InstanceBinaryBinding, error) {
	var binding model.InstanceBinaryBinding
	err := p.db.Where("instance_id = ?", instanceID).First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		slog.Warn("查询实例二进制绑定失败，按未登记处理", "instanceId", instanceID, "error", err)
		return nil, nil
	}
	return &binding, nil
}

// upsertInstanceBinaryBinding 写入/更新实例绑定（按 instance_id 唯一键 upsert）。
func (p *ProvisionService) upsertInstanceBinaryBinding(binding *model.InstanceBinaryBinding) error {
	if binding.InstanceID == 0 {
		return errors.New("二进制绑定缺少实例 ID")
	}
	var existing model.InstanceBinaryBinding
	err := p.db.Where("instance_id = ?", binding.InstanceID).First(&existing).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return p.db.Create(binding).Error
	case err != nil:
		return fmt.Errorf("查询实例二进制绑定失败: %w", err)
	}
	binding.ID = existing.ID
	return p.db.Model(&model.InstanceBinaryBinding{}).Where("id = ?", existing.ID).
		Select("*").Omit("id", "instance_id").Updates(binding).Error
}

// recordBinaryBindingOnProvision 搭建时登记绑定（FR-468 §2.2）。
//
// asset 来源绑定 asset.ID；url / node_file 无制品库记录 → CurrentAssetID=0，
// 仅记落盘名与（请求给出的）摘要，展示为「未知来源」。
func (p *ProvisionService) recordBinaryBindingOnProvision(inst *model.Instance, plan *binaryFetchPlan, assetID uint) {
	binding := &model.InstanceBinaryBinding{
		InstanceID:            inst.ID,
		CurrentAssetID:        assetID,
		CurrentFilename:       plan.Filename,
		CurrentSHA256:         strings.ToLower(strings.TrimSpace(plan.SHA256)),
		StartCommandAtBinding: inst.StartCommand,
	}
	if assetID != 0 && p.binaryAssets != nil {
		if asset, err := p.resolveBinaryAsset(assetID); err == nil {
			binding.CurrentVersion = asset.Version
			binding.CurrentFilename = beaconAssetFilenameOrDefault(asset, plan.Filename)
			if asset.SHA256 != "" {
				binding.CurrentSHA256 = strings.ToLower(strings.TrimSpace(asset.SHA256))
			}
		}
	}
	if err := p.upsertInstanceBinaryBinding(binding); err != nil {
		// 绑定写入失败不阻断搭建：版本管理是增强信息，缺失时表现为「未登记绑定」。
		slog.Warn("登记二进制版本绑定失败", "instanceId", inst.ID, "error", err)
	}
}

// refreshBindingAfterRebuild 重建成功后把绑定的落盘名/摘要刷新为本次实际取件的值。
// 无绑定（url/node_file 早期实例）时按需补建一条，使后续重建同样能冻结版本。
func (p *ProvisionService) refreshBindingAfterRebuild(instanceID uint, plan *binaryFetchPlan) {
	binding, err := p.instanceBinaryBinding(instanceID)
	if err != nil {
		return
	}
	if binding == nil {
		var inst model.Instance
		if err := p.db.Select("id", "start_command").First(&inst, instanceID).Error; err != nil {
			return
		}
		p.recordBinaryBindingOnProvision(&inst, plan, plan.AssetID)
		return
	}
	if err := p.db.Model(&model.InstanceBinaryBinding{}).Where("id = ?", binding.ID).
		Updates(map[string]any{
			"current_filename": plan.Filename,
			"current_sha256":   strings.ToLower(strings.TrimSpace(plan.SHA256)),
		}).Error; err != nil {
		slog.Warn("刷新二进制绑定失败", "instanceId", instanceID, "error", err)
	}
}

// beaconAssetFilenameOrDefault 取 asset 语义的落盘名；asset 无有效名时沿用 plan 的名字。
func beaconAssetFilenameOrDefault(asset *model.Asset, fallback string) string {
	if name := beaconAssetFilename(asset); validBinaryFilename(name) {
		return name
	}
	return fallback
}

// binaryPlanFromBinding 由绑定还原取件计划（FR-468 §2.2 重建冻结版本）。
//
// 返回 (nil, nil) 表示无绑定（调用方回落旧行为）；返回 error 表示绑定存在但目标
// 制品已不可交付（失效/被删），此时**不应**静默改用其它版本，而是明确失败。
func (p *ProvisionService) binaryPlanFromBinding(binding *model.InstanceBinaryBinding, requestBaseURL string) (*binaryFetchPlan, error) {
	if binding == nil || binding.CurrentAssetID == 0 {
		return nil, nil
	}
	asset, err := p.resolveBinaryAsset(binding.CurrentAssetID)
	if err != nil {
		return nil, fmt.Errorf("绑定版本 asset#%d 不可用: %w", binding.CurrentAssetID, err)
	}
	if p.artifactVersions == nil {
		return nil, errors.New("制品版本库服务未装配，无法按绑定版本取件")
	}
	token, err := p.artifactVersions.IssueBinaryDownloadToken(BinaryDownloadTokenScope{AssetID: asset.ID})
	if err != nil {
		return nil, fmt.Errorf("签发制品分发 token 失败: %w", err)
	}
	downloadURL, err := p.BuildBinaryDownloadURLForRequest(asset.ID, token, requestBaseURL)
	if err != nil {
		return nil, err
	}
	filename := binding.CurrentFilename
	if !validBinaryFilename(filename) {
		filename = beaconAssetFilenameOrDefault(asset, filename)
	}
	if !validBinaryFilename(filename) {
		return nil, fmt.Errorf("绑定版本 asset#%d 无可用落盘文件名", asset.ID)
	}
	return &binaryFetchPlan{
		SourceKind:  string(BinarySourceURL),
		DownloadURL: downloadURL,
		SHA256:      strings.ToLower(strings.TrimSpace(asset.SHA256)),
		Filename:    filename,
		Executable:  true,
		AssetID:     asset.ID,
	}, nil
}

// replaceBinaryTokenInStartCommand 把启动命令里对**旧二进制文件**的引用替换为新文件名。
//
// 只在启动命令确实引用了旧文件名时替换——`./run.sh`、`java -jar server.jar` 这类
// 不含旧文件名的命令是运维的明确意图（包装脚本 / 另一套启动方式），不得被系统改写。
// 返回 (新命令, 是否发生变化)。
func replaceBinaryTokenInStartCommand(startCommand, oldFilename, newFilename string) (string, bool) {
	cmd := strings.TrimSpace(startCommand)
	old := strings.TrimSpace(oldFilename)
	next := strings.TrimSpace(newFilename)
	if next == "" {
		return cmd, false
	}
	// 退化命令（`./`，即首次搭建时来源解析失败、文件名未知留下的空壳）：直接补成派生形式。
	// 这类命令不含任何运维意图，留着它必然 `command not found`。
	if cmd == "" || cmd == "./" {
		return "./" + next, true
	}
	if old == "" || old == next {
		return cmd, false
	}
	if !strings.Contains(cmd, old) {
		return cmd, false
	}
	return strings.ReplaceAll(cmd, old, next), true
}

// explicitBinarySourceProvided 报告请求是否显式指定了制品来源（FR-468 §2.2 逃生口）。
//
// 显式指定时以请求为准——运维「我有意换这个来源」是明确意图，不该被绑定冻结。
func explicitBinarySourceProvided(req ProvisionServerRequest) bool {
	src := binarySourceOf(req)
	return strings.TrimSpace(string(src.Kind)) != ""
}
