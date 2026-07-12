package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// pmTokenMask 是 registry 凭据脱敏占位（回传此值表示「不改该源 token」，同 proxy.url 保存语义，FR-306）。
const pmTokenMask = "********"

// PMConfigService 管理节点包管理器与 registry 配置（FR-306）。
type PMConfigService struct {
	db   *gorm.DB
	pool *cpgrpc.ClientPool
}

// NewPMConfigService 创建 PM 配置服务。
func NewPMConfigService(db *gorm.DB, pool *cpgrpc.ClientPool) *PMConfigService {
	return &PMConfigService{db: db, pool: pool}
}

// PMConfigView 对外表示（token 脱敏，tokenMasked 标识有无凭据）。
type PMConfigView struct {
	PM                string             `json:"pm"`
	CorepackAvailable bool               `json:"corepackAvailable"`
	PMVersion         string             `json:"pmVersion"`
	NodeBin           string             `json:"nodeBin"`
	Registries        []PMRegistryView   `json:"registries"`
}

// PMRegistryView 单条 registry 对外表示。
type PMRegistryView struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Scope       string `json:"scope"`
	Token       string `json:"token"`       // 脱敏后回传 pmTokenMask（有凭据时）或空
	TokenMasked bool   `json:"tokenMasked"` // 是否存有凭据
}

// SetPMConfigInput 保存入参。
type SetPMConfigInput struct {
	PM         string           `json:"pm"`
	Registries []PMRegistryView `json:"registries"`
}

func (s *PMConfigService) load(nodeID uint) (*model.NodePMConfig, []model.PMRegistry, error) {
	var cfg model.NodePMConfig
	err := s.db.Where("node_id = ?", nodeID).First(&cfg).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &model.NodePMConfig{NodeID: nodeID, PM: "npm"}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var regs []model.PMRegistry
	if strings.TrimSpace(cfg.Registries) != "" {
		_ = json.Unmarshal([]byte(cfg.Registries), &regs)
	}
	return &cfg, regs, nil
}

// Get 读取节点 PM 配置：DB 偏好 + registry（脱敏）叠加 Worker 报告的 corepack 可用性/版本。
func (s *PMConfigService) Get(nodeID uint) (*PMConfigView, error) {
	node, err := s.nodeByID(nodeID)
	if err != nil {
		return nil, err
	}
	cfg, regs, err := s.load(nodeID)
	if err != nil {
		return nil, err
	}
	view := &PMConfigView{PM: cfg.PM}
	for _, r := range regs {
		view.Registries = append(view.Registries, PMRegistryView{
			Name: r.Name, URL: r.URL, Scope: r.Scope,
			Token: maskPMToken(r.Token), TokenMasked: strings.TrimSpace(r.Token) != "",
		})
	}
	// Worker 报告 corepack/版本（节点离线则留空，不报错——配置本身仍可看）。
	if client, ok := s.pool.Get(node.UUID); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if resp, err := client.Worker.GetPMConfig(ctx, &workerpb.GetPMConfigRequest{Pm: cfg.PM}); err == nil {
			view.CorepackAvailable = resp.GetCorepackAvailable()
			view.PMVersion = resp.GetPmVersion()
			view.NodeBin = resp.GetNodeBin()
		}
	}
	return view, nil
}

// Set 保存 PM 配置：掩码 token 保留旧值、明文更新；落库并下发 Worker（corepack enable + 写 .npmrc）。
func (s *PMConfigService) Set(nodeID uint, in SetPMConfigInput) (*PMConfigView, error) {
	node, err := s.nodeByID(nodeID)
	if err != nil {
		return nil, err
	}
	cfg, oldRegs, err := s.load(nodeID)
	if err != nil {
		return nil, err
	}
	// 旧 token 按 scope 索引，掩码回传时保留。
	oldToken := map[string]string{}
	for _, r := range oldRegs {
		oldToken[r.Scope] = r.Token
	}
	pm := strings.ToLower(strings.TrimSpace(in.PM))
	if pm == "" {
		pm = "npm"
	}
	var newRegs []model.PMRegistry
	for _, r := range in.Registries {
		tok := strings.TrimSpace(r.Token)
		if tok == pmTokenMask || tok == "" {
			tok = oldToken[r.Scope] // 掩码或空 → 沿用旧 token（掩码=不改；空=清除由前端显式传空区分不了，保守沿用）
		}
		newRegs = append(newRegs, model.PMRegistry{Name: r.Name, URL: strings.TrimSpace(r.URL), Scope: strings.TrimSpace(r.Scope), Token: tok})
	}

	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return nil, ErrNodeOffline
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	resp, err := client.Worker.SetPMConfig(ctx, &workerpb.SetPMConfigRequest{
		Pm:         pm,
		Registries: pmRegsToProto(newRegs),
	})
	if err != nil {
		return nil, fmt.Errorf("Worker SetPMConfig RPC 失败: %w", err)
	}
	if !resp.GetSuccess() {
		return nil, fmt.Errorf("%s", resp.GetError())
	}

	// Worker 成功才落库。
	blob, _ := json.Marshal(newRegs)
	cfg.PM = pm
	cfg.Registries = string(blob)
	cfg.UpdatedAt = time.Now()
	if cfg.ID == 0 {
		if err := s.db.Create(cfg).Error; err != nil {
			return nil, fmt.Errorf("保存 PM 配置失败: %w", err)
		}
	} else if err := s.db.Save(cfg).Error; err != nil {
		return nil, fmt.Errorf("保存 PM 配置失败: %w", err)
	}
	return s.Get(nodeID)
}

func (s *PMConfigService) nodeByID(nodeID uint) (*model.Node, error) {
	var n model.Node
	if err := s.db.First(&n, nodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, err
	}
	return &n, nil
}

func maskPMToken(t string) string {
	if strings.TrimSpace(t) == "" {
		return ""
	}
	return pmTokenMask
}

func pmRegsToProto(regs []model.PMRegistry) []*workerpb.PMRegistry {
	out := make([]*workerpb.PMRegistry, 0, len(regs))
	for _, r := range regs {
		out = append(out, &workerpb.PMRegistry{Name: r.Name, Url: r.URL, Scope: r.Scope, Token: r.Token})
	}
	return out
}
