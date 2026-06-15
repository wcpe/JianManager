package service

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

var (
	ErrNodeNotFound = errors.New("节点不存在")
)

// NodeService 节点管理服务。
type NodeService struct {
	db *gorm.DB
}

// NewNodeService 创建节点服务。
func NewNodeService(db *gorm.DB) *NodeService {
	return &NodeService{db: db}
}

// RegisterRequest 节点注册请求。
type RegisterRequest struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	GRPCPort    int    `json:"grpcPort"`
	WSPort      int    `json:"wsPort"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	CPUCores    int    `json:"cpuCores"`
	MemoryMB    int64  `json:"memoryMb"`
	DiskTotalMB int64  `json:"diskTotalMb"`
}

// RegisterResult 节点注册结果。
type RegisterResult struct {
	NodeUUID   string `json:"nodeUuid"`
	NodeSecret string `json:"nodeSecret"`
}

// Register 节点首次注册。已注册节点通过 secret 重新连接。
func (s *NodeService) Register(req RegisterRequest) (*RegisterResult, error) {
	secret, err := generateSecret()
	if err != nil {
		return nil, fmt.Errorf("生成节点密钥失败: %w", err)
	}

	node := &model.Node{
		Name:        req.Name,
		Host:        req.Host,
		GRPCPort:    req.GRPCPort,
		WSPort:      req.WSPort,
		Secret:      secret,
		Status:      model.NodeStatusOnline,
		OS:          req.OS,
		Arch:        req.Arch,
		CPUCores:    req.CPUCores,
		MemoryMB:    req.MemoryMB,
		DiskTotalMB: req.DiskTotalMB,
		LastHeartbeat: ptrTime(time.Now()),
	}

	if err := s.db.Create(node).Error; err != nil {
		return nil, fmt.Errorf("注册节点失败: %w", err)
	}

	return &RegisterResult{
		NodeUUID:   node.UUID,
		NodeSecret: secret,
	}, nil
}

// HeartbeatData 心跳上报数据。
type HeartbeatData struct {
	CPUUsage    float64 `json:"cpuUsage"`
	MemoryUsage float64 `json:"memoryUsage"`
	DiskUsage   float64 `json:"diskUsage"`
	MemoryUsedMB int64  `json:"memoryUsedMb"`
	DiskUsedMB  int64   `json:"diskUsedMb"`
}

// Heartbeat 处理节点心跳。
func (s *NodeService) Heartbeat(nodeUUID string, data HeartbeatData) error {
	now := time.Now()
	result := s.db.Model(&model.Node{}).Where("uuid = ?", nodeUUID).Updates(map[string]interface{}{
		"status":         model.NodeStatusOnline,
		"last_heartbeat": &now,
	})
	if result.RowsAffected == 0 {
		return ErrNodeNotFound
	}
	return result.Error
}

// List 返回所有节点。
func (s *NodeService) List() ([]model.Node, error) {
	var nodes []model.Node
	if err := s.db.Find(&nodes).Error; err != nil {
		return nil, fmt.Errorf("查询节点列表失败: %w", err)
	}
	return nodes, nil
}

// GetByID 按 ID 获取节点。
func (s *NodeService) GetByID(id uint) (*model.Node, error) {
	var node model.Node
	if err := s.db.First(&node, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("查询节点失败: %w", err)
	}
	return &node, nil
}

// GetByUUID 按 UUID 获取节点。
func (s *NodeService) GetByUUID(uuid string) (*model.Node, error) {
	var node model.Node
	if err := s.db.Where("uuid = ?", uuid).First(&node).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("查询节点失败: %w", err)
	}
	return &node, nil
}

// Delete 删除节点（仅离线时）。
func (s *NodeService) Delete(id uint) error {
	node, err := s.GetByID(id)
	if err != nil {
		return err
	}
	if node.Status == model.NodeStatusOnline {
		return fmt.Errorf("不能删除在线节点")
	}
	return s.db.Delete(&model.Node{}, id).Error
}

// CheckOfflineNodes 检测离线节点（超过 90s 无心跳）。
func (s *NodeService) CheckOfflineNodes() {
	threshold := time.Now().Add(-90 * time.Second)
	s.db.Model(&model.Node{}).
		Where("status = ? AND last_heartbeat < ?", model.NodeStatusOnline, &threshold).
		Update("status", model.NodeStatusOffline)
}

func generateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func ptrTime(t time.Time) *time.Time {
	return &t
}
