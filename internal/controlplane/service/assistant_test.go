package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/httpclient"
)

// AI 配置服务测试（FR-514）。
//
// 重点不在 CRUD 本身，而在几条「错了会静默出事」的语义：
//   - 凭据三态（保留 / 替换 / 清除）与重复保存不丢密钥；
//   - nil 加密器不得把凭据静默存成空串；
//   - 删除/停用默认模型不能让设置指向悬空或不可用的模型；
//   - 普通用户可见的模型摘要不得带端点与凭据。

// newAssistantTestDB 构造仅含 AI 三表的内存库。
func newAssistantTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.AssistantProvider{}, &model.AssistantModel{}, &model.AssistantSetting{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db
}

// newAssistantTestService 构造带加密器与出站准入的服务。
func newAssistantTestService(t *testing.T, enc *KeyEncryptor) (*AssistantService, *gorm.DB) {
	t.Helper()
	db := newAssistantTestDB(t)
	s := NewAssistantService(db, enc)
	s.SetEgress(httpclient.NewGuard(httpclient.GuardOptions{}))
	if err := s.EnsureSetting(); err != nil {
		t.Fatalf("初始化设置失败: %v", err)
	}
	return s, db
}

// testEncryptor 构造一个真实可用的 AES-256-GCM 加密器（32 字节密钥）。
func testEncryptor(t *testing.T) *KeyEncryptor {
	t.Helper()
	enc, _, err := ResolveKeyEncryptor(DevKeyEncSecretBase64, true, "")
	if err != nil || enc == nil {
		t.Fatalf("构造加密器失败: %v", err)
	}
	return enc
}

func baseProviderParams() SaveProviderParams {
	return SaveProviderParams{
		Name:           "示例供应商",
		Protocol:       model.AssistantProtocolOpenAIChat,
		BaseURL:        "https://api.example.com/v1",
		Enabled:        true,
		CredentialMode: model.AssistantCredentialNone,
	}
}

func TestAssistantProviderCRUD(t *testing.T) {
	s, _ := newAssistantTestService(t, testEncryptor(t))

	created, err := s.CreateProvider(baseProviderParams())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if created.ID == 0 || created.Version != 1 {
		t.Fatalf("创建结果异常: %+v", created)
	}
	if created.HasCredential {
		t.Fatal("none 模式不应有凭据")
	}

	list, err := s.ListProviders()
	if err != nil || len(list) != 1 {
		t.Fatalf("列表异常: %v, len=%d", err, len(list))
	}

	// 重名必须冲突。
	if _, err := s.CreateProvider(baseProviderParams()); !errors.Is(err, ErrAssistantConflict) {
		t.Fatalf("重名应返回冲突，实际 %v", err)
	}

	// 乐观并发：用过期的 version 更新必须失败。
	params := baseProviderParams()
	params.Name = "改名"
	params.Version = 99
	if _, err := s.UpdateProvider(created.ID, params); !errors.Is(err, ErrAssistantConflict) {
		t.Fatalf("版本不匹配应冲突，实际 %v", err)
	}

	params.Version = created.Version
	updated, err := s.UpdateProvider(created.ID, params)
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if updated.Name != "改名" || updated.Version != 2 {
		t.Fatalf("更新结果异常: %+v", updated)
	}

	if err := s.DeleteProvider(created.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := s.GetProvider(created.ID); !errors.Is(err, ErrAssistantNotFound) {
		t.Fatalf("删除后应不存在，实际 %v", err)
	}
}

// 更新时不提供密钥必须保留原密文——否则每次改个显示名就把凭据弄丢。
func TestAssistantProviderCredentialKeepOnUpdate(t *testing.T) {
	s, db := newAssistantTestService(t, testEncryptor(t))

	params := baseProviderParams()
	params.CredentialMode = model.AssistantCredentialStored
	params.CredentialSecret = "sk-original-secret"
	created, err := s.CreateProvider(params)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if !created.HasCredential {
		t.Fatal("stored 模式应标记有凭据")
	}
	var before model.AssistantProvider
	if err := db.First(&before, created.ID).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if strings.Contains(before.CredentialEnc, "sk-original-secret") {
		t.Fatal("库中不得出现明文凭据")
	}

	// 只改名字、不带密钥。
	upd := baseProviderParams()
	upd.Name = "只改名字"
	upd.CredentialMode = model.AssistantCredentialStored
	upd.CredentialSecret = ""
	upd.Version = created.Version
	updated, err := s.UpdateProvider(created.ID, upd)
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if !updated.HasCredential {
		t.Fatal("未提供新密钥时应保留原凭据")
	}
	var after model.AssistantProvider
	if err := db.First(&after, created.ID).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if after.CredentialEnc != before.CredentialEnc {
		t.Fatal("未提供新密钥时密文不应变化")
	}

	// 提供新密钥才替换。
	upd2 := baseProviderParams()
	upd2.CredentialMode = model.AssistantCredentialStored
	upd2.CredentialSecret = "sk-new-secret"
	upd2.Version = updated.Version
	if _, err := s.UpdateProvider(created.ID, upd2); err != nil {
		t.Fatalf("替换密钥失败: %v", err)
	}
	var replaced model.AssistantProvider
	if err := db.First(&replaced, created.ID).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if replaced.CredentialEnc == before.CredentialEnc {
		t.Fatal("提供新密钥时密文应变化")
	}
}

// 显式清除凭据要同时把模式降为 none，不留「stored 但无密文」的悬空态。
func TestAssistantProviderCredentialClear(t *testing.T) {
	s, db := newAssistantTestService(t, testEncryptor(t))

	params := baseProviderParams()
	params.CredentialMode = model.AssistantCredentialStored
	params.CredentialSecret = "sk-x"
	created, err := s.CreateProvider(params)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	clear := baseProviderParams()
	clear.CredentialMode = model.AssistantCredentialStored
	clear.ClearCredential = true
	clear.Version = created.Version
	updated, err := s.UpdateProvider(created.ID, clear)
	if err != nil {
		t.Fatalf("清除失败: %v", err)
	}
	if updated.HasCredential {
		t.Fatal("清除后不应再有凭据")
	}
	if updated.CredentialMode != model.AssistantCredentialNone {
		t.Fatalf("清除后模式应降为 none，实际 %q", updated.CredentialMode)
	}
	var row model.AssistantProvider
	if err := db.First(&row, created.ID).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if row.CredentialEnc != "" {
		t.Fatal("清除后密文应为空")
	}
}

// nil 加密器的 Encrypt 返回空串且不报错——服务必须显式拦住，否则凭据静默丢失。
func TestAssistantProviderRejectsStoredCredentialWithoutEncryptor(t *testing.T) {
	s, _ := newAssistantTestService(t, nil)

	params := baseProviderParams()
	params.CredentialMode = model.AssistantCredentialStored
	params.CredentialSecret = "sk-x"
	_, err := s.CreateProvider(params)
	if !errors.Is(err, ErrAssistantEncryptorMissing) {
		t.Fatalf("无加密器时应拒绝 stored 凭据，实际 %v", err)
	}

	// 但 env 模式仍可用：这让 AI 在缺加密器的部署里不至于完全不可用。
	envParams := baseProviderParams()
	envParams.Name = "环境变量供应商"
	envParams.CredentialMode = model.AssistantCredentialEnv
	envParams.CredentialEnv = "JIANMANAGER_ASSISTANT_TEST_KEY"
	if _, err := s.CreateProvider(envParams); err != nil {
		t.Fatalf("无加密器时 env 模式应可用，实际 %v", err)
	}
}

func TestAssistantProviderCredentialEnvValidation(t *testing.T) {
	s, _ := newAssistantTestService(t, testEncryptor(t))

	bad := []string{"", "OPENAI_KEY", "jianmanager_assistant_key", "JIANMANAGER_ASSISTANT_", "JIANMANAGER_ASSISTANT_A-B"}
	for _, env := range bad {
		p := baseProviderParams()
		p.CredentialMode = model.AssistantCredentialEnv
		p.CredentialEnv = env
		if _, err := s.CreateProvider(p); !errors.Is(err, ErrAssistantInvalid) {
			t.Fatalf("环境变量名 %q 应被拒绝，实际 %v", env, err)
		}
	}

	good := baseProviderParams()
	good.CredentialMode = model.AssistantCredentialEnv
	good.CredentialEnv = "JIANMANAGER_ASSISTANT_PROVIDER_A_1"
	if _, err := s.CreateProvider(good); err != nil {
		t.Fatalf("合法环境变量名应通过，实际 %v", err)
	}
}

func TestAssistantProviderRejectsBadBaseURL(t *testing.T) {
	s, _ := newAssistantTestService(t, testEncryptor(t))

	for _, raw := range []string{
		"", "http://api.example.com", "https://user:p@api.example.com",
		"https://api.example.com?x=1", "ftp://api.example.com",
	} {
		p := baseProviderParams()
		p.BaseURL = raw
		if _, err := s.CreateProvider(p); !errors.Is(err, ErrAssistantInvalid) {
			t.Fatalf("地址 %q 应被拒绝，实际 %v", raw, err)
		}
	}
}

func TestAssistantProviderRejectsUnknownProtocol(t *testing.T) {
	s, _ := newAssistantTestService(t, testEncryptor(t))
	p := baseProviderParams()
	p.Protocol = "gemini"
	if _, err := s.CreateProvider(p); !errors.Is(err, ErrAssistantInvalid) {
		t.Fatalf("未知协议应被拒绝，实际 %v", err)
	}
}

// 删除仍在使用的供应商必须拒绝，不级联删子模型。
func TestAssistantProviderDeleteRefusedWhenModelsExist(t *testing.T) {
	s, _ := newAssistantTestService(t, testEncryptor(t))

	p, err := s.CreateProvider(baseProviderParams())
	if err != nil {
		t.Fatalf("创建供应商失败: %v", err)
	}
	if _, err := s.CreateModel(SaveModelParams{
		ProviderID: p.ID, ModelID: "m1", DisplayName: "M1", Streaming: true, Tools: true, Enabled: true,
	}); err != nil {
		t.Fatalf("创建模型失败: %v", err)
	}

	if err := s.DeleteProvider(p.ID); !errors.Is(err, ErrAssistantInUse) {
		t.Fatalf("有子模型时应拒绝删除，实际 %v", err)
	}
}

func TestAssistantModelUniquenessAndValidation(t *testing.T) {
	s, _ := newAssistantTestService(t, testEncryptor(t))
	p, err := s.CreateProvider(baseProviderParams())
	if err != nil {
		t.Fatalf("创建供应商失败: %v", err)
	}

	params := SaveModelParams{ProviderID: p.ID, ModelID: "gpt-x", DisplayName: "X", Streaming: true, Tools: true, Enabled: true}
	if _, err := s.CreateModel(params); err != nil {
		t.Fatalf("创建模型失败: %v", err)
	}
	// 同供应商下同 modelId 冲突。
	if _, err := s.CreateModel(params); !errors.Is(err, ErrAssistantConflict) {
		t.Fatalf("重复模型应冲突，实际 %v", err)
	}
	// 不同供应商可同名。
	p2, err := s.CreateProvider(func() SaveProviderParams {
		q := baseProviderParams()
		q.Name = "另一个"
		q.BaseURL = "https://api2.example.com"
		return q
	}())
	if err != nil {
		t.Fatalf("创建第二个供应商失败: %v", err)
	}
	params2 := params
	params2.ProviderID = p2.ID
	if _, err := s.CreateModel(params2); err != nil {
		t.Fatalf("不同供应商可同名模型，实际 %v", err)
	}

	// 空 modelId 与非正数限额被拒。
	if _, err := s.CreateModel(SaveModelParams{ProviderID: p.ID, ModelID: "  "}); !errors.Is(err, ErrAssistantInvalid) {
		t.Fatal("空模型标识应被拒")
	}
	zero := 0
	if _, err := s.CreateModel(SaveModelParams{ProviderID: p.ID, ModelID: "m2", ContextWindow: &zero}); !errors.Is(err, ErrAssistantInvalid) {
		t.Fatal("非正数上下文窗口应被拒")
	}
	// 供应商不存在。
	if _, err := s.CreateModel(SaveModelParams{ProviderID: 9999, ModelID: "m3"}); !errors.Is(err, ErrAssistantInvalid) {
		t.Fatal("供应商不存在应被拒")
	}
}

// 默认模型：必须同时校验模型与供应商都启用；删除默认模型要顺手清空指针。
func TestAssistantDefaultModelLifecycle(t *testing.T) {
	s, db := newAssistantTestService(t, testEncryptor(t))
	p, _ := s.CreateProvider(baseProviderParams())
	m, err := s.CreateModel(SaveModelParams{ProviderID: p.ID, ModelID: "m1", Enabled: true, Streaming: true})
	if err != nil {
		t.Fatalf("创建模型失败: %v", err)
	}

	id := m.ID
	if err := s.SetDefaultModel(&id, 0); err != nil {
		t.Fatalf("设置默认模型失败: %v", err)
	}
	_, defaultID, err := s.AvailableModels()
	if err != nil {
		t.Fatalf("取可用模型失败: %v", err)
	}
	if defaultID == nil || *defaultID != m.ID {
		t.Fatalf("默认模型应为 %d，实际 %v", m.ID, defaultID)
	}

	// 停用模型后不能设为默认。
	if _, err := s.UpdateModel(m.ID, SaveModelParams{
		ProviderID: p.ID, ModelID: "m1", Enabled: false, Version: m.Version,
	}); err != nil {
		t.Fatalf("停用模型失败: %v", err)
	}
	if err := s.SetDefaultModel(&id, 0); !errors.Is(err, ErrAssistantInvalid) {
		t.Fatalf("停用的模型不应能设为默认，实际 %v", err)
	}

	// 删除默认模型后设置里的指针必须被清空，不留悬空引用。
	// 注：上一步把模型停用了，但停用不会自动清空默认指针（那是管理员可见的配置，
	// 由 AvailableModels 在可用性判定时过滤）；真正清空发生在删除时。
	var setting model.AssistantSetting
	if err := db.First(&setting, 1).Error; err != nil {
		t.Fatalf("回读设置失败: %v", err)
	}
	if setting.DefaultModelID == nil || *setting.DefaultModelID != m.ID {
		t.Fatalf("停用模型不应清空默认指针，实际 %v", setting.DefaultModelID)
	}
	// 停用后对外不可见（可用性由 AvailableModels 收敛）。
	if _, defaultID, err := s.AvailableModels(); err != nil {
		t.Fatalf("取可用模型失败: %v", err)
	} else if defaultID != nil {
		t.Fatalf("模型停用后默认不应对外可见，实际 %d", *defaultID)
	}

	if err := s.DeleteModel(m.ID); err != nil {
		t.Fatalf("删除模型失败: %v", err)
	}
	if err := db.First(&setting, 1).Error; err != nil {
		t.Fatalf("回读设置失败: %v", err)
	}
	if setting.DefaultModelID != nil {
		t.Fatal("删除默认模型后应清空默认指针")
	}
}

// 供应商停用后，其模型不应出现在普通用户的可用列表里，默认模型也随之消失。
func TestAssistantAvailableModelsRespectsProviderEnabled(t *testing.T) {
	s, _ := newAssistantTestService(t, testEncryptor(t))
	p, _ := s.CreateProvider(baseProviderParams())
	m, err := s.CreateModel(SaveModelParams{ProviderID: p.ID, ModelID: "m1", Enabled: true, Streaming: true})
	if err != nil {
		t.Fatalf("创建模型失败: %v", err)
	}
	id := m.ID
	if err := s.SetDefaultModel(&id, 0); err != nil {
		t.Fatalf("设置默认失败: %v", err)
	}

	// 停用供应商（子模型 enabled 保持不变）。
	upd := baseProviderParams()
	upd.Enabled = false
	upd.Version = p.Version
	if _, err := s.UpdateProvider(p.ID, upd); err != nil {
		t.Fatalf("停用供应商失败: %v", err)
	}

	models, defaultID, err := s.AvailableModels()
	if err != nil {
		t.Fatalf("取可用模型失败: %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("供应商停用后不应有可用模型，实际 %d 个", len(models))
	}
	if defaultID != nil {
		t.Fatal("供应商停用后默认模型不应再对外可见")
	}

	// 子模型自身开关未被改写（停用供应商不等于改子模型状态）。
	all, err := s.ListModels()
	if err != nil {
		t.Fatalf("取全部模型失败: %v", err)
	}
	if len(all) != 1 || !all[0].Enabled {
		t.Fatalf("供应商停用不应改写子模型 enabled: %+v", all)
	}
	if all[0].ProviderEnabled {
		t.Fatal("列表应如实反映供应商已停用")
	}
}

// 普通用户可见摘要是 DTO 投影，绝不能带出端点或任何凭据痕迹。
func TestAssistantAvailableModelsExcludesSensitiveFields(t *testing.T) {
	s, _ := newAssistantTestService(t, testEncryptor(t))
	p, _ := s.CreateProvider(baseProviderParams())
	if _, err := s.CreateModel(SaveModelParams{ProviderID: p.ID, ModelID: "m1", Enabled: true, Streaming: true}); err != nil {
		t.Fatalf("创建模型失败: %v", err)
	}
	models, _, err := s.AvailableModels()
	if err != nil {
		t.Fatalf("取可用模型失败: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("应有 1 个模型，实际 %d", len(models))
	}
	// AvailableModel 是独立结构体，编译期就不含 baseUrl/credential* 字段；
	// 这里再断言「没有意外把供应商视图嵌进来」。
	got := models[0]
	if got.ModelID != "m1" || got.Protocol != model.AssistantProtocolOpenAIChat {
		t.Fatalf("摘要字段异常: %+v", got)
	}
}
