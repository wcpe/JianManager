package service

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TemplateService 模板服务。
type TemplateService struct {
	db *gorm.DB
}

// NewTemplateService 创建模板服务。
func NewTemplateService(db *gorm.DB) *TemplateService {
	return &TemplateService{db: db}
}

// CreateTemplateRequest 创建模板请求。
type CreateTemplateRequest struct {
	Name           string `json:"name" binding:"required"`
	Type           string `json:"type" binding:"required"`
	Description    string `json:"description"`
	StartCommand   string `json:"startCommand" binding:"required"`
	DefaultWorkDir string `json:"defaultWorkDir"`
	DownloadURL    string `json:"downloadUrl"`
	ConfigFiles    string `json:"configFiles"`
}

// Create 创建模板。
func (s *TemplateService) Create(req CreateTemplateRequest) (*model.Template, error) {
	t := &model.Template{
		Name:           req.Name,
		Type:           req.Type,
		Description:    req.Description,
		StartCommand:   req.StartCommand,
		DefaultWorkDir: req.DefaultWorkDir,
		DownloadURL:    req.DownloadURL,
		ConfigFiles:    req.ConfigFiles,
	}
	if err := s.db.Create(t).Error; err != nil {
		return nil, fmt.Errorf("创建模板失败: %w", err)
	}
	return t, nil
}

// List 返回模板列表。
func (s *TemplateService) List() ([]model.Template, error) {
	var templates []model.Template
	if err := s.db.Find(&templates).Error; err != nil {
		return nil, err
	}
	return templates, nil
}

// Delete 删除模板。模板与实例为松关联（建实例时拷贝 startCommand），
// 删除模板不影响已创建的实例。
func (s *TemplateService) Delete(id uint) error {
	return s.db.Delete(&model.Template{}, id).Error
}
