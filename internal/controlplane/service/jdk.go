package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	cpgrpc "github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/proto/workerpb"
)

var (
	ErrJDKNotFound = errors.New("JDK 不存在")
	ErrJDKInUse    = errors.New("JDK 正被实例占用")
	ErrNodeOffline = errors.New("节点未连接")
)

type JDKService struct {
	db   *gorm.DB
	pool *cpgrpc.ClientPool
}

func NewJDKService(db *gorm.DB, pool *cpgrpc.ClientPool) *JDKService {
	return &JDKService{db: db, pool: pool}
}

type CreateJDKRequest struct {
	Vendor       string `json:"vendor" binding:"required"`
	MajorVersion int    `json:"majorVersion" binding:"required"`
	Version      string `json:"version" binding:"required"`
	Arch         string `json:"arch" binding:"required"`
	Path         string `json:"path" binding:"required"`
	Managed      bool   `json:"managed"`
}

type InstallJDKRequest struct {
	Vendor       string `json:"vendor" binding:"required"`
	MajorVersion int    `json:"majorVersion" binding:"required"`
	Arch         string `json:"arch" binding:"required"`
}

func (s *JDKService) List(nodeID uint) ([]model.NodeJDK, error) {
	if err := s.syncFromWorker(nodeID); err != nil {
		slog.Debug("JDK 同步失败（容忍）", "nodeId", nodeID, "error", err)
	}
	var jdks []model.NodeJDK
	if err := s.db.Where("node_id = ?", nodeID).Order("major_version desc, id desc").Find(&jdks).Error; err != nil {
		return nil, fmt.Errorf("查询 JDK 列表失败: %w", err)
	}
	return jdks, nil
}

func (s *JDKService) Create(nodeID uint, req CreateJDKRequest) (*model.NodeJDK, error) {
	if _, err := s.nodeByID(nodeID); err != nil {
		return nil, err
	}
	jdk := &model.NodeJDK{NodeID: nodeID, Vendor: req.Vendor, MajorVersion: req.MajorVersion, Version: req.Version, Arch: req.Arch, Path: req.Path, Managed: req.Managed}
	if err := s.db.Create(jdk).Error; err != nil {
		return nil, fmt.Errorf("登记 JDK 失败: %w", err)
	}
	return jdk, nil
}

func (s *JDKService) Install(nodeID uint, req InstallJDKRequest) (*model.NodeJDK, error) {
	node, err := s.nodeByID(nodeID)
	if err != nil {
		return nil, err
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return nil, ErrNodeOffline
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	resp, err := client.Worker.InstallJDK(ctx, &workerpb.InstallJDKRequest{
		Vendor:       req.Vendor,
		MajorVersion: int32(req.MajorVersion),
		Arch:         req.Arch,
	})
	if err != nil {
		return nil, fmt.Errorf("Worker InstallJDK RPC 失败: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("Worker 拒绝安装: %s", resp.Error)
	}
	if resp.Jdk == nil {
		return nil, fmt.Errorf("Worker 返回缺少 JDK 详情")
	}
	jdk := &model.NodeJDK{
		NodeID:       nodeID,
		Vendor:       resp.Jdk.Vendor,
		MajorVersion: int(resp.Jdk.MajorVersion),
		Version:      resp.Jdk.Version,
		Arch:         resp.Jdk.Arch,
		Path:         resp.Jdk.Path,
		Managed:      true,
	}
	if err := s.db.Create(jdk).Error; err != nil {
		return nil, fmt.Errorf("保存 JDK 记录失败: %w", err)
	}
	return jdk, nil
}

func (s *JDKService) Delete(nodeID, jdkID uint) ([]model.Instance, error) {
	var jdk model.NodeJDK
	if err := s.db.Where("id = ? AND node_id = ?", jdkID, nodeID).First(&jdk).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrJDKNotFound
		}
		return nil, fmt.Errorf("查询 JDK 失败: %w", err)
	}
	var used []model.Instance
	if err := s.db.Where("node_id = ? AND jdk_id = ?", nodeID, jdkID).Find(&used).Error; err != nil {
		return nil, err
	}
	if len(used) > 0 {
		return used, ErrJDKInUse
	}

	if s.pool != nil {
		if node, err := s.nodeByID(nodeID); err == nil {
			if client, ok := s.pool.Get(node.UUID); ok {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				resp, err := client.Worker.RemoveJDK(ctx, &workerpb.RemoveJDKRequest{Path: jdk.Path})
				if err != nil {
					return nil, fmt.Errorf("Worker RemoveJDK RPC 失败: %w", err)
				}
				if !resp.Success {
					return nil, fmt.Errorf("Worker 拒绝删除: %s", resp.Error)
				}
			}
		}
	}

	return nil, s.db.Delete(&model.NodeJDK{}, jdkID).Error
}

func (s *JDKService) ResolveForInstance(nodeID, jdkID uint, javaMajor int) (*model.NodeJDK, error) {
	if jdkID == 0 && javaMajor == 0 {
		return nil, nil
	}
	var jdk model.NodeJDK
	q := s.db.Where("node_id = ?", nodeID)
	if jdkID > 0 {
		q = q.Where("id = ?", jdkID)
	} else {
		q = q.Where("major_version = ?", javaMajor).Order("id desc")
	}
	if err := q.First(&jdk).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrJDKNotFound
		}
		return nil, err
	}
	return &jdk, nil
}

func (s *JDKService) syncFromWorker(nodeID uint) error {
	if s.pool == nil {
		return nil
	}
	node, err := s.nodeByID(nodeID)
	if err != nil {
		return err
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return ErrNodeOffline
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := client.Worker.ListJDKs(ctx, &workerpb.ListJDKsRequest{})
	if err != nil {
		return fmt.Errorf("ListJDKs RPC: %w", err)
	}
	for _, j := range resp.Jdks {
		var existing model.NodeJDK
		err := s.db.Where("node_id = ? AND path = ?", nodeID, j.Path).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := s.db.Create(&model.NodeJDK{
				NodeID:       nodeID,
				Vendor:       j.Vendor,
				MajorVersion: int(j.MajorVersion),
				Version:      j.Version,
				Arch:         j.Arch,
				Path:         j.Path,
				Managed:      j.Managed,
			}).Error; err != nil {
				slog.Warn("同步 JDK 失败（插入）", "error", err)
			}
		} else if err == nil {
			existing.Vendor = j.Vendor
			existing.MajorVersion = int(j.MajorVersion)
			existing.Version = j.Version
			existing.Arch = j.Arch
			existing.Managed = j.Managed
			if err := s.db.Save(&existing).Error; err != nil {
				slog.Warn("同步 JDK 失败（更新）", "error", err)
			}
		}
	}
	return nil
}

func (s *JDKService) nodeByID(nodeID uint) (*model.Node, error) {
	var n model.Node
	if err := s.db.First(&n, nodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, err
	}
	return &n, nil
}
