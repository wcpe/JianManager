package service

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

var (
	ErrJDKNotFound = errors.New("JDK 不存在")
	ErrJDKInUse    = errors.New("JDK 正被实例占用")
)

type JDKService struct{ db *gorm.DB }

func NewJDKService(db *gorm.DB) *JDKService { return &JDKService{db: db} }

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
	var jdks []model.NodeJDK
	if err := s.db.Where("node_id = ?", nodeID).Order("major_version desc, id desc").Find(&jdks).Error; err != nil {
		return nil, fmt.Errorf("查询 JDK 列表失败: %w", err)
	}
	return jdks, nil
}

func (s *JDKService) Create(nodeID uint, req CreateJDKRequest) (*model.NodeJDK, error) {
	if err := s.ensureNode(nodeID); err != nil { return nil, err }
	jdk := &model.NodeJDK{NodeID: nodeID, Vendor: req.Vendor, MajorVersion: req.MajorVersion, Version: req.Version, Arch: req.Arch, Path: req.Path, Managed: req.Managed}
	if err := s.db.Create(jdk).Error; err != nil { return nil, fmt.Errorf("登记 JDK 失败: %w", err) }
	return jdk, nil
}

func (s *JDKService) Install(nodeID uint, req InstallJDKRequest) error {
	if err := s.ensureNode(nodeID); err != nil { return err }
	return fmt.Errorf("JDK 一键下载尚未接入 Worker 下载器，请先使用 POST /nodes/%d/jdks 登记已有 JDK", nodeID)
}

func (s *JDKService) Delete(nodeID, jdkID uint) ([]model.Instance, error) {
	var jdk model.NodeJDK
	if err := s.db.Where("id = ? AND node_id = ?", jdkID, nodeID).First(&jdk).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) { return nil, ErrJDKNotFound }
		return nil, fmt.Errorf("查询 JDK 失败: %w", err)
	}
	var used []model.Instance
	if err := s.db.Where("node_id = ? AND jdk_id = ?", nodeID, jdkID).Find(&used).Error; err != nil { return nil, err }
	if len(used) > 0 { return used, ErrJDKInUse }
	return nil, s.db.Delete(&model.NodeJDK{}, jdkID).Error
}

func (s *JDKService) ResolveForInstance(nodeID, jdkID uint, javaMajor int) (*model.NodeJDK, error) {
	if jdkID == 0 && javaMajor == 0 { return nil, nil }
	var jdk model.NodeJDK
	q := s.db.Where("node_id = ?", nodeID)
	if jdkID > 0 { q = q.Where("id = ?", jdkID) } else { q = q.Where("major_version = ?", javaMajor).Order("id desc") }
	if err := q.First(&jdk).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) { return nil, ErrJDKNotFound }
		return nil, err
	}
	return &jdk, nil
}

func (s *JDKService) ensureNode(nodeID uint) error {
	var n model.Node
	if err := s.db.First(&n, nodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) { return ErrNodeNotFound }
		return err
	}
	return nil
}
