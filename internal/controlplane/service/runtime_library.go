package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

var (
	// ErrInvalidRuntimeType 运行时类型不在支持集内（FR-298 验收：未知类型拒绝），路由映射 422。
	ErrInvalidRuntimeType = errors.New("不支持的运行时类型")
	// ErrRuntimeNotFound 运行时登记记录不存在。
	ErrRuntimeNotFound = errors.New("运行时不存在")
	// ErrRuntimeDuplicated 同节点同类型同路径重复登记。
	ErrRuntimeDuplicated = errors.New("该路径已登记同类型运行时")
)

// scanableRuntimeTypes 可扫描类型（Worker 有探测器的）。
var scanableRuntimeTypes = map[string]bool{"jdk": true, "nodejs": true}

// registrableRuntimeTypes 可登记类型（python 仅预留枚举：可手动登记，无扫描/安装器）。
var registrableRuntimeTypes = map[string]bool{"jdk": true, "nodejs": true, "python": true}

// RuntimeLibraryService 节点运行时库（FR-298）：统一 Runtime 视图（node_jdks + node_runtimes
// 读侧拼装，写侧各走各表）、扫描发现（代理 Worker ScanRuntimes）、登记与删除。
// JDK 的写路径全部委托既有 JDKService（node_jdks 一字不动，实例外键零变更）。
type RuntimeLibraryService struct {
	db   *gorm.DB
	pool *cpgrpc.ClientPool
	jdk  *JDKService
}

// NewRuntimeLibraryService 创建节点运行时库服务。
func NewRuntimeLibraryService(db *gorm.DB, pool *cpgrpc.ClientPool, jdk *JDKService) *RuntimeLibraryService {
	return &RuntimeLibraryService{db: db, pool: pool, jdk: jdk}
}

// RuntimeView 统一 Runtime 视图行：node_jdks(type=jdk) 与 node_runtimes 读侧拼装。
// jdk 行 Name=厂商（Temurin/...），ID 为 node_jdks 主键；其它行 ID 为 node_runtimes 主键——
// 增删改必须带 type 定位承载表（见 Delete）。
type RuntimeView struct {
	ID           uint      `json:"id"`
	NodeID       uint      `json:"nodeId"`
	Type         string    `json:"type"`
	Name         string    `json:"name"`
	MajorVersion int       `json:"majorVersion"`
	Version      string    `json:"version"`
	Arch         string    `json:"arch"`
	Path         string    `json:"path"`
	Managed      bool      `json:"managed"`
	CreatedAt    time.Time `json:"createdAt"`
}

// RuntimeCandidate 一条扫描发现的运行时候选（对齐 workerpb.RuntimeCandidate）。
type RuntimeCandidate struct {
	Type              string `json:"type"`
	Vendor            string `json:"vendor"`
	Version           string `json:"version"`
	MajorVersion      int    `json:"majorVersion"`
	Arch              string `json:"arch"`
	Path              string `json:"path"`
	AlreadyRegistered bool   `json:"alreadyRegistered"`
}

// Scan 代理 Worker ScanRuntimes 扫描常见安装路径回候选列表（FR-298）。
// types 空 = 全部可扫描类型；未知类型返回 ErrInvalidRuntimeType（不下发）。
// Worker 只能按托管根标 already_registered，这里再按 DB 已登记路径补标
// （外部路径入库后重复扫描也能标出「已在库」）。
func (s *RuntimeLibraryService) Scan(nodeID uint, types []string) ([]RuntimeCandidate, error) {
	normalized := make([]string, 0, len(types))
	for _, t := range types {
		tt := strings.ToLower(strings.TrimSpace(t))
		if tt == "" {
			continue
		}
		if !scanableRuntimeTypes[tt] {
			return nil, fmt.Errorf("%w: %q（可扫描类型：jdk、nodejs）", ErrInvalidRuntimeType, t)
		}
		normalized = append(normalized, tt)
	}

	node, err := s.nodeByID(nodeID)
	if err != nil {
		return nil, err
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return nil, ErrNodeOffline
	}
	// 扫描会对每个候选跑 java/node 探测，候选多时偏慢；60s 上限防节点卡顿拖死请求。
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := client.Worker.ScanRuntimes(ctx, &workerpb.ScanRuntimesRequest{Types: normalized})
	if err != nil {
		return nil, fmt.Errorf("Worker ScanRuntimes RPC 失败: %w", err)
	}

	registered, err := s.registeredPaths(nodeID)
	if err != nil {
		return nil, err
	}
	out := make([]RuntimeCandidate, 0, len(resp.Candidates))
	for _, c := range resp.Candidates {
		out = append(out, RuntimeCandidate{
			Type:              c.Type,
			Vendor:            c.Vendor,
			Version:           c.Version,
			MajorVersion:      int(c.MajorVersion),
			Arch:              c.Arch,
			Path:              c.Path,
			AlreadyRegistered: c.AlreadyRegistered || registered[c.Type+"\x00"+c.Path],
		})
	}
	return out, nil
}

// registeredPaths 该节点已登记的 (type, path) 集合：node_jdks 记 jdk、node_runtimes 记各自类型。
func (s *RuntimeLibraryService) registeredPaths(nodeID uint) (map[string]bool, error) {
	out := map[string]bool{}
	var jdkPaths []string
	if err := s.db.Model(&model.NodeJDK{}).Where("node_id = ?", nodeID).Pluck("path", &jdkPaths).Error; err != nil {
		return nil, fmt.Errorf("查询已登记 JDK 失败: %w", err)
	}
	for _, p := range jdkPaths {
		out["jdk\x00"+p] = true
	}
	var runtimes []model.NodeRuntime
	if err := s.db.Select("type, path").Where("node_id = ?", nodeID).Find(&runtimes).Error; err != nil {
		return nil, fmt.Errorf("查询已登记运行时失败: %w", err)
	}
	for _, rt := range runtimes {
		out[rt.Type+"\x00"+rt.Path] = true
	}
	return out, nil
}

// List 统一 Runtime 视图（FR-298）：node_jdks(type=jdk) + node_runtimes 读侧拼装。
// JDK 部分复用 JDKService.List（自带 syncFromWorker 容忍语义：同步失败仍回 DB 数据）。
// 排序：type 升序（jdk 在前）→ major 降序 → id 降序。
func (s *RuntimeLibraryService) List(nodeID uint) ([]RuntimeView, error) {
	if _, err := s.nodeByID(nodeID); err != nil {
		return nil, err
	}
	jdks, err := s.jdk.List(nodeID)
	if err != nil {
		return nil, err
	}
	out := make([]RuntimeView, 0, len(jdks))
	for _, j := range jdks {
		out = append(out, RuntimeView{
			ID:           j.ID,
			NodeID:       j.NodeID,
			Type:         "jdk",
			Name:         j.Vendor,
			MajorVersion: j.MajorVersion,
			Version:      j.Version,
			Arch:         j.Arch,
			Path:         j.Path,
			Managed:      j.Managed,
			CreatedAt:    j.CreatedAt,
		})
	}
	var runtimes []model.NodeRuntime
	if err := s.db.Where("node_id = ?", nodeID).
		Order("type asc, major desc, id desc").Find(&runtimes).Error; err != nil {
		return nil, fmt.Errorf("查询运行时列表失败: %w", err)
	}
	for _, rt := range runtimes {
		out = append(out, RuntimeView{
			ID:           rt.ID,
			NodeID:       rt.NodeID,
			Type:         rt.Type,
			Name:         rt.Name,
			MajorVersion: rt.Major,
			Version:      rt.Version,
			Arch:         rt.Arch,
			Path:         rt.Path,
			Managed:      rt.Managed,
			CreatedAt:    rt.CreatedAt,
		})
	}
	return out, nil
}

// RegisterRuntimeRequest 登记请求（泛化多类型）。type=jdk 时 vendor（或 name 兜底）+
// majorVersion 必填并转发现有 JDK 登记链路；其它类型落 node_runtimes。
type RegisterRuntimeRequest struct {
	Type         string `json:"type" binding:"required"`
	Name         string `json:"name"`
	Vendor       string `json:"vendor"`
	MajorVersion int    `json:"majorVersion"`
	Version      string `json:"version" binding:"required"`
	Arch         string `json:"arch"`
	Path         string `json:"path" binding:"required"`
	Managed      bool   `json:"managed"`
}

// Register 登记运行时（FR-298）：type=jdk 转发 JDKService.Create（落 node_jdks），
// 其它已知类型落 node_runtimes；未知类型 ErrInvalidRuntimeType。
func (s *RuntimeLibraryService) Register(nodeID uint, req RegisterRuntimeRequest) (*RuntimeView, error) {
	typ := strings.ToLower(strings.TrimSpace(req.Type))
	if !registrableRuntimeTypes[typ] {
		return nil, fmt.Errorf("%w: %q（可登记类型：jdk、nodejs、python）", ErrInvalidRuntimeType, req.Type)
	}

	if typ == "jdk" {
		vendor := req.Vendor
		if vendor == "" {
			vendor = req.Name
		}
		if vendor == "" || req.MajorVersion == 0 {
			return nil, fmt.Errorf("登记 JDK 需提供 vendor 与 majorVersion")
		}
		jdk, err := s.jdk.Create(nodeID, CreateJDKRequest{
			Vendor:       vendor,
			MajorVersion: req.MajorVersion,
			Version:      req.Version,
			Arch:         req.Arch,
			Path:         req.Path,
			Managed:      req.Managed,
		})
		if err != nil {
			return nil, err
		}
		return &RuntimeView{
			ID: jdk.ID, NodeID: jdk.NodeID, Type: "jdk", Name: jdk.Vendor,
			MajorVersion: jdk.MajorVersion, Version: jdk.Version, Arch: jdk.Arch,
			Path: jdk.Path, Managed: jdk.Managed, CreatedAt: jdk.CreatedAt,
		}, nil
	}

	if _, err := s.nodeByID(nodeID); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = defaultRuntimeName(typ, req.MajorVersion)
	}
	// 应用层先查重（SQLite/MySQL 唯一冲突错误形态不一，统一成业务错误）；
	// 唯一索引 (node_id,type,path) 仍兜底并发窗口。
	var existing int64
	if err := s.db.Model(&model.NodeRuntime{}).
		Where("node_id = ? AND type = ? AND path = ?", nodeID, typ, req.Path).
		Count(&existing).Error; err != nil {
		return nil, fmt.Errorf("查重失败: %w", err)
	}
	if existing > 0 {
		return nil, ErrRuntimeDuplicated
	}
	rt := &model.NodeRuntime{
		NodeID:  nodeID,
		Type:    typ,
		Name:    name,
		Version: req.Version,
		Major:   req.MajorVersion,
		Arch:    req.Arch,
		Path:    req.Path,
		Managed: req.Managed,
	}
	if err := s.db.Create(rt).Error; err != nil {
		return nil, fmt.Errorf("登记运行时失败: %w", err)
	}
	return &RuntimeView{
		ID: rt.ID, NodeID: rt.NodeID, Type: rt.Type, Name: rt.Name,
		MajorVersion: rt.Major, Version: rt.Version, Arch: rt.Arch,
		Path: rt.Path, Managed: rt.Managed, CreatedAt: rt.CreatedAt,
	}, nil
}

// defaultRuntimeName 按类型生成默认展示名（如 "Node.js 22"）。
func defaultRuntimeName(typ string, major int) string {
	base := typ
	switch typ {
	case "nodejs":
		base = "Node.js"
	case "python":
		base = "Python"
	}
	if major > 0 {
		return fmt.Sprintf("%s %d", base, major)
	}
	return base
}

// Delete 删除运行时（FR-298）。type=jdk 委托 JDKService.Delete（保留占用守卫 +
// 托管连文件语义，返回占用实例）；其它类型只删 node_runtimes 记录——
// 波1 非 JDK 均为外部登记，不动磁盘文件；托管 Node 的文件清理随 FR-299 安装器一起做。
func (s *RuntimeLibraryService) Delete(nodeID, runtimeID uint, typ string) ([]model.Instance, error) {
	typ = strings.ToLower(strings.TrimSpace(typ))
	if !registrableRuntimeTypes[typ] {
		return nil, fmt.Errorf("%w: %q", ErrInvalidRuntimeType, typ)
	}
	if typ == "jdk" {
		return s.jdk.Delete(nodeID, runtimeID)
	}
	var rt model.NodeRuntime
	if err := s.db.Where("id = ? AND node_id = ? AND type = ?", runtimeID, nodeID, typ).First(&rt).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRuntimeNotFound
		}
		return nil, fmt.Errorf("查询运行时失败: %w", err)
	}
	return nil, s.db.Delete(&model.NodeRuntime{}, rt.ID).Error
}

func (s *RuntimeLibraryService) nodeByID(nodeID uint) (*model.Node, error) {
	var n model.Node
	if err := s.db.First(&n, nodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, err
	}
	return &n, nil
}
