package service

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// JmPackService 服务端 .jmpack 打包工具（FR-097，见 ADR-021/022）。
//
// 复用已存制品字节（codec 即制品 codec，不重新压缩）+ Ed25519 签名（复用 FR-087 信任根）→ 入制品库 type=client-pack。
// 与现有 per-file 投递正交：per-file 仍是主投递，.jmpack 为就绪容器（供 FR-098 块级 diff / 可选打包投递复用）。
type JmPackService struct {
	assets  *AssetService
	version *ClientVersionService
	signer  *ManifestSigner
}

// NewJmPackService 创建打包服务。signer 为 nil 时 PackVersion 报 ErrSignKeyNotConfigured。
func NewJmPackService(assets *AssetService, version *ClientVersionService, signer *ManifestSigner) *JmPackService {
	return &JmPackService{assets: assets, version: version, signer: signer}
}

// PackVersion 把频道 latest 版本的文件打成 .jmpack 入库（内容寻址去重），返回 .jmpack 制品元数据。
// 频道不存在→ErrChannelNotFound；无 latest→ErrNoLatestVersion；未配签名私钥→ErrSignKeyNotConfigured。
func (s *JmPackService) PackVersion(channelID string) (*ClientFileResult, error) {
	if s.signer == nil {
		return nil, ErrSignKeyNotConfigured
	}
	ch, err := s.version.getChannel(channelID)
	if err != nil {
		return nil, err
	}
	if ch.CurrentVersion <= 0 {
		return nil, ErrNoLatestVersion
	}
	ver, err := s.version.findVersion(channelID, ch.CurrentVersion)
	if err != nil {
		if errors.Is(err, ErrVersionNotFound) {
			return nil, ErrNoLatestVersion
		}
		return nil, err
	}
	files, _, _, err := decodeVersionSnapshot(ver)
	if err != nil {
		return nil, err
	}

	inputs := make([]JmPackInput, 0, len(files))
	for _, f := range files {
		// ignore 文件仅展示/审计、无制品，不入包。
		if f.Sync == "ignore" || f.Artifact.SHA256 == "" {
			continue
		}
		_, absPath, oerr := s.version.OpenArtifact(f.Artifact.SHA256)
		if oerr != nil {
			return nil, fmt.Errorf("读取制品 %s 失败: %w", f.Path, oerr)
		}
		data, rerr := os.ReadFile(absPath)
		if rerr != nil {
			return nil, fmt.Errorf("读取制品文件失败: %w", rerr)
		}
		inputs = append(inputs, JmPackInput{
			Path: f.Path, SHA256: f.SHA256, Size: f.Size, Codec: f.Artifact.Codec, Data: data,
		})
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("%w: 版本无可打包文件", ErrInvalidVersionFiles)
	}

	packed, err := PackJmPack(inputs, s.signer.SignRaw)
	if err != nil {
		return nil, err
	}
	asset, err := s.assets.Ingest(bytes.NewReader(packed), IngestParams{
		Type:     model.AssetTypeClientPack,
		Filename: fmt.Sprintf("%s-v%d.jmpack", channelID, ver.Version),
	})
	if err != nil {
		return nil, err
	}
	return &ClientFileResult{SHA256: asset.SHA256, MD5: asset.MD5, Size: asset.Size, Codec: "none"}, nil
}
