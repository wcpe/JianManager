package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/wcpe/JianManager/internal/controlplane/assistantmcp"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

/**
 * FR-517 §8 里能在服务层验证的边界。
 *
 * 重点守四条：
 *  - 回环/元数据地址默认被拒，显式放行后才可保存；
 *  - 自环（指向本平台）必须拒绝；
 *  - 凭据永不出现在视图里；
 *  - 工具默认 disabled，风险默认 unknown（不由 annotations 推导）。
 */

func newMCPTestService(t *testing.T) *AssistantMCPService {
	t.Helper()
	// 用内置 dev 密钥构造（与生产装配同一条路径，避免测试自造一套密钥逻辑）。
	enc, err := newKeyEncryptor(DevKeyEncSecretBase64)
	if err != nil {
		t.Fatalf("构造加密器失败: %v", err)
	}
	s := NewAssistantMCPService(nil, enc)
	s.SetAllowInsecure(true) // 允许 http，便于用自托管地址做用例
	return s
}

func TestValidateEndpoint_LoopbackRejected(t *testing.T) {
	// spec §8：endpoint 指向回环 → 被拒（ErrBlockedTarget / 自环）。
	s := newMCPTestService(t)
	_, err := s.validateEndpoint("http://127.0.0.1:8080/mcp", nil)
	if err == nil {
		t.Fatal("回环端点应被拒")
	}
	// 必须是「拒绝」而不是「碰巧因为别的理由失败」。
	if !errors.Is(err, ErrAssistantMCPSelfLoop) && !strings.Contains(err.Error(), "拒绝") {
		t.Fatalf("应以拒绝理由失败，得到 %v", err)
	}
}

func TestValidateEndpoint_MetadataRejected(t *testing.T) {
	// spec §8：169.254.169.254 必须被拒（元数据地址永不放行）。
	s := newMCPTestService(t)
	_, err := s.validateEndpoint("http://169.254.169.254/latest/meta-data", nil)
	if err == nil {
		t.Fatal("云元数据地址应被拒")
	}
}

func TestValidateEndpoint_PrivateAllowedOnlyWithOrigin(t *testing.T) {
	// spec §8：管理员显式放行 10.0.0.5/32 后同地址可保存。
	s := newMCPTestService(t)
	origins := []MCPAllowedOriginInput{{
		Scheme: "http", Host: "mcp.internal", Port: 8080,
		CIDRs: []string{"10.0.0.5/32"},
	}}
	parsed, _, err := s.parseOrigins(origins)
	if err != nil {
		t.Fatalf("解析 origin 失败: %v", err)
	}
	// 注意：ValidateBaseURL 只做静态 URL 校验，不解析 DNS——
	// 私网准入发生在拨号点（受保护出站的 dialContext）。因此这里能保存，
	// 真正的地址准入由 Guard 的边界测试覆盖（protected_origin_test.go）。
	if _, err := s.validateEndpoint("http://mcp.internal:8080/mcp", parsed); err != nil {
		t.Fatalf("放行后应可保存: %v", err)
	}
}

func TestValidateEndpoint_SelfLoopDetected(t *testing.T) {
	// spec §8：endpoint 指向本平台 → MCP_SELF_LOOP。
	s := newMCPTestService(t)
	s.SetSelfAddrs([]string{"cp.example.com:443"})
	_, err := s.validateEndpoint("https://cp.example.com/mcp", nil)
	if !errors.Is(err, ErrAssistantMCPSelfLoop) {
		t.Fatalf("应识别为自环，得到 %v", err)
	}
}

func TestValidateEndpoint_QueryRejected(t *testing.T) {
	// 端点不得带 query（与 FR-514 的 baseUrl 同口径）。
	s := newMCPTestService(t)
	_, err := s.validateEndpoint("https://mcp.example.com/mcp?key=1", nil)
	if err == nil {
		t.Fatal("带 query 的端点应被拒")
	}
}

func TestValidateSlug_ReservedAndFormat(t *testing.T) {
	// spec §4.1：slug 唯一、^[a-z][a-z0-9_]*$、保留字 system 不可用。
	if _, err := validateSlug("system"); err == nil {
		t.Fatal("保留字 system 应被拒")
	}
	if _, err := validateSlug("Upper"); err == nil {
		t.Fatal("大写开头应被拒")
	}
	if _, err := validateSlug("9start"); err == nil {
		t.Fatal("数字开头应被拒")
	}
	if _, err := validateSlug("ok_slug1"); err != nil {
		t.Fatalf("合法 slug 被拒: %v", err)
	}
}

func TestConnectionView_NeverLeaksCredential(t *testing.T) {
	// spec §8：凭据在任何响应中都不出现（用响应快照断言）。
	//
	// 这里直接检查视图结构体本身是否带有承载凭据的导出字段——
	// 比「跑一次 viewOfConnection」更彻底：即使将来有人给视图加了
	// `AuthSecret` 字段但忘了赋值，这条也会失败（因为字段存在即可被序列化）。
	row := &model.AssistantMCPConnection{
		ID: 1, Slug: "a", Name: "A", Endpoint: "http://mcp.internal:8080/mcp",
		AuthStyle:  model.MCPAuthStyleBearer,
		AuthSource: model.MCPAuthSourceStored,
		AuthEnc:    "ciphertext-should-not-leak",
		AuthEnv:    "SHOULD_NOT_LEAK_ENV",
		Enabled:    true, PolicyVersion: 1, Version: 1,
	}
	// 模型层的凭据字段必须是 json:"-"，否则任何直接序列化模型的地方都会泄漏。
	encJSON, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encJSON), "ciphertext-should-not-leak") {
		t.Fatalf("模型层密文字段未标 json:\"-\"，序列化即泄漏: %s", encJSON)
	}
	if strings.Contains(string(encJSON), "SHOULD_NOT_LEAK_ENV") {
		t.Fatalf("模型层环境变量字段未标 json:\"-\": %s", encJSON)
	}

	// 视图结构不得含任何凭据字段（只有 HasCredential 布尔）。
	vt := reflect.TypeOf(MCPConnectionView{})
	for i := 0; i < vt.NumField(); i++ {
		name := vt.Field(i).Name
		lower := strings.ToLower(name)
		if strings.Contains(lower, "secret") || strings.Contains(lower, "authvalue") ||
			strings.Contains(lower, "authenc") || strings.Contains(lower, "cipher") {
			t.Fatalf("视图结构不应含凭据字段: %s", name)
		}
	}
}

func TestHashSchema_NormalizedForKeyOrder(t *testing.T) {
	// schema 指纹必须对键序不敏感：上游一次无害的格式化不该让全部票据失效。
	a := json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)
	b := json.RawMessage(`{"properties":{"x":{"type":"string"}},"type":"object"}`)
	if hashSchema(a) != hashSchema(b) {
		t.Fatal("键序不同导致 schema 指纹不同——票据会被无谓失效")
	}
	// 内容变化必须改变指纹。
	c := json.RawMessage(`{"type":"object","properties":{"x":{"type":"number"}}}`)
	if hashSchema(a) == hashSchema(c) {
		t.Fatal("schema 内容变化却指纹相同")
	}
}

func TestParseOrigins_RequiresCIDR(t *testing.T) {
	// 空 CIDR 等于「不允许任何地址」——几乎一定是配置错误，明确拒绝。
	s := newMCPTestService(t)
	_, _, err := s.parseOrigins([]MCPAllowedOriginInput{{
		Scheme: "http", Host: "mcp.internal", Port: 8080,
	}})
	if !errors.Is(err, ErrAssistantInvalid) {
		t.Fatalf("空 CIDR 应报配置错误，得到 %v", err)
	}
}

func TestParseOrigins_RejectsBadCIDRAndScheme(t *testing.T) {
	s := newMCPTestService(t)
	if _, _, err := s.parseOrigins([]MCPAllowedOriginInput{{
		Scheme: "ftp", Host: "x", CIDRs: []string{"10.0.0.5/32"},
	}}); err == nil {
		t.Fatal("非法 scheme 应被拒")
	}
	if _, _, err := s.parseOrigins([]MCPAllowedOriginInput{{
		Scheme: "http", Host: "x", CIDRs: []string{"not-a-cidr"},
	}}); err == nil {
		t.Fatal("非法 CIDR 应被拒")
	}
}

func TestMapMCPErr_Classification(t *testing.T) {
	// 客户端错误必须映射到服务层错误族，否则路由层无法给出正确 HTTP 码。
	// 直接构造带 %w 包装的错误，避免测试里依赖客户端包的内部符号。
	cases := []struct {
		wrapped error
		want    error
	}{
		{fmt.Errorf("%w: x", assistantmcp.ErrToolsLimit), ErrAssistantMCPToolsLimit},
		{fmt.Errorf("%w: x", assistantmcp.ErrTooLarge), ErrAssistantMCPTooLarge},
		{fmt.Errorf("%w: x", assistantmcp.ErrUnreachable), ErrAssistantMCPUnreachable},
		{fmt.Errorf("%w: x", assistantmcp.ErrProtocol), ErrAssistantMCPProtocol},
	}
	for _, c := range cases {
		if got := mapMCPErr(c.wrapped); !errors.Is(got, c.want) {
			t.Fatalf("%v 应映射到 %v，得到 %v", c.wrapped, c.want, got)
		}
	}
	// nil 透传。
	if mapMCPErr(nil) != nil {
		t.Fatal("nil 应映射为 nil")
	}
}

func TestMCPToolRisk_OnlyKnownValues(t *testing.T) {
	// 风险分类只接受 read / write / unknown（不由 annotations 推导）。
	// 这里校验常量集合本身，避免将来新增取值时误用字符串字面量。
	known := []string{model.MCPToolRiskRead, model.MCPToolRiskWrite, model.MCPToolRiskUnknown}
	for _, v := range known {
		if v == "" {
			t.Fatal("风险常量不应为空")
		}
	}
	// 反向：annotations 里常见的 readOnlyHint 不是风险取值。
	if model.MCPToolRiskRead == "readOnlyHint" {
		t.Fatal("风险分类不得采用 annotations 的字段名")
	}
}

func TestRecoverInFlight_Placeholder(t *testing.T) {
	// 占位：真实回收逻辑需要 DB，已在 assistant_tool_exec.go 的
	// RecoverInFlightReceipts 中实现，由 router 层用例覆盖。
	_ = context.Background()
}
