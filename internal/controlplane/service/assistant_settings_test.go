package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/wcpe/JianManager/internal/controlplane/ai"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/httpclient"
)

// 助手全局设置：出站准入放行段（§4.3 / §6.2 缺口 8）。
//
// 这组用例守三件事：
//  1. 非法 CIDR 必须**拒绝并指出是哪一项**，不能静默丢弃；
//  2. 保存后必须**立即生效**（重建 Guard），否则「保存成功但行为不变」最难自查；
//  3. metadata 段即便被显式放行也仍被拒绝——这条不因新增入口而松动。

// newEgressTestService 构造带「可重建出站工厂」的服务（模拟 main.go 的装配）。
func newEgressTestService(t *testing.T) *AssistantService {
	t.Helper()
	db := newAssistantTestDB(t)
	s := NewAssistantService(db, testEncryptor(t))
	factory := func(allowPrivate []netip.Prefix) *httpclient.Guard {
		return httpclient.NewGuard(httpclient.GuardOptions{
			AllowInsecure: true, // 测试要打 httptest 的 http 地址
			AllowPrivate:  allowPrivate,
		})
	}
	s.SetEgressFactory(factory)
	s.SetEgress(factory(nil))
	s.SetAllowInsecure(true)
	if err := s.EnsureSetting(); err != nil {
		t.Fatalf("初始化设置失败: %v", err)
	}
	return s
}

func TestEgressRejectsInvalidCIDRWithItem(t *testing.T) {
	s := newEgressTestService(t)

	// 混入一项非法值：必须整体拒绝，并在消息里点名该项。
	err := s.SaveSettings(SaveSettingsParams{AllowPrivateCIDRs: []string{"10.0.0.0/8", "这不是CIDR"}, Version: 1})
	if !errors.Is(err, ErrAssistantInvalid) {
		t.Fatalf("应拒绝非法 CIDR，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "这不是CIDR") {
		t.Fatalf("错误信息必须指出是哪一项，实际 %v", err)
	}

	// 被拒绝时不得写库：放行段仍应为空。
	view, gerr := s.GetSettings()
	if gerr != nil {
		t.Fatalf("读取失败: %v", gerr)
	}
	if len(view.AllowPrivateCIDRs) != 0 {
		t.Fatalf("非法输入不应落库，实际 %v", view.AllowPrivateCIDRs)
	}
}

func TestEgressNormalizesAndDeduplicates(t *testing.T) {
	s := newEgressTestService(t)

	// 主机位应被掩掉、重复项应合并：否则 10.0.0.1/8 与 10.0.0.0/8 会被当成两条。
	if err := s.SaveSettings(SaveSettingsParams{AllowPrivateCIDRs: []string{" 10.0.0.1/8 ", "10.0.0.0/8", "192.168.1.0/24"}, Version: 1}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	view, err := s.GetSettings()
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	want := []string{"10.0.0.0/8", "192.168.1.0/24"}
	if len(view.AllowPrivateCIDRs) != len(want) {
		t.Fatalf("期望 %v，实际 %v", want, view.AllowPrivateCIDRs)
	}
	for i := range want {
		if view.AllowPrivateCIDRs[i] != want[i] {
			t.Fatalf("第 %d 项期望 %q 实际 %q", i, want[i], view.AllowPrivateCIDRs[i])
		}
	}
	// version 应自增，供下一次 CAS 使用。
	if view.Version != 2 {
		t.Fatalf("version 应自增到 2，实际 %d", view.Version)
	}
}

func TestEgressVersionConflict(t *testing.T) {
	s := newEgressTestService(t)
	if err := s.SaveSettings(SaveSettingsParams{AllowPrivateCIDRs: []string{"10.0.0.0/8"}, Version: 1}); err != nil {
		t.Fatalf("首次保存失败: %v", err)
	}
	// 用过期 version 再写：必须冲突，而不是覆盖别人的改动。
	if err := s.SaveSettings(SaveSettingsParams{AllowPrivateCIDRs: []string{"10.1.0.0/16"}, Version: 1}); !errors.Is(err, ErrAssistantConflict) {
		t.Fatalf("过期 version 应冲突，实际 %v", err)
	}
}

func TestEgressTakesEffectImmediately(t *testing.T) {
	// 核心用例：保存放行段后，**同一个 service 实例**必须立刻能连上原本被拦截的回环地址。
	// 若忘了重建 Guard，这里会仍然是 egress_denied——那正是「保存成功但行为不变」的形态。
	s := newEgressTestService(t)
	srv := sseServer(t, http.StatusOK, openAIChatSSE("pong"))
	addr := strings.TrimPrefix(srv.URL, "http://")
	host, _, ok := strings.Cut(addr, ":")
	if !ok {
		t.Fatalf("无法解析 httptest 地址: %s", srv.URL)
	}
	cidr := host + "/32"

	p, err := s.CreateProvider(SaveProviderParams{
		Name: "loopback", Protocol: model.AssistantProtocolOpenAIChat, BaseURL: srv.URL,
		Enabled: true, CredentialMode: model.AssistantCredentialNone,
	})
	if err != nil {
		t.Fatalf("建供应商失败（此时应仍允许保存地址，准入在拨号时生效）: %v", err)
	}
	m, err := s.CreateModel(SaveModelParams{
		ProviderID: p.ID, ModelID: "m", Enabled: true, Streaming: true,
	})
	if err != nil {
		t.Fatalf("建模型失败: %v", err)
	}

	// 放行前：应被准入拒绝。
	before, err := s.TestModel(context.Background(), m.ID, false)
	if err != nil {
		t.Fatalf("不应返回请求错误: %v", err)
	}
	if before.OK || before.ClassifiedError != string(ai.CodeEgressDenied) {
		t.Fatalf("放行前应被 egress_denied 拒绝，实际 %+v", before)
	}

	// 放行后（不重建 service，只保存设置）：应立即可达。
	if err := s.SaveSettings(SaveSettingsParams{AllowPrivateCIDRs: []string{cidr}, Version: 1}); err != nil {
		t.Fatalf("保存放行段失败: %v", err)
	}
	after, err := s.TestModel(context.Background(), m.ID, false)
	if err != nil {
		t.Fatalf("不应返回请求错误: %v", err)
	}
	if !after.OK {
		t.Fatalf("放行后应可达，实际 %+v（分类=%s 说明=%s）", after, after.ClassifiedError, after.Message)
	}
}

func TestEgressMetadataStillBlockedWhenWhitelisted(t *testing.T) {
	// metadata 地址段永远拒绝：即便管理员把 169.254.0.0/16 显式放行也不生效。
	// 这条不因「新增了管理员可改的放行入口」而松动。
	s := newEgressTestService(t)
	if err := s.SaveSettings(SaveSettingsParams{AllowPrivateCIDRs: []string{"169.254.0.0/16"}, Version: 1}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	p, err := s.CreateProvider(SaveProviderParams{
		Name: "metadata", Protocol: model.AssistantProtocolOpenAIChat,
		BaseURL: "http://169.254.169.254", Enabled: true,
		CredentialMode: model.AssistantCredentialNone,
	})
	if err != nil {
		t.Fatalf("建供应商失败: %v", err)
	}
	m, err := s.CreateModel(SaveModelParams{
		ProviderID: p.ID, ModelID: "m", Enabled: true, Streaming: true,
	})
	if err != nil {
		t.Fatalf("建模型失败: %v", err)
	}

	res, err := s.TestModel(context.Background(), m.ID, false)
	if err != nil {
		t.Fatalf("不应返回请求错误: %v", err)
	}
	if res.OK {
		t.Fatal("metadata 地址不应可达")
	}
	// 必须仍是准入拒绝（而不是「连不上」）：说明是在拨号前被挡住的。
	if res.ClassifiedError != string(ai.CodeEgressDenied) {
		t.Fatalf("应为 egress_denied，实际 %q（说明=%s）", res.ClassifiedError, res.Message)
	}
}

func TestApplyStoredEgressOnStartup(t *testing.T) {
	// 重启场景：新实例启动时 ApplyStoredEgress 应把库里的放行段装进 Guard。
	s := newEgressTestService(t)
	if err := s.SaveSettings(SaveSettingsParams{AllowPrivateCIDRs: []string{"127.0.0.1/32"}, Version: 1}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	srv := sseServer(t, http.StatusOK, openAIChatSSE("pong"))
	p, err := s.CreateProvider(SaveProviderParams{
		Name: "loopback2", Protocol: model.AssistantProtocolOpenAIChat, BaseURL: srv.URL,
		Enabled: true, CredentialMode: model.AssistantCredentialNone,
	})
	if err != nil {
		t.Fatalf("建供应商失败: %v", err)
	}
	m, err := s.CreateModel(SaveModelParams{
		ProviderID: p.ID, ModelID: "m", Enabled: true, Streaming: true,
	})
	if err != nil {
		t.Fatalf("建模型失败: %v", err)
	}

	// 模拟重启：同一 DB，新建 service，只调 ApplyStoredEgress（不手工设放行段）。
	restarted := NewAssistantService(s.db, testEncryptor(t))
	factory := func(allowPrivate []netip.Prefix) *httpclient.Guard {
		return httpclient.NewGuard(httpclient.GuardOptions{AllowInsecure: true, AllowPrivate: allowPrivate})
	}
	restarted.SetEgressFactory(factory)
	restarted.SetEgress(factory(nil))
	if err := restarted.ApplyStoredEgress(); err != nil {
		t.Fatalf("装配失败: %v", err)
	}

	res, err := restarted.TestModel(context.Background(), m.ID, false)
	if err != nil {
		t.Fatalf("不应返回请求错误: %v", err)
	}
	if !res.OK {
		t.Fatalf("重启后放行段应已生效，实际 %+v（说明=%s）", res, res.Message)
	}
}

func TestApplyStoredEgressFailsLoudOnCorruptRow(t *testing.T) {
	// 库里存着非法值（有人手改库）：必须报错，不能静默按空放行段运行——
	// 静默会让「昨天还能连的内网网关今天全断了」且现场无任何线索。
	s := newEgressTestService(t)
	if err := s.db.Model(&model.AssistantSetting{}).Where("id = ?", 1).
		Update("allow_private_cidrs", `"not-json-array"`).Error; err != nil {
		t.Fatalf("造脏数据失败: %v", err)
	}
	if err := s.ApplyStoredEgress(); err == nil {
		t.Fatal("脏数据应返回错误")
	}
}

// ── 供应商代理──
//
// 三条不变量：
//  1. 默认 direct（**不**回退环境代理）；
//  2. custom 的地址语法在保存期就挡住；
//  3. 代理地址与口令**永不回显**，只回 hasAuth 布尔。

func TestProviderProxy_DefaultsToDirect(t *testing.T) {
	db := newAssistantTestDB(t)
	svc := NewAssistantService(db, nil)
	svc.SetEgress(httpclient.NewGuard(httpclient.GuardOptions{})) // 保存期校验需要出站初始化
	view, err := svc.CreateProvider(SaveProviderParams{
		Name: "p", Protocol: model.AssistantProtocolOpenAIChat,
		BaseURL: "https://api.example.com", Enabled: true,
		CredentialMode: model.AssistantCredentialNone,
	})
	if err != nil {
		t.Fatalf("创建应成功: %v", err)
	}
	// 未指定时必须落到 direct——这是「不静默走环境代理」的落点。
	if view.ProxyMode != model.AssistantProxyDirect {
		t.Fatalf("默认应为 direct，实际 %q", view.ProxyMode)
	}
	if view.ProxyHasAuth {
		t.Fatal("默认不应有代理凭据")
	}
}

func TestProviderProxy_CustomValidation(t *testing.T) {
	db := newAssistantTestDB(t)
	svc := NewAssistantService(db, nil)
	svc.SetEgress(httpclient.NewGuard(httpclient.GuardOptions{}))
	base := SaveProviderParams{
		Name: "p", Protocol: model.AssistantProtocolOpenAIChat,
		BaseURL: "https://api.example.com", Enabled: true,
		CredentialMode: model.AssistantCredentialNone,
	}

	// custom 但地址为空 → 拒绝（不能保存一个必然拨号失败的配置）。
	bad := base
	bad.ProxyMode = model.AssistantProxyCustom
	if _, err := svc.CreateProvider(bad); err == nil {
		t.Fatal("custom 模式缺地址应被拒绝")
	}

	// 非法 scheme → 拒绝并点名（否则错误会在拨号阶段变成笼统的「连不上」）。
	bad2 := base
	bad2.ProxyMode = model.AssistantProxyCustom
	bad2.ProxyURL = "ftp://proxy:21"
	if _, err := svc.CreateProvider(bad2); err == nil {
		t.Fatal("不支持的代理协议应被拒绝")
	}

	// 合法 socks5 + userinfo：落库且 hasAuth=true，但地址不回显。
	ok := base
	ok.ProxyMode = model.AssistantProxyCustom
	ok.ProxyURL = "socks5://user:pass@proxy:1080"
	view, err := svc.CreateProvider(ok)
	if err != nil {
		t.Fatalf("合法代理应保存成功: %v", err)
	}
	if view.ProxyMode != model.AssistantProxyCustom {
		t.Fatalf("模式应为 custom，实际 %q", view.ProxyMode)
	}
	if !view.ProxyHasAuth {
		t.Fatal("带 userinfo 的代理应标记 hasAuth=true")
	}
	// 视图里**不得**出现地址：ProviderView 没有该字段，这里用序列化再确认一次。
	blob, _ := json.Marshal(view)
	if strings.Contains(string(blob), "pass") || strings.Contains(string(blob), "proxy:1080") {
		t.Fatalf("视图泄漏了代理地址或口令: %s", blob)
	}
}

func TestProviderProxy_SwitchBackToDirectClearsURL(t *testing.T) {
	db := newAssistantTestDB(t)
	svc := NewAssistantService(db, nil)
	svc.SetEgress(httpclient.NewGuard(httpclient.GuardOptions{}))
	view, err := svc.CreateProvider(SaveProviderParams{
		Name: "p", Protocol: model.AssistantProtocolOpenAIChat,
		BaseURL: "https://api.example.com", Enabled: true,
		CredentialMode: model.AssistantCredentialNone,
		ProxyMode:      model.AssistantProxyCustom, ProxyURL: "http://proxy:8080",
	})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	// 切回 direct：必须清空旧地址。
	// 否则将来误切回 custom 会静默启用一个早已废弃的代理。
	updated, err := svc.UpdateProvider(view.ID, SaveProviderParams{
		Name: "p", Protocol: model.AssistantProtocolOpenAIChat,
		BaseURL: "https://api.example.com", Enabled: true,
		CredentialMode: model.AssistantCredentialNone,
		ProxyMode:      model.AssistantProxyDirect, Version: view.Version,
	})
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if updated.ProxyMode != model.AssistantProxyDirect || updated.ProxyHasAuth {
		t.Fatalf("切回 direct 后应为 direct 且无凭据，实际 %q/%v", updated.ProxyMode, updated.ProxyHasAuth)
	}
	var row model.AssistantProvider
	if err := db.First(&row, view.ID).Error; err != nil {
		t.Fatalf("查回失败: %v", err)
	}
	if row.ProxyURL != "" {
		t.Fatalf("切回 direct 必须清空代理地址，实际仍为 %q", row.ProxyURL)
	}
}
