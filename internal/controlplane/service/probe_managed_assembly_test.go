package service

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestManagedProbeAssemblyPairing 验证「身份文件 + config」成对下发的不变量。
//
// 守三件事（都是真机踩过或推理出的硬约束）：
//  1. 身份文件里的 endpoint / instanceId 与插件桥地址、实例 UUID 一致；
//  2. credential 非空（探针必填项，空了整份身份会被当“字段不完整”而降级独立模式）；
//  3. 托管版 config **不含 bridge 段**（写了会让排查者误判模式，也会被探针的迁移逻辑当残留）。
func TestManagedProbeAssemblyPairing(t *testing.T) {
	const secret = "test-secret"
	const uuid = "inst-uuid-abc"
	const wsPort = 9102

	p := NewProvisionService(nil, nil, nil, nil, NewPluginBridgeService(secret))
	identity := p.buildManagedIdentity(uuid, wsPort)
	if identity == "" {
		t.Fatal("有 bridge 时应能生成身份文件")
	}

	var got ManagedAgentIdentity
	if err := json.Unmarshal([]byte(identity), &got); err != nil {
		t.Fatalf("身份文件不是合法 JSON: %v\n%s", err, identity)
	}
	if got.SchemaVersion != 1 {
		t.Fatalf("schemaVersion 必须为 1，实际 %d", got.SchemaVersion)
	}
	if got.InstanceID != uuid {
		t.Fatalf("instanceId 应为 %q，实际 %q", uuid, got.InstanceID)
	}
	wantEndpoint := pluginBridgeWSURL(wsPort)
	if got.Endpoint != wantEndpoint {
		t.Fatalf("endpoint 应为 %q，实际 %q", wantEndpoint, got.Endpoint)
	}
	if !strings.HasPrefix(got.Endpoint, "ws://") {
		// 探针只支持明文 ws，wss:// 在建连期会被 require 拒绝。这是硬约束，不是风格问题。
		t.Fatalf("endpoint 必须是 ws:// 开头（探针不支持 wss），实际 %q", got.Endpoint)
	}
	if got.Credential == "" {
		t.Fatal("credential 不能为空（探针必填，空了会降级独立模式）")
	}

	// 托管版 config 不得含 bridge 段。
	cfg := buildManagedProbeConfig(19999)
	if strings.Contains(cfg, "bridge:") {
		t.Fatalf("托管版 config 不应包含 bridge 段：\n%s", cfg)
	}
	if !strings.Contains(cfg, "metrics:") {
		t.Fatalf("托管版 config 仍应保留 metrics 基段（便于回退独立模式）：\n%s", cfg)
	}

	// 独立版 config 则**必须**含 bridge 段（否则探针不会反向连入）。
	indep := buildServerProbeConfig(19999,
		NewPluginBridgeService(secret).BuildBridgeConfigBlock(pluginBridgeWSURL(wsPort), uuid, "tok"))
	if !strings.Contains(indep, "bridge:") {
		t.Fatalf("独立版 config 必须包含 bridge 段：\n%s", indep)
	}
}

// TestBuildManagedIdentityWithoutBridge 没注入 bridge 时不下发身份（退回独立模式）。
func TestBuildManagedIdentityWithoutBridge(t *testing.T) {
	p := NewProvisionService(nil, nil, nil, nil, nil)
	if got := p.buildManagedIdentity("uuid", 9102); got != "" {
		t.Fatalf("无 bridge 时应返回空串（不下发身份），实际 %q", got)
	}
}

// TestManagedIdentityIsStableShape 两次签发的身份文件**结构**一致（只差时间与凭据）。
//
// 为什么要测：建服与在线更新是两条代码路径，若产出不同形的身份，
// 同一台机器在不同时机更新会出现两种行为。
func TestManagedIdentityIsStableShape(t *testing.T) {
	svc := NewPluginBridgeService("s3cr3t")
	p := NewProvisionService(nil, nil, nil, nil, svc)
	a := p.buildManagedIdentity("uuid-1", 9102)
	b := p.buildManagedIdentity("uuid-1", 9102)
	var ma, mb map[string]any
	if err := json.Unmarshal([]byte(a), &ma); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(b), &mb); err != nil {
		t.Fatal(err)
	}
	if len(ma) != len(mb) {
		t.Fatalf("字段数不一致: %d vs %d", len(ma), len(mb))
	}
	for k := range ma {
		if _, ok := mb[k]; !ok {
			t.Fatalf("第二次签发缺少字段 %q", k)
		}
	}
}
